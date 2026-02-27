package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// OpportunityEvaluator evaluates proactive opportunities.
// Implemented by agents.ProactiveAgent to avoid import cycles.
type OpportunityEvaluator interface {
	EvaluateOpportunityRaw(ctx context.Context, sessionID string, oppType, title, description, relatedTo string, confidence float64) (json.RawMessage, error)
}

// RegisterProactiveTools adds the evaluate_opportunity tool to the registry.
// If evaluator is nil, no tools are registered (graceful no-op).
func RegisterProactiveTools(reg *Registry, evaluator OpportunityEvaluator) {
	if evaluator == nil {
		return
	}

	reg.Register(&Tool{
		Name:        "evaluate_opportunity",
		Description: "Evaluate a proactive opportunity using LLM assessment. Determines whether a suggestion is worth surfacing to the user based on relevance, timing, and quality.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"type": {"type": "string", "description": "Category of the opportunity (e.g. email_highlight, networking, reminder)"},
				"title": {"type": "string", "description": "Short title of the opportunity"},
				"description": {"type": "string", "description": "Detailed description of the opportunity"},
				"related_to": {"type": "string", "description": "What this opportunity relates to (optional context)"},
				"confidence": {"type": "number", "description": "Initial confidence score 0.0-1.0 (default 0.5)"}
			},
			"required": ["type", "title", "description"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Type        string  `json:"type"`
				Title       string  `json:"title"`
				Description string  `json:"description"`
				RelatedTo   string  `json:"related_to"`
				Confidence  float64 `json:"confidence"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("evaluate_opportunity: parse input: %w", err)
			}

			if params.Confidence == 0 {
				params.Confidence = 0.5
			}

			sessionID := ""
			if sess != nil {
				sessionID = sess.ID
			}

			result, err := evaluator.EvaluateOpportunityRaw(ctx, sessionID, params.Type, params.Title, params.Description, params.RelatedTo, params.Confidence)
			if err != nil {
				return "", fmt.Errorf("evaluate_opportunity: %w", err)
			}

			return string(result), nil
		},
	})
}
