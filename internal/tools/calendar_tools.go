package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/calendar"
	"github.com/KekwanuLabs/crayfish/internal/security"
)

// RegisterCalendarTools adds Google Calendar tools to the registry.
// Called only when Gmail/Calendar is configured (same auth).
func RegisterCalendarTools(reg *Registry, client *calendar.Client) {
	reg.logger.Info("registering calendar tools")

	// calendar_today — what's on my schedule today?
	reg.Register(&Tool{
		Name:        "calendar_today",
		Description: "Get today's calendar events. Shows what's on your schedule for today.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			events, err := client.GetToday()
			if err != nil {
				return "", fmt.Errorf("calendar_today: %w", err)
			}

			if len(events) == 0 {
				return "No events scheduled for today.", nil
			}

			var summaries []calendar.EventSummary
			for _, e := range events {
				summaries = append(summaries, e.ToSummary())
			}

			result, _ := json.Marshal(summaries)
			return string(result), nil
		},
	})

	// calendar_upcoming — what's coming up?
	reg.Register(&Tool{
		Name:        "calendar_upcoming",
		Description: "Get upcoming calendar events for the next few days. Use to check your schedule ahead.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"days": {
					"type": "integer",
					"description": "Number of days to look ahead (default: 7, max: 30)"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Days int `json:"days"`
			}
			json.Unmarshal(input, &params)

			days := 7
			if params.Days > 0 && params.Days <= 30 {
				days = params.Days
			}

			events, err := client.GetUpcoming(days)
			if err != nil {
				return "", fmt.Errorf("calendar_upcoming: %w", err)
			}

			if len(events) == 0 {
				return fmt.Sprintf("No events scheduled for the next %d days.", days), nil
			}

			// Group by day for readability
			type dayEvents struct {
				Date   string                   `json:"date"`
				Events []calendar.EventSummary `json:"events"`
			}

			byDay := make(map[string][]calendar.EventSummary)
			for _, e := range events {
				day := e.Start.Format("Mon, Jan 2")
				byDay[day] = append(byDay[day], e.ToSummary())
			}

			// Convert to ordered list
			var result []dayEvents
			now := time.Now()
			for i := 0; i < days; i++ {
				day := now.AddDate(0, 0, i).Format("Mon, Jan 2")
				if evts, ok := byDay[day]; ok {
					result = append(result, dayEvents{Date: day, Events: evts})
				}
			}

			out, _ := json.Marshal(result)
			return string(out), nil
		},
	})

	// calendar_add — add a new event
	reg.Register(&Tool{
		Name:        "calendar_add",
		Description: "Add a new event to your calendar. Specify title, date/time, and optionally location and description.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {
					"type": "string",
					"description": "Event title (e.g., 'Team meeting', 'Doctor appointment')"
				},
				"date": {
					"type": "string",
					"description": "Date in YYYY-MM-DD format (e.g., '2024-03-15')"
				},
				"start_time": {
					"type": "string",
					"description": "Start time in HH:MM format, 24-hour (e.g., '14:30' for 2:30 PM). Omit for all-day event."
				},
				"end_time": {
					"type": "string",
					"description": "End time in HH:MM format. Defaults to 1 hour after start."
				},
				"location": {
					"type": "string",
					"description": "Event location (optional)"
				},
				"description": {
					"type": "string",
					"description": "Event description/notes (optional)"
				}
			},
			"required": ["title", "date"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Title       string `json:"title"`
				Date        string `json:"date"`
				StartTime   string `json:"start_time"`
				EndTime     string `json:"end_time"`
				Location    string `json:"location"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("calendar_add: parse input: %w", err)
			}

			if params.Title == "" {
				return "", fmt.Errorf("calendar_add: title is required")
			}
			if params.Date == "" {
				return "", fmt.Errorf("calendar_add: date is required")
			}

			// Parse date
			date, err := time.Parse("2006-01-02", params.Date)
			if err != nil {
				return "", fmt.Errorf("calendar_add: invalid date format (use YYYY-MM-DD)")
			}

			event := &calendar.Event{
				Title:       params.Title,
				Location:    params.Location,
				Description: params.Description,
			}

			if params.StartTime == "" {
				// All-day event
				event.AllDay = true
				event.Start = date
				event.End = date.AddDate(0, 0, 1)
			} else {
				// Timed event
				startParts := strings.Split(params.StartTime, ":")
				if len(startParts) != 2 {
					return "", fmt.Errorf("calendar_add: invalid start_time format (use HH:MM)")
				}
				var hour, minute int
				fmt.Sscanf(params.StartTime, "%d:%d", &hour, &minute)
				event.Start = time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, time.Local)

				if params.EndTime != "" {
					var endHour, endMinute int
					fmt.Sscanf(params.EndTime, "%d:%d", &endHour, &endMinute)
					event.End = time.Date(date.Year(), date.Month(), date.Day(), endHour, endMinute, 0, 0, time.Local)
				} else {
					// Default to 1 hour duration
					event.End = event.Start.Add(time.Hour)
				}
			}

			if err := client.CreateEvent(event); err != nil {
				return "", fmt.Errorf("calendar_add: %w", err)
			}

			timeStr := "all day"
			if !event.AllDay {
				timeStr = event.Start.Format("3:04 PM") + " - " + event.End.Format("3:04 PM")
			}

			return fmt.Sprintf("Event added: %s on %s (%s)", event.Title, date.Format("Mon, Jan 2"), timeStr), nil
		},
	})

	// calendar_search — find events
	reg.Register(&Tool{
		Name:        "calendar_search",
		Description: "Search for calendar events by keyword in title, or by date range.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Search text to find in event titles"
				},
				"from_date": {
					"type": "string",
					"description": "Start date for search range (YYYY-MM-DD)"
				},
				"to_date": {
					"type": "string",
					"description": "End date for search range (YYYY-MM-DD)"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Query    string `json:"query"`
				FromDate string `json:"from_date"`
				ToDate   string `json:"to_date"`
			}
			json.Unmarshal(input, &params)

			// Default to next 30 days if no date range
			start := time.Now()
			end := start.AddDate(0, 0, 30)

			if params.FromDate != "" {
				if t, err := time.Parse("2006-01-02", params.FromDate); err == nil {
					start = t
				}
			}
			if params.ToDate != "" {
				if t, err := time.Parse("2006-01-02", params.ToDate); err == nil {
					end = t.AddDate(0, 0, 1) // Include the end date
				}
			}

			events, err := client.GetEvents(start, end)
			if err != nil {
				return "", fmt.Errorf("calendar_search: %w", err)
			}

			// Filter by query if provided
			if params.Query != "" {
				query := strings.ToLower(params.Query)
				var filtered []calendar.Event
				for _, e := range events {
					if strings.Contains(strings.ToLower(e.Title), query) ||
						strings.Contains(strings.ToLower(e.Description), query) ||
						strings.Contains(strings.ToLower(e.Location), query) {
						filtered = append(filtered, e)
					}
				}
				events = filtered
			}

			if len(events) == 0 {
				return "No matching events found.", nil
			}

			var summaries []calendar.EventSummary
			for _, e := range events {
				summaries = append(summaries, e.ToSummary())
			}

			result, _ := json.Marshal(summaries)
			return string(result), nil
		},
	})

	// calendar_free — when am I free?
	reg.Register(&Tool{
		Name:        "calendar_free",
		Description: "Check for free time slots on a specific date. Useful for scheduling new meetings.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"date": {
					"type": "string",
					"description": "Date to check (YYYY-MM-DD). Defaults to today."
				},
				"work_hours_start": {
					"type": "integer",
					"description": "Work day start hour (default: 9)"
				},
				"work_hours_end": {
					"type": "integer",
					"description": "Work day end hour (default: 17)"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Date           string `json:"date"`
				WorkHoursStart int    `json:"work_hours_start"`
				WorkHoursEnd   int    `json:"work_hours_end"`
			}
			json.Unmarshal(input, &params)

			date := time.Now()
			if params.Date != "" {
				if t, err := time.Parse("2006-01-02", params.Date); err == nil {
					date = t
				}
			}

			workStart := 9
			workEnd := 17
			if params.WorkHoursStart > 0 && params.WorkHoursStart < 24 {
				workStart = params.WorkHoursStart
			}
			if params.WorkHoursEnd > 0 && params.WorkHoursEnd < 24 {
				workEnd = params.WorkHoursEnd
			}

			// Get events for the day
			dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
			dayEnd := dayStart.Add(24 * time.Hour)
			events, err := client.GetEvents(dayStart, dayEnd)
			if err != nil {
				return "", fmt.Errorf("calendar_free: %w", err)
			}

			// Find free slots during work hours
			type slot struct {
				Start string `json:"start"`
				End   string `json:"end"`
			}

			var freeSlots []slot
			currentTime := time.Date(date.Year(), date.Month(), date.Day(), workStart, 0, 0, 0, date.Location())
			endOfDay := time.Date(date.Year(), date.Month(), date.Day(), workEnd, 0, 0, 0, date.Location())

			// Sort events by start time (simple bubble sort - few events expected)
			for i := 0; i < len(events); i++ {
				for j := i + 1; j < len(events); j++ {
					if events[j].Start.Before(events[i].Start) {
						events[i], events[j] = events[j], events[i]
					}
				}
			}

			for _, e := range events {
				if e.AllDay {
					continue // Skip all-day events for free time calculation
				}
				if e.End.Before(currentTime) || e.Start.After(endOfDay) {
					continue // Outside work hours
				}

				// Free slot before this event?
				if currentTime.Before(e.Start) {
					freeSlots = append(freeSlots, slot{
						Start: currentTime.Format("3:04 PM"),
						End:   e.Start.Format("3:04 PM"),
					})
				}
				if e.End.After(currentTime) {
					currentTime = e.End
				}
			}

			// Free time after last event
			if currentTime.Before(endOfDay) {
				freeSlots = append(freeSlots, slot{
					Start: currentTime.Format("3:04 PM"),
					End:   endOfDay.Format("3:04 PM"),
				})
			}

			if len(freeSlots) == 0 {
				return fmt.Sprintf("No free time on %s during work hours (%d:00 - %d:00).",
					date.Format("Mon, Jan 2"), workStart, workEnd), nil
			}

			result := map[string]interface{}{
				"date":       date.Format("Mon, Jan 2"),
				"work_hours": fmt.Sprintf("%d:00 AM - %d:00 PM", workStart, workEnd-12),
				"free_slots": freeSlots,
			}
			out, _ := json.Marshal(result)
			return string(out), nil
		},
	})

	// calendar_delete — remove an event by ID.
	reg.Register(&Tool{
		Name:        "calendar_delete",
		Description: "Delete a calendar event by its ID. Use calendar_search or calendar_today first to find the event ID, then delete it.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"event_id": {
					"type": "string",
					"description": "The event ID from calendar_today, calendar_upcoming, or calendar_search results"
				}
			},
			"required": ["event_id"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				EventID string `json:"event_id"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("calendar_delete: parse input: %w", err)
			}
			if params.EventID == "" {
				return "", fmt.Errorf("calendar_delete: event_id is required")
			}

			if err := client.DeleteEvent(params.EventID); err != nil {
				return "", fmt.Errorf("calendar_delete: %w", err)
			}

			return fmt.Sprintf("Event %s deleted.", params.EventID), nil
		},
	})
}
