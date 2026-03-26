package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAuthRejectsWithoutKey(t *testing.T) {
	g := &Gateway{config: Config{APIKey: "test-secret-key"}}

	handler := g.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["error"] != "unauthorized" {
		t.Errorf("error = %q, want %q", resp["error"], "unauthorized")
	}
}

func TestRequireAuthRejectsWrongKey(t *testing.T) {
	g := &Gateway{config: Config{APIKey: "test-secret-key"}}

	handler := g.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestRequireAuthPassesWithCorrectKey(t *testing.T) {
	g := &Gateway{config: Config{APIKey: "test-secret-key"}}

	handler := g.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	req.Header.Set("Authorization", "Bearer test-secret-key")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestRequireAuthPassthroughFromLocalhost(t *testing.T) {
	g := &Gateway{config: Config{APIKey: ""}}

	handler := g.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Localhost requests always pass through regardless of API key.
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (localhost = always allowed)", rec.Code, http.StatusOK)
	}
}

func TestRequireAuthBlocksExternalWithNoKey(t *testing.T) {
	g := &Gateway{config: Config{APIKey: ""}}

	handler := g.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// External request with no API key configured — must be blocked.
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	req.RemoteAddr = "203.0.113.1:54321" // external IP
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (external + no key = forbidden)", rec.Code, http.StatusForbidden)
	}
}
