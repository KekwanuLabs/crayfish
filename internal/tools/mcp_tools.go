package tools

import (
	"context"
	"encoding/json"

	"github.com/KekwanuLabs/crayfish/internal/mcp"
	"github.com/KekwanuLabs/crayfish/internal/security"
)

// RegisterMCPTools registers all tools from connected MCP servers.
// Call this after MCP servers are connected.
func RegisterMCPTools(reg *Registry, mcpMgr *mcp.Manager) {
	for _, tool := range mcpMgr.AllTools() {
		// Capture for closure.
		t := tool

		reg.Register(&Tool{
			Name:        "mcp_" + t.Name,
			Description: t.Description,
			MinTier:     security.TierOperator, // MCP tools require elevated trust.
			InputSchema: t.InputSchema,
			Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
				// Strip "mcp_" prefix to get the actual MCP tool name.
				mcpToolName := t.Name
				return mcpMgr.CallTool(ctx, mcpToolName, input)
			},
		})
	}

	reg.logger.Info("registered MCP tools", "count", len(mcpMgr.AllTools()))
}
