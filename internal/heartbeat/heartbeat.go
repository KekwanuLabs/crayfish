// Package heartbeat provides proactive check-ins that scan email, calendar, and tasks,
// then nudge the user with important updates. This makes Crayfish feel alive and helpful
// without the user having to ask.
//
// Philosophy: No setup required. It just works. During work hours, Crayfish checks in
// every 30 minutes and tells you what needs your attention.
package heartbeat

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/calendar"
	"github.com/KekwanuLabs/crayfish/internal/gmail"
)

// DefaultUrgencyKeywords are the default keywords for urgent email detection.
var DefaultUrgencyKeywords = []string{"urgent", "asap", "important", "action required", "deadline", "reminder"}

// Config holds heartbeat configuration.
type Config struct {
	Enabled         bool          // Whether heartbeat is active (default: true when email/calendar configured)
	Interval        time.Duration // Check interval (default: 30 minutes)
	WorkHourStart   int           // Start of work hours (default: 9)
	WorkHourEnd     int           // End of work hours (default: 18)
	WeekdaysOnly    bool          // Only run on weekdays (default: true)
	UrgencyKeywords []string      // Keywords that trigger urgent notifications
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:         true,
		Interval:        30 * time.Minute,
		WorkHourStart:   9,
		WorkHourEnd:     18,
		WeekdaysOnly:    true,
		UrgencyKeywords: DefaultUrgencyKeywords,
	}
}

// ServiceDeps holds all dependencies for the heartbeat service.
type ServiceDeps struct {
	Config           Config
	Email            gmail.EmailProvider
	Calendar         *calendar.Client
	Notify           NotifyFunc
	DB               *sql.DB
	LLMComplete      func(ctx context.Context, system, user string) (string, error)
	SelfEmail        string
	AutoReplyEnabled bool
	Logger           *slog.Logger
}

// Update contains the information from a heartbeat check.
type Update struct {
	UrgentEmails   []EmailSummary `json:"urgent_emails,omitempty"`
	UnreadCount    int            `json:"unread_count"`
	UpcomingEvents []EventSummary `json:"upcoming_events,omitempty"`
	NextMeeting    *EventSummary  `json:"next_meeting,omitempty"`
	MinutesToNext  int            `json:"minutes_to_next,omitempty"`
	HasUpdates     bool           `json:"has_updates"`
	Message        string         `json:"message"`
}

// EmailSummary is a brief email representation.
type EmailSummary struct {
	From    string `json:"from"`
	Subject string `json:"subject"`
	Preview string `json:"preview"`
}

// EventSummary is a brief event representation.
type EventSummary struct {
	Title    string `json:"title"`
	Time     string `json:"time"`
	Location string `json:"location,omitempty"`
}

// NotifyFunc is called when there's an update to send to the user.
type NotifyFunc func(ctx context.Context, message string) error

// Service runs periodic heartbeat checks.
type Service struct {
	config           Config
	email            gmail.EmailProvider
	calendar         *calendar.Client
	notify           NotifyFunc
	db               *sql.DB
	llmComplete      func(ctx context.Context, system, user string) (string, error)
	selfEmail        string
	autoReplyEnabled bool
	logger           *slog.Logger

	mu                sync.Mutex
	stopCh            chan struct{}
	wg                sync.WaitGroup
	lastRun           time.Time
	calendarDisabled  bool
	calendarFailCount int
}

// NewService creates a new heartbeat service.
func NewService(deps ServiceDeps) *Service {
	cfg := deps.Config
	if len(cfg.UrgencyKeywords) == 0 {
		cfg.UrgencyKeywords = DefaultUrgencyKeywords
	}
	return &Service{
		config:           cfg,
		email:            deps.Email,
		calendar:         deps.Calendar,
		notify:           deps.Notify,
		db:               deps.DB,
		llmComplete:      deps.LLMComplete,
		selfEmail:        deps.SelfEmail,
		autoReplyEnabled: deps.AutoReplyEnabled,
		logger:           deps.Logger,
		stopCh:           make(chan struct{}),
	}
}

// UpdateConfig hot-reloads the heartbeat configuration.
func (s *Service) UpdateConfig(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(cfg.UrgencyKeywords) == 0 {
		cfg.UrgencyKeywords = DefaultUrgencyKeywords
	}
	s.config = cfg
}

// SetAutoReplyEnabled updates the auto-reply state at runtime.
func (s *Service) SetAutoReplyEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoReplyEnabled = enabled
}

