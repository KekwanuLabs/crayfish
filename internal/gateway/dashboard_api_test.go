package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/storage"
)

// testDB creates a fresh storage.DB for testing with full schema.
func testDB(t *testing.T) *storage.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	db, err := storage.Open(context.Background(), dbPath, logger)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// mockBus implements bus.Bus for testing.
type mockBus struct {
	lastID int64
}

func (b *mockBus) Publish(_ context.Context, _ bus.Event) (int64, error) { return 0, nil }
func (b *mockBus) Subscribe(_ context.Context, _ []string) (<-chan bus.Event, error) {
	return make(chan bus.Event), nil
}
func (b *mockBus) Replay(_ context.Context, _ int64) ([]bus.Event, error) { return nil, nil }
func (b *mockBus) LastID(_ context.Context) (int64, error)                { return b.lastID, nil }
func (b *mockBus) Close() error                                           { return nil }

// mockAppAccessor implements AppAccessor for testing.
type mockAppAccessor struct {
	version   string
	uptime    time.Duration
	config    map[string]any
	updateErr error
}

func (m *mockAppAccessor) DashboardConfig() map[string]any { return m.config }
func (m *mockAppAccessor) UpdateConfig(updates map[string]any) (bool, error) {
	if m.updateErr != nil {
		return false, m.updateErr
	}
	// Simulate: non-hot fields trigger restart
	for k := range updates {
		if k == "api_key" || k == "provider" {
			return true, nil
		}
	}
	return false, nil
}
func (m *mockAppAccessor) Uptime() time.Duration                { return m.uptime }
func (m *mockAppAccessor) AppVersion() string                   { return m.version }
func (m *mockAppAccessor) VoiceInstallProgress() map[string]any { return nil }

