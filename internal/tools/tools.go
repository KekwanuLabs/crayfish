// Package tools implements the built-in tool system for Crayfish v1.
// Each tool declares its minimum trust tier and execution logic.
// No plugin system in v1 — simplicity and auditability over extensibility.
package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/channels"
	"github.com/KekwanuLabs/crayfish/internal/security"
)

// Tool represents a built-in tool that the agent can invoke.
type Tool struct {
	Name        string                                                                    `json:"name"`
	Description string                                                                    `json:"description"`
	MinTier     security.TrustTier                                                        `json:"-"`
	InputSchema json.RawMessage                                                           `json:"input_schema"`
	Execute     func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) `json:"-"`
}

// Registry holds all registered tools and provides lookup/filtering.
// Thread-safe: tools can be registered from background goroutines (e.g., after OAuth completes).
type Registry struct {
	mu     sync.RWMutex
	tools  map[string]*Tool
	logger *slog.Logger
}

// NewRegistry creates an empty tool registry.
func NewRegistry(logger *slog.Logger) *Registry {
	return &Registry{
		tools:  make(map[string]*Tool),
		logger: logger,
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name] = tool
	r.logger.Debug("tool registered", "name", tool.Name, "min_tier", tool.MinTier)
}

// Get returns a tool by name, or nil if not found.
func (r *Registry) Get(name string) *Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// ForTier returns all tools accessible at the given trust tier.
func (r *Registry) ForTier(tier security.TrustTier) []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Tool
	for _, t := range r.tools {
		if tier >= t.MinTier {
			result = append(result, t)
		}
	}
	return result
}

// Execute runs a tool by name after checking the session's trust tier.
func (r *Registry) Execute(ctx context.Context, sess *security.Session, name string, input json.RawMessage) (string, error) {
	r.mu.RLock()
	tool := r.tools[name]
	r.mu.RUnlock()

	if tool == nil {
		return "", fmt.Errorf("tools.Execute: unknown tool %q", name)
	}

	if !security.CanUseTool(sess, name, tool.MinTier) {
		return "", fmt.Errorf("tools.Execute: trust tier %s cannot use tool %q (requires %s)",
			sess.Trust, name, tool.MinTier)
	}

	r.logger.Info("executing tool", "name", name, "session", sess.ID, "trust", sess.Trust)
	return tool.Execute(ctx, sess, input)
}

