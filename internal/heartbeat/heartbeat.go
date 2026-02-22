// Package heartbeat provides proactive check-ins that scan email, calendar, and tasks,
// then nudge the user with important updates. This makes Crayfish feel alive and helpful
// without the user having to ask.
//
// Philosophy: No setup required. It just works. During work hours, Crayfish checks in
// every 30 minutes and tells you what needs your attention.
package heartbeat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/calendar"
	"github.com/KekwanuLabs/crayfish/internal/gmail"
)

// Config holds heartbeat configuration.
type Config struct {
	Enabled       bool          // Whether heartbeat is active (default: true when email/calendar configured)
	Interval      time.Duration // Check interval (default: 30 minutes)
	WorkHourStart int           // Start of work hours (default: 9)
	WorkHourEnd   int           // End of work hours (default: 18)
	WeekdaysOnly  bool          // Only run on weekdays (default: true)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:       true,
		Interval:      30 * time.Minute,
		WorkHourStart: 9,
		WorkHourEnd:   18,
		WeekdaysOnly:  true,
	}
}

// Update contains the information from a heartbeat check.
type Update struct {
	UrgentEmails    []EmailSummary   `json:"urgent_emails,omitempty"`
	UnreadCount     int              `json:"unread_count"`
	UpcomingEvents  []EventSummary   `json:"upcoming_events,omitempty"`
	NextMeeting     *EventSummary    `json:"next_meeting,omitempty"`
	MinutesToNext   int              `json:"minutes_to_next,omitempty"`
	HasUpdates      bool             `json:"has_updates"`
	Message         string           `json:"message"`
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
	config   Config
	gmail    *gmail.Poller
	calendar *calendar.Client
	notify   NotifyFunc
	logger   *slog.Logger

	mu       sync.Mutex
	stopCh   chan struct{}
	wg       sync.WaitGroup
	lastRun  time.Time
}

// NewService creates a new heartbeat service.
func NewService(cfg Config, gmailPoller *gmail.Poller, calendarClient *calendar.Client, notify NotifyFunc, logger *slog.Logger) *Service {
	return &Service{
		config:   cfg,
		gmail:    gmailPoller,
		calendar: calendarClient,
		notify:   notify,
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
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

	// Only run during work hours
	if !s.isWorkHours(now) {
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
func (s *Service) isWorkHours(t time.Time) bool {
	// Check weekday
	if s.config.WeekdaysOnly {
		day := t.Weekday()
		if day == time.Saturday || day == time.Sunday {
			return false
		}
	}

	// Check hour
	hour := t.Hour()
	return hour >= s.config.WorkHourStart && hour < s.config.WorkHourEnd
}

// check performs the actual heartbeat check.
func (s *Service) check(ctx context.Context) (*Update, error) {
	update := &Update{}
	var messages []string

	// Check emails
	if s.gmail != nil {
		emails, err := s.gmail.ListEmails(ctx, true, 20) // unread only
		if err != nil {
			s.logger.Warn("heartbeat: email check failed", "error", err)
		} else {
			update.UnreadCount = len(emails)

			// Find urgent emails (simple heuristic: subject contains urgent keywords)
			urgentKeywords := []string{"urgent", "asap", "important", "action required", "deadline", "reminder"}
			for _, e := range emails {
				subjectLower := strings.ToLower(e.Subject)
				for _, kw := range urgentKeywords {
					if strings.Contains(subjectLower, kw) {
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
	if s.calendar != nil {
		now := time.Now()
		// Look for events in the next 2 hours
		events, err := s.calendar.GetEvents(now, now.Add(2*time.Hour))
		if err != nil {
			s.logger.Warn("heartbeat: calendar check failed", "error", err)
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
