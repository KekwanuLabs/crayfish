package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/KekwanuLabs/crayfish/internal/security"
	"github.com/KekwanuLabs/crayfish/internal/skills"
)

// SkillsAPI provides HTTP endpoints for managing skills.
type SkillsAPI struct {
	registry  *skills.Registry
	skillsDir string
	hub       *skills.HubClient
}

// NewSkillsAPI creates a skills API handler.
func NewSkillsAPI(registry *skills.Registry, skillsDir string, hub *skills.HubClient) *SkillsAPI {
	return &SkillsAPI{
		registry:  registry,
		skillsDir: skillsDir,
		hub:       hub,
	}
}

// RegisterRoutes adds skills endpoints to the HTTP mux.
// The wrap function applies authentication middleware to each handler.
func (api *SkillsAPI) RegisterRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/skills/hub", wrap(api.handleHub))
	mux.HandleFunc("/api/skills/hub/install", wrap(api.handleHubInstall))
	mux.HandleFunc("/api/skills", wrap(api.handleSkills))
	mux.HandleFunc("/api/skills/", wrap(api.handleSkill))
}

// handleSkills handles GET (list) and POST (create) for /api/skills
func (api *SkillsAPI) handleSkills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.listSkills(w, r)
	case http.MethodPost:
		api.createSkill(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSkill handles GET, DELETE, PUT for /api/skills/{name}
func (api *SkillsAPI) handleSkill(w http.ResponseWriter, r *http.Request) {
	// Extract skill name from path: /api/skills/{name}
	name := strings.TrimPrefix(r.URL.Path, "/api/skills/")
	if name == "" {
		http.Error(w, "Skill name required", http.StatusBadRequest)
		return
	}

	// Handle /api/skills/{name}/toggle
	if strings.HasSuffix(name, "/toggle") {
		name = strings.TrimSuffix(name, "/toggle")
		if r.Method == http.MethodPut {
			api.toggleSkill(w, r, name)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch r.Method {
	case http.MethodGet:
		api.getSkill(w, r, name)
	case http.MethodDelete:
		api.deleteSkill(w, r, name)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// listSkills returns all registered skills.
func (api *SkillsAPI) listSkills(w http.ResponseWriter, r *http.Request) {
	allSkills := api.registry.All()

	// Convert to a simpler JSON format.
	type skillSummary struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Type        string `json:"type"`
		Source      string `json:"source"`
		Enabled     bool   `json:"enabled"`
		Trigger     struct {
			Command         string   `json:"command,omitempty"`
			Schedule        string   `json:"schedule,omitempty"`
			ScheduleHuman   string   `json:"schedule_human,omitempty"`
			Event           string   `json:"event,omitempty"`
			Keywords        []string `json:"keywords,omitempty"`
		} `json:"trigger"`
	}

	summaries := make([]skillSummary, 0, len(allSkills))
	for _, s := range allSkills {
		enabled := s.Enabled == nil || *s.Enabled
		sum := skillSummary{
			Name:        s.Name,
			Description: s.Description,
			Type:        string(s.Type),
			Source:      s.Source,
			Enabled:     enabled,
		}
		sum.Trigger.Command = s.Trigger.Command
		sum.Trigger.Schedule = s.Trigger.Schedule
		if s.Trigger.Schedule != "" {
			sum.Trigger.ScheduleHuman = skills.CronToHuman(s.Trigger.Schedule)
		}
		sum.Trigger.Event = s.Trigger.Event
		sum.Trigger.Keywords = s.Trigger.Keywords
		summaries = append(summaries, sum)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"skills": summaries,
		"count":  len(summaries),
	})
}

// getSkill returns a single skill by name.
func (api *SkillsAPI) getSkill(w http.ResponseWriter, r *http.Request, name string) {
	skill := api.registry.Get(name)
	if skill == nil {
		http.Error(w, "Skill not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(skill)
}

// createSkill registers a new skill from JSON body.
func (api *SkillsAPI) createSkill(w http.ResponseWriter, r *http.Request) {
	var skill skills.Skill
	if err := json.NewDecoder(r.Body).Decode(&skill); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if skill.Name == "" {
		http.Error(w, "Skill name is required", http.StatusBadRequest)
		return
	}

	// Set defaults.
	if skill.Type == "" {
		skill.Type = skills.TypePrompt
	}
	if skill.Version == 0 {
		skill.Version = 1
	}
	skill.Source = "web"

	// Validate skill safety before registering.
	var toolSteps []struct{ Tool string }
	for _, s := range skill.Steps {
		toolSteps = append(toolSteps, struct{ Tool string }{Tool: s.Tool})
	}
	validation := security.ValidateSkill(skill.Name, skill.Prompt, toolSteps)
	if !validation.Safe {
		http.Error(w, "Skill rejected: "+strings.Join(validation.Errors, "; "), http.StatusBadRequest)
		return
	}

	// Register in memory.
	if err := api.registry.Register(&skill); err != nil {
		http.Error(w, "Failed to register skill: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Save to file for persistence.
	if api.skillsDir != "" {
		if err := api.registry.SaveToFile(&skill, api.skillsDir); err != nil {
			// Non-fatal: skill is registered in memory.
			// Log warning but don't fail the request.
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "created",
		"skill":   skill.Name,
		"message": "Skill registered and ready to use",
	})
}

// deleteSkill removes a skill by name.
func (api *SkillsAPI) deleteSkill(w http.ResponseWriter, r *http.Request, name string) {
	skill := api.registry.Get(name)
	if skill == nil {
		http.Error(w, "Skill not found", http.StatusNotFound)
		return
	}

	// Don't allow deleting built-in skills.
	if skill.Source == "builtin" {
		http.Error(w, "Cannot delete built-in skills", http.StatusForbidden)
		return
	}

	// Delete from registry.
	if !api.registry.Delete(name) {
		http.Error(w, "Failed to delete skill", http.StatusInternalServerError)
		return
	}

	// Delete file if it exists.
	if api.skillsDir != "" {
		api.registry.DeleteFile(name, api.skillsDir)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "deleted",
		"skill":   name,
		"message": "Skill removed",
	})
}

// toggleSkill enables or disables a skill.
func (api *SkillsAPI) toggleSkill(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := api.registry.SetEnabled(name, body.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "updated",
		"skill":   name,
		"enabled": body.Enabled,
	})
}

// handleHub proxies the hub index for the dashboard (avoids CORS).
func (api *SkillsAPI) handleHub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if api.hub == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"skills": []interface{}{},
			"count":  0,
		})
		return
	}

	index, err := api.hub.FetchIndex(r.Context())
	if err != nil {
		http.Error(w, "Failed to fetch hub: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"skills": index.Skills,
		"count":  len(index.Skills),
	})
}

// handleHubInstall installs a skill from the hub by name.
func (api *SkillsAPI) handleHubInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if api.hub == nil {
		http.Error(w, "Skill Hub is not configured", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "Skill name is required", http.StatusBadRequest)
		return
	}

	// Look up in hub.
	hubSkill, err := api.hub.FindByName(r.Context(), body.Name)
	if err != nil {
		http.Error(w, "Skill not found in hub: "+err.Error(), http.StatusNotFound)
		return
	}

	// Download and parse.
	skill, err := api.hub.FetchSkill(r.Context(), hubSkill.URL)
	if err != nil {
		http.Error(w, "Failed to download skill: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Validate.
	var toolSteps []struct{ Tool string }
	for _, s := range skill.Steps {
		toolSteps = append(toolSteps, struct{ Tool string }{Tool: s.Tool})
	}
	validation := security.ValidateSkill(skill.Name, skill.Prompt, toolSteps)
	if !validation.Safe {
		http.Error(w, "Skill rejected: "+strings.Join(validation.Errors, "; "), http.StatusBadRequest)
		return
	}

	// Register.
	skill.Source = "hub"
	if err := api.registry.Register(skill); err != nil {
		http.Error(w, "Failed to register: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Save to file.
	if api.skillsDir != "" {
		if err := api.registry.SaveToFile(skill, api.skillsDir); err != nil {
			// Non-fatal.
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "installed",
		"skill":   skill.Name,
		"message": "Skill installed from hub",
	})
}
