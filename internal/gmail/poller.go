package gmail

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	defaultPollInterval = 5 * time.Minute
	maxFetchBatch       = 50
	batchInsertSize     = 10
)

// Poller polls Gmail IMAP for new emails and stores them in SQLite.
// Designed for single-connection, low-memory operation on Pi.
type Poller struct {
	cfg    Config
	db     *sql.DB
	smtp   *SMTPClient
	logger *slog.Logger

	mu           sync.Mutex
	imap         *IMAPClient
	stopChan     chan struct{}
	pollWg       sync.WaitGroup
	shutdownOnce sync.Once
}

// NewPoller creates a new Gmail background poller.
func NewPoller(cfg Config, db *sql.DB, logger *slog.Logger) *Poller {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaultPollInterval
	}
	return &Poller{
		cfg:    cfg,
		db:     db,
		smtp:   NewSMTPClient(cfg.Email, cfg.AppPassword, logger),
		logger: logger,
		stopChan: make(chan struct{}),
	}
}

// SMTP returns the SMTP client for sending emails.
func (p *Poller) SMTP() *SMTPClient {
	return p.smtp
}

// Email returns the configured Gmail address.
func (p *Poller) Email() string {
	return p.cfg.Email
}

// Start begins the background polling loop.
func (p *Poller) Start(ctx context.Context) error {
	p.logger.Info("Gmail poller starting",
		"email", maskEmail(p.cfg.Email),
		"interval", p.cfg.PollInterval,
	)

	p.pollWg.Add(1)
	go p.pollLoop(ctx)
	return nil
}

// Stop gracefully shuts down the poller.
func (p *Poller) Stop() error {
	var err error
	p.shutdownOnce.Do(func() {
		close(p.stopChan)

		done := make(chan struct{})
		go func() {
			p.pollWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			p.logger.Info("Gmail poller stopped")
		case <-time.After(10 * time.Second):
			err = fmt.Errorf("gmail poller shutdown timeout")
		}

		p.mu.Lock()
		if p.imap != nil {
			p.imap.Close()
			p.imap = nil
		}
		p.mu.Unlock()
	})
	return err
}

// pollLoop runs the sync at configured intervals.
func (p *Poller) pollLoop(ctx context.Context) {
	defer p.pollWg.Done()

	// Sync once immediately on startup.
	p.syncEmails(ctx)

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.syncEmails(ctx)
		}
	}
}

// syncEmails fetches new unread emails from Gmail and stores them.
func (p *Poller) syncEmails(ctx context.Context) {
	p.logger.Debug("Gmail sync starting")

	// Check if sync already in progress.
	var inProgress int
	if err := p.db.QueryRowContext(ctx,
		"SELECT sync_in_progress FROM gmail_sync_state WHERE id = 1",
	).Scan(&inProgress); err != nil {
		p.logger.Error("failed to check sync status", "error", err)
		return
	}

	if inProgress == 1 {
		p.logger.Debug("Gmail sync already in progress, skipping")
		return
	}

	// Mark sync in progress.
	if _, err := p.db.ExecContext(ctx,
		"UPDATE gmail_sync_state SET sync_in_progress = 1 WHERE id = 1"); err != nil {
		p.logger.Warn("failed to mark sync in progress", "error", err)
	}
	defer func() {
		if _, err := p.db.ExecContext(context.Background(),
			"UPDATE gmail_sync_state SET sync_in_progress = 0, last_sync_at = datetime('now') WHERE id = 1"); err != nil {
			p.logger.Warn("failed to clear sync in progress", "error", err)
		}
	}()

	// Connect to IMAP (lazy, reconnect on failure).
	p.mu.Lock()
	if p.imap == nil {
		conn, err := Dial(p.cfg.Email, p.cfg.AppPassword, p.logger)
		if err != nil {
			p.mu.Unlock()
			p.logger.Error("Gmail IMAP connect failed", "error", err)
			if _, dbErr := p.db.ExecContext(context.Background(),
				"UPDATE gmail_sync_state SET error_message = ? WHERE id = 1",
				err.Error()); dbErr != nil {
				p.logger.Warn("failed to update sync error state", "error", dbErr)
			}
			return
		}
		p.imap = conn
	}
	imapClient := p.imap
	p.mu.Unlock()

	// Fetch unread emails.
	emails, err := imapClient.FetchUnread(maxFetchBatch)
	if err != nil {
		p.logger.Error("Gmail fetch failed", "error", err)
		// Reset connection on error so next sync reconnects.
		p.mu.Lock()
		if p.imap != nil {
			p.imap.Close()
			p.imap = nil
		}
		p.mu.Unlock()
		if _, dbErr := p.db.ExecContext(context.Background(),
			"UPDATE gmail_sync_state SET error_message = ? WHERE id = 1",
			err.Error()); dbErr != nil {
			p.logger.Warn("failed to update sync error state", "error", dbErr)
		}
		return
	}

	if len(emails) == 0 {
		p.logger.Debug("No new unread emails")
		if _, dbErr := p.db.ExecContext(ctx,
			"UPDATE gmail_sync_state SET error_message = NULL WHERE id = 1"); dbErr != nil {
			p.logger.Warn("failed to clear sync error state", "error", dbErr)
		}
		return
	}

	// Store in batches to limit memory.
	stored := 0
	for i := 0; i < len(emails); i += batchInsertSize {
		end := i + batchInsertSize
		if end > len(emails) {
			end = len(emails)
		}
		batch := emails[i:end]

		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			p.logger.Error("Gmail batch tx failed", "error", err)
			continue
		}

		for _, email := range batch {
			if err := storeEmail(ctx, tx, &email); err != nil {
				p.logger.Warn("Failed to store email",
					"message_id", email.MessageID, "error", err)
				continue
			}
			stored++
		}

		if err := tx.Commit(); err != nil {
			p.logger.Error("Gmail batch commit failed", "error", err)
			tx.Rollback()
		}
	}

	p.logger.Info("Gmail sync completed", "fetched", len(emails), "stored", stored)
	if _, dbErr := p.db.ExecContext(ctx,
		"UPDATE gmail_sync_state SET error_message = NULL WHERE id = 1"); dbErr != nil {
		p.logger.Warn("failed to clear sync error state", "error", dbErr)
	}
}

