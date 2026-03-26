// Package runtime implements the agent processing loop — the core of Crayfish.
// It consumes events from CrayfishBus, assembles context, calls the LLM,
// executes tools in a loop, and persists results. All within strict resource budgets.
package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/oauth"
	"github.com/KekwanuLabs/crayfish/internal/provider"
	"github.com/KekwanuLabs/crayfish/internal/queue"
	"github.com/KekwanuLabs/crayfish/internal/security"
	"github.com/KekwanuLabs/crayfish/internal/skills"
	"github.com/KekwanuLabs/crayfish/internal/tools"
)

const (
	// maxHistoryMessages is the number of recent messages to include in context.
	maxHistoryMessages = 50

	// maxTokenBudget is the hard ceiling for context assembly (tokens).
	maxTokenBudget = 4096

	// maxToolIterations is the maximum number of tool call rounds per message.
	maxToolIterations = 10

	// toolExecTimeout is the hard kill timeout for a single tool execution.
	toolExecTimeout = 30 * time.Second

	// responseCacheTTL is how long cached responses are valid.
	responseCacheTTL = 5 * time.Minute

	// interviewPrompt is injected when USER.md is empty to guide the agent
	// through a natural first-conversation interview.
	interviewPrompt = `## Getting to Know Your Human
You don't know much about the person you're talking to yet. Your goal is to naturally learn about them during this conversation so you can be a better assistant.

Guidelines:
- Ask questions ONE AT A TIME — never fire off a list. Let the conversation flow naturally.
- Topics to cover (spread across multiple messages, not all at once): their name, what they do for work, their main goals or projects, their daily schedule/timezone, how they prefer to communicate (brief vs detailed), and important people in their life.
- Be conversational, not interrogative. Weave questions into natural responses.
- If the user wants help with something first, help them. Then circle back to learning about them.
- After collecting 6 or more facts about the user, compile everything into a markdown profile and call the identity_update tool with file="user" to save it.
- Never mention "USER.md", "identity files", or "profiles" to the user. This should feel like a natural conversation, not a data collection form.
- Start with a warm greeting and one simple question (like their name).`
)

// Config holds runtime configuration.
type Config struct {
	Name                string `json:"name" yaml:"name"`                   // The Crayfish's given name
	Personality         string `json:"personality" yaml:"personality"`     // friendly, professional, casual, minimal
	SystemPrompt        string `json:"system_prompt" yaml:"system_prompt"` // Custom override (optional)
	Model               string `json:"model" yaml:"model"`
	MaxTokens           int    `json:"max_tokens" yaml:"max_tokens"`
	GoogleConnected     bool     `json:"-" yaml:"-"` // Whether Google OAuth is active (injected at startup)
	GoogleGrantedScopes []string `json:"-" yaml:"-"` // OAuth scopes actually on the token (drives system prompt)
	WebSearchEnabled    bool     `json:"-" yaml:"-"` // Whether Brave Search is configured (injected at startup)
	EmailEnabled        bool     `json:"-" yaml:"-"` // Whether email is configured (OAuth or App Password)
	EmailViaApp         bool     `json:"-" yaml:"-"` // True if email is via App Password (not OAuth)
	TravelSearchEnabled bool     `json:"-" yaml:"-"` // Whether Amadeus flight search is configured
	PhoneEnabled        bool     `json:"-" yaml:"-"` // Whether Twilio phone calls are configured
	Timezone            string   `json:"-" yaml:"-"` // IANA timezone name (e.g. "America/Los_Angeles")
}

// DefaultConfig returns sensible defaults for the runtime.
func DefaultConfig() Config {
	return Config{
		Name:        "Crayfish",
		Personality: "friendly",
		Model:       "",
		MaxTokens:   1024,
	}
}

