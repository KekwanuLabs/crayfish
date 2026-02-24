// Package mcp implements a minimal MCP (Model Context Protocol) client.
// MCP is a standard protocol for AI agents to connect to external tool servers.
// This implementation supports both stdio and HTTP transports.
//
// ~200 lines. No bloat.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client connects to an MCP server and discovers/invokes tools.
type Client struct {
	name      string
	transport Transport
	logger    *slog.Logger
	tools     []ToolDef
	mu        sync.RWMutex
	nextID    atomic.Int64
}

// Transport abstracts stdio vs HTTP communication.
type Transport interface {
	Send(ctx context.Context, msg []byte) ([]byte, error)
	Close() error
}

// ToolDef represents a discovered MCP tool.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// JSONRPCRequest is a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewClient creates an MCP client for the given server.
// For stdio: command is the executable (e.g., "npx @modelcontextprotocol/server-github")
// For HTTP: command is the URL (e.g., "http://localhost:3000/mcp")
func NewClient(name, command string, logger *slog.Logger) (*Client, error) {
	var transport Transport
	var err error

	if strings.HasPrefix(command, "http://") || strings.HasPrefix(command, "https://") {
		transport = &HTTPTransport{url: command, client: &http.Client{Timeout: 30 * time.Second}}
	} else {
		transport, err = newStdioTransport(command, logger)
		if err != nil {
			return nil, fmt.Errorf("mcp: start process: %w", err)
		}
	}

	return &Client{
		name:      name,
		transport: transport,
		logger:    logger,
	}, nil
}

// Initialize performs the MCP handshake and discovers tools.
func (c *Client) Initialize(ctx context.Context) error {
	// Send initialize request.
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "crayfish",
			"version": "1.0.0",
		},
	}

	_, err := c.call(ctx, "initialize", initParams)
	if err != nil {
		return fmt.Errorf("mcp: initialize: %w", err)
	}

	// Send initialized notification (no response expected, but we send as request for simplicity).
	c.call(ctx, "notifications/initialized", nil)

	// Discover tools.
	result, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return fmt.Errorf("mcp: tools/list: %w", err)
	}

	var toolsResult struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(result, &toolsResult); err != nil {
		return fmt.Errorf("mcp: parse tools: %w", err)
	}

	c.mu.Lock()
	c.tools = toolsResult.Tools
	c.mu.Unlock()

	c.logger.Info("MCP client initialized", "server", c.name, "tools", len(c.tools))
	return nil
}

// Tools returns the discovered tools.
func (c *Client) Tools() []ToolDef {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tools
}

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	params := map[string]interface{}{
		"name":      name,
		"arguments": json.RawMessage(args),
	}

	result, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return "", err
	}

	// Parse tool result - MCP returns content array.
	var callResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &callResult); err != nil {
		return string(result), nil // Return raw if can't parse.
	}

	if callResult.IsError && len(callResult.Content) > 0 {
		return "", fmt.Errorf("mcp tool error: %s", callResult.Content[0].Text)
	}

	// Concatenate text content.
	var texts []string
	for _, c := range callResult.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

// Close shuts down the MCP connection.
func (c *Client) Close() error {
	return c.transport.Close()
}

// Name returns the server name.
func (c *Client) Name() string {
	return c.name
}

func (c *Client) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID.Add(1),
		Method:  method,
		Params:  params,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	respBytes, err := c.transport.Send(ctx, reqBytes)
	if err != nil {
		return nil, err
	}

	var resp JSONRPCResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	return resp.Result, nil
}

// HTTPTransport implements MCP over HTTP.
type HTTPTransport struct {
	url    string
	client *http.Client
}

func (t *HTTPTransport) Send(ctx context.Context, msg []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", t.url, strings.NewReader(string(msg)))
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func (t *HTTPTransport) Close() error { return nil }

// StdioTransport implements MCP over subprocess stdio.
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
}

func newStdioTransport(command string, logger *slog.Logger) (*StdioTransport, error) {
	parts := strings.Fields(command)
	cmd := exec.Command(parts[0], parts[1:]...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	logger.Debug("MCP stdio process started", "command", command, "pid", cmd.Process.Pid)

	return &StdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
	}, nil
}

func (t *StdioTransport) Send(ctx context.Context, msg []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Write request with newline delimiter.
	if _, err := t.stdin.Write(append(msg, '\n')); err != nil {
		return nil, err
	}

	// Read response line.
	line, err := t.stdout.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	return line, nil
}

func (t *StdioTransport) Close() error {
	t.stdin.Close()
	return t.cmd.Process.Kill()
}
