// Package agents provides autonomous agent components for Crayfish.
// Each agent handles a specific domain (proactive suggestions, evaluation, etc.)
// and communicates via typed messages.
package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/KekwanuLabs/crayfish/internal/runtime"
)

// Opportunity represents a proactive suggestion that can be evaluated.
type Opportunity struct {
	ID          string  `json:"id"`
	UserID      string  `json:"user_id,omitempty"`
	Type        string  `json:"type"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	RelatedTo   string  `json:"related_to,omitempty"`
	Confidence  float64 `json:"confidence"`
	CreatedAt   string  `json:"created_at,omitempty"`
}

// OpportunityEvaluation holds the LLM's assessment of an opportunity.
type OpportunityEvaluation struct {
	Verdict          string  `json:"verdict"`           // surface, skip, delay
	Confidence       float64 `json:"confidence"`        // 0.0-1.0 adjusted
	Relevance        float64 `json:"relevance"`         // 0.0-1.0
	Timing           float64 `json:"timing"`            // 0.0-1.0
	Quality          float64 `json:"quality"`           // 0.0-1.0
	Reason           string  `json:"reason"`            // Brief explanation
	SuggestedMessage string  `json:"suggested_message"` // Optionally rewritten message
}

// Message represents an inter-agent message with a typed payload.
type Message struct {
	Type      string          `json:"type"`
	UserID    string          `json:"user_id"`
	SessionID string          `json:"session_id"`
	Payload   json.RawMessage `json:"payload"`
}

// UnmarshalPayload decodes the message payload into the provided struct.
func (m *Message) UnmarshalPayload(v any) error {
	return json.Unmarshal(m.Payload, v)
}

// Response is the result of processing an agent message.
type Response struct {
	Success bool           `json:"success"`
	Error   string         `json:"error,omitempty"`
	State   map[string]any `json:"state,omitempty"`
}

// NewErrorResponse creates a failed response from an error.
func NewErrorResponse(err error) *Response {
	return &Response{
		Success: false,
		Error:   err.Error(),
	}
}

// ProactiveAgent evaluates opportunities for proactive user engagement.
// It uses the LLM to assess whether a suggestion is worth surfacing,
// considering relevance, timing, and quality against the user's context.
type ProactiveAgent struct {
	memory      *runtime.MemoryRetriever
	db          *sql.DB
	llmComplete func(ctx context.Context, system, user string) (string, error)
	logger      *slog.Logger
}

// ProactiveAgentDeps holds dependencies for the proactive agent.
type ProactiveAgentDeps struct {
	Memory      *runtime.MemoryRetriever
	DB          *sql.DB
	LLMComplete func(ctx context.Context, system, user string) (string, error)
	Logger      *slog.Logger
}

// NewProactiveAgent creates a new proactive agent.
func NewProactiveAgent(deps ProactiveAgentDeps) *ProactiveAgent {
	return &ProactiveAgent{
		memory:      deps.Memory,
		db:          deps.DB,
		llmComplete: deps.LLMComplete,
		logger:      deps.Logger,
	}
}

// HandleEvaluateOpportunity evaluates a proactive opportunity using LLM assessment.
// It loads the opportunity (from payload or DB), retrieves user context from memory,
// and uses the LLM to assess relevance, timing, and quality.
//
// Payload accepts two forms:
//
//	{"opportunity_id": "uuid-string"}           — loads from suggestions table
//	{"opportunity": {<inline opportunity>}}     — evaluates directly
func (a *ProactiveAgent) HandleEvaluateOpportunity(ctx context.Context, msg *Message) (*Response, error) {
	var payload struct {
		OpportunityID string       `json:"opportunity_id"`
		Opportunity   *Opportunity `json:"opportunity"`
	}
	if err := msg.UnmarshalPayload(&payload); err != nil {
		return NewErrorResponse(err), nil
	}

	opp := payload.Opportunity

	// Load from DB if only ID provided.
	if opp == nil && payload.OpportunityID != "" {
		loaded, err := a.loadOpportunityFromDB(ctx, payload.OpportunityID)
		if err != nil {
			return NewErrorResponse(fmt.Errorf("failed to load opportunity: %w", err)), nil
		}
		opp = loaded
	}

	if opp == nil {
		return NewErrorResponse(fmt.Errorf("no opportunity provided or found")), nil
	}

	// Load user context for evaluation.
	var userContext string
	if a.memory != nil && msg.SessionID != "" {
		query := opp.Type
		if opp.RelatedTo != "" {
			query = opp.RelatedTo + " " + opp.Type
		}
		memories, err := a.memory.RetrieveRelevant(ctx, msg.SessionID, query, 10)
		if err == nil && len(memories) > 0 {
			userContext = a.memory.FormatForContext(memories)
		}
	}

	// Evaluate with LLM.
	evaluation, err := a.evaluateWithLLM(ctx, opp, userContext)
	if err != nil {
		a.logger.Warn("LLM evaluation failed for opportunity", "id", opp.ID, "error", err)
		// Fail open — return the opportunity as-is with original confidence.
		return &Response{
			Success: true,
			State: map[string]any{
				"evaluated":   true,
				"verdict":     "surface",
				"confidence":  opp.Confidence,
				"reason":      "evaluation failed, using original confidence",
				"opportunity": opp,
			},
		}, nil
	}

	return &Response{
		Success: true,
		State: map[string]any{
			"evaluated":           true,
			"verdict":             evaluation.Verdict,
			"adjusted_confidence": evaluation.Confidence,
			"reason":              evaluation.Reason,
			"relevance":           evaluation.Relevance,
			"timing":              evaluation.Timing,
			"quality":             evaluation.Quality,
			"suggested_message":   evaluation.SuggestedMessage,
			"opportunity":         opp,
		},
	}, nil
}

// loadOpportunityFromDB loads an opportunity from the suggestions table by ID.
func (a *ProactiveAgent) loadOpportunityFromDB(ctx context.Context, id string) (*Opportunity, error) {
	var opp Opportunity
	var content string
	err := a.db.QueryRowContext(ctx,
		`SELECT id, user_id, type, content, confidence, created_at
		FROM suggestions WHERE id = ?`, id,
	).Scan(&opp.ID, &opp.UserID, &opp.Type, &content, &opp.Confidence, &opp.CreatedAt)
	if err != nil {
		return nil, err
	}
	opp.Description = content
	return &opp, nil
}

// evaluateWithLLM uses the LLM to assess an opportunity's value, timing, and relevance.
func (a *ProactiveAgent) evaluateWithLLM(ctx context.Context, opp *Opportunity, userContext string) (*OpportunityEvaluation, error) {
	systemPrompt := `You evaluate proactive suggestions for a personal AI assistant.
Given an opportunity and user context, assess whether this is worth surfacing to the user.

Score each dimension 0.0-1.0:
- relevance: How well does this align with the user's goals, interests, or needs?
- timing: Is now a good time? Is there urgency or a reason to wait?
- quality: Is this specific and actionable, or vague/generic?

Then decide:
- "surface": Worth sending to the user now (relevance >= 0.6 AND quality >= 0.5)
- "skip": Not valuable enough to interrupt (low relevance or very low quality)
- "delay": Good opportunity but timing isn't right

If verdict is "surface", optionally provide a suggested_message — a natural, concise way to present this to the user.

Return ONLY valid JSON:
{
  "verdict": "surface|skip|delay",
  "confidence": 0.0-1.0,
  "relevance": 0.0-1.0,
  "timing": 0.0-1.0,
  "quality": 0.0-1.0,
  "reason": "1-2 sentence explanation",
  "suggested_message": "optional rewritten message or empty string"
}`

	userMsg := fmt.Sprintf("OPPORTUNITY:\nType: %s\nTitle: %s\nDescription: %s\nRelated to: %s\nOriginal confidence: %.2f\n\nUSER CONTEXT:\n%s",
		opp.Type, opp.Title, opp.Description, opp.RelatedTo, opp.Confidence, userContext)

	response, err := a.llmComplete(ctx, systemPrompt, userMsg)
	if err != nil {
		return nil, err
	}

	// Parse JSON response (strip markdown code blocks if present).
	body := strings.TrimSpace(response)
	body = strings.TrimPrefix(body, "```json")
	body = strings.TrimPrefix(body, "```")
	body = strings.TrimSuffix(body, "```")
	body = strings.TrimSpace(body)

	var eval OpportunityEvaluation
	if err := json.Unmarshal([]byte(body), &eval); err != nil {
		// Try bracket extraction fallback.
		start := strings.Index(body, "{")
		end := strings.LastIndex(body, "}")
		if start >= 0 && end > start {
			if err := json.Unmarshal([]byte(body[start:end+1]), &eval); err != nil {
				return nil, fmt.Errorf("failed to parse evaluation: %w", err)
			}
		} else {
			return nil, fmt.Errorf("no valid JSON in evaluation response")
		}
	}

	// Validate verdict.
	switch eval.Verdict {
	case "surface", "skip", "delay":
		// valid
	default:
		eval.Verdict = "surface" // fail open
	}

	return &eval, nil
}
