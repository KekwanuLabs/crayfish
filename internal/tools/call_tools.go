package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// CallMaker is the interface for making phone calls (implemented by the phone adapter).
type CallMaker interface {
	MakeCall(ctx context.Context, toNumber, contactName, callerName, purpose, opening string) (string, error)
}

// RegisterCallTools registers call_make for initiating outbound phone calls.
func RegisterCallTools(reg *Registry, caller CallMaker) {
	reg.Register(&Tool{
		Name: "call_make",
		Description: `Make an outbound phone call on the user's behalf.

Use this when the user asks you to call someone — e.g. "call my wife and tell her I'll be late", "call the restaurant and make a reservation", "call Mom and wish her happy birthday".

Before calling:
1. If the user said "call my wife/mom/friend/boss" (a relationship, not a number), search memory for their phone number first. If you don't have it, ask the user: "What's [person]'s phone number? I'll save it for next time."
2. Build a natural opening — the first thing you'll say when they pick up.
3. Make the call with full context so the phone agent knows what to say.

The call is a live two-way conversation — the person can respond and ask questions.`,
		MinTier: security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["to"],
			"properties": {
				"to": {
					"type": "string",
					"description": "Phone number in E.164 format (e.g. +12025551234). Look up from memory if you know the contact name."
				},
				"contact_name": {
					"type": "string",
					"description": "Name of the person being called (e.g. 'Sarah', 'Mom'). Used to personalize the conversation."
				},
				"caller_name": {
					"type": "string",
					"description": "Name of the person on whose behalf we're calling (usually the user's name from their profile)."
				},
				"purpose": {
					"type": "string",
					"description": "Why we're calling — what message or task to complete. Be specific: 'tell her Chuks will be 30 minutes late coming home', not just 'message'."
				},
				"opening": {
					"type": "string",
					"description": "Optional: exact first sentence to say when the call connects. If not provided, one is generated from contact_name and caller_name."
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, params json.RawMessage) (string, error) {
			var input struct {
				To          string `json:"to"`
				ContactName string `json:"contact_name"`
				CallerName  string `json:"caller_name"`
				Purpose     string `json:"purpose"`
				Opening     string `json:"opening"`
			}
			if err := json.Unmarshal(params, &input); err != nil {
				return "", fmt.Errorf("invalid parameters: %w", err)
			}
			if input.To == "" {
				return "", fmt.Errorf("phone number is required — look it up from memory first, or ask the user")
			}

			callSid, err := caller.MakeCall(ctx, input.To,
				input.ContactName, input.CallerName, input.Purpose, input.Opening)
			if err != nil {
				return "", fmt.Errorf("call failed: %w", err)
			}

			who := input.To
			if input.ContactName != "" {
				who = input.ContactName + " (" + input.To + ")"
			}
			return fmt.Sprintf("Calling %s now. The call is connecting — they'll hear from me shortly.\nCall ID: %s", who, callSid), nil
		},
	})
}
