// Package cli implements the CLI channel adapter for local development and debugging.
// It reads from stdin and writes to stdout, treating the local user as the Operator.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/KekwanuLabs/crayfish/internal/bus"
	"github.com/KekwanuLabs/crayfish/internal/channels"
)

const (
	adapterName  = "cli"
	cliUserID    = "operator"
	cliSessionID = "cli:operator"
)

// Adapter implements channels.ChannelAdapter for stdin/stdout interaction.
type Adapter struct {
	logger   *slog.Logger
	eventBus bus.Bus
	stdin    io.Reader
	stdout   io.Writer
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// New creates a new CLI adapter. Defaults to os.Stdin/os.Stdout.
func New(logger *slog.Logger) *Adapter {
	return &Adapter{
		logger: logger,
		stdin:  os.Stdin,
		stdout: os.Stdout,
	}
}

// NewWithIO creates a CLI adapter with custom I/O streams (useful for testing).
func NewWithIO(logger *slog.Logger, in io.Reader, out io.Writer) *Adapter {
	return &Adapter{
		logger: logger,
		stdin:  in,
		stdout: out,
	}
}

// Name returns "cli".
func (a *Adapter) Name() string { return adapterName }

// Start begins reading from stdin and publishing messages to the bus.
func (a *Adapter) Start(ctx context.Context, b bus.Bus) error {
	a.eventBus = b

	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	a.wg.Add(1)
	go a.readLoop(ctx)

	a.logger.Info("CLI adapter started", "session_id", cliSessionID)
	return nil
}

// Stop shuts down the CLI adapter.
func (a *Adapter) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	a.logger.Info("CLI adapter stopped")
	return nil
}

// Send writes a message to stdout.
func (a *Adapter) Send(ctx context.Context, msg channels.OutboundMessage) error {
	_, err := fmt.Fprintf(a.stdout, "\n%s\n\n", msg.Text)
	return err
}

// readLoop continuously reads lines from stdin and publishes them as inbound events.
func (a *Adapter) readLoop(ctx context.Context) {
	defer a.wg.Done()
	scanner := bufio.NewScanner(a.stdin)
	scanner.Buffer(make([]byte, 64*1024), 64*1024) // 64KB max line

	fmt.Fprintf(a.stdout, "Crayfish — AI for the rest of us. Type a message (Ctrl+D to quit)\n> ")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				a.logger.Error("stdin read error", "error", err)
			}
			return // EOF or error
		}

		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			fmt.Fprintf(a.stdout, "> ")
			continue
		}

		// Special commands.
		if text == "/quit" || text == "/exit" {
			fmt.Fprintf(a.stdout, "Goodbye!\n")
			if a.cancel != nil {
				a.cancel()
			}
			return
		}

		// Publish inbound message event.
		payload := bus.MustJSON(bus.InboundMessage{
			From: cliUserID,
			Text: text,
		})

		_, err := a.eventBus.Publish(ctx, bus.Event{
			Type:      bus.TypeMessageInbound,
			Channel:   adapterName,
			SessionID: cliSessionID,
			Payload:   payload,
		})
		if err != nil {
			a.logger.Error("failed to publish inbound message", "error", err)
			fmt.Fprintf(a.stdout, "⚠️  Error: %v\n> ", err)
		}
	}
}
