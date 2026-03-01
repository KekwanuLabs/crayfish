// Package security implements the capability-based trust tier system.
// Every inbound message is untrusted by default. Trust is earned through
// explicit pairing (Operator) or allowlisting (Trusted).
package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// TrustTier represents a user's capability level.
type TrustTier int

const (
	// TierUnknown is the default for unrecognized senders. Pairing flow only.
	TierUnknown TrustTier = iota
	// TierGroup allows read-only tool access in group chats.
	TierGroup
	// TierTrusted allows read + approved write tools for allowlisted users.
	TierTrusted
	// TierOperator has full access — the device owner paired via CLI.
	TierOperator
)

// String returns the human-readable name of a trust tier.
func (t TrustTier) String() string {
	switch t {
	case TierUnknown:
		return "unknown"
	case TierGroup:
		return "group"
	case TierTrusted:
		return "trusted"
	case TierOperator:
		return "operator"
	default:
		return "invalid"
	}
}

// Session represents an active user session with its trust context.
type Session struct {
	ID           string    `json:"id"`
	Channel      string    `json:"channel"`
	UserID       string    `json:"user_id"`
	Trust        TrustTier `json:"trust"`
	Paired       bool      `json:"paired"`
	AllowedTools []string  `json:"allowed_tools"`
	CreatedAt    time.Time `json:"created_at"`
	LastActive   time.Time `json:"last_active"`
}

// SessionStore manages session persistence and trust resolution.
type SessionStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSessionStore creates a new session store backed by SQLite.
func NewSessionStore(db *sql.DB, logger *slog.Logger) *SessionStore {
	return &SessionStore{db: db, logger: logger}
}

// Resolve finds or creates a session for the given channel and user.
// New sessions default to TierUnknown (no capabilities).
func (s *SessionStore) Resolve(ctx context.Context, channel, userID string) (*Session, error) {
	sessionID := fmt.Sprintf("%s:%s", channel, userID)

	var sess Session
	var allowedToolsJSON string
	var createdStr, activeStr string

	err := s.db.QueryRowContext(ctx,
		"SELECT id, channel, user_id, trust_tier, paired, allowed_tools, created_at, last_active FROM sessions WHERE id = ?",
		sessionID,
	).Scan(&sess.ID, &sess.Channel, &sess.UserID, &sess.Trust, &sess.Paired,
		&allowedToolsJSON, &createdStr, &activeStr)

	if err == sql.ErrNoRows {
		// System channel sessions (scheduler, heartbeat, etc.) are internal — grant operator trust.
		// All other new sessions start at unknown and must go through the pairing flow.
		trust := TierUnknown
		if channel == "system" {
			trust = TierOperator
		}
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		_, err = s.db.ExecContext(ctx,
			"INSERT INTO sessions (id, channel, user_id, trust_tier, paired, allowed_tools, created_at, last_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			sessionID, channel, userID, int(trust), 0, "[]", now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("security.Resolve: insert: %w", err)
		}
		s.logger.Info("new session created", "session_id", sessionID, "trust", trust)
		return &Session{
			ID:           sessionID,
			Channel:      channel,
			UserID:       userID,
			Trust:        trust,
			Paired:       false,
			AllowedTools: []string{},
			CreatedAt:    time.Now().UTC(),
			LastActive:   time.Now().UTC(),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("security.Resolve: query: %w", err)
	}

	if err := json.Unmarshal([]byte(allowedToolsJSON), &sess.AllowedTools); err != nil {
		s.logger.Warn("failed to unmarshal session allowed_tools", "session_id", sessionID, "error", err)
	}
	sess.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdStr)
	sess.LastActive, _ = time.Parse("2006-01-02 15:04:05", activeStr)

	// Upgrade existing system sessions that were created before this fix.
	if channel == "system" && sess.Trust < TierOperator {
		sess.Trust = TierOperator
		s.db.ExecContext(ctx, "UPDATE sessions SET trust_tier = ? WHERE id = ?", int(TierOperator), sessionID) //nolint:errcheck
	}

	// Update last active timestamp.
	if _, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET last_active = datetime('now') WHERE id = ?", sessionID); err != nil {
		s.logger.Warn("failed to update session last_active", "session_id", sessionID, "error", err)
	}

	return &sess, nil
}

// SetTrust updates the trust tier for a session.
func (s *SessionStore) SetTrust(ctx context.Context, sessionID string, tier TrustTier) error {
	paired := 0
	if tier == TierOperator {
		paired = 1
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET trust_tier = ?, paired = ? WHERE id = ?",
		int(tier), paired, sessionID,
	)
	if err != nil {
		return fmt.Errorf("security.SetTrust: %w", err)
	}
	s.logger.Info("trust tier updated", "session_id", sessionID, "tier", tier)
	return nil
}

// CanUseTool checks whether a session's trust tier allows using a specific tool.
func CanUseTool(sess *Session, toolName string, toolMinTier TrustTier) bool {
	return sess.Trust >= toolMinTier
}

// WrapUserMessage adds boundary markers to user input for prompt injection defense.
// System prompts remain outside these markers and are immutable.
func WrapUserMessage(text string) string {
	return fmt.Sprintf("<user_message>\n%s\n</user_message>", text)
}
