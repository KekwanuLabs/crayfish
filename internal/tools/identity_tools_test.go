package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// mockIdentityStore records calls for test verification.
type mockIdentityStore struct {
	soulContent string
	userContent string
	writeErr    error
	writeCalls  int
}

func (m *mockIdentityStore) Soul() string    { return m.soulContent }
func (m *mockIdentityStore) User() string    { return m.userContent }
func (m *mockIdentityStore) HasUser() bool   { return len(m.userContent) > 10 }

func (m *mockIdentityStore) WriteSoul(content string) error {
	m.writeCalls++
	if m.writeErr != nil {
		return m.writeErr
	}
	m.soulContent = content
	return nil
}

func (m *mockIdentityStore) WriteUser(content string) error {
	m.writeCalls++
	if m.writeErr != nil {
		return m.writeErr
	}
	m.userContent = content
	return nil
}

func identityTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestIdentityToolRegistration(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{}

	RegisterIdentityTools(reg, store)

	readTool := reg.Get("identity_read")
	if readTool == nil {
		t.Fatal("identity_read tool not registered")
	}
	if readTool.MinTier != security.TierOperator {
		t.Errorf("identity_read MinTier = %v, want TierOperator", readTool.MinTier)
	}

	updateTool := reg.Get("identity_update")
	if updateTool == nil {
		t.Fatal("identity_update tool not registered")
	}
	if updateTool.MinTier != security.TierOperator {
		t.Errorf("identity_update MinTier = %v, want TierOperator", updateTool.MinTier)
	}

	// Verify input schemas are valid JSON.
	for _, tool := range []*Tool{readTool, updateTool} {
		var schema map[string]interface{}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s InputSchema is not valid JSON: %v", tool.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s InputSchema type = %v, want 'object'", tool.Name, schema["type"])
		}
	}
}

func TestIdentityReadSoul(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{soulContent: "I am warm and curious."}
	RegisterIdentityTools(reg, store)

	sess := &security.Session{ID: "sess_test", Trust: security.TierOperator}
	input := json.RawMessage(`{"file": "soul"}`)

	result, err := reg.Execute(context.Background(), sess, "identity_read", input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result != "I am warm and curious." {
		t.Errorf("result = %q, want %q", result, "I am warm and curious.")
	}
}

func TestIdentityReadUser(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{userContent: "Name: Alice\nJob: Engineer"}
	RegisterIdentityTools(reg, store)

	sess := &security.Session{ID: "sess_test", Trust: security.TierOperator}
	input := json.RawMessage(`{"file": "user"}`)

	result, err := reg.Execute(context.Background(), sess, "identity_read", input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result != "Name: Alice\nJob: Engineer" {
		t.Errorf("result = %q, want %q", result, "Name: Alice\nJob: Engineer")
	}
}

func TestIdentityReadEmpty(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{}
	RegisterIdentityTools(reg, store)

	sess := &security.Session{ID: "sess_test", Trust: security.TierOperator}

	// Empty soul.
	result, err := reg.Execute(context.Background(), sess, "identity_read", json.RawMessage(`{"file": "soul"}`))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "empty") {
		t.Errorf("expected 'empty' in result for empty soul, got: %s", result)
	}

	// Empty user.
	result, err = reg.Execute(context.Background(), sess, "identity_read", json.RawMessage(`{"file": "user"}`))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "empty") {
		t.Errorf("expected 'empty' in result for empty user, got: %s", result)
	}
}

func TestIdentityReadInvalidFile(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{}
	RegisterIdentityTools(reg, store)

	sess := &security.Session{ID: "sess_test", Trust: security.TierOperator}
	input := json.RawMessage(`{"file": "invalid"}`)

	_, err := reg.Execute(context.Background(), sess, "identity_read", input)
	if err == nil {
		t.Fatal("expected error for invalid file, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention invalid file, got: %v", err)
	}
}

func TestIdentityUpdateSoul(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{}
	RegisterIdentityTools(reg, store)

	sess := &security.Session{ID: "sess_test", Trust: security.TierOperator}
	input := json.RawMessage(`{"file": "soul", "content": "I am playful and witty."}`)

	result, err := reg.Execute(context.Background(), sess, "identity_update", input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "SOUL.md updated") {
		t.Errorf("result should confirm update, got: %s", result)
	}
	if store.soulContent != "I am playful and witty." {
		t.Errorf("store.soulContent = %q, want %q", store.soulContent, "I am playful and witty.")
	}
	if store.writeCalls != 1 {
		t.Errorf("writeCalls = %d, want 1", store.writeCalls)
	}
}

