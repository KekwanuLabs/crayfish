// Package phone implements a Twilio ConversationRelay channel adapter.
// Real-time voice conversations over PSTN using:
//   - Twilio ConversationRelay (handles STT via Deepgram, barge-in, turn detection)
//   - ElevenLabs (TTS — configured natively in TwiML, no synthesis code needed here)
//   - Claude streaming (LLM — tokens piped to Twilio as they arrive, ~1-1.4s latency)
package phone

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/channels"
	"github.com/KekwanuLabs/crayfish/internal/provider"
)

// Config holds everything the phone channel needs.
type Config struct {
	TwilioAccountSID  string
	TwilioAuthToken   string
	TwilioFromNumber  string
	TunnelURL         string // e.g. "https://abc.trycloudflare.com"
	ElevenLabsVoiceID string
	SystemPrompt      string // injected at the start of every call
}

// Adapter is the Crayfish channel adapter for phone calls.
type Adapter struct {
	config   Config
	llm      provider.Provider
	logger   *slog.Logger
	sessions sync.Map // callSid → *Session
}

// New creates a phone channel adapter.
func New(cfg Config, llm provider.Provider, logger *slog.Logger) *Adapter {
	return &Adapter{config: cfg, llm: llm, logger: logger}
}

// Name returns "phone".
func (a *Adapter) Name() string { return "phone" }

// Start implements channels.ChannelAdapter. Phone sessions are driven by WebSocket, not the bus.
func (a *Adapter) Start(_ context.Context, _ bus.Bus) error {
	a.logger.Info("phone channel ready",
		"from", a.config.TwilioFromNumber,
		"tunnel", a.config.TunnelURL)
	return nil
}

// Stop terminates all active call sessions.
func (a *Adapter) Stop() error {
	a.sessions.Range(func(_, v any) bool {
		if sess, ok := v.(*Session); ok {
			sess.end()
		}
		return true
	})
	return nil
}

// Send is a no-op — phone responses are sent inside WebSocket sessions directly.
func (a *Adapter) Send(_ context.Context, _ channels.OutboundMessage) error { return nil }

