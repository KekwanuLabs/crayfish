package bus

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary SQLite database with the events table.
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
		CREATE TABLE events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			type       TEXT    NOT NULL,
			channel    TEXT    NOT NULL DEFAULT '',
			session_id TEXT    NOT NULL DEFAULT '',
			payload    TEXT    NOT NULL DEFAULT '{}',
			created_at TEXT    NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		t.Fatalf("create events table: %v", err)
	}
	return db
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func TestPublishAndReplay(t *testing.T) {
	db := setupTestDB(t)
	b := NewSQLiteBus(db, testLogger())
	defer b.Close()

	ctx := context.Background()

	// Publish two events.
	id1, err := b.Publish(ctx, Event{
		Type:      TypeMessageInbound,
		Channel:   "cli",
		SessionID: "cli:operator",
		Payload:   MustJSON(InboundMessage{From: "operator", Text: "hello"}),
	})
	if err != nil {
		t.Fatalf("publish 1: %v", err)
	}

	id2, err := b.Publish(ctx, Event{
		Type:      TypeMessageOutbound,
		Channel:   "cli",
		SessionID: "cli:operator",
		Payload:   MustJSON(OutboundMessage{To: "operator", Text: "hi there"}),
	})
	if err != nil {
		t.Fatalf("publish 2: %v", err)
	}

	if id2 <= id1 {
		t.Errorf("expected id2 > id1, got %d <= %d", id2, id1)
	}

	// Replay from the beginning.
	events, err := b.Replay(ctx, 0)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != TypeMessageInbound {
		t.Errorf("event 0 type = %q, want %q", events[0].Type, TypeMessageInbound)
	}
	if events[1].Type != TypeMessageOutbound {
		t.Errorf("event 1 type = %q, want %q", events[1].Type, TypeMessageOutbound)
	}

	// Replay from id1 — should only get event 2.
	events, err = b.Replay(ctx, id1)
	if err != nil {
		t.Fatalf("replay from id1: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestSubscribe(t *testing.T) {
	db := setupTestDB(t)
	b := NewSQLiteBus(db, testLogger())
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Subscribe to inbound messages only.
	ch, err := b.Subscribe(ctx, []string{TypeMessageInbound})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Publish an outbound event (should NOT be received).
	b.Publish(ctx, Event{
		Type:    TypeMessageOutbound,
		Payload: MustJSON(OutboundMessage{To: "x", Text: "y"}),
	})

	// Publish an inbound event (should be received).
	b.Publish(ctx, Event{
		Type:    TypeMessageInbound,
		Payload: MustJSON(InboundMessage{From: "a", Text: "b"}),
	})

	select {
	case event := <-ch:
		if event.Type != TypeMessageInbound {
			t.Errorf("got type %q, want %q", event.Type, TypeMessageInbound)
		}
		var msg InboundMessage
		json.Unmarshal(event.Payload, &msg)
		if msg.Text != "b" {
			t.Errorf("got text %q, want %q", msg.Text, "b")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestLastID(t *testing.T) {
	db := setupTestDB(t)
	b := NewSQLiteBus(db, testLogger())
	defer b.Close()

	ctx := context.Background()

	// Empty bus.
	id, err := b.LastID(ctx)
	if err != nil {
		t.Fatalf("LastID: %v", err)
	}
	if id != 0 {
		t.Errorf("expected 0, got %d", id)
	}

	// After publishing.
	pubID, _ := b.Publish(ctx, Event{Type: TypeSystemStartup, Payload: []byte("{}")})
	lastID, _ := b.LastID(ctx)
	if lastID != pubID {
		t.Errorf("expected %d, got %d", pubID, lastID)
	}
}

func TestMustJSON(t *testing.T) {
	data := MustJSON(map[string]string{"key": "value"})
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["key"] != "value" {
		t.Errorf("expected value, got %q", m["key"])
	}
}
