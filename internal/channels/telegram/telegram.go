// Package telegram implements the Telegram Bot API channel adapter.
//
// Telegram uses HTTP long-polling for simplicity and reliability on unstable connections.
// No webhooks (would require port forwarding or ngrok on Pi).
package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/channels"
)

const adapterName = "telegram"

// API endpoints
const (
	telegramAPIBase = "https://api.telegram.org"
	defaultTimeout  = 30 // seconds for long-polling
)

// Telegram API response types
type getUpdatesResponse struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
	Error  string   `json:"description"`
}

type Update struct {
	UpdateID int64   `json:"update_id"`
	Message  Message `json:"message,omitempty"`
}

type Message struct {
	MessageID int64       `json:"message_id"`
	Chat      Chat        `json:"chat"`
	Text      string      `json:"text,omitempty"`
	From      User        `json:"from"`
	Voice     *Voice      `json:"voice,omitempty"`
	Audio     *Audio      `json:"audio,omitempty"`
	VideoNote *VideoNote  `json:"video_note,omitempty"`
	Photo     []PhotoSize `json:"photo,omitempty"`
	Caption   string      `json:"caption,omitempty"`
}

type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int    `json:"file_size,omitempty"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type Audio struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type VideoNote struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	Length   int    `json:"length"`
	FileSize int    `json:"file_size,omitempty"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type User struct {
	ID int64 `json:"id"`
}

type sendMessageResponse struct {
	OK     bool   `json:"ok"`
	Result Result `json:"result,omitempty"`
	Error  string `json:"description,omitempty"`
}

type Result struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

// rateLimiter tracks message sends per chat to enforce Telegram's 30 msg/sec limit
type rateLimiter struct {
	mu        sync.Mutex
	chatLimts map[int64]*limiter // per-chat rate limit state
}

type limiter struct {
	lastSendTime time.Time
	messageCount int
	windowStart  time.Time
}

// Adapter implements channels.ChannelAdapter for Telegram Bot API.
type Adapter struct {
	botToken       string
	logger         *slog.Logger
	eventBus       bus.Bus
	httpClient     *http.Client
	rateLimiter    *rateLimiter
	stopChan       chan struct{}
	pollWg         sync.WaitGroup
	sendMu         sync.Mutex // protects sends during shutdown
	shutdownOnce   sync.Once
	isShutdown     bool
	lastUpdateID   int64
	operatorChatID int64          // First user to interact becomes operator
	operatorMu     sync.Mutex     // protects operatorChatID
	sttEngine      STTTranscriber // Optional: for voice message transcription
	ttsEngine      TTSSynthesizer // Optional: for text-to-speech responses
	typingMu       sync.Mutex
	typingCancel   map[int64]context.CancelFunc // chatID -> cancel typing ticker
}

// STTTranscriber is the interface for speech-to-text engines.
type STTTranscriber interface {
	STTEnabled() bool
	Transcribe(ctx context.Context, audioData []byte, format string) (string, error)
}

