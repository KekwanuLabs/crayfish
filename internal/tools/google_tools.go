package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/oauth"
	"github.com/KekwanuLabs/crayfish/internal/security"
)

// GoogleToolsDeps holds the dependencies for Google OAuth tools.
type GoogleToolsDeps struct {
	OAuthClient     *oauth.Client
	GetToken        func() *oauth.Token // Returns current token (may be nil)
	OnTokenReceived func(oauth.Token)   // Called when device flow completes
	IsConnected     func() bool         // Check if Google is connected
	GetScopes       func() []string     // Get currently granted scopes
}

// RegisterGoogleTools adds google_connect and google_status tools to the registry.
// These are always registered (don't require existing auth) so the agent can
// offer to connect Google when the user asks about calendar or email.
func RegisterGoogleTools(reg *Registry, deps GoogleToolsDeps) {
	reg.logger.Info("registering Google OAuth tools")

	// google_status — check whether Google is connected and what scopes are granted.
	reg.Register(&Tool{
		Name:        "google_status",
		Description: "Check whether a Google account is connected and what permissions are granted (calendar, email, drive, docs, sheets, etc).",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			connected := deps.IsConnected()
			scopes := deps.GetScopes()

			result := map[string]any{
				"connected": connected,
			}

			if connected && len(scopes) > 0 {
				var friendly []string
				for _, s := range scopes {
					friendly = append(friendly, oauth.FriendlyScope(s))
				}
				result["scopes"] = friendly

				// Show what capabilities could be added.
				var upgradable []string
				for purpose, purposeScopes := range oauth.ScopesByPurpose {
					if !hasAllScopes(scopes, purposeScopes) {
						upgradable = append(upgradable, purpose)
					}
				}
				if len(upgradable) > 0 {
					result["available_upgrades"] = upgradable
				}
			}

			out, _ := json.Marshal(result)
			return string(out), nil
		},
	})

	// google_connect — initiate the device authorization flow.
	// Supports both initial connection and scope upgrades.
	// When called with a "purpose" (e.g. "drive", "docs", "sheets"), it re-runs
	// the device flow with expanded scopes. Google's consent screen shows only
	// the new permissions the user hasn't yet granted.
	reg.Register(&Tool{
		Name: "google_connect",
		Description: `Connect or upgrade a Google account using a device code. The user visits google.com/device on their phone and enters the code shown.

Without a purpose: enables calendar and email (base scopes).
With a purpose ("drive", "docs", "sheets"): adds that capability to the existing connection. Same quick code process — Google only asks for the new permissions.`,
		MinTier: security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"purpose": {
					"type": "string",
					"description": "Optional: what capability to add. One of: drive, docs, sheets. Omit for initial connection (calendar + email)."
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Purpose string `json:"purpose"`
			}
			json.Unmarshal(input, &params)

			// Determine which scopes to request.
			currentScopes := deps.GetScopes()
			requestedScopes := make([]string, len(oauth.ScopesBase))
			copy(requestedScopes, oauth.ScopesBase)

			// If already connected, start from current scopes (may include previous upgrades).
			if deps.IsConnected() && len(currentScopes) > 0 {
				requestedScopes = make([]string, len(currentScopes))
				copy(requestedScopes, currentScopes)
			}

			if params.Purpose != "" {
				extraScopes, ok := oauth.ScopesByPurpose[params.Purpose]
				if !ok {
					return "", fmt.Errorf("google_connect: unknown purpose %q — valid options: drive, docs, sheets", params.Purpose)
				}

				// Check if the requested scopes are already granted.
				if deps.IsConnected() && hasAllScopes(currentScopes, extraScopes) {
					return fmt.Sprintf(`{"status":"already_has_scope","purpose":"%s","message":"You already have %s access."}`,
						params.Purpose, params.Purpose), nil
				}

				// Merge new scopes with existing ones (deduplicated).
				for _, s := range extraScopes {
					if !containsScope(requestedScopes, s) {
						requestedScopes = append(requestedScopes, s)
					}
				}
			} else if deps.IsConnected() {
				// No purpose and already connected — nothing to do.
				friendly := make([]string, len(currentScopes))
				for i, s := range currentScopes {
					friendly[i] = oauth.FriendlyScope(s)
				}
				return fmt.Sprintf(`{"status":"already_connected","scopes":%s,"hint":"To add more capabilities, call google_connect with a purpose like 'drive', 'docs', or 'sheets'."}`,
					mustJSON(friendly)), nil
			}

			// Request device code with the full scope set.
			dc, err := deps.OAuthClient.RequestDeviceCodeWithScopes(ctx, requestedScopes)
			if err != nil {
				return "", fmt.Errorf("google_connect: request device code: %w", err)
			}

			action := "connect"
			if deps.IsConnected() {
				action = "upgrade"
			}

			codeResult := map[string]any{
				"status":           "waiting_for_user",
				"action":           action,
				"user_code":        dc.UserCode,
				"verification_url": dc.VerificationURL,
				"instructions":     fmt.Sprintf("Go to %s and enter code %s", dc.VerificationURL, dc.UserCode),
			}
			if params.Purpose != "" {
				codeResult["purpose"] = params.Purpose
			}
			codeJSON, _ := json.Marshal(codeResult)

			// Poll in background — the tool returns immediately with the code.
			go func() {
				pollCtx, cancel := context.WithTimeout(context.Background(),
					time.Duration(dc.ExpiresIn)*time.Second)
				defer cancel()

				tok, err := deps.OAuthClient.PollForToken(pollCtx, dc)
				if err != nil {
					reg.logger.Error("Google device flow failed", "error", err, "action", action)
					return
				}

				reg.logger.Info("Google account connected via agent tool",
					"action", action, "scopes", tok.Scopes)
				if deps.OnTokenReceived != nil {
					deps.OnTokenReceived(*tok)
				}
			}()

			return string(codeJSON), nil
		},
	})
}

// hasAllScopes checks whether current has all of required.
func hasAllScopes(current, required []string) bool {
	for _, r := range required {
		if !containsScope(current, r) {
			return false
		}
	}
	return true
}

// containsScope checks if a scope exists in a list.
func containsScope(scopes []string, scope string) bool {
	for _, s := range scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
