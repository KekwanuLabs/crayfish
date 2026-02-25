package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KekwanuLabs/crayfish/internal/security"
	"github.com/KekwanuLabs/crayfish/internal/skills"
)

// SkillToolDeps holds dependencies for the conversational skill management tools.
type SkillToolDeps struct {
	Registry  *skills.Registry
	SkillsDir string
	Hub       *skills.HubClient // nil-safe; hub tools degrade gracefully if unreachable.
}

// RegisterSkillTools adds conversational skill management tools to the registry.
// These let users discover, inspect, install, and remove skills via conversation.
func RegisterSkillTools(reg *Registry, deps SkillToolDeps) {
	reg.logger.Info("registering skill management tools")

	// skill_list — list installed skills with plain-language descriptions.
	reg.Register(&Tool{
		Name:        "skill_list",
		Description: "List all installed skills with descriptions and triggers. Optionally filter by category (type). Present the results conversationally.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"category": {
					"type": "string",
					"description": "Optional filter by skill type: 'workflow', 'prompt', or 'reactive'"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Category string `json:"category"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("skill_list: parse input: %w", err)
			}

			allSkills := deps.Registry.All()
			if len(allSkills) == 0 {
				return "No skills are installed yet. You can browse the Skill Hub to discover new ones — just ask me to show you what's available.", nil
			}

			var filtered []*skills.Skill
			for _, s := range allSkills {
				if params.Category != "" && string(s.Type) != params.Category {
					continue
				}
				filtered = append(filtered, s)
			}

			if len(filtered) == 0 {
				return fmt.Sprintf("No skills found with type %q. You have %d skills of other types.", params.Category, len(allSkills)), nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("You have %d skill(s) installed:\n\n", len(filtered)))

			for _, s := range filtered {
				enabled := "enabled"
				if s.Enabled != nil && !*s.Enabled {
					enabled = "DISABLED"
				}

				sb.WriteString(fmt.Sprintf("- **%s** (%s, %s)", s.Name, s.Type, enabled))
				if s.Description != "" {
					sb.WriteString(": " + s.Description)
				}
				sb.WriteString("\n")

				// Human-readable trigger info.
				if s.Trigger.Command != "" {
					sb.WriteString(fmt.Sprintf("  Trigger: command `%s`\n", s.Trigger.Command))
				}
				if s.Trigger.Schedule != "" {
					sb.WriteString(fmt.Sprintf("  Trigger: %s\n", skills.CronToHuman(s.Trigger.Schedule)))
				}
				if s.Trigger.Event != "" {
					sb.WriteString(fmt.Sprintf("  Trigger: on event `%s`\n", s.Trigger.Event))
				}
				if len(s.Trigger.Keywords) > 0 {
					sb.WriteString(fmt.Sprintf("  Keywords: %s\n", strings.Join(s.Trigger.Keywords, ", ")))
				}
			}

			return sb.String(), nil
		},
	})

	// skill_info — detailed info about a single skill.
	reg.Register(&Tool{
		Name:        "skill_info",
		Description: "Get detailed information about a specific installed skill. Returns description, triggers, tools used, and source.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "The name of the skill to inspect"
				}
			},
			"required": ["name"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("skill_info: parse input: %w", err)
			}
			if params.Name == "" {
				return "", fmt.Errorf("skill_info: name is required")
			}

			skill := deps.Registry.Get(params.Name)
			if skill == nil {
				return fmt.Sprintf("No skill named %q is installed.", params.Name), nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("**%s** (v%d)\n", skill.Name, skill.Version))
			if skill.Author != "" {
				sb.WriteString(fmt.Sprintf("Author: %s\n", skill.Author))
			}
			sb.WriteString(fmt.Sprintf("Type: %s\n", skill.Type))

			enabled := "Yes"
			if skill.Enabled != nil && !*skill.Enabled {
				enabled = "No (disabled)"
			}
			sb.WriteString(fmt.Sprintf("Enabled: %s\n", enabled))
			sb.WriteString(fmt.Sprintf("Source: %s\n", skill.Source))

			if skill.Description != "" {
				sb.WriteString(fmt.Sprintf("\nDescription: %s\n", skill.Description))
			}

			// Triggers
			sb.WriteString("\nTriggers:\n")
			hasTrigger := false
			if skill.Trigger.Command != "" {
				sb.WriteString(fmt.Sprintf("  Command: %s\n", skill.Trigger.Command))
				hasTrigger = true
			}
			if skill.Trigger.Schedule != "" {
				sb.WriteString(fmt.Sprintf("  Schedule: %s (%s)\n", skills.CronToHuman(skill.Trigger.Schedule), skill.Trigger.Schedule))
				hasTrigger = true
			}
			if skill.Trigger.Event != "" {
				sb.WriteString(fmt.Sprintf("  Event: %s\n", skill.Trigger.Event))
				hasTrigger = true
			}
			if len(skill.Trigger.Keywords) > 0 {
				sb.WriteString(fmt.Sprintf("  Keywords: %s\n", strings.Join(skill.Trigger.Keywords, ", ")))
				hasTrigger = true
			}
			if !hasTrigger {
				sb.WriteString("  None configured\n")
			}

			// Tools used
			if len(skill.Steps) > 0 {
				sb.WriteString("\nTools used:\n")
				for i, step := range skill.Steps {
					sb.WriteString(fmt.Sprintf("  %d. %s", i+1, step.Tool))
					if step.StoreAs != "" {
						sb.WriteString(fmt.Sprintf(" (saves as %q)", step.StoreAs))
					}
					sb.WriteString("\n")
				}
			}

			return sb.String(), nil
		},
	})

	// skill_install — install a skill from URL or hub by name.
	reg.Register(&Tool{
		Name:        "skill_install",
		Description: "Install a skill from a URL (YAML file) or from the Skill Hub by name. Downloads, validates, and registers the skill.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {
					"type": "string",
					"description": "Direct URL to a skill YAML file"
				},
				"name": {
					"type": "string",
					"description": "Name of a skill in the Skill Hub to install"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				URL  string `json:"url"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("skill_install: parse input: %w", err)
			}

			if params.URL == "" && params.Name == "" {
				return "", fmt.Errorf("skill_install: provide either 'url' or 'name'")
			}

			var skill *skills.Skill
			var err error

			if params.URL != "" {
				// Direct URL install.
				if deps.Hub == nil {
					return "Skill installation from URLs is not available.", nil
				}
				skill, err = deps.Hub.FetchSkill(ctx, params.URL)
				if err != nil {
					return fmt.Sprintf("Failed to fetch skill from URL: %v", err), nil
				}
			} else {
				// Hub lookup by name.
				if deps.Hub == nil {
					return "The Skill Hub is not configured.", nil
				}
				hubSkill, err := deps.Hub.FindByName(ctx, params.Name)
				if err != nil {
					return fmt.Sprintf("Could not find %q in the Skill Hub: %v", params.Name, err), nil
				}
				skill, err = deps.Hub.FetchSkill(ctx, hubSkill.URL)
				if err != nil {
					return fmt.Sprintf("Failed to download skill %q: %v", params.Name, err), nil
				}
			}

			// Validate via security checks.
			var toolSteps []struct{ Tool string }
			for _, s := range skill.Steps {
				toolSteps = append(toolSteps, struct{ Tool string }{Tool: s.Tool})
			}
			validation := security.ValidateSkill(skill.Name, skill.Prompt, toolSteps)
			if !validation.Safe {
				return "Skill rejected for safety: " + strings.Join(validation.Errors, "; "), nil
			}

			// Register in memory.
			skill.Source = "hub"
			if err := deps.Registry.Register(skill); err != nil {
				return fmt.Sprintf("Failed to register skill: %v", err), nil
			}

			// Save to file for persistence.
			if deps.SkillsDir != "" {
				if err := deps.Registry.SaveToFile(skill, deps.SkillsDir); err != nil {
					reg.logger.Warn("failed to save installed skill to file", "skill", skill.Name, "error", err)
				}
			}

			return fmt.Sprintf("Skill %q installed successfully! %s", skill.Name, skill.Description), nil
		},
	})

	// skill_remove — remove a user-installed skill.
	reg.Register(&Tool{
		Name:        "skill_remove",
		Description: "Remove a user-installed skill. Built-in skills cannot be removed.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Name of the skill to remove"
				}
			},
			"required": ["name"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("skill_remove: parse input: %w", err)
			}
			if params.Name == "" {
				return "", fmt.Errorf("skill_remove: name is required")
			}

			skill := deps.Registry.Get(params.Name)
			if skill == nil {
				return fmt.Sprintf("No skill named %q is installed.", params.Name), nil
			}

			if skill.Source == "builtin" {
				return fmt.Sprintf("Cannot remove %q — it's a built-in skill. You can disable it instead.", params.Name), nil
			}

			if !deps.Registry.Delete(params.Name) {
				return fmt.Sprintf("Failed to remove skill %q.", params.Name), nil
			}

			if deps.SkillsDir != "" {
				deps.Registry.DeleteFile(params.Name, deps.SkillsDir)
			}

			return fmt.Sprintf("Skill %q has been removed.", params.Name), nil
		},
	})

	// skill_hub_browse — browse the remote Skill Hub.
	reg.Register(&Tool{
		Name:        "skill_hub_browse",
		Description: "Browse the Skill Hub to discover new skills. Optionally search by keyword. Present results conversationally with descriptions.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"search": {
					"type": "string",
					"description": "Optional search query to filter hub skills"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Search string `json:"search"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("skill_hub_browse: parse input: %w", err)
			}

			if deps.Hub == nil {
				return "The Skill Hub is not available right now.", nil
			}

			results, err := deps.Hub.Search(ctx, params.Search)
			if err != nil {
				return fmt.Sprintf("Could not reach the Skill Hub: %v", err), nil
			}

			if len(results) == 0 {
				if params.Search != "" {
					return fmt.Sprintf("No skills found in the hub matching %q.", params.Search), nil
				}
				return "The Skill Hub is empty right now. Check back later.", nil
			}

			// Check which are already installed.
			installed := make(map[string]bool)
			for _, s := range deps.Registry.All() {
				installed[strings.ToLower(s.Name)] = true
			}

			var sb strings.Builder
			if params.Search != "" {
				sb.WriteString(fmt.Sprintf("Found %d skill(s) in the Skill Hub matching %q:\n\n", len(results), params.Search))
			} else {
				sb.WriteString(fmt.Sprintf("The Skill Hub has %d skill(s) available:\n\n", len(results)))
			}

			for _, s := range results {
				status := ""
				if installed[strings.ToLower(s.Name)] {
					status = " [installed]"
				}
				sb.WriteString(fmt.Sprintf("- **%s**%s: %s\n", s.Name, status, s.Description))
				if len(s.Tags) > 0 {
					sb.WriteString(fmt.Sprintf("  Tags: %s\n", strings.Join(s.Tags, ", ")))
				}
				if len(s.Requires) > 0 {
					sb.WriteString(fmt.Sprintf("  Requires: %s\n", strings.Join(s.Requires, ", ")))
				}
			}

			sb.WriteString("\nTo install a skill, just say \"install [skill name]\" and I'll set it up for you.")

			return sb.String(), nil
		},
	})
}
