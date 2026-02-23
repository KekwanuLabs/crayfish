package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/provider"
	"github.com/KekwanuLabs/crayfish/internal/security"
	_ "modernc.org/sqlite"
)

// mockProvider is a test double that returns a configurable response.
type mockProvider struct {
	response *provider.CompletionResponse
	err      error
	calls    int
}

func (m *mockProvider) Complete(_ context.Context, _ provider.CompletionRequest) (*provider.CompletionResponse, error) {
	m.calls++
	return m.response, m.err
}

func (m *mockProvider) Name() string { return "mock" }

// setupSnapshotDB creates an in-memory SQLite database with the tables needed for snapshot tests.
func setupSnapshotDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	// SQLite in-memory databases are per-connection. Limit to 1 connection
	// so background goroutines share the same database (matches production behavior).
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	// Create the tables that snapshot operations depend on.
	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			channel TEXT NOT NULL,
			user_id TEXT NOT NULL,
			trust_tier INTEGER NOT NULL DEFAULT 0,
			paired INTEGER NOT NULL DEFAULT 0,
			allowed_tools TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			last_active TEXT NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			tokens INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);

		CREATE TABLE session_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			trigger TEXT NOT NULL DEFAULT 'auto',
			active_task TEXT NOT NULL DEFAULT '',
			active_task_context TEXT NOT NULL DEFAULT '',
			last_exchanges TEXT NOT NULL DEFAULT '[]',
			pending_proposals TEXT NOT NULL DEFAULT '[]',
			decisions_in_flight TEXT NOT NULL DEFAULT '[]',
			conversational_tone TEXT NOT NULL DEFAULT '',
			key_resources TEXT NOT NULL DEFAULT '[]',
			message_count INTEGER NOT NULL DEFAULT 0,
			is_current INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE INDEX idx_snapshots_session ON session_snapshots(session_id);
		CREATE INDEX idx_snapshots_current ON session_snapshots(session_id, is_current);
	`)
	if err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}

	// Insert a test session so foreign key constraints pass.
	_, err = db.Exec(`INSERT INTO sessions (id, channel, user_id) VALUES ('sess_1', 'cli', 'user1')`)
	if err != nil {
		t.Fatalf("failed to insert test session: %v", err)
	}

	return db
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// --- SnapshotManager: save and load ---

func TestSnapshotSaveAndLoad(t *testing.T) {
	db := setupSnapshotDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	ctx := context.Background()

	snap := &snapshotResponse{
		ActiveTask:         "Building the login page",
		ActiveTaskContext:   "User asked for OAuth integration with Google",
		LastExchanges:      []map[string]string{{"role": "user", "summary": "Asked about OAuth"}},
		PendingProposals:   []string{"Should we also add GitHub OAuth?"},
		DecisionsInFlight:  []string{"Which OAuth library to use"},
		ConversationalTone: "focused",
		KeyResources:       []string{"auth.go", "https://oauth.example.com"},
	}

	err := mgr.saveSnapshot(ctx, "sess_1", "auto", snap, 12)
	if err != nil {
		t.Fatalf("saveSnapshot failed: %v", err)
	}

	loaded, err := mgr.LoadLatest(ctx, "sess_1")
	if err != nil {
		t.Fatalf("LoadLatest failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadLatest returned nil, expected a snapshot")
	}

	// Verify fields
	if loaded.SessionID != "sess_1" {
		t.Errorf("SessionID = %q, want %q", loaded.SessionID, "sess_1")
	}
	if loaded.Trigger != "auto" {
		t.Errorf("Trigger = %q, want %q", loaded.Trigger, "auto")
	}
	if loaded.ActiveTask != "Building the login page" {
		t.Errorf("ActiveTask = %q, want %q", loaded.ActiveTask, "Building the login page")
	}
	if loaded.ActiveTaskContext != "User asked for OAuth integration with Google" {
		t.Errorf("ActiveTaskContext = %q, want %q", loaded.ActiveTaskContext, "User asked for OAuth integration with Google")
	}
	if loaded.ConversationalTone != "focused" {
		t.Errorf("ConversationalTone = %q, want %q", loaded.ConversationalTone, "focused")
	}
	if loaded.MessageCount != 12 {
		t.Errorf("MessageCount = %d, want %d", loaded.MessageCount, 12)
	}
	if !loaded.IsCurrent {
		t.Error("IsCurrent = false, want true")
	}

	// Verify JSON array fields deserialize correctly
	var pending []string
	if err := json.Unmarshal([]byte(loaded.PendingProposals), &pending); err != nil {
		t.Fatalf("failed to parse PendingProposals JSON: %v", err)
	}
	if len(pending) != 1 || pending[0] != "Should we also add GitHub OAuth?" {
		t.Errorf("PendingProposals = %v, want [Should we also add GitHub OAuth?]", pending)
	}

	var resources []string
	if err := json.Unmarshal([]byte(loaded.KeyResources), &resources); err != nil {
		t.Fatalf("failed to parse KeyResources JSON: %v", err)
	}
	if len(resources) != 2 {
		t.Errorf("KeyResources length = %d, want 2", len(resources))
	}
}

// --- Only one snapshot is current at a time ---

func TestSnapshotOnlyOneCurrentPerSession(t *testing.T) {
	db := setupSnapshotDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	ctx := context.Background()

	// Save three snapshots in sequence.
	for i := 1; i <= 3; i++ {
		snap := &snapshotResponse{
			ActiveTask: fmt.Sprintf("Task %d", i),
		}
		if err := mgr.saveSnapshot(ctx, "sess_1", "auto", snap, i*5); err != nil {
			t.Fatalf("saveSnapshot %d failed: %v", i, err)
		}
	}

	// Only one should be current.
	var currentCount int
	err := db.QueryRow("SELECT COUNT(*) FROM session_snapshots WHERE session_id = 'sess_1' AND is_current = 1").Scan(&currentCount)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if currentCount != 1 {
		t.Errorf("current snapshot count = %d, want 1", currentCount)
	}

	// Total should be 3.
	var totalCount int
	err = db.QueryRow("SELECT COUNT(*) FROM session_snapshots WHERE session_id = 'sess_1'").Scan(&totalCount)
	if err != nil {
		t.Fatalf("total count query failed: %v", err)
	}
	if totalCount != 3 {
		t.Errorf("total snapshot count = %d, want 3", totalCount)
	}

	// The current one should be the latest.
	loaded, err := mgr.LoadLatest(ctx, "sess_1")
	if err != nil {
		t.Fatalf("LoadLatest failed: %v", err)
	}
	if loaded.ActiveTask != "Task 3" {
		t.Errorf("ActiveTask = %q, want %q", loaded.ActiveTask, "Task 3")
	}
}

// --- LoadLatest returns nil for unknown session ---

func TestSnapshotLoadLatestNoSession(t *testing.T) {
	db := setupSnapshotDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	ctx := context.Background()

	loaded, err := mgr.LoadLatest(ctx, "nonexistent_session")
	if err != nil {
		t.Fatalf("LoadLatest failed: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil snapshot for nonexistent session, got %+v", loaded)
	}
}

// --- FormatForContext ---

func TestSnapshotFormatForContext(t *testing.T) {
	mgr := NewSnapshotManager(nil, nil, testLogger())

	// Nil snapshot returns empty string.
	if result := mgr.FormatForContext(nil); result != "" {
		t.Errorf("FormatForContext(nil) = %q, want empty", result)
	}

	snap := &Snapshot{
		ActiveTask:         "Fixing the login bug",
		ActiveTaskContext:   "User reported 500 errors on POST /login",
		LastExchanges:      `[{"role":"user","summary":"Reported login failure"},{"role":"assistant","summary":"Found null pointer in auth handler"}]`,
		PendingProposals:   `["Add rate limiting to login endpoint"]`,
		DecisionsInFlight:  `["Whether to switch to JWT from sessions"]`,
		ConversationalTone: "frustrated",
		KeyResources:       `["auth/handler.go","POST /login"]`,
	}

	formatted := mgr.FormatForContext(snap)

	expectations := []string{
		"[Session State — auto-recovered]",
		"Active task: Fixing the login bug",
		"Context: User reported 500 errors on POST /login",
		"Last exchanges:",
		"user: Reported login failure",
		"assistant: Found null pointer in auth handler",
		"Pending proposals:",
		"Add rate limiting to login endpoint",
		"Decisions in flight:",
		"Whether to switch to JWT from sessions",
		"Tone: frustrated",
		"Key resources: auth/handler.go, POST /login",
		"Continue naturally from this state.",
	}

	for _, expected := range expectations {
		if !strings.Contains(formatted, expected) {
			t.Errorf("FormatForContext missing %q\n\nGot:\n%s", expected, formatted)
		}
	}
}

func TestSnapshotFormatForContextEmptyFields(t *testing.T) {
	mgr := NewSnapshotManager(nil, nil, testLogger())

	// Snapshot with only ActiveTask populated, arrays empty.
	snap := &Snapshot{
		ActiveTask:        "Idle",
		LastExchanges:     "[]",
		PendingProposals:  "[]",
		DecisionsInFlight: "[]",
		KeyResources:      "[]",
	}

	formatted := mgr.FormatForContext(snap)

	if !strings.Contains(formatted, "Active task: Idle") {
		t.Errorf("missing active task in output:\n%s", formatted)
	}
	// Empty arrays should NOT produce section headers.
	if strings.Contains(formatted, "Pending proposals:") {
		t.Errorf("should not contain 'Pending proposals:' for empty array:\n%s", formatted)
	}
	if strings.Contains(formatted, "Decisions in flight:") {
		t.Errorf("should not contain 'Decisions in flight:' for empty array:\n%s", formatted)
	}
	if strings.Contains(formatted, "Last exchanges:") {
		t.Errorf("should not contain 'Last exchanges:' for empty array:\n%s", formatted)
	}
}

// --- SaveCheckpoint (tools.CheckpointSaver interface) ---

func TestSaveCheckpoint(t *testing.T) {
	db := setupSnapshotDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	ctx := context.Background()

	input := json.RawMessage(`{
		"active_task": "Writing tests",
		"active_task_context": "User asked for comprehensive test coverage",
		"pending_proposals": ["Add benchmarks"],
		"decisions_in_flight": [],
		"conversational_tone": "diligent",
		"key_resources": ["snapshot_test.go"]
	}`)

	err := mgr.SaveCheckpoint(ctx, "sess_1", input)
	if err != nil {
		t.Fatalf("SaveCheckpoint failed: %v", err)
	}

	loaded, err := mgr.LoadLatest(ctx, "sess_1")
	if err != nil {
		t.Fatalf("LoadLatest failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadLatest returned nil after SaveCheckpoint")
	}
	if loaded.Trigger != "manual" {
		t.Errorf("Trigger = %q, want %q", loaded.Trigger, "manual")
	}
	if loaded.ActiveTask != "Writing tests" {
		t.Errorf("ActiveTask = %q, want %q", loaded.ActiveTask, "Writing tests")
	}
	if loaded.ConversationalTone != "diligent" {
		t.Errorf("ConversationalTone = %q, want %q", loaded.ConversationalTone, "diligent")
	}
}

// --- GenerateAndSave with mock provider ---

func TestGenerateAndSave(t *testing.T) {
	db := setupSnapshotDB(t)

	mockResp := &provider.CompletionResponse{
		Content: `{
			"active_task": "Discussing project architecture",
			"active_task_context": "Deciding between microservices and monolith",
			"last_exchanges": [{"role": "user", "summary": "Asked about scaling"}],
			"pending_proposals": ["Consider event-driven architecture"],
			"decisions_in_flight": ["Monolith vs microservices"],
			"conversational_tone": "curious",
			"key_resources": ["architecture.md"]
		}`,
	}
	mock := &mockProvider{response: mockResp}

	mgr := NewSnapshotManager(db, mock, testLogger())
	ctx := context.Background()

	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are helpful."},
		{Role: provider.RoleUser, Content: "How should we architect the backend?"},
		{Role: provider.RoleAssistant, Content: "There are a few approaches..."},
		{Role: provider.RoleUser, Content: "What about scaling?"},
	}

	err := mgr.GenerateAndSave(ctx, "sess_1", "auto", messages)
	if err != nil {
		t.Fatalf("GenerateAndSave failed: %v", err)
	}

	if mock.calls != 1 {
		t.Errorf("provider called %d times, want 1", mock.calls)
	}

	loaded, err := mgr.LoadLatest(ctx, "sess_1")
	if err != nil {
		t.Fatalf("LoadLatest failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadLatest returned nil after GenerateAndSave")
	}
	if loaded.ActiveTask != "Discussing project architecture" {
		t.Errorf("ActiveTask = %q, want %q", loaded.ActiveTask, "Discussing project architecture")
	}
	if loaded.MessageCount != 4 {
		t.Errorf("MessageCount = %d, want 4", loaded.MessageCount)
	}
}

func TestGenerateAndSaveHandlesMarkdownFences(t *testing.T) {
	db := setupSnapshotDB(t)

	// LLMs sometimes wrap JSON in markdown fences despite instructions.
	mockResp := &provider.CompletionResponse{
		Content: "```json\n" + `{
			"active_task": "Wrapped in fences",
			"active_task_context": "",
			"last_exchanges": [],
			"pending_proposals": [],
			"decisions_in_flight": [],
			"conversational_tone": "neutral",
			"key_resources": []
		}` + "\n```",
	}
	mock := &mockProvider{response: mockResp}
	mgr := NewSnapshotManager(db, mock, testLogger())

	err := mgr.GenerateAndSave(context.Background(), "sess_1", "auto", []provider.Message{
		{Role: provider.RoleUser, Content: "test"},
	})
	if err != nil {
		t.Fatalf("GenerateAndSave with markdown fences failed: %v", err)
	}

	loaded, _ := mgr.LoadLatest(context.Background(), "sess_1")
	if loaded == nil || loaded.ActiveTask != "Wrapped in fences" {
		t.Errorf("failed to parse fenced JSON, got ActiveTask = %q", loaded.ActiveTask)
	}
}

