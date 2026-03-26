package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// CallMaker is the interface for making phone calls (implemented by the phone adapter).
type CallMaker interface {
	MakeCall(ctx context.Context, toNumber, greeting string) (string, error)
}

// RegisterCallTools registers call_make for initiating outbound phone calls.
func RegisterCallTools(reg *Registry, caller CallMaker) {
	reg.Register(&Tool{
		Name:        "call_make",
		Description: "Make an outbound phone call to any number. Use this when the user asks you to call someone, or when a workflow triggers a call. The call will be a live two-way conversation using the user's configured voice.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["to"],
			"properties": {
				"to": {
					"type": "string",
					"description": "Phone number to call in E.164 format (e.g. +12025551234)"
				},
				"greeting": {
					"type": "string",
					"description": "Optional opening line spoken when the call connects (e.g. 'Hi, this is Crayfish calling on behalf of Chuks...')"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, params json.RawMessage) (string, error) {
			var input struct {
				To       string `json:"to"`
				Greeting string `json:"greeting"`
			}
			if err := json.Unmarshal(params, &input); err != nil {
				return "", fmt.Errorf("invalid parameters: %w", err)
			}
			if input.To == "" {
				return "", fmt.Errorf("phone number required")
			}

			callSid, err := caller.MakeCall(ctx, input.To, input.Greeting)
			if err != nil {
				return "", fmt.Errorf("call failed: %w", err)
			}

			return fmt.Sprintf("Call initiated to %s (SID: %s). The call is connecting — they'll hear your voice shortly.", input.To, callSid), nil
		},
	})
}