// Start begins the heartbeat loop.
func (s *Service) Start(ctx context.Context) error {
	if !s.config.Enabled {
		s.logger.Info("heartbeat disabled")
		return nil
	}

	s.wg.Add(1)
	go s.loop(ctx)
	s.logger.Info("heartbeat started",
		"interval", s.config.Interval,
		"work_hours", fmt.Sprintf("%d:00-%d:00", s.config.WorkHourStart, s.config.WorkHourEnd),
	)
	return nil
}

// Stop gracefully stops the heartbeat service.
func (s *Service) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	s.logger.Info("heartbeat stopped")
}

// loop runs the heartbeat check at configured intervals.
func (s *Service) loop(ctx context.Context) {
	defer s.wg.Done()

	// Check shortly after start (give other services time to init)
	time.Sleep(30 * time.Second)
	s.checkAndNotify(ctx)

	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkAndNotify(ctx)
		}
	}
}

// checkAndNotify performs a heartbeat check and notifies if there are updates.
func (s *Service) checkAndNotify(ctx context.Context) {
	now := time.Now()

	s.mu.Lock()
	cfg := s.config
	s.mu.Unlock()

	// Only run during work hours
	if !isWorkHours(cfg, now) {
		s.logger.Debug("skipping heartbeat (outside work hours)")
		return
	}

	s.mu.Lock()
	s.lastRun = now
	s.mu.Unlock()

	update, err := s.check(ctx)
	if err != nil {
		s.logger.Warn("heartbeat check failed", "error", err)
		return
	}

	if !update.HasUpdates {
		s.logger.Debug("heartbeat check: nothing to report")
		return
	}

	s.logger.Info("heartbeat: found updates",
		"urgent_emails", len(update.UrgentEmails),
		"upcoming_events", len(update.UpcomingEvents),
	)

	if s.notify != nil {
		if err := s.notify(ctx, update.Message); err != nil {
			s.logger.Warn("heartbeat notify failed", "error", err)
		}
	}
}

// isWorkHours checks if the current time is within work hours.
func isWorkHours(cfg Config, t time.Time) bool {
	if cfg.WeekdaysOnly {
		day := t.Weekday()
		if day == time.Saturday || day == time.Sunday {
			return false
		}
	}
	hour := t.Hour()
	return hour >= cfg.WorkHourStart && hour < cfg.WorkHourEnd
}

// check performs the actual heartbeat check.
func (s *Service) check(ctx context.Context) (*Update, error) {
	update := &Update{}
	var messages []string

	s.mu.Lock()
	cfg := s.config
	s.mu.Unlock()

	// Check emails
	if s.email != nil {
		emails, err := s.email.ListEmails(ctx, true, 20) // unread only, for urgent scan
		if err != nil {
			s.logger.Warn("heartbeat: email check failed", "error", err)
			messages = append(messages, "⚠️ Email check failed — I couldn't reach your inbox. You may want to check your Gmail credentials.")
		} else {
			// Get accurate unread count from the database instead of using len(emails)
			unreadCount, countErr := s.email.GetUnreadCount(ctx)
			if countErr != nil {
				s.logger.Warn("heartbeat: unread count failed", "error", countErr)
				update.UnreadCount = len(emails) // fallback to len(emails)
			} else {
				update.UnreadCount = unreadCount
			}

			// Find urgent emails using configurable keywords.
			for _, e := range emails {
				subjectLower := strings.ToLower(e.Subject)
				for _, kw := range cfg.UrgencyKeywords {
					if strings.Contains(subjectLower, strings.ToLower(kw)) {
						update.UrgentEmails = append(update.UrgentEmails, EmailSummary{
							From:    e.From,
							Subject: e.Subject,
							Preview: e.Preview,
						})
						break
					}
				}
			}

			if len(update.UrgentEmails) > 0 {
				messages = append(messages, fmt.Sprintf("📧 %d urgent email(s) need your attention", len(update.UrgentEmails)))
			} else if update.UnreadCount > 5 {
				messages = append(messages, fmt.Sprintf("📬 You have %d unread emails", update.UnreadCount))
			}
		}
	}

	// Check calendar
	if s.calendar != nil && !s.calendarDisabled {
		now := time.Now()
		// Look for events in the next 2 hours
		events, err := s.calendar.GetEvents(now, now.Add(2*time.Hour))
		if err != nil {
			s.calendarFailCount++
			s.logger.Warn("heartbeat: calendar check failed", "error", err, "attempt", s.calendarFailCount)
			if s.calendarFailCount >= 3 {
				s.calendarDisabled = true
				messages = append(messages, "⚠️ Calendar check failed — I couldn't reach your calendar. You may want to check your credentials.")
			}
		} else {
			for _, e := range events {
				if e.Start.After(now) { // Only upcoming events
					summary := EventSummary{
						Title:    e.Title,
						Time:     e.Start.Format("3:04 PM"),
						Location: e.Location,
					}
					update.UpcomingEvents = append(update.UpcomingEvents, summary)

					// Track next meeting
					if update.NextMeeting == nil {
						update.NextMeeting = &summary
						update.MinutesToNext = int(e.Start.Sub(now).Minutes())
					}
				}
			}

			if update.NextMeeting != nil {
				if update.MinutesToNext <= 15 {
					messages = append(messages, fmt.Sprintf("📅 \"%s\" starts in %d minutes!", update.NextMeeting.Title, update.MinutesToNext))
				} else if update.MinutesToNext <= 60 {
					messages = append(messages, fmt.Sprintf("📅 Next up: \"%s\" at %s", update.NextMeeting.Title, update.NextMeeting.Time))
				}
			}
		}
	}

	// Build final message
	if len(messages) > 0 {
		update.HasUpdates = true

		// Build a friendly message
		var sb strings.Builder
		sb.WriteString("Hey! Quick check-in:\n\n")
		for _, msg := range messages {
			sb.WriteString("• ")
			sb.WriteString(msg)
			sb.WriteString("\n")
		}

		// Add details for urgent emails
		if len(update.UrgentEmails) > 0 && len(update.UrgentEmails) <= 3 {
			sb.WriteString("\nUrgent emails:\n")
			for _, e := range update.UrgentEmails {
				sb.WriteString(fmt.Sprintf("  • %s: %s\n", shortName(e.From), e.Subject))
			}
		}

		update.Message = sb.String()
	}

	return update, nil
}

