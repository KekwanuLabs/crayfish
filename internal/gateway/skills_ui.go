package gateway

import (
	"html/template"
	"net/http"

	"github.com/KekwanuLabs/crayfish/internal/skills"
)

// SkillsUI serves the skills management web interface.
type SkillsUI struct {
	registry *skills.Registry
	apiKey   string
	tmpl     *template.Template
}

// NewSkillsUI creates the skills web UI handler.
func NewSkillsUI(registry *skills.Registry, apiKey string) *SkillsUI {
	tmpl := template.Must(template.New("skills").Parse(skillsPageHTML))
	return &SkillsUI{
		registry: registry,
		apiKey:   apiKey,
		tmpl:     tmpl,
	}
}

// RegisterRoutes adds the skills UI route.
func (ui *SkillsUI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/skills", ui.handlePage)
}

func (ui *SkillsUI) handlePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	ui.tmpl.Execute(w, map[string]interface{}{
		"SkillCount": ui.registry.Count(),
		"APIKey":     ui.apiKey,
	})
}

const skillsPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Crayfish Skills</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: linear-gradient(135deg, #0c1222 0%, #1a1a2e 50%, #16213e 100%);
    color: #e2e8f0;
    min-height: 100vh;
    padding: 1.5rem;
  }
  .container { max-width: 800px; margin: 0 auto; }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 2rem;
    padding-bottom: 1rem;
    border-bottom: 1px solid rgba(71, 85, 105, 0.5);
  }

  .logo {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .logo svg { width: 40px; height: 40px; }

  .logo h1 {
    font-size: 1.5rem;
    background: linear-gradient(135deg, #ff6b35 0%, #f7931e 100%);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
  }

  .btn {
    padding: 0.625rem 1.25rem;
    border: none;
    border-radius: 8px;
    font-size: 0.875rem;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.2s;
  }

  .btn-primary {
    background: linear-gradient(135deg, #f97316 0%, #fb923c 100%);
    color: #0f172a;
  }

  .btn-primary:hover { transform: translateY(-1px); }

  .btn-secondary {
    background: transparent;
    border: 1px solid #475569;
    color: #94a3b8;
  }

  .btn-secondary:hover { border-color: #f97316; color: #f97316; }

  .btn-danger {
    background: rgba(220, 38, 38, 0.2);
    border: 1px solid #dc2626;
    color: #fca5a5;
  }

  .btn-danger:hover { background: rgba(220, 38, 38, 0.4); }

  /* Skills List */
  .skills-grid {
    display: grid;
    gap: 1rem;
  }

  .skill-card {
    background: rgba(30, 41, 59, 0.8);
    border-radius: 12px;
    padding: 1.25rem;
    border: 1px solid rgba(71, 85, 105, 0.5);
    transition: border-color 0.2s;
  }

  .skill-card:hover { border-color: rgba(249, 115, 22, 0.5); }

  .skill-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    margin-bottom: 0.75rem;
  }

  .skill-name {
    font-size: 1.125rem;
    font-weight: 600;
    color: #f8fafc;
  }

  .skill-type {
    font-size: 0.6875rem;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
  }

  .type-workflow { background: rgba(59, 130, 246, 0.2); color: #60a5fa; }
  .type-prompt { background: rgba(16, 185, 129, 0.2); color: #6ee7b7; }
  .type-reactive { background: rgba(168, 85, 247, 0.2); color: #c4b5fd; }

  .skill-desc {
    color: #94a3b8;
    font-size: 0.875rem;
    margin-bottom: 0.75rem;
  }

  .skill-trigger {
    font-size: 0.75rem;
    color: #64748b;
    display: flex;
    gap: 1rem;
    flex-wrap: wrap;
  }

  .trigger-item {
    display: flex;
    align-items: center;
    gap: 0.25rem;
  }

  .trigger-item code {
    background: rgba(15, 23, 42, 0.8);
    padding: 0.125rem 0.375rem;
    border-radius: 4px;
    color: #f97316;
  }

  .skill-actions {
    margin-top: 0.75rem;
    padding-top: 0.75rem;
    border-top: 1px solid rgba(71, 85, 105, 0.3);
    display: flex;
    gap: 0.5rem;
  }

  .skill-actions .btn { padding: 0.375rem 0.75rem; font-size: 0.75rem; }

  /* Modal */
  .modal-overlay {
    display: none;
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.7);
    align-items: center;
    justify-content: center;
    z-index: 100;
    padding: 1rem;
  }

  .modal-overlay.show { display: flex; }

  .modal {
    background: #1e293b;
    border-radius: 16px;
    width: 100%;
    max-width: 600px;
    max-height: 90vh;
    overflow-y: auto;
    border: 1px solid rgba(71, 85, 105, 0.5);
  }

  .modal-header {
    padding: 1.25rem;
    border-bottom: 1px solid rgba(71, 85, 105, 0.3);
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .modal-header h2 {
    font-size: 1.25rem;
    color: #f8fafc;
  }

  .modal-close {
    background: none;
    border: none;
    color: #64748b;
    font-size: 1.5rem;
    cursor: pointer;
    line-height: 1;
  }

  .modal-close:hover { color: #f97316; }

  .modal-body { padding: 1.25rem; }

  .form-group { margin-bottom: 1rem; }

  .form-group label {
    display: block;
    font-size: 0.8125rem;
    font-weight: 500;
    color: #94a3b8;
    margin-bottom: 0.375rem;
    text-transform: uppercase;
    letter-spacing: 0.5px;
  }

  .form-group input,
  .form-group select,
  .form-group textarea {
    width: 100%;
    padding: 0.75rem;
    border-radius: 8px;
    border: 1px solid #475569;
    background: rgba(15, 23, 42, 0.8);
    color: #f8fafc;
    font-size: 0.9375rem;
    font-family: inherit;
  }

  .form-group textarea {
    min-height: 120px;
    resize: vertical;
    font-family: ui-monospace, monospace;
    font-size: 0.8125rem;
  }

  .form-group input:focus,
  .form-group select:focus,
  .form-group textarea:focus {
    outline: none;
    border-color: #f97316;
  }

  .form-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
  }

  .form-hint {
    font-size: 0.6875rem;
    color: #64748b;
    margin-top: 0.25rem;
  }

  .modal-footer {
    padding: 1.25rem;
    border-top: 1px solid rgba(71, 85, 105, 0.3);
    display: flex;
    justify-content: flex-end;
    gap: 0.75rem;
  }

  /* Empty state */
  .empty-state {
    text-align: center;
    padding: 3rem;
    color: #64748b;
  }

  .empty-state svg {
    width: 64px;
    height: 64px;
    margin-bottom: 1rem;
    opacity: 0.5;
  }

  /* Status messages */
  .status {
    padding: 0.75rem 1rem;
    border-radius: 8px;
    margin-bottom: 1rem;
    display: none;
  }

  .status.show { display: block; }
  .status.success { background: rgba(6, 78, 59, 0.8); color: #6ee7b7; }
  .status.error { background: rgba(127, 29, 29, 0.8); color: #fca5a5; }
</style>
</head>
<body>
<div class="container">
  <header>
    <div class="logo">
      <svg viewBox="0 0 100 100" xmlns="http://www.w3.org/2000/svg">
        <defs>
          <linearGradient id="g" x1="0%" y1="0%" x2="100%" y2="100%">
            <stop offset="0%" style="stop-color:#ff6b35"/>
            <stop offset="100%" style="stop-color:#f7931e"/>
          </linearGradient>
        </defs>
        <ellipse cx="50" cy="50" rx="20" ry="14" fill="url(#g)"/>
        <ellipse cx="35" cy="48" rx="12" ry="8" fill="url(#g)"/>
        <circle cx="28" cy="45" r="3" fill="#1a1a2e"/>
      </svg>
      <h1>Crayfish Skills</h1>
    </div>
    <button class="btn btn-primary" onclick="openCreateModal()">+ New Skill</button>
  </header>

  <div id="status" class="status"></div>

  <div class="skills-grid" id="skills-list">
    <div class="empty-state">
      <svg fill="currentColor" viewBox="0 0 16 16">
        <path d="M6 1h6v2h-6V1zm-.5 3h7a.5.5 0 0 1 .5.5v4a.5.5 0 0 1-.5.5h-7a.5.5 0 0 1-.5-.5v-4a.5.5 0 0 1 .5-.5zM3 4a1 1 0 0 0-1 1v5a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V5a1 1 0 0 0-1-1H3z"/>
      </svg>
      <p>Loading skills...</p>
    </div>
  </div>
</div>

<!-- Create/Edit Modal -->
<div class="modal-overlay" id="modal">
  <div class="modal">
    <div class="modal-header">
      <h2 id="modal-title">Create New Skill</h2>
      <button class="modal-close" onclick="closeModal()">&times;</button>
    </div>
    <div class="modal-body">
      <form id="skill-form">
        <div class="form-row">
          <div class="form-group">
            <label for="skill-name">Name</label>
            <input type="text" id="skill-name" placeholder="my-skill" required>
            <div class="form-hint">Unique identifier (lowercase, no spaces)</div>
          </div>
          <div class="form-group">
            <label for="skill-type">Type</label>
            <select id="skill-type">
              <option value="workflow">Workflow (multi-step tools)</option>
              <option value="prompt">Prompt (augment context)</option>
              <option value="reactive">Reactive (event-triggered)</option>
            </select>
          </div>
        </div>

        <div class="form-group">
          <label for="skill-desc">Description</label>
          <input type="text" id="skill-desc" placeholder="What this skill does...">
        </div>

        <div class="form-row">
          <div class="form-group">
            <label for="skill-command">Command Trigger</label>
            <input type="text" id="skill-command" placeholder="/briefing">
            <div class="form-hint">Slash command to activate (e.g., /briefing)</div>
          </div>
          <div class="form-group">
            <label for="skill-schedule">Schedule (Cron)</label>
            <input type="text" id="skill-schedule" placeholder="0 7 * * *">
            <div class="form-hint">Cron expression (e.g., 0 7 * * * for 7 AM daily)</div>
          </div>
        </div>

        <div class="form-group">
          <label for="skill-event">Event Trigger</label>
          <input type="text" id="skill-event" placeholder="email.new">
          <div class="form-hint">Bus event type (e.g., email.new, message.inbound)</div>
        </div>

        <div class="form-group">
          <label for="skill-prompt">Prompt Template</label>
          <textarea id="skill-prompt" placeholder="Enter the prompt template...

Use {{"{{variable}}"}} for interpolation.
For workflow skills, step results are available as variables."></textarea>
        </div>

        <div class="form-group">
          <label for="skill-steps">Steps (JSON, for workflow type)</label>
          <textarea id="skill-steps" placeholder='[
  {"tool": "email_check", "params": {"limit": 10}, "store_as": "emails"},
  {"tool": "web_search", "params": {"query": "{{"{{topic}}"}}"}, "store_as": "news"}
]'></textarea>
          <div class="form-hint">Array of tool invocations with params and store_as</div>
        </div>
      </form>
    </div>
    <div class="modal-footer">
      <button class="btn btn-secondary" onclick="closeModal()">Cancel</button>
      <button class="btn btn-primary" onclick="saveSkill()">Save Skill</button>
    </div>
  </div>
</div>

<script>
const _apiKey = '{{.APIKey}}';
function authHeaders(extra) {
  const h = Object.assign({}, extra || {});
  if (_apiKey) h['Authorization'] = 'Bearer ' + _apiKey;
  return h;
}
let skills = [];

async function loadSkills() {
  try {
    const resp = await fetch('/api/skills', {headers: authHeaders()});
    const data = await resp.json();
    skills = data.skills || [];
    renderSkills();
  } catch (e) {
    showStatus('Failed to load skills: ' + e.message, 'error');
  }
}

function renderSkills() {
  const container = document.getElementById('skills-list');

  if (skills.length === 0) {
    container.innerHTML = '<div class="empty-state"><p>No skills yet. Create your first skill!</p></div>';
    return;
  }

  container.innerHTML = skills.map(s => ` + "`" + `
    <div class="skill-card">
      <div class="skill-header">
        <span class="skill-name">${s.name}</span>
        <span class="skill-type type-${s.type}">${s.type}</span>
      </div>
      <div class="skill-desc">${s.description || 'No description'}</div>
      <div class="skill-trigger">
        ${s.trigger.command ? '<span class="trigger-item">Command: <code>' + s.trigger.command + '</code></span>' : ''}
        ${s.trigger.schedule ? '<span class="trigger-item">Schedule: <code>' + s.trigger.schedule + '</code></span>' : ''}
        ${s.trigger.event ? '<span class="trigger-item">Event: <code>' + s.trigger.event + '</code></span>' : ''}
      </div>
      <div class="skill-actions">
        <button class="btn btn-secondary" onclick="viewSkill('${s.name}')">View</button>
        ${s.source !== 'builtin' ? '<button class="btn btn-danger" onclick="deleteSkill(\'' + s.name + '\')">Delete</button>' : ''}
      </div>
    </div>
  ` + "`" + `).join('');
}

function openCreateModal() {
  document.getElementById('modal-title').textContent = 'Create New Skill';
  document.getElementById('skill-form').reset();
  document.getElementById('modal').classList.add('show');
}

function closeModal() {
  document.getElementById('modal').classList.remove('show');
}

async function viewSkill(name) {
  try {
    const resp = await fetch('/api/skills/' + name, {headers: authHeaders()});
    const skill = await resp.json();

    document.getElementById('modal-title').textContent = 'Edit Skill: ' + name;
    document.getElementById('skill-name').value = skill.name;
    document.getElementById('skill-type').value = skill.type;
    document.getElementById('skill-desc').value = skill.description || '';
    document.getElementById('skill-command').value = skill.trigger?.command || '';
    document.getElementById('skill-schedule').value = skill.trigger?.schedule || '';
    document.getElementById('skill-event').value = skill.trigger?.event || '';
    document.getElementById('skill-prompt').value = skill.prompt || '';
    document.getElementById('skill-steps').value = skill.steps ? JSON.stringify(skill.steps, null, 2) : '';

    document.getElementById('modal').classList.add('show');
  } catch (e) {
    showStatus('Failed to load skill: ' + e.message, 'error');
  }
}

async function saveSkill() {
  const name = document.getElementById('skill-name').value.trim();
  if (!name) {
    showStatus('Skill name is required', 'error');
    return;
  }

  let steps = [];
  const stepsText = document.getElementById('skill-steps').value.trim();
  if (stepsText) {
    try {
      steps = JSON.parse(stepsText);
    } catch (e) {
      showStatus('Invalid JSON in steps: ' + e.message, 'error');
      return;
    }
  }

  const skill = {
    name: name,
    type: document.getElementById('skill-type').value,
    description: document.getElementById('skill-desc').value,
    trigger: {
      command: document.getElementById('skill-command').value.trim() || undefined,
      schedule: document.getElementById('skill-schedule').value.trim() || undefined,
      event: document.getElementById('skill-event').value.trim() || undefined,
    },
    prompt: document.getElementById('skill-prompt').value,
    steps: steps.length > 0 ? steps : undefined,
  };

  try {
    const resp = await fetch('/api/skills', {
      method: 'POST',
      headers: authHeaders({'Content-Type': 'application/json'}),
      body: JSON.stringify(skill),
    });

    if (!resp.ok) {
      const err = await resp.text();
      throw new Error(err);
    }

    closeModal();
    showStatus('Skill "' + name + '" saved successfully!', 'success');
    loadSkills();
  } catch (e) {
    showStatus('Failed to save skill: ' + e.message, 'error');
  }
}

async function deleteSkill(name) {
  if (!confirm('Delete skill "' + name + '"?')) return;

  try {
    const resp = await fetch('/api/skills/' + name, {method: 'DELETE', headers: authHeaders()});
    if (!resp.ok) throw new Error(await resp.text());

    showStatus('Skill "' + name + '" deleted', 'success');
    loadSkills();
  } catch (e) {
    showStatus('Failed to delete skill: ' + e.message, 'error');
  }
}

function showStatus(msg, type) {
  const el = document.getElementById('status');
  el.textContent = msg;
  el.className = 'status show ' + type;
  setTimeout(() => el.classList.remove('show'), 4000);
}

// Load on startup
loadSkills();
</script>
</body>
</html>`
