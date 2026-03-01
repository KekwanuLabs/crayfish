package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// STTConnectDeps holds dependencies for the stt_connect tool.
type STTConnectDeps struct {
	ProviderName        string               // Current LLM provider name (e.g. "openai", "anthropic")
	IsConfigured        func() bool          // True if cloud STT is already active (provider key or explicit key)
	TryReuseProviderKey func() bool          // Activate STT using the existing LLM provider key; returns true if it worked
	SaveKey             func(key string)     // Persist an explicit STT API key to config
	ActivateSTT         func(key string)     // Create engine and attach to Telegram adapter using the given key
}

// RegisterSTTConnectTool adds the stt_connect tool so users can set up voice
// transcription via cloud Whisper. Always registered so users on unsupported
// hardware can configure it conversationally.
func RegisterSTTConnectTool(reg *Registry, deps STTConnectDeps) {
	reg.logger.Info("registering stt_connect tool")

	reg.Register(&Tool{
		Name:        "stt_connect",
		Description: "Set up voice message transcription for Telegram. If the current provider already supports it, activate it immediately. Otherwise guide the user to get a free API key. Call with no arguments to check status.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"api_key": {
					"type": "string",
					"description": "OpenAI or Groq API key to use for voice transcription"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				APIKey string `json:"api_key"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("stt_connect: parse input: %w", err)
			}

			// No key provided — check status and guide if needed.
			if params.APIKey == "" {
				if deps.IsConfigured() {
					return "Voice transcription is already active. Voice messages sent on Telegram will be transcribed automatically.", nil
				}

				// Can we reuse the existing LLM provider key?
				provider := strings.ToLower(deps.ProviderName)
				if provider == "openai" || strings.Contains(provider, "groq") {
					if deps.TryReuseProviderKey != nil && deps.TryReuseProviderKey() {
						return fmt.Sprintf("Voice transcription is now active! I used your existing %s API key — nothing extra needed.", deps.ProviderName), nil
					}
				}

				// Guide the user to get an API key.
				return sttSetupInstructions(), nil
			}

			// Key provided — validate and activate.
			apiKey := strings.TrimSpace(params.APIKey)

			// Detect if it looks like a Groq key (they start with "gsk_").
			endpoint := "https://api.openai.com/v1/audio/transcriptions"
			validationURL := "https://api.openai.com/v1/models"
			if strings.HasPrefix(apiKey, "gsk_") {
				endpoint = "https://api.groq.com/openai/v1/audio/transcriptions"
				validationURL = "https://api.groq.com/openai/v1/models"
				_ = endpoint // endpoint is used via ActivateSTT
			}

			if err := validateWhisperKey(ctx, validationURL, apiKey); err != nil {
				return fmt.Sprintf("That key didn't work: %s\n\nMake sure you copied the full key and try again.", err.Error()), nil
			}

			deps.SaveKey(apiKey)
			deps.ActivateSTT(apiKey)

			return "Voice transcription is now active! I verified the key and saved it. Voice messages on Telegram will be transcribed automatically.", nil
		},
	})
}

// sttSetupInstructions returns friendly setup guidance based on the user's current provider.
func sttSetupInstructions() string {
	return `Voice transcription needs a small extra step on this device. Here are two ways to set it up — pick whichever feels easier:

**Option 1 — Groq (recommended, free, no credit card needed)**
1. Go to console.groq.com and sign up with your email
2. Click **"API Keys"** in the left sidebar
3. Click **"Create API Key"**, give it any name, and copy it
4. Paste the key here

**Option 2 — OpenAI (if you already have an account)**
1. Go to platform.openai.com/api-keys
2. Click **"Create new secret key"**, copy it
3. Paste the key here

Either key works. Once you paste it, I'll verify and activate it right away.`
}

// validateWhisperKey checks that an API key is valid by hitting the models list endpoint.
// Returns nil if valid, a user-friendly error otherwise.
func validateWhisperKey(ctx context.Context, modelsURL, apiKey string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "GET", modelsURL, bytes.NewReader(nil))
	if err != nil {
		return fmt.Errorf("couldn't reach the server — check your internet connection")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach the server — check your internet connection")
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("the key was rejected — it may be invalid or has been revoked")
	case http.StatusForbidden:
		return fmt.Errorf("the key doesn't have the right permissions")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("unexpected response (HTTP %d): %s", resp.StatusCode, string(body))
	}
}
