package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// MemoryRetriever retrieves relevant memories from the database and formats them for LLM context.
type MemoryRetriever struct {
	db     *sql.DB
	logger *slog.Logger
}

// Memory represents a single retrieved memory fact.
type Memory struct {
	Key        string
	Content    string
	Category   string
	Importance int
	CreatedAt  string
}

// NewMemoryRetriever creates a new memory retriever instance.
func NewMemoryRetriever(db *sql.DB, logger *slog.Logger) *MemoryRetriever {
	return &MemoryRetriever{
		db:     db,
		logger: logger,
	}
}

// RetrieveRelevant retrieves the most relevant memories for a given query using FTS5 search.
// It filters by session_id for privacy and orders by FTS5 rank and importance.
func (m *MemoryRetriever) RetrieveRelevant(ctx context.Context, sessionID, query string, limit int) ([]Memory, error) {
	if query == "" {
		return nil, nil
	}

	if limit <= 0 {
		limit = 5
	}

	// Escape the query for FTS5 - wrap in quotes and escape internal quotes
	// This prevents special characters (commas, parentheses, etc.) from being interpreted as FTS5 operators
	escapedQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`

	// Query memory_fts joined with memory_metadata
	rows, err := m.db.QueryContext(ctx, `
		SELECT mf.key, mf.content, mm.category, mm.importance, mf.created_at
		FROM memory_fts mf
		JOIN memory_metadata mm ON mf.rowid = mm.id
		WHERE memory_fts MATCH ? AND mm.session_id = ?
		ORDER BY rank, mm.importance DESC
		LIMIT ?
	`, escapedQuery, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var mem Memory
		if err := rows.Scan(&mem.Key, &mem.Content, &mem.Category, &mem.Importance, &mem.CreatedAt); err != nil {
			m.logger.Warn("failed to scan memory row", "error", err)
			continue
		}
		memories = append(memories, mem)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	// Update access tracking for retrieved memories
	if len(memories) > 0 {
		m.updateAccessTracking(ctx, memories)
	}

	return memories, nil
}

// updateAccessTracking updates the last_accessed timestamp and access_count for retrieved memories.
func (m *MemoryRetriever) updateAccessTracking(ctx context.Context, memories []Memory) {
	// Build a list of keys for batch update
	var keys []string
	for _, mem := range memories {
		keys = append(keys, mem.Key)
	}

	if len(keys) == 0 {
		return
	}

	// Update access metadata
	placeholders := make([]string, len(keys))
	args := make([]interface{}, len(keys))
	for i, key := range keys {
		placeholders[i] = "?"
		args[i] = key
	}

	query := fmt.Sprintf(`
		UPDATE memory_metadata
		SET last_accessed = datetime('now'),
		    access_count = access_count + 1
		WHERE id IN (
			SELECT rowid FROM memory_fts WHERE key IN (%s)
		)
	`, strings.Join(placeholders, ","))

	if _, err := m.db.ExecContext(ctx, query, args...); err != nil {
		m.logger.Warn("failed to update access tracking", "error", err)
	}
}

// FormatForContext formats retrieved memories into a readable system message for the LLM.
func (m *MemoryRetriever) FormatForContext(memories []Memory) string {
	if len(memories) == 0 {
		return ""
	}

	// Group memories by category
	byCategory := make(map[string][]Memory)
	for _, mem := range memories {
		byCategory[mem.Category] = append(byCategory[mem.Category], mem)
	}

	var builder strings.Builder
	builder.WriteString("[Relevant memories from past conversations]\n\n")

	// Define category order and display names
	categoryOrder := []struct {
		key  string
		name string
	}{
		{"preference", "Preferences"},
		{"personal", "Personal Context"},
		{"decision", "Recent Decisions"},
		{"context", "Important Context"},
		{"general", "General"},
	}

	for _, cat := range categoryOrder {
		mems, exists := byCategory[cat.key]
		if !exists || len(mems) == 0 {
			continue
		}

		builder.WriteString(fmt.Sprintf("%s:\n", cat.name))
		for _, mem := range mems {
			builder.WriteString(fmt.Sprintf("- %s\n", mem.Content))
		}
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String())
}