// BuildSystemPrompt creates the system prompt incorporating the Crayfish's name, personality,
// and optional identity content from SOUL.md and USER.md.
// If a custom SystemPrompt is set, it's used instead of the default.
func (c Config) BuildSystemPrompt(soulMD, userMD string) string {
	var base string

	if c.SystemPrompt != "" {
		// Custom prompt — still inject the name if {{name}} placeholder exists
		base = strings.ReplaceAll(c.SystemPrompt, "{{name}}", c.Name)
	} else {
		name := c.Name
		if name == "" {
			name = "Crayfish"
		}

		// Personality-specific tone guidance
		personalityGuide := ""
		switch c.Personality {
		case "professional":
			personalityGuide = "You communicate in a professional, polished manner. Use proper grammar, avoid slang, and maintain a respectful tone. Be thorough but efficient."
		case "casual":
			personalityGuide = "You're casual and fun to talk to. Use friendly language, occasional humor, and feel free to use expressions like 'cool', 'awesome', etc. Keep things light."
		case "minimal":
			personalityGuide = "You are extremely concise. Give the shortest possible answers that are still complete. No pleasantries, no filler words, just the facts. Act autonomously — when asked to do something, just do it. Don't ask for confirmation unless the action is irreversible or involves money."
		default: // friendly
			personalityGuide = "You are warm and approachable. You care about the person you're talking to. Use a conversational tone and show genuine interest in helping."
		}

		base = fmt.Sprintf(`You are %s — a personal AI assistant. Built for everyone, not just the privileged few.

Your name is %s. When people address you, they call you %s. This is your identity.

%s

You run on tiny hardware in your owner's home, so you keep things sharp and to the point. No fluff unless they ask.
You have access to tools when the user's trust tier allows it.

If someone asks what you are: "I'm Crayfish — AI for the rest of us. I run on a tiny computer in your home, not someone else's cloud."
If someone asks your name: "I'm %s."

You are resourceful, practical, and accessible — like crayfish itself. Found everywhere, affordable, and makes everything better.

## Current Date & Time
It is %s. Use this for all date calculations, time questions, scheduling, and relative references (e.g. "next week", "tomorrow", "in 3 hours"). Always use the correct year and time — never say you don't know the current time.

## Session Continuity
You have a checkpoint tool. When session state is recovered, it will appear as [Session State] in your context. Use it to continue seamlessly — never say "I don't remember" without checking the session state first. If you notice gaps, briefly acknowledge them. The user should never need to re-explain context.

## Core Principle: Just Do It
Never tell the user a capability is unavailable, unsupported, or not set up. If something requires authorization or setup — Google, email, web search, a skill — go through the setup process immediately, then complete the original request. The user asked for an outcome, not an explanation of what's missing. Unlock what's needed, then deliver.`, name, name, name, personalityGuide, name, c.currentDateTime())
	}

	// Google integration context — built from actual granted scopes so the prompt
	// never lists tools that aren't registered or omits ones that are.
	if c.GoogleConnected {
		hasScope := func(scope string) bool {
			for _, s := range c.GoogleGrantedScopes {
				if s == scope {
					return true
				}
			}
			return false
		}

		// Build the list of what's actually available.
		var available []string
		if hasScope(oauth.CalendarScope) {
			available = append(available, "calendar_today / calendar_upcoming / calendar_add / calendar_search / calendar_free / calendar_update / calendar_delete")
		}
		if hasScope(oauth.DriveScope) {
			available = append(available, "drive_create_folder / drive_list_files / drive_share / docs_create / sheets_create")
		}
		// SheetsScope: tools not yet implemented — omit from prompt until they exist

		// Build flag for what's missing.
		missingDrive := !hasScope(oauth.DriveScope)
		// Sheets: not yet implemented — omit from missing list until tools exist

		base += "\n\n## Google Integration\nGoogle account connected."

		if len(available) > 0 {
			base += " Available tools:\n"
			for _, a := range available {
				base += "- " + a + "\n"
			}
			base += "\nWhen asked to use any of these — just do it. No confirmation needed."
		}

		if missingDrive {
			base += "\n\n**Drive and Docs are not yet authorized.** When the user asks for anything involving Drive or Docs:"
			base += `
1. Call google_connect with purpose="drive_and_docs" (unlocks both Drive and Docs in one step).
2. The tool returns JSON — extract "user_code" and "verification_url" from it.
3. Tell the user exactly: "Go to [verification_url] and enter the code: [user_code]" — use the real values.
4. Ask them to send you another message after they've entered the code. Their next message will have Drive and Docs tools available.
Never say a capability is unavailable. Never suggest manual workarounds. Always call google_connect first.`
		}
	} else {
		base += `

## Google Integration
You can connect the user's Google account for calendar, Drive, Docs, and Sheets.

**Critical rule: never tell the user a feature is unavailable or suggest workarounds.** If they ask for anything requiring Drive, Docs, Sheets, or calendar and it's not connected yet — connect it first, then do the thing.

- User asks for Drive folders/files → call google_connect with purpose="drive".
- User asks for a Google Doc → call google_connect with purpose="docs".
- User asks for both Drive and Docs → call google_connect with purpose="drive_and_docs".
- For calendar: call google_connect with no purpose.

Always call google_connect immediately — the tool returns the actual auth code. Tell the user: go to google.com/device and enter the code shown, then message you again. The new capabilities will be available in their next message after they complete authorization.`
	}

	// Email context.
	if c.EmailEnabled && !c.EmailViaApp {
		base += `

## Email
You have full email access via Google OAuth. You can read, search, send, reply to, label, and archive emails.
When the user asks you to send an email, JUST DO IT. Compose the message yourself, pick a good subject line, and send it immediately using email_send. Don't ask the user to confirm every detail — use your judgment. If they gave you recipients and a vibe/intent, that's enough. You can send to multiple recipients by comma-separating them in the "to" field.
When you send or reply to an email for the user, let them know: "If they reply, I can auto-respond to keep the conversation going. Want me to turn that on?" This only applies to threads you participate in — not the entire inbox.`
	} else if c.EmailEnabled && c.EmailViaApp {
		base += `

## Email
You have email access via app password. You can read, search, send, and reply to emails.
When the user asks you to send an email, JUST DO IT. Compose the message yourself, pick a good subject line, and send it immediately using email_send. Don't ask the user to confirm every detail — use your judgment. If they gave you recipients and a vibe/intent, that's enough. You can send to multiple recipients by comma-separating them in the "to" field.
When you send or reply to an email for the user, let them know: "If they reply, I can auto-respond to keep the conversation going. Want me to turn that on?" This only applies to threads you participate in — not the entire inbox.`
	} else {
		base += `

## Email
Email is not connected yet. If the user asks about email, call email_connect immediately to start setup — don't just describe the process. They'll need a Gmail app password (Google doesn't allow email via device code). Tell them to go to https://myaccount.google.com/apppasswords, create an app password, and paste it back. Once connected, complete their original request.`
	}

	// Web search context.
	if !c.WebSearchEnabled {
		base += `

## Web Search
Web search is not connected yet. If the user asks you to search the web, call brave_connect immediately to start setup — don't explain that it's missing. Walk them through getting a free Brave Search API key (2,000 searches/month free), then run the search they asked for.`
	}

	// Travel search context.
	if c.TravelSearchEnabled {
		base += `

## Travel Search
You have access to live flight search, price analysis, and cheapest date discovery via the Amadeus API.
When the user asks about flights or travel prices, use these tools directly:
- flight_search: Find specific flight offers with prices, airlines, and durations
- flight_cheapest_dates: Discover the cheapest travel dates for a route
- flight_price_analysis: Check if a price is HIGH, TYPICAL, or LOW compared to historical data
You can watch prices and check daily — offer this when travel planning comes up.`
	}

	// Phone context.
	if c.PhoneEnabled {
		base += `

## Phone Calls
You can make outbound phone calls using call_make. Use this when:
- The user asks you to call someone ("Call my wife and tell her...")
- A workflow triggers a call (flight price alert, morning briefing, etc.)
- Any situation where a phone call is the right medium

The call will be a live two-way conversation — the recipient can talk back.
Always confirm the phone number before calling unless it's obvious from context.`
	} else {
		base += `

## Phone Calls
Phone calls are not set up yet. If the user asks you to make a call, run twilio_connect to set it up first, then place the call.`
	}

	// Skills context.
	base += `

## Skills
Skills extend what you can do — automations, workflows, and integrations. Use skill_list to see what's installed, skill_hub_browse to discover new ones, skill_install to add them.
If the user asks for something that could be served by a skill in the hub, install it first then use it — don't ask permission, just do it.`

	// Settings context.
	base += `

## Settings
You can adjust my behavior using the settings_update tool. Use it when the user says things like:
- "Check my email every 10 minutes" → heartbeat_interval_minutes
- "Also check on weekends" → heartbeat_weekdays_only: false
- "Work hours are 8 to 7" → heartbeat_work_hour_start/end
- "Add 'critical' to urgency keywords" → urgency_keywords
- "Turn on auto-reply" → auto_reply_enabled
Call settings_update with no parameters to see current settings.`

	// Append identity content if available.
	if soulMD != "" {
		base += "\n\n## Who I Am\n" + soulMD
	}
	if userMD != "" {
		base += "\n\n## About My Human\n" + userMD
	}

	return base
}