func testAPI(t *testing.T) (*DashboardAPI, *storage.DB) {
	t.Helper()
	db := testDB(t)
	b := &mockBus{lastID: 42}
	app := &mockAppAccessor{
		version: "0.1.0-test",
		uptime:  5 * time.Minute,
		config:  map[string]any{"name": "TestCray", "api_key": "sk-t****"},
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	api := NewDashboardAPI(db, b, app, func() []string { return []string{"cli", "telegram"} }, logger)
	return api, db
}

func TestHandleOverview(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	// Seed data.
	inner.ExecContext(ctx, "INSERT INTO sessions (id, channel, user_id) VALUES ('s1', 'cli', 'u1')")
	inner.ExecContext(ctx, "INSERT INTO messages (session_id, role, content, created_at) VALUES ('s1', 'user', 'hello', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO memory_fts (key, content, session_id, created_at) VALUES ('k1', 'test', 's1', datetime('now'))")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	api.handleOverview(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["version"] != "0.1.0-test" {
		t.Errorf("version = %v, want 0.1.0-test", resp["version"])
	}
	if resp["messages"].(float64) != 1 {
		t.Errorf("messages = %v, want 1", resp["messages"])
	}
	if resp["sessions"].(float64) != 1 {
		t.Errorf("sessions = %v, want 1", resp["sessions"])
	}
	if resp["memories"].(float64) != 1 {
		t.Errorf("memories = %v, want 1", resp["memories"])
	}
	if resp["events"].(float64) != 42 {
		t.Errorf("events = %v, want 42", resp["events"])
	}
	adapters := resp["adapters"].([]any)
	if len(adapters) != 2 {
		t.Errorf("adapters count = %d, want 2", len(adapters))
	}
}

func TestHandleOverviewMethodNotAllowed(t *testing.T) {
	api, _ := testAPI(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/dashboard/overview", nil)
	api.handleOverview(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleConfigGet(t *testing.T) {
	api, _ := testAPI(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/config", nil)
	api.handleConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["name"] != "TestCray" {
		t.Errorf("name = %v, want TestCray", resp["name"])
	}
}

func TestHandleConfigPut(t *testing.T) {
	api, _ := testAPI(t)
	body := strings.NewReader(`{"name":"NewName"}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/dashboard/config", body)
	api.handleConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "saved" {
		t.Errorf("status = %v, want saved", resp["status"])
	}
}

func TestHandleConfigPutRestart(t *testing.T) {
	api, _ := testAPI(t)
	body := strings.NewReader(`{"api_key":"sk-new-key"}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/dashboard/config", body)
	api.handleConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["restart_needed"] != true {
		t.Errorf("restart_needed = %v, want true", resp["restart_needed"])
	}
}

func TestHandleConfigPutInvalidJSON(t *testing.T) {
	api, _ := testAPI(t)
	body := strings.NewReader(`{invalid`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/dashboard/config", body)
	api.handleConfig(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleSessions(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	inner.ExecContext(ctx, "INSERT INTO sessions (id, channel, user_id, trust_tier) VALUES ('s1', 'cli', 'u1', 3)")
	inner.ExecContext(ctx, "INSERT INTO sessions (id, channel, user_id, trust_tier) VALUES ('s2', 'telegram', 'u2', 1)")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/sessions", nil)
	api.handleSessions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	sessions := resp["sessions"].([]any)
	if len(sessions) != 2 {
		t.Errorf("sessions count = %d, want 2", len(sessions))
	}
}

func TestHandleSessionsEmpty(t *testing.T) {
	api, _ := testAPI(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/sessions", nil)
	api.handleSessions(w, r)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	sessions := resp["sessions"].([]any)
	if len(sessions) != 0 {
		t.Errorf("sessions count = %d, want 0", len(sessions))
	}
}

func TestHandleSessionMessages(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	inner.ExecContext(ctx, "INSERT INTO sessions (id, channel, user_id) VALUES ('sess-123', 'cli', 'u1')")
	inner.ExecContext(ctx, "INSERT INTO messages (session_id, role, content, created_at) VALUES ('sess-123', 'user', 'hello', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO messages (session_id, role, content, created_at) VALUES ('sess-123', 'assistant', 'hi there', datetime('now'))")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/sessions/sess-123/messages", nil)
	api.handleSessionMessages(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	msgs := resp["messages"].([]any)
	if len(msgs) != 2 {
		t.Errorf("messages count = %d, want 2", len(msgs))
	}
	// Should be in chronological order.
	first := msgs[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("first message role = %v, want user", first["role"])
	}
}

func TestHandleSessionMessagesInvalidPath(t *testing.T) {
	api, _ := testAPI(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/sessions/", nil)
	api.handleSessionMessages(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleMemory(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	inner.ExecContext(ctx, "INSERT INTO sessions (id, channel, user_id) VALUES ('s1', 'cli', 'u1')")
	inner.ExecContext(ctx, "INSERT INTO memory_fts (key, content, session_id, created_at) VALUES ('user_pref', 'likes dark mode', 's1', datetime('now'))")

	// Get the actual rowid from FTS5 to use for metadata join.
	var rowID int64
	err := inner.QueryRowContext(ctx, "SELECT rowid FROM memory_fts LIMIT 1").Scan(&rowID)
	if err != nil {
		t.Fatalf("get fts rowid: %v", err)
	}
	_, metaErr := inner.ExecContext(ctx, "INSERT INTO memory_metadata (id, session_id, category) VALUES (?, 's1', 'preference')", rowID)
	if metaErr != nil {
		t.Fatalf("insert memory_metadata: %v", metaErr)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/memory", nil)
	api.handleMemory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	memories := resp["memories"].([]any)
	if len(memories) != 1 {
		t.Errorf("memories count = %d, want 1", len(memories))
	}
	mem := memories[0].(map[string]any)
	if mem["category"] != "preference" {
		t.Errorf("category = %v, want preference", mem["category"])
	}
}

func TestHandleMemorySearch(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	inner.ExecContext(ctx, "INSERT INTO sessions (id, channel, user_id) VALUES ('s1', 'cli', 'u1')")
	inner.ExecContext(ctx, "INSERT INTO memory_fts (key, content, session_id, created_at) VALUES ('pref', 'likes dark mode', 's1', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO memory_fts (key, content, session_id, created_at) VALUES ('fact', 'lives in Lagos', 's1', datetime('now'))")

	// Search for "dark".
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/memory?q=dark", nil)
	api.handleMemory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	memories := resp["memories"].([]any)
	if len(memories) != 1 {
		t.Errorf("search results = %d, want 1", len(memories))
	}
}

func TestHandleMemoryDelete(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	inner.ExecContext(ctx, "INSERT INTO sessions (id, channel, user_id) VALUES ('s1', 'cli', 'u1')")
	inner.ExecContext(ctx, "INSERT INTO memory_fts (key, content, session_id, created_at) VALUES ('k1', 'test', 's1', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO memory_metadata (id, category) VALUES (1, 'general')")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/dashboard/memory/1", nil)
	api.handleMemoryDelete(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Verify deleted.
	var count int
	inner.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_fts").Scan(&count)
	if count != 0 {
		t.Errorf("memory_fts count = %d, want 0 after delete", count)
	}
	inner.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_metadata").Scan(&count)
	if count != 0 {
		t.Errorf("memory_metadata count = %d, want 0 after delete", count)
	}
}

func TestHandleMemoryDeleteInvalidID(t *testing.T) {
	api, _ := testAPI(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/dashboard/memory/abc", nil)
	api.handleMemoryDelete(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleMemoryDeleteMethodNotAllowed(t *testing.T) {
	api, _ := testAPI(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/memory/1", nil)
	api.handleMemoryDelete(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleEvents(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	// Seed events.
	inner.ExecContext(ctx, "INSERT INTO events (type, channel, session_id, payload, created_at) VALUES ('message.inbound', 'cli', 's1', '{}', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO events (type, channel, session_id, payload, created_at) VALUES ('message.outbound', 'cli', 's1', '{}', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO events (type, channel, session_id, payload, created_at) VALUES ('tool.request', '', 's1', '{}', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO events (type, channel, session_id, payload, created_at) VALUES ('system.startup', '', '', '{}', datetime('now'))")

	// All events.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/events", nil)
	api.handleEvents(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	events := resp["events"].([]any)
	if len(events) != 4 {
		t.Errorf("all events = %d, want 4", len(events))
	}
}

func TestHandleEventsTypeFilter(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	inner.ExecContext(ctx, "INSERT INTO events (type, channel, session_id, payload, created_at) VALUES ('message.inbound', 'cli', 's1', '{}', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO events (type, channel, session_id, payload, created_at) VALUES ('message.outbound', 'cli', 's1', '{}', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO events (type, channel, session_id, payload, created_at) VALUES ('tool.request', '', 's1', '{}', datetime('now'))")
	inner.ExecContext(ctx, "INSERT INTO events (type, channel, session_id, payload, created_at) VALUES ('system.startup', '', '', '{}', datetime('now'))")

	// Filter by message prefix — should match message.inbound and message.outbound.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/events?type=message", nil)
	api.handleEvents(w, r)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	events := resp["events"].([]any)
	if len(events) != 2 {
		t.Errorf("message events = %d, want 2", len(events))
	}
}

func TestHandleEventsLimitParam(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		inner.ExecContext(ctx, "INSERT INTO events (type, channel, session_id, payload, created_at) VALUES ('message.inbound', 'cli', 's1', '{}', datetime('now'))")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/events?limit=3", nil)
	api.handleEvents(w, r)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	events := resp["events"].([]any)
	if len(events) != 3 {
		t.Errorf("limited events = %d, want 3", len(events))
	}
}

func TestHandleSnapshots(t *testing.T) {
	api, db := testAPI(t)
	inner := db.Inner()
	ctx := context.Background()

	inner.ExecContext(ctx, "INSERT INTO sessions (id, channel, user_id) VALUES ('s1', 'cli', 'u1')")
	inner.ExecContext(ctx, "INSERT INTO session_snapshots (session_id, trigger, active_task, is_current) VALUES ('s1', 'auto', 'working on stuff', 1)")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/snapshots", nil)
	api.handleSnapshots(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	snaps := resp["snapshots"].([]any)
	if len(snaps) != 1 {
		t.Errorf("snapshots = %d, want 1", len(snaps))
	}
	snap := snaps[0].(map[string]any)
	if snap["active_task"] != "working on stuff" {
		t.Errorf("active_task = %v, want 'working on stuff'", snap["active_task"])
	}
}

func TestHandleSnapshotsEmpty(t *testing.T) {
	api, _ := testAPI(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dashboard/snapshots", nil)
	api.handleSnapshots(w, r)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	snaps := resp["snapshots"].([]any)
	if len(snaps) != 0 {
		t.Errorf("snapshots = %d, want 0", len(snaps))
	}
}

func TestDashboardAPIRoutes(t *testing.T) {
	api, _ := testAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	// Verify all routes are registered by making requests.
	routes := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/dashboard/overview", http.StatusOK},
		{http.MethodGet, "/api/dashboard/config", http.StatusOK},
		{http.MethodGet, "/api/dashboard/sessions", http.StatusOK},
		{http.MethodGet, "/api/dashboard/memory", http.StatusOK},
		{http.MethodGet, "/api/dashboard/events", http.StatusOK},
		{http.MethodGet, "/api/dashboard/snapshots", http.StatusOK},
	}

	for _, tc := range routes {
		t.Run(fmt.Sprintf("%s %s", tc.method, tc.path), func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(tc.method, tc.path, nil)
			mux.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}
