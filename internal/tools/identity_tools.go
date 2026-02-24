package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// IdentityStore is the interface for reading and writing identity files.
// Implemented by identity.Store to avoid import cycles.
type IdentityStore interface {
	Soul() string
	User() string
	HasUser() bool
	WriteSoul(content string) error
	WriteUser(content string) error
}

// RegisterIdentityTools adds identity_read and identity_update tools to the registry.
// If store is nil, no tools are registered (graceful no-op).
func RegisterIdentityTools(reg *Registry, store IdentityStore) {
	if store == nil {
		return
	}

	reg.Register(&Tool{
		Name:        "identity_read",
		Description: "Read the agent's identity files. Specify 'file' as 'soul' (agent personality/values) or 'user' (info about the human).",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file": {"type": "string", "enum": ["soul", "user"], "description": "Which identity file to read: 'soul' (agent personality) or 'user' (human info)"}
			},
			"required": ["file"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				File string `json:"file"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("identity_read: parse input: %w", err)
			}

			switch params.File {
			case "soul":
				content := store.Soul()
				if content == "" {
					return "SOUL.md is empty. No agent personality has been defined yet.", nil
				}
				return content, nil
			case "user":
				content := store.User()
				if content == "" {
					return "USER.md is empty. No information about the human has been saved yet.", nil
				}
				return content, nil
			default:
				return "", fmt.Errorf("identity_read: invalid file %q, must be 'soul' or 'user'", params.File)
			}
		},
	})

	reg.Register(&Tool{
		Name:        "identity_update",
		Description: "Update the agent's identity files. Specify 'file' as 'soul' or 'user', and 'content' as the full markdown to write. This replaces the entire file.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file": {"type": "string", "enum": ["soul", "user"], "description": "Which identity file to update: 'soul' (agent personality) or 'user' (human info)"},
				"content": {"type": "string", "description": "Full markdown content to write to the identity file"}
			},
			"required": ["file", "content"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				File    string `json:"file"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("identity_update: parse input: %w", err)
			}

			if params.Content == "" {
				return "", fmt.Errorf("identity_update: content is required")
			}

			switch params.File {
			case "soul":
				if err := store.WriteSoul(params.Content); err != nil {
					return "", fmt.Errorf("identity_update: %w", err)
				}
				return "SOUL.md updated successfully. My personality and values have been refreshed.", nil
			case "user":
				if err := store.WriteUser(params.Content); err != nil {
					return "", fmt.Errorf("identity_update: %w", err)
				}
				return "USER.md updated successfully. I've saved what I know about you.", nil
			default:
				return "", fmt.Errorf("identity_update: invalid file %q, must be 'soul' or 'user'", params.File)
			}
		},
	})
}
