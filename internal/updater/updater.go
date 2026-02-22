// Package updater implements automatic self-updating for Crayfish.
// It checks GitHub releases periodically and performs atomic binary replacement.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// githubReleasesURL is the API endpoint for Crayfish releases.
	githubReleasesURL = "https://api.github.com/repos/KekwanuLabs/crayfish/releases/latest"

	// defaultCheckInterval is how often to check for updates.
	defaultCheckInterval = 6 * time.Hour

	// downloadTimeout is the maximum time for downloading an update.
	downloadTimeout = 5 * time.Minute
)

// Config holds updater configuration.
type Config struct {
	Enabled        bool   // Whether auto-update is enabled.
	Channel        string // "stable" or "beta".
	CurrentVersion string // Current running version (e.g., "0.4.0").
	BinaryPath     string // Path to the running binary (for replacement).
	BackupDir      string // Where to store the previous binary for rollback.
}

// NotifyFunc is called when an update event occurs.
type NotifyFunc func(msg string)

// Updater checks for and applies updates from GitHub releases.
type Updater struct {
	config Config
	notify NotifyFunc
	logger *slog.Logger
	client *http.Client

	mu      sync.Mutex
	stopCh  chan struct{}
	wg      sync.WaitGroup
	lastCheck time.Time
}

// New creates a new Updater.
func New(cfg Config, notify NotifyFunc, logger *slog.Logger) *Updater {
	if cfg.BinaryPath == "" {
		var err error
		cfg.BinaryPath, err = os.Executable()
		if err != nil {
			logger.Warn("could not determine binary path, using default", "error", err)
			cfg.BinaryPath = "/usr/local/bin/crayfish"
		}
	}
	if cfg.BackupDir == "" {
		cfg.BackupDir = filepath.Dir(cfg.BinaryPath)
	}
	if cfg.Channel == "" {
		cfg.Channel = "stable"
	}

	return &Updater{
		config: cfg,
		notify: notify,
		logger: logger,
		client: &http.Client{Timeout: 30 * time.Second},
		stopCh: make(chan struct{}),
	}
}

// Start begins the background update check loop.
func (u *Updater) Start(ctx context.Context) {
	if !u.config.Enabled {
		u.logger.Info("auto-update disabled")
		return
	}

	u.wg.Add(1)
	go u.loop(ctx)
	u.logger.Info("auto-updater started",
		"current_version", u.config.CurrentVersion,
		"channel", u.config.Channel,
		"check_interval", defaultCheckInterval)
}

// Stop gracefully stops the updater.
func (u *Updater) Stop() {
	close(u.stopCh)
	u.wg.Wait()
}

// CheckNow manually triggers an update check. Returns the latest version or empty string.
func (u *Updater) CheckNow(ctx context.Context) (string, error) {
	release, err := u.fetchLatestRelease(ctx)
	if err != nil {
		return "", err
	}
	return release.TagName, nil
}

func (u *Updater) loop(ctx context.Context) {
	defer u.wg.Done()

	// Check soon after startup (1 minute delay to let everything settle).
	initialDelay := time.NewTimer(1 * time.Minute)
	select {
	case <-ctx.Done():
		initialDelay.Stop()
		return
	case <-u.stopCh:
		initialDelay.Stop()
		return
	case <-initialDelay.C:
		u.checkAndUpdate(ctx)
	}

	ticker := time.NewTicker(defaultCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-u.stopCh:
			return
		case <-ticker.C:
			u.checkAndUpdate(ctx)
		}
	}
}

