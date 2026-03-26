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

	"github.com/KekwanuLabs/crayfish/internal/agents"
	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/device"
	"github.com/KekwanuLabs/crayfish/internal/calendar"
	"github.com/KekwanuLabs/crayfish/internal/channels"
	"github.com/KekwanuLabs/crayfish/internal/channels/cli"
	"github.com/KekwanuLabs/crayfish/internal/channels/phone"
	"github.com/KekwanuLabs/crayfish/internal/channels/telegram"
	"github.com/KekwanuLabs/crayfish/internal/tunnel"
	"github.com/KekwanuLabs/crayfish/internal/drive"
	"github.com/KekwanuLabs/crayfish/internal/gateway"
	"github.com/KekwanuLabs/crayfish/internal/gmail"
	"github.com/KekwanuLabs/crayfish/internal/heartbeat"
	"github.com/KekwanuLabs/crayfish/internal/identity"
	"github.com/KekwanuLabs/crayfish/internal/mcp"
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
	emailMu        sync.RWMutex        // Protects emailProvider
	heartbeatSvc   *heartbeat.Service
	proactiveAgent *agents.ProactiveAgent
	offlineQueue   *queue.OfflineQueue
	pairing        *security.PairingService
	skillRegistry  *skills.Registry
	skillScheduler *skills.Scheduler
	hubSyncer      *skills.HubSyncer
	autoUpdater    *updater.Updater
	voiceInstaller    *voice.Installer
	ttsInstaller      *voice.TTSInstaller

	// Identity system
	identityStore *identity.Store

	// MCP
	mcpMgr *mcp.Manager

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

	// Auto-generate dashboard API key on first run if none is configured.
	// This secures the dashboard without requiring manual setup.
	if a.Config.DashboardAPIKey == "" {
		key, err := generateAPIKey()
		if err == nil {
			a.Config.DashboardAPIKey = key
			if saveErr := a.Config.SaveConfig(); saveErr == nil {
				a.Logger.Info("dashboard API key generated (first run)",
					"key", key,
					"hint", "Add 'Authorization: Bearer "+key+"' header to access the dashboard remotely")
			}
		}
	}

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
	devInfo := device.Detect()
	if a.Config.TelegramToken != "" {
		tgAdapter = telegram.New(a.Config.TelegramToken, a.Logger.With("component", "telegram"))
		adapterMap[tgAdapter.Name()] = tgAdapter

		// Wire up STT for voice message transcription.
		if a.Config.STTEnabled {
			if devInfo.CanRunLocalSTT() {
				// Local whisper.cpp is viable on this hardware.
				sttEngine := voice.NewSTT(voice.STTConfig{
					Enabled:   true,
					ModelPath: a.Config.STTModelPath,
				}, a.Logger.With("component", "stt"))
				if sttEngine.STTEnabled() {
					tgAdapter.SetSTT(sttEngine)
					a.Logger.Info("voice transcription enabled for Telegram")
				}
			} else {
				// Local STT is too slow on this hardware (e.g. Pi 2, ARMv7).
				// Try cloud STT: auto-detect from LLM provider first, then explicit key.
				a.Logger.Info("local STT not supported on this hardware — trying cloud STT",
					"arch", devInfo.Arch, "arm_model", devInfo.ArmModel, "device", devInfo.String())
				a.wireCloudSTT(tgAdapter)
			}
		}

		// Wire up TTS for voice responses.
		// Priority: ElevenLabs (cloud, any hardware) > piper (local, fast hardware only).
		if a.Config.ElevenLabsAPIKey != "" {
			elEngine := voice.NewElevenLabsEngine(
				a.Config.ElevenLabsAPIKey,
				a.Config.ElevenLabsVoiceID,
				a.Logger.With("component", "elevenlabs"),
			)
			tgAdapter.SetTTS(elEngine)
			a.Logger.Info("text-to-speech enabled for Telegram (ElevenLabs)",
				"voice_id", a.Config.ElevenLabsVoiceID)
		} else if a.Config.VoiceEnabled && devInfo.CanRunLocalTTS() {
			// Try the configured model first; fall back to hardware-recommended model.
			modelName := a.Config.VoiceModel
			ttsEngine := voice.New(voice.Config{
				Enabled:   true,
				ModelName: modelName,
			}, a.Logger.With("component", "tts"))
			if !ttsEngine.Enabled() && modelName != "" {
				rec := voice.RecommendedPiperModel()
				if rec != modelName {
					ttsEngine = voice.New(voice.Config{
						Enabled:   true,
						ModelName: rec,
					}, a.Logger.With("component", "tts"))
					if ttsEngine.Enabled() {
						modelName = rec
						// Persist the actual model so restarts are consistent.
						a.Config.VoiceModel = rec
						_ = a.Config.SaveConfig()
					}
				}
			}
			if ttsEngine.Enabled() {
				tgAdapter.SetTTS(ttsEngine)
				a.Logger.Info("text-to-speech enabled for Telegram", "model", modelName)
			} else {
				a.Logger.Info("piper not yet installed; background installer will activate TTS")
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

	// Always register stt_connect so users can set up cloud voice transcription conversationally.
	tools.RegisterSTTConnectTool(toolReg, tools.STTConnectDeps{
		ProviderName: a.Config.Provider,
		IsConfigured: func() bool {
			a.configMu.RLock()
			defer a.configMu.RUnlock()
			// Configured if we have an explicit STT key OR the LLM provider supports Whisper.
			if a.Config.STTAPIKey != "" {
				return true
			}
			return voice.WhisperEndpointForProvider(a.Config.Provider, a.Config.Endpoint) != ""
		},
		TryReuseProviderKey: func() bool {
			// Attempt to activate STT using the existing LLM provider key.
			if tgAdapter == nil {
				return false
			}
			endpoint := voice.WhisperEndpointForProvider(a.Config.Provider, a.Config.Endpoint)
			if endpoint == "" || a.Config.APIKey == "" {
				return false
			}
			whisperSTT := voice.NewWhisperAPI(endpoint, a.Config.APIKey, a.Logger.With("component", "whisper-api"))
			tgAdapter.SetSTT(whisperSTT)
			a.Logger.Info("cloud STT activated using existing provider key", "provider", a.Config.Provider)
			return true
		},
		SaveKey: func(key string) {
			a.configMu.Lock()
			a.Config.STTAPIKey = key
			a.configMu.Unlock()
			if err := a.Config.SaveConfig(); err != nil {
				a.Logger.Warn("failed to save STT API key to config", "error", err)
			}
			a.Logger.Info("STT API key saved")
		},
		ActivateSTT: func(endpoint, key string) {
			if tgAdapter == nil {
				return
			}
			whisperSTT := voice.NewWhisperAPI(endpoint, key, a.Logger.With("component", "whisper-api"))
			tgAdapter.SetSTT(whisperSTT)
			a.Logger.Info("cloud STT activated via stt_connect", "endpoint", endpoint)
		},
	})

	// Always register voice_connect so users can set up ElevenLabs conversationally.
	tools.RegisterVoiceConnectTool(toolReg, tools.VoiceConnectDeps{
		IsConfigured: func() bool {
			a.configMu.RLock()
			defer a.configMu.RUnlock()
			return a.Config.ElevenLabsAPIKey != ""
		},
		SaveConfig: func(apiKey, voiceID string) {
			a.configMu.Lock()
			a.Config.ElevenLabsAPIKey = apiKey
			if voiceID != "" {
				a.Config.ElevenLabsVoiceID = voiceID
			}
			a.configMu.Unlock()
			if err := a.Config.SaveConfig(); err != nil {
				a.Logger.Warn("failed to save ElevenLabs config", "error", err)
			}
			a.Logger.Info("ElevenLabs config saved", "voice_id", voiceID)
		},
		ActivateTTS: func(apiKey, voiceID string) {
			if tgAdapter == nil {
				return
			}
			elEngine := voice.NewElevenLabsEngine(apiKey, voiceID, a.Logger.With("component", "elevenlabs"))
			tgAdapter.SetTTS(elEngine)
			a.Logger.Info("ElevenLabs TTS hot-activated", "voice_id", voiceID)
		},
	})

	// 6b. Phone/Twilio — ConversationRelay channel + call tools.
	var phoneAdapter *phone.Adapter
	if a.Config.TwilioAccountSID != "" {
		phoneAdapter = phone.New(phone.Config{
			TwilioAccountSID:  a.Config.TwilioAccountSID,
			TwilioAuthToken:   a.Config.TwilioAuthToken,
			TwilioFromNumber:  a.Config.TwilioFromNumber,
			TunnelURL:         a.Config.TunnelURL,
			ElevenLabsVoiceID: a.Config.ElevenLabsVoiceID,
			SystemPrompt:      "", // filled after runtime config is built below
		}, a.llm, a.Logger.With("component", "phone"))
		tools.RegisterCallTools(toolReg, phoneAdapter)
		a.Logger.Info("phone channel ready", "from", a.Config.TwilioFromNumber)
	}

	// Always register twilio_connect so users can set up phone calls conversationally.
	tools.RegisterTwilioConnectTool(toolReg, tools.TwilioConnectDeps{
		IsConfigured: func() bool {
			a.configMu.RLock()
			defer a.configMu.RUnlock()
			return a.Config.TwilioAccountSID != ""
		},
		SaveCreds: func(accountSID, authToken, fromNumber, tunnelURL string) {
			a.configMu.Lock()
			a.Config.TwilioAccountSID = accountSID
			a.Config.TwilioAuthToken = authToken
			a.Config.TwilioFromNumber = fromNumber
			if tunnelURL != "" {
				a.Config.TunnelURL = tunnelURL
			}
			a.configMu.Unlock()
			if err := a.Config.SaveConfig(); err != nil {
				a.Logger.Warn("failed to save Twilio config", "error", err)
			}
		},
		ActivatePhone: func(accountSID, authToken, fromNumber, tunnelURL string) {
			if phoneAdapter != nil {
				// Phone adapter already exists — update config in-place.
				return
			}
			phoneAdapter = phone.New(phone.Config{
				TwilioAccountSID: accountSID,
				TwilioAuthToken:  authToken,
				TwilioFromNumber: fromNumber,
				TunnelURL:        tunnelURL,
				ElevenLabsVoiceID: a.Config.ElevenLabsVoiceID,
			}, a.llm, a.Logger.With("component", "phone"))
			tools.RegisterCallTools(toolReg, phoneAdapter)
			a.gateway.RegisterPhoneAdapter(phoneAdapter)
			if err := phoneAdapter.Start(ctx, a.bus); err != nil {
				a.Logger.Warn("phone adapter start failed", "error", err)
			}
			a.Logger.Info("phone channel hot-activated", "from", fromNumber)
		},
	})

	// 6c. Amadeus flight tools
	if a.Config.AmadeusClientID != "" && a.Config.AmadeusClientSecret != "" {
		auth := tools.NewAmadeusAuth(a.Config.AmadeusClientID, a.Config.AmadeusClientSecret)
		tools.RegisterAmadeusTools(toolReg, auth)
	}

	// Always register amadeus_connect so users can set up flight search conversationally.
	tools.RegisterAmadeusConnectTool(toolReg, tools.AmadeusConnectDeps{
		IsConfigured: func() bool {
			a.configMu.RLock()
			defer a.configMu.RUnlock()
			return a.Config.AmadeusClientID != "" && a.Config.AmadeusClientSecret != ""
		},
		SaveCreds: func(clientID, clientSecret string) {
			a.configMu.Lock()
			a.Config.AmadeusClientID = clientID
			a.Config.AmadeusClientSecret = clientSecret
			a.configMu.Unlock()
			if err := a.Config.SaveConfig(); err != nil {
				a.Logger.Warn("failed to save Amadeus credentials to config", "error", err)
			}
			if a.rt != nil {
				a.rt.SetTravelSearchEnabled(true)
			}
			a.Logger.Info("Amadeus credentials saved and activated")
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

	// 7a. Email provider — priority: OAuth Gmail > IMAP+SMTP > email_connect tool
	emailViaApp := false

	// Auto-discover email from Google OAuth if not configured.
	// Uses the UserInfo API (only needs userinfo.email scope, not Gmail scopes).
	if a.oauthClient != nil && a.googleToken != nil && a.googleToken.RefreshToken != "" && a.Config.GmailUser == "" {
		accessToken, err := a.oauthClient.ValidAccessToken(ctx, a.googleToken)
		if err == nil {
			if email, err := oauth.GetUserEmail(ctx, accessToken); err == nil {
				a.Config.GmailUser = email
				if saveErr := a.Config.SaveConfig(); saveErr != nil {
					a.Logger.Warn("failed to persist discovered email", "error", saveErr)
				}
				a.Logger.Info("auto-discovered email from Google OAuth", "email", email)
			} else {
				a.Logger.Warn("failed to discover email from OAuth", "error", err)
			}
		}
	}

	// Gmail via OAuth REST API (requires Gmail scopes on the token).
	if a.oauthClient != nil && a.googleToken != nil && a.googleToken.RefreshToken != "" &&
		a.Config.GmailUser != "" && a.oauthClient.HasScope(a.googleToken, oauth.GmailModify) {
		tokenProvider := func(ctx context.Context) (string, error) {
			a.googleMu.RLock()
			tok := a.googleToken
			a.googleMu.RUnlock()
			return a.oauthClient.ValidAccessToken(ctx, tok)
		}
		apiClient := gmail.NewAPIClient(tokenProvider)

		a.gmailPoller = gmail.NewPoller(gmail.Config{
			Email:        a.Config.GmailUser,
			PollInterval: a.Config.GmailPollInterval(),
		}, apiClient, db.Inner(), a.Logger.With("component", "gmail"))

		a.emailMu.Lock()
		a.emailProvider = a.gmailPoller
		a.emailMu.Unlock()

		if err := a.gmailPoller.Start(ctx); err != nil {
			a.Logger.Error("Gmail poller failed to start", "error", err)
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
		tools.RegisterEmailTools(toolReg, a.emailProvider, db.Inner())
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

	// Wire Drive/Docs tools if scopes already present on the loaded token.
	if a.oauthClient != nil && a.googleToken != nil && a.googleToken.RefreshToken != "" {
		if hasScope(a.googleToken, oauth.DriveScope) {
			driveTP := func(ctx context.Context) (string, error) {
				a.googleMu.RLock()
				t := a.googleToken
				a.googleMu.RUnlock()
				return a.oauthClient.ValidAccessToken(ctx, t)
			}
			tools.RegisterDriveTools(toolReg, drive.NewClient(driveTP))
			a.Logger.Info("drive/docs tools registered")
		}
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

				// Auto-discover email via UserInfo API (only needs userinfo.email scope).
				discoverCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				accessToken, err := tokenProvider(discoverCtx)
				if err == nil {
					if email, err := oauth.GetUserEmail(discoverCtx, accessToken); err == nil && email != "" {
						a.configMu.Lock()
						if a.Config.GmailUser == "" {
							a.Config.GmailUser = email
						}
						a.configMu.Unlock()
						if err := a.Config.SaveConfig(); err != nil {
							a.Logger.Warn("failed to save discovered email", "error", err)
						}
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

				// Hot-reload Drive/Docs tools if those scopes were just granted.
				if hasScope(&tok, oauth.DriveScope) {
					tools.RegisterDriveTools(toolReg, drive.NewClient(tokenProvider))
					a.Logger.Info("drive/docs tools hot-reloaded after OAuth")
				}

				// Update runtime flags.
				if a.rt != nil {
					a.rt.SetGoogleConnected(true)
					a.rt.SetGoogleGrantedScopes(tok.Scopes)
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

	// 7d. Heartbeat service (needs email provider + calendar client)
	a.emailMu.RLock()
	hasEmailProvider := a.emailProvider != nil
	a.emailMu.RUnlock()

	// Hoist notifyFunc so the proactive agent can reuse it outside the heartbeat block.
	var proactiveNotify func(ctx context.Context, message string) error

	if hasEmailProvider || calendarClient != nil {
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
			proactiveNotify = notifyFunc
		}

		a.emailMu.RLock()
		ep := a.emailProvider
		a.emailMu.RUnlock()

		// LLM closure for auto-reply — enriches with identity context.
		llmComplete := func(ctx context.Context, system, user string) (string, error) {
			resp, err := llm.Complete(ctx, provider.CompletionRequest{
				Messages: []provider.Message{
					{Role: provider.RoleSystem, Content: system},
					{Role: provider.RoleUser, Content: user},
				},
				MaxTokens: 512,
			})
			if err != nil {
				return "", err
			}
			return resp.Content, nil
		}

		a.heartbeatSvc = heartbeat.NewService(heartbeat.ServiceDeps{
			Config:           a.Config.HeartbeatConfig(),
			Email:            ep,
			Calendar:         calendarClient,
			Notify:           notifyFunc,
			DB:               db.Inner(),
			LLMComplete:      llmComplete,
			SelfEmail:        a.Config.GmailUser,
			AutoReplyEnabled: a.Config.IsAutoReplyEnabled(),
			Logger:           a.Logger.With("component", "heartbeat"),
		})
		if err := a.heartbeatSvc.Start(ctx); err != nil {
			a.Logger.Error("Heartbeat service failed to start", "error", err)
		}

		// Wire sync callbacks for real-time urgency detection, auto-reply, and proactive evaluation.
		syncCallback := func(ctx context.Context, newIDs []string) {
			if a.heartbeatSvc != nil {
				a.heartbeatSvc.CheckNewEmails(ctx, newIDs)
				a.heartbeatSvc.CheckAutoReply(ctx, newIDs)
			}

			if a.proactiveAgent != nil && ep != nil {
				sessionID := ""
				if tgAdapter != nil {
					if chatID := tgAdapter.GetOperatorChatID(); chatID != 0 {
						sessionID = fmt.Sprintf("telegram:%d", chatID)
					}
				}

				// Process sequentially to avoid overwhelming the Pi with
				// concurrent TLS handshakes to the Anthropic API.
				go func(ids []string, sid string) {
					for _, id := range ids {
						email, err := ep.GetEmailByID(ctx, id)
						if err != nil {
							continue
						}

						opp := &agents.Opportunity{
							ID:          email.ID,
							Type:        "email_highlight",
							Title:       email.Subject,
							Description: fmt.Sprintf("From: %s\n\n%s", email.From, email.BodyPreview),
							RelatedTo:   email.Subject,
							Confidence:  0.5,
						}

						evalCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
						if err := a.proactiveAgent.EvaluateAndNotify(evalCtx, sid, opp); err != nil {
							a.Logger.Debug("proactive eval failed", "email", opp.ID, "error", err)
						}
						cancel()
					}
				}(newIDs, sessionID)
			}
		}
		if a.gmailPoller != nil {
			a.gmailPoller.SetOnSyncComplete(syncCallback)
		}
		// Also set on IMAP provider if it's separate from the Gmail poller.
		a.emailMu.RLock()
		if imapProv, ok := a.emailProvider.(*gmail.IMAPProvider); ok {
			imapProv.SetOnSyncComplete(syncCallback)
		}
		a.emailMu.RUnlock()
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
		"skills",                   // relative to working directory
		"/var/lib/crayfish/skills", // system install location
		"/etc/crayfish/skills",     // config location
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

	// 10b. Conversational settings tool
	tools.RegisterSettingsTool(toolReg, tools.SettingsDeps{
		GetSettings: func() map[string]any {
			a.configMu.RLock()
			defer a.configMu.RUnlock()

			weekdaysOnly := true
			if a.Config.HeartbeatWeekdaysOnly != nil {
				weekdaysOnly = *a.Config.HeartbeatWeekdaysOnly
			}

			keywords := a.Config.UrgencyKeywords
			if len(keywords) == 0 {
				keywords = heartbeat.DefaultUrgencyKeywords
			}

			interval := a.Config.HeartbeatIntervalMins
			if interval == 0 {
				interval = 30
			}

			workStart := a.Config.HeartbeatWorkHourStart
			if workStart == 0 {
				workStart = 9
			}

			workEnd := a.Config.HeartbeatWorkHourEnd
			if workEnd == 0 {
				workEnd = 18
			}

			return map[string]any{
				"heartbeat_interval_minutes": interval,
				"heartbeat_work_hour_start":  workStart,
				"heartbeat_work_hour_end":    workEnd,
				"heartbeat_weekdays_only":    weekdaysOnly,
				"urgency_keywords":           keywords,
				"auto_reply_enabled":         a.Config.IsAutoReplyEnabled(),
			}
		},
		UpdateSettings: func(updates map[string]any) error {
			a.configMu.Lock()

			for key, val := range updates {
				switch key {
				case "heartbeat_interval_minutes":
					if v, ok := val.(int); ok {
						a.Config.HeartbeatIntervalMins = v
					}
				case "heartbeat_work_hour_start":
					if v, ok := val.(int); ok {
						a.Config.HeartbeatWorkHourStart = v
					}
				case "heartbeat_work_hour_end":
					if v, ok := val.(int); ok {
						a.Config.HeartbeatWorkHourEnd = v
					}
				case "heartbeat_weekdays_only":
					if v, ok := val.(bool); ok {
						a.Config.HeartbeatWeekdaysOnly = &v
					}
				case "urgency_keywords":
					if v, ok := val.([]string); ok {
						a.Config.UrgencyKeywords = v
					}
				case "auto_reply_enabled":
					if v, ok := val.(bool); ok {
						a.Config.AutoReplyEnabled = &v
					}
				}
			}

			a.configMu.Unlock()

			if err := a.Config.SaveConfig(); err != nil {
				a.Logger.Warn("failed to save settings", "error", err)
			}

			// Hot-reload heartbeat config.
			if a.heartbeatSvc != nil {
				a.configMu.RLock()
				newCfg := a.Config.HeartbeatConfig()
				autoReply := a.Config.IsAutoReplyEnabled()
				a.configMu.RUnlock()
				a.heartbeatSvc.UpdateConfig(newCfg)
				a.heartbeatSvc.SetAutoReplyEnabled(autoReply)
			}

			return nil
		},
	})

	// 10c. Hub syncer — auto-sync skills from hub on startup + every 6 hours
	a.hubSyncer = skills.NewHubSyncer(hubClient, a.skillRegistry, skillsDir,
		a.Logger.With("component", "hub-syncer"))
	a.hubSyncer.Start(ctx)

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
			if proactiveNotify != nil {
				proactiveNotify(context.Background(), "🔄 "+msg)
			}
		}, a.Logger.With("component", "updater"))
		a.autoUpdater.Start(ctx)
	}

	// 13. Voice STT auto-installer (runs in background, local hardware only)
	if a.Config.STTEnabled && devInfo.CanRunLocalSTT() {
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
				// Re-enable STT now that whisper is installed.
				// Use installer's model path if not explicitly configured.
				modelPath := a.Config.STTModelPath
				if modelPath == "" {
					modelPath = a.voiceInstaller.ModelPath(a.voiceInstaller.RecommendedModel())
				}
				sttEngine := voice.NewSTT(voice.STTConfig{
					Enabled:   true,
					ModelPath: modelPath,
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

	// 14. TTS auto-installer (runs in background when piper isn't already present).
	// Skip when ElevenLabs is configured — no need to install piper too.
	if a.Config.VoiceEnabled && tgAdapter != nil && devInfo.CanRunLocalTTS() && a.Config.ElevenLabsAPIKey == "" {
		a.ttsInstaller = voice.NewTTSInstaller(
			voice.DefaultTTSInstallerConfig(),
			a.Logger.With("component", "tts-installer"),
		)
		if !a.ttsInstaller.IsInstalled() && voice.CanInstallTTS() {
			go func() {
				// Stagger from the STT installer to avoid competing downloads on Pi.
				time.Sleep(30 * time.Second)
				a.Logger.Info("starting background piper TTS setup")
				if err := a.ttsInstaller.Install(ctx); err != nil {
					a.Logger.Warn("piper TTS setup failed (non-fatal)", "error", err)
					return
				}
				// Hot-enable TTS now that piper is installed.
				// Use the recommended model for this hardware (may differ from config default).
				model := a.ttsInstaller.RecommendedModel()
				ttsEngine := voice.New(voice.Config{
					Enabled:   true,
					ModelName: model,
				}, a.Logger.With("component", "tts"))
				if ttsEngine.Enabled() {
					tgAdapter.SetTTS(ttsEngine)
					a.Logger.Info("text-to-speech enabled for Telegram (post-install)",
						"model", model)
					// Persist the correct model name so restarts use the right model.
					if a.Config.VoiceModel != model {
						a.Config.VoiceModel = model
						if err := a.Config.SaveConfig(); err != nil {
							a.Logger.Warn("could not save updated voice model to config", "error", err)
						}
					}
				}
			}()
		} else if a.ttsInstaller.IsInstalled() {
			a.Logger.Info("piper TTS already installed")
		}
	}

	// 15. Identity system (SOUL.md + USER.md)
	configDir := filepath.Dir(a.Config.ConfigPath)
	a.identityStore = identity.NewStore(configDir, a.Logger.With("component", "identity"))
	tools.RegisterIdentityTools(toolReg, a.identityStore)

	// 14b. MCP servers (power-user extension)
	if len(a.Config.MCPServers) > 0 {
		a.mcpMgr = mcp.NewManager(a.Logger.With("component", "mcp"))
		for _, srv := range a.Config.MCPServers {
			if !srv.Enabled {
				continue
			}
			if err := a.mcpMgr.Connect(ctx, mcp.ServerConfig{
				Name:    srv.Name,
				Command: srv.Command,
				Enabled: srv.Enabled,
			}); err != nil {
				a.Logger.Warn("failed to connect MCP server", "name", srv.Name, "error", err)
			}
		}
		if len(a.mcpMgr.Servers()) > 0 {
			tools.RegisterMCPTools(toolReg, a.mcpMgr)
			a.Logger.Info("MCP servers connected", "count", len(a.mcpMgr.Servers()))
		}
	}

	// 15. Memory components
	memExtractor := runtime.NewMemoryExtractor(db.Inner(), llm,
		a.Logger.With("component", "memory_extractor"))
	memRetriever := runtime.NewMemoryRetriever(db.Inner(),
		a.Logger.With("component", "memory_retriever"))

	// 15b. Proactive agent
	proactiveLLM := func(ctx context.Context, system, user string) (string, error) {
		resp, err := llm.Complete(ctx, provider.CompletionRequest{
			Messages: []provider.Message{
				{Role: provider.RoleSystem, Content: system},
				{Role: provider.RoleUser, Content: user},
			},
			MaxTokens: 512,
		})
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
	a.proactiveAgent = agents.NewProactiveAgent(agents.ProactiveAgentDeps{
		Memory:      memRetriever,
		DB:          db.Inner(),
		LLMComplete: proactiveLLM,
		Notify:      proactiveNotify,
		Logger:      a.Logger.With("component", "proactive-agent"),
	})
	tools.RegisterProactiveTools(toolReg, a.proactiveAgent)

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
	if a.googleToken != nil {
		rtCfg.GoogleGrantedScopes = a.googleToken.Scopes
	}
	rtCfg.WebSearchEnabled = a.Config.BraveAPIKey != ""
	rtCfg.TravelSearchEnabled = a.Config.AmadeusClientID != "" && a.Config.AmadeusClientSecret != ""
	rtCfg.PhoneEnabled = a.Config.TwilioAccountSID != ""
	rtCfg.Timezone = a.Config.Timezone
	a.emailMu.RLock()
	rtCfg.EmailEnabled = a.emailProvider != nil
	a.emailMu.RUnlock()
	rtCfg.EmailViaApp = emailViaApp

	// Create skill engine for prompt augmentations.
	skillEngine := skills.NewEngine(a.skillRegistry, a.Logger.With("component", "skill-engine"))

	rt := runtime.New(rtCfg, a.bus, db.Inner(), llm, a.sessions, toolReg,
		a.offlineQueue, a.pairing, memExtractor, memRetriever,
		snapshotMgr, a.identityStore, skillEngine, a.Config.SessionResumeMinutes,
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

			// Auto-discover email if not yet known.
			discoverCtx, discoverCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer discoverCancel()
			if accessToken, err := tokenProvider(discoverCtx); err == nil {
				if email, err := oauth.GetUserEmail(discoverCtx, accessToken); err == nil && email != "" {
					a.configMu.Lock()
					if a.Config.GmailUser == "" {
						a.Config.GmailUser = email
					}
					a.configMu.Unlock()
					_ = a.Config.SaveConfig()
				}
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

			// Hot-reload Drive/Docs tools if those scopes were granted via dashboard.
			a.googleMu.RLock()
			dashTok := a.googleToken
			a.googleMu.RUnlock()
			if hasScope(dashTok, oauth.DriveScope) {
				tools.RegisterDriveTools(toolReg, drive.NewClient(tokenProvider))
				a.Logger.Info("drive/docs tools hot-reloaded after dashboard OAuth")
			}

			if a.rt != nil {
				a.rt.SetGoogleConnected(true)
				if dashTok != nil {
					a.rt.SetGoogleGrantedScopes(dashTok.Scopes)
				}
			}

			a.Logger.Info("Google account connected via dashboard — calendar tools activated")
		})
	}

	a.gateway.RegisterAdapter(cliAdapter)
	if tgAdapter != nil {
		a.gateway.RegisterAdapter(tgAdapter)
	}
	if phoneAdapter != nil {
		a.gateway.RegisterPhoneAdapter(phoneAdapter)
		if err := phoneAdapter.Start(ctx, a.bus); err != nil {
			a.Logger.Warn("phone adapter start failed (non-fatal)", "error", err)
		}
	}

	// Auto-manage Cloudflare Tunnel when Twilio is configured.
	// Starts cloudflared, parses the URL, and updates the Twilio webhook automatically.
	// Runs in background; re-runs on crash with retry loop.
	if a.Config.TwilioAccountSID != "" && tunnel.IsAvailable() {
		go a.runTunnelManager(ctx, phoneAdapter)
	} else if a.Config.TwilioAccountSID != "" && !tunnel.IsAvailable() {
		a.Logger.Warn("cloudflared not installed — phone calls need a public URL",
			"hint", "Install with: curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-arm -o /usr/local/bin/cloudflared && chmod +x /usr/local/bin/cloudflared")
	}

	if err := a.gateway.Start(ctx, rt, a.bus); err != nil {
		cancel()
		return fmt.Errorf("start gateway: %w", err)
	}

	a.Logger.Info("Crayfish ready",
		"listen", a.Config.ListenAddr,
		"provider", llm.Name(),
		"model", llm.Model(),
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

	if a.hubSyncer != nil {
		a.hubSyncer.Stop()
	}

	if a.autoUpdater != nil {
		a.autoUpdater.Stop()
	}

	if a.mcpMgr != nil {
		a.mcpMgr.Close()
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
		"gmail_app_password":     mask(a.Config.GmailAppPassword),
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
		"amadeus_connected":      a.Config.AmadeusClientID != "" && a.Config.AmadeusClientSecret != "",
		// Phone & Tunnel
		"twilio_account_sid":  mask(a.Config.TwilioAccountSID),
		"twilio_from_number":  a.Config.TwilioFromNumber,
		"tunnel_url":          a.Config.TunnelURL,
		"phone_configured":    a.Config.TwilioAccountSID != "",
		"tunnel_type":         tunnelType(a.Config.TunnelURL),
		"dashboard_api_key":   mask(a.Config.DashboardAPIKey),
	}
}

// tunnelType returns "named" if the URL was explicitly set (stable), "quick" if auto-assigned.
func tunnelType(url string) string {
	if url == "" {
		return "none"
	}
	if strings.Contains(url, "trycloudflare.com") {
		return "quick"
	}
	return "named"
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
			if s, ok := val.(string); ok && !strings.Contains(s, "****") {
				s = strings.ReplaceAll(s, " ", "") // Google displays app passwords with spaces; strip them
				if s != a.Config.GmailAppPassword {
					a.Config.GmailAppPassword = s
					changed = true
				}
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

	// If tunnel_url was updated, persist and re-sync Twilio webhook immediately.
	if newURL, ok := updates["tunnel_url"].(string); ok && newURL != "" {
		a.Config.TunnelURL = newURL
		_ = a.Config.SaveConfig()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			twimlURL := strings.TrimSuffix(newURL, "/") + "/phone/twiml"
			if err := phone.UpdateWebhook(ctx, a.Config.TwilioAccountSID,
				a.Config.TwilioAuthToken, a.Config.TwilioFromNumber, twimlURL); err != nil {
				a.Logger.Warn("Twilio webhook update failed after tunnel URL change", "error", err)
			} else {
				a.Logger.Info("Twilio webhook updated from dashboard", "url", twimlURL)
			}
		}()
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

// hasScope reports whether an OAuth token contains the given scope.
func hasScope(tok *oauth.Token, scope string) bool {
	if tok == nil {
		return false
	}
	for _, s := range tok.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// wireCloudSTT activates cloud-based STT (Whisper API) on the Telegram adapter.
// Priority: (1) auto-detect from LLM provider, (2) explicit stt_api_key in config.
func (a *App) wireCloudSTT(tgAdapter *telegram.Adapter) {
	if tgAdapter == nil {
		return
	}

	// 1. Try to reuse the existing LLM provider key (zero setup for openai/groq users).
	endpoint := voice.WhisperEndpointForProvider(a.Config.Provider, a.Config.Endpoint)
	if endpoint != "" && a.Config.APIKey != "" {
		whisperSTT := voice.NewWhisperAPI(endpoint, a.Config.APIKey, a.Logger.With("component", "whisper-api"))
		tgAdapter.SetSTT(whisperSTT)
		a.Logger.Info("cloud STT auto-enabled using LLM provider key", "provider", a.Config.Provider)
		return
	}

	// 2. Fall back to an explicitly configured STT key.
	// Detect Groq vs OpenAI from the key prefix (Groq keys start with "gsk_").
	if a.Config.STTAPIKey != "" {
		sttEndpoint := voice.OpenAIWhisperEndpoint
		if strings.HasPrefix(a.Config.STTAPIKey, "gsk_") {
			sttEndpoint = voice.GroqWhisperEndpoint
		}
		whisperSTT := voice.NewWhisperAPI(sttEndpoint, a.Config.STTAPIKey, a.Logger.With("component", "whisper-api"))
		tgAdapter.SetSTT(whisperSTT)
		a.Logger.Info("cloud STT enabled using stt_api_key config", "endpoint", sttEndpoint)
		return
	}

	a.Logger.Info("cloud STT not configured — voice messages won't be transcribed; ask the assistant to set it up")
}

// cleanSnapshots periodically removes old session snapshots.
// runTunnelManager starts and supervises the Cloudflare Tunnel.
// When a URL is assigned (or reassigned after restart), it immediately
// updates the Twilio webhook — retrying until it succeeds.
// Any failure is logged AND sent to the user via Telegram so they know.
func (a *App) runTunnelManager(ctx context.Context, phoneAdapter *phone.Adapter) {
	notify := func(msg string) {
		a.Logger.Warn("tunnel", "msg", msg)
		// Push to Telegram so the user knows something happened.
		if a.gateway != nil {
			_ = a.gateway.NotifyOperator(ctx, "📡 "+msg)
		}
	}

	mgr := tunnel.New("http://localhost:8119", func(tunnelURL string) {
		a.Logger.Info("tunnel URL assigned", "url", tunnelURL)

		// Update config so TwiML uses the new URL.
		a.configMu.Lock()
		a.Config.TunnelURL = tunnelURL
		a.configMu.Unlock()
		_ = a.Config.SaveConfig()

		if phoneAdapter != nil {
			phoneAdapter.UpdateTunnelURL(tunnelURL)
		}

		// Update Twilio webhook — retry with backoff until it succeeds.
		twimlURL := tunnelURL + "/phone/twiml"
		a.Logger.Info("updating Twilio webhook", "url", twimlURL)

		var lastErr error
		for attempt := 1; attempt <= 10; attempt++ {
			updateCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := phone.UpdateWebhook(updateCtx, a.Config.TwilioAccountSID,
				a.Config.TwilioAuthToken, a.Config.TwilioFromNumber, twimlURL)
			cancel()

			if err == nil {
				a.Logger.Info("Twilio webhook updated — phone calls ready", "url", twimlURL)
				notify(fmt.Sprintf("Phone calls ready. Tunnel: %s", tunnelURL))
				return
			}

			lastErr = err
			a.Logger.Warn("Twilio webhook update failed, retrying",
				"attempt", attempt, "error", err)

			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt*attempt) * time.Second): // exponential backoff
			}
		}

		// All retries exhausted.
		notify(fmt.Sprintf("⚠️ Tunnel is up (%s) but Twilio webhook update failed after 10 attempts: %v — phone calls may not work. Check your Twilio credentials.", tunnelURL, lastErr))
	}, a.Logger.With("component", "tunnel"))

	mgr.Start(ctx)
}

// generateAPIKey creates a cryptographically random 32-character hex API key.
func generateAPIKey() (string, error) {
	b := make([]byte, 16)
	if _, err := crypto_rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

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
