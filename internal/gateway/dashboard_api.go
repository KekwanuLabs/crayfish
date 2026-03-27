package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/firewall"
	"github.com/KekwanuLabs/crayfish/internal/storage"
)

// DashboardAPI provides HTTP endpoints for the admin dashboard.
type DashboardAPI struct {
	db         *storage.DB
	bus        bus.Bus
	appRef     AppAccessor
	adapters   func() []string
	logger     *slog.Logger
	restartFn  func() // injectable for tests; defaults to SIGTERM self
}

// NewDashboardAPI creates a new dashboard API handler.
func NewDashboardAPI(db *storage.DB, b bus.Bus, appRef AppAccessor, adaptersFn func() []string, logger *slog.Logger) *DashboardAPI {
	return &DashboardAPI{
		db:       db,
		bus:      b,
		appRef:   appRef,
		adapters: adaptersFn,
		logger:   logger,
		restartFn: func() {
			proc, _ := os.FindProcess(os.Getpid())
			proc.Signal(syscall.SIGTERM)
		},
	}
}

// RegisterRoutes adds dashboard API endpoints to the HTTP mux.
// The wrap function applies authentication middleware to each handler.
func (api *DashboardAPI) RegisterRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/dashboard/overview", wrap(api.handleOverview))
	mux.HandleFunc("/api/dashboard/config", wrap(api.handleConfig))
	mux.HandleFunc("/api/dashboard/sessions", wrap(api.handleSessions))
	mux.HandleFunc("/api/dashboard/sessions/", wrap(api.handleSessionMessages))
	mux.HandleFunc("/api/dashboard/memory", wrap(api.handleMemory))
	mux.HandleFunc("/api/dashboard/memory/", wrap(api.handleMemoryDelete))
	mux.HandleFunc("/api/dashboard/events", wrap(api.handleEvents))
	mux.HandleFunc("/api/dashboard/snapshots", wrap(api.handleSnapshots))
	mux.HandleFunc("/api/security/status", wrap(api.handleSecurityStatus))
	mux.HandleFunc("/api/network/status", wrap(api.handleNetworkStatus))
	mux.HandleFunc("/api/contacts", wrap(api.handleContacts))
	mux.HandleFunc("/api/contacts/", wrap(api.handleContactByID))
	mux.HandleFunc("/api/contacts/sync/google", wrap(api.handleContactsGoogleSync))
	mux.HandleFunc("/api/contacts/import/vcard", wrap(api.handleContactsVCardImport))
}

// GET /api/network/status — returns full network topology for the dashboard.
func (api *DashboardAPI) handleNetworkStatus(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()

	// Collect all active network interfaces.
	type ifaceInfo struct {
		Name string   `json:"name"`
		IPv4 []string `json:"ipv4"`
		IPv6 []string `json:"ipv6"`
	}
	var ifaces []ifaceInfo
	netIfaces, _ := net.Interfaces()
	for _, iface := range netIfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		info := ifaceInfo{Name: iface.Name}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ipNet.IP.To4() != nil {
				info.IPv4 = append(info.IPv4, ipNet.String())
			} else if len(ipNet.IP) == 16 {
				info.IPv6 = append(info.IPv6, ipNet.String())
			}
		}
		if len(info.IPv4) > 0 || len(info.IPv6) > 0 {
			ifaces = append(ifaces, info)
		}
	}

	// Firewall status — uses the firewall package (cross-platform: ufw on Linux, netsh on Windows).
	firewallEnabled := firewall.IsEnabled()
	var firewallRules []string
	// Try to read detailed rules (Linux only via ufw status verbose; best-effort).
	if out, err := exec.CommandContext(r.Context(), "ufw", "status", "verbose").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "ALLOW IN") || strings.Contains(line, "DENY IN") ||
				strings.Contains(line, "ALLOW FWD") {
				firewallRules = append(firewallRules, line)
			}
		}
	}

	cfg := api.appRef.DashboardConfig()
	tunnelURL, _ := cfg["tunnel_url"].(string)
	tunnelType, _ := cfg["tunnel_type"].(string)

	type serviceStatus struct {
		Configured bool   `json:"configured"`
		Provider   string `json:"provider,omitempty"`
	}
	provider, _ := cfg["provider"].(string)
	services := map[string]serviceStatus{
		"llm":    {Configured: cfg["api_key"] != "" || cfg["endpoint"] != "", Provider: provider},
		"tts":    {Configured: cfg["elevenlabs_api_key"] != "", Provider: "elevenlabs"},
		"stt":    {Configured: cfg["stt_api_key"] != "" || cfg["api_key"] != "", Provider: "groq/openai"},
		"search": {Configured: cfg["brave_api_key"] != "", Provider: "brave"},
		"email":  {Configured: cfg["gmail_user"] != "" || cfg["google_connected"] == true, Provider: "gmail/imap"},
		"phone":  {Configured: cfg["phone_configured"] == true, Provider: "twilio"},
	}

	api.writeJSON(w, map[string]any{
		"hostname":         hostname,
		"interfaces":       ifaces,
		"firewall_enabled": firewallEnabled,
		"firewall_rules":   firewallRules,
		"tunnel_url":       tunnelURL,
		"tunnel_type":      tunnelType,
		"services":         services,
	})
}