// HandleTwiML is the HTTP handler that serves the ConversationRelay TwiML.
// Twilio fetches this when the call connects (inbound or outbound).
func (a *Adapter) HandleTwiML(w http.ResponseWriter, r *http.Request) {
	callSid := r.URL.Query().Get("CallSid")
	if callSid == "" {
		callSid = r.FormValue("CallSid")
	}
	greeting := r.URL.Query().Get("greeting")

	wsURL := a.wsURL(callSid)
	voice := a.elevenLabsVoice()

	var welcomeAttr string
	if greeting != "" {
		welcomeAttr = fmt.Sprintf(`welcomeGreeting="%s"`, escapeXML(greeting))
	}

	twiml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Response>
  <Connect>
    <ConversationRelay
      url="%s"
      ttsProvider="ElevenLabs"
      voice="%s"
      interruptible="any"
      interruptSensitivity="high"
      %s
    />
  </Connect>
</Response>`, wsURL, voice, welcomeAttr)

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	fmt.Fprint(w, twiml)
	a.logger.Info("TwiML served", "call_sid", callSid)
}

// HandleWebSocket is the HTTP handler for the ConversationRelay WebSocket endpoint.
func (a *Adapter) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := UpgradeWS(w, r)
	if err != nil {
		a.logger.Warn("WebSocket upgrade failed", "error", err)
		return
	}

	sess := newSession(ws, a.config.SystemPrompt, a.llm, a.logger)
	go func() {
		sess.run()
		if sess.callSid != "" {
			a.sessions.Delete(sess.callSid)
		}
	}()
}

// MakeCall initiates an outbound call via the Twilio REST API.
func (a *Adapter) MakeCall(ctx context.Context, toNumber, greeting string) (string, error) {
	if a.config.TwilioAccountSID == "" {
		return "", fmt.Errorf("Twilio not configured — run twilio_connect first")
	}
	if a.config.TunnelURL == "" {
		return "", fmt.Errorf("tunnel URL not set — add tunnel_url to config or set CRAYFISH_TUNNEL_URL")
	}

	twimlURL := strings.TrimSuffix(a.config.TunnelURL, "/") + "/phone/twiml"
	if greeting != "" {
		twimlURL += "?greeting=" + url.QueryEscape(greeting)
	}

	sid, err := twilioCall(ctx,
		a.config.TwilioAccountSID,
		a.config.TwilioAuthToken,
		a.config.TwilioFromNumber,
		toNumber,
		twimlURL,
	)
	if err != nil {
		return "", err
	}
	a.logger.Info("outbound call initiated", "to", toNumber, "call_sid", sid)
	return sid, nil
}

// wsURL returns the WebSocket URL for ConversationRelay.
func (a *Adapter) wsURL(callSid string) string {
	base := strings.TrimSuffix(a.config.TunnelURL, "/")
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	if callSid != "" {
		return fmt.Sprintf("%s/phone/ws?callSid=%s", base, callSid)
	}
	return base + "/phone/ws"
}

// elevenLabsVoice returns the voice string for ConversationRelay TwiML.
// Format: {voiceId}-{model}-{speed}_{stability}_{similarity}
func (a *Adapter) elevenLabsVoice() string {
	id := a.config.ElevenLabsVoiceID
	if id == "" {
		id = "21m00Tcm4TlvDq8ikWAM" // Rachel
	}
	return fmt.Sprintf("%s-flash_v2_5-1.0_0.5_0.75", id)
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// --- ConversationRelay message types ---

type twilioMsg struct {
	Type                    string `json:"type"`
	CallSid                 string `json:"callSid"`
	From                    string `json:"from"`
	To                      string `json:"to"`
	VoicePrompt             string `json:"voicePrompt"`
	UtteranceUntilInterrupt string `json:"utteranceUntilInterrupt"`
	Description             string `json:"description"`
}

// --- Session ---

// Session manages one active phone call.
type Session struct {
	ws           *WSConn
	callSid      string
	from         string
	history      []provider.Message
	systemPrompt string
	llm          provider.Provider
	logger       *slog.Logger
	cancelStream context.CancelFunc
	mu           sync.Mutex
}

func newSession(ws *WSConn, systemPrompt string, llm provider.Provider, logger *slog.Logger) *Session {
	return &Session{
		ws:           ws,
		systemPrompt: systemPrompt,
		llm:          llm,
		logger:       logger,
	}
}

// run is the main conversation loop for a call.
func (s *Session) run() {
	defer s.ws.Close()

	for {
		var msg twilioMsg
		if err := s.ws.ReadJSON(&msg); err != nil {
			if !strings.Contains(err.Error(), "closed") {
				s.logger.Debug("call WebSocket closed", "call_sid", s.callSid, "error", err)
			}
			return
		}

		switch msg.Type {
		case "setup":
			s.callSid = msg.CallSid
			s.from = msg.From
			s.logger.Info("call connected", "call_sid", s.callSid, "from", s.from)

		case "prompt":
			s.cancelCurrent()
			s.mu.Lock()
			s.history = append(s.history, provider.Message{
				Role:    provider.RoleUser,
				Content: msg.VoicePrompt,
			})
			s.mu.Unlock()
			s.logger.Info("caller said", "call_sid", s.callSid, "text", msg.VoicePrompt)
			go s.respond()

		case "interrupt":
			// Caller spoke over the AI — cancel and truncate what was actually heard.
			s.cancelCurrent()
			if msg.UtteranceUntilInterrupt != "" {
				s.mu.Lock()
				for i := len(s.history) - 1; i >= 0; i-- {
					if s.history[i].Role == provider.RoleAssistant {
						s.history[i].Content = msg.UtteranceUntilInterrupt
						s.history = s.history[:i+1]
						break
					}
				}
				s.mu.Unlock()
			}
			s.logger.Debug("barge-in", "call_sid", s.callSid, "heard", msg.UtteranceUntilInterrupt)

		case "error":
			s.logger.Warn("ConversationRelay error", "call_sid", s.callSid, "detail", msg.Description)

		case "end":
			s.logger.Info("call ended", "call_sid", s.callSid)
			return
		}
	}
}

// respond streams Claude tokens back to Twilio.
func (s *Session) respond() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	s.mu.Lock()
	s.cancelStream = cancel
	s.mu.Unlock()
	defer cancel()

	s.mu.Lock()
	msgs := make([]provider.Message, 0, len(s.history)+1)
	if s.systemPrompt != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: s.systemPrompt})
	}
	msgs = append(msgs, s.history...)
	s.mu.Unlock()

	req := provider.CompletionRequest{
		Messages:  msgs,
		MaxTokens: 300, // Keep phone responses concise — caller doesn't want an essay
	}

	var full strings.Builder
	_, err := s.llm.Stream(ctx, req, func(token string) error {
		full.WriteString(token)
		return s.ws.WriteJSON(map[string]any{
			"type":  "text",
			"token": token,
			"last":  false,
		})
	})

	// Always send the terminator so Twilio knows the response is done.
	_ = s.ws.WriteJSON(map[string]any{"type": "text", "token": "", "last": true})

	if err != nil && ctx.Err() == nil {
		s.logger.Warn("stream error during call", "call_sid", s.callSid, "error", err)
		return
	}

	if full.Len() > 0 {
		s.mu.Lock()
		s.history = append(s.history, provider.Message{
			Role:    provider.RoleAssistant,
			Content: full.String(),
		})
		s.mu.Unlock()
	}
}

func (s *Session) cancelCurrent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelStream != nil {
		s.cancelStream()
		s.cancelStream = nil
	}
}

func (s *Session) end() {
	s.cancelCurrent()
	s.ws.Close()
}

// ensure Adapter implements channels.ChannelAdapter at compile time.
var _ channels.ChannelAdapter = (*Adapter)(nil)

// marshalJSON is a convenience for writing to the WebSocket.
func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}
