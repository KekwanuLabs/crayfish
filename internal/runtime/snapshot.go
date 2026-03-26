package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/provider"
)

const snapshotPrompt = `Analyze this conversation and create a state snapshot. Write as if you'll wake up with amnesia.

REQUIRED (return JSON only, no markdown fences):
{
  "active_task": "What are we working on? If idle, what are we waiting for?",
  "active_task_context": "Why and how — not just what",
  "last_exchanges": [{"role": "user/assistant", "summary": "paraphrase what was SAID"}],
  "pending_proposals": ["anything said but not addressed (VERBATIM)"],
  "decisions_in_flight": ["discussed but not decided"],
  "conversational_tone": "User's emotional state in 1-2 words",
  "key_resources": ["files, URLs, topics in play"]
}

Conversation:
%s`

// Snapshot represents a saved session state snapshot.
type Snapshot struct {
	ID                 int64
	SessionID          string
	Trigger            string
	ActiveTask         string
	ActiveTaskContext  string
	LastExchanges      string // JSON array
	PendingProposals   string // JSON array
	DecisionsInFlight  string // JSON array
	ConversationalTone string
	KeyResources       string // JSON array
	MessageCount       int
	IsCurrent          bool
	CreatedAt          string
}

// snapshotResponse is the JSON schema returned by the LLM.
type snapshotResponse struct {
	ActiveTask         string              `json:"active_task"`
	ActiveTaskContext  string              `json:"active_task_context"`
	LastExchanges      []map[string]string `json:"last_exchanges"`
	PendingProposals   []string            `json:"pending_proposals"`
	DecisionsInFlight  []string            `json:"decisions_in_flight"`
	ConversationalTone string              `json:"conversational_tone"`
	KeyResources       []string            `json:"key_resources"`
}

// SnapshotManager handles session state snapshots for continuity across summarization.
type SnapshotManager struct {
	db       *sql.DB
	provider provider.Provider
	logger   *slog.Logger
}

// NewSnapshotManager creates a new SnapshotManager.
func NewSnapshotManager(db *sql.DB, prov provider.Provider, logger *slog.Logger) *SnapshotManager {
	return &SnapshotManager{
		db:       db,
		provider: prov,
		logger:   logger,
	}
}

