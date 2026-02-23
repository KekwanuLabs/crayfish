package app

import (
	"log/slog"
	"os"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestDefaultConfigContinuityValues(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.ContinuityEnabled {
		t.Error("ContinuityEnabled should default to true")
	}
	if cfg.SessionResumeMinutes != 30 {
		t.Errorf("SessionResumeMinutes = %d, want 30", cfg.SessionResumeMinutes)
	}
	if cfg.SnapshotsPerSession != 3 {
		t.Errorf("SnapshotsPerSession = %d, want 3", cfg.SnapshotsPerSession)
	}
}

func TestContinuityEnvOverrides(t *testing.T) {
	// Save and restore env vars.
	for _, key := range []string{
		"CRAYFISH_CONTINUITY_ENABLED",
		"CRAYFISH_SESSION_RESUME_MINUTES",
		"CRAYFISH_SNAPSHOTS_PER_SESSION",
	} {
		if orig, ok := os.LookupEnv(key); ok {
			defer os.Setenv(key, orig)
		} else {
			defer os.Unsetenv(key)
		}
	}

	os.Setenv("CRAYFISH_CONTINUITY_ENABLED", "false")
	os.Setenv("CRAYFISH_SESSION_RESUME_MINUTES", "15")
	os.Setenv("CRAYFISH_SNAPSHOTS_PER_SESSION", "5")

	cfg := LoadConfig(testLogger())

	if cfg.ContinuityEnabled {
		t.Error("ContinuityEnabled should be false after env override")
	}
	if cfg.SessionResumeMinutes != 15 {
		t.Errorf("SessionResumeMinutes = %d, want 15", cfg.SessionResumeMinutes)
	}
	if cfg.SnapshotsPerSession != 5 {
		t.Errorf("SnapshotsPerSession = %d, want 5", cfg.SnapshotsPerSession)
	}
}

func TestNeedsSetupUnchanged(t *testing.T) {
	// Verify our config additions didn't break the setup check.
	cfg := DefaultConfig()
	// No API key and no local provider → needs setup.
	if !cfg.NeedsSetup() {
		t.Error("DefaultConfig with no API key should need setup")
	}

	cfg.APIKey = "sk-test"
	if cfg.NeedsSetup() {
		t.Error("Config with API key should not need setup")
	}
}
