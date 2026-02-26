package skills

import (
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool { return &b }

// isEnabled returns whether a skill is enabled (nil = true).
func isEnabled(s *Skill) bool {
	return s.Enabled == nil || *s.Enabled
}

// Registry holds all loaded skills and provides lookup by name, command, or event.
type Registry struct {
	mu     sync.RWMutex
	skills map[string]*Skill // name → skill
	logger *slog.Logger
}

// NewRegistry creates an empty skill registry.
func NewRegistry(logger *slog.Logger) *Registry {
	return &Registry{
		skills: make(map[string]*Skill),
		logger: logger,
	}
}

// Register adds a skill to the registry.
func (r *Registry) Register(skill *Skill) error {
	if skill.Name == "" {
		return fmt.Errorf("skill name is required")
	}
	if skill.Type == "" {
		skill.Type = TypePrompt
	}
	if skill.Category == "" {
		skill.Category = "general"
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.skills[skill.Name]; exists {
		r.logger.Info("skill replaced", "name", skill.Name, "source", skill.Source)
	}

	r.skills[skill.Name] = skill
	r.logger.Debug("skill registered", "name", skill.Name, "type", skill.Type, "source", skill.Source)
	return nil
}

// Get returns a skill by name, or nil if not found.
func (r *Registry) Get(name string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.skills[name]
}

// All returns all registered skills.
func (r *Registry) All() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		result = append(result, s)
	}
	return result
}

// FindByCommand returns the skill triggered by the given command (e.g., "/briefing").
// Disabled skills are skipped.
func (r *Registry) FindByCommand(command string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, s := range r.skills {
		if isEnabled(s) && s.Trigger.Command != "" && s.Trigger.Command == command {
			return s
		}
	}
	return nil
}

// FindByEvent returns all skills triggered by the given event type.
// Disabled skills are skipped.
func (r *Registry) FindByEvent(eventType string) []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Skill
	for _, s := range r.skills {
		if isEnabled(s) && s.Trigger.Event != "" && s.Trigger.Event == eventType {
			result = append(result, s)
		}
	}
	return result
}

// FindScheduled returns all skills that have a cron schedule.
// Disabled skills are skipped.
func (r *Registry) FindScheduled() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Skill
	for _, s := range r.skills {
		if isEnabled(s) && s.Trigger.Schedule != "" {
			result = append(result, s)
		}
	}
	return result
}

// FindByKeyword returns skills whose keywords match the given text (case-insensitive substring).
// Disabled skills are skipped.
func (r *Registry) FindByKeyword(text string) []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	lower := strings.ToLower(text)
	var result []*Skill
	for _, s := range r.skills {
		if !isEnabled(s) {
			continue
		}
		for _, kw := range s.Trigger.Keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				result = append(result, s)
				break
			}
		}
	}
	return result
}

// FindByCategory returns all skills in the given category.
func (r *Registry) FindByCategory(category string) []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Skill
	for _, s := range r.skills {
		if s.Category == category {
			result = append(result, s)
		}
	}
	return result
}

// Categories returns sorted unique category names across all registered skills.
func (r *Registry) Categories() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	for _, s := range r.skills {
		seen[s.Category] = true
	}

	cats := make([]string, 0, len(seen))
	for c := range seen {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	return cats
}

// SetEnabled enables or disables a skill by name. Updates in-memory and re-saves the YAML file.
func (r *Registry) SetEnabled(name string, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	skill, exists := r.skills[name]
	if !exists {
		return fmt.Errorf("skill %q not found", name)
	}

	skill.Enabled = boolPtr(enabled)
	r.logger.Info("skill enabled state changed", "name", name, "enabled", enabled)

	// Re-save to file if the skill has a file source.
	if skill.Source != "" && skill.Source != "builtin" && skill.Source != "web" {
		data, err := yaml.Marshal(skill)
		if err != nil {
			return fmt.Errorf("marshal skill: %w", err)
		}
		if err := os.WriteFile(skill.Source, data, 0644); err != nil {
			r.logger.Warn("failed to re-save skill file after toggle", "path", skill.Source, "error", err)
			// Non-fatal: in-memory state is updated.
		}
	}

	return nil
}

// Count returns the number of registered skills.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.skills)
}

// Delete removes a skill from the registry by name.
func (r *Registry) Delete(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.skills[name]; !exists {
		return false
	}
	delete(r.skills, name)
	r.logger.Info("skill deleted", "name", name)
	return true
}

// SaveToFile saves a skill to a YAML file under a category subfolder.
// Empty category defaults to "general".
func (r *Registry) SaveToFile(skill *Skill, dir string) error {
	data, err := yaml.Marshal(skill)
	if err != nil {
		return fmt.Errorf("marshal skill: %w", err)
	}

	if skill.Category == "" {
		skill.Category = "general"
	}
	targetDir := filepath.Join(dir, skill.Category)

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}

	path := filepath.Join(targetDir, skill.Name+".yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write skill file: %w", err)
	}

	skill.Source = path
	r.logger.Info("skill saved to file", "name", skill.Name, "path", path)
	return nil
}

// DeleteFile removes a skill's YAML file from category subfolders.
func (r *Registry) DeleteFile(name, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // Best effort.
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subPath := filepath.Join(dir, entry.Name(), name+".yaml")
		if err := os.Remove(subPath); err == nil {
			return nil
		}
	}
	return nil
}