func TestIdentityUpdateUser(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{}
	RegisterIdentityTools(reg, store)

	sess := &security.Session{ID: "sess_test", Trust: security.TierOperator}
	input := json.RawMessage(`{"file": "user", "content": "Name: Bob\nTimezone: EST"}`)

	result, err := reg.Execute(context.Background(), sess, "identity_update", input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "USER.md updated") {
		t.Errorf("result should confirm update, got: %s", result)
	}
	if store.userContent != "Name: Bob\nTimezone: EST" {
		t.Errorf("store.userContent = %q, want %q", store.userContent, "Name: Bob\nTimezone: EST")
	}
}

func TestIdentityUpdateEmptyContent(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{}
	RegisterIdentityTools(reg, store)

	sess := &security.Session{ID: "sess_test", Trust: security.TierOperator}
	input := json.RawMessage(`{"file": "soul", "content": ""}`)

	_, err := reg.Execute(context.Background(), sess, "identity_update", input)
	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
	if !strings.Contains(err.Error(), "content is required") {
		t.Errorf("error should mention content is required, got: %v", err)
	}
	if store.writeCalls != 0 {
		t.Errorf("writeCalls = %d, want 0 for empty content", store.writeCalls)
	}
}

func TestIdentityUpdateWriteError(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{writeErr: fmt.Errorf("disk full")}
	RegisterIdentityTools(reg, store)

	sess := &security.Session{ID: "sess_test", Trust: security.TierOperator}
	input := json.RawMessage(`{"file": "soul", "content": "some content"}`)

	_, err := reg.Execute(context.Background(), sess, "identity_update", input)
	if err == nil {
		t.Fatal("expected error from write failure, got nil")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error should propagate write error, got: %v", err)
	}
}

func TestIdentityToolsTrustEnforcement(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{}
	RegisterIdentityTools(reg, store)

	// Trusted user (below Operator) should be denied.
	sess := &security.Session{ID: "sess_test", Trust: security.TierTrusted}

	_, err := reg.Execute(context.Background(), sess, "identity_read", json.RawMessage(`{"file": "soul"}`))
	if err == nil {
		t.Fatal("expected trust tier error for TierTrusted on identity_read")
	}
	if !strings.Contains(err.Error(), "trust tier") {
		t.Errorf("error should mention trust tier, got: %v", err)
	}

	_, err = reg.Execute(context.Background(), sess, "identity_update", json.RawMessage(`{"file": "soul", "content": "test"}`))
	if err == nil {
		t.Fatal("expected trust tier error for TierTrusted on identity_update")
	}
	if !strings.Contains(err.Error(), "trust tier") {
		t.Errorf("error should mention trust tier, got: %v", err)
	}
}

func TestIdentityToolsNotRegisteredWithoutStore(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	RegisterIdentityTools(reg, nil)

	if reg.Get("identity_read") != nil {
		t.Error("identity_read should not be registered when store is nil")
	}
	if reg.Get("identity_update") != nil {
		t.Error("identity_update should not be registered when store is nil")
	}
}

func TestIdentityToolsVisibleToOperator(t *testing.T) {
	reg := NewRegistry(identityTestLogger())
	store := &mockIdentityStore{}
	RegisterIdentityTools(reg, store)

	operatorTools := reg.ForTier(security.TierOperator)
	foundRead := false
	foundUpdate := false
	for _, tool := range operatorTools {
		if tool.Name == "identity_read" {
			foundRead = true
		}
		if tool.Name == "identity_update" {
			foundUpdate = true
		}
	}
	if !foundRead {
		t.Error("identity_read should be visible at TierOperator")
	}
	if !foundUpdate {
		t.Error("identity_update should be visible at TierOperator")
	}

	// Not visible at TierTrusted.
	trustedTools := reg.ForTier(security.TierTrusted)
	for _, tool := range trustedTools {
		if tool.Name == "identity_read" || tool.Name == "identity_update" {
			t.Errorf("%s should NOT be visible at TierTrusted", tool.Name)
		}
	}
}
