// Package provider defines the LLM provider interface and message types.
// Crayfish supports remote LLM providers only (no local inference in v1).
package provider

import (
	"context"
	"encoding/json"
)

// Role constants for conversation messages.
const (
	RoleSystem     = "system"
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleToolResult = "tool_result"
)

// Message represents a single turn in a conversation.
// For tool-bearing messages, ToolCalls and ToolUseID carry the structured data.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`  // Set on assistant messages with tool use.
	ToolUseID string     `json:"tool_use_id,omitempty"` // Set on tool_result messages.
	IsError   bool       `json:"is_error,omitempty"`    // Set on tool_result messages if the tool failed.
}

// ToolDef describes a tool the model can call.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"` // JSON string
}

// CompletionRequest is sent to an LLM provider.
type CompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	MaxTokens   int       `json:"max_tokens"`
	TokenBudget int       `json:"-"` // Hard ceiling for context assembly, not sent to API.
}

// CompletionResponse is returned from an LLM provider.
type CompletionResponse struct {
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	TokensUsed int        `json:"tokens_used"`
	StopReason string     `json:"stop_reason"`
}

// Provider is the interface all LLM backends must implement.
type Provider interface {
	// Complete sends a completion request and returns the model's response.
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)

	// Name returns the provider identifier (e.g., "anthropic", "openai").
	Name() string

	// Model returns the resolved model name being used.
	Model() string
}