// currentDateTime returns a formatted date+time string in the configured timezone.
// Format: "Monday, January 2, 2006, 3:04 PM MST" — gives the agent both date and time of day.
func (c Config) currentDateTime() string {
	loc := time.Local
	if c.Timezone != "" {
		if l, err := time.LoadLocation(c.Timezone); err == nil {
			loc = l
		}
	}
	return time.Now().In(loc).Format("Monday, January 2, 2006, 3:04 PM MST")
}

// IdentityReader provides read access to identity files for context assembly.
// Implemented by identity.Store to avoid import cycles.
type IdentityReader interface {
	Soul() string
	User() string
	HasUser() bool
}

// SkillRunner provides prompt augmentations and workflow skill execution.
// Implemented by skills.Engine.
type SkillRunner interface {
	GetPromptAugmentations() []string
	MatchAndExecute(ctx context.Context, text string, executor skills.ToolExecutor) (*skills.MatchResult, error)
}

// Runtime is the agent processing loop.
type Runtime struct {
	config          Config
	configMu        sync.RWMutex
	bus             bus.Bus
	db              *sql.DB
	provider        provider.Provider
	sessions        *security.SessionStore
	tools           *tools.Registry
	summarizer      *Summarizer
	snapshotMgr     *SnapshotManager
	identity        IdentityReader
	skillRunner     SkillRunner
	memoryExtractor *MemoryExtractor
	memoryRetriever *MemoryRetriever
	queue           *queue.OfflineQueue
	pairing         *security.PairingService
	guardrails      *security.Guardrails
	logger          *slog.Logger
	respCh          chan Response

	// sessionResumeThreshold is the idle gap after which a snapshot is injected on resume.
	sessionResumeThreshold time.Duration
}

