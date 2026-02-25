package gateway

import (
	"html/template"
	"net/http"
)

// DashboardUI serves the admin dashboard web interface.
type DashboardUI struct {
	version string
	apiKey  string
	tmpl    *template.Template
}

// NewDashboardUI creates the dashboard web UI handler.
func NewDashboardUI(version, apiKey string) *DashboardUI {
	tmpl := template.Must(template.New("dashboard").Parse(dashboardPageHTML))
	return &DashboardUI{
		version: version,
		apiKey:  apiKey,
		tmpl:    tmpl,
	}
}

// RegisterRoutes adds the dashboard UI route.
func (ui *DashboardUI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", ui.handlePage)
}

func (ui *DashboardUI) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	ui.tmpl.Execute(w, map[string]interface{}{
		"Version": ui.version,
		"APIKey":  ui.apiKey,
	})
}

const dashboardPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Crayfish Dashboard</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: linear-gradient(135deg, #0c1222 0%, #1a1a2e 50%, #16213e 100%);
    color: #e2e8f0;
    min-height: 100vh;
    display: flex;
    flex-direction: column;
  }
  .container { max-width: 960px; margin: 0 auto; padding: 1.5rem; flex: 1; width: 100%; }

  /* Header */
  .dash-header {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 1.5rem;
    padding-bottom: 1rem;
    border-bottom: 1px solid rgba(71, 85, 105, 0.5);
  }
  .dash-header svg { width: 36px; height: 36px; flex-shrink: 0; }
  .dash-header h1 {
    font-size: 1.4rem;
    background: linear-gradient(135deg, #ff6b35 0%, #f7931e 100%);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
  }

  /* Tabs */
  .tab-bar {
    display: flex;
    gap: 0.375rem;
    margin-bottom: 1.5rem;
    overflow-x: auto;
    padding-bottom: 0.25rem;
  }
  .tab-btn {
    padding: 0.5rem 1rem;
    border: 1px solid rgba(71, 85, 105, 0.5);
    border-radius: 8px;
    background: transparent;
    color: #94a3b8;
    font-size: 0.8125rem;
    font-weight: 500;
    cursor: pointer;
    white-space: nowrap;
    transition: all 0.2s;
  }
  .tab-btn:hover { border-color: #f97316; color: #f97316; }
  .tab-btn.active {
    background: linear-gradient(135deg, #f97316 0%, #fb923c 100%);
    color: #0f172a;
    border-color: transparent;
    font-weight: 600;
  }
  .tab-content { display: none; }
  .tab-content.active { display: block; }

  /* Cards */
  .card {
    background: rgba(30, 41, 59, 0.8);
    backdrop-filter: blur(10px);
    border-radius: 12px;
    padding: 1.25rem;
    border: 1px solid rgba(71, 85, 105, 0.5);
    margin-bottom: 1rem;
    transition: border-color 0.2s;
  }
  .card:hover { border-color: rgba(249, 115, 22, 0.3); }
  .card h3 { color: #f8fafc; font-size: 1rem; margin-bottom: 0.75rem; }

  /* Stats grid */
  .stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 1rem; margin-bottom: 1.25rem; }
  .stat-card {
    background: rgba(30, 41, 59, 0.8);
    border-radius: 12px;
    padding: 1.25rem;
    border: 1px solid rgba(71, 85, 105, 0.5);
    text-align: center;
    cursor: pointer;
    transition: border-color 0.2s, transform 0.15s;
  }
  .stat-card:hover { border-color: rgba(249, 115, 22, 0.5); transform: translateY(-2px); }
  .stat-value { font-size: 2rem; font-weight: 700; color: #f8fafc; }
  .stat-label { font-size: 0.75rem; color: #94a3b8; text-transform: uppercase; letter-spacing: 0.5px; margin-top: 0.25rem; }

  /* Voice install progress */
  .voice-status {
    background: rgba(249, 115, 22, 0.1);
    border: 1px solid rgba(249, 115, 22, 0.3);
    border-radius: 10px;
    padding: 0.875rem 1.25rem;
    margin-bottom: 1.25rem;
  }
  .voice-status-text { font-size: 0.8125rem; color: #fb923c; margin-bottom: 0.5rem; }
  .voice-progress-bar { height: 6px; background: rgba(71, 85, 105, 0.5); border-radius: 3px; overflow: hidden; }
  .voice-progress-fill { height: 100%; background: linear-gradient(90deg, #f97316, #fb923c); border-radius: 3px; transition: width 0.5s ease; }

  /* Status badge */
  .status-bar { display: flex; align-items: center; gap: 1rem; flex-wrap: wrap; margin-bottom: 1.25rem; }
  .status-badge {
    display: inline-flex; align-items: center; gap: 0.5rem;
    padding: 0.375rem 0.875rem; border-radius: 20px;
    font-size: 0.8125rem; font-weight: 500;
    background: rgba(16, 185, 129, 0.15); color: #6ee7b7; border: 1px solid rgba(16, 185, 129, 0.3);
  }
  .status-dot { width: 8px; height: 8px; border-radius: 50%; background: #6ee7b7; animation: pulse 2s infinite; }
  @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.5; } }
  .status-info { font-size: 0.8125rem; color: #94a3b8; }

  /* Adapter list */
  .adapter-list { display: flex; gap: 0.5rem; flex-wrap: wrap; }
  .adapter-chip {
    display: inline-flex; align-items: center; gap: 0.375rem;
    padding: 0.25rem 0.625rem; border-radius: 6px;
    font-size: 0.75rem; background: rgba(15, 23, 42, 0.8);
    border: 1px solid rgba(71, 85, 105, 0.5); color: #e2e8f0;
  }
  .adapter-dot { width: 6px; height: 6px; border-radius: 50%; background: #6ee7b7; }

  /* Buttons */
  .btn {
    padding: 0.625rem 1.25rem; border: none; border-radius: 8px;
    font-size: 0.875rem; font-weight: 600; cursor: pointer; transition: all 0.2s;
  }
  .btn-primary {
    background: linear-gradient(135deg, #f97316 0%, #fb923c 100%);
    color: #0f172a;
  }
  .btn-primary:hover { transform: translateY(-1px); }
  .btn-secondary { background: transparent; border: 1px solid #475569; color: #94a3b8; }
  .btn-secondary:hover { border-color: #f97316; color: #f97316; }
  .btn-danger { background: rgba(220, 38, 38, 0.2); border: 1px solid #dc2626; color: #fca5a5; }
  .btn-danger:hover { background: rgba(220, 38, 38, 0.4); }
  .btn-sm { padding: 0.375rem 0.75rem; font-size: 0.75rem; }

  /* Forms */
  .form-section { margin-bottom: 1.5rem; }
  .form-section-header {
    display: flex; align-items: center; gap: 0.5rem;
    font-size: 0.9375rem; font-weight: 600; color: #f8fafc;
    margin-bottom: 1rem; padding-bottom: 0.5rem;
    border-bottom: 1px solid rgba(71, 85, 105, 0.3);
  }
  .dot-hot { width: 8px; height: 8px; border-radius: 50%; background: #6ee7b7; }
  .dot-restart { width: 8px; height: 8px; border-radius: 50%; background: #fcd34d; }
  .form-group { margin-bottom: 0.875rem; }
  .form-group label {
    display: block; font-size: 0.75rem; font-weight: 500;
    color: #94a3b8; margin-bottom: 0.25rem;
    text-transform: uppercase; letter-spacing: 0.5px;
  }
  .form-group input, .form-group select, .form-group textarea {
    width: 100%; padding: 0.625rem 0.75rem; border-radius: 8px;
    border: 1px solid #475569; background: rgba(15, 23, 42, 0.8);
    color: #f8fafc; font-size: 0.875rem; font-family: inherit;
  }
  .form-group textarea { min-height: 80px; resize: vertical; font-family: ui-monospace, monospace; font-size: 0.8125rem; }
  .form-group input:focus, .form-group select:focus, .form-group textarea:focus { outline: none; border-color: #f97316; }
  .form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 0.875rem; }
  .pass-wrap { position: relative; }
  .pass-wrap input { padding-right: 2.5rem; }
  .pass-toggle {
    position: absolute; right: 0.5rem; top: 50%; transform: translateY(-50%);
    background: none; border: none; color: #64748b; cursor: pointer; font-size: 1rem; padding: 0.25rem;
  }
  .pass-toggle:hover { color: #f97316; }
  .check-group { display: flex; align-items: center; gap: 0.5rem; }
  .check-group input[type="checkbox"] { width: 16px; height: 16px; accent-color: #f97316; }
  .check-group label { text-transform: none; font-size: 0.875rem; color: #e2e8f0; margin-bottom: 0; }

  /* Table */
  .data-table { width: 100%; border-collapse: collapse; }
  .data-table th {
    text-align: left; padding: 0.625rem 0.75rem; font-size: 0.6875rem;
    text-transform: uppercase; letter-spacing: 0.5px; color: #64748b;
    border-bottom: 1px solid rgba(71, 85, 105, 0.5);
  }
  .data-table td {
    padding: 0.625rem 0.75rem; font-size: 0.8125rem; color: #e2e8f0;
    border-bottom: 1px solid rgba(71, 85, 105, 0.2);
  }
  .data-table tr { cursor: pointer; transition: background 0.15s; }
  .data-table tr:hover { background: rgba(249, 115, 22, 0.05); }

  /* Badges */
  .badge {
    display: inline-block; padding: 0.125rem 0.5rem; border-radius: 4px;
    font-size: 0.6875rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.3px;
  }
  .badge-blue { background: rgba(59, 130, 246, 0.2); color: #60a5fa; }
  .badge-green { background: rgba(16, 185, 129, 0.2); color: #6ee7b7; }
  .badge-orange { background: rgba(249, 115, 22, 0.2); color: #fb923c; }
  .badge-purple { background: rgba(168, 85, 247, 0.2); color: #c4b5fd; }
  .badge-gray { background: rgba(100, 116, 139, 0.2); color: #94a3b8; }
  .badge-yellow { background: rgba(234, 179, 8, 0.2); color: #fcd34d; }
  .badge-red { background: rgba(220, 38, 38, 0.2); color: #fca5a5; }

  /* Chat bubbles */
  .chat-container { padding: 0.75rem; max-height: 400px; overflow-y: auto; }
  .chat-msg { max-width: 80%; margin-bottom: 0.5rem; padding: 0.625rem 0.875rem; border-radius: 12px; font-size: 0.8125rem; line-height: 1.5; word-wrap: break-word; white-space: pre-wrap; }
  .chat-user { margin-left: auto; background: rgba(59, 130, 246, 0.2); border: 1px solid rgba(59, 130, 246, 0.3); }
  .chat-assistant { margin-right: auto; background: rgba(30, 41, 59, 0.9); border: 1px solid rgba(71, 85, 105, 0.4); }
  .chat-time { font-size: 0.625rem; color: #64748b; margin-top: 0.125rem; }

  /* Search */
  .search-bar {
    display: flex; align-items: center; gap: 0.5rem; margin-bottom: 1rem;
    padding: 0.625rem 0.875rem; border-radius: 8px;
    border: 1px solid #475569; background: rgba(15, 23, 42, 0.8);
  }
  .search-bar input { flex: 1; background: none; border: none; color: #f8fafc; font-size: 0.875rem; outline: none; }
  .search-icon { color: #64748b; }

  /* Filter bar */
  .filter-bar { display: flex; gap: 0.375rem; margin-bottom: 1rem; flex-wrap: wrap; align-items: center; }

  /* Memory card */
  .mem-card { position: relative; }
  .mem-card .mem-key { font-weight: 600; color: #f8fafc; margin-bottom: 0.25rem; }
  .mem-card .mem-content { color: #cbd5e1; font-size: 0.8125rem; margin-bottom: 0.5rem; line-height: 1.5; }
  .mem-card .mem-meta { display: flex; gap: 0.75rem; font-size: 0.6875rem; color: #64748b; }
  .mem-del { position: absolute; top: 0.75rem; right: 0.75rem; background: none; border: none; color: #64748b; cursor: pointer; font-size: 1rem; }
  .mem-del:hover { color: #fca5a5; }

  /* Events */
  .event-row { display: flex; align-items: center; gap: 0.75rem; padding: 0.5rem 0; border-bottom: 1px solid rgba(71, 85, 105, 0.2); font-size: 0.8125rem; }
  .event-type { min-width: 130px; }
  .event-chan { min-width: 70px; color: #94a3b8; }
  .event-sess { min-width: 80px; color: #64748b; font-family: ui-monospace, monospace; font-size: 0.75rem; }
  .event-time { color: #64748b; font-size: 0.75rem; margin-left: auto; }

  /* Toast */
  .toast {
    position: fixed; top: 1rem; right: 1rem; z-index: 1000;
    padding: 0.75rem 1.25rem; border-radius: 8px;
    font-size: 0.875rem; font-weight: 500;
    transform: translateX(120%); transition: transform 0.3s;
  }
  .toast.show { transform: translateX(0); }
  .toast-success { background: rgba(6, 78, 59, 0.95); color: #6ee7b7; border: 1px solid rgba(16, 185, 129, 0.3); }
  .toast-error { background: rgba(127, 29, 29, 0.95); color: #fca5a5; border: 1px solid rgba(220, 38, 38, 0.3); }

  /* Modal */
  .modal-overlay { display: none; position: fixed; inset: 0; background: rgba(0,0,0,0.7); align-items: center; justify-content: center; z-index: 100; padding: 1rem; }
  .modal-overlay.show { display: flex; }
  .modal { background: #1e293b; border-radius: 16px; width: 100%; max-width: 600px; max-height: 90vh; overflow-y: auto; border: 1px solid rgba(71,85,105,0.5); }
  .modal-header { padding: 1.25rem; border-bottom: 1px solid rgba(71,85,105,0.3); display: flex; justify-content: space-between; align-items: center; }
  .modal-header h2 { font-size: 1.125rem; color: #f8fafc; }
  .modal-close { background: none; border: none; color: #64748b; font-size: 1.5rem; cursor: pointer; }
  .modal-close:hover { color: #f97316; }
  .modal-body { padding: 1.25rem; }
  .modal-footer { padding: 1.25rem; border-top: 1px solid rgba(71,85,105,0.3); display: flex; justify-content: flex-end; gap: 0.75rem; }

  /* Toggle switch */
  .toggle { position: relative; display: inline-block; width: 36px; height: 20px; flex-shrink: 0; }
  .toggle input { opacity: 0; width: 0; height: 0; }
  .toggle .slider { position: absolute; cursor: pointer; inset: 0; background: #475569; border-radius: 20px; transition: 0.2s; }
  .toggle .slider:before { content: ""; position: absolute; height: 14px; width: 14px; left: 3px; bottom: 3px; background: #e2e8f0; border-radius: 50%; transition: 0.2s; }
  .toggle input:checked + .slider { background: #f97316; }
  .toggle input:checked + .slider:before { transform: translateX(16px); }

  /* Loading */
  .loading { text-align: center; padding: 2rem; color: #64748b; }
  .empty-state { text-align: center; padding: 2.5rem; color: #64748b; }

  /* Footer */
  .dash-footer {
    background: rgba(15, 23, 42, 0.6);
    border-top: 1px solid rgba(71, 85, 105, 0.3);
    padding: 1.25rem 1.5rem;
    margin-top: auto;
  }
  .footer-inner { max-width: 960px; margin: 0 auto; }
  .footer-top { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 0.75rem; margin-bottom: 0.75rem; }
  .footer-brand { display: flex; align-items: center; gap: 0.5rem; font-size: 0.8125rem; color: #94a3b8; }
  .footer-brand svg { width: 20px; height: 20px; }
  .footer-tagline { font-size: 0.8125rem; color: #64748b; }
  .footer-uptime { font-size: 0.8125rem; color: #94a3b8; font-family: ui-monospace, monospace; }
  .footer-bottom { font-size: 0.6875rem; color: #475569; text-align: center; padding-top: 0.75rem; border-top: 1px solid rgba(71, 85, 105, 0.15); }

  @media (max-width: 640px) {
    .form-row { grid-template-columns: 1fr; }
    .stats-grid { grid-template-columns: repeat(2, 1fr); }
    .footer-top { flex-direction: column; text-align: center; }
  }
</style>
</head>
<body>
<div class="container">
  <div class="dash-header">
    <svg viewBox="0 0 100 100" xmlns="http://www.w3.org/2000/svg"><defs><linearGradient id="cg" x1="0%" y1="0%" x2="100%" y2="100%"><stop offset="0%" style="stop-color:#ff6b35"/><stop offset="100%" style="stop-color:#f7931e"/></linearGradient></defs><ellipse cx="50" cy="52" rx="18" ry="12" fill="url(#cg)"/><path d="M32 52 Q28 42 20 35 Q18 33 20 31 L26 28 Q28 27 29 29 L35 40 Q37 44 35 48Z" fill="url(#cg)"/><path d="M68 52 Q72 42 80 35 Q82 33 80 31 L74 28 Q72 27 71 29 L65 40 Q63 44 65 48Z" fill="url(#cg)"/><ellipse cx="50" cy="66" rx="14" ry="6" fill="url(#cg)"/><ellipse cx="50" cy="76" rx="10" ry="5" fill="url(#cg)"/><ellipse cx="50" cy="84" rx="7" ry="4" fill="url(#cg)"/><circle cx="44" cy="48" r="2.5" fill="#1a1a2e"/><circle cx="56" cy="48" r="2.5" fill="#1a1a2e"/><line x1="38" y1="30" x2="34" y2="20" stroke="url(#cg)" stroke-width="2" stroke-linecap="round"/><line x1="62" y1="30" x2="66" y2="20" stroke="url(#cg)" stroke-width="2" stroke-linecap="round"/></svg>
    <h1>Crayfish Dashboard</h1>
  </div>

  <div class="tab-bar">
    <button class="tab-btn active" onclick="switchTab('overview')">Overview</button>
    <button class="tab-btn" onclick="switchTab('settings')">Settings</button>
    <button class="tab-btn" onclick="switchTab('skills')">Skills</button>
    <button class="tab-btn" onclick="switchTab('sessions')">Sessions</button>
    <button class="tab-btn" onclick="switchTab('memory')">Memory</button>
    <button class="tab-btn" onclick="switchTab('events')">Events</button>
  </div>

  <!-- Overview Tab -->
  <div class="tab-content active" id="tab-overview">
    <div class="status-bar">
      <span class="status-badge"><span class="status-dot"></span> Running</span>
      <span class="status-info" id="ov-version">{{.Version}}</span>
      <span class="status-info" id="ov-uptime"></span>
    </div>
    <div class="stats-grid" id="ov-stats">
      <div class="stat-card" onclick="switchTab('events')"><div class="stat-value" id="ov-messages">-</div><div class="stat-label">Messages</div></div>
      <div class="stat-card" onclick="switchTab('sessions')"><div class="stat-value" id="ov-sessions">-</div><div class="stat-label">Sessions</div></div>
      <div class="stat-card" onclick="switchTab('memory')"><div class="stat-value" id="ov-memories">-</div><div class="stat-label">Memories</div></div>
      <div class="stat-card" onclick="switchTab('events')"><div class="stat-value" id="ov-events">-</div><div class="stat-label">Events</div></div>
    </div>
    <div class="voice-status" id="ov-voice" style="display:none">
      <div class="voice-status-text" id="ov-voice-msg"></div>
      <div class="voice-progress-bar"><div class="voice-progress-fill" id="ov-voice-bar"></div></div>
    </div>
    <div class="card">
      <h3>Active Channels</h3>
      <div class="adapter-list" id="ov-adapters"><span class="loading">Loading...</span></div>
    </div>
  </div>

  <!-- Settings Tab -->
  <div class="tab-content" id="tab-settings">
    <div class="card">
      <div class="form-section">
        <div class="form-section-header"><span class="dot-hot"></span> Identity</div>
        <div class="form-row">
          <div class="form-group"><label>Name</label><input type="text" id="cfg-name"></div>
          <div class="form-group"><label>Personality</label>
            <select id="cfg-personality"><option value="friendly">Friendly</option><option value="professional">Professional</option><option value="casual">Casual</option><option value="minimal">Minimal</option></select>
          </div>
        </div>
        <div class="form-group"><label>System Prompt (optional)</label><textarea id="cfg-system_prompt" placeholder="Leave empty for default..."></textarea></div>
      </div>
      <div class="form-section">
        <div class="form-section-header"><span class="dot-restart"></span> AI Provider</div>
        <div class="form-row">
          <div class="form-group"><label>Provider</label><input type="text" id="cfg-provider" placeholder="anthropic"></div>
          <div class="form-group"><label>Model</label><input type="text" id="cfg-model" placeholder="claude-sonnet-4-20250514"></div>
        </div>
        <div class="form-group"><label>API Key</label><div class="pass-wrap"><input type="password" id="cfg-api_key"><button class="pass-toggle" onclick="togglePass('cfg-api_key')">&#128065;</button></div></div>
        <div class="form-row">
          <div class="form-group"><label>Endpoint (optional)</label><input type="text" id="cfg-endpoint" placeholder="Custom endpoint"></div>
          <div class="form-group"><label>Max Tokens</label><input type="number" id="cfg-max_tokens" placeholder="1024"></div>
        </div>
      </div>
      <div class="form-section">
        <div class="form-section-header"><span class="dot-restart"></span> Integrations</div>
        <div class="form-group"><label>Telegram Token</label><div class="pass-wrap"><input type="password" id="cfg-telegram_token"><button class="pass-toggle" onclick="togglePass('cfg-telegram_token')">&#128065;</button></div></div>
        <div class="form-row">
          <div class="form-group"><label>Gmail User</label><input type="text" id="cfg-gmail_user" placeholder="you@gmail.com"></div>
          <div class="form-group"><label>Gmail App Password</label><div class="pass-wrap"><input type="password" id="cfg-gmail_app_password"><button class="pass-toggle" onclick="togglePass('cfg-gmail_app_password')">&#128065;</button></div></div>
        </div>
        <div class="form-group"><label>Brave API Key</label><div class="pass-wrap"><input type="password" id="cfg-brave_api_key"><button class="pass-toggle" onclick="togglePass('cfg-brave_api_key')">&#128065;</button></div></div>
      </div>
      <div class="form-section">
        <div class="form-section-header"><span class="dot-restart"></span> Network</div>
        <div class="form-group"><label>Listen Address</label><input type="text" id="cfg-listen_addr"></div>
      </div>
      <div class="form-section">
        <div class="form-section-header"><span class="dot-hot"></span> System</div>
        <div class="form-row">
          <div class="form-group"><label>Session Resume (min)</label><input type="number" id="cfg-session_resume_minutes"></div>
          <div class="form-group"><label>Snapshots per Session</label><input type="number" id="cfg-snapshots_per_session"></div>
        </div>
        <div class="form-row">
          <div class="form-group"><label>Update Channel</label>
            <select id="cfg-update_channel"><option value="stable">Stable</option><option value="beta">Beta</option></select>
          </div>
          <div></div>
        </div>
        <div style="display:flex;gap:1.5rem;margin-top:0.5rem;">
          <div class="check-group"><input type="checkbox" id="cfg-continuity_enabled"><label for="cfg-continuity_enabled">Session Continuity</label></div>
          <div class="check-group"><input type="checkbox" id="cfg-auto_update"><label for="cfg-auto_update">Auto Update</label></div>
        </div>
      </div>
      <div style="display:flex;justify-content:flex-end;padding-top:0.75rem;border-top:1px solid rgba(71,85,105,0.3);">
        <button class="btn btn-primary" onclick="saveSettings()">Save Settings</button>
      </div>
    </div>
  </div>

  <!-- Skills Tab -->
  <div class="tab-content" id="tab-skills">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:1rem;">
      <div style="display:flex;gap:0.375rem;">
        <button class="tab-btn btn-sm active" id="sk-tab-my" onclick="switchSkillTab('my')">My Skills</button>
        <button class="tab-btn btn-sm" id="sk-tab-hub" onclick="switchSkillTab('hub')">Browse Hub</button>
      </div>
      <button class="btn btn-primary btn-sm" onclick="openSkillModal()">+ New Skill</button>
    </div>
    <div id="skills-my-list"><div class="loading">Loading skills...</div></div>
    <div id="skills-hub-list" style="display:none;"><div class="loading">Loading hub...</div></div>
  </div>

  <!-- Sessions Tab -->
  <div class="tab-content" id="tab-sessions">
    <div class="card" style="overflow-x:auto;">
      <table class="data-table" id="sessions-table">
        <thead><tr><th>ID</th><th>Channel</th><th>Trust</th><th>Created</th><th>Last Active</th></tr></thead>
        <tbody id="sessions-body"><tr><td colspan="5" class="loading">Loading...</td></tr></tbody>
      </table>
    </div>
    <div id="session-messages" style="display:none;">
      <div class="card">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:0.75rem;">
          <h3 id="session-msg-title">Messages</h3>
          <button class="btn btn-secondary btn-sm" onclick="closeMessages()">Close</button>
        </div>
        <div class="chat-container" id="chat-area"></div>
      </div>
    </div>
  </div>

  <!-- Memory Tab -->
  <div class="tab-content" id="tab-memory">
    <div class="search-bar">
      <span class="search-icon">&#128269;</span>
      <input type="text" id="mem-search" placeholder="Search memories..." oninput="debounceMemSearch()">
    </div>
    <div id="memory-list"><div class="loading">Loading...</div></div>
  </div>

  <!-- Events Tab -->
  <div class="tab-content" id="tab-events">
    <div class="filter-bar">
      <button class="tab-btn btn-sm active" onclick="filterEvents('')">All</button>
      <button class="tab-btn btn-sm" onclick="filterEvents('message')">Messages</button>
      <button class="tab-btn btn-sm" onclick="filterEvents('tool')">Tools</button>
      <button class="tab-btn btn-sm" onclick="filterEvents('system')">System</button>
      <div style="margin-left:auto;">
        <button class="btn btn-secondary btn-sm" id="ev-auto-btn" onclick="toggleAutoRefresh()">Auto-refresh: Off</button>
      </div>
    </div>
    <div id="events-list"><div class="loading">Loading...</div></div>
  </div>
</div>

<!-- Skill Modal -->
<div class="modal-overlay" id="skill-modal">
  <div class="modal">
    <div class="modal-header"><h2 id="skill-modal-title">New Skill</h2><button class="modal-close" onclick="closeSkillModal()">&times;</button></div>
    <div class="modal-body">
      <div class="form-row"><div class="form-group"><label>Name</label><input type="text" id="sk-name" placeholder="my-skill"></div><div class="form-group"><label>Type</label><select id="sk-type"><option value="workflow">Workflow</option><option value="prompt">Prompt</option><option value="reactive">Reactive</option></select></div></div>
      <div class="form-group"><label>Description</label><input type="text" id="sk-desc" placeholder="What this skill does"></div>
      <div class="form-row"><div class="form-group"><label>Command</label><input type="text" id="sk-command" placeholder="/briefing"></div><div class="form-group"><label>Schedule (cron)</label><input type="text" id="sk-schedule" placeholder="0 7 * * *"></div></div>
      <div class="form-group"><label>Event Trigger</label><input type="text" id="sk-event" placeholder="email.new"></div>
      <div class="form-group"><label>Prompt</label><textarea id="sk-prompt" placeholder="Prompt template..."></textarea></div>
      <div class="form-group"><label>Steps (JSON)</label><textarea id="sk-steps" placeholder='[{"tool":"...","params":{}}]'></textarea></div>
    </div>
    <div class="modal-footer"><button class="btn btn-secondary" onclick="closeSkillModal()">Cancel</button><button class="btn btn-primary" onclick="saveSkill()">Save</button></div>
  </div>
</div>

<!-- Toast -->
<div class="toast" id="toast"></div>

<footer class="dash-footer">
  <div class="footer-inner">
    <div class="footer-top">
      <div class="footer-brand">
        <svg viewBox="0 0 100 100" xmlns="http://www.w3.org/2000/svg"><defs><linearGradient id="fg" x1="0%" y1="0%" x2="100%" y2="100%"><stop offset="0%" style="stop-color:#ff6b35"/><stop offset="100%" style="stop-color:#f7931e"/></linearGradient></defs><ellipse cx="50" cy="52" rx="18" ry="12" fill="url(#fg)"/><path d="M32 52 Q28 42 20 35 Q18 33 20 31 L26 28 Q28 27 29 29 L35 40 Q37 44 35 48Z" fill="url(#fg)"/><path d="M68 52 Q72 42 80 35 Q82 33 80 31 L74 28 Q72 27 71 29 L65 40 Q63 44 65 48Z" fill="url(#fg)"/><ellipse cx="50" cy="66" rx="14" ry="6" fill="url(#fg)"/><ellipse cx="50" cy="76" rx="10" ry="5" fill="url(#fg)"/><ellipse cx="50" cy="84" rx="7" ry="4" fill="url(#fg)"/><circle cx="44" cy="48" r="2.5" fill="#1a1a2e"/><circle cx="56" cy="48" r="2.5" fill="#1a1a2e"/></svg>
        Crayfish {{.Version}}
      </div>
      <div class="footer-tagline">Accessible AI for everyone</div>
      <div class="footer-uptime" id="footer-uptime"></div>
    </div>
    <div class="footer-bottom">Built with care for a world where AI belongs to everyone, not just the privileged few.</div>
  </div>
</footer>

<script>
const S = {tab:'overview', eventFilter:'', autoRefresh:false, refreshTimer:null, uptimeSec:0, memTimer:null, voiceTimer:null};

/* === Tabs === */
function switchTab(t) {
  S.tab = t;
  document.querySelectorAll('.tab-bar .tab-btn').forEach(b => {
    b.classList.toggle('active', b.textContent.toLowerCase() === t);
  });
  document.querySelectorAll('.tab-content').forEach(c => c.classList.toggle('active', c.id === 'tab-'+t));
  loadTab(t);
}

async function loadTab(t) {
  try {
    switch(t) {
      case 'overview': await loadOverview(); break;
      case 'settings': await loadSettings(); break;
      case 'skills': await loadSkills(); break;
      case 'sessions': await loadSessions(); break;
      case 'memory': await loadMemory(''); break;
      case 'events': await loadEvents(); break;
    }
  } catch(e) { showToast('Failed to load: '+e.message, 'error'); }
}

/* === Overview === */
async function loadOverview() {
  const d = await fetchJSON('/api/dashboard/overview');
  setText('ov-messages', fmtNum(d.messages));
  setText('ov-sessions', fmtNum(d.sessions));
  setText('ov-memories', fmtNum(d.memories));
  setText('ov-events', fmtNum(d.events));
  S.uptimeSec = d.uptime_seconds || 0;
  setText('ov-uptime', 'Uptime: ' + fmtUptime(S.uptimeSec));
  if (d.voice && d.voice.status !== 'complete' && d.voice.status !== 'not_started' && d.voice.status !== 'not_supported') {
    document.getElementById('ov-voice').style.display = '';
    setText('ov-voice-msg', d.voice.message);
    document.getElementById('ov-voice-bar').style.width = (d.voice.progress * 100) + '%';
    // Poll while voice install is in progress.
    if (!S.voiceTimer) {
      S.voiceTimer = setInterval(async () => {
        if (S.tab === 'overview') await loadOverview();
      }, 3000);
    }
  } else {
    document.getElementById('ov-voice').style.display = 'none';
    if (S.voiceTimer) { clearInterval(S.voiceTimer); S.voiceTimer = null; }
  }
  const al = document.getElementById('ov-adapters');
  if (d.adapters && d.adapters.length) {
    al.innerHTML = d.adapters.map(a => '<span class="adapter-chip"><span class="adapter-dot"></span>'+esc(a)+'</span>').join('');
  } else { al.innerHTML = '<span style="color:#64748b">No adapters active</span>'; }
}

/* === Settings === */
async function loadSettings() {
  const c = await fetchJSON('/api/dashboard/config');
  const fields = ['name','personality','system_prompt','provider','api_key','endpoint','model','max_tokens',
    'telegram_token','gmail_user','gmail_app_password','brave_api_key','listen_addr',
    'session_resume_minutes','snapshots_per_session','update_channel'];
  fields.forEach(f => {
    const el = document.getElementById('cfg-'+f);
    if (!el) return;
    if (el.type === 'checkbox') return;
    el.value = c[f] != null ? c[f] : '';
  });
  document.getElementById('cfg-continuity_enabled').checked = !!c.continuity_enabled;
  document.getElementById('cfg-auto_update').checked = !!c.auto_update;
}

async function saveSettings() {
  const u = {};
  const text = ['name','personality','system_prompt','provider','api_key','endpoint','model',
    'telegram_token','gmail_user','gmail_app_password','brave_api_key','listen_addr','update_channel'];
  text.forEach(f => { const v = document.getElementById('cfg-'+f).value; u[f] = v; });
  const nums = ['max_tokens','session_resume_minutes','snapshots_per_session'];
  nums.forEach(f => { const v = parseInt(document.getElementById('cfg-'+f).value); if (!isNaN(v)) u[f] = v; });
  u.continuity_enabled = document.getElementById('cfg-continuity_enabled').checked;
  u.auto_update = document.getElementById('cfg-auto_update').checked;
  const r = await fetchJSON('/api/dashboard/config', {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify(u)});
  if (r.restart_needed) showToast('Settings saved \u2014 restart needed for some changes','success');
  else showToast('Settings saved','success');
}

function togglePass(id) {
  const el = document.getElementById(id);
  el.type = el.type === 'password' ? 'text' : 'password';
}

/* === Skills === */
let skillTab = 'my';
function switchSkillTab(t) {
  skillTab = t;
  document.getElementById('sk-tab-my').classList.toggle('active', t==='my');
  document.getElementById('sk-tab-hub').classList.toggle('active', t==='hub');
  document.getElementById('skills-my-list').style.display = t==='my'?'':'none';
  document.getElementById('skills-hub-list').style.display = t==='hub'?'':'none';
  if (t==='hub') loadHub();
  else loadSkills();
}
async function loadSkills() {
  const d = await fetchJSON('/api/skills');
  const list = d.skills || [];
  const el = document.getElementById('skills-my-list');
  if (!list.length) { el.innerHTML = '<div class="empty-state"><p style="font-size:1rem;color:#94a3b8;margin-bottom:0.5rem;">Skills teach your Crayfish new tricks automatically</p><p style="font-size:0.8125rem;">Create a skill or <a href="#" onclick="switchSkillTab(\'hub\');return false;" style="color:#f97316;">browse the Skill Hub</a> to get started.</p></div>'; return; }
  el.innerHTML = list.map(s => {
    const tc = s.type==='workflow'?'badge-blue':s.type==='prompt'?'badge-green':'badge-purple';
    const typeLabel = s.type==='workflow'?'Multi-step workflow':s.type==='prompt'?'Context enhancer':'Auto-trigger';
    const trigger = s.trigger.schedule_human||s.trigger.command||s.trigger.event||(s.trigger.keywords&&s.trigger.keywords.length?'Keywords: '+s.trigger.keywords.join(', '):'');
    const checked = s.enabled?'checked':'';
    return '<div class="card"><div style="display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:0.5rem;">'
      +'<div style="display:flex;align-items:center;gap:0.625rem;"><label class="toggle"><input type="checkbox" '+checked+' onchange="toggleSkill(\''+esc(s.name)+'\',this.checked)"><span class="slider"></span></label><span style="font-weight:600;color:#f8fafc;">'+esc(s.name)+'</span></div>'
      +'<span class="badge '+tc+'" title="'+esc(typeLabel)+'">'+esc(typeLabel)+'</span></div>'
      +'<div style="color:#94a3b8;font-size:0.8125rem;margin-bottom:0.5rem;">'+(esc(s.description)||'No description')+'</div>'
      +(trigger?'<div style="font-size:0.6875rem;color:#64748b;">'+esc(trigger)+'</div>':'')
      +'<div style="margin-top:0.625rem;padding-top:0.625rem;border-top:1px solid rgba(71,85,105,0.3);display:flex;gap:0.5rem;">'
      +'<button class="btn btn-secondary btn-sm" onclick="editSkill(\''+esc(s.name)+'\')">Edit</button>'
      +(s.source!=='builtin'?'<button class="btn btn-danger btn-sm" onclick="delSkill(\''+esc(s.name)+'\')">Delete</button>':'')
      +'</div></div>';
  }).join('');
}
async function toggleSkill(name, enabled) {
  try {
    await fetchJSON('/api/skills/'+name+'/toggle',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({enabled:enabled})});
    showToast('Skill '+(enabled?'enabled':'disabled'),'success');
  } catch(e) { showToast('Toggle failed: '+e.message,'error'); loadSkills(); }
}
async function loadHub() {
  const el = document.getElementById('skills-hub-list');
  el.innerHTML = '<div class="loading">Loading hub...</div>';
  try {
    const d = await fetchJSON('/api/skills/hub');
    const hubSkills = d.skills || [];
    const installed = await fetchJSON('/api/skills');
    const installedNames = new Set((installed.skills||[]).map(s=>s.name.toLowerCase()));
    if (!hubSkills.length) { el.innerHTML = '<div class="empty-state">The Skill Hub is empty right now. Check back later.</div>'; return; }
    el.innerHTML = hubSkills.map(s => {
      const isInstalled = installedNames.has(s.name.toLowerCase());
      const tags = (s.tags||[]).map(t=>'<span class="badge badge-gray" style="margin-right:0.25rem;">'+esc(t)+'</span>').join('');
      return '<div class="card"><div style="display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:0.5rem;">'
        +'<span style="font-weight:600;color:#f8fafc;">'+esc(s.name)+'</span>'
        +(isInstalled?'<span class="badge badge-green">Installed</span>':'<button class="btn btn-primary btn-sm" onclick="installHub(\''+esc(s.name)+'\')">Install</button>')
        +'</div><div style="color:#94a3b8;font-size:0.8125rem;margin-bottom:0.5rem;">'+esc(s.description)+'</div>'
        +(tags?'<div style="margin-top:0.375rem;">'+tags+'</div>':'')
        +'</div>';
    }).join('');
  } catch(e) { el.innerHTML = '<div class="empty-state">Could not reach the Skill Hub: '+esc(e.message)+'</div>'; }
}
async function installHub(name) {
  try {
    await fetchJSON('/api/skills/hub/install',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:name})});
    showToast('Skill "'+name+'" installed!','success');
    loadHub();
  } catch(e) { showToast('Install failed: '+e.message,'error'); }
}
function openSkillModal() { document.getElementById('skill-modal-title').textContent='New Skill'; document.querySelectorAll('#skill-modal input,#skill-modal textarea,#skill-modal select').forEach(e=>e.value=''); document.getElementById('skill-modal').classList.add('show'); }
function closeSkillModal() { document.getElementById('skill-modal').classList.remove('show'); }
async function editSkill(name) {
  const s = await fetchJSON('/api/skills/'+name);
  document.getElementById('skill-modal-title').textContent='Edit: '+name;
  document.getElementById('sk-name').value=s.name||'';
  document.getElementById('sk-type').value=s.type||'prompt';
  document.getElementById('sk-desc').value=s.description||'';
  document.getElementById('sk-command').value=(s.trigger&&s.trigger.command)||'';
  document.getElementById('sk-schedule').value=(s.trigger&&s.trigger.schedule)||'';
  document.getElementById('sk-event').value=(s.trigger&&s.trigger.event)||'';
  document.getElementById('sk-prompt').value=s.prompt||'';
  document.getElementById('sk-steps').value=s.steps?JSON.stringify(s.steps,null,2):'';
  document.getElementById('skill-modal').classList.add('show');
}
async function saveSkill() {
  const name = document.getElementById('sk-name').value.trim();
  if (!name) { showToast('Skill name required','error'); return; }
  let steps = []; const st = document.getElementById('sk-steps').value.trim();
  if (st) { try { steps = JSON.parse(st); } catch(e) { showToast('Invalid steps JSON','error'); return; } }
  const skill = {name:name, type:document.getElementById('sk-type').value, description:document.getElementById('sk-desc').value,
    trigger:{command:document.getElementById('sk-command').value.trim()||undefined, schedule:document.getElementById('sk-schedule').value.trim()||undefined, event:document.getElementById('sk-event').value.trim()||undefined},
    prompt:document.getElementById('sk-prompt').value, steps:steps.length?steps:undefined};
  await fetchJSON('/api/skills',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(skill)});
  closeSkillModal(); showToast('Skill "'+name+'" saved','success'); loadSkills();
}
async function delSkill(name) {
  if (!confirm('Delete skill "'+name+'"?')) return;
  await fetchJSON('/api/skills/'+name,{method:'DELETE'});
  showToast('Skill deleted','success'); loadSkills();
}

/* === Sessions === */
async function loadSessions() {
  const d = await fetchJSON('/api/dashboard/sessions');
  const list = d.sessions || [];
  const tbody = document.getElementById('sessions-body');
  if (!list.length) { tbody.innerHTML = '<tr><td colspan="5" class="empty-state">No sessions yet</td></tr>'; return; }
  const trustBadge = t => {const m={0:['Unknown','badge-gray'],1:['Group','badge-blue'],2:['Trusted','badge-green'],3:['Operator','badge-orange']};const p=m[t]||m[0];return '<span class="badge '+p[1]+'">'+p[0]+'</span>';};
  tbody.innerHTML = list.map(s => '<tr onclick="loadMessages(\''+esc(s.id)+'\')">'
    +'<td style="font-family:ui-monospace,monospace;font-size:0.75rem;">'+esc(s.id.substring(0,8))+'&hellip;</td>'
    +'<td><span class="badge badge-blue">'+esc(s.channel)+'</span></td>'
    +'<td>'+trustBadge(s.trust_tier)+'</td>'
    +'<td style="font-size:0.75rem;color:#94a3b8;">'+fmtDate(s.created_at)+'</td>'
    +'<td style="font-size:0.75rem;color:#94a3b8;">'+fmtDate(s.last_active)+'</td></tr>').join('');
}
async function loadMessages(sid) {
  const d = await fetchJSON('/api/dashboard/sessions/'+sid+'/messages');
  const msgs = d.messages || [];
  document.getElementById('session-msg-title').textContent = 'Messages \u2014 '+sid.substring(0,8)+'...';
  const area = document.getElementById('chat-area');
  if (!msgs.length) { area.innerHTML = '<div class="empty-state">No messages</div>'; }
  else { area.innerHTML = msgs.map(m => '<div class="chat-msg '+(m.role==='user'?'chat-user':'chat-assistant')+'">'+esc(m.content)+'<div class="chat-time">'+fmtDate(m.created_at)+'</div></div>').join(''); }
  document.getElementById('session-messages').style.display = 'block';
  area.scrollTop = area.scrollHeight;
}
function closeMessages() { document.getElementById('session-messages').style.display = 'none'; }

/* === Memory === */
let memDebounce = null;
function debounceMemSearch() { clearTimeout(memDebounce); memDebounce = setTimeout(() => loadMemory(document.getElementById('mem-search').value.trim()), 300); }
async function loadMemory(q) {
  const url = q ? '/api/dashboard/memory?q='+encodeURIComponent(q) : '/api/dashboard/memory';
  const d = await fetchJSON(url);
  const list = d.memories || [];
  const el = document.getElementById('memory-list');
  if (!list.length) { el.innerHTML = '<div class="empty-state">'+(q?'No memories match "'+esc(q)+'"':'No memories stored yet')+'</div>'; return; }
  const catBadge = c => {const m={preference:'badge-purple',fact:'badge-blue',context:'badge-green'};return '<span class="badge '+(m[c]||'badge-gray')+'">'+esc(c)+'</span>';};
  el.innerHTML = list.map(m => '<div class="card mem-card">'
    +'<button class="mem-del" onclick="delMemory('+m.id+')" title="Delete">&times;</button>'
    +catBadge(m.category)
    +'<div class="mem-key" style="margin-top:0.5rem;">'+esc(m.key)+'</div>'
    +'<div class="mem-content">'+esc(m.content)+'</div>'
    +'<div class="mem-meta"><span>Session: '+esc((m.session_id||'').substring(0,8))+'</span><span>'+fmtDate(m.created_at)+'</span></div></div>').join('');
}
async function delMemory(id) {
  if (!confirm('Delete this memory?')) return;
  await fetchJSON('/api/dashboard/memory/'+id, {method:'DELETE'});
  showToast('Memory deleted','success');
  loadMemory(document.getElementById('mem-search').value.trim());
}

/* === Events === */
async function loadEvents() {
  let url = '/api/dashboard/events?limit=50';
  if (S.eventFilter) url += '&type='+S.eventFilter;
  const d = await fetchJSON(url);
  const list = d.events || [];
  const el = document.getElementById('events-list');
  if (!list.length) { el.innerHTML = '<div class="empty-state">No events recorded</div>'; return; }
  const typeBadge = t => {
    const m={'message.inbound':'badge-blue','message.outbound':'badge-green','tool.request':'badge-yellow','tool.result':'badge-purple','system.startup':'badge-gray','system.shutdown':'badge-gray'};
    return '<span class="badge '+(m[t]||'badge-gray')+'">'+esc(t)+'</span>';
  };
  el.innerHTML = list.map(e => '<div class="event-row">'
    +'<span class="event-type">'+typeBadge(e.type)+'</span>'
    +'<span class="event-chan">'+esc(e.channel||'\u2014')+'</span>'
    +'<span class="event-sess">'+(e.session_id?esc(e.session_id.substring(0,8)):'\u2014')+'</span>'
    +'<span class="event-time">'+fmtDate(e.created_at)+'</span></div>').join('');
}
function filterEvents(f) {
  const typeMap = {'message':'message','tool':'tool','system':'system'};
  S.eventFilter = typeMap[f] || '';
  document.querySelectorAll('.filter-bar .tab-btn').forEach(b => b.classList.remove('active'));
  if (event && event.target) event.target.classList.add('active');
  loadEvents();
}
function toggleAutoRefresh() {
  S.autoRefresh = !S.autoRefresh;
  document.getElementById('ev-auto-btn').textContent = 'Auto-refresh: '+(S.autoRefresh?'On':'Off');
  if (S.autoRefresh) { S.refreshTimer = setInterval(()=>{ if(S.tab==='events') loadEvents(); }, 5000); }
  else { clearInterval(S.refreshTimer); S.refreshTimer = null; }
}

/* === Utilities === */
const _apiKey = '{{.APIKey}}';
async function fetchJSON(url, opts) {
  if (!opts) opts = {};
  if (!opts.headers) opts.headers = {};
  if (_apiKey) opts.headers['Authorization'] = 'Bearer ' + _apiKey;
  const r = await fetch(url, opts);
  if (!r.ok) { const t = await r.text(); throw new Error(t); }
  return r.json();
}
function setText(id, v) { const el = document.getElementById(id); if (el) el.textContent = v; }
function esc(s) { if (!s) return ''; const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
function fmtNum(n) { return n != null ? n.toLocaleString() : '0'; }
function fmtUptime(sec) {
  if (!sec || sec < 0) return '0m';
  const d = Math.floor(sec/86400), h = Math.floor((sec%86400)/3600), m = Math.floor((sec%3600)/60);
  let s = ''; if (d) s += d+'d '; if (h || d) s += h+'h '; s += m+'m'; return s;
}
function fmtDate(s) {
  if (!s) return '';
  try {
    const dt = new Date(s.replace(' ','T')+'Z');
    const now = new Date();
    const diff = (now - dt) / 1000;
    if (diff < 60) return 'just now';
    if (diff < 3600) return Math.floor(diff/60) + 'm ago';
    if (diff < 86400) return Math.floor(diff/3600) + 'h ago';
    if (diff < 604800) return Math.floor(diff/86400) + 'd ago';
    return dt.toLocaleDateString(undefined, {month:'short',day:'numeric'}) + ' ' + dt.toLocaleTimeString(undefined, {hour:'2-digit',minute:'2-digit'});
  } catch(e) { return s; }
}
function showToast(msg, type) {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'toast toast-'+(type||'success')+' show';
  setTimeout(() => t.classList.remove('show'), 3500);
}

/* === Uptime ticker === */
setInterval(() => {
  S.uptimeSec++;
  setText('ov-uptime', 'Uptime: '+fmtUptime(S.uptimeSec));
  setText('footer-uptime', 'Uptime: '+fmtUptime(S.uptimeSec));
}, 1000);

/* === Init === */
loadTab('overview');
</script>
</body>
</html>` + ""
