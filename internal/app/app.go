package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/calendar"
	"github.com/KekwanuLabs/crayfish/internal/channels"
	"github.com/KekwanuLabs/crayfish/internal/channels/cli"
	"github.com/KekwanuLabs/crayfish/internal/channels/telegram"
	"github.com/KekwanuLabs/crayfish/internal/gateway"
	"github.com/KekwanuLabs/crayfish/internal/gmail"
	"github.com/KekwanuLabs/crayfish/internal/heartbeat"
	"github.com/KekwanuLabs/crayfish/internal/identity"
	"github.com/KekwanuLabs/crayfish/internal/provider"
	"github.com/KekwanuLabs/crayfish/internal/queue"
	"github.com/KekwanuLabs/crayfish/internal/runtime"
	"github.com/KekwanuLabs/crayfish/internal/security"
	"github.com/KekwanuLabs/crayfish/internal/skills"
	"github.com/KekwanuLabs/crayfish/internal/storage"
	"github.com/KekwanuLabs/crayfish/internal/tools"
	"github.com/KekwanuLabs/crayfish/internal/updater"
	"github.com/KekwanuLabs/crayfish/internal/voice"
)

// App is the top-level Crayfish application. It owns all components and their lifecycle.
type App struct {
	Config  Config
	Version string // e.g., "0.4.0-dev"
	Logger  *slog.Logger

	// Core infrastructure
	db       *storage.DB
	bus      *bus.SQLiteBus
	sessions *security.SessionStore
	llm      provider.Provider

	// Components
	gateway        *gateway.Gateway
	gmailPoller    *gmail.Poller
	heartbeatSvc   *heartbeat.Service
	offlineQueue   *queue.OfflineQueue
	pairing        *security.PairingService
	skillRegistry  *skills.Registry
	skillScheduler *skills.Scheduler
	autoUpdater    *updater.Updater
	voiceInstaller *voice.Installer

	// Identity system
	identityStore *identity.Store

	// Runtime reference for hot-reload
	rt *runtime.Runtime

	// Lifecycle
	StartedAt time.Time
	configMu  sync.RWMutex
	cancel    context.CancelFunc
}

// New creates a new Crayfish application with the given config.
func New(cfg Config, version string, logger *slog.Logger) *App {
	return &App{
		Config:  cfg,
		Version: version,
		Logger:  logger,
	}
}

// DB returns the underlying sql.DB for direct access.
func (a *App) DB() *sql.DB {
	if a.db == nil {
		return nil
	}
	return a.db.Inner()
}