// TTSSynthesizer is the interface for text-to-speech engines.
type TTSSynthesizer interface {
	Enabled() bool
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// New creates a new Telegram adapter.
func New(botToken string, logger *slog.Logger) *Adapter {
	return &Adapter{
		botToken:     botToken,
		logger:       logger,
		httpClient:   &http.Client{Timeout: time.Second * 35}, // 30s poll + 5s buffer
		rateLimiter:  &rateLimiter{chatLimts: make(map[int64]*limiter)},
		stopChan:     make(chan struct{}),
		typingCancel: make(map[int64]context.CancelFunc),
	}
}

// Name returns "telegram".
func (a *Adapter) Name() string { return adapterName }

// SetSTT sets the speech-to-text engine for voice message transcription.
func (a *Adapter) SetSTT(stt STTTranscriber) {
	a.sttEngine = stt
}

// SetTTS sets the text-to-speech engine for voice responses.
// When set, outbound messages are synthesized and sent as voice notes.
func (a *Adapter) SetTTS(tts TTSSynthesizer) {
	a.ttsEngine = tts
}

// Start begins polling for Telegram updates and publishing them to the bus.
func (a *Adapter) Start(ctx context.Context, b bus.Bus) error {
	a.eventBus = b
	a.logger.Info("Telegram adapter starting", "bot_token", maskToken(a.botToken))

	// Start the long-polling loop
	a.pollWg.Add(1)
	go a.pollLoop(ctx)

	return nil
}

// Stop gracefully shuts down the Telegram adapter.
func (a *Adapter) Stop() error {
	var err error
	a.shutdownOnce.Do(func() {
		a.sendMu.Lock()
		defer a.sendMu.Unlock()

		a.isShutdown = true
		close(a.stopChan)

		// Wait for polling to finish with timeout
		done := make(chan struct{})
		go func() {
			a.pollWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			a.logger.Info("Telegram adapter stopped gracefully")
		case <-time.After(10 * time.Second):
			a.logger.Warn("Telegram adapter shutdown timeout")
			err = fmt.Errorf("telegram.Stop: shutdown timeout")
		}
	})
	return err
}

// Send delivers a message via the Telegram Bot API with rate limiting and retries.
func (a *Adapter) Send(ctx context.Context, msg channels.OutboundMessage) error {
	a.sendMu.Lock()
	if a.isShutdown {
		a.sendMu.Unlock()
		return fmt.Errorf("telegram.Send: adapter is shutdown")
	}
	a.sendMu.Unlock()

	// Parse chat_id from the "to" field (should be numeric string)
	chatID, err := strconv.ParseInt(msg.To, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram.Send: invalid chat_id '%s': %w", msg.To, err)
	}

	// Stop the typing indicator — the response is about to land
	a.stopTypingLoop(chatID)

	// Attempt TTS voice response if engine is available and text is short enough.
	// Long responses (email summaries, lists, etc.) stay as text — voice UX breaks
	// down past ~500 chars and synthesis is slow on low-end hardware.
	const maxTTSChars = 500
	if a.ttsEngine != nil && a.ttsEngine.Enabled() && len(msg.Text) <= maxTTSChars {
		a.logger.Info("synthesizing voice response", "chars", len(msg.Text))
		if err := a.sendTTSVoice(ctx, chatID, msg.Text); err != nil {
			a.logger.Warn("TTS voice send failed, falling back to text", "error", err)
			// Fall through to text below.
		} else {
			return nil
		}
	} else if a.ttsEngine != nil && a.ttsEngine.Enabled() && len(msg.Text) > maxTTSChars {
		a.logger.Info("response too long for voice, sending as text", "chars", len(msg.Text), "limit", maxTTSChars)
	}

	// Apply rate limiting
	if !a.checkRateLimit(chatID) {
		// Wait for the next available slot
		time.Sleep(time.Millisecond * 50)
		if !a.checkRateLimit(chatID) {
			return fmt.Errorf("telegram.Send: rate limit exceeded for chat %d", chatID)
		}
	}

	// Send message with retry logic for 429 Too Many Requests
	return a.sendMessageWithRetry(ctx, chatID, msg.Text)
}

// SendToOperator sends a message to the operator (first user who interacted).
// Used by heartbeat service for proactive notifications.
func (a *Adapter) SendToOperator(ctx context.Context, message string) error {
	a.operatorMu.Lock()
	chatID := a.operatorChatID
	a.operatorMu.Unlock()

	if chatID == 0 {
		a.logger.Debug("skipping notification, no operator chat ID set yet")
		return nil
	}

	return a.Send(ctx, channels.OutboundMessage{
		To:   strconv.FormatInt(chatID, 10),
		Text: message,
	})
}

// GetOperatorChatID returns the operator's chat ID (or 0 if not set).
func (a *Adapter) GetOperatorChatID() int64 {
	a.operatorMu.Lock()
	defer a.operatorMu.Unlock()
	return a.operatorChatID
}

// checkRateLimit checks if we can send a message for the given chat.
// Returns true if we can send, false if rate limit exceeded.
func (a *Adapter) checkRateLimit(chatID int64) bool {
	a.rateLimiter.mu.Lock()
	defer a.rateLimiter.mu.Unlock()

	lim, exists := a.rateLimiter.chatLimts[chatID]
	if !exists {
		lim = &limiter{
			windowStart: time.Now(),
		}
		a.rateLimiter.chatLimts[chatID] = lim
	}

	now := time.Now()

	// Reset window if 1 second has passed
	if now.Sub(lim.windowStart) >= time.Second {
		lim.windowStart = now
		lim.messageCount = 0
	}

	// Check if we can send (Telegram limit: 30 msg/sec per chat)
	if lim.messageCount < 30 {
		lim.messageCount++
		lim.lastSendTime = now
		return true
	}

	return false
}

// sendMessageWithRetry sends a message with retry on 429 status.
func (a *Adapter) sendMessageWithRetry(ctx context.Context, chatID int64, text string) error {
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := a.sendMessage(ctx, chatID, text)
		if err == nil {
			return nil
		}

		// Check if this is a rate limit error
		if isRateLimitError(err) {
			retryAfter := extractRetryAfter(err)
			if retryAfter > 0 {
				select {
				case <-time.After(time.Duration(retryAfter) * time.Second):
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			// If no Retry-After header, back off exponentially
			backoffSecs := 1 << uint(attempt)
			select {
			case <-time.After(time.Duration(backoffSecs) * time.Second):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// Non-retryable error
		return err
	}

	return fmt.Errorf("telegram.Send: max retries exceeded for chat %d", chatID)
}

// sendChatAction sends a chat action (e.g., "typing") to show the bot is working.
func (a *Adapter) sendChatAction(ctx context.Context, chatID int64, action string) error {
	endpoint := fmt.Sprintf("%s/bot%s/sendChatAction", telegramAPIBase, a.botToken)

	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("action", action)

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram.sendChatAction: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram.sendChatAction: http request: %w", err)
	}
	defer resp.Body.Close()

	// We don't really care about the response for typing indicators
	return nil
}

// startTypingLoop sends the "typing" (or "record_voice" if TTS is active) indicator
// every 4 seconds until stopped. Telegram's typing indicator expires after ~5 seconds,
// so we resend before it fades.
func (a *Adapter) startTypingLoop(ctx context.Context, chatID int64) {
	action := "typing"
	if a.ttsEngine != nil && a.ttsEngine.Enabled() {
		action = "record_voice"
	}

	a.typingMu.Lock()
	if cancel, ok := a.typingCancel[chatID]; ok {
		cancel()
	}
	loopCtx, cancel := context.WithCancel(ctx)
	a.typingCancel[chatID] = cancel
	a.typingMu.Unlock()

	// Send the first one immediately
	_ = a.sendChatAction(loopCtx, chatID, action)

	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if err := a.sendChatAction(loopCtx, chatID, action); err != nil {
					return
				}
			}
		}
	}()
}

