package voice

import (
	"archive/tar"
	"archive/zip"
	"bytes"
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
	"sync"
	"time"
)

// TTSInstallerConfig holds configuration for the piper TTS installer.
type TTSInstallerConfig struct {
	// DataDir is where piper binary and models are stored.
	// Defaults to ~/.crayfish/piper (or /var/lib/crayfish/piper for root).
	DataDir string

	// PiperReleaseURL is the base URL for piper releases.
	PiperReleaseURL string

	// ModelBaseURL is the HuggingFace base URL for piper voice models.
	ModelBaseURL string
}

// DefaultTTSInstallerConfig returns sensible defaults.
func DefaultTTSInstallerConfig() TTSInstallerConfig {
	dataDir := filepath.Join(userHomeDir(), ".crayfish", "piper")
	if runtime.GOOS == "linux" && os.Getuid() == 0 {
		dataDir = "/var/lib/crayfish/piper"
	}
	return TTSInstallerConfig{
		DataDir:         dataDir,
		PiperReleaseURL: "https://github.com/rhasspy/piper/releases/latest/download",
		ModelBaseURL:    "https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0",
	}
}

// TTSInstallStatus represents the current TTS installation state.
type TTSInstallStatus int

const (
	TTSStatusNotStarted TTSInstallStatus = iota
	TTSStatusDownloadingBinary
	TTSStatusDownloadingModel
	TTSStatusComplete
	TTSStatusFailed
	TTSStatusNotSupported
)

func (s TTSInstallStatus) String() string {
	switch s {
	case TTSStatusNotStarted:
		return "not_started"
	case TTSStatusDownloadingBinary:
		return "downloading_binary"
	case TTSStatusDownloadingModel:
		return "downloading_model"
	case TTSStatusComplete:
		return "complete"
	case TTSStatusFailed:
		return "failed"
	case TTSStatusNotSupported:
		return "not_supported"
	default:
		return "unknown"
	}
}

// TTSInstallProgress contains current TTS installation progress.
type TTSInstallProgress struct {
	Status   TTSInstallStatus `json:"status"`
	Progress float64          `json:"progress"` // 0.0 to 1.0
	Message  string           `json:"message"`
	Error    string           `json:"error,omitempty"`
}

// TTSInstaller handles automatic piper TTS setup.
type TTSInstaller struct {
	config TTSInstallerConfig
	logger *slog.Logger

	mu       sync.Mutex
	status   TTSInstallStatus
	progress float64
	message  string
}

// NewTTSInstaller creates a new piper TTS installer.
func NewTTSInstaller(cfg TTSInstallerConfig, logger *slog.Logger) *TTSInstaller {
	return &TTSInstaller{
		config: cfg,
		logger: logger,
		status: TTSStatusNotStarted,
	}
}

// Progress returns current installation progress.
func (i *TTSInstaller) Progress() TTSInstallProgress {
	i.mu.Lock()
	defer i.mu.Unlock()
	return TTSInstallProgress{
		Status:   i.status,
		Progress: i.progress,
		Message:  i.message,
	}
}

// BinaryPath returns the expected path to the piper binary.
func (i *TTSInstaller) BinaryPath() string {
	return filepath.Join(i.config.DataDir, piperBinaryName())
}

// ModelPath returns the expected path to a voice model file.
func (i *TTSInstaller) ModelPath(modelName string) string {
	return filepath.Join(i.config.DataDir, "models", modelName+".onnx")
}

// EspeakDataDir returns the directory for espeak-ng-data (required by piper).
func (i *TTSInstaller) EspeakDataDir() string {
	return filepath.Join(i.config.DataDir, "espeak-ng-data")
}

// RecommendedModel returns the voice model appropriate for the detected hardware.
// ARMv7 (Pi 2/3) uses the low-quality model for speed; everything else uses medium.
func (i *TTSInstaller) RecommendedModel() string {
	if runtime.GOARCH == "arm" {
		return "en_US-lessac-low"
	}
	return "en_US-lessac-medium"
}

// IsInstalled checks whether piper binary and a voice model are already present.
func (i *TTSInstaller) IsInstalled() bool {
	if _, err := os.Stat(i.BinaryPath()); err != nil {
		return false
	}
	model := i.RecommendedModel()
	if _, err := os.Stat(i.ModelPath(model)); err != nil {
		return false
	}
	return true
}

// RecommendedPiperModel returns the best piper voice model for the current hardware.
// ARMv7 (Pi 2/3) uses the low-quality model for speed; everything else uses medium.
func RecommendedPiperModel() string {
	if runtime.GOARCH == "arm" {
		return "en_US-lessac-low"
	}
	return "en_US-lessac-medium"
}