// Start initializes all components and begins processing.
func (a *App) Start(ctx context.Context) error {
	a.StartedAt = time.Now()
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	// 1. Storage
	db, err := storage.Open(ctx, a.Config.DBPath, a.Logger.With("component", "storage"))
	if err != nil {
		cancel()
		return fmt.Errorf("open storage: %w", err)
	}
	a.db = db

	// 2. Event bus
	a.bus = bus.NewSQLiteBus(db.Inner(), a.Logger.With("component", "bus"))

	// 3. Session store
	a.sessions = security.NewSessionStore(db.Inner(), a.Logger.With("component", "sessions"))

	// 4. LLM provider
	provCfg := provider.ProviderConfig{
		Provider:  a.Config.Provider,
		APIKey:    a.Config.APIKey,
		Endpoint:  a.Config.Endpoint,
		Model:     a.Config.Model,
		MaxTokens: a.Config.MaxTokens,
	}
	llm, err := provider.CreateProvider(provCfg, a.Logger.With("component", "provider"))
	if err != nil {
		cancel()
		return fmt.Errorf("create provider: %w", err)
	}
	a.llm = llm

	// 5. Channel adapters
	cliAdapter := cli.New(a.Logger.With("component", "cli"))
	adapterMap := map[string]channels.ChannelAdapter{
		cliAdapter.Name(): cliAdapter,
	}

	if a.Config.TelegramToken != "" {
		tg := telegram.New(a.Config.TelegramToken, a.Logger.With("component", "telegram"))
		adapterMap[tg.Name()] = tg

		// Wire up STT for voice message transcription
		if a.Config.STTEnabled {
			sttEngine := voice.NewSTT(voice.STTConfig{
				Enabled:   true,
				ModelPath: a.Config.STTModelPath,
			}, a.Logger.With("component", "stt"))
			if sttEngine.STTEnabled() {
				tg.SetSTT(sttEngine)
				a.Logger.Info("voice transcription enabled for Telegram")
			}
		}
	}

	// 6. Tool registry
	toolReg := tools.NewRegistry(a.Logger.With("component", "tools"))
	tools.RegisterBuiltins(toolReg, adapterMap, db.Inner())

	if a.Config.BraveAPIKey != "" {
		tools.RegisterSearchTools(toolReg, tools.BraveSearchConfig{APIKey: a.Config.BraveAPIKey})
	}

	// 7. Gmail (optional)
	if a.Config.GmailUser != "" && a.Config.GmailAppPassword != "" {
		a.gmailPoller = gmail.NewPoller(gmail.Config{
			Email:        a.Config.GmailUser,
			AppPassword:  a.Config.GmailAppPassword,
			PollInterval: a.Config.GmailPollInterval(),
		}, db.Inner(), a.Logger.With("component", "gmail"))

		tools.RegisterEmailTools(toolReg, a.gmailPoller)

		// Calendar uses the same credentials as Gmail (App Password works for CalDAV too)
		calendarClient := calendar.NewClient(
			a.Config.GmailUser,
			a.Config.GmailAppPassword,
			a.Logger.With("component", "calendar"),
		)
		tools.RegisterCalendarTools(toolReg, calendarClient)

		if err := a.gmailPoller.Start(ctx); err != nil {
			a.Logger.Error("Gmail poller failed to start", "error", err)
			// Non-fatal
		}

		// Heartbeat service - proactive check-ins
		// Find the Telegram adapter for notifications
		var notifyFunc heartbeat.NotifyFunc
		if tgAdapter, ok := adapterMap["telegram"].(*telegram.Adapter); ok {
			notifyFunc = func(ctx context.Context, message string) error {
				return tgAdapter.SendToOperator(ctx, message)
			}
		}

		a.heartbeatSvc = heartbeat.NewService(
			heartbeat.DefaultConfig(),
			a.gmailPoller,
			calendarClient,
			notifyFunc,
			a.Logger.With("component", "heartbeat"),
		)
		if err := a.heartbeatSvc.Start(ctx); err != nil {
			a.Logger.Error("Heartbeat service failed to start", "error", err)
		}
	}

	// 8. Offline queue
	a.offlineQueue = queue.New(db.Inner(), a.Logger.With("component", "queue"))
	a.offlineQueue.RegisterProcessor(func(ctx context.Context, item queue.QueueItem) error {
		_, err := a.bus.Publish(ctx, bus.Event{
			Type:      item.EventType,
			Channel:   item.Channel,
			SessionID: item.SessionID,
			Payload:   item.Payload,
		})
		return err
	})
	if err := a.offlineQueue.Start(ctx); err != nil {
		a.Logger.Error("offline queue failed to start", "error", err)
	}

	// 9. Pairing service
	a.pairing = security.NewPairingService(db.Inner(), a.sessions, a.Logger.With("component", "pairing"))
	go a.cleanExpiredOTPs(ctx)

	// 10. Skills system
	a.skillRegistry = skills.NewRegistry(a.Logger.With("component", "skills"))

	// Load skills from standard directories.
	skillDirs := []string{
		"skills",                      // relative to working directory
		"/var/lib/crayfish/skills",    // system install location
		"/etc/crayfish/skills",        // config location
	}
	for _, dir := range skillDirs {
		a.skillRegistry.LoadFromDir(dir)
	}

	if a.skillRegistry.Count() > 0 {
		a.Logger.Info("skills loaded", "count", a.skillRegistry.Count())
	}

	// 11. Skill scheduler
	a.skillScheduler = skills.NewScheduler(a.skillRegistry, func(ctx context.Context, skill *skills.Skill) {
		a.Logger.Info("scheduled skill triggered", "skill", skill.Name)
		// TODO: execute via runtime when skill engine is wired to agent runtime
	}, a.Logger.With("component", "skill-scheduler"))
	a.skillScheduler.Start(ctx)

	// 12. Auto-updater
	if a.Config.AutoUpdate {
		a.autoUpdater = updater.New(updater.Config{
			Enabled:        true,
			Channel:        a.Config.UpdateChannel,
			CurrentVersion: a.Version,
		}, func(msg string) {
			a.Logger.Info("updater", "message", msg)
			// TODO: notify via Telegram when channel adapter supports it
		}, a.Logger.With("component", "updater"))
		a.autoUpdater.Start(ctx)
	}

	// 13. Voice STT auto-installer (runs in background)
	if a.Config.STTEnabled {
		a.voiceInstaller = voice.NewInstaller(
			voice.DefaultInstallerConfig(),
			a.Logger.With("component", "voice-installer"),
		)
		if !a.voiceInstaller.IsInstalled() {
			go func() {
				// Wait a bit for other services to start
				time.Sleep(10 * time.Second)
				a.Logger.Info("starting background voice recognition setup")
				if err := a.voiceInstaller.Install(ctx); err != nil {
					a.Logger.Warn("voice recognition setup failed (non-fatal)", "error", err)
				}
			}()
		} else {
			a.Logger.Info("voice recognition already installed")
		}
	}

	// 14. Identity system (SOUL.md + USER.md)
	configDir := filepath.Dir(a.Config.ConfigPath)
	a.identityStore = identity.NewStore(configDir, a.Logger.With("component", "identity"))
	tools.RegisterIdentityTools(toolReg, a.identityStore)

	// 15. Memory components
	memExtractor := runtime.NewMemoryExtractor(db.Inner(), llm,
		a.Logger.With("component", "memory_extractor"))
	memRetriever := runtime.NewMemoryRetriever(db.Inner(),
		a.Logger.With("component", "memory_retriever"))

	// 16. Session continuity (snapshot manager)
	var snapshotMgr *runtime.SnapshotManager
	if a.Config.ContinuityEnabled {
		snapshotMgr = runtime.NewSnapshotManager(db.Inner(), llm,
			a.Logger.With("component", "snapshot"))

		// Register checkpoint tool
		tools.RegisterCheckpointTool(toolReg, db.Inner(), snapshotMgr)

		// Periodic snapshot cleanup
		snapshotsPerSession := a.Config.SnapshotsPerSession
		if snapshotsPerSession <= 0 {
			snapshotsPerSession = 3
		}
		go a.cleanSnapshots(ctx, snapshotMgr, snapshotsPerSession)

		a.Logger.Info("session continuity enabled",
			"resume_minutes", a.Config.SessionResumeMinutes,
			"snapshots_per_session", snapshotsPerSession)
	}

	// 17. Agent runtime
	rtCfg := runtime.DefaultConfig()
	if a.Config.Name != "" {
		rtCfg.Name = a.Config.Name
	}
	if a.Config.SystemPrompt != "" {
		rtCfg.SystemPrompt = a.Config.SystemPrompt
	}
	if a.Config.Model != "" {
		rtCfg.Model = a.Config.Model
	}
	if a.Config.MaxTokens > 0 {
		rtCfg.MaxTokens = a.Config.MaxTokens
	}
	if a.Config.Personality != "" {
		rtCfg.Personality = a.Config.Personality
	}

	rt := runtime.New(rtCfg, a.bus, db.Inner(), llm, a.sessions, toolReg,
		a.offlineQueue, a.pairing, memExtractor, memRetriever,
		snapshotMgr, a.identityStore, a.Config.SessionResumeMinutes,
		a.Logger.With("component", "runtime"))
	a.rt = rt

	// 18. Gateway
	skillsDir := "skills" // Default to local directory for user-created skills
	if _, err := os.Stat("/var/lib/crayfish"); err == nil {
		skillsDir = "/var/lib/crayfish/skills"
	}

	a.gateway = gateway.New(gateway.Config{
		ListenAddr: a.Config.ListenAddr,
		DBMaxMB:    500,
		SkillsDir:  skillsDir,
	}, db, a.Logger.With("component", "gateway"))

	// Wire skill registry and app accessor to gateway for API, web UI, and dashboard.
	a.gateway.SetSkillRegistry(a.skillRegistry)
	a.gateway.SetAppAccessor(a)

	a.gateway.RegisterAdapter(cliAdapter)
	if a.Config.TelegramToken != "" {
		for _, adapter := range adapterMap {
			if adapter.Name() == "telegram" {
				a.gateway.RegisterAdapter(adapter)
			}
		}
	}

	if err := a.gateway.Start(ctx, rt, a.bus); err != nil {
		cancel()
		return fmt.Errorf("start gateway: %w", err)
	}

	a.Logger.Info("Crayfish ready",
		"listen", a.Config.ListenAddr,
		"provider", llm.Name(),
		"model", a.Config.Model,
	)

	return nil
}

