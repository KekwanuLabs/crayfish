package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/storage"
)

// DashboardAPI provides HTTP endpoints for the admin dashboard.
type DashboardAPI struct {
	db       *storage.DB
	bus      bus.Bus
	appRef   AppAccessor
	adapters func() []string
	logger   *slog.Logger
}

// NewDashboardAPI creates a new dashboard API handler.
func NewDashboardAPI(db *storage.DB, b bus.Bus, appRef AppAccessor, adaptersFn func() []string, logger *slog.Logger) *DashboardAPI {
	return &DashboardAPI{
		db:       db,
		bus:      b,
		appRef:   appRef,
		adapters: adaptersFn,
		logger:   logger,
	}
}

// RegisterRoutes adds dashboard API endpoints to the HTTP mux.
func (api *DashboardAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/dashboard/overview", api.handleOverview)
	mux.HandleFunc("/api/dashboard/config", api.handleConfig)
	mux.HandleFunc("/api/dashboard/sessions", api.handleSessions)
	mux.HandleFunc("/api/dashboard/sessions/", api.handleSessionMessages)
	mux.HandleFunc("/api/dashboard/memory", api.handleMemory)
	mux.HandleFunc("/api/dashboard/memory/", api.handleMemoryDelete)
	mux.HandleFunc("/api/dashboard/events", api.handleEvents)
	mux.HandleFunc("/api/dashboard/snapshots", api.handleSnapshots)
}

func (api *DashboardAPI) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (api *DashboardAPI) writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// GET /api/dashboard/overview
func (api *DashboardAPI) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	db := api.db.Inner()
	ctx := r.Context()

	var messageCount, sessionCount, memoryCount int64
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&messageCount)
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&sessionCount)
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_fts").Scan(&memoryCount)

	lastEventID, _ := api.bus.LastID(ctx)

	resp := map[string]any{
		"messages":       messageCount,
		"sessions":       sessionCount,
		"memories":       memoryCount,
		"events":         lastEventID,
		"adapters":       api.adapters(),
		"uptime_seconds": int(api.appRef.Uptime().Seconds()),
		"version":        api.appRef.AppVersion(),
	}
	if voice := api.appRef.VoiceInstallProgress(); voice != nil {
		resp["voice"] = voice
	}
	api.writeJSON(w, resp)
}

// GET/PUT /api/dashboard/config
func (api *DashboardAPI) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.writeJSON(w, api.appRef.DashboardConfig())
	case http.MethodPut:
		var updates map[string]any
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			api.writeError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		restartNeeded, err := api.appRef.UpdateConfig(updates)
		if err != nil {
			api.writeError(w, "update failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		api.writeJSON(w, map[string]any{
			"status":         "saved",
			"restart_needed": restartNeeded,
		})
	default:
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /api/dashboard/sessions
func (api *DashboardAPI) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	db := api.db.Inner()
	rows, err := db.QueryContext(r.Context(),
		"SELECT id, channel, user_id, trust_tier, created_at, last_active FROM sessions ORDER BY last_active DESC LIMIT 50")
	if err != nil {
		api.writeError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type session struct {
		ID         string `json:"id"`
		Channel    string `json:"channel"`
		UserID     string `json:"user_id"`
		TrustTier  int    `json:"trust_tier"`
		CreatedAt  string `json:"created_at"`
		LastActive string `json:"last_active"`
	}

	var sessions []session
	for rows.Next() {
		var s session
		if err := rows.Scan(&s.ID, &s.Channel, &s.UserID, &s.TrustTier, &s.CreatedAt, &s.LastActive); err != nil {
			continue
		}
		sessions = append(sessions, s)
	}
	if sessions == nil {
		sessions = []session{}
	}

	api.writeJSON(w, map[string]any{"sessions": sessions, "count": len(sessions)})
}

// GET /api/dashboard/sessions/{id}/messages
func (api *DashboardAPI) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract session ID: /api/dashboard/sessions/{id}/messages
	path := strings.TrimPrefix(r.URL.Path, "/api/dashboard/sessions/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || parts[1] != "messages" || parts[0] == "" {
		api.writeError(w, "invalid path, expected /api/dashboard/sessions/{id}/messages", http.StatusBadRequest)
		return
	}
	sessionID := parts[0]

	db := api.db.Inner()
	rows, err := db.QueryContext(r.Context(),
		"SELECT role, content, created_at FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT 100",
		sessionID)
	if err != nil {
		api.writeError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type message struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		CreatedAt string `json:"created_at"`
	}

	var messages []message
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.Role, &m.Content, &m.CreatedAt); err != nil {
			continue
		}
		messages = append(messages, m)
	}
	if messages == nil {
		messages = []message{}
	}

	// Reverse to chronological order.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	api.writeJSON(w, map[string]any{"messages": messages, "count": len(messages)})
}

