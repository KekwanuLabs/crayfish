// Package bus implements the CrayfishBus — an append-only event stream
// backed by SQLite. It decouples message ingestion from processing and
// enables crash recovery via event replay.
package bus

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Event types used throughout the system.
const (
	TypeMessageInbound  = "message.inbound"
	TypeMessageOutbound = "message.outbound"
	TypeToolRequest     = "tool.request"
	TypeToolResult      = "tool.result"
	TypeSystemStartup   = "system.startup"
	TypeSystemShutdown  = "system.shutdown"
)

// Event represents a single entry in the append-only event log.
type Event struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	Channel   string    `json:"channel"`
	SessionID string    `json:"session_id"`
	Payload   []byte    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

// Bus is the CrayfishBus interface — publish, subscribe, and replay events.
type Bus interface {
	// Publish appends an event to the log and notifies all matching subscribers.
	Publish(ctx context.Context, event Event) (int64, error)

	// Subscribe returns a channel that receives events matching the given types.
	// An empty types slice subscribes to all event types.
	Subscribe(ctx context.Context, types []string) (<-chan Event, error)

	// Replay returns all events from the given ID onward for crash recovery.
	Replay(ctx context.Context, fromID int64) ([]Event, error)

	// LastID returns the highest event ID in the log.
	LastID(ctx context.Context) (int64, error)

	// Close shuts down the bus and all subscriptions.
	Close() error
}

// subscriber holds a filtered event channel.
type subscriber struct {
	ch    chan Event
	types map[string]bool // empty map = all types
	once  sync.Once       // guards channel close to prevent double-close panic
}

// SQLiteBus implements Bus on top of the events table in SQLite.
type SQLiteBus struct {
	db          *sql.DB
	logger      *slog.Logger
	mu          sync.RWMutex
	subscribers []*subscriber
	closed      bool
}

// NewSQLiteBus creates a new CrayfishBus backed by the given database.
func NewSQLiteBus(db *sql.DB, logger *slog.Logger) *SQLiteBus {
	return &SQLiteBus{
		db:     db,
		logger: logger,
	}
}

// Publish inserts an event into the log and fans out to subscribers.
func (b *SQLiteBus) Publish(ctx context.Context, event Event) (int64, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	result, err := b.db.ExecContext(ctx,
		"INSERT INTO events (type, channel, session_id, payload, created_at) VALUES (?, ?, ?, ?, ?)",
		event.Type, event.Channel, event.SessionID, string(event.Payload), now,
	)
	if err != nil {
		return 0, fmt.Errorf("bus.Publish: insert: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("bus.Publish: last id: %w", err)
	}

	event.ID = id
	event.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", now)

	b.fanout(event)
	return id, nil
}

// Subscribe creates a new subscription for the given event types.
func (b *SQLiteBus) Subscribe(ctx context.Context, types []string) (<-chan Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, fmt.Errorf("bus.Subscribe: bus is closed")
	}

	typeMap := make(map[string]bool, len(types))
	for _, t := range types {
		typeMap[t] = true
	}

	ch := make(chan Event, 64) // Buffered to avoid blocking the publisher.
	b.subscribers = append(b.subscribers, &subscriber{ch: ch, types: typeMap})

	// Close the channel when the context is cancelled.
	go func() {
		<-ctx.Done()
		b.removeSubscriber(ch)
	}()

	return ch, nil
}

// Replay reads events starting from fromID for crash recovery.
func (b *SQLiteBus) Replay(ctx context.Context, fromID int64) ([]Event, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, type, channel, session_id, payload, created_at FROM events WHERE id > ? ORDER BY id ASC",
		fromID,
	)
	if err != nil {
		return nil, fmt.Errorf("bus.Replay: query: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var createdStr string
		if err := rows.Scan(&e.ID, &e.Type, &e.Channel, &e.SessionID, &e.Payload, &createdStr); err != nil {
			return nil, fmt.Errorf("bus.Replay: scan: %w", err)
		}
		e.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdStr)
		events = append(events, e)
	}
	return events, rows.Err()
}

// LastID returns the most recent event ID.
func (b *SQLiteBus) LastID(ctx context.Context) (int64, error) {
	var id int64
	err := b.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM events").Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("bus.LastID: %w", err)
	}
	return id, nil
}

// Close marks the bus as closed and drains all subscriber channels.
func (b *SQLiteBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true
	for _, s := range b.subscribers {
		s.once.Do(func() { close(s.ch) })
	}
	b.subscribers = nil
	return nil
}

// fanout sends an event to all matching subscribers, non-blocking.
func (b *SQLiteBus) fanout(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, s := range b.subscribers {
		if len(s.types) == 0 || s.types[event.Type] {
			select {
			case s.ch <- event:
			default:
				b.logger.Warn("subscriber channel full, dropping event",
					"type", event.Type, "event_id", event.ID)
			}
		}
	}
}

// removeSubscriber removes and closes a subscriber channel.
func (b *SQLiteBus) removeSubscriber(ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, s := range b.subscribers {
		if s.ch == ch {
			s.once.Do(func() { close(s.ch) })
			b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
			return
		}
	}
}

// MustJSON marshals v to JSON bytes, panicking on failure (for known-good types).
func MustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("bus.MustJSON: %v", err))
	}
	return data
}

// InboundMessage is the payload for message.inbound events.
type InboundMessage struct {
	From    string `json:"from"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// OutboundMessage is the payload for message.outbound events.
type OutboundMessage struct {
	To   string `json:"to"`
	Text string `json:"text"`
}
