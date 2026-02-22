// Package channels defines the common interface for all messaging channel adapters.
// Each adapter (Telegram, CLI, WhatsApp, SMS) implements this interface.
package channels

import (
	"context"

	"github.com/KekwanuLabs/crayfish/internal/bus"
)

// OutboundMessage is a message to be sent via a channel adapter.
type OutboundMessage struct {
	To   string `json:"to"`
	Text string `json:"text"`
}

// ChannelAdapter is the interface all channel adapters must implement.
type ChannelAdapter interface {
	// Name returns the adapter identifier (e.g., "telegram", "cli").
	Name() string

	// Start begins listening for inbound messages and publishing them to the bus.
	Start(ctx context.Context, b bus.Bus) error

	// Stop gracefully shuts down the adapter.
	Stop() error

	// Send delivers an outbound message via this channel.
	Send(ctx context.Context, msg OutboundMessage) error
}