// RegisterBuiltins adds all v1.0 built-in tools to the registry.
// It takes a registry, channel adapters, and a database connection for stateful tools.
func RegisterBuiltins(reg *Registry, adapters map[string]channels.ChannelAdapter, db *sql.DB) {
	// Initialize database tables for stateful tools.
	initializeTables(db, reg.logger)

	// message.send — send a message via channel adapter.
	reg.Register(&Tool{
		Name:        "message_send",
		Description: "Send a message to a channel. Specify 'channel' (adapter name), 'to' (recipient), and 'text' (message content).",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"channel": {"type": "string", "description": "Channel adapter name (e.g., 'cli', 'telegram')"},
				"to": {"type": "string", "description": "Recipient identifier"},
				"text": {"type": "string", "description": "Message text to send"}
			},
			"required": ["channel", "to", "text"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Channel string `json:"channel"`
				To      string `json:"to"`
				Text    string `json:"text"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("message.send: parse input: %w", err)
			}

			if params.Channel == "" {
				return "", fmt.Errorf("message.send: channel is required")
			}
			if params.To == "" {
				return "", fmt.Errorf("message.send: to is required")
			}
			if params.Text == "" {
				return "", fmt.Errorf("message.send: text is required")
			}

			adapter := adapters[params.Channel]
			if adapter == nil {
				return "", fmt.Errorf("message.send: no adapter found for channel %q", params.Channel)
			}

			if err := adapter.Send(ctx, channels.OutboundMessage{
				To:   params.To,
				Text: params.Text,
			}); err != nil {
				return "", fmt.Errorf("message.send: %w", err)
			}

			return fmt.Sprintf("Message sent to %s via %s", params.To, adapter.Name()), nil
		},
	})

	// message.schedule — schedule a message to be sent after a delay.
	reg.Register(&Tool{
		Name:        "message_schedule",
		Description: "Schedule a message to be sent after a delay. Specify 'channel', 'to', 'text', and 'delay_seconds'.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"channel": {"type": "string", "description": "Channel adapter name"},
				"to": {"type": "string", "description": "Recipient identifier"},
				"text": {"type": "string", "description": "Message text to send"},
				"delay_seconds": {"type": "integer", "description": "Delay in seconds before sending", "minimum": 1}
			},
			"required": ["channel", "to", "text", "delay_seconds"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Channel      string `json:"channel"`
				To           string `json:"to"`
				Text         string `json:"text"`
				DelaySeconds int    `json:"delay_seconds"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("message.schedule: parse input: %w", err)
			}

			if params.Channel == "" {
				return "", fmt.Errorf("message.schedule: channel is required")
			}
			if params.To == "" {
				return "", fmt.Errorf("message.schedule: to is required")
			}
			if params.Text == "" {
				return "", fmt.Errorf("message.schedule: text is required")
			}
			if params.DelaySeconds < 1 {
				return "", fmt.Errorf("message.schedule: delay_seconds must be at least 1")
			}

			adapter := adapters[params.Channel]
			if adapter == nil {
				return "", fmt.Errorf("message.schedule: no adapter found for channel %q", params.Channel)
			}

			// Launch goroutine to send the message after delay.
			go func() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(params.DelaySeconds) * time.Second):
					adapter.Send(context.Background(), channels.OutboundMessage{
						To:   params.To,
						Text: params.Text,
					})
				}
			}()

			return fmt.Sprintf("Message scheduled to be sent to %s in %d seconds", params.To, params.DelaySeconds), nil
		},
	})

	// http.fetch — fetch a URL. Requires Operator trust tier.
	reg.Register(&Tool{
		Name:        "http_fetch",
		Description: "Fetch content from a URL. Specify 'url' and optionally 'method' (default: GET).",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {"type": "string", "description": "URL to fetch"},
				"method": {"type": "string", "description": "HTTP method (GET, POST, etc.). Default: GET"}
			},
			"required": ["url"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				URL    string `json:"url"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("http.fetch: parse input: %w", err)
			}

			if params.URL == "" {
				return "", fmt.Errorf("http.fetch: url is required")
			}

			if params.Method == "" {
				params.Method = "GET"
			}
			params.Method = strings.ToUpper(params.Method)

			// Parse URL to extract domain.
			u, err := url.Parse(params.URL)
			if err != nil {
				return "", fmt.Errorf("http.fetch: invalid url: %w", err)
			}

			// Reject non-HTTP(S) schemes.
			if u.Scheme != "http" && u.Scheme != "https" {
				return "", fmt.Errorf("http.fetch: only http/https URLs are supported")
			}

			// Create request with timeout.
			req, err := http.NewRequestWithContext(ctx, params.Method, params.URL, nil)
			if err != nil {
				return "", fmt.Errorf("http.fetch: create request: %w", err)
			}

			// Create client with 10 second timeout.
			client := &http.Client{
				Timeout: 10 * time.Second,
			}

			resp, err := client.Do(req)
			if err != nil {
				return "", fmt.Errorf("http.fetch: request failed: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return "", fmt.Errorf("http.fetch: http %d", resp.StatusCode)
			}

			// Read body with 32KB limit.
			limitedReader := io.LimitedReader{R: resp.Body, N: 32 * 1024}
			body, err := io.ReadAll(&limitedReader)
			if err != nil {
				return "", fmt.Errorf("http.fetch: read body: %w", err)
			}

			return fmt.Sprintf("Fetched %d bytes from %s", len(body), params.URL), nil
		},
	})

	// timer.set — set a countdown timer that fires via a goroutine.
	reg.Register(&Tool{
		Name:        "timer_set",
		Description: "Set a countdown timer that fires after the specified duration. Specify 'name', 'seconds', and 'message'.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "Name/label for the timer"},
				"seconds": {"type": "integer", "description": "Duration in seconds", "minimum": 1},
				"message": {"type": "string", "description": "Message to display when timer fires"}
			},
			"required": ["name", "seconds", "message"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Name    string `json:"name"`
				Seconds int    `json:"seconds"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("timer.set: parse input: %w", err)
			}

			if params.Name == "" {
				return "", fmt.Errorf("timer.set: name is required")
			}
			if params.Seconds < 1 {
				return "", fmt.Errorf("timer.set: seconds must be at least 1")
			}
			if params.Message == "" {
				return "", fmt.Errorf("timer.set: message is required")
			}

			// Launch goroutine to fire the timer.
			go func() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(params.Seconds) * time.Second):
					reg.logger.Info("timer fired", "name", params.Name, "message", params.Message)
				}
			}()

			return fmt.Sprintf("Timer %q set for %d seconds", params.Name, params.Seconds), nil
		},
	})

	// todo.add — add an item to the todo list.
	reg.Register(&Tool{
		Name:        "todo_add",
		Description: "Add an item to the todo list. Specify 'text' for the todo item content.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"text": {"type": "string", "description": "Todo item text"}
			},
			"required": ["text"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("todo.add: parse input: %w", err)
			}

			if params.Text == "" {
				return "", fmt.Errorf("todo.add: text is required")
			}

			result, err := db.ExecContext(ctx,
				"INSERT INTO todos (text, completed, created_at) VALUES (?, 0, datetime('now'))",
				params.Text)
			if err != nil {
				return "", fmt.Errorf("todo.add: insert: %w", err)
			}

			id, err := result.LastInsertId()
			if err != nil {
				return "", fmt.Errorf("todo.add: get id: %w", err)
			}

			return fmt.Sprintf("Added todo #%d: %s", id, params.Text), nil
		},
	})

	// todo.list — list all todo items.
	reg.Register(&Tool{
		Name:        "todo_list",
		Description: "List all todo items from the todo list.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			rows, err := db.QueryContext(ctx,
				"SELECT id, text, completed FROM todos ORDER BY created_at ASC")
			if err != nil {
				return "", fmt.Errorf("todo.list: query: %w", err)
			}
			defer rows.Close()

			var items []string
			for rows.Next() {
				var id int64
				var text string
				var completed int

				if err := rows.Scan(&id, &text, &completed); err != nil {
					return "", fmt.Errorf("todo.list: scan: %w", err)
				}

				status := "[ ]"
				if completed == 1 {
					status = "[x]"
				}
				items = append(items, fmt.Sprintf("%s #%d: %s", status, id, text))
			}

			if err := rows.Err(); err != nil {
				return "", fmt.Errorf("todo.list: rows: %w", err)
			}

			if len(items) == 0 {
				return "No todos found", nil
			}

			return "Todo list:\n" + strings.Join(items, "\n"), nil
		},
	})

	// memory.save — save a key-value note to the FTS5 memory table.
	reg.Register(&Tool{
		Name:        "memory_save",
		Description: "Save a note to memory with a key. Specify 'key', 'content', and optionally 'category' and 'importance'.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"key": {"type": "string", "description": "Key/title for the memory entry"},
				"content": {"type": "string", "description": "Content to store"},
				"category": {"type": "string", "description": "Category (preference, personal, decision, context, general). Default: general"},
				"importance": {"type": "integer", "description": "Importance level (1-10). Default: 5", "minimum": 1, "maximum": 10}
			},
			"required": ["key", "content"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Key        string `json:"key"`
				Content    string `json:"content"`
				Category   string `json:"category"`
				Importance int    `json:"importance"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("memory.save: parse input: %w", err)
			}

			if params.Key == "" {
				return "", fmt.Errorf("memory.save: key is required")
			}
			if params.Content == "" {
				return "", fmt.Errorf("memory.save: content is required")
			}

			// Set defaults
			if params.Category == "" {
				params.Category = "general"
			}
			if params.Importance <= 0 {
				params.Importance = 5
			}
			if params.Importance > 10 {
				params.Importance = 10
			}

			// Insert into both tables
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return "", fmt.Errorf("memory.save: begin tx: %w", err)
			}
			defer tx.Rollback()

			result, err := tx.ExecContext(ctx,
				"INSERT INTO memory_fts (key, content, session_id, created_at) VALUES (?, ?, ?, datetime('now'))",
				params.Key, params.Content, sess.ID)
			if err != nil {
				return "", fmt.Errorf("memory.save: insert fts: %w", err)
			}

			rowID, err := result.LastInsertId()
			if err != nil {
				return "", fmt.Errorf("memory.save: get rowid: %w", err)
			}

			_, err = tx.ExecContext(ctx,
				`INSERT INTO memory_metadata (id, session_id, category, importance, source_context, created_at, access_count)
				VALUES (?, ?, ?, ?, 'Manual save via tool', datetime('now'), 0)`,
				rowID, sess.ID, params.Category, params.Importance)
			if err != nil {
				return "", fmt.Errorf("memory.save: insert metadata: %w", err)
			}

			if err := tx.Commit(); err != nil {
				return "", fmt.Errorf("memory.save: commit: %w", err)
			}

			return fmt.Sprintf("Saved to memory: %s (category: %s, importance: %d)", params.Key, params.Category, params.Importance), nil
		},
	})

	// memory.search — search memory via FTS5.
	reg.Register(&Tool{
		Name:        "memory_search",
		Description: "Search memory entries using full-text search. Specify 'query' and optionally 'category'.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "Search query"},
				"category": {"type": "string", "description": "Optional category filter (preference, personal, decision, context, general)"}
			},
			"required": ["query"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Query    string `json:"query"`
				Category string `json:"category"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("memory.search: parse input: %w", err)
			}

			if params.Query == "" {
				return "", fmt.Errorf("memory.search: query is required")
			}

			// Escape the search query for FTS5 (wrap in quotes, escape internal quotes)
			escapedQuery := `"` + strings.ReplaceAll(params.Query, `"`, `""`) + `"`

			// Build query with optional category filter
			query := `
				SELECT mf.key, mf.content, mm.category, mm.importance
				FROM memory_fts mf
				JOIN memory_metadata mm ON mf.rowid = mm.id
				WHERE memory_fts MATCH ? AND mm.session_id = ?`
			args := []interface{}{escapedQuery, sess.ID}

			if params.Category != "" {
				query += " AND mm.category = ?"
				args = append(args, params.Category)
			}

			query += " ORDER BY rank, mm.importance DESC LIMIT 20"

			rows, err := db.QueryContext(ctx, query, args...)
			if err != nil {
				return "", fmt.Errorf("memory.search: query: %w", err)
			}
			defer rows.Close()

			var results []string
			for rows.Next() {
				var key, content, category string
				var importance int
				if err := rows.Scan(&key, &content, &category, &importance); err != nil {
					return "", fmt.Errorf("memory.search: scan: %w", err)
				}
				results = append(results, fmt.Sprintf("- [%s|%d] %s: %s", category, importance, key, content))
			}

			if err := rows.Err(); err != nil {
				return "", fmt.Errorf("memory.search: rows: %w", err)
			}

			if len(results) == 0 {
				return fmt.Sprintf("No results found for query: %s", params.Query), nil
			}

			return fmt.Sprintf("Memory search results for '%s':\n%s", params.Query, strings.Join(results, "\n")), nil
		},
	})

	// memory.list — list all memories for the session
	reg.Register(&Tool{
		Name:        "memory_list",
		Description: "List all saved memories, optionally filtered by category. Specify optional 'category' and 'limit'.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"category": {"type": "string", "description": "Optional category filter (preference, personal, decision, context, general)"},
				"limit": {"type": "integer", "description": "Maximum number of results (default: 20)", "minimum": 1, "maximum": 100}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Category string `json:"category"`
				Limit    int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("memory.list: parse input: %w", err)
			}

			if params.Limit <= 0 {
				params.Limit = 20
			}
			if params.Limit > 100 {
				params.Limit = 100
			}

			query := `
				SELECT mf.key, mf.content, mm.category, mm.importance, mf.created_at
				FROM memory_fts mf
				JOIN memory_metadata mm ON mf.rowid = mm.id
				WHERE mm.session_id = ?`
			args := []interface{}{sess.ID}

			if params.Category != "" {
				query += " AND mm.category = ?"
				args = append(args, params.Category)
			}

			query += " ORDER BY mf.created_at DESC LIMIT ?"
			args = append(args, params.Limit)

			rows, err := db.QueryContext(ctx, query, args...)
			if err != nil {
				return "", fmt.Errorf("memory.list: query: %w", err)
			}
			defer rows.Close()

			var results []string
			for rows.Next() {
				var key, content, category, createdAt string
				var importance int
				if err := rows.Scan(&key, &content, &category, &importance, &createdAt); err != nil {
					return "", fmt.Errorf("memory.list: scan: %w", err)
				}
				results = append(results, fmt.Sprintf("- [%s|%d] %s: %s (created: %s)", category, importance, key, content, createdAt))
			}

			if err := rows.Err(); err != nil {
				return "", fmt.Errorf("memory.list: rows: %w", err)
			}

			if len(results) == 0 {
				return "No memories found", nil
			}

			return fmt.Sprintf("Memories (%d):\n%s", len(results), strings.Join(results, "\n")), nil
		},
	})

	// memory.delete — delete a memory entry
	reg.Register(&Tool{
		Name:        "memory_delete",
		Description: "Delete a memory entry by its key. Specify 'key'.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"key": {"type": "string", "description": "Key of the memory entry to delete"}
			},
			"required": ["key"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Key string `json:"key"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("memory.delete: parse input: %w", err)
			}

			if params.Key == "" {
				return "", fmt.Errorf("memory.delete: key is required")
			}

			// Delete from both tables
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return "", fmt.Errorf("memory.delete: begin tx: %w", err)
			}
			defer tx.Rollback()

			// Get rowid first
			var rowID int64
			err = tx.QueryRowContext(ctx,
				"SELECT rowid FROM memory_fts WHERE key = ? AND session_id = ?",
				params.Key, sess.ID).Scan(&rowID)
			if err != nil {
				if err == sql.ErrNoRows {
					return fmt.Sprintf("Memory not found: %s", params.Key), nil
				}
				return "", fmt.Errorf("memory.delete: query rowid: %w", err)
			}

			// Delete metadata
			_, err = tx.ExecContext(ctx, "DELETE FROM memory_metadata WHERE id = ?", rowID)
			if err != nil {
				return "", fmt.Errorf("memory.delete: delete metadata: %w", err)
			}

			// Delete FTS entry
			_, err = tx.ExecContext(ctx,
				"DELETE FROM memory_fts WHERE rowid = ?", rowID)
			if err != nil {
				return "", fmt.Errorf("memory.delete: delete fts: %w", err)
			}

			if err := tx.Commit(); err != nil {
				return "", fmt.Errorf("memory.delete: commit: %w", err)
			}

			return fmt.Sprintf("Deleted memory: %s", params.Key), nil
		},
	})

	// memory.update — update an existing memory
	reg.Register(&Tool{
		Name:        "memory_update",
		Description: "Update an existing memory's content or metadata. Specify 'key' and optional 'new_content', 'new_importance', 'new_category'.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"key": {"type": "string", "description": "Key of the memory entry to update"},
				"new_content": {"type": "string", "description": "New content for the memory"},
				"new_importance": {"type": "integer", "description": "New importance (1-10)", "minimum": 1, "maximum": 10},
				"new_category": {"type": "string", "description": "New category (preference, personal, decision, context, general)"}
			},
			"required": ["key"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Key            string `json:"key"`
				NewContent     string `json:"new_content"`
				NewImportance  int    `json:"new_importance"`
				NewCategory    string `json:"new_category"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("memory.update: parse input: %w", err)
			}

			if params.Key == "" {
				return "", fmt.Errorf("memory.update: key is required")
			}

			if params.NewContent == "" && params.NewImportance == 0 && params.NewCategory == "" {
				return "", fmt.Errorf("memory.update: at least one field must be specified to update")
			}

			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return "", fmt.Errorf("memory.update: begin tx: %w", err)
			}
			defer tx.Rollback()

			// Get rowid
			var rowID int64
			err = tx.QueryRowContext(ctx,
				"SELECT rowid FROM memory_fts WHERE key = ? AND session_id = ?",
				params.Key, sess.ID).Scan(&rowID)
			if err != nil {
				if err == sql.ErrNoRows {
					return fmt.Sprintf("Memory not found: %s", params.Key), nil
				}
				return "", fmt.Errorf("memory.update: query rowid: %w", err)
			}

			// Update content if provided
			if params.NewContent != "" {
				// FTS5 doesn't support UPDATE, need to delete and reinsert
				var oldSessionID, oldCreatedAt string
				err = tx.QueryRowContext(ctx,
					"SELECT session_id, created_at FROM memory_fts WHERE rowid = ?",
					rowID).Scan(&oldSessionID, &oldCreatedAt)
				if err != nil {
					return "", fmt.Errorf("memory.update: query old data: %w", err)
				}

				_, err = tx.ExecContext(ctx, "DELETE FROM memory_fts WHERE rowid = ?", rowID)
				if err != nil {
					return "", fmt.Errorf("memory.update: delete old fts: %w", err)
				}

				_, err = tx.ExecContext(ctx,
					"INSERT INTO memory_fts (rowid, key, content, session_id, created_at) VALUES (?, ?, ?, ?, ?)",
					rowID, params.Key, params.NewContent, oldSessionID, oldCreatedAt)
				if err != nil {
					return "", fmt.Errorf("memory.update: insert new fts: %w", err)
				}
			}

			// Update metadata
			var updates []string
			var args []interface{}

			if params.NewImportance > 0 {
				updates = append(updates, "importance = ?")
				args = append(args, params.NewImportance)
			}
			if params.NewCategory != "" {
				updates = append(updates, "category = ?")
				args = append(args, params.NewCategory)
			}

			if len(updates) > 0 {
				args = append(args, rowID)
				query := fmt.Sprintf("UPDATE memory_metadata SET %s WHERE id = ?", strings.Join(updates, ", "))
				_, err = tx.ExecContext(ctx, query, args...)
				if err != nil {
					return "", fmt.Errorf("memory.update: update metadata: %w", err)
				}
			}

			if err := tx.Commit(); err != nil {
				return "", fmt.Errorf("memory.update: commit: %w", err)
			}

			return fmt.Sprintf("Updated memory: %s", params.Key), nil
		},
	})

	// memory.stats — show memory statistics
	reg.Register(&Tool{
		Name:        "memory_stats",
		Description: "Show memory usage statistics for this session.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			// Total count
			var total int
			err := db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM memory_metadata WHERE session_id = ?",
				sess.ID).Scan(&total)
			if err != nil {
				return "", fmt.Errorf("memory.stats: count: %w", err)
			}

			// Count by category
			rows, err := db.QueryContext(ctx,
				"SELECT category, COUNT(*) FROM memory_metadata WHERE session_id = ? GROUP BY category",
				sess.ID)
			if err != nil {
				return "", fmt.Errorf("memory.stats: category count: %w", err)
			}
			defer rows.Close()

			categoryStats := make(map[string]int)
			for rows.Next() {
				var category string
				var count int
				if err := rows.Scan(&category, &count); err != nil {
					return "", fmt.Errorf("memory.stats: scan: %w", err)
				}
				categoryStats[category] = count
			}

			// Most accessed
			var mostAccessedKey string
			var accessCount int
			err = db.QueryRowContext(ctx,
				`SELECT mf.key, mm.access_count
				FROM memory_metadata mm
				JOIN memory_fts mf ON mf.rowid = mm.id
				WHERE mm.session_id = ?
				ORDER BY mm.access_count DESC
				LIMIT 1`,
				sess.ID).Scan(&mostAccessedKey, &accessCount)
			if err != nil && err != sql.ErrNoRows {
				return "", fmt.Errorf("memory.stats: most accessed: %w", err)
			}

			// Build response
			var builder strings.Builder
			builder.WriteString(fmt.Sprintf("Memory Statistics:\n\nTotal memories: %d\n\n", total))

			if len(categoryStats) > 0 {
				builder.WriteString("By category:\n")
				for cat, count := range categoryStats {
					builder.WriteString(fmt.Sprintf("  %s: %d\n", cat, count))
				}
			}

			if mostAccessedKey != "" {
				builder.WriteString(fmt.Sprintf("\nMost accessed: %s (%d times)\n", mostAccessedKey, accessCount))
			}

			return builder.String(), nil
		},
	})

	// forms.confirm — present a yes/no question.
	// Phase 1: logs the question. Phase 2 will handle actual confirmation flow.
	reg.Register(&Tool{
		Name:        "forms_confirm",
		Description: "Present a yes/no confirmation question. Specify 'question'. Returns 'awaiting_confirmation' (actual confirmation is Phase 2).",
		MinTier:     security.TierUnknown,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"question": {"type": "string", "description": "The yes/no question to ask"}
			},
			"required": ["question"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Question string `json:"question"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("forms.confirm: parse input: %w", err)
			}

			if params.Question == "" {
				return "", fmt.Errorf("forms.confirm: question is required")
			}

			reg.logger.Info("confirmation requested", "question", params.Question)
			return "awaiting_confirmation", nil
		},
	})
}


// initializeTables creates the todos table if it doesn't exist.
func initializeTables(db *sql.DB, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS todos (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			text      TEXT    NOT NULL,
			completed INTEGER NOT NULL DEFAULT 0,
			created_at TEXT   NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		logger.Error("failed to create todos table", "error", err)
	}
}
