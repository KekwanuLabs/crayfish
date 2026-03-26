package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	elevenLabsBaseURL      = "https://api.elevenlabs.io/v1"
	elevenLabsDefaultVoice = "21m00Tcm4TlvDq8ikWAM" // Rachel — natural, calm
	elevenLabsDefaultModel = "eleven_turbo_v2_5"     // Fastest + cheapest, great quality
)

// ElevenLabsEngine implements TTSSynthesizer using the ElevenLabs cloud API.
// It requests pcm_22050 output (22050 Hz, 16-bit mono PCM) which matches
// piper's output format, so the existing WAV→OGG→Telegram pipeline works unchanged.
type ElevenLabsEngine struct {
	apiKey  string
	voiceID string
	model   string
	client  *http.Client
	logger  *slog.Logger
}

// NewElevenLabsEngine creates an ElevenLabs TTS engine.
// voiceID defaults to Rachel if empty. model defaults to eleven_turbo_v2_5.
func NewElevenLabsEngine(apiKey, voiceID string, logger *slog.Logger) *ElevenLabsEngine {
	if voiceID == "" {
		voiceID = elevenLabsDefaultVoice
	}
	return &ElevenLabsEngine{
		apiKey:  apiKey,
		voiceID: voiceID,
		model:   elevenLabsDefaultModel,
		client:  &http.Client{Timeout: 30 * time.Second},
		logger:  logger,
	}
}

// Enabled returns true if an API key is configured.
func (e *ElevenLabsEngine) Enabled() bool {
	return e.apiKey != ""
}

// Synthesize converts text to speech via ElevenLabs and returns WAV audio.
// Uses pcm_22050 output format (22050 Hz, 16-bit, mono) wrapped in a WAV header.
func (e *ElevenLabsEngine) Synthesize(ctx context.Context, text string) ([]byte, error) {
	url := fmt.Sprintf("%s/text-to-speech/%s?output_format=pcm_22050", elevenLabsBaseURL, e.voiceID)

	reqBody, err := json.Marshal(map[string]any{
		"text":     text,
		"model_id": e.model,
		"voice_settings": map[string]any{
			"stability":        0.5,
			"similarity_boost": 0.75,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("xi-api-key", e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/pcm")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ElevenLabs API error %d: %s", resp.StatusCode, string(body))
	}

	pcmData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(pcmData) == 0 {
		return nil, fmt.Errorf("empty audio response from ElevenLabs")
	}

	// Wrap raw PCM in a WAV header so the existing WAV→OGG pipeline works.
	wav := pcmToWAV(pcmData, 22050, 16, 1)
	e.logger.Debug("ElevenLabs synthesis complete", "text_len", len(text), "audio_bytes", len(wav))
	return wav, nil
}

// ElevenLabsVoice represents a voice available in the user's ElevenLabs account.
type ElevenLabsVoice struct {
	VoiceID  string `json:"voice_id"`
	Name     string `json:"name"`
	Category string `json:"category"` // "premade", "cloned", etc.
}

// ListVoices fetches the voices available on this account.
func ListElevenLabsVoices(ctx context.Context, apiKey string) ([]ElevenLabsVoice, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", elevenLabsBaseURL+"/voices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error %d", resp.StatusCode)
	}

	var result struct {
		Voices []ElevenLabsVoice `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Voices, nil
}

// ValidateElevenLabsKey checks whether the API key is valid.
func ValidateElevenLabsKey(ctx context.Context, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", elevenLabsBaseURL+"/user", nil)
	if err != nil {
		return err
	}
	req.Header.Set("xi-api-key", apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error %d", resp.StatusCode)
	}
	return nil
}
