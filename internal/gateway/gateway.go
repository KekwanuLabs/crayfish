// Package gateway implements the Crayfish Gateway — the always-on edge control plane.
// It wires together channel adapters, the bus, runtime, and provides HTTP/WebSocket APIs.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/channels"
	"github.com/KekwanuLabs/crayfish/internal/channels/phone"
	"github.com/KekwanuLabs/crayfish/internal/oauth"
	"github.com/KekwanuLabs/crayfish/internal/runtime"
	"github.com/KekwanuLabs/crayfish/internal/skills"
	"github.com/KekwanuLabs/crayfish/internal/storage"
)

// AppAccessor provides dashboard access to app-level state.
type AppAccessor interface {
	DashboardConfig() map[string]any
	UpdateConfig(updates map[string]any) (restartNeeded bool, err error)
	Uptime() time.Duration
	AppVersion() string
	VoiceInstallProgress() map[string]any
}

// Config holds gateway configuration.
type Config struct {
	ListenAddr string `json:"listen_addr" yaml:"listen_addr"`
	DBMaxMB    int64  `json:"db_max_mb" yaml:"db_max_mb"`
	SkillsDir  string `json:"skills_dir" yaml:"skills_dir"` // Directory for user skills
	APIKey     string `json:"-" yaml:"-"`                   // Dashboard API key for authentication
}

// DefaultConfig returns sensible gateway defaults.
func DefaultConfig() Config {
	return Config{
		ListenAddr: ":8119",
		DBMaxMB:    500,
	}
}

// Gateway is the main application orchestrator.
type Gateway struct {
	config          Config
	db              *storage.DB
	bus             bus.Bus
	rt              *runtime.Runtime
	adapters        map[string]channels.ChannelAdapter
	phoneAdapter    *phone.Adapter // optional — registered when Twilio is configured
	skillRegistry   *skills.Registry
	skillHub        *skills.HubClient
	appRef          AppAccessor
	oauthClient     *oauth.Client
	onOAuthComplete func(oauth.Token)
	server          *http.Server
	logger          *slog.Logger
	mu              sync.RWMutex
}

// New creates a new Gateway instance. Accepts a pre-opened DB so the entire
// process shares a single SQLite connection (critical for WAL correctness).
func New(cfg Config, db *storage.DB, logger *slog.Logger) *Gateway {
	return &Gateway{
		config:   cfg,
		db:       db,
		adapters: make(map[string]channels.ChannelAdapter),
		logger:   logger,
	}
}

// SetSkillRegistry sets the skill registry for the gateway.
// This enables the skills API and web UI.
func (g *Gateway) SetSkillRegistry(registry *skills.Registry) {
	g.skillRegistry = registry
}

// SetSkillHub sets the hub client for the gateway's skills API.
func (g *Gateway) SetSkillHub(hub *skills.HubClient) {
	g.skillHub = hub
}

// SetAppAccessor sets the app accessor for the dashboard.
func (g *Gateway) SetAppAccessor(a AppAccessor) {
	g.appRef = a
}

// SetOAuthClient sets the OAuth client and completion callback for Google integration.
func (g *Gateway) SetOAuthClient(client *oauth.Client, onComplete func(oauth.Token)) {
	g.oauthClient = client
	g.onOAuthComplete = onComplete
}

// RegisterAdapter adds a channel adapter to the gateway.
func (g *Gateway) RegisterAdapter(adapter channels.ChannelAdapter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.adapters[adapter.Name()] = adapter
	g.logger.Info("adapter registered", "name", adapter.Name())
}

// NotifyOperator sends a system message to the operator via their preferred channel.
// Used by background services (tunnel manager, etc.) to report status changes.
func (g *Gateway) NotifyOperator(ctx context.Context, message string) error {
	g.mu.RLock()
	tgAdapter, ok := g.adapters["telegram"]
	g.mu.RUnlock()
	if !ok || tgAdapter == nil {
		return nil
	}
	return tgAdapter.Send(ctx, channels.OutboundMessage{
		Text: message,
	})
}