// GET /api/dashboard/memory?q=
// DELETE /api/dashboard/memory/{id}
func (api *DashboardAPI) handleMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	db := api.db.Inner()
	q := r.URL.Query().Get("q")

	type memoryEntry struct {
		RowID     int64  `json:"id"`
		Key       string `json:"key"`
		Content   string `json:"content"`
		SessionID string `json:"session_id"`
		CreatedAt string `json:"created_at"`
		Category  string `json:"category"`
	}

	var entries []memoryEntry
	var rows interface {
		Next() bool
		Scan(...any) error
		Close() error
	}
	var err error

	if q != "" {
		rows2, err2 := db.QueryContext(r.Context(),
			`SELECT f.rowid, f.key, f.content, f.session_id, f.created_at, COALESCE(m.category, 'general')
			FROM memory_fts f LEFT JOIN memory_metadata m ON f.rowid = m.id
			WHERE memory_fts MATCH ? LIMIT 50`, q)
		if err2 != nil {
			api.writeError(w, "search failed: "+err2.Error(), http.StatusInternalServerError)
			return
		}
		rows = rows2
		err = err2
	} else {
		rows2, err2 := db.QueryContext(r.Context(),
			`SELECT f.rowid, f.key, f.content, f.session_id, f.created_at, COALESCE(m.category, 'general')
			FROM memory_fts f LEFT JOIN memory_metadata m ON f.rowid = m.id
			ORDER BY f.rowid DESC LIMIT 50`)
		if err2 != nil {
			api.writeError(w, "query failed: "+err2.Error(), http.StatusInternalServerError)
			return
		}
		rows = rows2
		err = err2
	}
	_ = err
	defer rows.Close()

	for rows.Next() {
		var e memoryEntry
		if err := rows.Scan(&e.RowID, &e.Key, &e.Content, &e.SessionID, &e.CreatedAt, &e.Category); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []memoryEntry{}
	}

	api.writeJSON(w, map[string]any{"memories": entries, "count": len(entries)})
}

// DELETE /api/dashboard/memory/{id}
func (api *DashboardAPI) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := strings.TrimPrefix(r.URL.Path, "/api/dashboard/memory/")
	if idStr == "" {
		api.writeError(w, "memory ID required", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		api.writeError(w, "invalid memory ID", http.StatusBadRequest)
		return
	}

	db := api.db.Inner()
	_, err = db.ExecContext(r.Context(), "DELETE FROM memory_fts WHERE rowid = ?", id)
	if err != nil {
		api.writeError(w, "delete failed", http.StatusInternalServerError)
		return
	}
	// Also delete metadata if it exists.
	db.ExecContext(r.Context(), "DELETE FROM memory_metadata WHERE id = ?", id)

	api.writeJSON(w, map[string]string{"status": "deleted"})
}

// GET /api/dashboard/events?limit=50&type=
func (api *DashboardAPI) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	db := api.db.Inner()
	eventType := r.URL.Query().Get("type")

	type event struct {
		ID        int64  `json:"id"`
		Type      string `json:"type"`
		Channel   string `json:"channel"`
		SessionID string `json:"session_id"`
		CreatedAt string `json:"created_at"`
	}

	var events []event
	var queryRows interface {
		Next() bool
		Scan(...any) error
		Close() error
	}

	if eventType != "" {
		r2, err := db.QueryContext(r.Context(),
			"SELECT id, type, channel, session_id, created_at FROM events WHERE type LIKE ? ORDER BY id DESC LIMIT ?",
			eventType+".%", limit)
		if err != nil {
			api.writeError(w, "query failed", http.StatusInternalServerError)
			return
		}
		queryRows = r2
	} else {
		r2, err := db.QueryContext(r.Context(),
			"SELECT id, type, channel, session_id, created_at FROM events ORDER BY id DESC LIMIT ?",
			limit)
		if err != nil {
			api.writeError(w, "query failed", http.StatusInternalServerError)
			return
		}
		queryRows = r2
	}
	defer queryRows.Close()

	for queryRows.Next() {
		var e event
		if err := queryRows.Scan(&e.ID, &e.Type, &e.Channel, &e.SessionID, &e.CreatedAt); err != nil {
			continue
		}
		events = append(events, e)
	}
	if events == nil {
		events = []event{}
	}

	api.writeJSON(w, map[string]any{"events": events, "count": len(events)})
}

// GET /api/dashboard/snapshots
func (api *DashboardAPI) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	db := api.db.Inner()
	rows, err := db.QueryContext(r.Context(),
		`SELECT id, session_id, trigger, active_task, is_current, created_at
		FROM session_snapshots ORDER BY id DESC LIMIT 50`)
	if err != nil {
		api.writeError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type snapshot struct {
		ID         int64  `json:"id"`
		SessionID  string `json:"session_id"`
		Trigger    string `json:"trigger"`
		ActiveTask string `json:"active_task"`
		IsCurrent  bool   `json:"is_current"`
		CreatedAt  string `json:"created_at"`
	}

	var snapshots []snapshot
	for rows.Next() {
		var s snapshot
		if err := rows.Scan(&s.ID, &s.SessionID, &s.Trigger, &s.ActiveTask, &s.IsCurrent, &s.CreatedAt); err != nil {
			continue
		}
		snapshots = append(snapshots, s)
	}
	if snapshots == nil {
		snapshots = []snapshot{}
	}

	api.writeJSON(w, map[string]any{"snapshots": snapshots, "count": len(snapshots)})
}