// GenerateAndSave analyzes messages via LLM and saves a snapshot.
func (sm *SnapshotManager) GenerateAndSave(ctx context.Context, sessionID, trigger string, messages []provider.Message) error {
	if len(messages) == 0 {
		return nil
	}

	// Build conversation text
	var sb strings.Builder
	for _, msg := range messages {
		if msg.Role == provider.RoleSystem {
			continue
		}
		fmt.Fprintf(&sb, "[%s]: %s\n", msg.Role, msg.Content)
	}

	prompt := fmt.Sprintf(snapshotPrompt, sb.String())

	req := provider.CompletionRequest{
		Messages: []provider.Message{
			{
				Role:    provider.RoleSystem,
				Content: "You are a precise state-capture assistant. Return only valid JSON, no markdown fences.",
			},
			{
				Role:    provider.RoleUser,
				Content: prompt,
			},
		},
		MaxTokens: 4096,
	}

	resp, err := sm.provider.Complete(ctx, req)
	if err != nil {
		return fmt.Errorf("snapshot LLM call: %w", err)
	}

	if resp.Content == "" {
		return fmt.Errorf("empty snapshot response from LLM")
	}

	// Strip markdown fences if present
	content := strings.TrimSpace(resp.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var snap snapshotResponse
	if err := json.Unmarshal([]byte(content), &snap); err != nil {
		sm.logger.Warn("failed to parse snapshot JSON", "error", err, "response", content)
		return fmt.Errorf("parse snapshot JSON: %w", err)
	}

	return sm.saveSnapshot(ctx, sessionID, trigger, &snap, len(messages))
}

// SaveCheckpoint implements the tools.CheckpointSaver interface.
// It accepts the raw JSON input from the checkpoint tool and persists a manual snapshot.
func (sm *SnapshotManager) SaveCheckpoint(ctx context.Context, sessionID string, input json.RawMessage) error {
	var snap snapshotResponse
	if err := json.Unmarshal(input, &snap); err != nil {
		return fmt.Errorf("parse checkpoint input: %w", err)
	}
	return sm.saveSnapshot(ctx, sessionID, "manual", &snap, 0)
}

func (sm *SnapshotManager) saveSnapshot(ctx context.Context, sessionID, trigger string, snap *snapshotResponse, messageCount int) error {
	lastExchanges, err := json.Marshal(snap.LastExchanges)
	if err != nil {
		return fmt.Errorf("marshal last_exchanges: %w", err)
	}
	pendingProposals, err := json.Marshal(snap.PendingProposals)
	if err != nil {
		return fmt.Errorf("marshal pending_proposals: %w", err)
	}
	decisionsInFlight, err := json.Marshal(snap.DecisionsInFlight)
	if err != nil {
		return fmt.Errorf("marshal decisions_in_flight: %w", err)
	}
	keyResources, err := json.Marshal(snap.KeyResources)
	if err != nil {
		return fmt.Errorf("marshal key_resources: %w", err)
	}

	tx, err := sm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Mark previous snapshots as not current
	_, err = tx.ExecContext(ctx,
		"UPDATE session_snapshots SET is_current = 0 WHERE session_id = ? AND is_current = 1",
		sessionID)
	if err != nil {
		return fmt.Errorf("mark previous not current: %w", err)
	}

	// Insert new snapshot
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_snapshots
			(session_id, trigger, active_task, active_task_context, last_exchanges,
			 pending_proposals, decisions_in_flight, conversational_tone, key_resources,
			 message_count, is_current, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, datetime('now'))`,
		sessionID, trigger, snap.ActiveTask, snap.ActiveTaskContext,
		string(lastExchanges), string(pendingProposals), string(decisionsInFlight),
		snap.ConversationalTone, string(keyResources), messageCount)
	if err != nil {
		return fmt.Errorf("insert snapshot: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
	}

	sm.logger.Info("session snapshot saved",
		"session_id", sessionID, "trigger", trigger, "message_count", messageCount)

	// Promote open decisions to the todos table so they survive restarts and
	// session changes. Existing entries are skipped to avoid duplicates.
	if len(snap.DecisionsInFlight) > 0 {
		sm.syncDecisionsToTodos(ctx, snap.DecisionsInFlight)
	}

	return nil
}

// syncDecisionsToTodos writes each decision-in-flight to the todos table
// (list_name='decisions') if an identical entry doesn't already exist.
// This makes open decisions durable across restarts and session boundaries.
func (sm *SnapshotManager) syncDecisionsToTodos(ctx context.Context, decisions []string) {
	for _, d := range decisions {
		if d == "" {
			continue
		}
		// Insert only if this exact text isn't already an open decision todo.
		_, err := sm.db.ExecContext(ctx, `
			INSERT INTO todos (text, completed, list_name)
			SELECT ?, 0, 'decisions'
			WHERE NOT EXISTS (
				SELECT 1 FROM todos
				WHERE text = ? AND list_name = 'decisions' AND completed = 0
			)`, d, d)
		if err != nil {
			sm.logger.Warn("failed to sync decision to todos", "decision", d, "error", err)
		}
	}
}

// LoadLatest retrieves the most recent current snapshot for a session.
func (sm *SnapshotManager) LoadLatest(ctx context.Context, sessionID string) (*Snapshot, error) {
	row := sm.db.QueryRowContext(ctx, `
		SELECT id, session_id, trigger, active_task, active_task_context,
		       last_exchanges, pending_proposals, decisions_in_flight,
		       conversational_tone, key_resources, message_count, is_current, created_at
		FROM session_snapshots
		WHERE session_id = ? AND is_current = 1
		ORDER BY created_at DESC
		LIMIT 1`, sessionID)

	var s Snapshot
	err := row.Scan(&s.ID, &s.SessionID, &s.Trigger, &s.ActiveTask, &s.ActiveTaskContext,
		&s.LastExchanges, &s.PendingProposals, &s.DecisionsInFlight,
		&s.ConversationalTone, &s.KeyResources, &s.MessageCount, &s.IsCurrent, &s.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load latest snapshot: %w", err)
	}
	return &s, nil
}

// FormatForContext formats a snapshot as a system message for context injection.
func (sm *SnapshotManager) FormatForContext(snap *Snapshot) string {
	if snap == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[Session State — auto-recovered]\n")

	if snap.ActiveTask != "" {
		fmt.Fprintf(&sb, "Active task: %s\n", snap.ActiveTask)
	}
	if snap.ActiveTaskContext != "" {
		fmt.Fprintf(&sb, "Context: %s\n", snap.ActiveTaskContext)
	}

	// Format last exchanges
	var exchanges []map[string]string
	if err := json.Unmarshal([]byte(snap.LastExchanges), &exchanges); err == nil && len(exchanges) > 0 {
		sb.WriteString("Last exchanges:\n")
		for _, ex := range exchanges {
			role := ex["role"]
			summary := ex["summary"]
			if role != "" && summary != "" {
				fmt.Fprintf(&sb, "  - %s: %s\n", role, summary)
			}
		}
	}

	// Format pending proposals
	var pending []string
	if err := json.Unmarshal([]byte(snap.PendingProposals), &pending); err == nil && len(pending) > 0 {
		sb.WriteString("Pending proposals:\n")
		for _, p := range pending {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
	}

	// Format decisions in flight
	var decisions []string
	if err := json.Unmarshal([]byte(snap.DecisionsInFlight), &decisions); err == nil && len(decisions) > 0 {
		sb.WriteString("Decisions in flight:\n")
		for _, d := range decisions {
			fmt.Fprintf(&sb, "  - %s\n", d)
		}
	}

	if snap.ConversationalTone != "" {
		fmt.Fprintf(&sb, "Tone: %s\n", snap.ConversationalTone)
	}

	// Format key resources
	var resources []string
	if err := json.Unmarshal([]byte(snap.KeyResources), &resources); err == nil && len(resources) > 0 {
		sb.WriteString("Key resources: ")
		sb.WriteString(strings.Join(resources, ", "))
		sb.WriteString("\n")
	}

	// Surface stale open decisions from the persistent todos table.
	staleTodos := sm.loadStaleDecisions(snap.SessionID)
	if len(staleTodos) > 0 {
		sb.WriteString("Open decisions still pending your answer:\n")
		for _, d := range staleTodos {
			fmt.Fprintf(&sb, "  - %s\n", d)
		}
	}

	sb.WriteString("\nContinue naturally from this state.")
	return sb.String()
}

// loadStaleDecisions fetches open decision todos that haven't been completed.
// Called when formatting a snapshot for context injection, so the agent is
// always aware of outstanding decisions regardless of summarization state.
func (sm *SnapshotManager) loadStaleDecisions(sessionID string) []string {
	if sm.db == nil {
		return nil
	}
	rows, err := sm.db.QueryContext(context.Background(), `
		SELECT text FROM todos
		WHERE list_name = 'decisions' AND completed = 0
		ORDER BY created_at ASC
		LIMIT 20`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var decisions []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err == nil {
			decisions = append(decisions, text)
		}
	}
	return decisions
}

// Cleanup removes old snapshots, keeping only the most recent `keep` per session.
// Also deletes all snapshots from sessions older than maxAge.
func (sm *SnapshotManager) Cleanup(ctx context.Context, keep int, maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge).UTC().Format("2006-01-02 15:04:05")

	// Delete snapshots from old sessions
	_, err := sm.db.ExecContext(ctx,
		"DELETE FROM session_snapshots WHERE created_at < ?", cutoff)
	if err != nil {
		return fmt.Errorf("delete old snapshots: %w", err)
	}

	// For each session, keep only the most recent `keep` snapshots
	rows, err := sm.db.QueryContext(ctx,
		"SELECT DISTINCT session_id FROM session_snapshots")
	if err != nil {
		return fmt.Errorf("list snapshot sessions: %w", err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			continue
		}
		sessionIDs = append(sessionIDs, sid)
	}

	for _, sid := range sessionIDs {
		_, err := sm.db.ExecContext(ctx, `
			DELETE FROM session_snapshots
			WHERE session_id = ? AND id NOT IN (
				SELECT id FROM session_snapshots
				WHERE session_id = ?
				ORDER BY id DESC
				LIMIT ?
			)`, sid, sid, keep)
		if err != nil {
			sm.logger.Warn("failed to trim snapshots for session", "session_id", sid, "error", err)
		}
	}

	return nil
}

// IsSessionResume checks if the last message in a session is older than the given threshold,
// indicating the user is resuming after a gap.
func (sm *SnapshotManager) IsSessionResume(ctx context.Context, sessionID string, threshold time.Duration) bool {
	var lastMessageAt string
	err := sm.db.QueryRowContext(ctx,
		"SELECT MAX(created_at) FROM messages WHERE session_id = ?",
		sessionID).Scan(&lastMessageAt)
	if err != nil || lastMessageAt == "" {
		return false
	}

	lastTime, err := time.Parse("2006-01-02 15:04:05", lastMessageAt)
	if err != nil {
		return false
	}

	return time.Since(lastTime) > threshold
}
