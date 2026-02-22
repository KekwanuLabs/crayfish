package gmail

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const (
	gmailIMAPAddr = "imap.gmail.com:993"
	dialTimeout   = 15 * time.Second
	maxBodyBytes  = 4096 // 4KB preview limit for Pi memory safety
)

// IMAPClient wraps go-imap client for Gmail operations.
type IMAPClient struct {
	c      *client.Client
	logger *slog.Logger
}

// Dial connects to Gmail IMAP with TLS and authenticates.
func Dial(email, appPassword string, logger *slog.Logger) (*IMAPClient, error) {
	tlsCfg := &tls.Config{ServerName: "imap.gmail.com"}
	c, err := client.DialTLS(gmailIMAPAddr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("gmail.Dial: %w", err)
	}
	c.Timeout = 30 * time.Second

	if err := c.Login(email, appPassword); err != nil {
		c.Logout()
		return nil, fmt.Errorf("gmail.Login: %w", err)
	}

	logger.Info("Gmail IMAP connected", "email", maskEmail(email))
	return &IMAPClient{c: c, logger: logger}, nil
}

// Close logs out and closes the connection.
func (ic *IMAPClient) Close() error {
	if ic.c != nil {
		return ic.c.Logout()
	}
	return nil
}

// FetchUnread fetches unread emails from INBOX. Returns at most maxCount.
// Only fetches headers + body preview to conserve memory.
func (ic *IMAPClient) FetchUnread(maxCount int) ([]Email, error) {
	mbox, err := ic.c.Select("INBOX", false)
	if err != nil {
		return nil, fmt.Errorf("select INBOX: %w", err)
	}
	if mbox.Messages == 0 {
		return nil, nil
	}

	// Search for unseen messages.
	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	uids, err := ic.c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("search unseen: %w", err)
	}
	if len(uids) == 0 {
		return nil, nil
	}

	// Limit to most recent maxCount.
	if len(uids) > maxCount {
		uids = uids[len(uids)-maxCount:]
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uids...)

	// Fetch envelope + body structure + first body section.
	section := &imap.BodySectionName{Peek: true} // PEEK = don't mark as read
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchUid, section.FetchItem()}

	messages := make(chan *imap.Message, maxCount)
	done := make(chan error, 1)
	go func() {
		done <- ic.c.Fetch(seqSet, items, messages)
	}()

	var emails []Email
	for msg := range messages {
		e, err := parseMessage(msg, section)
		if err != nil {
			ic.logger.Warn("failed to parse email", "uid", msg.Uid, "error", err)
			continue
		}
		emails = append(emails, e)
	}

	if err := <-done; err != nil {
		return emails, fmt.Errorf("fetch: %w", err)
	}

	return emails, nil
}

// FetchByID fetches a single email with full body by its message-ID.
func (ic *IMAPClient) FetchByID(messageID string) (*Email, error) {
	_, err := ic.c.Select("INBOX", true) // read-only
	if err != nil {
		// Try All Mail for archived messages.
		if _, err = ic.c.Select("[Gmail]/All Mail", true); err != nil {
			return nil, fmt.Errorf("select mailbox: %w", err)
		}
	}

	criteria := imap.NewSearchCriteria()
	criteria.Header.Set("Message-Id", messageID)
	uids, err := ic.c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("search by message-id: %w", err)
	}
	if len(uids) == 0 {
		return nil, fmt.Errorf("email not found: %s", messageID)
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uids[0])

	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchUid, section.FetchItem()}

	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- ic.c.Fetch(seqSet, items, messages)
	}()

	var email *Email
	for msg := range messages {
		e, err := parseMessage(msg, section)
		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		// For FetchByID, read full body (up to 64KB).
		e.BodyFull = e.BodyPreview
		if bodySection := msg.GetBody(section); bodySection != nil {
			fullBody, _ := io.ReadAll(io.LimitReader(bodySection, 64*1024))
			parsed, err := mail.ReadMessage(strings.NewReader(string(fullBody)))
			if err == nil {
				body, _ := io.ReadAll(io.LimitReader(parsed.Body, 64*1024))
				e.BodyFull = string(body)
			}
		}
		email = &e
	}

	if err := <-done; err != nil {
		return email, fmt.Errorf("fetch: %w", err)
	}

	return email, nil
}