// CanInstallTTS returns true if this architecture is supported by piper.
// ARMv6 (Pi Zero, Pi 1) is not supported by the piper release binaries.
func CanInstallTTS() bool {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm" {
		// Check for ARMv6 — piper only ships armv7l and above.
		d := detectARMVersion()
		if d == 6 {
			return false
		}
	}
	return true
}

// Install performs the full piper TTS installation.
// Safe to call multiple times — skips if already installed.
func (i *TTSInstaller) Install(ctx context.Context) error {
	i.mu.Lock()
	if i.status == TTSStatusComplete {
		i.mu.Unlock()
		return nil
	}
	i.mu.Unlock()

	if !CanInstallTTS() {
		i.setStatus(TTSStatusNotSupported, 0, "Piper TTS not supported on ARMv6")
		return fmt.Errorf("piper TTS not supported on ARMv6 hardware")
	}

	// Create directories.
	modelsDir := filepath.Join(i.config.DataDir, "models")
	if err := os.MkdirAll(modelsDir, 0755); err != nil {
		return i.fail("create dirs: %w", err)
	}

	// Step 0: Ensure libespeak-ng1 is installed (required by piper on Linux).
	if runtime.GOOS == "linux" {
		if err := i.ensureLibEspeakNG(ctx); err != nil {
			i.logger.Warn("could not install libespeak-ng1 (piper may fail)", "error", err)
		}
	}

	// Step 1: Download piper binary tarball.
	if _, err := os.Stat(i.BinaryPath()); err != nil {
		i.setStatus(TTSStatusDownloadingBinary, 0.1, "Downloading Piper TTS engine...")
		if err := i.downloadBinary(ctx); err != nil {
			return i.fail("download piper binary: %w", err)
		}
		i.logger.Info("piper binary installed", "path", i.BinaryPath())
	}

	// Step 2: Download voice model.
	model := i.RecommendedModel()
	modelPath := i.ModelPath(model)
	modelJSON := modelPath + ".json"

	if _, err := os.Stat(modelPath); err != nil {
		i.setStatus(TTSStatusDownloadingModel, 0.5, fmt.Sprintf("Downloading voice model (%s)...", model))
		if err := i.downloadModel(ctx, model); err != nil {
			return i.fail("download voice model: %w", err)
		}
		i.logger.Info("voice model installed", "model", model, "path", modelPath)
	}

	// Ensure the .json config is present (piper requires it alongside the .onnx).
	if _, err := os.Stat(modelJSON); err != nil {
		i.setStatus(TTSStatusDownloadingModel, 0.9, "Downloading model config...")
		if err := i.downloadModelConfig(ctx, model); err != nil {
			// Non-fatal: piper can sometimes run without the json config.
			i.logger.Warn("voice model config download failed (non-fatal)", "error", err)
		}
	}

	i.setStatus(TTSStatusComplete, 1.0, "Piper TTS ready!")
	i.logger.Info("piper TTS installation complete",
		"binary", i.BinaryPath(),
		"model", modelPath)
	return nil
}

// piperPlatform returns the platform string used in piper release filenames.
func piperPlatform() (string, error) {
	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "linux_x86_64", nil
		case "arm64":
			return "linux_aarch64", nil
		case "arm":
			return "linux_armv7l", nil
		}
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			return "macos_x86_64", nil
		case "arm64":
			return "macos_aarch64", nil
		}
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			return "windows_amd64", nil
		case "arm64":
			return "windows_arm64", nil
		}
	}
	return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
}

// piperBinaryName returns the correct piper binary filename for the current OS.
func piperBinaryName() string {
	if runtime.GOOS == "windows" {
		return "piper.exe"
	}
	return "piper"
}

// downloadBinary downloads and extracts the piper binary tarball.
func (i *TTSInstaller) downloadBinary(ctx context.Context) error {
	platform, err := piperPlatform()
	if err != nil {
		return err
	}

	// Windows releases are .zip; Linux/macOS are .tar.gz
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}

	url := fmt.Sprintf("%s/piper_%s%s", i.config.PiperReleaseURL, platform, ext)
	i.logger.Info("downloading piper binary", "url", url)

	client := &http.Client{Timeout: 10 * time.Minute}
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
		return fmt.Errorf("HTTP %d downloading piper binary", resp.StatusCode)
	}

	if runtime.GOOS == "windows" {
		// Write zip to temp file then extract (archive/zip needs io.ReaderAt).
		tmp, err := os.CreateTemp("", "piper-*.zip")
		if err != nil {
			return fmt.Errorf("create temp zip: %w", err)
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if _, err := io.Copy(tmp, resp.Body); err != nil {
			tmp.Close()
			return fmt.Errorf("download zip: %w", err)
		}
		tmp.Close()
		return i.extractZipStripped(tmpPath, i.config.DataDir)
	}

	// Linux/macOS: extract tarball directly.
	if err := i.extractTarGzStripped(resp.Body, i.config.DataDir); err != nil {
		return err
	}

	// Create versioned .so symlinks on Linux so the piper binary can find
	// its bundled shared libraries without system ldconfig.
	if runtime.GOOS == "linux" {
		return i.createSoSymlinks(i.config.DataDir)
	}
	return nil
}