// Stop gracefully shuts down all components.
func (a *App) Stop() error {
	a.Logger.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	// Shutdown order matters: stop accepting traffic first, then drain queues,
	// then background workers, then storage.

	if a.gateway != nil {
		if err := a.gateway.Stop(shutdownCtx); err != nil {
			a.Logger.Error("gateway stop error", "error", err)
		}
	}

	if a.offlineQueue != nil {
		if err := a.offlineQueue.Stop(); err != nil {
			a.Logger.Error("offline queue stop error", "error", err)
		}
	}

	if a.gmailPoller != nil {
		if err := a.gmailPoller.Stop(); err != nil {
			a.Logger.Error("Gmail poller stop error", "error", err)
		}
	}

	if a.heartbeatSvc != nil {
		a.heartbeatSvc.Stop()
	}

	if a.skillScheduler != nil {
		a.skillScheduler.Stop()
	}

	if a.autoUpdater != nil {
		a.autoUpdater.Stop()
	}

	if a.db != nil {
		a.db.Close()
	}

	if a.cancel != nil {
		a.cancel()
	}

	return nil
}

// DashboardConfig returns the current config with secrets masked.
func (a *App) DashboardConfig() map[string]any {
	a.configMu.RLock()
	defer a.configMu.RUnlock()

	mask := func(s string) string {
		if len(s) <= 4 {
			if s == "" {
				return ""
			}
			return "****"
		}
		return s[:4] + strings.Repeat("*", len(s)-4)
	}

	return map[string]any{
		"name":                   a.Config.Name,
		"personality":            a.Config.Personality,
		"db_path":                a.Config.DBPath,
		"listen_addr":            a.Config.ListenAddr,
		"provider":               a.Config.Provider,
		"api_key":                mask(a.Config.APIKey),
		"endpoint":               a.Config.Endpoint,
		"model":                  a.Config.Model,
		"max_tokens":             a.Config.MaxTokens,
		"voice_enabled":          a.Config.VoiceEnabled,
		"voice_model":            a.Config.VoiceModel,
		"stt_enabled":            a.Config.STTEnabled,
		"telegram_token":         mask(a.Config.TelegramToken),
		"gmail_user":             a.Config.GmailUser,
		"gmail_app_password":     mask(a.Config.GmailAppPassword),
		"gmail_poll_minutes":     a.Config.GmailPollMinutes,
		"brave_api_key":          mask(a.Config.BraveAPIKey),
		"system_prompt":          a.Config.SystemPrompt,
		"continuity_enabled":     a.Config.ContinuityEnabled,
		"session_resume_minutes": a.Config.SessionResumeMinutes,
		"snapshots_per_session":  a.Config.SnapshotsPerSession,
		"auto_update":            a.Config.AutoUpdate,
		"update_channel":         a.Config.UpdateChannel,
	}
}