// SearchEmails searches INBOX using IMAP criteria.
func (ic *IMAPClient) SearchEmails(from, subject, body string, since, before time.Time, maxCount int) ([]Email, error) {
	_, err := ic.c.Select("INBOX", true)
	if err != nil {
		return nil, fmt.Errorf("select INBOX: %w", err)
	}

	criteria := imap.NewSearchCriteria()
	if from != "" {
		criteria.Header.Set("From", from)
	}
	if subject != "" {
		criteria.Header.Set("Subject", subject)
	}
	if body != "" {
		criteria.Body = []string{body}
	}
	if !since.IsZero() {
		criteria.Since = since
	}
	if !before.IsZero() {
		criteria.Before = before
	}

	uids, err := ic.c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if len(uids) == 0 {
		return nil, nil
	}

	if len(uids) > maxCount {
		uids = uids[len(uids)-maxCount:]
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uids...)

	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchUid, section.FetchItem()}

	messages := make(chan *imap.Message, maxCount)
	done := make(chan error, 1)
	go func() {
		done <- ic.c.Fetch(seqSet, items, messages)
	}()

	var emails []Email
	for msg := range messages {
		e, err := parseMessage(msg, section)
		if err != nil {
			continue
		}
		emails = append(emails, e)
	}

	if err := <-done; err != nil {
		return emails, fmt.Errorf("fetch: %w", err)
	}

	return emails, nil
}

// MarkRead marks a message as read by UID.
func (ic *IMAPClient) MarkRead(uid uint32) error {
	_, err := ic.c.Select("INBOX", false)
	if err != nil {
		return fmt.Errorf("select: %w", err)
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.SeenFlag}
	return ic.c.UidStore(seqSet, item, flags, nil)
}

// Archive removes the INBOX label (moves to All Mail on Gmail).
func (ic *IMAPClient) Archive(uid uint32) error {
	_, err := ic.c.Select("INBOX", false)
	if err != nil {
		return fmt.Errorf("select: %w", err)
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	// On Gmail, moving out of INBOX = archive.
	return ic.c.UidMove(seqSet, "[Gmail]/All Mail")
}

// AddLabel copies a message to a Gmail label folder.
func (ic *IMAPClient) AddLabel(uid uint32, label string) error {
	_, err := ic.c.Select("INBOX", false)
	if err != nil {
		return fmt.Errorf("select: %w", err)
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	return ic.c.UidCopy(seqSet, label)
}

// parseMessage converts an imap.Message to our Email type.
func parseMessage(msg *imap.Message, section *imap.BodySectionName) (Email, error) {
	if msg.Envelope == nil {
		return Email{}, fmt.Errorf("no envelope")
	}

	env := msg.Envelope

	// Extract sender.
	from := ""
	if len(env.From) > 0 {
		a := env.From[0]
		if a.PersonalName != "" {
			from = fmt.Sprintf("%s <%s@%s>", a.PersonalName, a.MailboxName, a.HostName)
		} else {
			from = fmt.Sprintf("%s@%s", a.MailboxName, a.HostName)
		}
	}

	// Extract recipients.
	to := formatAddresses(env.To)
	cc := formatAddresses(env.Cc)

	// Extract body preview.
	preview := ""
	if bodySection := msg.GetBody(section); bodySection != nil {
		raw, err := io.ReadAll(io.LimitReader(bodySection, int64(maxBodyBytes)))
		if err == nil {
			// Try to parse as mail message to get just the text body.
			parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
			if err == nil {
				body, _ := io.ReadAll(io.LimitReader(parsed.Body, int64(maxBodyBytes)))
				preview = cleanPreview(string(body))
			} else {
				preview = cleanPreview(string(raw))
			}
		}
	}

	// Check flags.
	isRead := false
	isStarred := false
	for _, flag := range msg.Flags {
		if flag == imap.SeenFlag {
			isRead = true
		}
		if flag == imap.FlaggedFlag {
			isStarred = true
		}
	}

	msgID := env.MessageId
	if msgID == "" {
		msgID = fmt.Sprintf("uid-%d", msg.Uid)
	}

	return Email{
		ID:          fmt.Sprintf("%d", msg.Uid),
		MessageID:   msgID,
		ThreadID:    env.InReplyTo, // Rough threading via In-Reply-To
		From:        from,
		To:          to,
		Cc:          cc,
		Subject:     env.Subject,
		BodyPreview: preview,
		IsRead:      isRead,
		IsStarred:   isStarred,
		Labels:      `["INBOX"]`,
		ReceivedAt:  env.Date,
	}, nil
}

func formatAddresses(addrs []*imap.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	var parts []string
	for _, a := range addrs {
		parts = append(parts, fmt.Sprintf("%s@%s", a.MailboxName, a.HostName))
	}
	return strings.Join(parts, ", ")
}

// cleanPreview strips excessive whitespace and truncates.
func cleanPreview(s string) string {
	// Collapse whitespace.
	fields := strings.Fields(s)
	cleaned := strings.Join(fields, " ")
	if len(cleaned) > 500 {
		cleaned = cleaned[:500] + "..."
	}
	return cleaned
}

// maskEmail redacts the middle of an email for logging.
func maskEmail(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return "***"
	}
	name := parts[0]
	if len(name) <= 2 {
		return "**@" + parts[1]
	}
	return name[:2] + "***@" + parts[1]
}
