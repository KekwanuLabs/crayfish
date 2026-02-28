package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// mockLLM returns a closure that returns the given response or error.
func mockLLM(response string, err error) func(context.Context, string, string) (string, error) {
	return func(_ context.Context, _, _ string) (string, error) {
		return response, err
	}
}

// mockLLMCapture returns a closure that captures the system and user prompts.
func mockLLMCapture(response string, system, user *string) func(context.Context, string, string) (string, error) {
	return func(_ context.Context, s, u string) (string, error) {
		*system = s
		*user = u
		return response, nil
	}
}

func newTestAgent(llm func(context.Context, string, string) (string, error)) *ProactiveAgent {
	return NewProactiveAgent(ProactiveAgentDeps{
		LLMComplete: llm,
		Logger:      testLogger(),
	})
}

func makeMessage(payload any) *Message {
	data, _ := json.Marshal(payload)
	return &Message{
		Type:      "evaluate_opportunity",
		UserID:    "user-1",
		SessionID: "session-1",
		Payload:   data,
	}
}

// --- evaluateWithLLM tests ---

func TestEvaluateWithLLM_CleanJSON(t *testing.T) {
	resp := `{"verdict":"surface","confidence":0.85,"relevance":0.9,"timing":0.7,"quality":0.8,"reason":"Highly relevant","suggested_message":"Check this out"}`
	agent := newTestAgent(mockLLM(resp, nil))
	opp := &Opportunity{Type: "networking", Title: "Test", Confidence: 0.5}

	eval, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Verdict != "surface" {
		t.Errorf("verdict = %q, want %q", eval.Verdict, "surface")
	}
	if eval.Confidence != 0.85 {
		t.Errorf("confidence = %v, want 0.85", eval.Confidence)
	}
	if eval.Relevance != 0.9 {
		t.Errorf("relevance = %v, want 0.9", eval.Relevance)
	}
	if eval.Timing != 0.7 {
		t.Errorf("timing = %v, want 0.7", eval.Timing)
	}
	if eval.Quality != 0.8 {
		t.Errorf("quality = %v, want 0.8", eval.Quality)
	}
	if eval.Reason != "Highly relevant" {
		t.Errorf("reason = %q, want %q", eval.Reason, "Highly relevant")
	}
	if eval.SuggestedMessage != "Check this out" {
		t.Errorf("suggested_message = %q, want %q", eval.SuggestedMessage, "Check this out")
	}
}

func TestEvaluateWithLLM_MarkdownCodeBlock(t *testing.T) {
	resp := "```json\n{\"verdict\":\"skip\",\"confidence\":0.3,\"relevance\":0.2,\"timing\":0.5,\"quality\":0.1,\"reason\":\"Not relevant\",\"suggested_message\":\"\"}\n```"
	agent := newTestAgent(mockLLM(resp, nil))
	opp := &Opportunity{Type: "test", Title: "Test"}

	eval, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Verdict != "skip" {
		t.Errorf("verdict = %q, want %q", eval.Verdict, "skip")
	}
}

func TestEvaluateWithLLM_BracketExtractionFallback(t *testing.T) {
	resp := "Here is my evaluation:\n{\"verdict\":\"delay\",\"confidence\":0.6,\"relevance\":0.7,\"timing\":0.3,\"quality\":0.6,\"reason\":\"Good but not now\",\"suggested_message\":\"\"}\nHope that helps!"
	agent := newTestAgent(mockLLM(resp, nil))
	opp := &Opportunity{Type: "test", Title: "Test"}

	eval, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Verdict != "delay" {
		t.Errorf("verdict = %q, want %q", eval.Verdict, "delay")
	}
	if eval.Reason != "Good but not now" {
		t.Errorf("reason = %q, want %q", eval.Reason, "Good but not now")
	}
}

