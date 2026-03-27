// Package phone implements a Twilio ConversationRelay channel adapter.
// Real-time voice conversations over PSTN using:
//   - Twilio ConversationRelay (handles STT via Deepgram, barge-in, turn detection)
//   - ElevenLabs (TTS — configured natively in TwiML, no synthesis code needed here)
//   - Claude streaming (LLM — tokens piped to Twilio as they arrive, ~1-1.4s latency)
package phone

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
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
	config     Config
	llm        provider.Provider
	logger     *slog.Logger
	sessions   sync.Map // callSid → *Session
	mu         sync.RWMutex
	smsHandler func(from, body string) // set by app.go for async SMS processing
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

// Send delivers an outbound SMS reply via the Twilio Messages REST API.
// Called by the gateway when the AI agent produces a response to an SMS.
func (a *Adapter) Send(ctx context.Context, msg channels.OutboundMessage) error {
	if msg.Text == "" || msg.To == "" {
		return nil
	}
	return sendSMS(ctx, a.config.TwilioAccountSID, a.config.TwilioAuthToken,
		a.config.TwilioFromNumber, msg.To, msg.Text)
}

// HandleTwiML is the HTTP handler that serves the ConversationRelay TwiML.
// Twilio fetches this when the call connects (inbound or outbound).
// Requests not originating from Twilio are rejected (HMAC-SHA1 signature check).
func (a *Adapter) HandleTwiML(w http.ResponseWriter, r *http.Request) {
	if !validateTwilioRequest(r, a.config.TwilioAuthToken) {
		a.logger.Warn("rejected unauthorized TwiML request", "remote", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Parse form body (POST) first, fall back to URL query params.
	_ = r.ParseForm()
	callSid := r.FormValue("CallSid")
	if callSid == "" {
		callSid = r.URL.Query().Get("CallSid")
	}
	// Per-call context — set by MakeCall as query params on the TwiML URL.
	contact := r.URL.Query().Get("contact")
	caller  := r.URL.Query().Get("caller")
	purpose := r.URL.Query().Get("purpose")
	opening := r.URL.Query().Get("opening")

	wsURL := a.wsURL(callSid)
	voice := a.elevenLabsVoice()

	// Build welcomeGreeting — the first thing the phone agent says.
	var welcomeAttr string
	if opening != "" {
		welcomeAttr = fmt.Sprintf(`welcomeGreeting="%s"`, escapeXML(opening))
	} else if contact != "" && caller != "" {
		auto := fmt.Sprintf("Hi %s, this is Crayfish calling on behalf of %s.", contact, caller)
		if purpose != "" {
			auto += " I have a quick message for you."
		}
		welcomeAttr = fmt.Sprintf(`welcomeGreeting="%s"`, escapeXML(auto))
	}

	// Pass context as ConversationRelay <Parameter> elements — they arrive
	// in the WebSocket setup message as customParameters so the session
	// can build a per-call system prompt.
	var params strings.Builder
	if contact != "" {
		params.WriteString(fmt.Sprintf(`      <Parameter name="contact" value="%s"/>`, escapeXML(contact)) + "\n")
	}
	if caller != "" {
		params.WriteString(fmt.Sprintf(`      <Parameter name="caller" value="%s"/>`, escapeXML(caller)) + "\n")
	}
	if purpose != "" {
		params.WriteString(fmt.Sprintf(`      <Parameter name="purpose" value="%s"/>`, escapeXML(purpose)) + "\n")
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
    >
%s    </ConversationRelay>
  </Connect>
</Response>`, wsURL, voice, welcomeAttr, params.String())

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	fmt.Fprint(w, twiml)
	a.logger.Info("TwiML served", "call_sid", callSid)
}

// HandleSMS is the HTTP handler for incoming SMS messages from Twilio.
// Validates the Twilio signature, then routes the message to the event bus
// for the AI agent to process. Responds with empty TwiML immediately and
// sends the AI reply asynchronously via the Twilio Messages REST API.
func (a *Adapter) HandleSMS(w http.ResponseWriter, r *http.Request) {
	if !validateTwilioRequest(r, a.config.TwilioAuthToken) {
		a.logger.Warn("rejected unauthorized SMS", "remote", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	_ = r.ParseForm()
	from := r.FormValue("From") // sender's phone number
	body := r.FormValue("Body") // SMS text content
	msgSID := r.FormValue("MessageSid")

	a.logger.Info("SMS received", "from", from, "preview", truncate(body, 60), "sid", msgSID)

	// Respond with empty TwiML immediately — Twilio has a 15s timeout.
	// The AI processes the message asynchronously and replies via REST API.
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Response/>`)

	// Route to event bus for async AI processing.
	if a.smsHandler != nil {
		go a.smsHandler(from, body)
	}
}

// SetSMSHandler registers the callback that processes incoming SMS messages.
// Called by app.go after wiring up the event bus.
func (a *Adapter) SetSMSHandler(fn func(from, body string)) {
	a.smsHandler = fn
}

// HandleWebSocket is the HTTP handler for the ConversationRelay WebSocket endpoint.
// Validates the Twilio signature before upgrading.
func (a *Adapter) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !validateTwilioRequest(r, a.config.TwilioAuthToken) {
		a.logger.Warn("rejected unauthorized WebSocket upgrade", "remote", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
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

// UpdateTunnelURL updates the tunnel URL used for outbound calls and TwiML.
// Called automatically by the tunnel manager whenever the URL changes.
func (a *Adapter) UpdateTunnelURL(url string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.config.TunnelURL = url
}

// MakeCall initiates an outbound call via the Twilio REST API.
// contactName, callerName, purpose, and opening provide per-call context
// that shapes how the phone agent introduces itself and conducts the call.
func (a *Adapter) MakeCall(ctx context.Context, toNumber, contactName, callerName, purpose, opening string) (string, error) {
	if a.config.TwilioAccountSID == "" {
		return "", fmt.Errorf("Twilio not configured — run twilio_connect first")
	}
	if a.config.TunnelURL == "" {
		return "", fmt.Errorf("tunnel URL not set — phone calls need a public URL (Cloudflare Tunnel). Tell the user to set up a tunnel or set CRAYFISH_TUNNEL_URL.")
	}

	twimlURL := strings.TrimSuffix(a.config.TunnelURL, "/") + "/phone/twiml"

	// Encode call context as query params — TwiML handler reads them and
	// passes them to the WebSocket session as ConversationRelay <Parameter> elements.
	q := url.Values{}
	if contactName != "" {
		q.Set("contact", contactName)
	}
	if callerName != "" {
		q.Set("caller", callerName)
	}
	if purpose != "" {
		q.Set("purpose", purpose)
	}
	if opening != "" {
		q.Set("opening", opening)
	}
	if len(q) > 0 {
		twimlURL += "?" + q.Encode()
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
	a.logger.Info("outbound call initiated", "to", toNumber, "call_sid", sid, "purpose", purpose)
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

// validateTwilioRequest returns true if the request genuinely came from Twilio.
// Twilio signs every request with HMAC-SHA1(authToken, fullURL + sorted POST params).
// Rejecting unsigned requests prevents toll fraud from anyone who finds the tunnel URL.
// See: https://www.twilio.com/docs/usage/security#validating-signatures-from-twilio
func validateTwilioRequest(r *http.Request, authToken string) bool {
	if authToken == "" {
		return true // no token configured — skip validation (setup phase)
	}

	signature := r.Header.Get("X-Twilio-Signature")
	if signature == "" {
		return false
	}

	// Build the full URL including scheme.
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	fullURL := scheme + "://" + r.Host + r.RequestURI

	// Parse POST body to get form params (Twilio uses POST for TwiML webhook).
	_ = r.ParseForm()

	// Sort POST params alphabetically and concatenate key+value pairs.
	var sb strings.Builder
	sb.WriteString(fullURL)
	if r.Method == "POST" {
		keys := make([]string, 0, len(r.PostForm))
		for k := range r.PostForm {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteString(k)
			sb.WriteString(r.PostForm.Get(k))
		}
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(sb.String()))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
	Type                    string            `json:"type"`
	CallSid                 string            `json:"callSid"`
	From                    string            `json:"from"`
	To                      string            `json:"to"`
	VoicePrompt             string            `json:"voicePrompt"`
	UtteranceUntilInterrupt string            `json:"utteranceUntilInterrupt"`
	Description             string            `json:"description"`
	CustomParameters        map[string]string `json:"customParameters"`
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
			// Build per-call system prompt from context passed via <Parameter> elements.
			if p := msg.CustomParameters; len(p) > 0 {
				s.systemPrompt = buildCallSystemPrompt(
					s.systemPrompt, p["caller"], p["contact"], p["purpose"])
			}
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

// buildCallSystemPrompt creates a per-call system prompt injected into the
// phone agent's context. Layers on top of the base system prompt.
func buildCallSystemPrompt(base, callerName, contactName, purpose string) string {
	var sb strings.Builder

	sb.WriteString("You are Crayfish, an AI voice assistant")
	if callerName != "" {
		sb.WriteString(" calling on behalf of " + callerName)
	}
	sb.WriteString(".\n\n")

	if contactName != "" {
		sb.WriteString("You are speaking with " + contactName + ".\n")
	}

	if purpose != "" {
		sb.WriteString("Purpose of this call: " + purpose + "\n\n")
		sb.WriteString("Deliver this message clearly and naturally. Answer any follow-up questions as best you can. ")
		sb.WriteString("If they ask something you don't know (like specific plans or details), acknowledge it warmly and suggest they contact ")
		if callerName != "" {
			sb.WriteString(callerName)
		} else {
			sb.WriteString("the caller")
		}
		sb.WriteString(" directly.\n\n")
	}

	sb.WriteString("Phone call guidelines:\n")
	sb.WriteString("- Keep responses short — this is a phone call, not a chat\n")
	sb.WriteString("- Speak naturally, warmly, and conversationally\n")
	sb.WriteString("- If the call purpose is complete and they have no more questions, wrap up politely\n")
	sb.WriteString("- Don't mention being an AI unless directly asked\n")

	if base != "" {
		return base + "\n\n---\n\n" + sb.String()
	}
	return sb.String()
}

// ensure Adapter implements channels.ChannelAdapter at compile time.
var _ channels.ChannelAdapter = (*Adapter)(nil)

// marshalJSON is a convenience for writing to the WebSocket.
func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}
