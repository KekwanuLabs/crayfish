package storage

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	db, err := Open(context.Background(), dbPath, logger)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify tables exist.
	tables := []string{
		"events", "sessions", "messages", "config", "message_cache", "_migrations",
		"pairing_otps", "pairing_attempts", "offline_queue", "conversation_summaries", "todos",
		"emails", "gmail_sync_state", "email_attachments",
		"memory_metadata", "identities",
		"session_snapshots",
	}
	for _, table := range tables {
		var name string
		err := db.Inner().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}

	// Verify FTS5 table.
	var ftsName string
	err = db.Inner().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='memory_fts'").Scan(&ftsName)
	if err != nil {
		t.Errorf("FTS5 table memory_fts not found: %v", err)
	}

	// Verify indexes for session_snapshots.
	indexes := []string{"idx_snapshots_session", "idx_snapshots_current"}
	for _, idx := range indexes {
		var name string
		err := db.Inner().QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx).Scan(&name)
		if err != nil {
			t.Errorf("index %q not found: %v", idx, err)
		}
	}
}

func TestSessionSnapshotsSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	db, err := Open(context.Background(), dbPath, logger)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	inner := db.Inner()

	// Insert a session for FK constraint.
	_, err = inner.ExecContext(ctx,
		"INSERT INTO sessions (id, channel, user_id) VALUES ('test_sess', 'cli', 'user1')")
	if err != nil {
		t.Fatalf("insert session failed: %v", err)
	}

	// Insert a snapshot with all fields.
	_, err = inner.ExecContext(ctx, `
		INSERT INTO session_snapshots
			(session_id, trigger, active_task, active_task_context, last_exchanges,
			 pending_proposals, decisions_in_flight, conversational_tone, key_resources,
			 message_count, is_current)
		VALUES ('test_sess', 'auto', 'Test task', 'Test context', '[]', '[]', '[]', 'neutral', '[]', 10, 1)`)
	if err != nil {
		t.Fatalf("insert snapshot failed: %v", err)
	}

	// Read it back.
	var sessionID, trigger, activeTask, tone string
	var messageCount, isCurrent int
	err = inner.QueryRowContext(ctx,
		"SELECT session_id, trigger, active_task, conversational_tone, message_count, is_current FROM session_snapshots WHERE session_id = 'test_sess'",
	).Scan(&sessionID, &trigger, &activeTask, &tone, &messageCount, &isCurrent)
	if err != nil {
		t.Fatalf("query snapshot failed: %v", err)
	}

	if sessionID != "test_sess" || trigger != "auto" || activeTask != "Test task" || tone != "neutral" || messageCount != 10 || isCurrent != 1 {
		t.Errorf("snapshot values mismatch: got session_id=%q trigger=%q active_task=%q tone=%q count=%d current=%d",
			sessionID, trigger, activeTask, tone, messageCount, isCurrent)
	}

	// Verify defaults work (insert with minimal fields).
	_, err = inner.ExecContext(ctx,
		"INSERT INTO session_snapshots (session_id) VALUES ('test_sess')")
	if err != nil {
		t.Fatalf("insert minimal snapshot failed: %v", err)
	}

	var defaultTrigger, defaultTask, defaultExchanges string
	err = inner.QueryRowContext(ctx,
		"SELECT trigger, active_task, last_exchanges FROM session_snapshots ORDER BY id DESC LIMIT 1",
	).Scan(&defaultTrigger, &defaultTask, &defaultExchanges)
	if err != nil {
		t.Fatalf("query default snapshot failed: %v", err)
	}
	if defaultTrigger != "auto" || defaultTask != "" || defaultExchanges != "[]" {
		t.Errorf("defaults mismatch: trigger=%q task=%q exchanges=%q", defaultTrigger, defaultTask, defaultExchanges)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Open twice — migrations should be idempotent.
	db1, err := Open(context.Background(), dbPath, logger)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	db1.Close()

	db2, err := Open(context.Background(), dbPath, logger)
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}
	defer db2.Close()

	// Check migration count — should match len(migrations) and not increase on re-open.
	var count int
	db2.Inner().QueryRow("SELECT COUNT(*) FROM _migrations").Scan(&count)
	if count != len(migrations) {
		t.Errorf("expected %d migrations, got %d", len(migrations), count)
	}
}

func TestCompact(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	db, err := Open(context.Background(), dbPath, logger)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Compact should not fail even on a small DB.
	if err := db.Compact(context.Background(), 500); err != nil {
		t.Errorf("Compact failed: %v", err)
	}
}