// RegisterPhoneAdapter wires the Twilio ConversationRelay phone adapter.
// Exposes /phone/twiml (TwiML endpoint) and /phone/ws (WebSocket endpoint).
// isLocalRequest returns true if the request originates from localhost or a private network.
func isLocalRequest(r *http.Request) bool {
	host := r.RemoteAddr
	// Strip port.
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	host = strings.TrimPrefix(host, "[") // IPv6
	host = strings.TrimSuffix(host, "]")

	// Check X-Forwarded-For from Cloudflare Tunnel (it sends the real IP).
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		host = strings.TrimSpace(strings.Split(xff, ",")[0])
	}

	return host == "127.0.0.1" || host == "::1" ||
		strings.HasPrefix(host, "192.168.") ||
		strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "172.16.") ||
		strings.HasPrefix(host, "172.17.") ||
		strings.HasPrefix(host, "172.18.") ||
		strings.HasPrefix(host, "172.19.") ||
		strings.HasPrefix(host, "172.2") ||
		strings.HasPrefix(host, "172.3")
}

func (g *Gateway) RegisterPhoneAdapter(adapter *phone.Adapter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.phoneAdapter = adapter
	g.adapters[adapter.Name()] = adapter
	g.logger.Info("phone adapter registered (Twilio ConversationRelay)")
}

// Start initializes and runs the complete gateway stack.
func (g *Gateway) Start(ctx context.Context, rt *runtime.Runtime, eventBus bus.Bus) error {
	g.bus = eventBus
	g.rt = rt

	// Start all adapters.
	for name, adapter := range g.adapters {
		g.logger.Info("starting adapter", "name", name)
		if err := adapter.Start(ctx, eventBus); err != nil {
			return fmt.Errorf("start adapter %s: %w", name, err)
		}
	}

	// Start the agent runtime in a goroutine.
	go func() {
		if err := rt.Run(ctx); err != nil && ctx.Err() == nil {
			g.logger.Error("agent runtime exited with error", "error", err)
		}
	}()

	// Route responses from runtime back to adapters.
	go g.routeResponses(ctx)

	// Start HTTP server for health/status.
	g.server = &http.Server{
		Addr:         g.config.ListenAddr,
		Handler:      g.httpHandler(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		g.logger.Info("HTTP server starting", "addr", g.config.ListenAddr)
		if err := g.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			g.logger.Error("HTTP server error", "error", err)
		}
	}()

	// Publish startup event.
	eventBus.Publish(ctx, bus.Event{
		Type:    bus.TypeSystemStartup,
		Payload: bus.MustJSON(map[string]string{"version": "0.1.0"}),
	})

	return nil
}

// Stop gracefully shuts down the gateway.
func (g *Gateway) Stop(ctx context.Context) error {
	g.logger.Info("gateway shutting down")

	// Publish shutdown event.
	if g.bus != nil {
		g.bus.Publish(ctx, bus.Event{
			Type:    bus.TypeSystemShutdown,
			Payload: bus.MustJSON(map[string]string{"reason": "shutdown"}),
		})
	}

	// Stop HTTP server.
	if g.server != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		g.server.Shutdown(shutdownCtx)
	}

	// Stop adapters.
	for name, adapter := range g.adapters {
		g.logger.Info("stopping adapter", "name", name)
		if err := adapter.Stop(); err != nil {
			g.logger.Error("failed to stop adapter", "name", name, "error", err)
		}
	}

	// Close bus.
	if g.bus != nil {
		g.bus.Close()
	}

	// Compact storage (don't close — main.go owns the lifecycle).
	if g.db != nil {
		g.db.Compact(ctx, g.config.DBMaxMB)
	}

	return nil
}

// routeResponses reads from the runtime response channel and sends via adapters.
func (g *Gateway) routeResponses(ctx context.Context) {
	respCh := g.rt.ResponseChan()
	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-respCh:
			if !ok {
				return
			}
			g.mu.RLock()
			adapter, exists := g.adapters[resp.Channel]
			g.mu.RUnlock()

			if !exists {
				g.logger.Warn("no adapter for response channel", "channel", resp.Channel)
				continue
			}

			if err := adapter.Send(ctx, channels.OutboundMessage{
				To:   resp.To,
				Text: resp.Text,
			}); err != nil {
				g.logger.Error("failed to send response", "channel", resp.Channel, "error", err)
			}
		}
	}
}

// adapterNames returns the names of all registered adapters.
func (g *Gateway) adapterNames() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, 0, len(g.adapters))
	for name := range g.adapters {
		names = append(names, name)
	}
	return names
}

