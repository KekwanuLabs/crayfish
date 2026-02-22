// Package provider — Anthropic Claude API implementation.
// Supports tool use via the Messages API content block protocol.
// Response decompression handles gzip when the server sends it.
package provider

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
	defaultModel        = "claude-sonnet-4-20250514"
	defaultMaxTokens    = 1024
)

// AnthropicProvider implements Provider for the Claude Messages API.
type AnthropicProvider struct {
	apiKey     string
	model      string
	maxTokens  int
	httpClient *http.Client
	logger     *slog.Logger
}

// AnthropicOption configures the Anthropic provider.
type AnthropicOption func(*AnthropicProvider)

// WithModel sets the Claude model to use.
func WithModel(model string) AnthropicOption {
	return func(p *AnthropicProvider) { p.model = model }
}

// WithMaxTokens sets the default max tokens for responses.
func WithMaxTokens(n int) AnthropicOption {
	return func(p *AnthropicProvider) { p.maxTokens = n }
}

// NewAnthropicProvider creates a provider for the Claude Messages API.
func NewAnthropicProvider(apiKey string, logger *slog.Logger, opts ...AnthropicOption) *AnthropicProvider {
	p := &AnthropicProvider{
		apiKey:    apiKey,
		model:     defaultModel,
		maxTokens: defaultMaxTokens,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		logger: logger,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns "anthropic".
func (p *AnthropicProvider) Name() string { return "anthropic" }

// --- Wire format types for the Anthropic Messages API ---

type anthropicRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    string              `json:"system,omitempty"`
	Messages  []anthropicMessage  `json:"messages"`
	Tools     []anthropicToolDef  `json:"tools,omitempty"`
}

// anthropicMessage supports both simple string content and structured content blocks.
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []contentBlock
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicResponse struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
	StopReason string     `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends a request to the Claude Messages API with tool support.
func (p *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = p.maxTokens
	}

	// Separate system message from conversation messages.
	var systemPrompt string
	var msgs []anthropicMessage
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			systemPrompt = m.Content
			continue
		}
		if m.Role == RoleToolResult {
			// Tool results use structured content blocks.
			msgs = append(msgs, anthropicMessage{
				Role: "user",
				Content: []contentBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolUseID,
					Content:   m.Content,
					IsError:   m.IsError,
				}},
			})
			continue
		}
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			// Assistant messages with tool calls need structured content.
			var blocks []contentBlock
			if m.Content != "" {
				blocks = append(blocks, contentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, contentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: json.RawMessage(tc.Input),
				})
			}
			msgs = append(msgs, anthropicMessage{Role: "assistant", Content: blocks})
			continue
		}
		msgs = append(msgs, anthropicMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	if len(msgs) == 0 {
		return nil, fmt.Errorf("anthropic.Complete: no user messages provided")
	}

	apiReq := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    systemPrompt,
		Messages:  msgs,
	}

	// Add tool definitions if provided.
	for _, t := range req.Tools {
		apiReq.Tools = append(apiReq.Tools, anthropicToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: marshal: %w", err)
	}

	p.logger.Debug("sending request to Anthropic",
		"model", model, "messages", len(msgs), "tools", len(req.Tools), "body_bytes", len(body))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: new request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", p.apiKey)
	httpReq.Header.Set("Anthropic-Version", anthropicAPIVersion)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: http do: %w", err)
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("anthropic.Complete: gzip reader: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	respBody, err := io.ReadAll(io.LimitReader(reader, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr anthropicError
		json.Unmarshal(respBody, &apiErr)
		return nil, fmt.Errorf("anthropic.Complete: API error %d: %s — %s",
			resp.StatusCode, apiErr.Error.Type, apiErr.Error.Message)
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("anthropic.Complete: unmarshal response: %w", err)
	}

	// Parse content blocks into our response type.
	result := &CompletionResponse{
		TokensUsed: apiResp.Usage.InputTokens + apiResp.Usage.OutputTokens,
		StopReason: apiResp.StopReason,
	}

	for _, block := range apiResp.Content {
		switch block.Type {
		case "text":
			result.Content += block.Text
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: string(block.Input),
			})
		}
	}

	return result, nil
}
