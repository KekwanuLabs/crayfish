// Package app handles application lifecycle — configuration, wiring, start, and shutdown.
package app

import (
	"log/slog"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for a Crayfish instance.
type Config struct {
	// Identity — Every Crayfish has a name given by its owner.
	// This name is used in all interactions and forms the AI's identity.
	Name        string `yaml:"name"`
	Personality string `yaml:"personality"` // friendly, professional, casual, minimal

	// Storage
	DBPath string `yaml:"db_path"`

	// Network
	ListenAddr string `yaml:"listen_addr"`

	// LLM Provider
	Provider  string `yaml:"provider"`
	APIKey    string `yaml:"api_key"`
	Endpoint  string `yaml:"endpoint"`
	Model     string `yaml:"model"`
	MaxTokens int    `yaml:"max_tokens"`

	// Voice — Local text-to-speech
	VoiceEnabled bool   `yaml:"voice_enabled"`
	VoiceModel   string `yaml:"voice_model"` // Piper voice model name

	// STT — Speech-to-text via whisper.cpp
	STTEnabled   bool   `yaml:"stt_enabled"`
	STTModelPath string `yaml:"stt_model_path"` // Path to whisper model (auto-detect if empty)

	// Channels
	TelegramToken string `yaml:"telegram_token"`

	// Gmail
	GmailUser        string `yaml:"gmail_user"`
	GmailAppPassword string `yaml:"gmail_app_password"`
	GmailPollMinutes int    `yaml:"gmail_poll_minutes"`

	// Search
	BraveAPIKey string `yaml:"brave_api_key"`

	// Runtime
	SystemPrompt string `yaml:"system_prompt"`

	// Session continuity
	ContinuityEnabled    bool `yaml:"continuity_enabled"`     // Enable session snapshot system (default: true)
	SessionResumeMinutes int  `yaml:"session_resume_minutes"` // Idle gap threshold for snapshot injection (default: 30)
	SnapshotsPerSession  int  `yaml:"snapshots_per_session"`  // Max snapshots retained per session (default: 3)

	// Updates
	AutoUpdate    bool   `yaml:"auto_update"`
	UpdateChannel string `yaml:"update_channel"` // "stable" or "beta"
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Name:                 "Crayfish", // Default name, owner should personalize
		Personality:          "minimal",  // Concise responses, no filler
		DBPath:               "crayfish.db",
		ListenAddr:           ":8119",
		ContinuityEnabled:    true,
		SessionResumeMinutes: 30,
		SnapshotsPerSession:  3,
		AutoUpdate:           true,
		UpdateChannel:        "stable",
		VoiceEnabled:         false,
		VoiceModel:           "en_US-lessac-medium", // Default Piper voice
		STTEnabled:           true,                  // Auto-enable if whisper.cpp is available
		STTModelPath:         "",                    // Auto-detect
	}
}

