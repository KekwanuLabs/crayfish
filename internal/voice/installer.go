package voice

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/device"
)

// InstallerConfig holds configuration for the STT installer.
type InstallerConfig struct {
	// DataDir is where whisper binaries and models are stored.
	// Defaults to ~/.crayfish/whisper
	DataDir string

	// BinaryURL is the base URL for pre-built binaries.
	// Binaries are expected at {BinaryURL}/whisper-{os}-{arch}.tar.gz
	BinaryURL string

	// ModelURL is the base URL for model downloads.
	// Models are expected at {ModelURL}/ggml-{model}.bin
	ModelURL string

	// ForceCompile skips binary download and always compiles from source.
	ForceCompile bool
}

// DefaultInstallerConfig returns sensible defaults.
func DefaultInstallerConfig() InstallerConfig {
	dataDir := filepath.Join(os.Getenv("HOME"), ".crayfish", "whisper")
	if runtime.GOOS == "linux" {
		// On Linux, prefer /var/lib for system-wide installs
		if os.Getuid() == 0 {
			dataDir = "/var/lib/crayfish/whisper"
		}
	}

	return InstallerConfig{
		DataDir: dataDir,
		// GitHub releases for pre-built binaries (built by build-whisper.yml workflow)
		BinaryURL: "https://github.com/KekwanuLabs/crayfish/releases/latest/download",
		// Hugging Face for models (official source)
		ModelURL: "https://huggingface.co/ggerganov/whisper.cpp/resolve/main",
	}
}

// Installer handles automatic whisper.cpp setup.
type Installer struct {
	config InstallerConfig
	device device.Info
	logger *slog.Logger

	mu       sync.Mutex
	status   InstallStatus
	progress float64
	message  string
}

// InstallStatus represents the current installation state.
type InstallStatus int

const (
	StatusNotStarted InstallStatus = iota
	StatusChecking
	StatusDownloadingBinary
	StatusCompilingSource
	StatusDownloadingModel
	StatusVerifying
	StatusComplete
	StatusFailed
	StatusNotSupported // Device can't run whisper
)

func (s InstallStatus) String() string {
	switch s {
	case StatusNotStarted:
		return "not_started"
	case StatusChecking:
		return "checking"
	case StatusDownloadingBinary:
		return "downloading_binary"
	case StatusCompilingSource:
		return "compiling"
	case StatusDownloadingModel:
		return "downloading_model"
	case StatusVerifying:
		return "verifying"
	case StatusComplete:
		return "complete"
	case StatusFailed:
		return "failed"
	case StatusNotSupported:
		return "not_supported"
	default:
		return "unknown"
	}
}

// InstallProgress contains current installation progress.
type InstallProgress struct {
	Status   InstallStatus `json:"status"`
	Progress float64       `json:"progress"` // 0.0 to 1.0
	Message  string        `json:"message"`
	Error    string        `json:"error,omitempty"`
}

// NewInstaller creates a new whisper installer.
func NewInstaller(cfg InstallerConfig, logger *slog.Logger) *Installer {
	return &Installer{
		config: cfg,
		device: device.Detect(),
		logger: logger,
		status: StatusNotStarted,
	}
}

// Progress returns current installation progress.
func (i *Installer) Progress() InstallProgress {
	i.mu.Lock()
	defer i.mu.Unlock()

	return InstallProgress{
		Status:   i.status,
		Progress: i.progress,
		Message:  i.message,
	}
}

// BinaryPath returns the path to the whisper binary.
func (i *Installer) BinaryPath() string {
	return filepath.Join(i.config.DataDir, "bin", i.device.WhisperBinaryName())
}

// ModelPath returns the path to the whisper model.
func (i *Installer) ModelPath(model string) string {
	return filepath.Join(i.config.DataDir, "models", fmt.Sprintf("ggml-%s.bin", model))
}

// IsInstalled checks if whisper is already installed and working.
func (i *Installer) IsInstalled() bool {
	// Check if binary exists
	binPath := i.BinaryPath()
	if _, err := os.Stat(binPath); err != nil {
		// Also check system PATH
		if _, err := exec.LookPath("whisper"); err != nil {
			if _, err := exec.LookPath("whisper-cpp"); err != nil {
				return false
			}
		}
	}

	// Check if a model exists
	model := i.device.RecommendedWhisperModel()
	if model == "" {
		return false
	}

	modelPath := i.ModelPath(model)
	if _, err := os.Stat(modelPath); err != nil {
		// Check default locations
		if findWhisperModel() == "" {
			return false
		}
	}

	return true
}