// stopTypingLoop cancels the typing indicator loop for a chat.
func (a *Adapter) stopTypingLoop(chatID int64) {
	a.typingMu.Lock()
	if cancel, ok := a.typingCancel[chatID]; ok {
		cancel()
		delete(a.typingCancel, chatID)
	}
	a.typingMu.Unlock()
}

// sendMessage sends a message via Telegram Bot API.
func (a *Adapter) sendMessage(ctx context.Context, chatID int64, text string) error {
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, a.botToken)

	// Build request body with URL encoding
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", text)
	form.Set("parse_mode", "Markdown")

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram.sendMessage: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram.sendMessage: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("telegram.sendMessage: read response: %w", err)
	}

	var apiResp sendMessageResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("telegram.sendMessage: parse response: %w", err)
	}

	if !apiResp.OK {
		// If it's a 429, include that context in the error
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := resp.Header.Get("Retry-After")
			return &rateLimitError{
				statusCode:  resp.StatusCode,
				retryAfter:  retryAfter,
				description: apiResp.Error,
			}
		}
		return fmt.Errorf("telegram.sendMessage: api error: %s", apiResp.Error)
	}

	a.logger.Debug("Message sent via Telegram", "chat_id", chatID, "message_id", apiResp.Result.MessageID)
	return nil
}

// rateLimitError wraps 429 errors with Retry-After info
type rateLimitError struct {
	statusCode  int
	retryAfter  string
	description string
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("rate limit (429): %s, retry-after: %s", e.description, e.retryAfter)
}

// isRateLimitError checks if an error is a rate limit error
func isRateLimitError(err error) bool {
	_, ok := err.(*rateLimitError)
	return ok
}

