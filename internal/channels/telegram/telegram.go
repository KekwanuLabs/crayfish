// Package telegram implements the Telegram Bot API channel adapter.
//
// Telegram uses HTTP long-polling for simplicity and reliability on unstable connections.
// No webhooks (would require port forwarding or ngrok on Pi).
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	OK     bool      `json:"ok"`
	Result []Update  `json:"result"`
	Error  string    `json:"description"`
}

type Update struct {
	UpdateID int64   `json:"update_id"`
	Message  Message `json:"message,omitempty"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text,omitempty"`
	From      User   `json:"from"`
	Voice     *Voice `json:"voice,omitempty"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type,omitempty"`
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
	MessageID int64 `json:"message_id"`
	Chat      Chat  `json:"chat"`
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
	botToken        string
	logger          *slog.Logger
	eventBus        bus.Bus
	httpClient      *http.Client
	rateLimiter     *rateLimiter
	stopChan        chan struct{}
	pollWg          sync.WaitGroup
	sendMu          sync.Mutex // protects sends during shutdown
	shutdownOnce    sync.Once
	isShutdown      bool
	lastUpdateID    int64
	operatorChatID  int64      // First user to interact becomes operator
	operatorMu      sync.Mutex // protects operatorChatID
	sttEngine       STTTranscriber // Optional: for voice message transcription
}

// STTTranscriber is the interface for speech-to-text engines.
type STTTranscriber interface {
	STTEnabled() bool
	Transcribe(ctx context.Context, audioData []byte, format string) (string, error)
}

// New creates a new Telegram adapter.
func New(botToken string, logger *slog.Logger) *Adapter {
	return &Adapter{
		botToken:    botToken,
		logger:      logger,
		httpClient:  &http.Client{Timeout: time.Second * 35}, // 30s poll + 5s buffer
		rateLimiter: &rateLimiter{chatLimts: make(map[int64]*limiter)},
		stopChan:    make(chan struct{}),
	}
}

// Name returns "telegram".
func (a *Adapter) Name() string { return adapterName }

// SetSTT sets the speech-to-text engine for voice message transcription.
func (a *Adapter) SetSTT(stt STTTranscriber) {
	a.sttEngine = stt
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

	// Handle voice messages
	if update.Message.Voice != nil {
		if a.sttEngine == nil || !a.sttEngine.STTEnabled() {
			a.sendMessageWithRetry(ctx, chatID, "Voice messages aren't enabled yet. Send me text instead!")
			return
		}

		// Show "recording audio" action
		a.sendChatAction(ctx, chatID, "record_audio")

		// Download and transcribe voice
		transcript, err := a.transcribeVoice(ctx, update.Message.Voice)
		if err != nil {
			a.logger.Warn("voice transcription failed", "error", err)
			a.sendMessageWithRetry(ctx, chatID, "Sorry, I couldn't understand that voice message. Try again?")
			return
		}

		text = strings.TrimSpace(transcript)
		if text == "" {
			a.sendMessageWithRetry(ctx, chatID, "I couldn't hear anything in that voice message.")
			return
		}

		a.logger.Info("voice message transcribed", "chat_id", chatID, "text", text)
	} else if update.Message.Text != "" {
		text = strings.TrimSpace(update.Message.Text)
	} else {
		// Ignore updates without text or voice
		return
	}

	// Track the first user as the operator (for proactive messages)
	a.operatorMu.Lock()
	if a.operatorChatID == 0 {
		a.operatorChatID = chatID
		a.logger.Info("operator chat ID set", "chat_id", chatID)
	}
	a.operatorMu.Unlock()

	// /start — route through the runtime so the assistant greets in its own voice.
	if strings.HasPrefix(text, "/start") {
		text = "Hi!"
	}

	// Show typing indicator while AI is thinking
	if err := a.sendChatAction(ctx, chatID, "typing"); err != nil {
		a.logger.Debug("Failed to send typing indicator", "error", err, "chat_id", chatID)
	}

	// Publish inbound message to the bus
	inboundMsg := bus.InboundMessage{
		From: strconv.FormatInt(chatID, 10),
		Text: text,
	}

	event := bus.Event{
		Type:      bus.TypeMessageInbound,
		Channel:   adapterName,
		SessionID: sessionID,
		Payload:   bus.MustJSON(inboundMsg),
	}

	if _, err := a.eventBus.Publish(ctx, event); err != nil {
		a.logger.Error("Failed to publish inbound message", "error", err, "chat_id", chatID)
	} else {
		a.logger.Debug("Published inbound message", "chat_id", chatID, "session_id", sessionID)
	}
}


// transcribeVoice downloads a voice message and transcribes it.
func (a *Adapter) transcribeVoice(ctx context.Context, voice *Voice) (string, error) {
	// Get file path from Telegram
	fileURL := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", telegramAPIBase, a.botToken, voice.FileID)
	resp, err := a.httpClient.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("get file info: %w", err)
	}
	defer resp.Body.Close()

	var fileResp struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fileResp); err != nil {
		return "", fmt.Errorf("parse file info: %w", err)
	}
	if !fileResp.OK || fileResp.Result.FilePath == "" {
		return "", fmt.Errorf("no file path returned")
	}

	// Download the voice file
	downloadURL := fmt.Sprintf("%s/file/bot%s/%s", telegramAPIBase, a.botToken, fileResp.Result.FilePath)
	dlResp, err := a.httpClient.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("download voice: %w", err)
	}
	defer dlResp.Body.Close()

	audioData, err := io.ReadAll(dlResp.Body)
	if err != nil {
		return "", fmt.Errorf("read voice data: %w", err)
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

	// Transcribe
	return a.sttEngine.Transcribe(ctx, audioData, format)
}

// maskToken returns a redacted version of the bot token for logging.
func maskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "****" + token[len(token)-4:]
}