func TestEvaluateWithLLM_InvalidJSON(t *testing.T) {
	agent := newTestAgent(mockLLM("this is not json at all", nil))
	opp := &Opportunity{Type: "test", Title: "Test"}

	_, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestEvaluateWithLLM_EmptyResponse(t *testing.T) {
	agent := newTestAgent(mockLLM("", nil))
	opp := &Opportunity{Type: "test", Title: "Test"}

	_, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err == nil {
		t.Fatal("expected error for empty response, got nil")
	}
}

func TestEvaluateWithLLM_LLMError(t *testing.T) {
	agent := newTestAgent(mockLLM("", fmt.Errorf("connection refused")))
	opp := &Opportunity{Type: "test", Title: "Test"}

	_, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err == nil {
		t.Fatal("expected error when LLM fails, got nil")
	}
	if err.Error() != "connection refused" {
		t.Errorf("error = %q, want %q", err.Error(), "connection refused")
	}
}

func TestEvaluateWithLLM_UnknownVerdictDefaultsToSkip(t *testing.T) {
	resp := `{"verdict":"maybe","confidence":0.5,"relevance":0.5,"timing":0.5,"quality":0.5,"reason":"Uncertain","suggested_message":""}`
	agent := newTestAgent(mockLLM(resp, nil))
	opp := &Opportunity{Type: "test", Title: "Test"}

	eval, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Verdict != "skip" {
		t.Errorf("verdict = %q, want %q (fail-closed default)", eval.Verdict, "skip")
	}
}

func TestEvaluateWithLLM_AllValidVerdicts(t *testing.T) {
	for _, verdict := range []string{"surface", "skip", "delay"} {
		t.Run(verdict, func(t *testing.T) {
			resp := fmt.Sprintf(`{"verdict":%q,"confidence":0.5,"relevance":0.5,"timing":0.5,"quality":0.5,"reason":"test","suggested_message":""}`, verdict)
			agent := newTestAgent(mockLLM(resp, nil))
			opp := &Opportunity{Type: "test", Title: "Test"}

			eval, err := agent.evaluateWithLLM(context.Background(), opp, "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if eval.Verdict != verdict {
				t.Errorf("verdict = %q, want %q", eval.Verdict, verdict)
			}
		})
	}
}

func TestEvaluateWithLLM_PromptContainsOpportunityFields(t *testing.T) {
	var capturedSystem, capturedUser string
	resp := `{"verdict":"surface","confidence":0.8,"relevance":0.8,"timing":0.8,"quality":0.8,"reason":"ok","suggested_message":""}`
	agent := newTestAgent(mockLLMCapture(resp, &capturedSystem, &capturedUser))

	opp := &Opportunity{
		Type:        "networking",
		Title:       "Coffee with Alice",
		Description: "Alice mentioned she works on ML infrastructure",
		RelatedTo:   "career goals",
		Confidence:  0.72,
	}
	eval, err := agent.evaluateWithLLM(context.Background(), opp, "User is interested in ML")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval == nil {
		t.Fatal("expected non-nil evaluation")
	}

	// System prompt should mention evaluation criteria.
	if capturedSystem == "" {
		t.Fatal("system prompt was empty")
	}

	// User message should contain all opportunity fields.
	for _, want := range []string{"networking", "Coffee with Alice", "Alice mentioned", "career goals", "0.72", "User is interested in ML"} {
		if !contains(capturedUser, want) {
			t.Errorf("user prompt missing %q", want)
		}
	}
}

func TestEvaluateWithLLM_EmptyUserContext(t *testing.T) {
	var capturedUser string
	resp := `{"verdict":"surface","confidence":0.5,"relevance":0.5,"timing":0.5,"quality":0.5,"reason":"ok","suggested_message":""}`
	agent := newTestAgent(mockLLMCapture(resp, new(string), &capturedUser))
	opp := &Opportunity{Type: "test", Title: "Test", Confidence: 0.5}

	_, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still work with empty user context.
	if capturedUser == "" {
		t.Error("user prompt was empty even with opportunity data")
	}
}

// --- HandleEvaluateOpportunity tests ---

func TestHandleEvaluateOpportunity_InlineOpportunity(t *testing.T) {
	resp := `{"verdict":"surface","confidence":0.9,"relevance":0.9,"timing":0.8,"quality":0.85,"reason":"Great match","suggested_message":"You should reach out"}`
	agent := newTestAgent(mockLLM(resp, nil))

	msg := makeMessage(map[string]any{
		"opportunity": map[string]any{
			"id":          "opp-1",
			"type":        "networking",
			"title":       "Intro to Bob",
			"description": "Bob is hiring for ML roles",
			"confidence":  0.7,
		},
	})

	result, err := agent.HandleEvaluateOpportunity(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.State["evaluated"] != true {
		t.Error("expected evaluated=true")
	}
	if result.State["verdict"] != "surface" {
		t.Errorf("verdict = %v, want %q", result.State["verdict"], "surface")
	}
	if result.State["adjusted_confidence"] != 0.9 {
		t.Errorf("adjusted_confidence = %v, want 0.9", result.State["adjusted_confidence"])
	}
	if result.State["suggested_message"] != "You should reach out" {
		t.Errorf("suggested_message = %v, want %q", result.State["suggested_message"], "You should reach out")
	}
	// Verify the opportunity is echoed back.
	opp, ok := result.State["opportunity"].(*Opportunity)
	if !ok {
		t.Fatalf("opportunity not returned in state")
	}
	if opp.Title != "Intro to Bob" {
		t.Errorf("opportunity title = %q, want %q", opp.Title, "Intro to Bob")
	}
}

func TestHandleEvaluateOpportunity_NoOpportunityOrID(t *testing.T) {
	agent := newTestAgent(mockLLM("", nil))
	msg := makeMessage(map[string]any{})

	result, err := agent.HandleEvaluateOpportunity(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure when no opportunity provided")
	}
	if result.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleEvaluateOpportunity_InvalidPayload(t *testing.T) {
	agent := newTestAgent(mockLLM("", nil))
	msg := &Message{
		Type:    "evaluate_opportunity",
		Payload: json.RawMessage(`{not valid json`),
	}

	result, err := agent.HandleEvaluateOpportunity(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for invalid payload")
	}
}

func TestHandleEvaluateOpportunity_LLMFailure_FailsClosed(t *testing.T) {
	agent := newTestAgent(mockLLM("", fmt.Errorf("rate limited")))

	msg := makeMessage(map[string]any{
		"opportunity": map[string]any{
			"id":         "opp-1",
			"type":       "reminder",
			"title":      "Follow up",
			"confidence": 0.65,
		},
	})

	result, err := agent.HandleEvaluateOpportunity(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success (fail-closed), got error: %s", result.Error)
	}
	if result.State["verdict"] != "skip" {
		t.Errorf("verdict = %v, want %q (fail-closed)", result.State["verdict"], "skip")
	}
	if result.State["confidence"] != 0.65 {
		t.Errorf("confidence = %v, want 0.65 (original)", result.State["confidence"])
	}
	if result.State["reason"] != "evaluation failed, skipping to avoid noise" {
		t.Errorf("reason = %v, want fail-closed message", result.State["reason"])
	}
}

func TestHandleEvaluateOpportunity_NilMemory(t *testing.T) {
	resp := `{"verdict":"skip","confidence":0.3,"relevance":0.2,"timing":0.5,"quality":0.3,"reason":"Low relevance","suggested_message":""}`
	agent := newTestAgent(mockLLM(resp, nil))
	// memory is nil by default in newTestAgent

	msg := makeMessage(map[string]any{
		"opportunity": map[string]any{
			"type":  "test",
			"title": "Test Opp",
		},
	})

	result, err := agent.HandleEvaluateOpportunity(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.State["verdict"] != "skip" {
		t.Errorf("verdict = %v, want %q", result.State["verdict"], "skip")
	}
}

func TestHandleEvaluateOpportunity_ResponseFields(t *testing.T) {
	resp := `{"verdict":"delay","confidence":0.6,"relevance":0.7,"timing":0.3,"quality":0.65,"reason":"Wait until Monday","suggested_message":"Consider reaching out next week"}`
	agent := newTestAgent(mockLLM(resp, nil))

	msg := makeMessage(map[string]any{
		"opportunity": map[string]any{
			"type":  "follow_up",
			"title": "Weekly sync",
		},
	})

	result, err := agent.HandleEvaluateOpportunity(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	// Verify all expected state keys are present.
	expectedKeys := []string{"evaluated", "verdict", "adjusted_confidence", "reason", "relevance", "timing", "quality", "suggested_message", "opportunity"}
	for _, key := range expectedKeys {
		if _, ok := result.State[key]; !ok {
			t.Errorf("missing state key %q", key)
		}
	}

	if result.State["verdict"] != "delay" {
		t.Errorf("verdict = %v, want %q", result.State["verdict"], "delay")
	}
	if result.State["relevance"] != 0.7 {
		t.Errorf("relevance = %v, want 0.7", result.State["relevance"])
	}
	if result.State["timing"] != 0.3 {
		t.Errorf("timing = %v, want 0.3", result.State["timing"])
	}
	if result.State["quality"] != 0.65 {
		t.Errorf("quality = %v, want 0.65", result.State["quality"])
	}
}

// --- loadOpportunityFromDB tests ---

func setupSuggestionsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE suggestions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			type TEXT NOT NULL,
			content TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 0.5,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestLoadOpportunityFromDB_Found(t *testing.T) {
	db := setupSuggestionsDB(t)
	_, err := db.Exec(
		`INSERT INTO suggestions (id, user_id, type, content, confidence) VALUES (?, ?, ?, ?, ?)`,
		"opp-abc", "user-1", "networking", "Meet Alice for coffee", 0.82,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	agent := NewProactiveAgent(ProactiveAgentDeps{
		DB:     db,
		Logger: testLogger(),
	})

	opp, err := agent.loadOpportunityFromDB(context.Background(), "opp-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opp.ID != "opp-abc" {
		t.Errorf("ID = %q, want %q", opp.ID, "opp-abc")
	}
	if opp.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", opp.UserID, "user-1")
	}
	if opp.Type != "networking" {
		t.Errorf("Type = %q, want %q", opp.Type, "networking")
	}
	if opp.Description != "Meet Alice for coffee" {
		t.Errorf("Description = %q, want %q", opp.Description, "Meet Alice for coffee")
	}
	if opp.Confidence != 0.82 {
		t.Errorf("Confidence = %v, want 0.82", opp.Confidence)
	}
}

func TestLoadOpportunityFromDB_NotFound(t *testing.T) {
	db := setupSuggestionsDB(t)

	agent := NewProactiveAgent(ProactiveAgentDeps{
		DB:     db,
		Logger: testLogger(),
	})

	_, err := agent.loadOpportunityFromDB(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing opportunity, got nil")
	}
}

func TestHandleEvaluateOpportunity_LoadFromDB(t *testing.T) {
	db := setupSuggestionsDB(t)
	_, err := db.Exec(
		`INSERT INTO suggestions (id, user_id, type, content, confidence) VALUES (?, ?, ?, ?, ?)`,
		"opp-db", "user-1", "follow_up", "Check in with Bob", 0.75,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	resp := `{"verdict":"surface","confidence":0.8,"relevance":0.75,"timing":0.9,"quality":0.7,"reason":"Timely follow-up","suggested_message":"Time to check in with Bob"}`
	agent := NewProactiveAgent(ProactiveAgentDeps{
		DB:          db,
		LLMComplete: mockLLM(resp, nil),
		Logger:      testLogger(),
	})

	msg := makeMessage(map[string]any{
		"opportunity_id": "opp-db",
	})

	result, err := agent.HandleEvaluateOpportunity(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.State["verdict"] != "surface" {
		t.Errorf("verdict = %v, want %q", result.State["verdict"], "surface")
	}

	opp, ok := result.State["opportunity"].(*Opportunity)
	if !ok {
		t.Fatal("opportunity not returned in state")
	}
	if opp.Description != "Check in with Bob" {
		t.Errorf("description = %q, want %q", opp.Description, "Check in with Bob")
	}
}

func TestHandleEvaluateOpportunity_DBLoadFailure(t *testing.T) {
	db := setupSuggestionsDB(t)
	agent := NewProactiveAgent(ProactiveAgentDeps{
		DB:          db,
		LLMComplete: mockLLM("", nil),
		Logger:      testLogger(),
	})

	msg := makeMessage(map[string]any{
		"opportunity_id": "nonexistent",
	})

	result, err := agent.HandleEvaluateOpportunity(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for missing DB opportunity")
	}
	if result.Error == "" {
		t.Error("expected non-empty error message")
	}
}

// --- Message/Response type tests ---

func TestUnmarshalPayload(t *testing.T) {
	msg := &Message{
		Payload: json.RawMessage(`{"opportunity_id":"abc","opportunity":{"type":"test","title":"Hello"}}`),
	}

	var payload struct {
		OpportunityID string       `json:"opportunity_id"`
		Opportunity   *Opportunity `json:"opportunity"`
	}
	if err := msg.UnmarshalPayload(&payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload.OpportunityID != "abc" {
		t.Errorf("opportunity_id = %q, want %q", payload.OpportunityID, "abc")
	}
	if payload.Opportunity == nil {
		t.Fatal("opportunity was nil")
	}
	if payload.Opportunity.Title != "Hello" {
		t.Errorf("title = %q, want %q", payload.Opportunity.Title, "Hello")
	}
}

func TestNewErrorResponse(t *testing.T) {
	resp := NewErrorResponse(fmt.Errorf("something broke"))
	if resp.Success {
		t.Error("expected Success=false")
	}
	if resp.Error != "something broke" {
		t.Errorf("error = %q, want %q", resp.Error, "something broke")
	}
	if resp.State != nil {
		t.Errorf("expected nil State, got %v", resp.State)
	}
}

// --- Edge case tests ---

func TestEvaluateWithLLM_NestedJSONInResponse(t *testing.T) {
	// LLM returns JSON with nested braces in the suggested_message.
	resp := `{"verdict":"surface","confidence":0.7,"relevance":0.8,"timing":0.6,"quality":0.7,"reason":"Good match","suggested_message":"Check out {this event}"}`
	agent := newTestAgent(mockLLM(resp, nil))
	opp := &Opportunity{Type: "test", Title: "Test"}

	eval, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.SuggestedMessage != "Check out {this event}" {
		t.Errorf("suggested_message = %q, want %q", eval.SuggestedMessage, "Check out {this event}")
	}
}

func TestEvaluateWithLLM_WhitespaceAroundJSON(t *testing.T) {
	resp := "\n\n  {\"verdict\":\"skip\",\"confidence\":0.2,\"relevance\":0.1,\"timing\":0.5,\"quality\":0.1,\"reason\":\"Spam\",\"suggested_message\":\"\"}  \n\n"
	agent := newTestAgent(mockLLM(resp, nil))
	opp := &Opportunity{Type: "test", Title: "Test"}

	eval, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Verdict != "skip" {
		t.Errorf("verdict = %q, want %q", eval.Verdict, "skip")
	}
}

func TestEvaluateWithLLM_PartialJSON(t *testing.T) {
	// Truncated JSON — no closing brace.
	agent := newTestAgent(mockLLM(`{"verdict":"surface","confidence":0.5`, nil))
	opp := &Opportunity{Type: "test", Title: "Test"}

	_, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err == nil {
		t.Fatal("expected error for partial JSON, got nil")
	}
}

func TestHandleEvaluateOpportunity_InlineTakesPrecedenceOverID(t *testing.T) {
	// When both opportunity and opportunity_id are provided, inline should be used.
	resp := `{"verdict":"surface","confidence":0.9,"relevance":0.9,"timing":0.9,"quality":0.9,"reason":"Inline used","suggested_message":""}`
	agent := newTestAgent(mockLLM(resp, nil))

	msg := makeMessage(map[string]any{
		"opportunity_id": "should-not-be-loaded",
		"opportunity": map[string]any{
			"type":        "inline",
			"title":       "Inline Opportunity",
			"description": "This one",
		},
	})

	result, err := agent.HandleEvaluateOpportunity(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	opp, ok := result.State["opportunity"].(*Opportunity)
	if !ok {
		t.Fatal("opportunity not in state")
	}
	if opp.Title != "Inline Opportunity" {
		t.Errorf("title = %q, want %q (inline should take precedence)", opp.Title, "Inline Opportunity")
	}
}

func TestEvaluateWithLLM_EmptyVerdictDefaultsToSkip(t *testing.T) {
	resp := `{"verdict":"","confidence":0.5,"relevance":0.5,"timing":0.5,"quality":0.5,"reason":"Empty verdict","suggested_message":""}`
	agent := newTestAgent(mockLLM(resp, nil))
	opp := &Opportunity{Type: "test", Title: "Test"}

	eval, err := agent.evaluateWithLLM(context.Background(), opp, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Verdict != "skip" {
		t.Errorf("verdict = %q, want %q (fail-closed for empty)", eval.Verdict, "skip")
	}
}

// --- EvaluateAndNotify tests ---

func TestEvaluateAndNotify_SurfaceVerdict_NotifyCalled(t *testing.T) {
	resp := `{"verdict":"surface","confidence":0.9,"relevance":0.9,"timing":0.8,"quality":0.85,"reason":"Great match","suggested_message":"You should check this out"}`
	var notified string
	agent := NewProactiveAgent(ProactiveAgentDeps{
		LLMComplete: mockLLM(resp, nil),
		Notify: func(_ context.Context, msg string) error {
			notified = msg
			return nil
		},
		Logger: testLogger(),
	})

	opp := &Opportunity{ID: "opp-1", Type: "email_highlight", Title: "Important email", Description: "From Alice about ML"}
	err := agent.EvaluateAndNotify(context.Background(), "session-1", opp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notified != "You should check this out" {
		t.Errorf("notified = %q, want %q", notified, "You should check this out")
	}
}

func TestEvaluateAndNotify_SkipVerdict_NotifyNotCalled(t *testing.T) {
	resp := `{"verdict":"skip","confidence":0.2,"relevance":0.1,"timing":0.5,"quality":0.2,"reason":"Not relevant","suggested_message":""}`
	notifyCalled := false
	agent := NewProactiveAgent(ProactiveAgentDeps{
		LLMComplete: mockLLM(resp, nil),
		Notify: func(_ context.Context, _ string) error {
			notifyCalled = true
			return nil
		},
		Logger: testLogger(),
	})

	opp := &Opportunity{ID: "opp-2", Type: "test", Title: "Irrelevant", Description: "Nothing useful"}
	err := agent.EvaluateAndNotify(context.Background(), "session-1", opp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notifyCalled {
		t.Error("notify should not have been called for skip verdict")
	}
}

func TestEvaluateAndNotify_NilNotify_NoPanic(t *testing.T) {
	resp := `{"verdict":"surface","confidence":0.8,"relevance":0.8,"timing":0.8,"quality":0.8,"reason":"Worth it","suggested_message":"Check this"}`
	agent := NewProactiveAgent(ProactiveAgentDeps{
		LLMComplete: mockLLM(resp, nil),
		// Notify is nil
		Logger: testLogger(),
	})

	opp := &Opportunity{ID: "opp-3", Type: "test", Title: "Test", Description: "Test"}
	err := agent.EvaluateAndNotify(context.Background(), "session-1", opp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not panic, just log a warning and return nil.
}

func TestEvaluateAndNotify_LLMError_FailClosed_NoNotify(t *testing.T) {
	var notified string
	agent := NewProactiveAgent(ProactiveAgentDeps{
		LLMComplete: mockLLM("", fmt.Errorf("rate limited")),
		Notify: func(_ context.Context, msg string) error {
			notified = msg
			return nil
		},
		Logger: testLogger(),
	})

	opp := &Opportunity{ID: "opp-4", Type: "email_highlight", Title: "Urgent email", Description: "From Bob about deadline"}
	err := agent.EvaluateAndNotify(context.Background(), "session-1", opp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// LLM failure triggers fail-closed (verdict=skip), so notify should NOT be called.
	if notified != "" {
		t.Errorf("expected no notification on LLM failure (fail-closed), got %q", notified)
	}
}

func TestEvaluateAndNotify_SurfaceWithEmptySuggestedMessage_UsesDefault(t *testing.T) {
	resp := `{"verdict":"surface","confidence":0.8,"relevance":0.8,"timing":0.8,"quality":0.8,"reason":"Good","suggested_message":""}`
	var notified string
	agent := NewProactiveAgent(ProactiveAgentDeps{
		LLMComplete: mockLLM(resp, nil),
		Notify: func(_ context.Context, msg string) error {
			notified = msg
			return nil
		},
		Logger: testLogger(),
	})

	opp := &Opportunity{ID: "opp-5", Type: "email_highlight", Title: "Meeting invite", Description: "Team standup"}
	err := agent.EvaluateAndNotify(context.Background(), "session-1", opp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should use the formatted default since suggested_message is empty.
	if !contains(notified, "Meeting invite") || !contains(notified, "Team standup") {
		t.Errorf("notified = %q, expected to contain title and description", notified)
	}
}

// --- EvaluateOpportunityRaw tests ---

func TestEvaluateOpportunityRaw_ReturnsJSON(t *testing.T) {
	resp := `{"verdict":"surface","confidence":0.8,"relevance":0.7,"timing":0.9,"quality":0.8,"reason":"Timely","suggested_message":"Check it"}`
	agent := newTestAgent(mockLLM(resp, nil))

	result, err := agent.EvaluateOpportunityRaw(context.Background(), "session-1", "email_highlight", "Test", "Description", "subject", 0.6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if parsed["success"] != true {
		t.Errorf("expected success=true, got %v", parsed["success"])
	}
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
