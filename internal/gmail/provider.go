package gmail

import "context"

// EmailProvider abstracts email operations so that different backends
// (Gmail REST API via OAuth, IMAP+SMTP via App Password) can be used
// interchangeably by the email tools.
type EmailProvider interface {
	// Email returns the configured email address.
	Email() string

	// Start begins background polling/sync.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the provider.
	Stop() error

	// ListEmails queries stored emails with optional filters.
	ListEmails(ctx context.Context, unreadOnly bool, limit int) ([]EmailSummary, error)

	// GetEmailByID reads a specific email by ID.
	GetEmailByID(ctx context.Context, emailID string) (*Email, error)

	// SearchStored searches emails using full-text search.
	SearchStored(ctx context.Context, query string, limit int) ([]EmailSummary, error)

	// GetUnreadCount returns the number of unread emails.
	GetUnreadCount(ctx context.Context) (int, error)

	// Send sends a new email.
	Send(ctx context.Context, to, subject, body string) error

	// SendReply sends a reply with proper threading headers.
	SendReply(ctx context.Context, to, subject, body, inReplyTo string) error

	// UpdateLabel adds or removes a label on an email.
	UpdateLabel(ctx context.Context, emailID, label string, add bool) error

	// ArchiveEmail removes the email from the inbox.
	ArchiveEmail(ctx context.Context, emailID string) error
}
