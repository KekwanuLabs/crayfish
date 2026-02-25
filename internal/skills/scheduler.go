package skills

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scheduler runs skills on cron-like schedules.
// It uses a simple tick-and-check approach rather than a full cron library
// to keep the binary small and dependency-free.
type Scheduler struct {
	registry *Registry
	callback ScheduleCallback
	logger   *slog.Logger

	mu       sync.Mutex
	entries  []schedEntry
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// ScheduleCallback is called when a scheduled skill should fire.
// The callback is responsible for executing the skill.
type ScheduleCallback func(ctx context.Context, skill *Skill)

type schedEntry struct {
	skillName string
	schedule  cronSchedule
	lastRun   time.Time
}

// NewScheduler creates a new skill scheduler.
func NewScheduler(registry *Registry, callback ScheduleCallback, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		registry: registry,
		callback: callback,
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
}

// Start loads scheduled skills and begins the check loop.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	s.entries = nil

	for _, skill := range s.registry.FindScheduled() {
		sched, err := parseCron(skill.Trigger.Schedule)
		if err != nil {
			s.logger.Warn("invalid cron schedule for skill",
				"skill", skill.Name, "schedule", skill.Trigger.Schedule, "error", err)
			continue
		}
		s.entries = append(s.entries, schedEntry{
			skillName: skill.Name,
			schedule:  sched,
		})
		s.logger.Info("scheduled skill", "skill", skill.Name, "schedule", skill.Trigger.Schedule)
	}
	s.mu.Unlock()

	if len(s.entries) == 0 {
		s.logger.Debug("no scheduled skills found")
		return nil
	}

	s.wg.Add(1)
	go s.loop(ctx)
	s.logger.Info("skill scheduler started", "scheduled_skills", len(s.entries))
	return nil
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	s.logger.Info("skill scheduler stopped")
}

// loop checks every minute if any scheduled skills should fire.
func (s *Scheduler) loop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			s.checkAndFire(ctx, now)
		}
	}
}

// checkAndFire checks all scheduled entries and fires any that match the current time.
func (s *Scheduler) checkAndFire(ctx context.Context, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.entries {
		entry := &s.entries[i]

		// Skip if already fired this minute.
		if now.Sub(entry.lastRun) < time.Minute {
			continue
		}

		if entry.schedule.matches(now) {
			skill := s.registry.Get(entry.skillName)
			if skill == nil {
				continue
			}

			entry.lastRun = now
			s.logger.Info("firing scheduled skill", "skill", skill.Name)

			// Fire in a goroutine so we don't block the check loop.
			go s.callback(ctx, skill)
		}
	}
}

// CronToHuman converts a cron expression to a human-readable description.
// Handles common patterns; falls back to the raw expression for complex cases.
func CronToHuman(expr string) string {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return expr
	}

	minute, hour, dom, month, dow := parts[0], parts[1], parts[2], parts[3], parts[4]

	// Every N minutes: */N * * * *
	if strings.HasPrefix(minute, "*/") && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		interval := minute[2:]
		if interval == "1" {
			return "Every minute"
		}
		return "Every " + interval + " minutes"
	}

	// Every N hours: 0 */N * * *
	if minute == "0" && strings.HasPrefix(hour, "*/") && dom == "*" && month == "*" && dow == "*" {
		interval := hour[2:]
		if interval == "1" {
			return "Every hour"
		}
		return "Every " + interval + " hours"
	}

	// Specific time patterns
	if dom == "*" && month == "*" {
		timeStr := formatTime(minute, hour)
		if timeStr == "" {
			return expr
		}

		switch dow {
		case "*":
			return "Every day at " + timeStr
		case "1-5":
			return "Weekdays at " + timeStr
		case "0,6":
			return "Weekends at " + timeStr
		case "0":
			return "Sundays at " + timeStr
		case "1":
			return "Mondays at " + timeStr
		case "2":
			return "Tuesdays at " + timeStr
		case "3":
			return "Wednesdays at " + timeStr
		case "4":
			return "Thursdays at " + timeStr
		case "5":
			return "Fridays at " + timeStr
		case "6":
			return "Saturdays at " + timeStr
		}
	}

	return expr
}

