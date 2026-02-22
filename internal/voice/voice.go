// Package voice provides text-to-speech using local models.
//
// Crayfish uses Piper (https://github.com/OHF-Voice/piper1-gpl) for high-quality
// neural TTS that runs on Raspberry Pi and other small devices.
//
// Piper is now maintained by Open Home Foundation and installed via pip:
//   pip install piper-tts
//
// Voice models are downloaded via:
//   python3 -m piper.download_voices en_US-lessac-medium
package voice

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
)

// Engine provides text-to-speech synthesis.
type Engine struct {
	enabled   bool
	modelName string // Voice model name (e.g., "en_US-lessac-medium")
	dataDir   string // Directory containing voice models (optional)
	logger    *slog.Logger
	mu        sync.Mutex
}

// Config holds voice engine configuration.
type Config struct {
	Enabled   bool   // Whether TTS is enabled
	ModelName string // Voice model name (e.g., "en_US-lessac-medium")
	DataDir   string // Directory containing voice models (optional, uses default if empty)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:   false,
		ModelName: "en_US-lessac-medium",
		DataDir:   "", // Use piper's default location
	}
}

// New creates a voice engine.
func New(cfg Config, logger *slog.Logger) *Engine {
	e := &Engine{
		enabled:   cfg.Enabled,
		modelName: cfg.ModelName,
		dataDir:   cfg.DataDir,
		logger:    logger,
	}

	if !cfg.Enabled {
		logger.Debug("voice engine disabled")
		return e
	}

	// Verify piper-tts is installed.
	if !isPiperInstalled() {
		logger.Warn("piper-tts not installed, voice disabled",
			"hint", "Install with: pip install piper-tts")
		e.enabled = false
		return e
	}

	logger.Info("voice engine ready", "model", cfg.ModelName)
	return e
}

// Enabled returns whether the engine is ready to synthesize.
func (e *Engine) Enabled() bool {
	return e.enabled
}

// Synthesize converts text to speech, returning WAV audio data.
// The audio is 22050 Hz, 16-bit mono PCM in WAV format.
func (e *Engine) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if !e.enabled {
		return nil, fmt.Errorf("voice engine not enabled")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Build piper command:
	// python3 -m piper -m <model> --output-raw -- 'text'
	args := []string{"-m", "piper", "-m", e.modelName, "--output-raw"}

	if e.dataDir != "" {
		args = append(args, "--data-dir", e.dataDir)
	}

	args = append(args, "--", text)

	cmd := exec.CommandContext(ctx, "python3", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		e.logger.Error("piper synthesis failed", "error", err, "stderr", stderr.String())
		return nil, fmt.Errorf("piper: %w", err)
	}

	// Convert raw PCM to WAV.
	wav := pcmToWAV(stdout.Bytes(), 22050, 16, 1)

	e.logger.Debug("synthesized speech", "text_len", len(text), "audio_len", len(wav))
	return wav, nil
}