// UpdateConfig applies config updates. Returns true if a restart is needed.
func (a *App) UpdateConfig(updates map[string]any) (bool, error) {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	restartNeeded := false

	// Hot-reloadable fields.
	hotFields := map[string]bool{
		"name": true, "personality": true, "system_prompt": true,
		"continuity_enabled": true, "session_resume_minutes": true,
		"snapshots_per_session": true, "auto_update": true,
		"update_channel": true,
	}

	for key, val := range updates {
		changed := false
		switch key {
		case "name":
			if s, ok := val.(string); ok && s != "" && s != a.Config.Name {
				a.Config.Name = s
				changed = true
			}
		case "personality":
			if s, ok := val.(string); ok && s != "" && s != a.Config.Personality {
				a.Config.Personality = s
				changed = true
			}
		case "system_prompt":
			if s, ok := val.(string); ok && s != a.Config.SystemPrompt {
				a.Config.SystemPrompt = s
				changed = true
			}
		case "api_key":
			if s, ok := val.(string); ok && s != "" && !strings.Contains(s, "****") && s != a.Config.APIKey {
				a.Config.APIKey = s
				changed = true
			}
		case "provider":
			if s, ok := val.(string); ok && s != "" && s != a.Config.Provider {
				a.Config.Provider = s
				changed = true
			}
		case "endpoint":
			if s, ok := val.(string); ok && s != a.Config.Endpoint {
				a.Config.Endpoint = s
				changed = true
			}
		case "model":
			if s, ok := val.(string); ok && s != "" && s != a.Config.Model {
				a.Config.Model = s
				changed = true
			}
		case "max_tokens":
			if f, ok := val.(float64); ok && int(f) != a.Config.MaxTokens {
				a.Config.MaxTokens = int(f)
				changed = true
			}
		case "telegram_token":
			if s, ok := val.(string); ok && !strings.Contains(s, "****") && s != a.Config.TelegramToken {
				a.Config.TelegramToken = s
				changed = true
			}
		case "gmail_user":
			if s, ok := val.(string); ok && s != a.Config.GmailUser {
				a.Config.GmailUser = s
				changed = true
			}
		case "gmail_app_password":
			if s, ok := val.(string); ok && !strings.Contains(s, "****") && s != a.Config.GmailAppPassword {
				a.Config.GmailAppPassword = s
				changed = true
			}
		case "brave_api_key":
			if s, ok := val.(string); ok && !strings.Contains(s, "****") && s != a.Config.BraveAPIKey {
				a.Config.BraveAPIKey = s
				changed = true
			}
		case "listen_addr":
			if s, ok := val.(string); ok && s != "" && s != a.Config.ListenAddr {
				a.Config.ListenAddr = s
				changed = true
			}
		case "continuity_enabled":
			if b, ok := val.(bool); ok && b != a.Config.ContinuityEnabled {
				a.Config.ContinuityEnabled = b
				changed = true
			}
		case "session_resume_minutes":
			if f, ok := val.(float64); ok && int(f) != a.Config.SessionResumeMinutes {
				a.Config.SessionResumeMinutes = int(f)
				changed = true
			}
		case "snapshots_per_session":
			if f, ok := val.(float64); ok && int(f) != a.Config.SnapshotsPerSession {
				a.Config.SnapshotsPerSession = int(f)
				changed = true
			}
		case "auto_update":
			if b, ok := val.(bool); ok && b != a.Config.AutoUpdate {
				a.Config.AutoUpdate = b
				changed = true
			}
		case "update_channel":
			if s, ok := val.(string); ok && s != a.Config.UpdateChannel {
				a.Config.UpdateChannel = s
				changed = true
			}
		}

		if changed && !hotFields[key] {
			restartNeeded = true
		}
	}

	// Save to YAML.
	if err := a.Config.SaveConfig(); err != nil {
		a.Logger.Warn("failed to save config", "error", err)
		// Non-fatal: in-memory config is updated.
	}

	// Hot-reload runtime config.
	if a.rt != nil {
		a.rt.UpdateConfig(a.Config.Name, a.Config.Personality, a.Config.SystemPrompt)
	}

	return restartNeeded, nil
}

// Uptime returns how long the app has been running.
func (a *App) Uptime() time.Duration {
	return time.Since(a.StartedAt)
}

// AppVersion returns the app version string.
func (a *App) AppVersion() string {
	return a.Version
}

// VoiceInstallProgress returns the current voice recognition installation progress, or nil if not active.
func (a *App) VoiceInstallProgress() map[string]any {
	if a.voiceInstaller == nil {
		return nil
	}
	p := a.voiceInstaller.Progress()
	return map[string]any{
		"status":   p.Status.String(),
		"progress": p.Progress,
		"message":  p.Message,
	}
}

// cleanExpiredOTPs periodically removes expired pairing OTPs.
func (a *App) cleanExpiredOTPs(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pairing.CleanExpired(context.Background())
		}
	}
}

// cleanSnapshots periodically removes old session snapshots.
func (a *App) cleanSnapshots(ctx context.Context, mgr *runtime.SnapshotManager, keep int) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := mgr.Cleanup(context.Background(), keep, 7*24*time.Hour); err != nil {
				a.Logger.Warn("snapshot cleanup failed", "error", err)
			}
		}
	}
}
