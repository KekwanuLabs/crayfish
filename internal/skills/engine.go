package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ToolExecutor is the interface for executing tools. The runtime implements this.
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, toolName string, input json.RawMessage) (string, error)
}

// Engine executes skills by running their tool chains and assembling prompts.
type Engine struct {
	registry *Registry
	logger   *slog.Logger
}

// NewEngine creates a skill execution engine.
func NewEngine(registry *Registry, logger *slog.Logger) *Engine {
	return &Engine{
		registry: registry,
		logger:   logger,
	}
}

// ExecuteWorkflow runs a workflow skill's steps and returns the assembled result.
func (e *Engine) ExecuteWorkflow(ctx context.Context, skill *Skill, executor ToolExecutor, vars map[string]string) (*SkillResult, error) {
	start := time.Now()

	result := &SkillResult{
		SkillName:   skill.Name,
		StepResults: make(map[string]string),
	}

	// Merge default vars with provided vars (provided take precedence).
	mergedVars := make(map[string]string)
	for k, v := range skill.Vars {
		mergedVars[k] = v
	}
	for k, v := range vars {
		mergedVars[k] = v
	}

	// Execute each step.
	for i, step := range skill.Steps {
		e.logger.Debug("executing skill step",
			"skill", skill.Name, "step", i, "tool", step.Tool)

		// Interpolate params with current variables.
		inputJSON, err := e.interpolateParams(step.Params, mergedVars)
		if err != nil {
			if step.OnError == "abort" {
				result.Error = fmt.Sprintf("step %d (%s): param interpolation: %v", i, step.Tool, err)
				result.Duration = time.Since(start)
				return result, fmt.Errorf("skill %s step %d: %w", skill.Name, i, err)
			}
			e.logger.Warn("skill step param error, skipping",
				"skill", skill.Name, "step", i, "error", err)
			continue
		}

		// Execute the tool.
		toolResult, err := executor.ExecuteTool(ctx, step.Tool, inputJSON)
		if err != nil {
			switch step.OnError {
			case "abort":
				result.Error = fmt.Sprintf("step %d (%s): %v", i, step.Tool, err)
				result.Duration = time.Since(start)
				return result, fmt.Errorf("skill %s step %d: %w", skill.Name, i, err)
			default: // "skip" or empty
				e.logger.Warn("skill step failed, skipping",
					"skill", skill.Name, "step", i, "tool", step.Tool, "error", err)
				if step.StoreAs != "" {
					mergedVars[step.StoreAs] = fmt.Sprintf("(error: %v)", err)
				}
				continue
			}
		}

		// Store result for use in subsequent steps or the final prompt.
		if step.StoreAs != "" {
			mergedVars[step.StoreAs] = toolResult
			result.StepResults[step.StoreAs] = toolResult
		}
	}

	// Assemble the final prompt with all collected results.
	if skill.Prompt != "" {
		result.FinalPrompt = interpolateTemplate(skill.Prompt, mergedVars)
	}

	result.Success = true
	result.Duration = time.Since(start)

	e.logger.Info("skill executed",
		"skill", skill.Name, "steps", len(skill.Steps),
		"duration_ms", result.Duration.Milliseconds())

	return result, nil
}

// MatchAndExecute checks if the message matches a workflow skill (by command or keyword),
// executes the workflow, and returns the assembled result. Returns nil if no skill matches.
// This implements the runtime.SkillRunner interface.
func (e *Engine) MatchAndExecute(ctx context.Context, text string, executor ToolExecutor) (*MatchResult, error) {
	trimmed := strings.TrimSpace(text)

	var skill *Skill

	// 1. Check for /command triggers.
	if strings.HasPrefix(trimmed, "/") {
		parts := strings.SplitN(trimmed, " ", 2)
		cmd := parts[0]
		skill = e.registry.FindByCommand(cmd)
	}

	// 2. Fall back to keyword matching (first match wins).
	if skill == nil {
		matches := e.registry.FindByKeyword(trimmed)
		for _, m := range matches {
			if m.Type == TypeWorkflow {
				skill = m
				break
			}
		}
	}

	// No workflow skill matched.
	if skill == nil || skill.Type != TypeWorkflow {
		return nil, nil
	}

	// Extract variables from the message text.
	vars := extractVars(trimmed, skill)

	e.logger.Info("executing matched skill",
		"skill", skill.Name, "destination", vars["destination"])

	result, err := e.ExecuteWorkflow(ctx, skill, executor, vars)
	if err != nil {
		return nil, fmt.Errorf("execute skill %s: %w", skill.Name, err)
	}

	return &MatchResult{
		SkillName:   result.SkillName,
		FinalPrompt: result.FinalPrompt,
		Success:     result.Success,
	}, nil
}