// Install performs the full whisper installation.
// This is safe to call multiple times - it will skip if already installed.
func (i *Installer) Install(ctx context.Context) error {
	i.mu.Lock()
	if i.status == StatusComplete {
		i.mu.Unlock()
		return nil
	}
	i.mu.Unlock()

	// Check if device can run whisper
	if !i.device.CanRunWhisper() {
		i.setStatus(StatusNotSupported, 0, "Device cannot run speech recognition (insufficient RAM)")
		i.logger.Warn("device cannot run whisper",
			"ram_mb", i.device.TotalRAMMB,
			"min_required_mb", 600)
		return fmt.Errorf("device has insufficient RAM for speech recognition")
	}

	i.setStatus(StatusChecking, 0.05, "Checking system...")
	i.logger.Info("starting whisper installation",
		"device", i.device.String(),
		"recommended_model", i.device.RecommendedWhisperModel())

	// Create directories
	if err := os.MkdirAll(filepath.Join(i.config.DataDir, "bin"), 0755); err != nil {
		return i.fail("create bin dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(i.config.DataDir, "models"), 0755); err != nil {
		return i.fail("create models dir: %w", err)
	}

	// Step 1: Get whisper binary
	if !i.config.ForceCompile {
		i.setStatus(StatusDownloadingBinary, 0.1, "Downloading speech recognition engine...")
		if err := i.downloadBinary(ctx); err != nil {
			i.logger.Warn("pre-built binary not available, will compile from source", "error", err)
			// Fall through to compile
		} else {
			i.logger.Info("downloaded pre-built whisper binary")
			goto downloadModel
		}
	}

	// Compile from source
	i.setStatus(StatusCompilingSource, 0.2, "Building speech recognition (this takes a few minutes on Pi)...")
	if err := i.compileFromSource(ctx); err != nil {
		return i.fail("compile whisper: %w", err)
	}

downloadModel:
	// Step 2: Download model
	model := i.device.RecommendedWhisperModel()
	modelPath := i.ModelPath(model)

	if _, err := os.Stat(modelPath); err != nil {
		i.setStatus(StatusDownloadingModel, 0.6, fmt.Sprintf("Downloading %s voice model...", model))
		if err := i.downloadModel(ctx, model); err != nil {
			return i.fail("download model: %w", err)
		}
	}

	// Step 3: Verify installation
	i.setStatus(StatusVerifying, 0.9, "Verifying installation...")
	if err := i.verify(ctx); err != nil {
		return i.fail("verification failed: %w", err)
	}

	i.setStatus(StatusComplete, 1.0, "Speech recognition ready!")
	i.logger.Info("whisper installation complete",
		"binary", i.BinaryPath(),
		"model", modelPath)

	return nil
}

func (i *Installer) setStatus(status InstallStatus, progress float64, message string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.status = status
	i.progress = progress
	i.message = message
}

func (i *Installer) fail(format string, args ...interface{}) error {
	err := fmt.Errorf(format, args...)
	i.mu.Lock()
	i.status = StatusFailed
	i.message = err.Error()
	i.mu.Unlock()
	i.logger.Error("whisper installation failed", "error", err)
	return err
}

// downloadBinary attempts to download a pre-built binary.
func (i *Installer) downloadBinary(ctx context.Context) error {
	// Determine platform string
	platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	if i.device.ArmModel != "" && runtime.GOARCH == "arm" {
		platform = fmt.Sprintf("%s-arm%s", runtime.GOOS, i.device.ArmModel)
	}

	url := fmt.Sprintf("%s/whisper-%s.tar.gz", i.config.BinaryURL, platform)
	i.logger.Debug("downloading whisper binary", "url", url)

	// Download with timeout
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("binary not available: HTTP %d", resp.StatusCode)
	}

	// Extract tarball
	return i.extractTarGz(resp.Body, filepath.Join(i.config.DataDir, "bin"))
}