// extractRetryAfter extracts seconds from a rate limit error's Retry-After header
func extractRetryAfter(err error) int {
	rle, ok := err.(*rateLimitError)
	if !ok {
		return 0
	}
	secs, _ := strconv.Atoi(rle.retryAfter)
	return secs
}

// pollLoop continuously polls Telegram for updates.
func (a *Adapter) pollLoop(ctx context.Context) {
	defer a.pollWg.Done()

	for {
		select {
		case <-a.stopChan:
			a.logger.Debug("Telegram poll loop stopping")
			return
		case <-ctx.Done():
			a.logger.Debug("Telegram poll loop context cancelled")
			return
		default:
		}

		// Call getUpdates
		updates, err := a.getUpdates(ctx)
		if err != nil {
			a.logger.Error("Telegram getUpdates failed", "error", err)
			// Backoff on error to avoid spamming
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-a.stopChan:
				return
			case <-ctx.Done():
				return
			}
		}

		// Process updates
		for _, update := range updates {
			a.handleUpdate(ctx, update)
		}
	}
}

// getUpdates polls Telegram for new messages using long-polling.
func (a *Adapter) getUpdates(ctx context.Context) ([]Update, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates", telegramAPIBase, a.botToken)

	// Build query string
	form := url.Values{}
	form.Set("timeout", strconv.Itoa(defaultTimeout))
	if a.lastUpdateID > 0 {
		form.Set("offset", strconv.FormatInt(a.lastUpdateID+1, 10))
	}

	// Create request with context that allows for long polling
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?"+form.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("telegram.getUpdates: create request: %w", err)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram.getUpdates: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("telegram.getUpdates: read response: %w", err)
	}

	var apiResp getUpdatesResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("telegram.getUpdates: parse response: %w", err)
	}

	if !apiResp.OK {
		return nil, fmt.Errorf("telegram.getUpdates: api error: %s", apiResp.Error)
	}

	// Update lastUpdateID to avoid reprocessing
	for _, update := range apiResp.Result {
		if update.UpdateID > a.lastUpdateID {
			a.lastUpdateID = update.UpdateID
		}
	}

	return apiResp.Result, nil
}

// handleUpdate processes a single Telegram update.
func (a *Adapter) handleUpdate(ctx context.Context, update Update) {
	chatID := update.Message.Chat.ID
	sessionID := fmt.Sprintf("telegram:%d", chatID)
	var text string

	var images []bus.ImageAttachment

	// Handle voice messages — transcribe async so the poll loop stays unblocked.
	// If STT isn't configured, route through the LLM so it can guide the user to set it up.
	if update.Message.Voice != nil {
		if a.sttEngine == nil || !a.sttEngine.STTEnabled() {
			a.publishInbound(ctx, chatID, sessionID, "[The user sent a voice message but voice transcription isn't set up yet. Use the stt_connect tool to help them enable it.]", nil)
			return
		}
		go a.transcribeAndPublish(ctx, chatID, sessionID, update.Message.Voice, "voice")
		return
	} else if update.Message.Audio != nil {
		if a.sttEngine == nil || !a.sttEngine.STTEnabled() {
			a.publishInbound(ctx, chatID, sessionID, "[The user sent an audio file but voice transcription isn't set up yet. Use the stt_connect tool to help them enable it.]", nil)
			return
		}
		v := &Voice{
			FileID:   update.Message.Audio.FileID,
			Duration: update.Message.Audio.Duration,
			MimeType: update.Message.Audio.MimeType,
		}
		go a.transcribeAndPublish(ctx, chatID, sessionID, v, "audio")
		return
	} else if update.Message.VideoNote != nil {
		if a.sttEngine == nil || !a.sttEngine.STTEnabled() {
			a.publishInbound(ctx, chatID, sessionID, "[The user sent a video note but voice transcription isn't set up yet. Use the stt_connect tool to help them enable it.]", nil)
			return
		}
		v := &Voice{
			FileID:   update.Message.VideoNote.FileID,
			Duration: update.Message.VideoNote.Duration,
			MimeType: "video/mp4",
		}
		go a.transcribeAndPublish(ctx, chatID, sessionID, v, "video")
		return
	} else if len(update.Message.Photo) > 0 {
		// Photos: pick the largest size (last in Telegram's array).
		largest := update.Message.Photo[len(update.Message.Photo)-1]
		photoData, err := a.downloadFile(ctx, largest.FileID)
		if err != nil {
			a.logger.Warn("photo download failed", "error", err)
			a.sendMessageWithRetry(ctx, chatID, "Sorry, I couldn't download that photo. Try again?")
			return
		}

		images = append(images, bus.ImageAttachment{
			Data:      base64.StdEncoding.EncodeToString(photoData),
			MediaType: "image/jpeg", // Telegram always serves photos as JPEG
		})

		text = strings.TrimSpace(update.Message.Caption)
		if text == "" {
			text = "What's in this image?"
		}

		a.logger.Info("photo received", "chat_id", chatID, "size_bytes", len(photoData))
	} else if update.Message.Text != "" {
		text = strings.TrimSpace(update.Message.Text)
	} else {
		// Ignore updates without text, voice, or photo
		return
	}

	a.publishInbound(ctx, chatID, sessionID, text, images)
}

