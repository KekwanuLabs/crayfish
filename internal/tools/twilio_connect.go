package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KekwanuLabs/crayfish/internal/channels/phone"
	"github.com/KekwanuLabs/crayfish/internal/security"
)

// TwilioConnectDeps are injected by app.go when registering the twilio_connect tool.
type TwilioConnectDeps struct {
	IsConfigured func() bool
	SaveCreds    func(accountSID, authToken, fromNumber, tunnelURL string)
	ActivatePhone func(accountSID, authToken, fromNumber, tunnelURL string)
}

// RegisterTwilioConnectTool registers the twilio_connect setup tool.
func RegisterTwilioConnectTool(reg *Registry, deps TwilioConnectDeps) {
	reg.Register(&Tool{
		Name: "twilio_connect",
		Description: `Set up Twilio to enable outbound phone calls.

Required information:
- account_sid: Twilio Account SID (from console.twilio.com — starts with "AC")
- auth_token: Twilio Auth Token (from console.twilio.com)
- from_number: Your Twilio phone number in E.164 format (e.g. +12025551234)
- tunnel_url: (optional) Public URL for this device — needed for calls to work.
  Use Cloudflare Tunnel for a free stable URL: run 'cloudflared tunnel --url http://localhost:8119'

Once configured, you can make calls with call_make.

Free tier: Twilio trial gives $15 credit. Phone numbers cost ~$1/month. Calls ~$0.013/min.`,
		MinTier: security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"account_sid": {"type": "string", "description": "Twilio Account SID (starts with AC)"},
				"auth_token":  {"type": "string", "description": "Twilio Auth Token"},
				"from_number": {"type": "string", "description": "Your Twilio phone number (E.164 format)"},
				"tunnel_url":  {"type": "string", "description": "Public URL for this device (Cloudflare Tunnel URL)"},
				"action":      {"type": "string", "enum": ["setup", "status"], "description": "What to do (default: setup)"}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, params json.RawMessage) (string, error) {
			var input struct {
				AccountSID string `json:"account_sid"`
				AuthToken  string `json:"auth_token"`
				FromNumber string `json:"from_number"`
				TunnelURL  string `json:"tunnel_url"`
				Action     string `json:"action"`
			}
			if err := json.Unmarshal(params, &input); err != nil {
				return "", fmt.Errorf("invalid parameters: %w", err)
			}
			if input.Action == "" {
				input.Action = "setup"
			}

			switch input.Action {
			case "status":
				if deps.IsConfigured() {
					return "Phone calls are set up and ready. Just ask me to call someone!", nil
				}
				return "Phone calls aren't set up yet. Say 'set up phone calls' and I'll walk you through it step by step — takes about 5 minutes.", nil

			case "setup":
				if input.AccountSID == "" || input.AuthToken == "" {
					return `I'll set up phone calls for you! Here's what you need — it takes about 5 minutes:

**Step 1: Create a free Twilio account**
👉 https://www.twilio.com/try-twilio
You get $15 free credit to start. No credit card required for trial.

**Step 2: Get your credentials**
After signing up, go to your Twilio Console (console.twilio.com).
Right on the home page you'll see:
- **Account SID** — starts with "AC"
- **Auth Token** — click the eye icon to reveal it

**Step 3: Get a phone number**
In the console: Phone Numbers → Manage → Buy a Number
Search for a local number → Buy (~$1/month, covered by your trial credit)

**Step 4: Paste them here**
Reply with your Account SID, Auth Token, and the phone number you bought.
That's it — I'll handle the rest automatically!

> *Note: The tunnel for receiving calls runs automatically — you don't need to set that up yourself.*`, nil
				}

				// Validate credentials.
				if err := phone.ValidateTwilioCredentials(ctx, input.AccountSID, input.AuthToken); err != nil {
					return "", fmt.Errorf("credential check failed: %w — verify your Account SID and Auth Token at console.twilio.com", err)
				}

				if input.FromNumber == "" {
					return "Credentials valid! Now I need your Twilio phone number (the one you bought, in E.164 format like +12025551234).", nil
				}

				// Save and activate.
				deps.SaveCreds(input.AccountSID, input.AuthToken, input.FromNumber, input.TunnelURL)
				deps.ActivatePhone(input.AccountSID, input.AuthToken, input.FromNumber, input.TunnelURL)

				tunnelNote := ""
				if input.TunnelURL == "" {
					tunnelNote = "\n\n⚠️ No tunnel URL set. Run `cloudflared tunnel --url http://localhost:8119` on the Pi and tell me the URL — calls won't work without it."
				}

				return fmt.Sprintf("Phone calls are ready! Your number is %s.\n\nTry it: \"Call [number] and say hello from Crayfish\"%s", input.FromNumber, tunnelNote), nil

			default:
				return "", fmt.Errorf("unknown action %q", input.Action)
			}
		},
	})
}
