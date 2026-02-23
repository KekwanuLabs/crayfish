package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// CheckpointSaver is the interface for saving manual session checkpoints.
// Implemented by runtime.SnapshotManager to avoid import cycles.
type CheckpointSaver interface {
	SaveCheckpoint(ctx context.Context, sessionID string, input json.RawMessage) error
}

// RegisterCheckpointTool adds the save_checkpoint tool to the registry.
func RegisterCheckpointTool(reg *Registry, db *sql.DB, saver CheckpointSaver) {
	reg.Register(&Tool{
		Name:        "save_checkpoint",
		Description: "Save a session checkpoint to preserve conversational state. Use when the user says 'checkpoint', 'save state', or 'save progress'. Specify 'active_task' (required) and optionally other state fields.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"active_task": {"type": "string", "description": "What we are currently working on"},
				"active_task_context": {"type": "string", "description": "Why and how — not just what"},
				"pending_proposals": {"type": "array", "items": {"type": "string"}, "description": "Anything said but not yet addressed"},
				"decisions_in_flight": {"type": "array", "items": {"type": "string"}, "description": "Topics discussed but not yet decided"},
				"conversational_tone": {"type": "string", "description": "User's emotional state in 1-2 words"},
				"key_resources": {"type": "array", "items": {"type": "string"}, "description": "Files, URLs, topics currently in play"}
			},
			"required": ["active_task"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				ActiveTask string `json:"active_task"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("save_checkpoint: parse input: %w", err)
			}

			if params.ActiveTask == "" {
				return "", fmt.Errorf("save_checkpoint: active_task is required")
			}

			if err := saver.SaveCheckpoint(ctx, sess.ID, input); err != nil {
				return "", fmt.Errorf("save_checkpoint: %w", err)
			}

			return fmt.Sprintf("Session checkpoint saved. Active task: %s", params.ActiveTask), nil
		},
	})
}