// SynthesizeToFile writes speech to a WAV file.
func (e *Engine) SynthesizeToFile(ctx context.Context, text, filename string) error {
	if !e.enabled {
		return fmt.Errorf("voice engine not enabled")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Use piper's native file output for efficiency.
	// python3 -m piper -m <model> -f <file> -- 'text'
	args := []string{"-m", "piper", "-m", e.modelName, "-f", filename}

	if e.dataDir != "" {
		args = append(args, "--data-dir", e.dataDir)
	}

	args = append(args, "--", text)

	cmd := exec.CommandContext(ctx, "python3", args...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		e.logger.Error("piper synthesis failed", "error", err, "stderr", stderr.String())
		return fmt.Errorf("piper: %w", err)
	}

	e.logger.Debug("synthesized speech to file", "text_len", len(text), "file", filename)
	return nil
}

// SynthesizeStream writes speech to the provided writer.
func (e *Engine) SynthesizeStream(ctx context.Context, text string, w io.Writer) error {
	wav, err := e.Synthesize(ctx, text)
	if err != nil {
		return err
	}
	_, err = w.Write(wav)
	return err
}

// DownloadVoice downloads a voice model using piper's built-in downloader.
// This requires an internet connection.
func DownloadVoice(ctx context.Context, modelName, dataDir string) error {
	args := []string{"-m", "piper.download_voices", modelName}
	if dataDir != "" {
		args = append(args, "--data-dir", dataDir)
	}

	cmd := exec.CommandContext(ctx, "python3", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ListAvailableVoices lists voices that can be downloaded.
func ListAvailableVoices(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "python3", "-m", "piper.download_voices")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// isPiperInstalled checks if piper-tts Python package is installed.
func isPiperInstalled() bool {
	cmd := exec.Command("python3", "-c", "import piper")
	return cmd.Run() == nil
}

// pcmToWAV wraps raw PCM data in a WAV header.
func pcmToWAV(pcm []byte, sampleRate, bitsPerSample, channels int) []byte {
	dataLen := len(pcm)
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	// WAV header is 44 bytes.
	header := make([]byte, 44)

	// RIFF chunk.
	copy(header[0:4], "RIFF")
	putLE32(header[4:8], uint32(36+dataLen))
	copy(header[8:12], "WAVE")

	// fmt subchunk.
	copy(header[12:16], "fmt ")
	putLE32(header[16:20], 16) // Subchunk1Size for PCM.
	putLE16(header[20:22], 1)  // AudioFormat (1 = PCM).
	putLE16(header[22:24], uint16(channels))
	putLE32(header[24:28], uint32(sampleRate))
	putLE32(header[28:32], uint32(byteRate))
	putLE16(header[32:34], uint16(blockAlign))
	putLE16(header[34:36], uint16(bitsPerSample))

	// data subchunk.
	copy(header[36:40], "data")
	putLE32(header[40:44], uint32(dataLen))

	return append(header, pcm...)
}

func putLE16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

func putLE32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// --- Speech-to-Text (STT) via whisper.cpp ---

// STTEngine provides speech-to-text transcription using whisper.cpp.
type STTEngine struct {
	enabled   bool
	modelPath string // Path to whisper model (e.g., "ggml-tiny.bin" or "ggml-base.bin")
	logger    *slog.Logger
	mu        sync.Mutex
}

// STTConfig holds STT engine configuration.
type STTConfig struct {
	Enabled   bool   // Whether STT is enabled
	ModelPath string // Path to whisper model file
}

// DefaultSTTConfig returns sensible defaults for Pi.
func DefaultSTTConfig() STTConfig {
	return STTConfig{
		Enabled:   false,
		ModelPath: "", // Auto-detect if whisper-cpp is installed
	}
}

// NewSTT creates a speech-to-text engine.
func NewSTT(cfg STTConfig, logger *slog.Logger) *STTEngine {
	e := &STTEngine{
		enabled:   cfg.Enabled,
		modelPath: cfg.ModelPath,
		logger:    logger,
	}

	if !cfg.Enabled {
		logger.Debug("STT engine disabled")
		return e
	}

	// Check if whisper-cpp is available
	if !isWhisperInstalled() {
		logger.Warn("whisper-cpp not installed, STT disabled",
			"hint", "Install whisper.cpp from https://github.com/ggml-org/whisper.cpp")
		e.enabled = false
		return e
	}

	// Auto-detect model if not specified
	if e.modelPath == "" {
		e.modelPath = findWhisperModel()
		if e.modelPath == "" {
			logger.Warn("no whisper model found, STT disabled",
				"hint", "Download a model: ./models/download-ggml-model.sh tiny")
			e.enabled = false
			return e
		}
	}

	logger.Info("STT engine ready", "model", e.modelPath)
	return e
}

// STTEnabled returns whether STT is ready.
func (e *STTEngine) STTEnabled() bool {
	return e.enabled
}

// Transcribe converts audio to text.
// Accepts WAV, OGG, or MP3 audio data.
func (e *STTEngine) Transcribe(ctx context.Context, audioData []byte, format string) (string, error) {
	if !e.enabled {
		return "", fmt.Errorf("STT engine not enabled")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Write audio to temp file
	tmpFile, err := os.CreateTemp("", "whisper-*."+format)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(audioData); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("write audio: %w", err)
	}
	tmpFile.Close()

	// Convert to WAV if needed (whisper.cpp requires 16kHz WAV)
	wavPath := tmpPath
	if format != "wav" {
		wavPath = tmpPath + ".wav"
		defer os.Remove(wavPath)
		if err := convertToWAV(ctx, tmpPath, wavPath); err != nil {
			return "", fmt.Errorf("convert audio: %w", err)
		}
	}

	// Run whisper-cpp
	// whisper -m model.bin -f audio.wav -nt (no timestamps)
	args := []string{"-m", e.modelPath, "-f", wavPath, "-nt", "-np"}
	cmd := exec.CommandContext(ctx, "whisper", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Try alternate command name
		cmd2 := exec.CommandContext(ctx, "whisper-cpp", args...)
		cmd2.Stdout = &stdout
		cmd2.Stderr = &stderr
		if err2 := cmd2.Run(); err2 != nil {
			e.logger.Error("whisper transcription failed", "error", err, "stderr", stderr.String())
			return "", fmt.Errorf("whisper: %w", err)
		}
	}

	text := bytes.TrimSpace(stdout.Bytes())
	e.logger.Debug("transcribed audio", "text", string(text), "audio_size", len(audioData))
	return string(text), nil
}

// TranscribeFile transcribes audio from a file.
func (e *STTEngine) TranscribeFile(ctx context.Context, filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	// Detect format from extension
	format := "wav"
	if len(filePath) > 4 {
		ext := filePath[len(filePath)-3:]
		switch ext {
		case "ogg", "oga":
			format = "ogg"
		case "mp3":
			format = "mp3"
		case "m4a":
			format = "m4a"
		}
	}

	return e.Transcribe(ctx, data, format)
}

// isWhisperInstalled checks if whisper-cpp is available.
func isWhisperInstalled() bool {
	// Try common command names
	for _, cmd := range []string{"whisper", "whisper-cpp", "main"} {
		if _, err := exec.LookPath(cmd); err == nil {
			return true
		}
	}
	return false
}

// findWhisperModel looks for whisper models in common locations.
func findWhisperModel() string {
	// Common model locations
	locations := []string{
		// Relative to crayfish
		"models/ggml-tiny.bin",
		"models/ggml-base.bin",
		// User home
		os.Getenv("HOME") + "/.local/share/whisper/ggml-tiny.bin",
		os.Getenv("HOME") + "/.local/share/whisper/ggml-base.bin",
		// System
		"/usr/local/share/whisper/ggml-tiny.bin",
		"/usr/local/share/whisper/ggml-base.bin",
		// whisper.cpp default
		"/usr/local/bin/whisper.cpp/models/ggml-tiny.bin",
	}

	for _, path := range locations {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// convertToWAV converts audio to 16kHz mono WAV using ffmpeg.
func convertToWAV(ctx context.Context, input, output string) error {
	// ffmpeg -i input -ar 16000 -ac 1 -c:a pcm_s16le output.wav
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", input,
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-y", // Overwrite
		output,
	)
	cmd.Stderr = nil // Suppress ffmpeg output
	return cmd.Run()
}
