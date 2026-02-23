package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/calendar"
	"github.com/KekwanuLabs/crayfish/internal/channels"
	"github.com/KekwanuLabs/crayfish/internal/channels/cli"
	"github.com/KekwanuLabs/crayfish/internal/channels/telegram"
	"github.com/KekwanuLabs/crayfish/internal/gateway"
	"github.com/KekwanuLabs/crayfish/internal/gmail"
	"github.com/KekwanuLabs/crayfish/internal/heartbeat"
	"github.com/KekwanuLabs/crayfish/internal/provider"
	"github.com/KekwanuLabs/crayfish/internal/voice"
	"github.com/KekwanuLabs/crayfish/internal/queue"
	"github.com/KekwanuLabs/crayfish/internal/runtime"
	"github.com/KekwanuLabs/crayfish/internal/security"
	"github.com/KekwanuLabs/crayfish/internal/skills"
	"github.com/KekwanuLabs/crayfish/internal/storage"
	"github.com/KekwanuLabs/crayfish/internal/tools"
	"github.com/KekwanuLabs/crayfish/internal/updater"
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

	// Lifecycle
	cancel context.CancelFunc
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

	// 14. Memory components
	memExtractor := runtime.NewMemoryExtractor(db.Inner(), llm,
		a.Logger.With("component", "memory_extractor"))
	memRetriever := runtime.NewMemoryRetriever(db.Inner(),
		a.Logger.With("component", "memory_retriever"))

	// 15. Session continuity (snapshot manager)
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

	// 16. Agent runtime
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
		snapshotMgr, a.Config.SessionResumeMinutes,
		a.Logger.With("component", "runtime"))

	// 15. Gateway
	skillsDir := "skills" // Default to local directory for user-created skills
	if _, err := os.Stat("/var/lib/crayfish"); err == nil {
		skillsDir = "/var/lib/crayfish/skills"
	}

	a.gateway = gateway.New(gateway.Config{
		ListenAddr: a.Config.ListenAddr,
		DBMaxMB:    500,
		SkillsDir:  skillsDir,
	}, db, a.Logger.With("component", "gateway"))

	// Wire skill registry to gateway for API and web UI.
	a.gateway.SetSkillRegistry(a.skillRegistry)

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
