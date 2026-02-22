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
	tables := []string{"events", "sessions", "messages", "config", "message_cache", "_migrations"}
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
