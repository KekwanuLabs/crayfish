// Package oauth provides stdlib-only Google OAuth 2.0 using the Device Authorization Grant (RFC 8628).
// No external dependencies — uses net/http, net/url, encoding/json only.
package oauth

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

// Google OAuth 2.0 endpoints.
var (
	DeviceCodeURL = "https://oauth2.googleapis.com/device/code"
	TokenURL      = "https://oauth2.googleapis.com/token"
)

// Scopes for Google APIs.
const (
	CalendarScope = "https://www.googleapis.com/auth/calendar"
	GmailReadonly = "https://www.googleapis.com/auth/gmail.readonly"
	GmailSend     = "https://www.googleapis.com/auth/gmail.send"
	UserInfoEmail = "https://www.googleapis.com/auth/userinfo.email"
	DriveScope    = "https://www.googleapis.com/auth/drive"
	DriveReadonly = "https://www.googleapis.com/auth/drive.readonly"
	DocsScope     = "https://www.googleapis.com/auth/documents"
	DocsReadonly  = "https://www.googleapis.com/auth/documents.readonly"
	SheetsScope   = "https://www.googleapis.com/auth/spreadsheets"
	SheetsReadonly = "https://www.googleapis.com/auth/spreadsheets.readonly"
)

// ScopesBase is the default set of scopes for Crayfish (Calendar + Gmail).
var ScopesBase = []string{CalendarScope, GmailReadonly, GmailSend, UserInfoEmail}

// ScopesByPurpose maps user-facing feature names to the scopes they require.
// Used by the google_connect tool to determine which scopes to request
// when a user asks for a specific capability.
var ScopesByPurpose = map[string][]string{
	"drive":  {DriveScope},
	"docs":   {DocsScope},
	"sheets": {SheetsScope},
}

// FriendlyScope returns a human-readable name for a scope URI.
func FriendlyScope(scope string) string {
	switch scope {
	case CalendarScope:
		return "calendar"
	case GmailReadonly:
		return "gmail (read)"
	case GmailSend:
		return "gmail (send)"
	case UserInfoEmail:
		return "email address"
	case DriveScope:
		return "drive"
	case DriveReadonly:
		return "drive (read-only)"
	case DocsScope:
		return "docs"
	case DocsReadonly:
		return "docs (read-only)"
	case SheetsScope:
		return "sheets"
	case SheetsReadonly:
		return "sheets (read-only)"
	default:
		return scope
	}
}

// Client credentials for the Crayfish GCP project.
// Injected at build time via: go build -ldflags "-X ...oauth.CrayfishClientID=... -X ...oauth.CrayfishClientSecret=..."
// Empty when building from source without credentials — Google features disabled gracefully.
var (
	CrayfishClientID     string
	CrayfishClientSecret string
)

// Token holds OAuth 2.0 token data. Fields are tagged for both JSON (API responses)
// and YAML (config file persistence).
type Token struct {
	AccessToken  string   `json:"access_token" yaml:"access_token"`
	RefreshToken string   `json:"refresh_token" yaml:"refresh_token"`
	TokenType    string   `json:"token_type" yaml:"-"`
	ExpiresIn    int      `json:"expires_in" yaml:"-"`
	Expiry       time.Time `json:"-" yaml:"expiry"`
	Scopes       []string `json:"-" yaml:"scopes"`
}

// Valid reports whether the token has an access token that won't expire
// within the next 5 minutes.
func (t *Token) Valid() bool {
	if t == nil || t.AccessToken == "" {
		return false
	}
	if t.Expiry.IsZero() {
		return false
	}
	return time.Now().Add(5 * time.Minute).Before(t.Expiry)
}

// DeviceCode is the response from the device authorization request.
type DeviceCode struct {
	DeviceCode      string   `json:"device_code"`
	UserCode        string   `json:"user_code"`
	VerificationURL string   `json:"verification_url"`
	ExpiresIn       int      `json:"expires_in"`
	Interval        int      `json:"interval"`
	Scopes          []string `json:"-"` // The scopes that were requested (not in Google's response)
}

// Config holds the OAuth client configuration.
type Config struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// Client is a stdlib-only Google OAuth 2.0 client using Device Authorization Grant.
type Client struct {
	config         Config
	httpClient     *http.Client
	onTokenRefresh func(Token)
}

// NewClient creates a new OAuth client. The onTokenRefresh callback is called
// whenever a token is refreshed, allowing the caller to persist it.
func NewClient(cfg Config, onTokenRefresh func(Token)) *Client {
	return &Client{
		config:         cfg,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		onTokenRefresh: onTokenRefresh,
	}
}

// RequestDeviceCode initiates the device authorization flow using the
// client's configured scopes.
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	return c.RequestDeviceCodeWithScopes(ctx, c.config.Scopes)
}