func (u *Updater) checkAndUpdate(ctx context.Context) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.lastCheck = time.Now()

	release, err := u.fetchLatestRelease(ctx)
	if err != nil {
		u.logger.Warn("update check failed", "error", err)
		return
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	if !isNewer(latestVersion, u.config.CurrentVersion) {
		u.logger.Debug("already up to date", "current", u.config.CurrentVersion, "latest", latestVersion)
		return
	}

	u.logger.Info("update available", "current", u.config.CurrentVersion, "latest", latestVersion)

	// Find the right asset for our platform.
	assetURL := u.findAsset(release)
	if assetURL == "" {
		u.logger.Warn("no compatible binary found in release", "version", latestVersion)
		return
	}

	// Download and apply.
	if err := u.downloadAndApply(ctx, assetURL, latestVersion); err != nil {
		u.logger.Error("update failed", "error", err, "version", latestVersion)
		if u.notify != nil {
			u.notify(fmt.Sprintf("Update to v%s failed: %v", latestVersion, err))
		}
		return
	}

	u.logger.Info("update applied, restarting", "version", latestVersion)
	if u.notify != nil {
		u.notify(fmt.Sprintf("Updated to v%s. Restarting...", latestVersion))
	}

	// Exit cleanly — systemd will restart us with the new binary.
	os.Exit(0)
}

func (u *Updater) downloadAndApply(ctx context.Context, url, version string) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	// Download to temp file.
	tmpPath := filepath.Join(os.TempDir(), "crayfish-update")
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	// Limit download to 50MB (safety against huge/corrupted files).
	limited := io.LimitedReader{R: resp.Body, N: 50 * 1024 * 1024}
	if _, err := io.Copy(tmpFile, &limited); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	tmpFile.Close()

	// Make executable.
	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}

	// Backup current binary.
	backupPath := filepath.Join(u.config.BackupDir, "crayfish.prev")
	if u.config.BinaryPath != "" {
		os.Rename(u.config.BinaryPath, backupPath) // Best-effort backup.
	}

	// Atomic replacement: rename is atomic on the same filesystem.
	if err := os.Rename(tmpPath, u.config.BinaryPath); err != nil {
		// Try copy instead (cross-filesystem).
		if err := copyFile(tmpPath, u.config.BinaryPath); err != nil {
			// Restore backup.
			os.Rename(backupPath, u.config.BinaryPath)
			os.Remove(tmpPath)
			return fmt.Errorf("replace binary: %w", err)
		}
		os.Remove(tmpPath)
	}

	u.logger.Info("binary replaced",
		"path", u.config.BinaryPath,
		"backup", backupPath,
		"version", version)

	return nil
}

// findAsset finds the download URL for the current platform.
func (u *Updater) findAsset(release *githubRelease) string {
	// Build expected binary name.
	os_ := runtime.GOOS
	arch := runtime.GOARCH

	// Map Go arch names to our binary naming convention.
	archName := arch
	switch arch {
	case "arm":
		// Check GOARM for v6 vs v7. Default to v7.
		archName = "armv7"
	case "arm64":
		archName = "arm64"
	case "amd64":
		archName = "amd64"
	}

	expectedName := fmt.Sprintf("crayfish-%s-%s", os_, archName)

	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, expectedName) {
			return asset.DownloadURL
		}
	}

	// Also try shorter names (legacy naming).
	shortName := fmt.Sprintf("crayfish-%s", archName)
	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, shortName) {
			return asset.DownloadURL
		}
	}

	return ""
}

// fetchLatestRelease fetches the latest release info from GitHub API.
func (u *Updater) fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", githubReleasesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "Crayfish/"+u.config.CurrentVersion)

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("parse release: %w", err)
	}

	return &release, nil
}

// githubRelease is a subset of the GitHub Releases API response.
type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Name       string        `json:"name"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// isNewer returns true if latest is a newer version than current.
// Simple string comparison works for semver (0.3.0 < 0.4.0 < 1.0.0).
func isNewer(latest, current string) bool {
	// Strip any -dev, -beta suffixes for comparison.
	latest = strings.Split(latest, "-")[0]
	current = strings.Split(current, "-")[0]

	latestParts := strings.Split(latest, ".")
	currentParts := strings.Split(current, ".")

	// Pad to same length.
	for len(latestParts) < 3 {
		latestParts = append(latestParts, "0")
	}
	for len(currentParts) < 3 {
		currentParts = append(currentParts, "0")
	}

	for i := 0; i < 3; i++ {
		l := parseVersionPart(latestParts[i])
		c := parseVersionPart(currentParts[i])
		if l > c {
			return true
		}
		if l < c {
			return false
		}
	}
	return false
}

func parseVersionPart(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			break
		}
	}
	return n
}

// copyFile copies src to dst.
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

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Chmod(dst, 0755)
}
