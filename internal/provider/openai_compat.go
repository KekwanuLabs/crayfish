// Package provider — OpenAI-compatible HTTP provider.
// Works with OpenAI, Groq, DeepSeek, Together, Ollama, vLLM, and any
// API that speaks the OpenAI chat completions format.
// This is the "HTTP-first" pattern from picoclaw: one provider for many backends.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	openaiDefaultModel    = "gpt-4o-mini"
	openaiDefaultEndpoint = "https://api.openai.com/v1/chat/completions"
)

// OpenAICompatProvider implements Provider for any OpenAI-compatible API.
// Supports: OpenAI, Groq, DeepSeek, Together, Ollama, vLLM, LM Studio, etc.
type OpenAICompatProvider struct {
	apiKey     string
	endpoint   string
	model      string
	maxTokens  int
	provName   string
	httpClient *http.Client
	logger     *slog.Logger
}

// OpenAICompatOption configures the OpenAI-compatible provider.
type OpenAICompatOption func(*OpenAICompatProvider)

// WithOAIEndpoint sets a custom API endpoint.
func WithOAIEndpoint(endpoint string) OpenAICompatOption {
	return func(p *OpenAICompatProvider) { p.endpoint = endpoint }
}

// WithOAIModel sets the model name.
func WithOAIModel(model string) OpenAICompatOption {
	return func(p *OpenAICompatProvider) { p.model = model }
}

// WithOAIMaxTokens sets the default max tokens.
func WithOAIMaxTokens(n int) OpenAICompatOption {
	return func(p *OpenAICompatProvider) { p.maxTokens = n }
}

// WithOAIName sets the provider display name.
func WithOAIName(name string) OpenAICompatOption {
	return func(p *OpenAICompatProvider) { p.provName = name }
}

// NewOpenAICompatProvider creates a provider for any OpenAI-compatible API.
func NewOpenAICompatProvider(apiKey string, logger *slog.Logger, opts ...OpenAICompatOption) *OpenAICompatProvider {
	p := &OpenAICompatProvider{
		apiKey:    apiKey,
		endpoint:  openaiDefaultEndpoint,
		model:     openaiDefaultModel,
		maxTokens: 1024,
		provName:  "openai",
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		logger: logger,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider identifier.
func (p *OpenAICompatProvider) Name() string { return p.provName }

// Model returns the resolved model name.
func (p *OpenAICompatProvider) Model() string { return p.model }

// --- OpenAI wire format ---

type oaiRequest struct {
	Model     string       `json:"model"`
	Messages  []oaiMessage `json:"messages"`
	MaxTokens int          `json:"max_tokens,omitempty"`
	Tools     []oaiTool    `json:"tools,omitempty"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"` // string or []oaiContentPart for vision
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiContentPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *oaiImageURL `json:"image_url,omitempty"`
}

type oaiImageURL struct {
	URL string `json:"url"` // "data:image/jpeg;base64,..."
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Role      string        `json:"role"`
			Content   string        `json:"content"`
			ToolCalls []oaiToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete sends a request to the OpenAI-compatible API.
func (p *OpenAICompatProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = p.maxTokens
	}

	var msgs []oaiMessage
	for _, m := range req.Messages {
		switch m.Role {
		case RoleToolResult:
			msgs = append(msgs, oaiMessage{
				Role:       "tool",
				Content:    m.Content,
				ToolCallID: m.ToolUseID,
			})
		case RoleAssistant:
			msg := oaiMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, oaiToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: tc.Name, Arguments: tc.Input},
				})
			}
			msgs = append(msgs, msg)
		default:
			if m.Role == RoleUser && len(m.Images) > 0 {
				var parts []oaiContentPart
				for _, img := range m.Images {
					parts = append(parts, oaiContentPart{
						Type: "image_url",
						ImageURL: &oaiImageURL{
							URL: fmt.Sprintf("data:%s;base64,%s", img.MediaType, img.Data),
						},
					})
				}
				if m.Content != "" {
					parts = append(parts, oaiContentPart{Type: "text", Text: m.Content})
				}
				msgs = append(msgs, oaiMessage{Role: m.Role, Content: parts})
			} else {
				msgs = append(msgs, oaiMessage{Role: m.Role, Content: m.Content})
			}
		}
	}

	oaiReq := oaiRequest{
		Model:     model,
		Messages:  msgs,
		MaxTokens: maxTokens,
	}

	for _, t := range req.Tools {
		oaiReq.Tools = append(oaiReq.Tools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("%s.Complete: marshal: %w", p.provName, err)
	}

	p.logger.Debug("sending request",
		"provider", p.provName, "model", model, "messages", len(msgs), "body_bytes", len(body))

	var respBody []byte
	var statusCode int
	for attempt := 0; attempt <= maxLLMRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelay(attempt-1, nil)
			p.logger.Warn("retrying request", "provider", p.provName, "attempt", attempt, "delay", delay, "last_status", statusCode)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		var err error
		respBody, statusCode, err = p.doRequest(ctx, body)
		if err != nil {
			return nil, fmt.Errorf("%s.Complete: %w", p.provName, err)
		}
		if isRetryableStatus(statusCode) && attempt < maxLLMRetries {
			continue
		}
		break
	}

	var oaiResp oaiResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("%s.Complete: unmarshal: %w", p.provName, err)
	}

	if oaiResp.Error != nil {
		return nil, fmt.Errorf("%s.Complete: API error: %s — %s",
			p.provName, oaiResp.Error.Type, oaiResp.Error.Message)
	}

	if statusCode != http.StatusOK || len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("%s.Complete: HTTP %d, %d choices", p.provName, statusCode, len(oaiResp.Choices))
	}

	choice := oaiResp.Choices[0]
	result := &CompletionResponse{
		Content:    choice.Message.Content,
		TokensUsed: oaiResp.Usage.TotalTokens,
		StopReason: choice.FinishReason,
	}

	for _, tc := range choice.Message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
		})
	}

	return result, nil
}

// doRequest performs a single HTTP request to the OpenAI-compatible API.
func (p *OpenAICompatProvider) doRequest(ctx context.Context, body []byte) ([]byte, int, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("new request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("read body: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

// Stream falls back to Complete and calls onToken once with the full response.
// OpenAI-compatible providers can override this for native streaming support.
func (p *OpenAICompatProvider) Stream(ctx context.Context, req CompletionRequest, onToken TokenCallback) (*CompletionResponse, error) {
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Content != "" {
		if err := onToken(resp.Content); err != nil {
			return nil, err
		}
	}
	return resp, nil
}