// publishInbound handles the common post-processing for all inbound messages:
// operator tracking, /start rewrite, typing indicator, and bus publish.
func (a *Adapter) publishInbound(ctx context.Context, chatID int64, sessionID, text string, images []bus.ImageAttachment) {
	a.operatorMu.Lock()
	if a.operatorChatID == 0 {
		a.operatorChatID = chatID
		a.logger.Info("operator chat ID set", "chat_id", chatID)
	}
	a.operatorMu.Unlock()

	if strings.HasPrefix(text, "/start") {
		text = "Hi!"
	}

	a.startTypingLoop(ctx, chatID)

	inboundMsg := bus.InboundMessage{
		From:   strconv.FormatInt(chatID, 10),
		Text:   text,
		Images: images,
	}
	event := bus.Event{
		Type:      bus.TypeMessageInbound,
		Channel:   adapterName,
		SessionID: sessionID,
		Payload:   bus.MustJSON(inboundMsg),
	}
	if _, err := a.eventBus.Publish(ctx, event); err != nil {
		a.stopTypingLoop(chatID)
		a.logger.Error("Failed to publish inbound message", "error", err, "chat_id", chatID)
	} else {
		a.logger.Debug("Published inbound message", "chat_id", chatID, "session_id", sessionID)
	}
}

// transcribeAndPublish downloads and transcribes a voice-like message in a goroutine,
// showing a typing indicator throughout and publishing the result when done.
func (a *Adapter) transcribeAndPublish(ctx context.Context, chatID int64, sessionID string, voice *Voice, mediaType string) {
	a.startTypingLoop(ctx, chatID)

	transcript, err := a.transcribeVoice(ctx, voice)
	if err != nil {
		a.stopTypingLoop(chatID)
		a.logger.Warn(mediaType+" transcription failed", "error", err)
		a.sendMessageWithRetry(ctx, chatID, "Sorry, I couldn't understand that "+mediaType+" message. Try again?")
		return
	}

	text := strings.TrimSpace(transcript)
	if text == "" {
		a.stopTypingLoop(chatID)
		a.sendMessageWithRetry(ctx, chatID, "I couldn't hear anything in that "+mediaType+" message.")
		return
	}

	a.logger.Info(mediaType+" transcribed", "chat_id", chatID, "text", text)
	a.publishInbound(ctx, chatID, sessionID, text, nil)
}

// downloadFile downloads a file from Telegram by file_id.
// Returns the raw bytes, capped at 10MB to protect Pi memory.
func (a *Adapter) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	fileURL := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", telegramAPIBase, a.botToken, fileID)
	req, err := http.NewRequestWithContext(ctx, "GET", fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create getFile request: %w", err)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get file info: %w", err)
	}
	defer resp.Body.Close()

	var fileResp struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fileResp); err != nil {
		return nil, fmt.Errorf("parse file info: %w", err)
	}
	if !fileResp.OK || fileResp.Result.FilePath == "" {
		return nil, fmt.Errorf("no file path returned")
	}

	downloadURL := fmt.Sprintf("%s/file/bot%s/%s", telegramAPIBase, a.botToken, fileResp.Result.FilePath)
	dlReq, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	dlResp, err := a.httpClient.Do(dlReq)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	defer dlResp.Body.Close()

	const maxFileSize = 10 << 20 // 10MB
	data, err := io.ReadAll(io.LimitReader(dlResp.Body, maxFileSize))
	if err != nil {
		return nil, fmt.Errorf("read file data: %w", err)
	}

	return data, nil
}