// extractTarGzStripped extracts a .tar.gz, stripping the first path component.
func (i *TTSInstaller) extractTarGzStripped(r io.Reader, destDir string) error {
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

		// Strip the first path component (e.g. "piper/piper" → "piper").
		name := stripFirstComponent(header.Name)
		if name == "" {
			continue
		}

		target := filepath.Join(destDir, name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
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

// modelHFPath returns the HuggingFace subdirectory path for a model name.
// e.g. "en_US-lessac-medium" → "en/en_US/lessac/medium/en_US-lessac-medium"
func modelHFPath(modelName string) (string, error) {
	// Model names follow the pattern: {lang}_{country}-{speaker}-{quality}
	// e.g. en_US-lessac-medium, en_GB-jenny-medium, de_DE-thorsten-medium
	parts := splitModelName(modelName)
	if parts == nil {
		return "", fmt.Errorf("cannot parse model name %q", modelName)
	}
	lang := parts[0]      // e.g. "en"
	langCC := parts[1]    // e.g. "en_US"
	speaker := parts[2]   // e.g. "lessac"
	quality := parts[3]   // e.g. "medium"
	return fmt.Sprintf("%s/%s/%s/%s/%s", lang, langCC, speaker, quality, modelName), nil
}

// splitModelName splits "en_US-lessac-medium" into ["en", "en_US", "lessac", "medium"].
func splitModelName(name string) []string {
	// Find the first '-' which separates langCC from speaker
	dashIdx := -1
	for idx, c := range name {
		if c == '-' {
			dashIdx = idx
			break
		}
	}
	if dashIdx < 0 {
		return nil
	}
	langCC := name[:dashIdx] // "en_US"
	rest := name[dashIdx+1:] // "lessac-medium"

	// Split lang from country code
	underIdx := -1
	for idx, c := range langCC {
		if c == '_' {
			underIdx = idx
			break
		}
	}
	if underIdx < 0 {
		return nil
	}
	lang := langCC[:underIdx] // "en"

	// Split speaker from quality
	lastDash := -1
	for idx, c := range rest {
		if c == '-' {
			lastDash = idx
		}
	}
	if lastDash < 0 {
		return nil
	}
	speaker := rest[:lastDash]  // "lessac"
	quality := rest[lastDash+1:] // "medium"

	return []string{lang, langCC, speaker, quality}
}

// downloadModel downloads the .onnx voice model file.
func (i *TTSInstaller) downloadModel(ctx context.Context, modelName string) error {
	hfPath, err := modelHFPath(modelName)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/%s.onnx", i.config.ModelBaseURL, hfPath)
	dest := i.ModelPath(modelName)

	i.logger.Info("downloading voice model", "model", modelName, "url", url)
	return i.downloadFile(ctx, url, dest, func(downloaded, total int64) {
		if total > 0 {
			pct := float64(downloaded) / float64(total)
			sizeMB := downloaded / 1024 / 1024
			totalMB := total / 1024 / 1024
			i.setStatus(TTSStatusDownloadingModel, 0.5+pct*0.4,
				fmt.Sprintf("Downloading voice model... %dMB / %dMB", sizeMB, totalMB))
		}
	})
}

// downloadModelConfig downloads the .onnx.json config file (required by piper).
func (i *TTSInstaller) downloadModelConfig(ctx context.Context, modelName string) error {
	hfPath, err := modelHFPath(modelName)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/%s.onnx.json", i.config.ModelBaseURL, hfPath)
	dest := i.ModelPath(modelName) + ".json"
	return i.downloadFile(ctx, url, dest, nil)
}

// downloadFile downloads a URL to a destination file, with optional progress callback.
func (i *TTSInstaller) downloadFile(ctx context.Context, url, dest string, progress func(downloaded, total int64)) error {
	client := &http.Client{Timeout: 30 * time.Minute}
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
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	total := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			downloaded += int64(n)
			if progress != nil {
				progress(downloaded, total)
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

	return os.Rename(tmp, dest)
}

func (i *TTSInstaller) setStatus(status TTSInstallStatus, progress float64, message string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.status = status
	i.progress = progress
	i.message = message
}

func (i *TTSInstaller) fail(format string, args ...interface{}) error {
	err := fmt.Errorf(format, args...)
	i.mu.Lock()
	i.status = TTSStatusFailed
	i.message = err.Error()
	i.mu.Unlock()
	i.logger.Error("piper TTS installation failed", "error", err)
	return err
}

// createSoSymlinks creates short-name .so symlinks for versioned .so.X.Y.Z files.
// e.g. libpiper_phonemize.so.1.2.0 → libpiper_phonemize.so.1
// The piper binary uses SONAME lookups (short form) which won't resolve without these.
func (i *TTSInstaller) createSoSymlinks(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		// Match files like libfoo.so.1.2.3 but not libfoo.so.1 (already short)
		if !hasAtLeastTwoVersionParts(name) {
			continue
		}
		// Derive the short name: strip last version component
		// libfoo.so.1.2.3 → libfoo.so.1
		short := shortenSoName(name)
		if short == "" || short == name {
			continue
		}
		target := filepath.Join(dir, short)
		// Don't overwrite existing symlinks/files.
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if err := os.Symlink(name, target); err != nil {
			i.logger.Warn("could not create .so symlink", "link", target, "target", name, "error", err)
		} else {
			i.logger.Debug("created .so symlink", "link", short, "target", name)
		}
	}
	return nil
}

// hasAtLeastTwoVersionParts reports whether s has a .so.X.Y or .so.X.Y.Z pattern.
func hasAtLeastTwoVersionParts(s string) bool {
	idx := 0
	// Find ".so."
	for i := 0; i+4 <= len(s); i++ {
		if s[i:i+4] == ".so." {
			idx = i + 4
			break
		}
	}
	if idx == 0 {
		return false
	}
	// Count dots after ".so."
	dots := 0
	for _, c := range s[idx:] {
		if c == '.' {
			dots++
		}
	}
	return dots >= 1
}

// shortenSoName removes the last version component from a .so version string.
// libfoo.so.1.2.3 → libfoo.so.1
func shortenSoName(s string) string {
	// Find ".so."
	soIdx := -1
	for i := 0; i+4 <= len(s); i++ {
		if s[i:i+4] == ".so." {
			soIdx = i + 4
			break
		}
	}
	if soIdx < 0 {
		return ""
	}
	// Take only the first version component after ".so."
	rest := s[soIdx:]
	firstDot := -1
	for i, c := range rest {
		if c == '.' {
			firstDot = i
			break
		}
	}
	if firstDot < 0 {
		return "" // Only one component, already short
	}
	return s[:soIdx] + rest[:firstDot]
}

// extractZipStripped extracts a .zip archive, stripping the first path component.
// Used for piper on Windows since the Windows release ships as .zip, not .tar.gz.
func (i *TTSInstaller) extractZipStripped(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		name := stripFirstComponent(f.Name)
		if name == "" {
			continue
		}
		target := filepath.Join(destDir, name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ensureLibEspeakNG installs libespeak-ng1 via apt-get if not already present.
// Piper's binary is dynamically linked against it on Linux.
// On Windows, piper bundles its own espeak-ng DLL — no system install needed.
func (i *TTSInstaller) ensureLibEspeakNG(ctx context.Context) error {
	// Check if already installed by looking for the .so file.
	checkCmd := exec.CommandContext(ctx, "dpkg", "-s", "libespeak-ng1")
	if err := checkCmd.Run(); err == nil {
		return nil // already installed
	}

	i.logger.Info("installing libespeak-ng1 (required by piper TTS)")
	installCmd := exec.CommandContext(ctx, "sudo", "apt-get", "install", "-y", "-qq", "libespeak-ng1")
	var stderr bytes.Buffer
	installCmd.Stderr = &stderr
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("apt-get install libespeak-ng1: %w (stderr: %s)", err, stderr.String())
	}
	i.logger.Info("libespeak-ng1 installed")
	return nil
}

// stripFirstComponent strips the first directory component from a tar path.
// e.g. "piper/piper" → "piper", "piper/espeak-ng-data/en" → "espeak-ng-data/en"
func stripFirstComponent(name string) string {
	for i, c := range name {
		if c == '/' && i < len(name)-1 {
			return name[i+1:]
		}
	}
	return ""
}

// detectARMVersion reads /proc/cpuinfo to determine ARM version (6, 7, or 8).
// Returns 7 as a safe default if detection fails.
func detectARMVersion() int {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 7
	}
	content := string(data)
	if containsString(content, "ARMv6") || containsString(content, "armv6") {
		return 6
	}
	if containsString(content, "ARMv7") || containsString(content, "armv7") {
		return 7
	}
	return 7
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