// GET /api/security/status — returns firewall and network state.
// Reads /etc/ufw/ufw.conf directly (no sudo needed) because the crayfish
// systemd service runs with NoNewPrivileges=true which blocks sudo.
func (api *DashboardAPI) handleSecurityStatus(w http.ResponseWriter, r *http.Request) {
	// Use the firewall package which handles Linux (ufw), Windows (netsh), macOS.
	firewallInstalled := firewall.IsAvailable()
	firewallEnabled := firewall.IsEnabled()
	firewallNote := ""
	if firewallInstalled && !firewallEnabled {
		firewallNote = "Firewall installed but not active"
	}

	// Check whether any active interface has an IPv6 address.
	ipv6Active := false
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() == nil && len(ipNet.IP) == 16 {
				ipv6Active = true
				break
			}
		}
		if ipv6Active {
			break
		}
	}

	api.writeJSON(w, map[string]any{
		"firewall_installed": firewallInstalled,
		"firewall_enabled":   firewallEnabled,
		"firewall_note":      firewallNote,
		"ipv6_active":        ipv6Active,
	})
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

	db := api.db.Reader()
	ctx := r.Context()

	var messageCount, sessionCount, memoryCount int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&messageCount); err != nil {
		api.logger.Warn("failed to count messages", "error", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&sessionCount); err != nil {
		api.logger.Warn("failed to count sessions", "error", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_fts").Scan(&memoryCount); err != nil {
		api.logger.Warn("failed to count memories", "error", err)
	}

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
		if restartNeeded {
			fn := api.restartFn
			go func() {
				time.Sleep(500 * time.Millisecond)
				fn()
			}()
		}
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

	db := api.db.Reader()
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

	db := api.db.Reader()
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

	db := api.db.Reader()
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

	if q != "" {
		rows2, err := db.QueryContext(r.Context(),
			`SELECT f.rowid, f.key, f.content, f.session_id, f.created_at, COALESCE(m.category, 'general')
			FROM memory_fts f LEFT JOIN memory_metadata m ON f.rowid = m.id
			WHERE memory_fts MATCH ? LIMIT 50`, q)
		if err != nil {
			api.writeError(w, "search failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		rows = rows2
	} else {
		rows2, err := db.QueryContext(r.Context(),
			`SELECT f.rowid, f.key, f.content, f.session_id, f.created_at, COALESCE(m.category, 'general')
			FROM memory_fts f LEFT JOIN memory_metadata m ON f.rowid = m.id
			ORDER BY f.rowid DESC LIMIT 50`)
		if err != nil {
			api.writeError(w, "query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		rows = rows2
	}
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
	if _, err := db.ExecContext(r.Context(), "DELETE FROM memory_metadata WHERE id = ?", id); err != nil {
		api.logger.Warn("failed to delete memory metadata", "id", id, "error", err)
	}

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

	db := api.db.Reader()
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

	db := api.db.Reader()
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

// GET /api/contacts — list all contacts
// POST /api/contacts — create a contact
func (api *DashboardAPI) handleContacts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		type contact struct {
			ID           int    `json:"id"`
			Name         string `json:"name"`
			Relationship string `json:"relationship"`
			Phone        string `json:"phone"`
			Email        string `json:"email"`
			Notes        string `json:"notes"`
			IsOwner      bool   `json:"is_owner"`
		}
		rows, err := api.db.Inner().QueryContext(r.Context(),
			`SELECT id, name, relationship, phone, email, notes, is_owner
			 FROM contacts ORDER BY is_owner DESC, name ASC`)
		if err != nil {
			api.writeError(w, "query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var contacts []contact
		for rows.Next() {
			var c contact
			var isOwner int
			if err := rows.Scan(&c.ID, &c.Name, &c.Relationship, &c.Phone, &c.Email, &c.Notes, &isOwner); err == nil {
				c.IsOwner = isOwner == 1
				contacts = append(contacts, c)
			}
		}
		if contacts == nil {
			contacts = []contact{}
		}
		api.writeJSON(w, contacts)

	case http.MethodPost:
		var body struct {
			Name         string `json:"name"`
			Relationship string `json:"relationship"`
			Phone        string `json:"phone"`
			Email        string `json:"email"`
			Notes        string `json:"notes"`
			IsOwner      bool   `json:"is_owner"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.writeError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			api.writeError(w, "name required", http.StatusBadRequest)
			return
		}
		isOwner := 0
		if body.IsOwner {
			isOwner = 1
		}
		res, err := api.db.Inner().ExecContext(r.Context(),
			`INSERT INTO contacts (name, relationship, phone, email, notes, is_owner) VALUES (?,?,?,?,?,?)`,
			body.Name, body.Relationship, body.Phone, body.Email, body.Notes, isOwner)
		if err != nil {
			api.writeError(w, "insert failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		api.writeJSON(w, map[string]any{"id": id, "status": "created"})

	default:
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT /api/contacts/{id} — update a contact
// DELETE /api/contacts/{id} — delete a contact
func (api *DashboardAPI) handleContactByID(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/contacts/")
	if idStr == "" {
		api.writeError(w, "id required", http.StatusBadRequest)
		return
	}
	// Validate ID is a positive integer — reject path traversal attempts.
	if _, err := strconv.ParseInt(idStr, 10, 64); err != nil {
		api.writeError(w, "invalid contact ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var body struct {
			Name         string `json:"name"`
			Relationship string `json:"relationship"`
			Phone        string `json:"phone"`
			Email        string `json:"email"`
			Notes        string `json:"notes"`
			IsOwner      bool   `json:"is_owner"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.writeError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			api.writeError(w, "name required", http.StatusBadRequest)
			return
		}
		isOwner := 0
		if body.IsOwner {
			isOwner = 1
		}
		_, err := api.db.Inner().ExecContext(r.Context(),
			`UPDATE contacts SET name=?, relationship=?, phone=?, email=?, notes=?, is_owner=?, updated_at=datetime('now') WHERE id=?`,
			body.Name, body.Relationship, body.Phone, body.Email, body.Notes, isOwner, idStr)
		if err != nil {
			api.writeError(w, "update failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		api.writeJSON(w, map[string]any{"status": "updated"})

	case http.MethodDelete:
		_, err := api.db.Inner().ExecContext(r.Context(), `DELETE FROM contacts WHERE id=?`, idStr)
		if err != nil {
			api.writeError(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		api.writeJSON(w, map[string]any{"status": "deleted"})

	default:
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// POST /api/contacts/sync/google — sync contacts from Google People API
func (api *DashboardAPI) handleContactsGoogleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	n, err := api.appRef.SyncGoogleContacts(r.Context())
	if err != nil {
		api.writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	api.writeJSON(w, map[string]any{"synced": n, "message": "Synced " + strconv.Itoa(n) + " contacts from Google"})
}

// POST /api/contacts/import/vcard — parse a vCard (.vcf) file and import contacts
func (api *DashboardAPI) handleContactsVCardImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10MB max
	if err != nil {
		api.writeError(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	contacts := parseVCard(string(body))
	imported := 0
	for _, c := range contacts {
		if c.Name == "" || (c.Phone == "" && c.Email == "") {
			continue
		}
		_, err := api.db.Inner().ExecContext(r.Context(),
			`INSERT OR IGNORE INTO contacts (name, phone, email, notes)
			 VALUES (?, ?, ?, ?)`,
			c.Name, c.Phone, c.Email, c.Notes)
		if err == nil {
			imported++
		}
	}
	api.writeJSON(w, map[string]any{
		"imported": imported,
		"total":    len(contacts),
		"message":  strconv.Itoa(imported) + " contacts imported (" + strconv.Itoa(len(contacts)-imported) + " skipped — already exist or missing phone/email)",
	})
}

type vcardContact struct {
	Name  string
	Phone string
	Email string
	Notes string
}

// parseVCard parses a vCard (.vcf) file and returns contacts.
// Handles both vCard 2.1 and 3.0/4.0 formats.
func parseVCard(data string) []vcardContact {
	var contacts []vcardContact
	var current *vcardContact

	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")

		upper := strings.ToUpper(line)
		switch {
		case upper == "BEGIN:VCARD":
			current = &vcardContact{}

		case upper == "END:VCARD":
			if current != nil {
				contacts = append(contacts, *current)
				current = nil
			}

		case current == nil:
			continue

		case strings.HasPrefix(upper, "FN:"):
			current.Name = strings.TrimSpace(line[3:])

		case strings.HasPrefix(upper, "N:") && current.Name == "":
			// Fallback: parse N field (Last;First;Middle;Prefix;Suffix)
			parts := strings.SplitN(line[2:], ";", 3)
			if len(parts) >= 2 {
				last := strings.TrimSpace(parts[0])
				first := strings.TrimSpace(parts[1])
				if first != "" && last != "" {
					current.Name = first + " " + last
				} else if first != "" {
					current.Name = first
				} else {
					current.Name = last
				}
			}

		case strings.HasPrefix(upper, "TEL") && current.Phone == "":
			// TEL;TYPE=CELL:+12025551234 or TEL:+12025551234
			idx := strings.LastIndex(line, ":")
			if idx >= 0 {
				phone := strings.TrimSpace(line[idx+1:])
				current.Phone = normalizeVCardPhone(phone)
			}

		case strings.HasPrefix(upper, "EMAIL") && current.Email == "":
			idx := strings.LastIndex(line, ":")
			if idx >= 0 {
				current.Email = strings.TrimSpace(line[idx+1:])
			}

		case strings.HasPrefix(upper, "NOTE:"):
			current.Notes = strings.TrimSpace(line[5:])
		}
	}
	return contacts
}

func normalizeVCardPhone(s string) string {
	clean := ""
	for _, c := range s {
		if (c >= '0' && c <= '9') || c == '+' {
			clean += string(c)
		}
	}
	if len(clean) == 10 {
		return "+1" + clean
	}
	if len(clean) == 11 && len(clean) > 0 && clean[0] == '1' {
		return "+" + clean
	}
	if len(clean) > 0 && clean[0] != '+' {
		return "+" + clean
	}
	return clean
}
