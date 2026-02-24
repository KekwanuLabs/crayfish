// Package gateway implements the Crayfish Gateway — the always-on edge control plane.
// It wires together channel adapters, the bus, runtime, and provides HTTP/WebSocket APIs.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/channels"
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
	config        Config
	db            *storage.DB
	bus           bus.Bus
	rt            *runtime.Runtime
	adapters      map[string]channels.ChannelAdapter
	skillRegistry *skills.Registry
	appRef        AppAccessor
	server        *http.Server
	logger        *slog.Logger
	mu            sync.RWMutex
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

// SetAppAccessor sets the app accessor for the dashboard.
func (g *Gateway) SetAppAccessor(a AppAccessor) {
	g.appRef = a
}

// RegisterAdapter adds a channel adapter to the gateway.
func (g *Gateway) RegisterAdapter(adapter channels.ChannelAdapter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.adapters[adapter.Name()] = adapter
	g.logger.Info("adapter registered", "name", adapter.Name())
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

	// Register skills API and UI if registry is available.
	if g.skillRegistry != nil {
		skillsAPI := NewSkillsAPI(g.skillRegistry, g.config.SkillsDir)
		skillsAPI.RegisterRoutes(mux)

		skillsUI := NewSkillsUI(g.skillRegistry)
		skillsUI.RegisterRoutes(mux)

		g.logger.Info("skills API and UI registered", "skills_dir", g.config.SkillsDir)
	}

	// Dashboard: replaces the old minimal "/" page.
	if g.appRef != nil {
		dashUI := NewDashboardUI(g.appRef.AppVersion())
		dashUI.RegisterRoutes(mux)

		dashAPI := NewDashboardAPI(g.db, g.bus, g.appRef, g.adapterNames, g.logger)
		dashAPI.RegisterRoutes(mux)

		g.logger.Info("dashboard registered")
	} else {
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
