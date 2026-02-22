// Package calendar provides Google Calendar integration via CalDAV.
// Uses the same App Password authentication as Gmail - no OAuth required.
package calendar

import (
	"time"
)

// Event represents a calendar event.
type Event struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Location    string    `json:"location,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"all_day,omitempty"`
	Attendees   []string  `json:"attendees,omitempty"`
	CalendarID  string    `json:"calendar_id,omitempty"`
}

// EventSummary is a brief event representation for listings.
type EventSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Location string `json:"location,omitempty"`
	AllDay   bool   `json:"all_day,omitempty"`
}

// Config holds calendar connection settings.
type Config struct {
	Email       string
	AppPassword string
}

// FormatTime returns a human-friendly time string.
func (e *Event) FormatTime() string {
	if e.AllDay {
		return e.Start.Format("Mon, Jan 2")
	}
	if e.Start.Format("2006-01-02") == e.End.Format("2006-01-02") {
		// Same day
		return e.Start.Format("Mon, Jan 2 · 3:04 PM") + " - " + e.End.Format("3:04 PM")
	}
	return e.Start.Format("Mon, Jan 2 3:04 PM") + " - " + e.End.Format("Mon, Jan 2 3:04 PM")
}

// ToSummary converts an Event to EventSummary.
func (e *Event) ToSummary() EventSummary {
	startFmt := e.Start.Format("3:04 PM")
	endFmt := e.End.Format("3:04 PM")
	if e.AllDay {
		startFmt = "All day"
		endFmt = ""
	}
	return EventSummary{
		ID:       e.ID,
		Title:    e.Title,
		Start:    startFmt,
		End:      endFmt,
		Location: e.Location,
		AllDay:   e.AllDay,
	}
}
