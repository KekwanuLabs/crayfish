package gmail

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	gmailIMAPAddr = "imap.gmail.com:993"
	gmailSMTPAddr = "smtp.gmail.com:587"
	imapMaxFetch  = 50
)

// IMAPConfig holds IMAP+SMTP connection settings.
type IMAPConfig struct {
	Email        string
	AppPassword  string
	PollInterval time.Duration
}

// IMAPProvider implements EmailProvider using IMAP for reading and SMTP for sending.
type IMAPProvider struct {
	cfg    IMAPConfig
	db     *sql.DB
	logger *slog.Logger

	stopChan     chan struct{}
	pollWg       sync.WaitGroup
	shutdownOnce sync.Once
}

// NewIMAPProvider creates a new IMAP+SMTP email provider.
func NewIMAPProvider(cfg IMAPConfig, db *sql.DB, logger *slog.Logger) *IMAPProvider {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaultPollInterval
	}
	return &IMAPProvider{
		cfg:      cfg,
		db:       db,
		logger:   logger,
		stopChan: make(chan struct{}),
	}
}

// Email returns the configured email address.
func (p *IMAPProvider) Email() string {
	return p.cfg.Email
}

// TestLogin verifies IMAP credentials by connecting and authenticating.
func (p *IMAPProvider) TestLogin(ctx context.Context) error {
	client, err := p.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Logout().Wait() }()
	return nil
}

// Start begins background IMAP polling.
func (p *IMAPProvider) Start(ctx context.Context) error {
	p.logger.Info("IMAP email provider starting",
		"email", maskEmail(p.cfg.Email),
		"interval", p.cfg.PollInterval,
	)

	p.pollWg.Add(1)
	go p.pollLoop(ctx)
	return nil
}

// Stop gracefully shuts down the provider.
func (p *IMAPProvider) Stop() error {
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
			p.logger.Info("IMAP email provider stopped")
		case <-time.After(10 * time.Second):
			err = fmt.Errorf("IMAP provider shutdown timeout")
		}
	})
	return err
}

// Send sends a new email via SMTP.
func (p *IMAPProvider) Send(ctx context.Context, to, subject, body string) error {
	return p.sendSMTP(to, subject, body, "")
}

// SendReply sends a reply email via SMTP with threading headers.
func (p *IMAPProvider) SendReply(ctx context.Context, to, subject, body, inReplyTo string) error {
	return p.sendSMTP(to, subject, body, inReplyTo)
}

