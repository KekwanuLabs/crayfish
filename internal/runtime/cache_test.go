package runtime

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// setupCacheDB creates an in-memory SQLite database with the message_cache table.
func setupCacheDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE message_cache (
			hash       TEXT PRIMARY KEY,
			response   TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			expires_at TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestHashPromptIncludesSessionID(t *testing.T) {
	// Same prompt, different sessions should produce different hashes.
	h1 := hashPrompt("session_a", "hello world")
	h2 := hashPrompt("session_b", "hello world")
	if h1 == h2 {
		t.Error("hash should differ for different sessions with same prompt")
	}

	// Same session and prompt should produce the same hash.
	h3 := hashPrompt("session_a", "hello world")
	if h1 != h3 {
		t.Error("hash should be stable for same session and prompt")
	}
}

func TestCacheSessionIsolation(t *testing.T) {
	db := setupCacheDB(t)
	r := &Runtime{db: db}
	ctx := context.Background()

	// Cache a response for session A.
	r.cacheResponse(ctx, "sess_a", "what time is it?", "It's 3pm for you")

	// Session A should get a cache hit.
	if got := r.checkCache(ctx, "sess_a", "what time is it?"); got != "It's 3pm for you" {
		t.Errorf("session A cache hit = %q, want %q", got, "It's 3pm for you")
	}

	// Session B should NOT get session A's cached response.
	if got := r.checkCache(ctx, "sess_b", "what time is it?"); got != "" {
		t.Errorf("session B should get no cache hit, got %q", got)
	}
}
