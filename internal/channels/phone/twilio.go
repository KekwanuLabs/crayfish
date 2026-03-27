package phone

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var twilioHTTPClient = &http.Client{Timeout: 15 * time.Second}

// twilioCall initiates an outbound call via the Twilio REST API.
// Returns the call SID.
func twilioCall(ctx context.Context, accountSID, authToken, from, to, twimlURL string) (string, error) {
	endpoint := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Calls.json", accountSID)

	form := url.Values{}
	form.Set("To", to)
	form.Set("From", from)
	form.Set("Url", twimlURL)

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("twilio: create request: %w", err)
	}
	req.SetBasicAuth(accountSID, authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := twilioHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("twilio: http request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var result struct {
		SID     string `json:"sid"`
		Status  string `json:"status"`
		Message string `json:"message"` // error field
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("twilio: parse response: %w", err)
	}
	if result.SID == "" {
		return "", fmt.Errorf("twilio: call failed: %s", result.Message)
	}

	return result.SID, nil
}

// ValidateTwilioCredentials checks account SID + auth token against the Twilio API.
func ValidateTwilioCredentials(ctx context.Context, accountSID, authToken string) error {
	endpoint := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s.json", accountSID)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(accountSID, authToken)

	resp, err := twilioHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid credentials")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error %d", resp.StatusCode)
	}
	return nil
}

// UpdateWebhook configures the Twilio phone number's Voice and SMS webhooks.
// Both use POST (not GET) — POST keeps params in the body, not the URL,
// which is required for correct HMAC-SHA1 signature validation.
//
// twimlURL is the voice webhook (e.g. https://tunnel.com/phone/twiml).
// The SMS webhook is derived from the same base: /sms/incoming.
func UpdateWebhook(ctx context.Context, accountSID, authToken, fromNumber, twimlURL string) error {
	sid, err := lookupPhoneNumberSID(ctx, accountSID, authToken, fromNumber)
	if err != nil {
		return fmt.Errorf("lookup phone SID: %w", err)
	}

	// Derive SMS URL from the same base as the voice URL.
	// e.g. https://tunnel.com/phone/twiml → https://tunnel.com/sms/incoming
	smsURL := deriveSMSURL(twimlURL)

	endpoint := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/IncomingPhoneNumbers/%s.json",
		accountSID, sid)

	form := url.Values{}
	form.Set("VoiceUrl", twimlURL)
	form.Set("VoiceMethod", "POST") // POST: params in body, correct for HMAC validation
	form.Set("SmsUrl", smsURL)
	form.Set("SmsMethod", "POST")

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(accountSID, authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := twilioHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("Twilio API %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// deriveSMSURL converts a voice TwiML URL to the SMS incoming URL.
// e.g. https://tunnel.com/phone/twiml → https://tunnel.com/sms/incoming
func deriveSMSURL(twimlURL string) string {
	base := twimlURL
	// Strip /phone/twiml suffix to get the base URL.
	if idx := strings.LastIndex(base, "/phone/twiml"); idx != -1 {
		base = base[:idx]
	}
	return base + "/sms/incoming"
}

// lookupPhoneNumberSID finds the SID for a given Twilio phone number.
func lookupPhoneNumberSID(ctx context.Context, accountSID, authToken, phoneNumber string) (string, error) {
	endpoint := fmt.Sprintf(
		"https://api.twilio.com/2010-04-01/Accounts/%s/IncomingPhoneNumbers.json?PhoneNumber=%s",
		accountSID, url.QueryEscape(phoneNumber))

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(accountSID, authToken)

	resp, err := twilioHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		IncomingPhoneNumbers []struct {
			SID string `json:"sid"`
		} `json:"incoming_phone_numbers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if len(result.IncomingPhoneNumbers) == 0 {
		return "", fmt.Errorf("phone number %s not found in this Twilio account", phoneNumber)
	}
	return result.IncomingPhoneNumbers[0].SID, nil
}

// sendSMS sends an outbound SMS via the Twilio Messages REST API.
func sendSMS(ctx context.Context, accountSID, authToken, from, to, body string) error {
	endpoint := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json", accountSID)

	form := url.Values{}
	form.Set("From", from)
	form.Set("To", to)
	form.Set("Body", body)

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(accountSID, authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := twilioHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("send SMS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("Twilio SMS API %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