// LoadFromDir loads skills from category subdirectories (one level deep).
// Top-level YAML files are ignored with a warning log.
func (r *Registry) LoadFromDir(dirPath string) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			r.logger.Debug("skills directory not found, skipping", "path", dirPath)
			return nil
		}
		return fmt.Errorf("read skills dir %s: %w", dirPath, err)
	}

	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() {
			// Recurse one level into subdirectories.
			subDir := filepath.Join(dirPath, entry.Name())
			category := entry.Name()
			n, err := r.loadSkillsFromDir(subDir, category)
			if err != nil {
				r.logger.Warn("failed to load skills from subfolder", "path", subDir, "error", err)
				continue
			}
			loaded += n
			continue
		}

		// Top-level YAML files are no longer loaded — skills must live in category subfolders.
		name := entry.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			r.logger.Warn("ignoring top-level skill file (move to a category subfolder)",
				"path", filepath.Join(dirPath, name))
		}
	}

	if loaded > 0 {
		r.logger.Info("loaded skills from directory", "path", dirPath, "count", loaded)
	}
	return nil
}

// loadSkillsFromDir loads skill files from a directory, optionally inferring category
// from the subfolder name when the skill has no explicit category.
func (r *Registry) loadSkillsFromDir(dirPath, category string) (int, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0, fmt.Errorf("read skills subdir %s: %w", dirPath, err)
	}

	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue // Only one level deep.
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		path := filepath.Join(dirPath, name)
		skill, err := LoadSkillFile(path)
		if err != nil {
			r.logger.Warn("failed to load skill", "path", path, "error", err)
			continue
		}

		// Infer category from subfolder if not set explicitly in YAML.
		// Since ParseSkill defaults to "general", override that with the actual subfolder name.
		if category != "" && (skill.Category == "" || skill.Category == "general") {
			skill.Category = category
		}

		skill.Source = path
		if err := r.Register(skill); err != nil {
			r.logger.Warn("failed to register skill", "path", path, "error", err)
			continue
		}
		loaded++
	}
	return loaded, nil
}

// LoadFromEmbed loads skills from an embedded filesystem (for built-in skills).
func (r *Registry) LoadFromEmbed(fs embed.FS, dir string) error {
	entries, err := fs.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read embedded skills dir: %w", err)
	}

	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		data, err := fs.ReadFile(filepath.Join(dir, name))
		if err != nil {
			r.logger.Warn("failed to read embedded skill", "name", name, "error", err)
			continue
		}

		skill, err := ParseSkill(data)
		if err != nil {
			r.logger.Warn("failed to parse embedded skill", "name", name, "error", err)
			continue
		}

		skill.Source = "builtin"
		if err := r.Register(skill); err != nil {
			r.logger.Warn("failed to register embedded skill", "name", name, "error", err)
			continue
		}
		loaded++
	}

	if loaded > 0 {
		r.logger.Info("loaded built-in skills", "count", loaded)
	}
	return nil
}

// LoadSkillFile reads and parses a single skill YAML file.
func LoadSkillFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseSkill(data)
}

// ParseSkill parses a YAML byte slice into a Skill.
func ParseSkill(data []byte) (*Skill, error) {
	var skill Skill
	if err := yaml.Unmarshal(data, &skill); err != nil {
		return nil, fmt.Errorf("parse skill YAML: %w", err)
	}

	if skill.Name == "" {
		return nil, fmt.Errorf("skill is missing required 'name' field")
	}

	// Default version to 1.
	if skill.Version == 0 {
		skill.Version = 1
	}

	// Default type to prompt.
	if skill.Type == "" {
		skill.Type = TypePrompt
	}

	// Default enabled to true.
	if skill.Enabled == nil {
		skill.Enabled = boolPtr(true)
	}

	// Default category to "general".
	if skill.Category == "" {
		skill.Category = "general"
	}

	// Security validation.
	if err := validateSkillSecurity(&skill); err != nil {
		return nil, fmt.Errorf("skill security check failed: %w", err)
	}

	return &skill, nil
}

// validateSkillSecurity checks a skill for security issues.
func validateSkillSecurity(skill *Skill) error {
	// Check prompt for dangerous patterns.
	dangerousPatterns := []string{
		"curl ", "wget ", "bash -c", "sh -c",
		"eval(", "exec(", "system(",
		"subprocess", "os.system", "import os",
		"child_process", "<script", "javascript:",
	}

	promptLower := strings.ToLower(skill.Prompt)
	for _, pattern := range dangerousPatterns {
		if strings.Contains(promptLower, pattern) {
			return fmt.Errorf("prompt contains dangerous pattern: %s", pattern)
		}
	}

	// Check for excessive prompt length (could hide malicious content).
	if len(skill.Prompt) > 50000 {
		return fmt.Errorf("prompt exceeds maximum length (50KB)")
	}

	// Validate step tool names don't contain shell-like patterns.
	for i, step := range skill.Steps {
		toolLower := strings.ToLower(step.Tool)
		if strings.ContainsAny(toolLower, ";|&$`") {
			return fmt.Errorf("step %d tool name contains invalid characters", i)
		}
		if toolLower == "shell" || toolLower == "exec" || toolLower == "system" {
			return fmt.Errorf("step %d references forbidden tool: %s", i, step.Tool)
		}
	}

	return nil
}
