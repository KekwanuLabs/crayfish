package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KekwanuLabs/crayfish/internal/security"
	"github.com/KekwanuLabs/crayfish/internal/voice"
)

// VoiceConnectDeps are injected by app.go when registering the voice_connect tool.
type VoiceConnectDeps struct {
	// IsConfigured returns true if ElevenLabs is already set up.
	IsConfigured func() bool

	// SaveConfig persists the API key and voice ID to the config file.
	SaveConfig func(apiKey, voiceID string)

	// ActivateTTS hot-enables the ElevenLabs engine on the Telegram adapter
	// without requiring a service restart.
	ActivateTTS func(apiKey, voiceID string)
}

// RegisterVoiceConnectTool registers the voice_connect tool.
// Always registered so users can set up ElevenLabs conversationally at any time.
func RegisterVoiceConnectTool(reg *Registry, deps VoiceConnectDeps) {
	reg.Register(&Tool{
		Name: "voice_connect",
		Description: `Set up ElevenLabs voice synthesis so I can respond with spoken audio.

When called with an API key, this tool validates the key, lists available voices,
and activates voice responses immediately — no restart needed.

Parameters:
- api_key: ElevenLabs API key (from elevenlabs.io/app/keys)
- voice_id: (optional) specific voice to use; omit to list available voices first
- action: "setup" (default), "list_voices", or "status"

ElevenLabs free tier: 10,000 characters/month. Paid plans from $5/month.`,
		MinTier: security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"api_key": {
					"type": "string",
					"description": "ElevenLabs API key"
				},
				"voice_id": {
					"type": "string",
					"description": "Voice ID to use (omit to list available voices)"
				},
				"action": {
					"type": "string",
					"enum": ["setup", "list_voices", "status"],
					"description": "What to do (default: setup)"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, params json.RawMessage) (string, error) {
			var input struct {
				APIKey  string `json:"api_key"`
				VoiceID string `json:"voice_id"`
				Action  string `json:"action"`
			}
			if err := json.Unmarshal(params, &input); err != nil {
				return "", fmt.Errorf("invalid parameters: %w", err)
			}
			if input.Action == "" {
				input.Action = "setup"
			}

			switch input.Action {

			case "status":
				if deps.IsConfigured() {
					return "ElevenLabs voice is active — I'll respond with spoken audio on Telegram and phone calls use your configured voice.", nil
				}
				return "ElevenLabs isn't set up yet. Say 'set up my voice' and I'll walk you through it — takes 2 minutes and the free tier is plenty for everyday use.", nil

			case "list_voices":
				if input.APIKey == "" {
					return "", fmt.Errorf("provide an api_key to list voices")
				}
				voices, err := voice.ListElevenLabsVoices(ctx, input.APIKey)
				if err != nil {
					return "", fmt.Errorf("could not fetch voices: %w", err)
				}
				var sb strings.Builder
				sb.WriteString("Voices available on this account:\n\n")
				for _, v := range voices {
					sb.WriteString(fmt.Sprintf("- **%s** (`%s`)", v.Name, v.VoiceID))
					if v.Category != "" {
						sb.WriteString(fmt.Sprintf(" — %s", v.Category))
					}
					sb.WriteString("\n")
				}
				sb.WriteString("\nCall voice_connect again with your chosen voice_id to activate.")
				return sb.String(), nil

			case "setup":
				if input.APIKey == "" {
					return `I'll give you a voice! Here's how to get set up in about 2 minutes:

**Step 1: Create a free ElevenLabs account**
👉 https://elevenlabs.io (click "Sign Up Free")
Free tier includes 10,000 characters/month — about 80–100 voice responses.

**Step 2: Get your API key**
Once signed in: click your profile icon (top right) → API Keys → Create API Key

**Step 3: Paste it here**
That's all I need. I'll pick a natural-sounding voice for you automatically, or you can choose one from your library.

> *If you want a custom voice (clone your own voice or pick from hundreds), ElevenLabs paid plans start at $5/month.*`, nil
				}

				// Validate the key.
				if err := voice.ValidateElevenLabsKey(ctx, input.APIKey); err != nil {
					return "", fmt.Errorf("key validation failed: %w — double-check your API key at https://elevenlabs.io/app/keys", err)
				}

				// If no voice ID given, list voices for the user to pick.
				if input.VoiceID == "" {
					voices, err := voice.ListElevenLabsVoices(ctx, input.APIKey)
					if err != nil || len(voices) == 0 {
						// Fall back to Rachel.
						input.VoiceID = "21m00Tcm4TlvDq8ikWAM"
					} else {
						var sb strings.Builder
						sb.WriteString("API key is valid! Here are your available voices:\n\n")
						limit := len(voices)
						if limit > 10 {
							limit = 10
						}
						for _, v := range voices[:limit] {
							sb.WriteString(fmt.Sprintf("- **%s** (`%s`)", v.Name, v.VoiceID))
							if v.Category != "" {
								sb.WriteString(fmt.Sprintf(" — %s", v.Category))
							}
							sb.WriteString("\n")
						}
						sb.WriteString("\nReply with the voice_id you'd like, or say \"use default\" to use Rachel.")
						return sb.String(), nil
					}
				}

				// Save and hot-activate.
				deps.SaveConfig(input.APIKey, input.VoiceID)
				deps.ActivateTTS(input.APIKey, input.VoiceID)

				return fmt.Sprintf("Voice activated! I'll now respond with spoken audio using ElevenLabs.\nVoice: `%s`\n\nYou can change voices any time — just say \"change my voice\" or call voice_connect with a different voice_id.", input.VoiceID), nil

			default:
				return "", fmt.Errorf("unknown action %q — use setup, list_voices, or status", input.Action)
			}
		},
	})
}