// LoadConfig reads configuration from a YAML file (if present) and environment variables.
// Environment variables always take precedence over YAML.
func LoadConfig(logger *slog.Logger) Config {
	cfg := DefaultConfig()

	// Try to load from YAML config file.
	configPath := os.Getenv("CRAYFISH_CONFIG")
	if configPath == "" {
		// Search standard locations: working dir, user config, system config.
		searchPaths := []string{"crayfish.yaml", "/etc/crayfish/crayfish.yaml"}
		if home, err := os.UserHomeDir(); err == nil {
			// Insert XDG config path as second priority (after working dir).
			searchPaths = []string{"crayfish.yaml", home + "/.config/crayfish/crayfish.yaml", "/etc/crayfish/crayfish.yaml"}
		}
		for _, path := range searchPaths {
			if _, err := os.Stat(path); err == nil {
				configPath = path
				break
			}
		}
	}

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			logger.Warn("failed to read config file", "path", configPath, "error", err)
		} else {
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				logger.Warn("failed to parse config file", "path", configPath, "error", err)
			} else {
				logger.Info("loaded config from file", "path", configPath)
			}
		}
	}

	// Override with environment variables.
	envStr := func(key string, dest *string) {
		if v := os.Getenv(key); v != "" {
			*dest = v
		}
	}
	envInt := func(key string, dest *int) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dest = n
			} else {
				logger.Warn("invalid env var", "key", key, "value", v, "error", err)
			}
		}
	}
	envBool := func(key string, dest *bool) {
		if v := os.Getenv(key); v != "" {
			*dest = v == "true" || v == "1" || v == "yes"
		}
	}

	envStr("CRAYFISH_NAME", &cfg.Name)
	envStr("CRAYFISH_DB_PATH", &cfg.DBPath)
	envStr("CRAYFISH_LISTEN", &cfg.ListenAddr)
	envBool("CRAYFISH_VOICE_ENABLED", &cfg.VoiceEnabled)
	envStr("CRAYFISH_VOICE_MODEL", &cfg.VoiceModel)
	envBool("CRAYFISH_STT_ENABLED", &cfg.STTEnabled)
	envStr("CRAYFISH_STT_MODEL_PATH", &cfg.STTModelPath)
	envStr("CRAYFISH_PROVIDER", &cfg.Provider)
	envStr("CRAYFISH_ENDPOINT", &cfg.Endpoint)
	envStr("CRAYFISH_MODEL", &cfg.Model)
	envInt("CRAYFISH_MAX_TOKENS", &cfg.MaxTokens)
	envStr("CRAYFISH_SYSTEM_PROMPT", &cfg.SystemPrompt)
	envStr("CRAYFISH_TELEGRAM_TOKEN", &cfg.TelegramToken)
	envStr("CRAYFISH_GMAIL_USER", &cfg.GmailUser)
	envStr("CRAYFISH_GMAIL_APP_PASSWORD", &cfg.GmailAppPassword)
	envInt("CRAYFISH_GMAIL_POLL_MINUTES", &cfg.GmailPollMinutes)
	envStr("CRAYFISH_BRAVE_API_KEY", &cfg.BraveAPIKey)
	envBool("CRAYFISH_CONTINUITY_ENABLED", &cfg.ContinuityEnabled)
	envInt("CRAYFISH_SESSION_RESUME_MINUTES", &cfg.SessionResumeMinutes)
	envInt("CRAYFISH_SNAPSHOTS_PER_SESSION", &cfg.SnapshotsPerSession)
	envBool("CRAYFISH_AUTO_UPDATE", &cfg.AutoUpdate)
	envStr("CRAYFISH_UPDATE_CHANNEL", &cfg.UpdateChannel)

	// API key: check Crayfish key first, fall back to provider-specific.
	if v := os.Getenv("CRAYFISH_API_KEY"); v != "" {
		cfg.APIKey = v
	} else if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		cfg.APIKey = v
	}

	return cfg
}

// GmailPollInterval returns the configured Gmail poll interval with a sensible default.
func (c Config) GmailPollInterval() time.Duration {
	if c.GmailPollMinutes > 0 {
		return time.Duration(c.GmailPollMinutes) * time.Minute
	}
	return 5 * time.Minute
}

// NeedsSetup returns true if the minimum required config is missing.
// For cloud providers (anthropic, openai, grok, etc.), an API key is required.
// For local providers (ollama, vllm, lmstudio), only an endpoint is needed.
func (c Config) NeedsSetup() bool {
	// Local providers don't need an API key.
	if isLocalProvider(c.Provider) {
		// For local providers, we need either an endpoint or we'll use the default.
		return false
	}
	// Cloud providers need an API key.
	return c.APIKey == ""
}

// isLocalProvider returns true for providers that run locally and don't need API keys.
func isLocalProvider(provider string) bool {
	switch provider {
	case "ollama", "vllm", "lmstudio", "llamacpp", "localai":
		return true
	default:
		return false
	}
}