func TestGenerateAndSaveEmptyMessages(t *testing.T) {
	mgr := NewSnapshotManager(nil, nil, testLogger())

	// Should return nil (no-op) for empty messages.
	err := mgr.GenerateAndSave(context.Background(), "sess_1", "auto", nil)
	if err != nil {
		t.Errorf("GenerateAndSave with nil messages should return nil, got: %v", err)
	}

	err = mgr.GenerateAndSave(context.Background(), "sess_1", "auto", []provider.Message{})
	if err != nil {
		t.Errorf("GenerateAndSave with empty messages should return nil, got: %v", err)
	}
}

func TestGenerateAndSaveProviderError(t *testing.T) {
	db := setupSnapshotDB(t)
	mock := &mockProvider{err: fmt.Errorf("API rate limited")}
	mgr := NewSnapshotManager(db, mock, testLogger())

	err := mgr.GenerateAndSave(context.Background(), "sess_1", "auto", []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
	})
	if err == nil {
		t.Fatal("expected error from provider failure, got nil")
	}
	if !strings.Contains(err.Error(), "API rate limited") {
		t.Errorf("error should contain provider message, got: %v", err)
	}
}

// --- Cleanup ---

func TestSnapshotCleanupKeepsN(t *testing.T) {
	db := setupSnapshotDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	ctx := context.Background()

	// Insert 5 snapshots.
	for i := 1; i <= 5; i++ {
		snap := &snapshotResponse{ActiveTask: fmt.Sprintf("Task %d", i)}
		if err := mgr.saveSnapshot(ctx, "sess_1", "auto", snap, i); err != nil {
			t.Fatalf("saveSnapshot %d failed: %v", i, err)
		}
	}

	// Cleanup: keep 2.
	if err := mgr.Cleanup(ctx, 2, 24*time.Hour); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM session_snapshots WHERE session_id = 'sess_1'").Scan(&count)
	if count != 2 {
		t.Errorf("after Cleanup(keep=2), count = %d, want 2", count)
	}

	// The remaining two should be the most recent (Task 4 and Task 5).
	rows, err := db.Query("SELECT active_task FROM session_snapshots WHERE session_id = 'sess_1' ORDER BY id DESC")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	var tasks []string
	for rows.Next() {
		var task string
		rows.Scan(&task)
		tasks = append(tasks, task)
	}
	if len(tasks) != 2 || tasks[0] != "Task 5" || tasks[1] != "Task 4" {
		t.Errorf("remaining tasks = %v, want [Task 5, Task 4]", tasks)
	}
}

