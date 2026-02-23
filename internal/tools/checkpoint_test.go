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

// mockCheckpointSaver records SaveCheckpoint calls for test verification.
type mockCheckpointSaver struct {
	lastSessionID string
	lastInput     json.RawMessage
	err           error
	calls         int
}

func (m *mockCheckpointSaver) SaveCheckpoint(_ context.Context, sessionID string, input json.RawMessage) error {
	m.calls++
	m.lastSessionID = sessionID
	m.lastInput = input
	return m.err
}

func TestCheckpointToolRegistration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	reg := NewRegistry(logger)
	saver := &mockCheckpointSaver{}

	RegisterCheckpointTool(reg, nil, saver)

	tool := reg.Get("save_checkpoint")
	if tool == nil {
		t.Fatal("save_checkpoint tool not registered")
	}
	if tool.MinTier != security.TierOperator {
		t.Errorf("MinTier = %v, want TierOperator (%v)", tool.MinTier, security.TierOperator)
	}
	if tool.Description == "" {
		t.Error("Description is empty")
	}

	// Verify input schema is valid JSON.
	var schema map[string]interface{}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("InputSchema type = %v, want 'object'", schema["type"])
	}

	// Verify required field.
	required, ok := schema["required"].([]interface{})
	if !ok || len(required) != 1 || required[0] != "active_task" {
		t.Errorf("InputSchema required = %v, want [active_task]", schema["required"])
	}
}

func TestCheckpointToolExecute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	reg := NewRegistry(logger)
	saver := &mockCheckpointSaver{}

	RegisterCheckpointTool(reg, nil, saver)

	sess := &security.Session{
		ID:    "sess_test",
		Trust: security.TierOperator,
	}

	input := json.RawMessage(`{
		"active_task": "Building login page",
		"active_task_context": "User wants OAuth",
		"pending_proposals": ["Add GitHub OAuth"],
		"conversational_tone": "focused"
	}`)

	result, err := reg.Execute(context.Background(), sess, "save_checkpoint", input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !strings.Contains(result, "Building login page") {
		t.Errorf("result should contain active_task, got: %s", result)
	}
	if !strings.Contains(result, "Session checkpoint saved") {
		t.Errorf("result should confirm save, got: %s", result)
	}

	// Verify the saver was called.
	if saver.calls != 1 {
		t.Errorf("SaveCheckpoint called %d times, want 1", saver.calls)
	}
	if saver.lastSessionID != "sess_test" {
		t.Errorf("lastSessionID = %q, want %q", saver.lastSessionID, "sess_test")
	}

	// Verify the full input was passed through.
	var passedInput struct {
		ActiveTask string `json:"active_task"`
		Tone       string `json:"conversational_tone"`
	}
	if err := json.Unmarshal(saver.lastInput, &passedInput); err != nil {
		t.Fatalf("failed to parse passed input: %v", err)
	}
	if passedInput.ActiveTask != "Building login page" {
		t.Errorf("passed active_task = %q, want %q", passedInput.ActiveTask, "Building login page")
	}
	if passedInput.Tone != "focused" {
		t.Errorf("passed tone = %q, want %q", passedInput.Tone, "focused")
	}
}

func TestCheckpointToolMissingActiveTask(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	reg := NewRegistry(logger)
	saver := &mockCheckpointSaver{}

	RegisterCheckpointTool(reg, nil, saver)

	sess := &security.Session{
		ID:    "sess_test",
		Trust: security.TierOperator,
	}

	// Empty active_task should fail.
	input := json.RawMessage(`{"active_task": ""}`)
	_, err := reg.Execute(context.Background(), sess, "save_checkpoint", input)
	if err == nil {
		t.Fatal("expected error for empty active_task, got nil")
	}
	if !strings.Contains(err.Error(), "active_task is required") {
		t.Errorf("error should mention active_task, got: %v", err)
	}

	// Saver should not have been called.
	if saver.calls != 0 {
		t.Errorf("SaveCheckpoint called %d times on invalid input, want 0", saver.calls)
	}
}

func TestCheckpointToolSaverError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	reg := NewRegistry(logger)
	saver := &mockCheckpointSaver{err: fmt.Errorf("database locked")}

	RegisterCheckpointTool(reg, nil, saver)

	sess := &security.Session{
		ID:    "sess_test",
		Trust: security.TierOperator,
	}

	input := json.RawMessage(`{"active_task": "Some task"}`)
	_, err := reg.Execute(context.Background(), sess, "save_checkpoint", input)
	if err == nil {
		t.Fatal("expected error from saver failure, got nil")
	}
	if !strings.Contains(err.Error(), "database locked") {
		t.Errorf("error should propagate saver error, got: %v", err)
	}
}

func TestCheckpointToolTrustEnforcement(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	reg := NewRegistry(logger)
	saver := &mockCheckpointSaver{}

	RegisterCheckpointTool(reg, nil, saver)

	// Trusted user (below Operator) should be denied.
	sess := &security.Session{
		ID:    "sess_test",
		Trust: security.TierTrusted,
	}

	input := json.RawMessage(`{"active_task": "Some task"}`)
	_, err := reg.Execute(context.Background(), sess, "save_checkpoint", input)
	if err == nil {
		t.Fatal("expected trust tier error for TierTrusted, got nil")
	}
	if !strings.Contains(err.Error(), "trust tier") {
		t.Errorf("error should mention trust tier, got: %v", err)
	}
}

func TestCheckpointToolVisibleToOperator(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	reg := NewRegistry(logger)
	saver := &mockCheckpointSaver{}

	RegisterCheckpointTool(reg, nil, saver)

	// ForTier(Operator) should include the checkpoint tool.
	operatorTools := reg.ForTier(security.TierOperator)
	found := false
	for _, tool := range operatorTools {
		if tool.Name == "save_checkpoint" {
			found = true
			break
		}
	}
	if !found {
		t.Error("save_checkpoint should be visible at TierOperator")
	}

	// ForTier(Trusted) should NOT include it.
	trustedTools := reg.ForTier(security.TierTrusted)
	for _, tool := range trustedTools {
		if tool.Name == "save_checkpoint" {
			t.Error("save_checkpoint should NOT be visible at TierTrusted")
		}
	}
}