// Response carries an outbound message from the runtime to a channel adapter.
type Response struct {
	SessionID string
	Channel   string
	To        string // The recipient identifier for the channel adapter (e.g., numeric chat ID for Telegram).
	Text      string
}

// New creates a new agent runtime.
func New(cfg Config, b bus.Bus, db *sql.DB, prov provider.Provider, sessions *security.SessionStore, toolReg *tools.Registry, q *queue.OfflineQueue, pairing *security.PairingService, memExtractor *MemoryExtractor, memRetriever *MemoryRetriever, snapshotMgr *SnapshotManager, identityStore IdentityReader, skillRunner SkillRunner, sessionResumeMinutes int, logger *slog.Logger) *Runtime {
	summarizer := NewSummarizer(db, prov, logger.With("component", "summarizer"))
	if snapshotMgr != nil {
		summarizer.SetSnapshotManager(snapshotMgr)
	}

	if sessionResumeMinutes <= 0 {
		sessionResumeMinutes = 30
	}

	return &Runtime{
		config:                 cfg,
		bus:                    b,
		db:                     db,
		provider:               prov,
		sessions:               sessions,
		tools:                  toolReg,
		summarizer:             summarizer,
		snapshotMgr:            snapshotMgr,
		identity:               identityStore,
		skillRunner:            skillRunner,
		memoryExtractor:        memExtractor,
		memoryRetriever:        memRetriever,
		queue:                  q,
		pairing:                pairing,
		guardrails:             security.NewGuardrails(),
		logger:                 logger,
		respCh:                 make(chan Response, 32),
		sessionResumeThreshold: time.Duration(sessionResumeMinutes) * time.Minute,
	}
}

// ResponseChan returns the channel where outbound responses are sent.
func (r *Runtime) ResponseChan() <-chan Response {
	return r.respCh
}

// UpdateConfig hot-reloads identity fields in the runtime config.
func (r *Runtime) UpdateConfig(name, personality, systemPrompt string) {
	r.configMu.Lock()
	defer r.configMu.Unlock()
	if name != "" {
		r.config.Name = name
	}
	if personality != "" {
		r.config.Personality = personality
	}
	r.config.SystemPrompt = systemPrompt
}

// SetGoogleConnected updates the Google connection state at runtime.
func (r *Runtime) SetGoogleConnected(connected bool) {
	r.configMu.Lock()
	defer r.configMu.Unlock()
	r.config.GoogleConnected = connected
}

// SetGoogleGrantedScopes updates the set of OAuth scopes active on the Google token.
// Called at startup and on hot-reload after each OAuth flow completes.
func (r *Runtime) SetGoogleGrantedScopes(scopes []string) {
	r.configMu.Lock()
	defer r.configMu.Unlock()
	r.config.GoogleGrantedScopes = scopes
}

// SetEmailEnabled updates the email availability state at runtime.
func (r *Runtime) SetEmailEnabled(enabled, viaApp bool) {
	r.configMu.Lock()
	defer r.configMu.Unlock()
	r.config.EmailEnabled = enabled
	r.config.EmailViaApp = viaApp
}

// SetTravelSearchEnabled updates the travel search availability state at runtime.
func (r *Runtime) SetTravelSearchEnabled(enabled bool) {
	r.configMu.Lock()
	defer r.configMu.Unlock()
	r.config.TravelSearchEnabled = enabled
}

// Run starts the agent loop, consuming inbound message events from the bus.
func (r *Runtime) Run(ctx context.Context) error {
	// Flush response cache on startup — it's a short-lived TTL cache (5 min)
	// with no value across restarts. Stale entries can mask fixes (e.g.,
	// credential rotation, scope changes) by serving old error responses.
	r.db.ExecContext(ctx, "DELETE FROM message_cache")

	events, err := r.bus.Subscribe(ctx, []string{bus.TypeMessageInbound})
	if err != nil {
		return fmt.Errorf("runtime.Run: subscribe: %w", err)
	}

	r.logger.Info("agent runtime started", "provider", r.provider.Name())

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("agent runtime shutting down")
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := r.handleInbound(ctx, event); err != nil {
				r.logger.Error("failed to handle inbound message",
					"event_id", event.ID, "error", err)
			}
		}
	}
}