func (i *Installer) extractTarGz(r io.Reader, destDir string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

// compileFromSource clones and builds whisper.cpp.
func (i *Installer) compileFromSource(ctx context.Context) error {
	srcDir := filepath.Join(i.config.DataDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return err
	}

	whisperDir := filepath.Join(srcDir, "whisper.cpp")

	// Clone or update repo
	if _, err := os.Stat(filepath.Join(whisperDir, "Makefile")); err != nil {
		i.logger.Info("cloning whisper.cpp repository")
		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1",
			"https://github.com/ggml-org/whisper.cpp.git", whisperDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git clone: %w", err)
		}
	}

	// Check if cmake is available (whisper.cpp now uses cmake by default)
	_, cmakeErr := exec.LookPath("cmake")
	hasCmake := cmakeErr == nil

	// Try to install cmake if not available (Debian/Ubuntu/Raspberry Pi OS)
	if !hasCmake && runtime.GOOS == "linux" {
		i.logger.Info("cmake not found, attempting to install build dependencies")

		// Try apt-get (Debian/Ubuntu/Raspberry Pi OS)
		installCmd := exec.CommandContext(ctx, "sudo", "apt-get", "install", "-y", "cmake", "g++")
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr
		if err := installCmd.Run(); err != nil {
			i.logger.Warn("could not auto-install cmake", "error", err)
		} else {
			hasCmake = true
			i.logger.Info("cmake installed successfully")
		}
	}

	// Determine build method and flags
	var cmd *exec.Cmd

	if hasCmake {
		// Use cmake build (preferred)
		i.logger.Info("compiling whisper.cpp with cmake", "cores", i.device.CPUCores)
		buildDir := filepath.Join(whisperDir, "build")
		os.MkdirAll(buildDir, 0755)

		// Configure
		cmakeArgs := []string{"..", "-DCMAKE_BUILD_TYPE=Release"}
		if runtime.GOARCH == "arm" {
			cmakeArgs = append(cmakeArgs,
				"-DCMAKE_EXE_LINKER_FLAGS=-latomic",
				"-DCMAKE_SHARED_LINKER_FLAGS=-latomic",
			)
		}
		configCmd := exec.CommandContext(ctx, "cmake", cmakeArgs...)
		configCmd.Dir = buildDir
		configCmd.Stdout = os.Stdout
		configCmd.Stderr = os.Stderr
		if err := configCmd.Run(); err != nil {
			return fmt.Errorf("cmake configure: %w", err)
		}

		// Build
		cores := i.device.CPUCores
		if cores > 2 {
			cores = 2 // Don't overwhelm small devices
		}
		cmd = exec.CommandContext(ctx, "cmake", "--build", ".", "-j", fmt.Sprintf("%d", cores))
		cmd.Dir = buildDir
	} else {
		// Try legacy make with NO_CMAKE flag for older/simpler builds
		i.logger.Info("compiling whisper.cpp with legacy make (no cmake)", "cores", i.device.CPUCores)

		// Check if there's a simple Makefile option
		// Some systems may need to use the examples directly
		cores := i.device.CPUCores
		if cores > 2 {
			cores = 2
		}

		// Try building just the main binary directly with gcc
		// This is a fallback for systems without cmake
		mainC := filepath.Join(whisperDir, "examples", "main", "main.cpp")
		if _, err := os.Stat(mainC); err == nil {
			i.logger.Info("building whisper main example directly")

			// Simple gcc/g++ compilation
			gppArgs := []string{
				"-O3", "-std=c++11", "-pthread",
				"-I" + whisperDir,
				"-I" + filepath.Join(whisperDir, "examples"),
				filepath.Join(whisperDir, "ggml", "src", "ggml.c"),
				filepath.Join(whisperDir, "src", "whisper.cpp"),
				mainC,
				"-o", filepath.Join(whisperDir, "main"),
				"-lm",
			}
			if runtime.GOARCH == "arm" {
				gppArgs = append(gppArgs, "-latomic")
			}
			cmd = exec.CommandContext(ctx, "g++", gppArgs...)
			cmd.Dir = whisperDir
		} else {
			return fmt.Errorf("cmake not installed and legacy build not available; install cmake with: sudo apt-get install cmake")
		}
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// Provide helpful error message
		if !hasCmake {
			return fmt.Errorf("build failed (cmake not installed): %w\nInstall cmake: sudo apt-get install cmake", err)
		}
		return fmt.Errorf("build: %w", err)
	}

	// Find and copy binary to our bin directory
	var srcBin string
	possiblePaths := []string{
		filepath.Join(whisperDir, "build", "bin", "whisper-cli"),
		filepath.Join(whisperDir, "build", "bin", "main"),
		filepath.Join(whisperDir, "build", "main"),
		filepath.Join(whisperDir, "main"),
	}
	for _, p := range possiblePaths {
		if _, err := os.Stat(p); err == nil {
			srcBin = p
			break
		}
	}
	if srcBin == "" {
		return fmt.Errorf("whisper binary not found after build")
	}

	dstBin := i.BinaryPath()
	if err := copyFile(srcBin, dstBin); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}

	if err := os.Chmod(dstBin, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	i.logger.Info("whisper binary installed", "path", dstBin)
	return nil
}