// storeEmail inserts or updates an email in SQLite.
func storeEmail(ctx context.Context, tx *sql.Tx, e *Email) error {
	_, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO emails
			(id, message_id, thread_id, from_addr, to_addrs, cc_addrs,
			 subject, body_preview, is_read, is_starred, labels,
			 has_attachments, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.MessageID, e.ThreadID, e.From, e.To, e.Cc,
		e.Subject, e.BodyPreview, boolToInt(e.IsRead), boolToInt(e.IsStarred),
		e.Labels, boolToInt(e.HasAttachments),
		e.ReceivedAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// GetEmailByID reads a stored email from SQLite.
func (p *Poller) GetEmailByID(ctx context.Context, emailID string) (*Email, error) {
	var e Email
	var isRead, isStarred, hasAttach int
	var receivedStr string

	err := p.db.QueryRowContext(ctx, `
		SELECT id, message_id, thread_id, from_addr, to_addrs, cc_addrs,
		       subject, body_preview, COALESCE(body_full, ''), is_read, is_starred,
		       labels, has_attachments, received_at
		FROM emails WHERE id = ?`, emailID,
	).Scan(&e.ID, &e.MessageID, &e.ThreadID, &e.From, &e.To, &e.Cc,
		&e.Subject, &e.BodyPreview, &e.BodyFull, &isRead, &isStarred,
		&e.Labels, &hasAttach, &receivedStr)

	if err != nil {
		return nil, fmt.Errorf("email not found: %s", emailID)
	}

	e.IsRead = isRead == 1
	e.IsStarred = isStarred == 1
	e.HasAttachments = hasAttach == 1
	e.ReceivedAt, _ = time.Parse("2006-01-02 15:04:05", receivedStr)

	return &e, nil
}

// GetUnreadCount returns the total number of unread emails in the local store.
func (p *Poller) GetUnreadCount(ctx context.Context) (int, error) {
	var count int
	err := p.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM emails WHERE is_read = 0").Scan(&count)
	return count, err
}

// ListEmails queries stored emails with optional filters.
func (p *Poller) ListEmails(ctx context.Context, unreadOnly bool, limit int) ([]EmailSummary, error) {
	query := `SELECT id, from_addr, subject, body_preview, is_read, has_attachments, received_at
	          FROM emails`
	var args []interface{}

	if unreadOnly {
		query += " WHERE is_read = 0"
	}
	query += " ORDER BY received_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []EmailSummary
	for rows.Next() {
		var s EmailSummary
		var isRead, hasAttach int
		if err := rows.Scan(&s.ID, &s.From, &s.Subject, &s.Preview,
			&isRead, &hasAttach, &s.ReceivedAt); err != nil {
			continue
		}
		s.IsRead = isRead == 1
		s.HasAttachments = hasAttach == 1
		// Truncate preview for list view.
		if len(s.Preview) > 100 {
			s.Preview = s.Preview[:100] + "..."
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// SearchStored searches emails using FTS5 full-text search.
func (p *Poller) SearchStored(ctx context.Context, query string, limit int) ([]EmailSummary, error) {
	// Escape the search query for FTS5 (wrap in quotes, escape internal quotes)
	escapedQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`

	rows, err := p.db.QueryContext(ctx, `
		SELECT e.id, e.from_addr, e.subject, e.body_preview, e.is_read,
		       e.has_attachments, e.received_at
		FROM emails e
		JOIN emails_fts f ON e.rowid = f.rowid
		WHERE emails_fts MATCH ?
		ORDER BY e.received_at DESC
		LIMIT ?`, escapedQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []EmailSummary
	for rows.Next() {
		var s EmailSummary
		var isRead, hasAttach int
		if err := rows.Scan(&s.ID, &s.From, &s.Subject, &s.Preview,
			&isRead, &hasAttach, &s.ReceivedAt); err != nil {
			continue
		}
		s.IsRead = isRead == 1
		s.HasAttachments = hasAttach == 1
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// UpdateLabel updates a label on a stored email.
func (p *Poller) UpdateLabel(ctx context.Context, emailID, label string, add bool) error {
	var labelsJSON string
	err := p.db.QueryRowContext(ctx, "SELECT labels FROM emails WHERE id = ?", emailID).Scan(&labelsJSON)
	if err != nil {
		return fmt.Errorf("email not found: %s", emailID)
	}

	var labels []string
	if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
		return fmt.Errorf("unmarshal labels: %w", err)
	}

	if add {
		// Don't duplicate.
		for _, l := range labels {
			if l == label {
				return nil
			}
		}
		labels = append(labels, label)
	} else {
		filtered := labels[:0]
		for _, l := range labels {
			if l != label {
				filtered = append(filtered, l)
			}
		}
		labels = filtered
	}

	newJSON, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}
	_, err = p.db.ExecContext(ctx, "UPDATE emails SET labels = ? WHERE id = ?",
		string(newJSON), emailID)
	return err
}

// ArchiveEmail removes INBOX label from an email.
func (p *Poller) ArchiveEmail(ctx context.Context, emailID string) error {
	return p.UpdateLabel(ctx, emailID, "INBOX", false)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