// MatchResult is returned by MatchAndExecute with the skill execution outcome.
type MatchResult struct {
	SkillName   string
	FinalPrompt string
	Success     bool
}

// extractVars pulls variable values from the message text for a given skill.
func extractVars(text string, skill *Skill) map[string]string {
	vars := make(map[string]string)

	// Extract destination for trip-related skills.
	if _, hasDest := skill.Vars["destination"]; hasDest {
		if dest := extractDestination(text); dest != "" {
			vars["destination"] = dest
		}
	}

	return vars
}

// extractDestination parses destination from patterns like "trip to Hawaii",
// "travel to Japan", "visit Paris", "/trip Hawaii".
func extractDestination(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))

	// Handle /command form: "/trip Hawaii" or "/itinerary Paris".
	if strings.HasPrefix(lower, "/") {
		parts := strings.SplitN(lower, " ", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[1])
		}
		return ""
	}

	// Match patterns: "trip to X", "travel to X", "visit X", "vacation in X", "vacation to X".
	patterns := []string{
		"trip to ", "travel to ", "visit ", "vacation to ",
		"vacation in ", "fly to ", "going to ",
	}
	for _, p := range patterns {
		if idx := strings.Index(lower, p); idx != -1 {
			dest := text[idx+len(p):]
			// Trim trailing punctuation and common suffixes.
			dest = strings.TrimRight(dest, ".!?,;")
			dest = strings.TrimSpace(dest)
			// Take only the meaningful portion (stop at common filler words).
			for _, stop := range []string{" for ", " with ", " this ", " next ", " in the "} {
				if si := strings.Index(strings.ToLower(dest), stop); si != -1 {
					dest = dest[:si]
				}
			}
			if dest != "" {
				return strings.TrimSpace(dest)
			}
		}
	}

	return ""
}

// GetPromptAugmentations returns all prompt augmentations from enabled prompt-type skills.
// This implements the runtime.SkillRunner interface.
func (e *Engine) GetPromptAugmentations() []string {
	var augmentations []string
	for _, skill := range e.registry.All() {
		if skill.Type == TypePrompt && isEnabled(skill) && skill.Prompt != "" {
			aug := e.BuildPromptAugmentation(skill, nil)
			if aug != "" {
				augmentations = append(augmentations, aug)
			}
		}
	}
	return augmentations
}

// BuildPromptAugmentation returns the prompt text for a prompt-type skill.
func (e *Engine) BuildPromptAugmentation(skill *Skill, vars map[string]string) string {
	mergedVars := make(map[string]string)
	for k, v := range skill.Vars {
		mergedVars[k] = v
	}
	for k, v := range vars {
		mergedVars[k] = v
	}
	return interpolateTemplate(skill.Prompt, mergedVars)
}

// interpolateParams converts step params with {{var}} interpolation into JSON.
func (e *Engine) interpolateParams(params map[string]interface{}, vars map[string]string) (json.RawMessage, error) {
	if params == nil {
		return json.RawMessage(`{}`), nil
	}

	// Deep-interpolate string values in the params map.
	interpolated := make(map[string]interface{}, len(params))
	for k, v := range params {
		switch val := v.(type) {
		case string:
			interpolated[k] = interpolateTemplate(val, vars)
		default:
			interpolated[k] = v
		}
	}

	data, err := json.Marshal(interpolated)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	return json.RawMessage(data), nil
}

// interpolateTemplate replaces {{variable}} placeholders in a template string.
func interpolateTemplate(tmpl string, vars map[string]string) string {
	result := tmpl
	for k, v := range vars {
		placeholder := "{{" + k + "}}"
		result = strings.ReplaceAll(result, placeholder, v)
	}
	return result
}