// ListEmails queries stored emails from SQLite.
func (p *IMAPProvider) ListEmails(ctx context.Context, unreadOnly bool, limit int) ([]EmailSummary, error) {
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
		if len(s.Preview) > 100 {
			s.Preview = s.Preview[:100] + "..."
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// GetEmailByID reads a stored email from SQLite. Falls back to IMAP fetch if body not cached.
func (p *IMAPProvider) GetEmailByID(ctx context.Context, emailID string) (*Email, error) {
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

// SearchStored searches emails using FTS5 full-text search.
func (p *IMAPProvider) SearchStored(ctx context.Context, query string, limit int) ([]EmailSummary, error) {
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

// GetUnreadCount returns the number of unread emails.
func (p *IMAPProvider) GetUnreadCount(ctx context.Context) (int, error) {
	var count int
	err := p.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM emails WHERE is_read = 0").Scan(&count)
	return count, err
}

// UpdateLabel updates a label on a stored email (local-only for IMAP provider).
func (p *IMAPProvider) UpdateLabel(ctx context.Context, emailID, label string, add bool) error {
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

// ArchiveEmail marks an email as archived (local-only for IMAP).
func (p *IMAPProvider) ArchiveEmail(ctx context.Context, emailID string) error {
	return p.UpdateLabel(ctx, emailID, "INBOX", false)
}

// --- IMAP internals ---

// connect creates a new IMAP connection and logs in.
func (p *IMAPProvider) connect(ctx context.Context) (*imapclient.Client, error) {
	dialer := &tls.Dialer{
		Config: &tls.Config{ServerName: "imap.gmail.com"},
	}

	conn, err := dialer.DialContext(ctx, "tcp", gmailIMAPAddr)
	if err != nil {
		return nil, fmt.Errorf("imap connect: %w", err)
	}

	client := imapclient.New(conn, nil)

	if err := client.Login(p.cfg.Email, p.cfg.AppPassword).Wait(); err != nil {
		client.Close()
		return nil, fmt.Errorf("imap login: %w", err)
	}

	return client, nil
}

// pollLoop runs IMAP sync at configured intervals.
func (p *IMAPProvider) pollLoop(ctx context.Context) {
	defer p.pollWg.Done()

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

// syncEmails fetches new emails from IMAP and stores them in SQLite.
func (p *IMAPProvider) syncEmails(ctx context.Context) {
	p.logger.Debug("IMAP sync starting")

	client, err := p.connect(ctx)
	if err != nil {
		p.logger.Error("IMAP sync connect failed", "error", err)
		return
	}
	defer func() { _ = client.Logout().Wait() }()

	// Select INBOX.
	mbox, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		p.logger.Error("IMAP select INBOX failed", "error", err)
		return
	}

	if mbox.NumMessages == 0 {
		p.logger.Debug("INBOX is empty")
		return
	}

	// Search for unseen messages.
	criteria := &imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}
	searchData, err := client.Search(criteria, nil).Wait()
	if err != nil {
		p.logger.Error("IMAP search failed", "error", err)
		return
	}

	seqNums := searchData.AllSeqNums()
	if len(seqNums) == 0 {
		p.logger.Debug("no unseen messages")
		return
	}

	// Limit fetch batch.
	if len(seqNums) > imapMaxFetch {
		seqNums = seqNums[len(seqNums)-imapMaxFetch:]
	}

	// Build sequence set from results.
	seqSet := imap.SeqSetNum(seqNums...)

	// Fetch envelope + body text.
	bodySection := &imap.FetchItemBodySection{
		Specifier: imap.PartSpecifierText,
		Peek:      true,
	}
	fetchOpts := &imap.FetchOptions{
		Envelope:    true,
		Flags:       true,
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}

	messages, err := client.Fetch(seqSet, fetchOpts).Collect()
	if err != nil {
		p.logger.Error("IMAP fetch failed", "error", err)
		return
	}

	stored := 0
	for _, msg := range messages {
		email := p.imapToEmail(msg, bodySection)
		if email == nil {
			continue
		}

		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			p.logger.Warn("IMAP store tx failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if err := storeEmail(ctx, tx, email); err != nil {
			p.logger.Warn("failed to store IMAP email", "id", email.ID, "error", err)
			tx.Rollback()
			continue
		}

		if err := tx.Commit(); err != nil {
			p.logger.Warn("IMAP store commit failed", "error", err)
			tx.Rollback()
			continue
		}
		stored++
	}

	p.logger.Info("IMAP sync completed", "fetched", len(messages), "stored", stored)
}

// imapToEmail converts a fetched IMAP message to our Email type.
func (p *IMAPProvider) imapToEmail(msg *imapclient.FetchMessageBuffer, bodySection *imap.FetchItemBodySection) *Email {
	if msg.Envelope == nil {
		return nil
	}

	env := msg.Envelope

	// Build email ID from UID.
	emailID := fmt.Sprintf("imap-%d", msg.UID)

	// Format addresses.
	from := formatAddresses(env.From)
	to := formatAddresses(env.To)
	cc := formatAddresses(env.Cc)

	// Extract body preview.
	var bodyPreview string
	if bodyData := msg.FindBodySection(bodySection); bodyData != nil {
		bodyPreview = cleanPreview(string(bodyData))
	}

	// Check flags.
	isRead := false
	isStarred := false
	var labels []string
	labels = append(labels, "INBOX")
	for _, f := range msg.Flags {
		switch f {
		case imap.FlagSeen:
			isRead = true
		case imap.FlagFlagged:
			isStarred = true
		}
	}

	labelsJSON, _ := json.Marshal(labels)

	messageID := env.MessageID

	return &Email{
		ID:          emailID,
		MessageID:   messageID,
		ThreadID:    messageID, // IMAP doesn't have thread IDs like Gmail; use message ID.
		From:        from,
		To:          to,
		Cc:          cc,
		Subject:     env.Subject,
		BodyPreview: bodyPreview,
		BodyFull:    bodyPreview, // IMAP gives us the full body text in the fetch.
		IsRead:      isRead,
		IsStarred:   isStarred,
		Labels:      string(labelsJSON),
		ReceivedAt:  env.Date,
	}
}

// formatAddresses converts IMAP addresses to "Name <email>" format.
func formatAddresses(addrs []imap.Address) string {
	var parts []string
	for _, a := range addrs {
		addr := a.Addr()
		if addr == "" {
			continue
		}
		if a.Name != "" {
			parts = append(parts, fmt.Sprintf("%s <%s>", a.Name, addr))
		} else {
			parts = append(parts, addr)
		}
	}
	return strings.Join(parts, ", ")
}

// --- SMTP internals ---

// sendSMTP sends an email via Gmail SMTP with STARTTLS.
func (p *IMAPProvider) sendSMTP(to, subject, body, inReplyTo string) error {
	// Connect to SMTP server.
	conn, err := net.DialTimeout("tcp", gmailSMTPAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}

	host := "smtp.gmail.com"
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer client.Close()

	// STARTTLS.
	if err := client.StartTLS(&tls.Config{ServerName: host}); err != nil {
		return fmt.Errorf("smtp starttls: %w", err)
	}

	// Auth with PLAIN.
	auth := smtp.PlainAuth("", p.cfg.Email, p.cfg.AppPassword, host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}

	// Set sender.
	if err := client.Mail(p.cfg.Email); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}

	// Set recipients.
	recipients := strings.Split(to, ",")
	for _, rcpt := range recipients {
		rcpt = strings.TrimSpace(rcpt)
		if rcpt != "" {
			if err := client.Rcpt(rcpt); err != nil {
				return fmt.Errorf("smtp rcpt to %s: %w", rcpt, err)
			}
		}
	}

	// Build message.
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("From: %s\r\n", p.cfg.Email))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	if inReplyTo != "" {
		msg.WriteString(fmt.Sprintf("In-Reply-To: <%s>\r\n", inReplyTo))
		msg.WriteString(fmt.Sprintf("References: <%s>\r\n", inReplyTo))
	}
	msg.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	msg.WriteString("\r\n")
	msg.WriteString(body)

	if _, err := io.WriteString(w, msg.String()); err != nil {
		return fmt.Errorf("smtp write body: %w", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}

	return client.Quit()
}