// --- IsSessionResume ---

func TestIsSessionResume(t *testing.T) {
	db := setupSnapshotDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	ctx := context.Background()

	// No messages yet: should not be a resume.
	if mgr.IsSessionResume(ctx, "sess_1", 30*time.Minute) {
		t.Error("IsSessionResume should be false when no messages exist")
	}

	// Insert a recent message.
	_, err := db.Exec(
		"INSERT INTO messages (session_id, role, content, created_at) VALUES (?, ?, ?, datetime('now'))",
		"sess_1", "user", "hello")
	if err != nil {
		t.Fatalf("insert message failed: %v", err)
	}

	// With a 30-minute threshold, a message just inserted should not trigger resume.
	if mgr.IsSessionResume(ctx, "sess_1", 30*time.Minute) {
		t.Error("IsSessionResume should be false for a message just inserted")
	}

	// With a 0-second threshold, even a recent message counts as a resume.
	// (anything > 0 seconds old triggers it)
	// We use 0 to test the boundary — a just-inserted message is at least ~0ms old.
	// Use -1 second to definitively trigger.
	if !mgr.IsSessionResume(ctx, "sess_1", -1*time.Second) {
		t.Error("IsSessionResume should be true when threshold is negative (always triggers)")
	}

	// Insert an old message (2 hours ago).
	_, err = db.Exec(
		"INSERT INTO messages (session_id, role, content, created_at) VALUES (?, ?, ?, datetime('now', '-2 hours'))",
		"sess_1", "assistant", "old response")
	if err != nil {
		t.Fatalf("insert old message failed: %v", err)
	}

	// The MAX(created_at) is still the recent message, so 30-min threshold should still be false.
	if mgr.IsSessionResume(ctx, "sess_1", 30*time.Minute) {
		t.Error("IsSessionResume should be false since most recent message is still new")
	}
}

