package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// OpenAIWhisperEndpoint is the default endpoint for OpenAI Whisper transcription.
const OpenAIWhisperEndpoint = "https://api.openai.com/v1/audio/transcriptions"

// GroqWhisperEndpoint is the Groq endpoint for Whisper transcription (free tier).
const GroqWhisperEndpoint = "https://api.groq.com/openai/v1/audio/transcriptions"

// WhisperAPIEngine implements STTTranscriber using any OpenAI-compatible Whisper
// endpoint (OpenAI, Groq, etc.). The audio is sent as multipart/form-data.
type WhisperAPIEngine struct {
	endpoint string
	apiKey   string
	client   *http.Client
	logger   *slog.Logger
}

// NewWhisperAPI creates a Whisper API engine.
// endpoint is the full transcription URL (e.g. OpenAIWhisperEndpoint or GroqWhisperEndpoint).
func NewWhisperAPI(endpoint, apiKey string, logger *slog.Logger) *WhisperAPIEngine {
	return &WhisperAPIEngine{
		endpoint: endpoint,
		apiKey:   apiKey,
		client:   &http.Client{Timeout: 30 * time.Second},
		logger:   logger,
	}
}

// STTEnabled returns whether the engine is configured and ready.
func (e *WhisperAPIEngine) STTEnabled() bool {
	return e.apiKey != "" && e.endpoint != ""
}

// Transcribe sends audio to the Whisper API and returns the transcript.
// Supports OGG (Telegram voice messages), WAV, and MP3 formats.
func (e *WhisperAPIEngine) Transcribe(ctx context.Context, audioData []byte, format string) (string, error) {
	if !e.STTEnabled() {
		return "", fmt.Errorf("Whisper API not configured")
	}

	// Build multipart/form-data body.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	// Add the audio file field.
	filename := "audio." + format
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("whisper_api: create form file: %w", err)
	}
	if _, err := fw.Write(audioData); err != nil {
		return "", fmt.Errorf("whisper_api: write audio: %w", err)
	}

	// Add the model field.
	if err := w.WriteField("model", "whisper-1"); err != nil {
		return "", fmt.Errorf("whisper_api: write model field: %w", err)
	}
	w.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", e.endpoint, &body)
	if err != nil {
		return "", fmt.Errorf("whisper_api: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper_api: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("whisper_api: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper_api: API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("whisper_api: parse response: %w", err)
	}

	e.logger.Debug("whisper_api transcribed", "format", format, "audio_bytes", len(audioData), "text_len", len(result.Text))
	return result.Text, nil
}

// WhisperEndpointForProvider returns the Whisper transcription endpoint for a
// given LLM provider and its configured API endpoint.
// Returns empty string if the provider doesn't support Whisper.
func WhisperEndpointForProvider(providerName, configEndpoint string) string {
	switch providerName {
	case "openai":
		return OpenAIWhisperEndpoint
	}
	// Groq is configured via a custom endpoint URL — detect from it.
	if strings.Contains(configEndpoint, "groq.com") {
		return GroqWhisperEndpoint
	}
	return ""
}
