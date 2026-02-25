// Crayfish — Accessible AI for everyone.
// Runs on a Raspberry Pi. Your data stays home.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/app"
	"github.com/KekwanuLabs/crayfish/internal/setup"
)

// Build-time variables injected via -ldflags.
// These defaults are used for `go run` or `go build` without ldflags.
// Release builds use: make build (which injects git tag as version).
var (
	version   = "dev"
	commit    = "dev"
	buildTime = "unknown"
)

func main() {
	// Handle flags before anything else.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			fmt.Printf("Crayfish %s (%s) built %s\n", version, commit, buildTime)
			fmt.Println("Accessible AI for everyone.")
			os.Exit(0)
		case "--help", "-h", "help":
			printHelp()
			os.Exit(0)
		}
	}

	startTime := time.Now()

	// Configure structured logging.
	logLevel := slog.LevelInfo
	if os.Getenv("CRAYFISH_DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))

	logger.Info("Crayfish starting", "version", version, "commit", commit, "pid", os.Getpid())

	// Load config.
	cfg := app.LoadConfig(logger)

	// Signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// If no API key, launch the setup wizard.
	if cfg.NeedsSetup() {
		logger.Info("no API key configured, starting setup wizard")

		// Determine config paths. Prefer XDG config dir, fall back to current dir.
		configPath := "crayfish.yaml"
		envPath := "env"
		if home, err := os.UserHomeDir(); err == nil {
			xdgConfig := home + "/.config/crayfish"
			if err := os.MkdirAll(xdgConfig, 0755); err == nil {
				configPath = xdgConfig + "/crayfish.yaml"
				envPath = xdgConfig + "/env"
			}
		}

		wizard := setup.NewWizard(setup.WizardConfig{
			ListenAddr: cfg.ListenAddr,
			ConfigPath: configPath,
			EnvPath:    envPath,
			Version:    version,
		}, logger.With("component", "setup"))

		result, err := wizard.Start(ctx)
		if err != nil {
			logger.Error("setup wizard failed", "error", err)
			os.Exit(1)
		}

		// Apply the wizard's config to our running config.
		cfg.Name = result.Name
		cfg.Personality = result.Personality
		cfg.Provider = result.Provider
		cfg.APIKey = result.APIKey
		cfg.Endpoint = result.Endpoint
		cfg.Model = result.Model
		cfg.TelegramToken = result.TelegramToken
		cfg.BraveAPIKey = result.BraveAPIKey
		cfg.AutoUpdate = result.AutoUpdate

		logger.Info("setup complete", "name", result.Name)
	}

	// Create and start the application.
	crayfish := app.New(cfg, version, logger)

	if err := crayfish.Start(ctx); err != nil {
		logger.Error("fatal error", "error", err)
		os.Exit(1)
	}

	// Wait for shutdown signal.
	<-ctx.Done()
	logger.Info("shutdown signal received")

	crayfish.Stop()

	logger.Info("Crayfish stopped", "uptime", time.Since(startTime).String())
}

func printHelp() {
	fmt.Printf("Crayfish %s — Agentic AI for the Rest of the World\n\n", version)
	fmt.Println("Usage: crayfish [command]")
	fmt.Println("")
	fmt.Println("Commands:")
	fmt.Println("  (none)      Start the Crayfish gateway")
	fmt.Println("  version     Print version and exit")
	fmt.Println("  help        Print this help")
	fmt.Println("")
	fmt.Println("Environment:")
	fmt.Println("  CRAYFISH_API_KEY          LLM API key (required)")
	fmt.Println("  CRAYFISH_MODEL            Model name (e.g., claude-sonnet-4-20250514)")
	fmt.Println("  CRAYFISH_PROVIDER         Provider: anthropic, openai, groq, ollama, ...")
	fmt.Println("  CRAYFISH_ENDPOINT         Custom API endpoint (for Ollama, vLLM, etc.)")
	fmt.Println("  CRAYFISH_LISTEN           Listen address (default: :8119)")
	fmt.Println("  CRAYFISH_DB_PATH          Database path (default: crayfish.db)")
	fmt.Println("  CRAYFISH_TELEGRAM_TOKEN   Telegram bot token")
	fmt.Println("  CRAYFISH_BRAVE_API_KEY    Brave Search API key")
	fmt.Println("  CRAYFISH_GMAIL_USER       Gmail address for email tools")
	fmt.Println("  CRAYFISH_GMAIL_APP_PASSWORD  Gmail app password")
	fmt.Println("  CRAYFISH_DEBUG            Enable debug logging (any value)")
	fmt.Println("")
	fmt.Println("Config file: crayfish.yaml or /etc/crayfish/crayfish.yaml")
	fmt.Println("Env vars override config file values.")
	fmt.Println("")
	fmt.Println("Accessible AI for everyone.")
}
