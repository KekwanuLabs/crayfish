// Package gmail provides Gmail integration via the Gmail REST API for Crayfish.
// Designed for low-memory environments (Pi 1GB): fetches email previews,
// stores them in SQLite, fetches full bodies on demand.
package gmail

import "time"

// Email represents a stored Gmail message.
type Email struct {
	ID             string       `json:"id"`
	MessageID      string       `json:"message_id"`
	ThreadID       string       `json:"thread_id"`
	From           string       `json:"from"`
	To             string       `json:"to"`
	Cc             string       `json:"cc"`
	Subject        string       `json:"subject"`
	BodyPreview    string       `json:"body_preview"`
	BodyFull       string       `json:"body_full,omitempty"`
	IsRead         bool         `json:"is_read"`
	IsStarred      bool         `json:"is_starred"`
	Labels         string       `json:"labels"` // JSON array, e.g. ["INBOX","Work"]
	HasAttachments bool         `json:"has_attachments"`
	ReceivedAt     time.Time    `json:"received_at"`
	StoredAt       time.Time    `json:"stored_at"`
	Attachments    []Attachment `json:"attachments,omitempty"`
}

// EmailSummary is a lightweight view returned by email_check.
type EmailSummary struct {
	ID             string `json:"id"`
	From           string `json:"from"`
	Subject        string `json:"subject"`
	Preview        string `json:"preview"`
	IsRead         bool   `json:"is_read"`
	HasAttachments bool   `json:"has_attachments"`
	ReceivedAt     string `json:"received_at"`
}

// Attachment holds metadata about an email attachment (no binary data stored).
type Attachment struct {
	ID        int64  `json:"id"`
	EmailID   string `json:"email_id"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
}

// SyncState tracks sync progress.
type SyncState struct {
	LastSyncAt     time.Time
	SyncInProgress bool
	ErrorMessage   string
}

// Config holds Gmail connection settings.
type Config struct {
	Email        string
	PollInterval time.Duration
}