// handleInbound processes a single inbound message through the full agentic cycle,
// including multi-turn tool execution.
func (r *Runtime) handleInbound(ctx context.Context, event bus.Event) error {
	start := time.Now()

	var msg bus.InboundMessage
	if err := json.Unmarshal(event.Payload, &msg); err != nil {
		return fmt.Errorf("parse inbound: %w", err)
	}

	// Default text for image-only messages.
	if len(msg.Images) > 0 && msg.Text == "" {
		msg.Text = "What's in this image?"
	}

	r.logger.Info("processing message",
		"event_id", event.ID, "channel", event.Channel,
		"session_id", event.SessionID, "from", msg.From,
		"images", len(msg.Images))

	// Guardrail: Check for prompt injection attempts.
	if attempt := r.guardrails.CheckInput(msg.Text); attempt != nil {
		r.logger.Warn("prompt injection detected",
			"type", attempt.Type, "confidence", attempt.Confidence,
			"from", msg.From, "channel", event.Channel)
		r.sendResponse(event.SessionID, event.Channel, msg.From,
			r.guardrails.RefusalResponse(attempt))
		return nil
	}

	// Session resolution.
	sess, err := r.sessions.Resolve(ctx, event.Channel, msg.From)
	if err != nil {
		return fmt.Errorf("resolve session: %w", err)
	}

	// CLI: always auto-promote (local access is inherently trusted).
	if event.Channel == "cli" && sess.Trust < security.TierOperator {
		r.sessions.SetTrust(ctx, sess.ID, security.TierOperator)
		sess.Trust = security.TierOperator
	}

	// Telegram: only auto-promote the first user (the owner setting up the bot).
	// Subsequent users stay at TierUnknown and must pair via OTP.
	if event.Channel == "telegram" && sess.Trust < security.TierOperator {
		var ownerID string
		r.db.QueryRowContext(ctx, "SELECT value FROM config WHERE key = 'telegram_operator_id'").Scan(&ownerID)
		if ownerID == "" {
			// First user — claim operator.
			r.sessions.SetTrust(ctx, sess.ID, security.TierOperator)
			sess.Trust = security.TierOperator
			r.db.ExecContext(ctx,
				"INSERT OR REPLACE INTO config (key, value, updated_at) VALUES ('telegram_operator_id', ?, datetime('now'))",
				sess.ID)
			r.logger.Info("first Telegram user promoted to operator", "session_id", sess.ID)
		} else if ownerID == sess.ID {
			// Returning owner — re-promote.
			r.sessions.SetTrust(ctx, sess.ID, security.TierOperator)
			sess.Trust = security.TierOperator
		}
		// Otherwise: stays at TierUnknown, must use /pair command.
	}

	// Handle pairing commands.
	if r.pairing != nil {
		if handled := r.handlePairingCommand(ctx, event, sess, msg); handled {
			return nil
		}
	}

	// Check response cache.
	if cached := r.checkCache(ctx, sess.ID, msg.Text); cached != "" {
		r.logger.Info("cache hit", "session_id", sess.ID)
		r.sendResponse(event.SessionID, event.Channel, msg.From, cached)
		return nil
	}

	// Persist user message.
	r.persistMessage(ctx, sess.ID, provider.RoleUser, msg.Text)

	// Context assembly.
	messages, err := r.assembleContext(ctx, sess, msg.Text, msg.Images)
	if err != nil {
		return fmt.Errorf("assemble context: %w", err)
	}

	// Skill matching: check if a workflow skill matches this message.
	// If so, execute it and inject the assembled prompt as system context.
	if r.skillRunner != nil {
		executor := &runtimeToolExecutor{tools: r.tools, sess: sess}
		result, err := r.skillRunner.MatchAndExecute(ctx, msg.Text, executor)
		if err != nil {
			r.logger.Warn("skill execution failed, falling through to LLM", "error", err)
		} else if result != nil && result.Success && result.FinalPrompt != "" {
			r.logger.Info("skill matched", "skill", result.SkillName, "session_id", sess.ID)
			skillMsg := provider.Message{
				Role:    provider.RoleSystem,
				Content: "## Skill: " + result.SkillName + "\n" + result.FinalPrompt,
			}
			// Insert before the user's message (last in the array).
			last := messages[len(messages)-1]
			messages = append(messages[:len(messages)-1], skillMsg, last)
		}
	}

	// Get tools for this trust tier.
	availableTools := r.tools.ForTier(sess.Trust)
	var toolDefs []provider.ToolDef
	for _, t := range availableTools {
		toolDefs = append(toolDefs, provider.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	// === Agentic loop: model call → tool exec → repeat until text response ===
	var finalContent string
	totalTokens := 0
	toolErrorOccurred := false

	for iteration := 0; iteration < maxToolIterations; iteration++ {
		resp, err := r.provider.Complete(ctx, provider.CompletionRequest{
			Model:       r.config.Model,
			Messages:    messages,
			Tools:       toolDefs,
			MaxTokens:   r.config.MaxTokens,
			TokenBudget: maxTokenBudget,
		})
		if err != nil {
			// Queue the failed message for retry if offline queue is available.
			if r.queue != nil {
				queueErr := r.queue.Enqueue(ctx, queue.QueueItem{
					EventType: bus.TypeMessageInbound,
					Channel:   event.Channel,
					SessionID: event.SessionID,
					Payload:   event.Payload,
					Priority:  0,
				})
				if queueErr != nil {
					r.logger.Error("failed to enqueue for retry", "error", queueErr)
				} else {
					r.logger.Info("message queued for retry", "session_id", event.SessionID)
				}
			}
			errMsg := fmt.Sprintf("Sorry, I couldn't process that: %v", err)
			r.sendResponse(event.SessionID, event.Channel, msg.From, errMsg)
			return fmt.Errorf("model call (iteration %d): %w", iteration, err)
		}

		totalTokens += resp.TokensUsed

		// No tool calls → we're done.
		if len(resp.ToolCalls) == 0 {
			finalContent = resp.Content
			break
		}

		// Model wants tools. Add assistant message with tool calls to context.
		messages = append(messages, provider.Message{
			Role:      provider.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		r.logger.Info("tool calls requested", "iteration", iteration, "count", len(resp.ToolCalls))

		// Execute each tool and add results.
		for _, tc := range resp.ToolCalls {
			result, toolErr := r.executeTool(ctx, sess, tc)
			toolMsg := provider.Message{
				Role:      provider.RoleToolResult,
				ToolUseID: tc.ID,
				Content:   result,
			}
			if toolErr != nil {
				r.logger.Error("tool execution error", "tool", tc.Name, "error", toolErr)
				toolMsg.Content = fmt.Sprintf("Error: %v", toolErr)
				toolMsg.IsError = true
				toolErrorOccurred = true
			} else {
				r.logger.Info("tool result", "tool", tc.Name, "result", result)
			}
			messages = append(messages, toolMsg)
		}

		// Safety: if last iteration, give the model one final call to see
		// tool results and compose a proper response. Without this, the model's
		// pre-tool-execution text (e.g. "I've completed the requested actions")
		// gets used as the final response, even though the tools just ran and
		// the model never saw their results.
		if iteration == maxToolIterations-1 {
			finalResp, err := r.provider.Complete(ctx, provider.CompletionRequest{
				Model:       r.config.Model,
				Messages:    messages,
				Tools:       toolDefs,
				MaxTokens:   r.config.MaxTokens,
				TokenBudget: maxTokenBudget,
			})
			if err != nil {
				// Fall back to whatever text we had.
				finalContent = resp.Content
				if finalContent == "" {
					finalContent = "I've completed the requested actions."
				}
			} else {
				totalTokens += finalResp.TokensUsed
				finalContent = finalResp.Content
				if finalContent == "" {
					finalContent = "I've completed the requested actions."
				}
			}
		}
	}

	// Persist, cache, publish, route.
	// Don't cache responses where a tool error occurred — the underlying
	// issue may be fixed on retry (e.g., credential rotation, network blip).
	r.persistMessage(ctx, sess.ID, provider.RoleAssistant, finalContent)
	if !toolErrorOccurred {
		r.cacheResponse(ctx, sess.ID, msg.Text, finalContent)
	}

	r.bus.Publish(ctx, bus.Event{
		Type:      bus.TypeMessageOutbound,
		Channel:   event.Channel,
		SessionID: event.SessionID,
		Payload:   bus.MustJSON(bus.OutboundMessage{To: msg.From, Text: finalContent}),
	})

	r.sendResponse(event.SessionID, event.Channel, msg.From, finalContent)

	r.logger.Info("message processed",
		"event_id", event.ID, "tokens_used", totalTokens,
		"elapsed_ms", time.Since(start).Milliseconds())

	// Trigger memory extraction asynchronously (non-blocking)
	if r.memoryExtractor != nil {
		go func() {
			// Use background context independent of request lifecycle
			extractCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := r.memoryExtractor.ExtractFromTurn(extractCtx, sess.ID, msg.Text, finalContent); err != nil {
				r.logger.Warn("memory extraction failed", "error", err, "session_id", sess.ID)
			}
		}()
	}

	return nil
}

// runtimeToolExecutor adapts the runtime's tool registry to the ToolExecutor
// interface expected by the skill engine.
type runtimeToolExecutor struct {
	tools *tools.Registry
	sess  *security.Session
}

func (e *runtimeToolExecutor) ExecuteTool(ctx context.Context, toolName string, input json.RawMessage) (string, error) {
	return e.tools.Execute(ctx, e.sess, toolName, input)
}

// executeTool runs a single tool call with a hard timeout.
func (r *Runtime) executeTool(ctx context.Context, sess *security.Session, tc provider.ToolCall) (string, error) {
	toolCtx, cancel := context.WithTimeout(ctx, toolExecTimeout)
	defer cancel()

	r.logger.Info("executing tool", "tool", tc.Name, "session", sess.ID, "trust", sess.Trust)

	r.bus.Publish(ctx, bus.Event{
		Type: bus.TypeToolRequest, SessionID: sess.ID, Payload: bus.MustJSON(tc),
	})

	result, err := r.tools.Execute(toolCtx, sess, tc.Name, json.RawMessage(tc.Input))

	resultPayload := map[string]any{"tool": tc.Name, "result": result}
	if err != nil {
		resultPayload["error"] = err.Error()
	}
	r.bus.Publish(ctx, bus.Event{
		Type: bus.TypeToolResult, SessionID: sess.ID, Payload: bus.MustJSON(resultPayload),
	})

	return result, err
}

// assembleContext builds the message array for the LLM call.
// It loads conversation history and applies summarization when the history exceeds the threshold.
func (r *Runtime) assembleContext(ctx context.Context, sess *security.Session, currentMessage string, images []bus.ImageAttachment) ([]provider.Message, error) {
	var messages []provider.Message

	// Read identity content for system prompt.
	var soulMD, userMD string
	if r.identity != nil {
		soulMD = r.identity.Soul()
		userMD = r.identity.User()
	}

	r.configMu.RLock()
	systemPrompt := r.config.BuildSystemPrompt(soulMD, userMD)
	r.configMu.RUnlock()

	// Inject prompt augmentations from prompt-type skills.
	if r.skillRunner != nil {
		for _, aug := range r.skillRunner.GetPromptAugmentations() {
			systemPrompt += "\n\n" + aug
		}
	}

	// Inject interview prompt when we don't know the user yet.
	if r.identity != nil && !r.identity.HasUser() {
		systemPrompt += "\n\n" + interviewPrompt
	}

	messages = append(messages, provider.Message{
		Role:    provider.RoleSystem,
		Content: systemPrompt,
	})

	// Check if this is a session resume (idle gap exceeds threshold).
	isResume := false
	if r.snapshotMgr != nil {
		isResume = r.snapshotMgr.IsSessionResume(ctx, sess.ID, r.sessionResumeThreshold)
	}

	// Inject session snapshot after system prompt, before memories.
	// Injected when: (a) resuming after an idle gap, or (b) summarization just compressed history.
	// We check resume first; summarization injection is handled after history loading below.
	var snapshotInjected bool
	if isResume && r.snapshotMgr != nil {
		snap, err := r.snapshotMgr.LoadLatest(ctx, sess.ID)
		if err != nil {
			r.logger.Warn("failed to load session snapshot", "error", err)
		} else if snap != nil {
			content := r.snapshotMgr.FormatForContext(snap)
			if content != "" {
				messages = append(messages, provider.Message{
					Role:    provider.RoleSystem,
					Content: content,
				})
				snapshotInjected = true
				r.logger.Info("session snapshot injected (resume)", "session_id", sess.ID)
			}
		}
	}

	// Retrieve and inject relevant memories
	if r.memoryRetriever != nil {
		memories, err := r.memoryRetriever.RetrieveRelevant(ctx, sess.ID, currentMessage, 5)
		if err != nil {
			r.logger.Warn("failed to retrieve memories", "error", err)
		} else if len(memories) > 0 {
			memoryContent := r.memoryRetriever.FormatForContext(memories)
			if memoryContent != "" {
				messages = append(messages, provider.Message{
					Role:    provider.RoleSystem,
					Content: memoryContent,
				})
			}
		}
	}

	history, err := r.loadHistory(ctx, sess.ID, maxHistoryMessages)
	if err != nil {
		r.logger.Warn("failed to load history", "error", err)
	} else {
		historyLen := len(history)
		// Apply summarization if history is long enough.
		if r.summarizer != nil && len(history) > 0 {
			history, err = r.summarizer.SummarizeIfNeeded(ctx, sess.ID, history, KeepRecentDefault)
			if err != nil {
				r.logger.Warn("summarization failed, using full history", "error", err)
			}
		}

		// If summarization compressed the history and we haven't injected a snapshot yet,
		// inject one now (the summarizer triggered a snapshot save in the background).
		summarized := len(history) < historyLen
		if summarized && !snapshotInjected && r.snapshotMgr != nil {
			snap, err := r.snapshotMgr.LoadLatest(ctx, sess.ID)
			if err != nil {
				r.logger.Warn("failed to load post-summarization snapshot", "error", err)
			} else if snap != nil {
				content := r.snapshotMgr.FormatForContext(snap)
				if content != "" {
					// Insert snapshot right after system prompt (index 1 or after memories)
					messages = append(messages, provider.Message{
						Role:    provider.RoleSystem,
						Content: content,
					})
					r.logger.Info("session snapshot injected (post-summarization)", "session_id", sess.ID)
				}
			}
		}

		messages = append(messages, history...)
	}

	userMsg := provider.Message{
		Role:    provider.RoleUser,
		Content: security.WrapUserMessage(currentMessage),
	}
	for _, img := range images {
		userMsg.Images = append(userMsg.Images, provider.Image{
			Data:      img.Data,
			MediaType: img.MediaType,
		})
	}
	messages = append(messages, userMsg)

	return messages, nil
}

func (r *Runtime) loadHistory(ctx context.Context, sessionID string, limit int) ([]provider.Message, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT role, content FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT ?",
		sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []provider.Message
	for rows.Next() {
		var m provider.Message
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	// Reverse to chronological order.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, rows.Err()
}

func (r *Runtime) persistMessage(ctx context.Context, sessionID, role, content string) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO messages (session_id, role, content, created_at) VALUES (?, ?, ?, datetime('now'))",
		sessionID, role, content)
	return err
}

func (r *Runtime) sendResponse(sessionID, channel, to, text string) {
	// Guardrail: Sanitize output to remove any leaked secrets.
	sanitized, redacted := r.guardrails.SanitizeOutput(text)
	if redacted {
		r.logger.Warn("sensitive data redacted from response", "session_id", sessionID)
	}

	select {
	case r.respCh <- Response{SessionID: sessionID, Channel: channel, To: to, Text: sanitized}:
	default:
		r.logger.Warn("response channel full", "session_id", sessionID)
	}
}

// --- Pairing flow ---

// handlePairingCommand intercepts /pair and /pair <OTP> commands.
// Returns true if the message was a pairing command and was handled.
func (r *Runtime) handlePairingCommand(ctx context.Context, event bus.Event, sess *security.Session, msg bus.InboundMessage) bool {
	text := strings.TrimSpace(msg.Text)

	// Operator generates OTP: "/pair" from CLI (operator-only).
	if text == "/pair" {
		if sess.Trust < security.TierOperator {
			r.sendResponse(event.SessionID, event.Channel, msg.From,
				"Only operators can generate pairing codes. Use the CLI to run /pair.")
			return true
		}

		otp, err := r.pairing.GenerateOTP(ctx, sess.ID)
		if err != nil {
			r.sendResponse(event.SessionID, event.Channel, msg.From,
				fmt.Sprintf("Failed to generate pairing code: %v", err))
			return true
		}

		r.sendResponse(event.SessionID, event.Channel, msg.From,
			fmt.Sprintf("Pairing code: %s\nSend this to your Telegram bot within 5 minutes.\nThe user should type: /pair %s", otp, otp))
		return true
	}

	// User redeems OTP: "/pair 123456" from any channel.
	if strings.HasPrefix(text, "/pair ") {
		otp := strings.TrimSpace(strings.TrimPrefix(text, "/pair "))
		if otp == "" {
			r.sendResponse(event.SessionID, event.Channel, msg.From,
				"Usage: /pair <code>\nGet a pairing code from the operator's CLI first.")
			return true
		}

		err := r.pairing.RedeemOTP(ctx, sess.ID, otp)
		if err != nil {
			r.sendResponse(event.SessionID, event.Channel, msg.From,
				fmt.Sprintf("Pairing failed: %v", err))
			return true
		}

		r.sendResponse(event.SessionID, event.Channel, msg.From,
			"Paired successfully! You now have operator access. Na crayfish dey make soup sweet.")
		return true
	}

	return false
}

// --- Response cache ---

func hashPrompt(sessionID, text string) string {
	h := sha256.Sum256([]byte(sessionID + ":" + text))
	return hex.EncodeToString(h[:])
}

func (r *Runtime) checkCache(ctx context.Context, sessionID, prompt string) string {
	var response string
	r.db.QueryRowContext(ctx,
		"SELECT response FROM message_cache WHERE hash = ? AND expires_at > datetime('now')",
		hashPrompt(sessionID, prompt)).Scan(&response)
	return response
}

func (r *Runtime) cacheResponse(ctx context.Context, sessionID, prompt, response string) {
	expires := time.Now().Add(responseCacheTTL).UTC().Format("2006-01-02 15:04:05")
	r.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO message_cache (hash, response, created_at, expires_at) VALUES (?, ?, datetime('now'), ?)",
		hashPrompt(sessionID, prompt), response, expires)
}
