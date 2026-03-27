// Package tunnel manages a Cloudflare Tunnel process for exposing Crayfish
// to the internet (required for Twilio ConversationRelay phone calls).
//
// It starts cloudflared as a supervised child process, parses the assigned
// public URL from its output, and calls an OnURL callback whenever the URL
// is known or changes. Users never need to run cloudflared or configure URLs.
package tunnel

import (
	"bufio"
	"context"
	"log/slog"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// OnURLFunc is called whenever the tunnel URL is established or changes.
type OnURLFunc func(url string)

// Manager supervises a cloudflared quick-tunnel process.
type Manager struct {
	localAddr string // e.g. "http://localhost:8119"
	onURL     OnURLFunc
	logger    *slog.Logger
}

// New creates a tunnel manager.
// localAddr is the local service to expose (e.g. "http://localhost:8119").
// onURL is called once the public URL is known.
func New(localAddr string, onURL OnURLFunc, logger *slog.Logger) *Manager {
	return &Manager{
		localAddr: localAddr,
		onURL:     onURL,
		logger:    logger,
	}
}

// IsAvailable returns true if the cloudflared binary is installed.
func IsAvailable() bool {
	_, err := exec.LookPath("cloudflared")
	return err == nil
}

// Start runs the tunnel supervisor loop. It starts cloudflared, watches for
// the assigned URL, and restarts on crash. Runs until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	for {
		m.logger.Info("starting Cloudflare Tunnel", "local", m.localAddr)
		err := m.runOnce(ctx)

		if ctx.Err() != nil {
			m.logger.Info("tunnel manager stopped")
			return
		}

		if err != nil {
			m.logger.Warn("tunnel exited, restarting in 10s", "error", err)
		} else {
			m.logger.Info("tunnel exited cleanly, restarting in 10s")
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// urlPattern matches the URL that cloudflared prints when the tunnel is ready.
// Format varies by version; covers both old and new cloudflared output.
var urlPattern = regexp.MustCompile(`https://[a-z0-9\-]+\.trycloudflare\.com`)

// runOnce starts cloudflared once and blocks until it exits.
func (m *Manager) runOnce(ctx context.Context) error {
	args := []string{"tunnel", "--url", m.localAddr, "--no-autoupdate"}
	// Suppress file logging on Unix; /dev/null doesn't exist on Windows.
	if runtime.GOOS != "windows" {
		args = append(args, "--logfile", "/dev/null")
	}
	cmd := exec.CommandContext(ctx, "cloudflared", args...)

	// cloudflared logs to stderr.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// Scan stderr for the tunnel URL.
	urlFound := false
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()

		// New cloudflared (2024+) prints the URL on its own line.
		if strings.HasPrefix(strings.TrimSpace(line), "https://") {
			candidate := strings.TrimSpace(line)
			if urlPattern.MatchString(candidate) && !urlFound {
				urlFound = true
				m.logger.Info("tunnel established", "url", candidate)
				if m.onURL != nil {
					go m.onURL(candidate)
				}
			}
			continue
		}

		// Older cloudflared embeds it in a log line.
		if match := urlPattern.FindString(line); match != "" && !urlFound {
			urlFound = true
			m.logger.Info("tunnel established", "url", match)
			if m.onURL != nil {
				go m.onURL(match)
			}
		}
	}

	return cmd.Wait()
}