// CheckNewEmails is called by the sync callback to immediately check newly synced emails
// for urgency. NOT gated by work hours — urgent emails should always notify.
func (s *Service) CheckNewEmails(ctx context.Context, emailIDs []string) {
	if s.email == nil || s.db == nil || s.notify == nil {
		return
	}

	s.mu.Lock()
	keywords := s.config.UrgencyKeywords
	s.mu.Unlock()

	for _, id := range emailIDs {
		// Skip if already notified.
		var exists int
		if err := s.db.QueryRowContext(ctx,
			"SELECT 1 FROM urgency_notified WHERE email_id = ?", id).Scan(&exists); err == nil {
			continue
		}

		email, err := s.email.GetEmailByID(ctx, id)
		if err != nil {
			s.logger.Warn("urgency check: failed to fetch email", "id", id, "error", err)
			continue
		}

		subjectLower := strings.ToLower(email.Subject)
		isUrgent := false
		for _, kw := range keywords {
			if strings.Contains(subjectLower, strings.ToLower(kw)) {
				isUrgent = true
				break
			}
		}

		if !isUrgent {
			continue
		}

		// Record notification to avoid duplicates.
		if _, err := s.db.ExecContext(ctx,
			"INSERT OR IGNORE INTO urgency_notified (email_id) VALUES (?)", id); err != nil {
			s.logger.Warn("urgency check: failed to record notification", "id", id, "error", err)
		}

		// Notify immediately.
		msg := fmt.Sprintf("🚨 Urgent email from %s: %s\n\n%s",
			shortName(email.From), email.Subject, truncate(email.BodyPreview, 200))

		if err := s.notify(ctx, msg); err != nil {
			s.logger.Warn("urgency notification failed", "id", id, "error", err)
		} else {
			s.logger.Info("urgent email notification sent", "id", id, "subject", email.Subject)
		}
	}
}

// ForceCheck triggers an immediate heartbeat check (for testing/manual trigger).
func (s *Service) ForceCheck(ctx context.Context) (*Update, error) {
	return s.check(ctx)
}

// shortName extracts just the name from an email address like "John Doe <john@example.com>".
func shortName(from string) string {
	if idx := strings.Index(from, "<"); idx > 0 {
		return strings.TrimSpace(from[:idx])
	}
	if idx := strings.Index(from, "@"); idx > 0 {
		return from[:idx]
	}
	return from
}

// truncate cuts a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