// downloadModel downloads a whisper model from Hugging Face.
func (i *Installer) downloadModel(ctx context.Context, model string) error {
	url := fmt.Sprintf("%s/ggml-%s.bin", i.config.ModelURL, model)
	destPath := i.ModelPath(model)

	i.logger.Info("downloading whisper model", "model", model, "url", url)

	// Create temp file for download
	tmpPath := destPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath) // Clean up on failure

	// Download with progress
	client := &http.Client{Timeout: 30 * time.Minute} // Models can be large
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		f.Close()
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		f.Close()
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		f.Close()
		return fmt.Errorf("model download failed: HTTP %d", resp.StatusCode)
	}

	// Copy with progress tracking
	totalSize := resp.ContentLength
	var downloaded int64

	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				f.Close()
				return writeErr
			}
			downloaded += int64(n)

			if totalSize > 0 {
				pct := float64(downloaded) / float64(totalSize)
				// Map 0-1 to 0.6-0.9 range (model download phase)
				progress := 0.6 + (pct * 0.3)
				sizeMB := downloaded / 1024 / 1024
				totalMB := totalSize / 1024 / 1024
				i.setStatus(StatusDownloadingModel, progress,
					fmt.Sprintf("Downloading %s model... %dMB / %dMB", model, sizeMB, totalMB))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			return err
		}
	}

	f.Close()

	// Move to final location
	if err := os.Rename(tmpPath, destPath); err != nil {
		return err
	}

	return nil
}

// verify tests that whisper works correctly.
func (i *Installer) verify(ctx context.Context) error {
	// Find the binary
	binPath := i.BinaryPath()
	if _, err := os.Stat(binPath); err != nil {
		// Try system PATH
		if path, err := exec.LookPath("whisper"); err == nil {
			binPath = path
		} else if path, err := exec.LookPath("whisper-cpp"); err == nil {
			binPath = path
		} else {
			return fmt.Errorf("whisper binary not found")
		}
	}

	// Find the model
	model := i.device.RecommendedWhisperModel()
	modelPath := i.ModelPath(model)
	if _, err := os.Stat(modelPath); err != nil {
		modelPath = findWhisperModel()
		if modelPath == "" {
			return fmt.Errorf("whisper model not found")
		}
	}

	// Create a simple test audio file (1 second of silence as WAV)
	testAudio := filepath.Join(i.config.DataDir, "test.wav")
	if err := createSilentWAV(testAudio, 16000, 1); err != nil {
		return fmt.Errorf("create test audio: %w", err)
	}
	defer os.Remove(testAudio)

	// Run whisper on the test file
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "-m", modelPath, "-f", testAudio, "-nt", "-np")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("whisper test failed: %w (output: %s)", err, string(output))
	}

	i.logger.Debug("whisper verification passed", "output", strings.TrimSpace(string(output)))
	return nil
}

// createSilentWAV creates a WAV file with silence for testing.
func createSilentWAV(path string, sampleRate, seconds int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// WAV header for 16-bit mono PCM
	numSamples := sampleRate * seconds
	dataSize := numSamples * 2 // 16-bit = 2 bytes per sample
	fileSize := 44 + dataSize

	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	putLE32(header[4:8], uint32(fileSize-8))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	putLE32(header[16:20], 16)        // Subchunk1Size
	putLE16(header[20:22], 1)         // AudioFormat (PCM)
	putLE16(header[22:24], 1)         // NumChannels
	putLE32(header[24:28], uint32(sampleRate))
	putLE32(header[28:32], uint32(sampleRate*2)) // ByteRate
	putLE16(header[32:34], 2)                    // BlockAlign
	putLE16(header[34:36], 16)                   // BitsPerSample
	copy(header[36:40], "data")
	putLE32(header[40:44], uint32(dataSize))

	if _, err := f.Write(header); err != nil {
		return err
	}

	// Write silence (zeros)
	silence := make([]byte, dataSize)
	_, err = f.Write(silence)
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