func TestIsSessionResumeWithOldMessages(t *testing.T) {
	db := setupSnapshotDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	ctx := context.Background()

	// Insert only an old message (1 hour ago).
	_, err := db.Exec(
		"INSERT INTO messages (session_id, role, content, created_at) VALUES (?, ?, ?, datetime('now', '-1 hour'))",
		"sess_1", "user", "old message")
	if err != nil {
		t.Fatalf("insert message failed: %v", err)
	}

	// With 30-minute threshold, a 1-hour-old message should trigger resume.
	if !mgr.IsSessionResume(ctx, "sess_1", 30*time.Minute) {
		t.Error("IsSessionResume should be true when last message is 1 hour old and threshold is 30 min")
	}
}

// --- Summarizer integration: SetSnapshotManager ---

func TestSummarizerSnapshotIntegration(t *testing.T) {
	db := setupSnapshotDB(t)

	// Also create conversation_summaries for the summarizer to store summaries.
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS conversation_summaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			summary TEXT NOT NULL,
			message_count INTEGER NOT NULL,
			last_message_id INTEGER NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		t.Fatalf("create conversation_summaries failed: %v", err)
	}

	// Use atomic counter since the provider is called from both the main
	// goroutine (summary) and a background goroutine (snapshot).
	var callCount atomic.Int32

	summarizerProvider := &switchingProvider{
		callFunc: func(ctx context.Context, req provider.CompletionRequest) (*provider.CompletionResponse, error) {
			callCount.Add(1)
			if strings.Contains(req.Messages[1].Content, "Summarize this conversation") {
				return &provider.CompletionResponse{Content: "Summary of the conversation."}, nil
			}
			// Snapshot request
			return &provider.CompletionResponse{Content: `{
				"active_task": "Testing summarizer",
				"active_task_context": "Running integration test",
				"last_exchanges": [],
				"pending_proposals": [],
				"decisions_in_flight": [],
				"conversational_tone": "focused",
				"key_resources": []
			}`}, nil
		},
	}

	snapshotMgr := NewSnapshotManager(db, summarizerProvider, testLogger())
	summarizer := NewSummarizer(db, summarizerProvider, testLogger())
	summarizer.SetSnapshotManager(snapshotMgr)

	// Build enough messages to trigger summarization (> 15).
	var messages []provider.Message
	for i := 0; i < 18; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		messages = append(messages, provider.Message{
			Role:    role,
			Content: fmt.Sprintf("Message %d", i),
		})
	}

	result, err := summarizer.SummarizeIfNeeded(context.Background(), "sess_1", messages, 5)
	if err != nil {
		t.Fatalf("SummarizeIfNeeded failed: %v", err)
	}

	// Should return summary + 5 recent messages = 6 total.
	if len(result) != 6 {
		t.Errorf("result length = %d, want 6 (1 summary + 5 recent)", len(result))
	}

	// The first message should be the summary.
	if !strings.Contains(result[0].Content, "Previous conversation summary") {
		t.Errorf("first message should be summary, got: %q", result[0].Content)
	}

	// Give the background goroutine time to complete the snapshot save.
	time.Sleep(500 * time.Millisecond)

	// Verify that a snapshot was saved.
	var snapCount int
	db.QueryRow("SELECT COUNT(*) FROM session_snapshots WHERE session_id = 'sess_1'").Scan(&snapCount)
	if snapCount == 0 {
		t.Error("expected at least one snapshot to be saved during summarization")
	}

	// Both summary and snapshot calls should have been made.
	if c := callCount.Load(); c < 2 {
		t.Errorf("expected at least 2 provider calls (summary + snapshot), got %d", c)
	}
}

