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

func TestRequireAuthPassthroughWhenNoKey(t *testing.T) {
	g := &Gateway{config: Config{APIKey: ""}}

	handler := g.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// No Authorization header, no API key configured — should pass through.
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (no key configured = passthrough)", rec.Code, http.StatusOK)
	}
}
