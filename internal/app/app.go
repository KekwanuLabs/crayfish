package app

import (
	"context"
	crypto_rand "crypto/rand"
	"database/sql"
	"encoding/hex"
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
	"github.com/KekwanuLabs/crayfish/internal/oauth"
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
	emailProvider  gmail.EmailProvider // Active email provider (OAuth poller or IMAP)
	emailMu        sync.RWMutex       // Protects emailProvider
	heartbeatSvc   *heartbeat.Service
	offlineQueue   *queue.OfflineQueue
	pairing        *security.PairingService
	skillRegistry  *skills.Registry
	skillScheduler *skills.Scheduler
	autoUpdater    *updater.Updater
	voiceInstaller *voice.Installer

	// Identity system
	identityStore *identity.Store

	// Google OAuth
	oauthClient *oauth.Client
	googleToken *oauth.Token
	googleMu    sync.RWMutex

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

	var tgAdapter *telegram.Adapter
	if a.Config.TelegramToken != "" {
		tgAdapter = telegram.New(a.Config.TelegramToken, a.Logger.With("component", "telegram"))
		adapterMap[tgAdapter.Name()] = tgAdapter

		// Wire up STT for voice message transcription
		if a.Config.STTEnabled {
			sttEngine := voice.NewSTT(voice.STTConfig{
				Enabled:   true,
				ModelPath: a.Config.STTModelPath,
			}, a.Logger.With("component", "stt"))
			if sttEngine.STTEnabled() {
				tgAdapter.SetSTT(sttEngine)
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

	// Always register brave_connect so users can set up web search conversationally.
	tools.RegisterBraveConnectTool(toolReg, tools.BraveConnectDeps{
		IsConfigured: func() bool {
			a.configMu.RLock()
			defer a.configMu.RUnlock()
			return a.Config.BraveAPIKey != ""
		},
		SaveKey: func(key string) {
			a.configMu.Lock()
			a.Config.BraveAPIKey = key
			a.configMu.Unlock()
			if err := a.Config.SaveConfig(); err != nil {
				a.Logger.Warn("failed to save Brave API key to config", "error", err)
			}
			a.Logger.Info("Brave Search API key saved and activated")
		},
		Registry: toolReg,
	})

	// 7. Google OAuth client — build-time credentials (ldflags) are the source of truth
	// from the maintainer. Config file is a persistence layer for when the binary
	// doesn't have them (auto-updater, curl|bash installs). Self-hosters who set
	// config values without ldflags get their config respected.
	googleClientID := a.Config.GoogleClientID
	googleClientSecret := a.Config.GoogleClientSecret
	if oauth.CrayfishClientID != "" {
		// Build-time credentials always win — maintainer may have rotated keys.
		googleClientID = oauth.CrayfishClientID
		googleClientSecret = oauth.CrayfishClientSecret
	}

	// Persist build-time credentials to config so they survive binary replacement.
	if oauth.CrayfishClientID != "" && oauth.CrayfishClientID != a.Config.GoogleClientID {
		a.Config.GoogleClientID = oauth.CrayfishClientID
		a.Config.GoogleClientSecret = oauth.CrayfishClientSecret
		if err := a.Config.SaveConfig(); err != nil {
			a.Logger.Warn("failed to persist Google credentials to config", "error", err)
		} else {
			a.Logger.Info("Google OAuth credentials persisted to config file")
		}
	}

	if googleClientID == "" || googleClientSecret == "" {
		a.Logger.Info("Google OAuth disabled — no client credentials configured")
	} else {
		a.oauthClient = oauth.NewClient(oauth.Config{
			ClientID:     googleClientID,
			ClientSecret: googleClientSecret,
			Scopes:       oauth.ScopesBase,
		}, func(tok oauth.Token) {
			a.saveGoogleToken(tok)
		})
	}

	// Load existing Google token if available.
	a.googleToken = a.loadGoogleToken()

	// 7a. Email provider — priority: OAuth > IMAP+SMTP > email_connect tool
	emailViaApp := false

	if a.oauthClient != nil && a.googleToken != nil && a.googleToken.RefreshToken != "" {
		// OAuth path: Gmail REST API.
		tokenProvider := func(ctx context.Context) (string, error) {
			a.googleMu.RLock()
			tok := a.googleToken
			a.googleMu.RUnlock()
			return a.oauthClient.ValidAccessToken(ctx, tok)
		}
		apiClient := gmail.NewAPIClient(tokenProvider)

		// Auto-discover email from OAuth profile if not configured.
		gmailUser := a.Config.GmailUser
		if gmailUser == "" {
			if email, err := apiClient.GetProfile(ctx); err == nil {
				gmailUser = email
			}
		}

		if gmailUser != "" {
			a.gmailPoller = gmail.NewPoller(gmail.Config{
				Email:        gmailUser,
				PollInterval: a.Config.GmailPollInterval(),
			}, apiClient, db.Inner(), a.Logger.With("component", "gmail"))

			a.emailMu.Lock()
			a.emailProvider = a.gmailPoller
			a.emailMu.Unlock()

			if err := a.gmailPoller.Start(ctx); err != nil {
				a.Logger.Error("Gmail poller failed to start", "error", err)
			}
		}
	} else if a.Config.GmailUser != "" && a.Config.GmailAppPassword != "" {
		// IMAP+SMTP path: App Password.
		imapProvider := gmail.NewIMAPProvider(gmail.IMAPConfig{
			Email:        a.Config.GmailUser,
			AppPassword:  a.Config.GmailAppPassword,
			PollInterval: a.Config.GmailPollInterval(),
		}, db.Inner(), a.Logger.With("component", "email-imap"))

		a.emailMu.Lock()
		a.emailProvider = imapProvider
		a.emailMu.Unlock()
		emailViaApp = true

		if err := imapProvider.Start(ctx); err != nil {
			a.Logger.Error("IMAP email provider failed to start", "error", err)
		}
	}

	a.emailMu.RLock()
	hasEmail := a.emailProvider != nil
	a.emailMu.RUnlock()
	if hasEmail {
		a.emailMu.RLock()
		tools.RegisterEmailTools(toolReg, a.emailProvider)
		a.emailMu.RUnlock()
	}

	// Always register email_connect so users can set up email conversationally.
	tools.RegisterEmailConnectTool(toolReg, tools.EmailConnectDeps{
		IsConfigured: func() bool {
			a.emailMu.RLock()
			defer a.emailMu.RUnlock()
			return a.emailProvider != nil
		},
		SaveCreds: func(email, appPassword string) {
			a.configMu.Lock()
			a.Config.GmailUser = email
			a.Config.GmailAppPassword = appPassword
			a.configMu.Unlock()
			if err := a.Config.SaveConfig(); err != nil {
				a.Logger.Warn("failed to save email credentials to config", "error", err)
			}
			a.Logger.Info("email credentials saved and activated", "email", email)
		},
		StartProvider: func(provider gmail.EmailProvider) {
			go provider.Start(ctx)
		},
		Registry: toolReg,
		DB:       db.Inner(),
		Logger:   a.Logger.With("component", "email-imap"),
		OnConnected: func(provider gmail.EmailProvider) {
			a.emailMu.Lock()
			a.emailProvider = provider
			a.emailMu.Unlock()
			if a.rt != nil {
				a.rt.SetEmailEnabled(true, true)
			}
		},
	})

	// 7b. Calendar — prefer OAuth, fall back to App Password
	var calendarClient *calendar.Client
	if a.oauthClient != nil && a.googleToken != nil && a.googleToken.RefreshToken != "" && a.Config.GmailUser != "" {
		// OAuth path: use Bearer tokens for CalDAV.
		currentToken := a.googleToken
		calendarClient = calendar.NewOAuthClient(
			a.Config.GmailUser,
			func(ctx context.Context) (string, error) {
				a.googleMu.RLock()
				tok := a.googleToken
				a.googleMu.RUnlock()
				return a.oauthClient.ValidAccessToken(ctx, tok)
			},
			a.Logger.With("component", "calendar"),
		)
		a.Logger.Info("calendar using OAuth", "email", a.Config.GmailUser,
			"scopes", currentToken.Scopes)
	} else if a.Config.GmailUser != "" && a.Config.GmailAppPassword != "" {
		// Legacy path: App Password for CalDAV.
		calendarClient = calendar.NewClient(
			a.Config.GmailUser,
			a.Config.GmailAppPassword,
			a.Logger.With("component", "calendar"),
		)
		a.Logger.Info("calendar using App Password", "email", a.Config.GmailUser)
	}

	if calendarClient != nil {
		tools.RegisterCalendarTools(toolReg, calendarClient)
	}

	// 7c. Google OAuth tools (registered only when credentials are available)
	if a.oauthClient != nil {
		tools.RegisterGoogleTools(toolReg, tools.GoogleToolsDeps{
			OAuthClient: a.oauthClient,
			GetToken: func() *oauth.Token {
				a.googleMu.RLock()
				defer a.googleMu.RUnlock()
				return a.googleToken
			},
			OnTokenReceived: func(tok oauth.Token) {
				a.saveGoogleToken(tok)

				// Hot-reload: auto-discover email and register calendar tools immediately.
				tokenProvider := func(ctx context.Context) (string, error) {
					a.googleMu.RLock()
					t := a.googleToken
					a.googleMu.RUnlock()
					return a.oauthClient.ValidAccessToken(ctx, t)
				}

				// Auto-discover email via OAuth profile.
				discoverCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				apiClient := gmail.NewAPIClient(tokenProvider)
				if email, err := apiClient.GetProfile(discoverCtx); err == nil && email != "" {
					a.configMu.Lock()
					if a.Config.GmailUser == "" {
						a.Config.GmailUser = email
					}
					a.configMu.Unlock()
					if err := a.Config.SaveConfig(); err != nil {
						a.Logger.Warn("failed to save discovered email", "error", err)
					}
				}

				// Register calendar tools with the new token.
				a.configMu.RLock()
				gmailUser := a.Config.GmailUser
				a.configMu.RUnlock()

				if gmailUser != "" {
					calClient := calendar.NewOAuthClient(
						gmailUser,
						tokenProvider,
						a.Logger.With("component", "calendar"),
					)
					tools.RegisterCalendarTools(toolReg, calClient)
					a.Logger.Info("calendar tools hot-reloaded after OAuth", "email", gmailUser)
				}

				// Update runtime flags.
				if a.rt != nil {
					a.rt.SetGoogleConnected(true)
				}

				a.Logger.Info("Google account connected — calendar tools activated")
			},
			IsConnected: func() bool {
				a.googleMu.RLock()
				defer a.googleMu.RUnlock()
				return a.googleToken != nil && a.googleToken.RefreshToken != ""
			},
			GetScopes: func() []string {
				a.googleMu.RLock()
				defer a.googleMu.RUnlock()
				if a.googleToken == nil {
					return nil
				}
				return a.googleToken.Scopes
			},
		})
	}

	// 7d. Heartbeat service (needs Gmail poller + calendar client)
	if a.gmailPoller != nil || calendarClient != nil {
		var notifyFunc heartbeat.NotifyFunc
		if tgAdapter != nil {
			notifyFunc = func(ctx context.Context, message string) error {
				chatID := tgAdapter.GetOperatorChatID()
				if chatID != 0 {
					sessionID := fmt.Sprintf("telegram:%d", chatID)
					_, err := db.Inner().ExecContext(ctx,
						"INSERT INTO messages (session_id, role, content, created_at) VALUES (?, ?, ?, datetime('now'))",
						sessionID, "assistant", message)
					if err != nil {
						a.Logger.Warn("failed to persist heartbeat notification",
							"error", err, "session_id", sessionID)
					}

					a.bus.Publish(ctx, bus.Event{
						Type:      bus.TypeMessageOutbound,
						Channel:   "telegram",
						SessionID: sessionID,
						Payload: bus.MustJSON(bus.OutboundMessage{
							To:   fmt.Sprintf("%d", chatID),
							Text: message,
						}),
					})
				}
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

	// Determine skills directory (also used by gateway later).
	skillsDir := "skills" // Default to local directory for user-created skills
	if _, err := os.Stat("/var/lib/crayfish"); err == nil {
		skillsDir = "/var/lib/crayfish/skills"
	}

	// 10a. Skill Hub client + conversational tools
	hubClient := skills.NewHubClient(
		"https://raw.githubusercontent.com/KekwanuLabs/crayfish-skills/main/index.json",
		a.Logger.With("component", "skill-hub"),
	)
	tools.RegisterSkillTools(toolReg, tools.SkillToolDeps{
		Registry:  a.skillRegistry,
		SkillsDir: skillsDir,
		Hub:       hubClient,
	})

	// 11. Skill scheduler
	a.skillScheduler = skills.NewScheduler(a.skillRegistry, func(ctx context.Context, skill *skills.Skill) {
		a.Logger.Info("scheduled skill triggered", "skill", skill.Name)
		// Inject synthetic message through the bus so the runtime processes it.
		a.bus.Publish(ctx, bus.Event{
			Type:    bus.TypeMessageInbound,
			Channel: "system",
			Payload: bus.MustJSON(bus.InboundMessage{
				From: "scheduler",
				Text: skill.Trigger.Command,
			}),
		})
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
					return
				}
				// Re-enable STT now that whisper is installed
				sttEngine := voice.NewSTT(voice.STTConfig{
					Enabled:   true,
					ModelPath: a.Config.STTModelPath,
				}, a.Logger.With("component", "stt"))
				if sttEngine.STTEnabled() && tgAdapter != nil {
					tgAdapter.SetSTT(sttEngine)
					a.Logger.Info("voice transcription enabled for Telegram (post-install)")
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
	rtCfg.GoogleConnected = a.googleToken != nil && a.googleToken.RefreshToken != ""
	rtCfg.WebSearchEnabled = a.Config.BraveAPIKey != ""
	a.emailMu.RLock()
	rtCfg.EmailEnabled = a.emailProvider != nil
	a.emailMu.RUnlock()
	rtCfg.EmailViaApp = emailViaApp

	rt := runtime.New(rtCfg, a.bus, db.Inner(), llm, a.sessions, toolReg,
		a.offlineQueue, a.pairing, memExtractor, memRetriever,
		snapshotMgr, a.identityStore, a.Config.SessionResumeMinutes,
		a.Logger.With("component", "runtime"))
	a.rt = rt

	// 18. Gateway
	// Generate dashboard API key on first run.
	if a.Config.DashboardAPIKey == "" {
		key := make([]byte, 32)
		crypto_rand.Read(key)
		a.Config.DashboardAPIKey = hex.EncodeToString(key)
		if err := a.Config.SaveConfig(); err != nil {
			a.Logger.Warn("failed to save generated dashboard API key", "error", err)
		}
		a.Logger.Info("generated dashboard API key", "key", a.Config.DashboardAPIKey)
	}

	a.gateway = gateway.New(gateway.Config{
		ListenAddr: a.Config.ListenAddr,
		DBMaxMB:    500,
		SkillsDir:  skillsDir,
		APIKey:     a.Config.DashboardAPIKey,
	}, db, a.Logger.With("component", "gateway"))

	// Wire skill registry, hub client, and app accessor to gateway for API, web UI, and dashboard.
	a.gateway.SetSkillRegistry(a.skillRegistry)
	a.gateway.SetSkillHub(hubClient)
	a.gateway.SetAppAccessor(a)
	if a.oauthClient != nil {
		a.gateway.SetOAuthClient(a.oauthClient, func(tok oauth.Token) {
			a.saveGoogleToken(tok)

			// Hot-reload calendar tools from dashboard OAuth flow too.
			tokenProvider := func(ctx context.Context) (string, error) {
				a.googleMu.RLock()
				t := a.googleToken
				a.googleMu.RUnlock()
				return a.oauthClient.ValidAccessToken(ctx, t)
			}

			a.configMu.RLock()
			gmailUser := a.Config.GmailUser
			a.configMu.RUnlock()

			if gmailUser != "" {
				calClient := calendar.NewOAuthClient(
					gmailUser,
					tokenProvider,
					a.Logger.With("component", "calendar"),
				)
				tools.RegisterCalendarTools(toolReg, calClient)
			}

			if a.rt != nil {
				a.rt.SetGoogleConnected(true)
			}

			a.Logger.Info("Google account connected via dashboard — calendar tools activated")
		})
	}

	a.gateway.RegisterAdapter(cliAdapter)
	if tgAdapter != nil {
		a.gateway.RegisterAdapter(tgAdapter)
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

	// Stop IMAP provider if it's separate from the Gmail poller.
	a.emailMu.RLock()
	ep := a.emailProvider
	a.emailMu.RUnlock()
	if ep != nil && ep != a.gmailPoller {
		if err := ep.Stop(); err != nil {
			a.Logger.Error("email provider stop error", "error", err)
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

	// Cancel context first — signals goroutines to stop before we close the DB.
	if a.cancel != nil {
		a.cancel()
	}
	time.Sleep(500 * time.Millisecond)

	if a.db != nil {
		a.db.Close()
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
		"gmail_poll_minutes":     a.Config.GmailPollMinutes,
		"brave_api_key":          mask(a.Config.BraveAPIKey),
		"system_prompt":          a.Config.SystemPrompt,
		"continuity_enabled":     a.Config.ContinuityEnabled,
		"session_resume_minutes": a.Config.SessionResumeMinutes,
		"snapshots_per_session":  a.Config.SnapshotsPerSession,
		"auto_update":            a.Config.AutoUpdate,
		"update_channel":         a.Config.UpdateChannel,
		"google_connected":       a.Config.Google != nil && a.Config.Google.RefreshToken != "",
		"google_scopes":          googleScopes(a.Config.Google),
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

// googleScopes returns the scopes from a GoogleConfig, or nil if not configured.
func googleScopes(g *GoogleConfig) []string {
	if g == nil {
		return nil
	}
	return g.Scopes
}

// loadGoogleToken converts the config's GoogleConfig into an oauth.Token.
func (a *App) loadGoogleToken() *oauth.Token {
	a.configMu.RLock()
	defer a.configMu.RUnlock()

	g := a.Config.Google
	if g == nil || g.RefreshToken == "" {
		return nil
	}

	tok := &oauth.Token{
		AccessToken:  g.AccessToken,
		RefreshToken: g.RefreshToken,
		Scopes:       g.Scopes,
	}

	if g.Expiry != "" {
		if t, err := time.Parse(time.RFC3339, g.Expiry); err == nil {
			tok.Expiry = t
		}
	}

	return tok
}

// saveGoogleToken persists an OAuth token to the config file.
func (a *App) saveGoogleToken(tok oauth.Token) {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	a.Config.Google = &GoogleConfig{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry.Format(time.RFC3339),
		Scopes:       tok.Scopes,
	}

	// Update in-memory token.
	a.googleMu.Lock()
	a.googleToken = &tok
	a.googleMu.Unlock()

	if err := a.Config.SaveConfig(); err != nil {
		a.Logger.Warn("failed to save Google token to config", "error", err)
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