// requireAuth wraps an HTTP handler with Bearer token authentication.
// Local requests (127.0.0.1, ::1, 192.168.x.x, 10.x.x.x) always pass through
// so the setup wizard and local dashboard work without credentials.
// External (tunnel) requests require a valid Bearer token.
func (g *Gateway) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Always allow local network access.
		if isLocalRequest(r) {
			next(w, r)
			return
		}
		if g.config.APIKey == "" {
			// No key generated yet — block external access.
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) || auth[len(prefix):] != g.config.APIKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// httpHandler returns the HTTP handler for health and status endpoints.
func (g *Gateway) httpHandler() http.Handler {
	mux := http.NewServeMux()

	// Health check — stable contract.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		version := "0.1.0"
		if g.appRef != nil {
			version = g.appRef.AppVersion()
		}
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
		})
	})

	// Status API — stable contract.
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		lastID, _ := g.bus.LastID(r.Context())
		version := "0.1.0"
		if g.appRef != nil {
			version = g.appRef.AppVersion()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"version":       version,
			"adapters":      g.adapterNames(),
			"last_event_id": lastID,
		})
	})

	// Google OAuth API endpoints (for dashboard).
	if g.oauthClient != nil {
		mux.HandleFunc("/api/google/status", g.requireAuth(g.handleGoogleStatus))
		mux.HandleFunc("/api/google/connect", g.requireAuth(g.handleGoogleConnect))
	}

	// Register skills API and UI if registry is available.
	if g.skillRegistry != nil {
		skillsAPI := NewSkillsAPI(g.skillRegistry, g.config.SkillsDir, g.skillHub)
		skillsAPI.RegisterRoutes(mux, g.requireAuth)

		skillsUI := NewSkillsUI(g.skillRegistry, g.config.APIKey)
		skillsUI.RegisterRoutes(mux)

		g.logger.Info("skills API and UI registered", "skills_dir", g.config.SkillsDir)
	}

	// Dashboard: replaces the old minimal "/" page.
	if g.appRef != nil {
		dashUI := NewDashboardUI(g.appRef.AppVersion(), g.config.APIKey)
		dashUI.RegisterRoutes(mux)

		dashAPI := NewDashboardAPI(g.db, g.bus, g.appRef, g.adapterNames, g.logger)
		dashAPI.RegisterRoutes(mux, g.requireAuth)

		g.logger.Info("dashboard registered")
	}

	// Phone endpoints — registered when Twilio is configured.
	g.mu.RLock()
	phoneAdapter := g.phoneAdapter
	g.mu.RUnlock()
	if phoneAdapter != nil {
		mux.HandleFunc("/phone/twiml", phoneAdapter.HandleTwiML)
		mux.HandleFunc("/phone/ws", phoneAdapter.HandleWebSocket)
		g.logger.Info("phone endpoints registered", "twiml", "/phone/twiml", "ws", "/phone/ws")
	}

	if g.appRef == nil {
		// Fallback if no app accessor (shouldn't happen in normal use).
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Crayfish</title>
<style>body{font-family:system-ui;max-width:600px;margin:50px auto;padding:20px;text-align:center}
h1{color:#d63031}a{color:#0984e3}</style></head>
<body><h1>Crayfish is running</h1>
<p>Your AI assistant is ready.</p>
<p><a href="/skills">Manage Skills</a> &middot; <a href="/health">Health Check</a></p>
</body></html>`)
		})
	}

	return mux
}

// handleGoogleStatus returns the current Google OAuth connection state.
func (g *Gateway) handleGoogleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	connected := false
	var scopes []string
	if g.appRef != nil {
		cfg := g.appRef.DashboardConfig()
		if _, ok := cfg["google_connected"]; ok {
			connected, _ = cfg["google_connected"].(bool)
		}
		if s, ok := cfg["google_scopes"]; ok {
			scopes, _ = s.([]string)
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"connected": connected,
		"scopes":    scopes,
	})
}

// handleGoogleConnect initiates the device authorization flow.
// Returns the user_code and verification_url for the user to complete on their phone.
// Then polls in a goroutine until the user completes consent.
func (g *Gateway) handleGoogleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	dc, err := g.oauthClient.RequestDeviceCode(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Poll in background goroutine — the HTTP response returns immediately
	// with the user code so the dashboard can display it.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(dc.ExpiresIn)*time.Second)
		defer cancel()

		tok, err := g.oauthClient.PollForToken(ctx, dc)
		if err != nil {
			g.logger.Error("Google OAuth device flow failed", "error", err)
			return
		}

		g.logger.Info("Google account connected via dashboard")
		if g.onOAuthComplete != nil {
			g.onOAuthComplete(*tok)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"user_code":        dc.UserCode,
		"verification_url": dc.VerificationURL,
		"expires_in":       dc.ExpiresIn,
	})
}
