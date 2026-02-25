package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/gmail"
	"github.com/KekwanuLabs/crayfish/internal/security"
)

// RegisterEmailTools adds Gmail email tools to the registry.
// Called only when Gmail is configured.
func RegisterEmailTools(reg *Registry, poller *gmail.Poller) {
	reg.logger.Info("registering email tools", "email", poller.Email())

	// email_check — list new/unread emails.
	reg.Register(&Tool{
		Name:        "email_check",
		Description: "Check for new or unread emails. Returns subject, sender, and preview for each. Use 'unread_only' to filter.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"unread_only": {
					"type": "boolean",
					"description": "Only show unread emails (default: true)"
				},
				"limit": {
					"type": "integer",
					"description": "Maximum emails to return (default: 10, max: 25)"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				UnreadOnly *bool `json:"unread_only"`
				Limit      int   `json:"limit"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("email_check: parse input: %w", err)
			}

			unreadOnly := true
			if params.UnreadOnly != nil {
				unreadOnly = *params.UnreadOnly
			}
			limit := 10
			if params.Limit > 0 && params.Limit <= 25 {
				limit = params.Limit
			}

			summaries, err := poller.ListEmails(ctx, unreadOnly, limit)
			if err != nil {
				return "", fmt.Errorf("email_check: %w", err)
			}

			if len(summaries) == 0 {
				if unreadOnly {
					return "No unread emails.", nil
				}
				return "No emails found.", nil
			}

			result, _ := json.Marshal(summaries)
			return string(result), nil
		},
	})

	// email_read — read a specific email by ID.
	reg.Register(&Tool{
		Name:        "email_read",
		Description: "Read the full content of a specific email by its ID (from email_check results).",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"email_id": {
					"type": "string",
					"description": "The email ID from email_check results"
				}
			},
			"required": ["email_id"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				EmailID string `json:"email_id"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("email_read: parse input: %w", err)
			}
			if params.EmailID == "" {
				return "", fmt.Errorf("email_read: email_id is required")
			}

			email, err := poller.GetEmailByID(ctx, params.EmailID)
			if err != nil {
				return "", fmt.Errorf("email_read: %w", err)
			}

			// Return structured email data.
			body := email.BodyPreview
			if email.BodyFull != "" {
				body = email.BodyFull
			}

			result := map[string]interface{}{
				"id":              email.ID,
				"from":            email.From,
				"to":              email.To,
				"cc":              email.Cc,
				"subject":         email.Subject,
				"body":            body,
				"is_read":         email.IsRead,
				"has_attachments": email.HasAttachments,
				"received_at":     email.ReceivedAt.Format(time.RFC3339),
				"message_id":      email.MessageID,
			}

			out, _ := json.Marshal(result)
			return string(out), nil
		},
	})

	// email_search — search emails by text, sender, date.
	reg.Register(&Tool{
		Name:        "email_search",
		Description: "Search emails using full-text search. Can search by content, sender, subject, or date range.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Search query (searches subject, sender, and body)"
				},
				"from": {
					"type": "string",
					"description": "Filter by sender email or name"
				},
				"since": {
					"type": "string",
					"description": "Only emails after this date (YYYY-MM-DD)"
				},
				"before": {
					"type": "string",
					"description": "Only emails before this date (YYYY-MM-DD)"
				},
				"limit": {
					"type": "integer",
					"description": "Max results (default: 20)"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Query  string `json:"query"`
				From   string `json:"from"`
				Since  string `json:"since"`
				Before string `json:"before"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("email_search: parse input: %w", err)
			}

			limit := 20
			if params.Limit > 0 && params.Limit <= 50 {
				limit = params.Limit
			}

			// Build FTS5 query from params.
			var ftsTerms []string
			if params.Query != "" {
				ftsTerms = append(ftsTerms, params.Query)
			}
			if params.From != "" {
				ftsTerms = append(ftsTerms, fmt.Sprintf("from_addr:%s", params.From))
			}

			if len(ftsTerms) == 0 && params.Since == "" && params.Before == "" {
				return "", fmt.Errorf("email_search: provide at least query, from, since, or before")
			}

			// If we have FTS terms, use FTS5 search.
			if len(ftsTerms) > 0 {
				ftsQuery := strings.Join(ftsTerms, " ")
				summaries, err := poller.SearchStored(ctx, ftsQuery, limit)
				if err != nil {
					return "", fmt.Errorf("email_search: %w", err)
				}
				if len(summaries) == 0 {
					return "No emails match your search.", nil
				}
				result, _ := json.Marshal(summaries)
				return string(result), nil
			}

			// Date-only search falls back to regular query.
			summaries, err := poller.ListEmails(ctx, false, limit)
			if err != nil {
				return "", fmt.Errorf("email_search: %w", err)
			}

			// Filter by date.
			var filtered []gmail.EmailSummary
			for _, s := range summaries {
				t, _ := time.Parse("2006-01-02 15:04:05", s.ReceivedAt)
				if params.Since != "" {
					since, _ := time.Parse("2006-01-02", params.Since)
					if t.Before(since) {
						continue
					}
				}
				if params.Before != "" {
					before, _ := time.Parse("2006-01-02", params.Before)
					if t.After(before) {
						continue
					}
				}
				filtered = append(filtered, s)
			}

			if len(filtered) == 0 {
				return "No emails match your search.", nil
			}
			result, _ := json.Marshal(filtered)
			return string(result), nil
		},
	})

	// email_label — add or remove a label.
	reg.Register(&Tool{
		Name:        "email_label",
		Description: "Add or remove a label/category on an email. Use for organizing: 'Work', 'Personal', 'Bills', 'Important', etc.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"email_id": {
					"type": "string",
					"description": "The email ID"
				},
				"label": {
					"type": "string",
					"description": "Label name to add or remove"
				},
				"action": {
					"type": "string",
					"enum": ["add", "remove"],
					"description": "Whether to add or remove the label (default: add)"
				}
			},
			"required": ["email_id", "label"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				EmailID string `json:"email_id"`
				Label   string `json:"label"`
				Action  string `json:"action"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("email_label: parse input: %w", err)
			}
			if params.EmailID == "" || params.Label == "" {
				return "", fmt.Errorf("email_label: email_id and label are required")
			}

			add := params.Action != "remove"
			if err := poller.UpdateLabel(ctx, params.EmailID, params.Label, add); err != nil {
				return "", fmt.Errorf("email_label: %w", err)
			}

			action := "added"
			if !add {
				action = "removed"
			}
			return fmt.Sprintf("Label '%s' %s on email %s.", params.Label, action, params.EmailID), nil
		},
	})

	// email_archive — archive an email (remove from Inbox).
	reg.Register(&Tool{
		Name:        "email_archive",
		Description: "Archive an email (removes it from Inbox). The email is still in All Mail.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"email_id": {
					"type": "string",
					"description": "The email ID to archive"
				}
			},
			"required": ["email_id"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				EmailID string `json:"email_id"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("email_archive: parse input: %w", err)
			}
			if params.EmailID == "" {
				return "", fmt.Errorf("email_archive: email_id is required")
			}

			if err := poller.ArchiveEmail(ctx, params.EmailID); err != nil {
				return "", fmt.Errorf("email_archive: %w", err)
			}

			return fmt.Sprintf("Email %s archived.", params.EmailID), nil
		},
	})

	// email_send — compose and send a new email.
	reg.Register(&Tool{
		Name:        "email_send",
		Description: "Compose and send a new email. Use this to send emails to anyone. The email is sent from the configured Gmail account.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"to": {
					"type": "string",
					"description": "Recipient email address(es), comma-separated for multiple"
				},
				"subject": {
					"type": "string",
					"description": "Email subject line"
				},
				"body": {
					"type": "string",
					"description": "Email body text"
				}
			},
			"required": ["to", "subject", "body"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				To      string `json:"to"`
				Subject string `json:"subject"`
				Body    string `json:"body"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("email_send: parse input: %w", err)
			}
			if params.To == "" {
				return "", fmt.Errorf("email_send: 'to' is required")
			}
			if params.Subject == "" {
				return "", fmt.Errorf("email_send: 'subject' is required")
			}
			if params.Body == "" {
				return "", fmt.Errorf("email_send: 'body' is required")
			}

			if err := poller.Send(ctx, params.To, params.Subject, params.Body); err != nil {
				return "", fmt.Errorf("email_send: send failed: %w", err)
			}

			return fmt.Sprintf("Email sent to %s with subject: %s", params.To, params.Subject), nil
		},
	})

	// email_reply — draft or send a reply.
	reg.Register(&Tool{
		Name:        "email_reply",
		Description: "Reply to an email. Set 'send' to true to actually send, or false to just draft. The reply is sent from the configured Gmail account.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"email_id": {
					"type": "string",
					"description": "The email ID to reply to"
				},
				"body": {
					"type": "string",
					"description": "Reply message body"
				},
				"send": {
					"type": "boolean",
					"description": "Actually send the reply (default: false, just drafts)"
				}
			},
			"required": ["email_id", "body"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				EmailID string `json:"email_id"`
				Body    string `json:"body"`
				Send    bool   `json:"send"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("email_reply: parse input: %w", err)
			}
			if params.EmailID == "" || params.Body == "" {
				return "", fmt.Errorf("email_reply: email_id and body are required")
			}

			// Look up the original email.
			email, err := poller.GetEmailByID(ctx, params.EmailID)
			if err != nil {
				return "", fmt.Errorf("email_reply: %w", err)
			}

			subject := email.Subject
			if !strings.HasPrefix(strings.ToLower(subject), "re:") {
				subject = "Re: " + subject
			}

			if !params.Send {
				// Draft mode — just show what would be sent.
				draft := map[string]string{
					"to":      email.From,
					"subject": subject,
					"body":    params.Body,
					"status":  "draft",
				}
				out, _ := json.Marshal(draft)
				return string(out), nil
			}

			// Actually send via Gmail API.
			// Extract bare email from "Name <email>" format.
			replyTo := email.From
			if idx := strings.Index(replyTo, "<"); idx >= 0 {
				end := strings.Index(replyTo, ">")
				if end > idx {
					replyTo = replyTo[idx+1 : end]
				}
			}

			if err := poller.SendReply(ctx, replyTo, subject, params.Body, email.MessageID); err != nil {
				return "", fmt.Errorf("email_reply: send failed: %w", err)
			}

			return fmt.Sprintf("Reply sent to %s.", email.From), nil
		},
	})

	// email_summarize — digest of recent emails.
	reg.Register(&Tool{
		Name:        "email_summarize",
		Description: "Get a summary digest of recent emails. Shows count by sender, unread count, and key subjects. Good for daily briefings.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"days": {
					"type": "integer",
					"description": "Number of days back to summarize (default: 1)"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Days int `json:"days"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("email_summarize: unmarshal params: %w", err)
			}

			days := 1
			if params.Days > 0 && params.Days <= 30 {
				days = params.Days
			}

			// Get all emails from the past N days.
			summaries, err := poller.ListEmails(ctx, false, 100)
			if err != nil {
				return "", fmt.Errorf("email_summarize: %w", err)
			}

			cutoff := time.Now().AddDate(0, 0, -days)
			var recent []gmail.EmailSummary
			unread := 0
			senderCount := make(map[string]int)

			for _, s := range summaries {
				t, _ := time.Parse("2006-01-02 15:04:05", s.ReceivedAt)
				if t.Before(cutoff) {
					continue
				}
				recent = append(recent, s)
				if !s.IsRead {
					unread++
				}
				senderCount[s.From]++
			}

			// Build digest.
			digest := map[string]interface{}{
				"period":       fmt.Sprintf("Last %d day(s)", days),
				"total_emails": len(recent),
				"unread":       unread,
				"top_senders":  senderCount,
			}

			// Include subjects of unread emails.
			var unreadSubjects []string
			for _, s := range recent {
				if !s.IsRead && len(unreadSubjects) < 10 {
					unreadSubjects = append(unreadSubjects, fmt.Sprintf("• %s: %s", s.From, s.Subject))
				}
			}
			if len(unreadSubjects) > 0 {
				digest["unread_subjects"] = unreadSubjects
			}

			out, _ := json.Marshal(digest)
			return string(out), nil
		},
	})
}
