package heartbeat

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	maxAutoReplies       = 3
	autoReplyCooldown    = 30 * time.Minute
	autoReplyMaxTokens   = 512
)

// trackedThread represents a thread Crayfish initiated or replied to.
type trackedThread struct {
	ThreadID       string
	LastEmailID    string
	ToAddr         string
	Subject        string
	ReplyCount     int
	LastAutoReply  *time.Time
	Active         bool
}

// CheckAutoReply is called by the sync callback to detect replies to threads
// Crayfish started and auto-reply if enabled.
func (s *Service) CheckAutoReply(ctx context.Context, emailIDs []string) {
	s.mu.Lock()
	enabled := s.autoReplyEnabled
	s.mu.Unlock()

	if !enabled || s.email == nil || s.db == nil || s.llmComplete == nil {
		return
	}

	// Load active tracked threads.
	threads, err := s.loadActiveThreads(ctx)
	if err != nil {
		s.logger.Warn("auto-reply: failed to load tracked threads", "error", err)
		return
	}
	if len(threads) == 0 {
		return
	}

	// Build lookup maps for fast matching.
	byThreadID := make(map[string]*trackedThread)
	bySubject := make(map[string]*trackedThread)
	for i := range threads {
		t := &threads[i]
		byThreadID[t.ThreadID] = t
		// Normalize subject for matching.
		bySubject[normalizeSubject(t.Subject)] = t
	}

	for _, id := range emailIDs {
		email, err := s.email.GetEmailByID(ctx, id)
		if err != nil {
			s.logger.Warn("auto-reply: failed to fetch email", "id", id, "error", err)
			continue
		}

		// Loop prevention layer 1: skip emails from self.
		if s.selfEmail != "" && strings.Contains(strings.ToLower(email.From), strings.ToLower(s.selfEmail)) {
			continue
		}

		// Match against tracked threads by thread ID or normalized subject.
		var matched *trackedThread
		if email.ThreadID != "" {
			matched = byThreadID[email.ThreadID]
		}
		if matched == nil {
			matched = bySubject[normalizeSubject(email.Subject)]
		}
		if matched == nil {
			continue
		}

		// Loop prevention layer 2: max replies reached.
		if matched.ReplyCount >= maxAutoReplies {
			continue
		}

		// Loop prevention layer 3: cooldown.
		if matched.LastAutoReply != nil && time.Since(*matched.LastAutoReply) < autoReplyCooldown {
			continue
		}

		go s.generateAndSendReply(ctx, email.ID, email.From, email.Subject,
			email.BodyPreview, email.MessageID, matched)
	}
}

// generateAndSendReply creates an LLM-generated reply and sends it.
func (s *Service) generateAndSendReply(ctx context.Context, emailID, from, subject, body, messageID string, thread *trackedThread) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	systemPrompt := fmt.Sprintf(`You are replying to an email on behalf of your user. This is a thread you started.
Original subject: %s
Keep your reply concise, professional, and in your user's voice.
Do not mention that you are an AI or assistant. Reply naturally as if you are the user.
Maximum 3-4 sentences.`, thread.Subject)

	userPrompt := fmt.Sprintf("Reply from %s:\n\n%s\n\nWrite a natural reply.", shortName(from), body)

	reply, err := s.llmComplete(ctx, systemPrompt, userPrompt)
	if err != nil {
		s.logger.Warn("auto-reply: LLM generation failed", "error", err, "thread", thread.ThreadID)
		return
	}

	reply = strings.TrimSpace(reply)
	if reply == "" {
		return
	}

	// Extract bare email address.
	replyTo := from
	if idx := strings.Index(replyTo, "<"); idx >= 0 {
		end := strings.Index(replyTo, ">")
		if end > idx {
			replyTo = replyTo[idx+1 : end]
		}
	}

	// Build reply subject.
	replySubject := subject
	if !strings.HasPrefix(strings.ToLower(replySubject), "re:") {
		replySubject = "Re: " + replySubject
	}

	if err := s.email.SendReply(ctx, replyTo, replySubject, reply, messageID); err != nil {
		s.logger.Warn("auto-reply: send failed", "error", err, "thread", thread.ThreadID)
		return
	}

	// Update tracked thread state.
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	newCount := thread.ReplyCount + 1
	active := 1
	if newCount >= maxAutoReplies {
		active = 0
	}

	if _, err := s.db.ExecContext(ctx, `
		UPDATE tracked_threads
		SET reply_count = ?, last_auto_reply_at = ?, last_email_id = ?, active = ?
		WHERE thread_id = ?`,
		newCount, now, emailID, active, thread.ThreadID); err != nil {
		s.logger.Warn("auto-reply: failed to update thread", "error", err)
	}

	s.logger.Info("auto-reply sent", "thread", thread.ThreadID, "to", replyTo, "reply_count", newCount)

	// Notify user about the auto-reply.
	if s.notify != nil {
		notifyMsg := fmt.Sprintf("Auto-replied to %s in thread: %s\n\n%s",
			shortName(from), subject, truncateReply(reply, 300))
		s.notify(ctx, notifyMsg)
	}
}

// loadActiveThreads returns all active tracked threads from the database.
func (s *Service) loadActiveThreads(ctx context.Context) ([]trackedThread, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT thread_id, last_email_id, to_addr, subject, reply_count, last_auto_reply_at, active
		FROM tracked_threads WHERE active = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []trackedThread
	for rows.Next() {
		var t trackedThread
		var lastReply *string
		if err := rows.Scan(&t.ThreadID, &t.LastEmailID, &t.ToAddr, &t.Subject,
			&t.ReplyCount, &lastReply, &t.Active); err != nil {
			return nil, err
		}
		if lastReply != nil {
			if parsed, err := time.Parse("2006-01-02 15:04:05", *lastReply); err == nil {
				t.LastAutoReply = &parsed
			}
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// normalizeSubject strips Re:/Fwd: prefixes and lowercases for matching.
func normalizeSubject(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for {
		trimmed := s
		trimmed = strings.TrimPrefix(trimmed, "re: ")
		trimmed = strings.TrimPrefix(trimmed, "re:")
		trimmed = strings.TrimPrefix(trimmed, "fwd: ")
		trimmed = strings.TrimPrefix(trimmed, "fwd:")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == s {
			break
		}
		s = trimmed
	}
	return s
}

// truncateReply cuts a reply to maxLen for notification display.
func truncateReply(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