// RequestDeviceCodeWithScopes initiates the device authorization flow with
// the given scopes. Use this for scope upgrades — pass the full set of
// desired scopes (base + new). Google's consent screen will show the new
// permissions the user hasn't yet granted.
func (c *Client) RequestDeviceCodeWithScopes(ctx context.Context, scopes []string) (*DeviceCode, error) {
	data := url.Values{
		"client_id": {c.config.ClientID},
		"scope":     {strings.Join(scopes, " ")},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", DeviceCodeURL,
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: device code request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("oauth: read device code response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth: device code request failed (%d): %s", resp.StatusCode, body)
	}

	var dc DeviceCode
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("oauth: parse device code response: %w", err)
	}

	if dc.Interval == 0 {
		dc.Interval = 5 // Default poll interval
	}

	// Store the requested scopes so PollForToken can record them on the token.
	dc.Scopes = scopes

	return &dc, nil
}

// tokenResponse is the raw JSON response from the token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// PollForToken polls the token endpoint until the user completes the device
// authorization flow or the code expires. It blocks until one of those occurs.
func (c *Client) PollForToken(ctx context.Context, dc *DeviceCode) (*Token, error) {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("oauth: device code expired")
		}

		tok, err := c.exchangeDeviceCode(ctx, dc.DeviceCode)
		if err != nil {
			return nil, err
		}
		if tok != nil {
			tok.Scopes = dc.Scopes
			return tok, nil
		}

		// tok == nil means authorization_pending — keep polling.
	}
}

// exchangeDeviceCode makes a single token exchange attempt.
// Returns (nil, nil) for authorization_pending (caller should keep polling).
// Returns (token, nil) on success.
// Returns (nil, error) on fatal errors.
func (c *Client) exchangeDeviceCode(ctx context.Context, deviceCode string) (*Token, error) {
	data := url.Values{
		"client_id":     {c.config.ClientID},
		"client_secret": {c.config.ClientSecret},
		"device_code":   {deviceCode},
		"grant_type":    {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", TokenURL,
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("oauth: read token response: %w", err)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("oauth: parse token response: %w", err)
	}

	switch tr.Error {
	case "":
		// Success
		return &Token{
			AccessToken:  tr.AccessToken,
			RefreshToken: tr.RefreshToken,
			TokenType:    tr.TokenType,
			ExpiresIn:    tr.ExpiresIn,
			Expiry:       time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		}, nil
	case "authorization_pending":
		return nil, nil // Keep polling
	case "slow_down":
		// Back off — the caller's interval will naturally handle this since
		// we return nil,nil and they sleep again. But we can't increase their
		// interval from here, so we sleep an extra interval ourselves.
		time.Sleep(5 * time.Second)
		return nil, nil
	case "access_denied":
		return nil, fmt.Errorf("oauth: user denied access")
	case "expired_token":
		return nil, fmt.Errorf("oauth: device code expired")
	default:
		return nil, fmt.Errorf("oauth: token error: %s (%s)", tr.Error, tr.ErrorDesc)
	}
}

// Refresh exchanges a refresh token for a new access token.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	data := url.Values{
		"client_id":     {c.config.ClientID},
		"client_secret": {c.config.ClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", TokenURL,
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("oauth: read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth: refresh failed (%d): %s", resp.StatusCode, body)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("oauth: parse refresh response: %w", err)
	}

	if tr.Error != "" {
		return nil, fmt.Errorf("oauth: refresh error: %s (%s)", tr.Error, tr.ErrorDesc)
	}

	tok := &Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		ExpiresIn:    tr.ExpiresIn,
		Expiry:       time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}

	// Google sometimes doesn't return a new refresh token — preserve the old one.
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}

	return tok, nil
}

// ValidAccessToken returns a valid access token, refreshing if necessary.
// If the token is refreshed, the onTokenRefresh callback is invoked to persist it.
func (c *Client) ValidAccessToken(ctx context.Context, current *Token) (string, error) {
	if current == nil || current.RefreshToken == "" {
		return "", fmt.Errorf("oauth: no valid token — reauthorization required")
	}
	if current.Valid() {
		return current.AccessToken, nil
	}

	refreshed, err := c.Refresh(ctx, current.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("oauth: refresh failed: %w", err)
	}

	// Update the caller's token in place.
	current.AccessToken = refreshed.AccessToken
	current.RefreshToken = refreshed.RefreshToken
	current.Expiry = refreshed.Expiry
	current.ExpiresIn = refreshed.ExpiresIn

	// Notify the caller to persist the refreshed token.
	if c.onTokenRefresh != nil {
		c.onTokenRefresh(*current)
	}

	return refreshed.AccessToken, nil
}

// HasScope reports whether the token was granted the given scope.
func (c *Client) HasScope(token *Token, scope string) bool {
	if token == nil {
		return false
	}
	for _, s := range token.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}