// transcribeVoice downloads a voice message and transcribes it.
func (a *Adapter) transcribeVoice(ctx context.Context, voice *Voice) (string, error) {
	audioData, err := a.downloadFile(ctx, voice.FileID)
	if err != nil {
		return "", fmt.Errorf("download voice: %w", err)
	}

	// Telegram voice messages are OGG/Opus format
	format := "ogg"
	if voice.MimeType != "" {
		switch voice.MimeType {
		case "audio/mpeg", "audio/mp3":
			format = "mp3"
		case "audio/wav", "audio/x-wav":
			format = "wav"
		}
	}

	return a.sttEngine.Transcribe(ctx, audioData, format)
}

// sendTTSVoice synthesizes text to speech and sends it as a Telegram voice note.
// Returns an error if synthesis or upload fails; caller should fall back to text.
// ttsSynthesisTimeout caps how long we wait for piper to synthesize speech.
// On slow hardware (Pi 2 armv7) synthesis can take 10-30s for short text.
// If it exceeds this, we fall back to text so the user isn't left waiting.
const ttsSynthesisTimeout = 45 * time.Second

func (a *Adapter) sendTTSVoice(ctx context.Context, chatID int64, text string) error {
	synthCtx, cancel := context.WithTimeout(ctx, ttsSynthesisTimeout)
	defer cancel()

	start := time.Now()
	wavData, err := a.ttsEngine.Synthesize(synthCtx, text)
	if err != nil {
		if synthCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("synthesis timeout after %s (text too long for this hardware)", time.Since(start).Round(time.Second))
		}
		return fmt.Errorf("synthesize: %w", err)
	}
	a.logger.Info("piper synthesis complete", "elapsed", time.Since(start).Round(time.Millisecond), "chars", len(text))

	oggData, err := convertWAVToOGG(ctx, wavData)
	if err != nil {
		return fmt.Errorf("wav→ogg: %w", err)
	}

	return a.sendVoice(ctx, chatID, oggData)
}

// sendVoice uploads OGG/Opus audio to Telegram as a voice note.
func (a *Adapter) sendVoice(ctx context.Context, chatID int64, oggData []byte) error {
	endpoint := fmt.Sprintf("%s/bot%s/sendVoice", telegramAPIBase, a.botToken)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	if err := w.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return fmt.Errorf("write chat_id field: %w", err)
	}

	part, err := w.CreateFormFile("voice", "voice.ogg")
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(oggData); err != nil {
		return fmt.Errorf("write voice data: %w", err)
	}
	w.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, &body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	var apiResp sendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if !apiResp.OK {
		return fmt.Errorf("api error: %s", apiResp.Error)
	}

	a.logger.Info("voice note sent via Telegram", "chat_id", chatID, "ogg_bytes", len(oggData))
	return nil
}

// convertWAVToOGG converts WAV audio to OGG/Opus format using ffmpeg.
// Telegram's sendVoice endpoint requires OGG encoded with the Opus codec.
func convertWAVToOGG(ctx context.Context, wavData []byte) ([]byte, error) {
	wavFile, err := os.CreateTemp("", "crayfish-tts-*.wav")
	if err != nil {
		return nil, fmt.Errorf("create temp wav: %w", err)
	}
	wavPath := wavFile.Name()
	defer os.Remove(wavPath)

	if _, err := wavFile.Write(wavData); err != nil {
		wavFile.Close()
		return nil, fmt.Errorf("write wav: %w", err)
	}
	wavFile.Close()

	oggPath := wavPath + ".ogg"
	defer os.Remove(oggPath)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", wavPath,
		"-c:a", "libopus",
		"-b:a", "32k",
		"-y",
		oggPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w (stderr: %s)", err, stderr.String())
	}

	return os.ReadFile(oggPath)
}

// maskToken returns a redacted version of the bot token for logging.
func maskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "****" + token[len(token)-4:]
}
