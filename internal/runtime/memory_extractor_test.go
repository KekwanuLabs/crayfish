package runtime

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMemoryExtractorIntegration is a basic integration test for the memory system.
func TestMemoryExtractorIntegration(t *testing.T) {
	// Create in-memory database
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create tables
	_, err = db.Exec(`
		CREATE VIRTUAL TABLE memory_fts USING fts5(
			key, content, session_id, created_at
		);
		CREATE TABLE memory_metadata (
			id INTEGER PRIMARY KEY,
			session_id TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'general',
			importance INTEGER NOT NULL DEFAULT 5,
			source_context TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			last_accessed TEXT,
			access_count INTEGER NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}

	// Test data insertion
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	// Insert a test memory
	result, err := tx.ExecContext(ctx,
		"INSERT INTO memory_fts (key, content, session_id, created_at) VALUES (?, ?, ?, datetime('now'))",
		"test_key", "test content", "session_123")
	if err != nil {
		t.Fatalf("failed to insert into memory_fts: %v", err)
	}

	rowID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("failed to get last insert id: %v", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO memory_metadata (id, session_id, category, importance, source_context, created_at, access_count)
		VALUES (?, ?, ?, ?, ?, datetime('now'), 0)`,
		rowID, "session_123", "preference", 8, "Test")
	if err != nil {
		t.Fatalf("failed to insert into memory_metadata: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Query back the data
	var key, content, category string
	var importance int
	err = db.QueryRowContext(ctx, `
		SELECT mf.key, mf.content, mm.category, mm.importance
		FROM memory_fts mf
		JOIN memory_metadata mm ON mf.rowid = mm.id
		WHERE mf.session_id = ?
	`, "session_123").Scan(&key, &content, &category, &importance)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}

	if key != "test_key" {
		t.Errorf("expected key 'test_key', got %q", key)
	}
	if content != "test content" {
		t.Errorf("expected content 'test content', got %q", content)
	}
	if category != "preference" {
		t.Errorf("expected category 'preference', got %q", category)
	}
	if importance != 8 {
		t.Errorf("expected importance 8, got %d", importance)
	}

	t.Log("Memory system integration test passed!")
}

// TestTrivialMessageDetection tests the isTrivialMessage function.
func TestTrivialMessageDetection(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"hi", true},
		{"hello", true},
		{"thanks", true},
		{"ok", true},
		{"yes", true},
		{"This is a real message", false},
		{"I prefer Python over JavaScript", false},
		{"", false},
	}

	for _, tt := range tests {
		result := isTrivialMessage(tt.input)
		if result != tt.expected {
			t.Errorf("isTrivialMessage(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

// TestCategoryValidation tests the isValidCategory function.
func TestCategoryValidation(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"preference", true},
		{"personal", true},
		{"decision", true},
		{"context", true},
		{"general", true},
		{"session_state", true},
		{"pending", true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		result := isValidCategory(tt.input)
		if result != tt.expected {
			t.Errorf("isValidCategory(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}