// --- New memory categories ---

func TestNewMemoryCategories(t *testing.T) {
	newCategories := []struct {
		category string
		valid    bool
	}{
		{"session_state", true},
		{"pending", true},
		// Verify existing ones still work.
		{"preference", true},
		{"personal", true},
		{"decision", true},
		{"context", true},
		{"general", true},
		{"invalid_category", false},
		{"", false},
	}

	for _, tt := range newCategories {
		result := isValidCategory(tt.category)
		if result != tt.valid {
			t.Errorf("isValidCategory(%q) = %v, want %v", tt.category, result, tt.valid)
		}
	}
}

// --- assembleContext integration tests ---

// setupRuntimeDB creates the full schema needed for assembleContext tests.
func setupRuntimeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			channel TEXT NOT NULL,
			user_id TEXT NOT NULL,
			trust_tier INTEGER NOT NULL DEFAULT 0,
			paired INTEGER NOT NULL DEFAULT 0,
			allowed_tools TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			last_active TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			tokens INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE session_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			trigger TEXT NOT NULL DEFAULT 'auto',
			active_task TEXT NOT NULL DEFAULT '',
			active_task_context TEXT NOT NULL DEFAULT '',
			last_exchanges TEXT NOT NULL DEFAULT '[]',
			pending_proposals TEXT NOT NULL DEFAULT '[]',
			decisions_in_flight TEXT NOT NULL DEFAULT '[]',
			conversational_tone TEXT NOT NULL DEFAULT '',
			key_resources TEXT NOT NULL DEFAULT '[]',
			message_count INTEGER NOT NULL DEFAULT 0,
			is_current INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE VIRTUAL TABLE memory_fts USING fts5(key, content, session_id, created_at);
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
		CREATE TABLE conversation_summaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			summary TEXT NOT NULL,
			message_count INTEGER NOT NULL,
			last_message_id INTEGER NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO sessions (id, channel, user_id) VALUES ('sess_rt', 'cli', 'user1');
	`)
	if err != nil {
		t.Fatalf("schema setup failed: %v", err)
	}
	return db
}

// buildMinimalRuntime creates a Runtime with just enough wiring for assembleContext.
func buildMinimalRuntime(t *testing.T, db *sql.DB, snapshotMgr *SnapshotManager, resumeThreshold time.Duration) *Runtime {
	t.Helper()
	logger := testLogger()
	mock := &mockProvider{response: &provider.CompletionResponse{Content: "summary"}}
	summarizer := NewSummarizer(db, mock, logger)
	if snapshotMgr != nil {
		summarizer.SetSnapshotManager(snapshotMgr)
	}

	return &Runtime{
		config:                 DefaultConfig(),
		db:                     db,
		summarizer:             summarizer,
		snapshotMgr:            snapshotMgr,
		memoryRetriever:        NewMemoryRetriever(db, logger),
		logger:                 logger,
		sessionResumeThreshold: resumeThreshold,
	}
}

func TestAssembleContextNoSnapshotNormally(t *testing.T) {
	db := setupRuntimeDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	rt := buildMinimalRuntime(t, db, mgr, 30*time.Minute)

	// Insert a recent message so this is NOT a resume.
	db.Exec("INSERT INTO messages (session_id, role, content, created_at) VALUES ('sess_rt', 'user', 'hello', datetime('now'))")

	// Save a snapshot for this session.
	snap := &snapshotResponse{ActiveTask: "Test task"}
	mgr.saveSnapshot(context.Background(), "sess_rt", "auto", snap, 5)

	sess := &security.Session{ID: "sess_rt", Trust: security.TierOperator}
	messages, err := rt.assembleContext(context.Background(), sess, "new message")
	if err != nil {
		t.Fatalf("assembleContext failed: %v", err)
	}

	// There's a snapshot in the DB, but since this is NOT a resume and NOT post-summarization,
	// no snapshot should be injected.
	for _, msg := range messages {
		if strings.Contains(msg.Content, "[Session State — auto-recovered]") {
			t.Error("snapshot should NOT be injected during normal conversation")
		}
	}
}

func TestAssembleContextInjectsSnapshotOnResume(t *testing.T) {
	db := setupRuntimeDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	rt := buildMinimalRuntime(t, db, mgr, 30*time.Minute)

	// Insert an OLD message to simulate resume (1 hour ago).
	db.Exec("INSERT INTO messages (session_id, role, content, created_at) VALUES ('sess_rt', 'user', 'old message', datetime('now', '-1 hour'))")

	// Save a snapshot.
	snap := &snapshotResponse{
		ActiveTask:         "Debugging auth",
		ConversationalTone: "frustrated",
	}
	mgr.saveSnapshot(context.Background(), "sess_rt", "auto", snap, 10)

	sess := &security.Session{ID: "sess_rt", Trust: security.TierOperator}
	messages, err := rt.assembleContext(context.Background(), sess, "I'm back")
	if err != nil {
		t.Fatalf("assembleContext failed: %v", err)
	}

	// Snapshot should be injected since last message is > 30 min old.
	found := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, "[Session State — auto-recovered]") {
			found = true
			if !strings.Contains(msg.Content, "Debugging auth") {
				t.Errorf("snapshot should contain active task, got: %s", msg.Content)
			}
			if !strings.Contains(msg.Content, "frustrated") {
				t.Errorf("snapshot should contain tone, got: %s", msg.Content)
			}
		}
	}
	if !found {
		t.Error("snapshot should be injected when resuming after idle gap")
	}
}

func TestAssembleContextNoSnapshotMgrIsNoop(t *testing.T) {
	db := setupRuntimeDB(t)
	// No snapshot manager — should work without panics.
	rt := buildMinimalRuntime(t, db, nil, 30*time.Minute)

	db.Exec("INSERT INTO messages (session_id, role, content, created_at) VALUES ('sess_rt', 'user', 'old message', datetime('now', '-1 hour'))")

	sess := &security.Session{ID: "sess_rt", Trust: security.TierOperator}
	messages, err := rt.assembleContext(context.Background(), sess, "hello")
	if err != nil {
		t.Fatalf("assembleContext without snapshotMgr failed: %v", err)
	}

	// Should still produce a valid context (system prompt + history + user msg).
	if len(messages) < 2 {
		t.Errorf("expected at least 2 messages (system + user), got %d", len(messages))
	}
}

func TestAssembleContextMessageOrder(t *testing.T) {
	db := setupRuntimeDB(t)
	mgr := NewSnapshotManager(db, nil, testLogger())
	rt := buildMinimalRuntime(t, db, mgr, 30*time.Minute)

	// Insert old message to trigger resume.
	db.Exec("INSERT INTO messages (session_id, role, content, created_at) VALUES ('sess_rt', 'user', 'old msg', datetime('now', '-2 hours'))")
	db.Exec("INSERT INTO messages (session_id, role, content, created_at) VALUES ('sess_rt', 'assistant', 'old reply', datetime('now', '-2 hours'))")

	snap := &snapshotResponse{ActiveTask: "Order test"}
	mgr.saveSnapshot(context.Background(), "sess_rt", "auto", snap, 2)

	sess := &security.Session{ID: "sess_rt", Trust: security.TierOperator}
	messages, err := rt.assembleContext(context.Background(), sess, "test order")
	if err != nil {
		t.Fatalf("assembleContext failed: %v", err)
	}

	// Expected order: system prompt, snapshot, history..., user message
	if len(messages) < 4 {
		t.Fatalf("expected at least 4 messages, got %d", len(messages))
	}

	// First should be system prompt.
	if messages[0].Role != provider.RoleSystem || !strings.Contains(messages[0].Content, "personal AI assistant") {
		t.Errorf("first message should be system prompt, got role=%s content=%q", messages[0].Role, messages[0].Content[:50])
	}

	// Second should be the snapshot (session state).
	if !strings.Contains(messages[1].Content, "[Session State — auto-recovered]") {
		t.Errorf("second message should be snapshot, got: %q", messages[1].Content[:50])
	}

	// Last should be the current user message.
	lastMsg := messages[len(messages)-1]
	if lastMsg.Role != provider.RoleUser || !strings.Contains(lastMsg.Content, "test order") {
		t.Errorf("last message should be current user message, got role=%s content=%q", lastMsg.Role, lastMsg.Content)
	}
}

func TestBuildSystemPromptContainsContinuity(t *testing.T) {
	cfg := DefaultConfig()
	prompt := cfg.BuildSystemPrompt()

	if !strings.Contains(prompt, "Session Continuity") {
		t.Error("system prompt should contain Session Continuity section")
	}
	if !strings.Contains(prompt, "checkpoint tool") {
		t.Error("system prompt should mention checkpoint tool")
	}
	if !strings.Contains(prompt, "[Session State]") {
		t.Error("system prompt should reference [Session State] format")
	}
}

// --- switchingProvider is a flexible mock that delegates to a callback ---

type switchingProvider struct {
	callFunc func(ctx context.Context, req provider.CompletionRequest) (*provider.CompletionResponse, error)
}

func (sp *switchingProvider) Complete(ctx context.Context, req provider.CompletionRequest) (*provider.CompletionResponse, error) {
	return sp.callFunc(ctx, req)
}

func (sp *switchingProvider) Name() string { return "switching-mock" }
