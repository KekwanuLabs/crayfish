package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestDefaultConfigContinuityValues(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.ContinuityEnabled {
		t.Error("ContinuityEnabled should default to true")
	}
	if cfg.SessionResumeMinutes != 5 {
		t.Errorf("SessionResumeMinutes = %d, want 5", cfg.SessionResumeMinutes)
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

func TestSaveConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crayfish.yaml")

	cfg := DefaultConfig()
	cfg.ConfigPath = path
	cfg.Name = "TestBot"
	cfg.APIKey = "sk-test-key-12345"
	cfg.Provider = "anthropic"
	cfg.Model = "claude-sonnet-4-20250514"
	cfg.TelegramToken = "12345:ABCDEF"
	cfg.ContinuityEnabled = false
	cfg.SessionResumeMinutes = 15

	if err := cfg.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Read back and verify.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}

	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}

	if loaded.Name != "TestBot" {
		t.Errorf("Name = %q, want TestBot", loaded.Name)
	}
	if loaded.APIKey != "sk-test-key-12345" {
		t.Errorf("APIKey = %q, want sk-test-key-12345", loaded.APIKey)
	}
	if loaded.ContinuityEnabled {
		t.Error("ContinuityEnabled should be false")
	}
	if loaded.SessionResumeMinutes != 15 {
		t.Errorf("SessionResumeMinutes = %d, want 15", loaded.SessionResumeMinutes)
	}
	// ConfigPath should NOT be in the YAML (yaml:"-" tag).
	if loaded.ConfigPath != "" {
		t.Errorf("ConfigPath should not be serialized, got %q", loaded.ConfigPath)
	}
}

func TestSaveConfigCreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "crayfish.yaml")

	cfg := DefaultConfig()
	cfg.ConfigPath = path

	if err := cfg.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig with nested dir failed: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("config file was not created")
	}
}

func TestSaveConfigEmptyPathError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfigPath = ""

	if err := cfg.SaveConfig(); err == nil {
		t.Error("SaveConfig with empty path should return error")
	}
}

func TestConfigPathTrackedInLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crayfish.yaml")

	// Write a minimal config file.
	os.WriteFile(path, []byte("name: FromFile\n"), 0600)

	// Set env to point to this file.
	old, hadOld := os.LookupEnv("CRAYFISH_CONFIG")
	os.Setenv("CRAYFISH_CONFIG", path)
	defer func() {
		if hadOld {
			os.Setenv("CRAYFISH_CONFIG", old)
		} else {
			os.Unsetenv("CRAYFISH_CONFIG")
		}
	}()

	cfg := LoadConfig(testLogger())

	if cfg.ConfigPath != path {
		t.Errorf("ConfigPath = %q, want %q", cfg.ConfigPath, path)
	}
	if cfg.Name != "FromFile" {
		t.Errorf("Name = %q, want FromFile", cfg.Name)
	}
}
