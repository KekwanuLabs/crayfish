package security

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE sessions (
			id            TEXT PRIMARY KEY,
			channel       TEXT NOT NULL,
			user_id       TEXT NOT NULL,
			trust_tier    INTEGER NOT NULL DEFAULT 0,
			paired        INTEGER NOT NULL DEFAULT 0,
			allowed_tools TEXT NOT NULL DEFAULT '[]',
			created_at    TEXT NOT NULL DEFAULT (datetime('now')),
			last_active   TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE UNIQUE INDEX idx_sessions_channel_user ON sessions(channel, user_id);
	`)
	if err != nil {
		t.Fatalf("create sessions table: %v", err)
	}
	return db
}

func TestResolveNewSession(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store := NewSessionStore(db, logger)

	sess, err := store.Resolve(context.Background(), "telegram", "user123")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if sess.ID != "telegram:user123" {
		t.Errorf("session ID = %q, want %q", sess.ID, "telegram:user123")
	}
	if sess.Trust != TierUnknown {
		t.Errorf("trust = %v, want %v", sess.Trust, TierUnknown)
	}
	if sess.Paired {
		t.Error("new session should not be paired")
	}
}

func TestResolveExistingSession(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store := NewSessionStore(db, logger)
	ctx := context.Background()

	// Create session.
	sess1, _ := store.Resolve(ctx, "cli", "operator")

	// Resolve again — should return same session.
	sess2, err := store.Resolve(ctx, "cli", "operator")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sess2.ID != sess1.ID {
		t.Errorf("session IDs differ: %q vs %q", sess2.ID, sess1.ID)
	}
}

func TestSetTrust(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store := NewSessionStore(db, logger)
	ctx := context.Background()

	store.Resolve(ctx, "cli", "operator")
	err := store.SetTrust(ctx, "cli:operator", TierOperator)
	if err != nil {
		t.Fatalf("SetTrust: %v", err)
	}

	sess, _ := store.Resolve(ctx, "cli", "operator")
	if sess.Trust != TierOperator {
		t.Errorf("trust = %v, want %v", sess.Trust, TierOperator)
	}
	if !sess.Paired {
		t.Error("operator should be paired")
	}
}

func TestCanUseTool(t *testing.T) {
	tests := []struct {
		name     string
		tier     TrustTier
		minTier  TrustTier
		expected bool
	}{
		{"operator can use operator tool", TierOperator, TierOperator, true},
		{"operator can use trusted tool", TierOperator, TierTrusted, true},
		{"trusted can use trusted tool", TierTrusted, TierTrusted, true},
		{"trusted cannot use operator tool", TierTrusted, TierOperator, false},
		{"unknown cannot use trusted tool", TierUnknown, TierTrusted, false},
		{"group can use group tool", TierGroup, TierGroup, true},
		{"unknown cannot use group tool", TierUnknown, TierGroup, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := &Session{Trust: tt.tier}
			result := CanUseTool(sess, "test.tool", tt.minTier)
			if result != tt.expected {
				t.Errorf("CanUseTool(%v, %v) = %v, want %v", tt.tier, tt.minTier, result, tt.expected)
			}
		})
	}
}

func TestTrustTierString(t *testing.T) {
	tests := []struct {
		tier TrustTier
		want string
	}{
		{TierUnknown, "unknown"},
		{TierGroup, "group"},
		{TierTrusted, "trusted"},
		{TierOperator, "operator"},
		{TrustTier(99), "invalid"},
	}

	for _, tt := range tests {
		if got := tt.tier.String(); got != tt.want {
			t.Errorf("TrustTier(%d).String() = %q, want %q", tt.tier, got, tt.want)
		}
	}
}

func TestWrapUserMessage(t *testing.T) {
	input := "Hello, world!"
	wrapped := WrapUserMessage(input)
	expected := "<user_message>\nHello, world!\n</user_message>"
	if wrapped != expected {
		t.Errorf("WrapUserMessage = %q, want %q", wrapped, expected)
	}
}
