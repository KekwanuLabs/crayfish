package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestDeviceCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("client_id") != "test-client-id" {
			t.Errorf("unexpected client_id: %s", r.FormValue("client_id"))
		}

		json.NewEncoder(w).Encode(DeviceCode{
			DeviceCode:      "test-device-code",
			UserCode:        "ABCD-EFGH",
			VerificationURL: "https://www.google.com/device",
			ExpiresIn:       1800,
			Interval:        5,
		})
	}))
	defer server.Close()

	DeviceCodeURL = server.URL
	defer func() { DeviceCodeURL = "https://oauth2.googleapis.com/device/code" }()

	client := NewClient(Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-secret",
		Scopes:       []string{"scope1", "scope2"},
	}, nil)

	dc, err := client.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "ABCD-EFGH" {
		t.Errorf("expected user code ABCD-EFGH, got %s", dc.UserCode)
	}
	if dc.DeviceCode != "test-device-code" {
		t.Errorf("expected device code test-device-code, got %s", dc.DeviceCode)
	}
	if dc.VerificationURL != "https://www.google.com/device" {
		t.Errorf("unexpected verification URL: %s", dc.VerificationURL)
	}
	if dc.Interval != 5 {
		t.Errorf("expected interval 5, got %d", dc.Interval)
	}
}

func TestPollForToken(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n <= 2 {
			// First two calls: pending
			json.NewEncoder(w).Encode(map[string]string{
				"error": "authorization_pending",
			})
			return
		}
		// Third call: success
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "test-access-token",
			"refresh_token": "test-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	TokenURL = server.URL
	defer func() { TokenURL = "https://oauth2.googleapis.com/token" }()

	client := NewClient(Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-secret",
		Scopes:       []string{CalendarScope},
	}, nil)

	dc := &DeviceCode{
		DeviceCode: "test-device-code",
		ExpiresIn:  30,
		Interval:   1, // 1 second for fast test
		Scopes:     []string{CalendarScope},
	}

	tok, err := client.PollForToken(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "test-access-token" {
		t.Errorf("expected access token test-access-token, got %s", tok.AccessToken)
	}
	if tok.RefreshToken != "test-refresh-token" {
		t.Errorf("expected refresh token test-refresh-token, got %s", tok.RefreshToken)
	}
	if len(tok.Scopes) != 1 || tok.Scopes[0] != CalendarScope {
		t.Errorf("unexpected scopes: %v", tok.Scopes)
	}
}

func TestPollForTokenTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"error": "authorization_pending",
		})
	}))
	defer server.Close()

	TokenURL = server.URL
	defer func() { TokenURL = "https://oauth2.googleapis.com/token" }()

	client := NewClient(Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-secret",
		Scopes:       []string{CalendarScope},
	}, nil)

	dc := &DeviceCode{
		DeviceCode: "test-device-code",
		ExpiresIn:  2, // Expires in 2 seconds
		Interval:   1,
	}

	_, err := client.PollForToken(context.Background(), dc)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if err.Error() != "oauth: device code expired" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPollForTokenSlowDown(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			json.NewEncoder(w).Encode(map[string]string{
				"error": "slow_down",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "test-access-token",
			"refresh_token": "test-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	TokenURL = server.URL
	defer func() { TokenURL = "https://oauth2.googleapis.com/token" }()

	client := NewClient(Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-secret",
		Scopes:       []string{CalendarScope},
	}, nil)

	// Override the slow_down sleep for testing — we can't easily do this
	// without modifying the function, so we just verify the flow completes.
	dc := &DeviceCode{
		DeviceCode: "test-device-code",
		ExpiresIn:  30,
		Interval:   1,
	}

	tok, err := client.PollForToken(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "test-access-token" {
		t.Errorf("expected access token, got %s", tok.AccessToken)
	}
}

func TestTokenValid(t *testing.T) {
	tests := []struct {
		name  string
		token *Token
		want  bool
	}{
		{
			name:  "nil token",
			token: nil,
			want:  false,
		},
		{
			name:  "empty access token",
			token: &Token{AccessToken: "", Expiry: time.Now().Add(time.Hour)},
			want:  false,
		},
		{
			name:  "zero expiry",
			token: &Token{AccessToken: "tok", Expiry: time.Time{}},
			want:  false,
		},
		{
			name:  "expired",
			token: &Token{AccessToken: "tok", Expiry: time.Now().Add(-time.Hour)},
			want:  false,
		},
		{
			name:  "expiring within 5 min buffer",
			token: &Token{AccessToken: "tok", Expiry: time.Now().Add(3 * time.Minute)},
			want:  false,
		},
		{
			name:  "valid - well in future",
			token: &Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)},
			want:  true,
		},
		{
			name:  "valid - just past 5 min buffer",
			token: &Token{AccessToken: "tok", Expiry: time.Now().Add(6 * time.Minute)},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.token.Valid()
			if got != tt.want {
				t.Errorf("Token.Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRefresh(t *testing.T) {
	var refreshCallbackCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type refresh_token, got %s", r.FormValue("grant_type"))
		}
		if r.FormValue("refresh_token") != "old-refresh-token" {
			t.Errorf("unexpected refresh token: %s", r.FormValue("refresh_token"))
		}

		// Google often doesn't return a new refresh token on refresh.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "new-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	TokenURL = server.URL
	defer func() { TokenURL = "https://oauth2.googleapis.com/token" }()

	client := NewClient(Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-secret",
	}, func(tok Token) {
		refreshCallbackCalled = true
	})

	// Test Refresh directly
	tok, err := client.Refresh(context.Background(), "old-refresh-token")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "new-access-token" {
		t.Errorf("expected new-access-token, got %s", tok.AccessToken)
	}
	// Old refresh token should be preserved when Google doesn't return a new one.
	if tok.RefreshToken != "old-refresh-token" {
		t.Errorf("expected old refresh token preserved, got %s", tok.RefreshToken)
	}

	// Test ValidAccessToken triggers refresh and callback
	current := &Token{
		AccessToken:  "expired-token",
		RefreshToken: "old-refresh-token",
		Expiry:       time.Now().Add(-time.Hour), // Expired
	}

	accessToken, err := client.ValidAccessToken(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if accessToken != "new-access-token" {
		t.Errorf("expected new-access-token, got %s", accessToken)
	}
	if !refreshCallbackCalled {
		t.Error("expected onTokenRefresh callback to be called")
	}
	// Current token should be updated in place.
	if current.AccessToken != "new-access-token" {
		t.Errorf("expected current token updated, got %s", current.AccessToken)
	}
}

func TestHasScope(t *testing.T) {
	client := NewClient(Config{}, nil)

	tok := &Token{
		Scopes: []string{CalendarScope, GmailReadonly},
	}

	if !client.HasScope(tok, CalendarScope) {
		t.Error("expected HasScope to return true for CalendarScope")
	}
	if !client.HasScope(tok, GmailReadonly) {
		t.Error("expected HasScope to return true for GmailReadonly")
	}
	if client.HasScope(tok, DriveReadonly) {
		t.Error("expected HasScope to return false for DriveReadonly")
	}
	if client.HasScope(nil, CalendarScope) {
		t.Error("expected HasScope to return false for nil token")
	}
}
