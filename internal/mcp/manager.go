package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
)

// ServerConfig defines an MCP server to connect to.
type ServerConfig struct {
	Name    string `json:"name" yaml:"name"`       // Unique identifier (e.g., "github", "notion")
	Command string `json:"command" yaml:"command"` // Stdio command or HTTP URL
	Enabled bool   `json:"enabled" yaml:"enabled"` // Whether to connect on startup
}

// Manager manages multiple MCP server connections.
type Manager struct {
	clients map[string]*Client
	logger  *slog.Logger
	mu      sync.RWMutex
}

// NewManager creates an MCP manager.
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		clients: make(map[string]*Client),
		logger:  logger,
	}
}

// Connect establishes a connection to an MCP server.
func (m *Manager) Connect(ctx context.Context, cfg ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already connected.
	if _, exists := m.clients[cfg.Name]; exists {
		return fmt.Errorf("mcp: server %q already connected", cfg.Name)
	}

	client, err := NewClient(cfg.Name, cfg.Command, m.logger)
	if err != nil {
		return err
	}

	if err := client.Initialize(ctx); err != nil {
		client.Close()
		return err
	}

	m.clients[cfg.Name] = client
	m.logger.Info("MCP server connected", "name", cfg.Name, "tools", len(client.Tools()))
	return nil
}

// Disconnect closes an MCP server connection.
func (m *Manager) Disconnect(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, exists := m.clients[name]
	if !exists {
		return fmt.Errorf("mcp: server %q not connected", name)
	}

	delete(m.clients, name)
	return client.Close()
}

// AllTools returns all tools from all connected MCP servers.
// Tool names are prefixed with server name (e.g., "github.create_issue").
func (m *Manager) AllTools() []ToolDef {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []ToolDef
	for name, client := range m.clients {
		for _, tool := range client.Tools() {
			// Prefix tool name with server name for disambiguation.
			prefixed := ToolDef{
				Name:        name + "." + tool.Name,
				Description: fmt.Sprintf("[%s] %s", name, tool.Description),
				InputSchema: tool.InputSchema,
			}
			all = append(all, prefixed)
		}
	}
	return all
}

// CallTool invokes a tool. Name format: "server.tool_name".
func (m *Manager) CallTool(ctx context.Context, fullName string, args json.RawMessage) (string, error) {
	// Parse "server.tool" format.
	var serverName, toolName string
	for i := 0; i < len(fullName); i++ {
		if fullName[i] == '.' {
			serverName = fullName[:i]
			toolName = fullName[i+1:]
			break
		}
	}

	if serverName == "" || toolName == "" {
		return "", fmt.Errorf("mcp: invalid tool name %q (expected 'server.tool')", fullName)
	}

	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("mcp: server %q not connected", serverName)
	}

	return client.CallTool(ctx, toolName, args)
}

// Servers returns the names of all connected servers.
func (m *Manager) Servers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var names []string
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

// Close disconnects all servers.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, client := range m.clients {
		if err := client.Close(); err != nil {
			m.logger.Warn("error closing MCP client", "name", name, "error", err)
		}
	}
	m.clients = make(map[string]*Client)
	return nil
}