// formatTime converts minute and hour cron fields to a readable time string.
// Returns empty string if the fields aren't simple numeric values.
func formatTime(minuteField, hourField string) string {
	m, err := strconv.Atoi(minuteField)
	if err != nil {
		return ""
	}
	h, err := strconv.Atoi(hourField)
	if err != nil {
		return ""
	}

	period := "AM"
	displayHour := h
	if h == 0 {
		displayHour = 12
	} else if h == 12 {
		period = "PM"
	} else if h > 12 {
		displayHour = h - 12
		period = "PM"
	}

	return fmt.Sprintf("%d:%02d %s", displayHour, m, period)
}

// --- Minimal cron parser ---
// Supports: "minute hour day-of-month month day-of-week"
// Supports: *, specific numbers, */interval, ranges (1-5), lists (1,3,5)

type cronSchedule struct {
	minute     fieldMatcher
	hour       fieldMatcher
	dayOfMonth fieldMatcher
	month      fieldMatcher
	dayOfWeek  fieldMatcher
}

func (c cronSchedule) matches(t time.Time) bool {
	return c.minute.matches(t.Minute()) &&
		c.hour.matches(t.Hour()) &&
		c.dayOfMonth.matches(t.Day()) &&
		c.month.matches(int(t.Month())) &&
		c.dayOfWeek.matches(int(t.Weekday()))
}

type fieldMatcher struct {
	matchAll bool
	values   map[int]bool
}

func (f fieldMatcher) matches(value int) bool {
	if f.matchAll {
		return true
	}
	return f.values[value]
}

func parseCron(expr string) (cronSchedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return cronSchedule{}, fmt.Errorf("cron expression must have 5 fields, got %d", len(parts))
	}

	minute, err := parseField(parts[0], 0, 59)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("minute field: %w", err)
	}
	hour, err := parseField(parts[1], 0, 23)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("hour field: %w", err)
	}
	dom, err := parseField(parts[2], 1, 31)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("day-of-month field: %w", err)
	}
	month, err := parseField(parts[3], 1, 12)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("month field: %w", err)
	}
	dow, err := parseField(parts[4], 0, 6)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("day-of-week field: %w", err)
	}

	return cronSchedule{
		minute:     minute,
		hour:       hour,
		dayOfMonth: dom,
		month:      month,
		dayOfWeek:  dow,
	}, nil
}

func parseField(field string, min, max int) (fieldMatcher, error) {
	if field == "*" {
		return fieldMatcher{matchAll: true}, nil
	}

	values := make(map[int]bool)

	// Handle */interval
	if strings.HasPrefix(field, "*/") {
		interval, err := strconv.Atoi(field[2:])
		if err != nil || interval <= 0 {
			return fieldMatcher{}, fmt.Errorf("invalid interval: %s", field)
		}
		for i := min; i <= max; i += interval {
			values[i] = true
		}
		return fieldMatcher{values: values}, nil
	}

	// Handle comma-separated list (which can include ranges)
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)

		// Handle range (e.g., "1-5")
		if strings.Contains(part, "-") {
			rangeParts := strings.SplitN(part, "-", 2)
			start, err := strconv.Atoi(rangeParts[0])
			if err != nil {
				return fieldMatcher{}, fmt.Errorf("invalid range start: %s", part)
			}
			end, err := strconv.Atoi(rangeParts[1])
			if err != nil {
				return fieldMatcher{}, fmt.Errorf("invalid range end: %s", part)
			}
			for i := start; i <= end; i++ {
				values[i] = true
			}
		} else {
			// Single value
			val, err := strconv.Atoi(part)
			if err != nil {
				return fieldMatcher{}, fmt.Errorf("invalid value: %s", part)
			}
			values[val] = true
		}
	}

	return fieldMatcher{values: values}, nil
}
