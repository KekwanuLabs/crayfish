package calendar

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	googleCalDAVBase = "https://apidata.googleusercontent.com/caldav/v2"
	httpTimeout      = 30 * time.Second
)

// TokenProvider returns an OAuth 2.0 Bearer token for authenticated requests.
// The context allows the provider to perform token refresh if needed.
type TokenProvider func(ctx context.Context) (string, error)

// Client connects to Google Calendar via CalDAV.
type Client struct {
	email         string
	appPassword   string
	tokenProvider TokenProvider
	httpClient    *http.Client
	logger        *slog.Logger
}

// NewClient creates a new CalDAV client using App Password (Basic Auth).
func NewClient(email, appPassword string, logger *slog.Logger) *Client {
	return &Client{
		email:       email,
		appPassword: appPassword,
		httpClient:  &http.Client{Timeout: httpTimeout},
		logger:      logger,
	}
}

// NewOAuthClient creates a new CalDAV client using OAuth 2.0 Bearer tokens.
func NewOAuthClient(email string, tp TokenProvider, logger *slog.Logger) *Client {
	return &Client{
		email:         email,
		tokenProvider: tp,
		httpClient:    &http.Client{Timeout: httpTimeout},
		logger:        logger,
	}
}

// calendarURL returns the CalDAV URL for the user's primary calendar.
func (c *Client) calendarURL() string {
	return fmt.Sprintf("%s/%s/events/", googleCalDAVBase, c.email)
}

// doRequest performs an authenticated CalDAV request.
// Uses OAuth Bearer token if a TokenProvider is set, otherwise falls back to Basic Auth.
func (c *Client) doRequest(ctx context.Context, method, url string, body []byte, contentType string) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if c.tokenProvider != nil {
		token, err := c.tokenProvider(ctx)
		if err != nil {
			return nil, fmt.Errorf("get oauth token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.SetBasicAuth(c.email, c.appPassword)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		bodyPreview := string(respBody)
		if len(bodyPreview) > 200 {
			bodyPreview = bodyPreview[:200]
		}
		c.logger.Warn("CalDAV request failed",
			"method", method, "url", url,
			"status", resp.StatusCode,
			"body_preview", bodyPreview)
		return nil, fmt.Errorf("caldav error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// GetEvents fetches events in the given time range.
func (c *Client) GetEvents(start, end time.Time) ([]Event, error) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	// CalDAV REPORT request to query events
	reportXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop>
    <d:getetag/>
    <c:calendar-data/>
  </d:prop>
  <c:filter>
    <c:comp-filter name="VCALENDAR">
      <c:comp-filter name="VEVENT">
        <c:time-range start="%s" end="%s"/>
      </c:comp-filter>
    </c:comp-filter>
  </c:filter>
</c:calendar-query>`,
		start.UTC().Format("20060102T150405Z"),
		end.UTC().Format("20060102T150405Z"))

	respBody, err := c.doRequest(ctx, "REPORT", c.calendarURL(), []byte(reportXML), "application/xml")
	if err != nil {
		return nil, fmt.Errorf("caldav report: %w", err)
	}

	return c.parseMultistatus(respBody)
}

// GetToday returns today's events.
func (c *Client) GetToday() ([]Event, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(24 * time.Hour)
	return c.GetEvents(start, end)
}

// GetUpcoming returns events for the next N days.
func (c *Client) GetUpcoming(days int) ([]Event, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(time.Duration(days) * 24 * time.Hour)
	return c.GetEvents(start, end)
}

// CreateEvent creates a new calendar event.
func (c *Client) CreateEvent(event *Event) error {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	uid := fmt.Sprintf("%d@crayfish", time.Now().UnixNano())
	if event.ID == "" {
		event.ID = uid
	}

	// Build iCalendar format
	var ical strings.Builder
	ical.WriteString("BEGIN:VCALENDAR\r\n")
	ical.WriteString("VERSION:2.0\r\n")
	ical.WriteString("PRODID:-//Crayfish//Calendar//EN\r\n")
	ical.WriteString("BEGIN:VEVENT\r\n")
	ical.WriteString(fmt.Sprintf("UID:%s\r\n", event.ID))
	ical.WriteString(fmt.Sprintf("DTSTAMP:%s\r\n", time.Now().UTC().Format("20060102T150405Z")))

	if event.AllDay {
		ical.WriteString(fmt.Sprintf("DTSTART;VALUE=DATE:%s\r\n", event.Start.Format("20060102")))
		ical.WriteString(fmt.Sprintf("DTEND;VALUE=DATE:%s\r\n", event.End.Format("20060102")))
	} else {
		ical.WriteString(fmt.Sprintf("DTSTART:%s\r\n", event.Start.UTC().Format("20060102T150405Z")))
		ical.WriteString(fmt.Sprintf("DTEND:%s\r\n", event.End.UTC().Format("20060102T150405Z")))
	}

	ical.WriteString(fmt.Sprintf("SUMMARY:%s\r\n", escapeIcal(event.Title)))
	if event.Description != "" {
		ical.WriteString(fmt.Sprintf("DESCRIPTION:%s\r\n", escapeIcal(event.Description)))
	}
	if event.Location != "" {
		ical.WriteString(fmt.Sprintf("LOCATION:%s\r\n", escapeIcal(event.Location)))
	}
	ical.WriteString("END:VEVENT\r\n")
	ical.WriteString("END:VCALENDAR\r\n")

	eventURL := c.calendarURL() + event.ID + ".ics"
	_, err := c.doRequest(ctx, "PUT", eventURL, []byte(ical.String()), "text/calendar")
	if err != nil {
		return fmt.Errorf("create event: %w", err)
	}

	c.logger.Info("calendar event created", "title", event.Title, "start", event.Start)
	return nil
}

// DeleteEvent removes an event by ID.
func (c *Client) DeleteEvent(eventID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	eventURL := c.calendarURL() + eventID + ".ics"
	_, err := c.doRequest(ctx, "DELETE", eventURL, nil, "")
	return err
}

// parseMultistatus parses CalDAV multistatus XML response.
func (c *Client) parseMultistatus(data []byte) ([]Event, error) {
	// Simple regex-based parsing of iCalendar data from XML
	// Full XML parsing would be more robust but this works for Google Calendar
	var events []Event

	// Extract calendar-data elements
	re := regexp.MustCompile(`<[^>]*calendar-data[^>]*>([\s\S]*?)</[^>]*calendar-data>`)
	matches := re.FindAllSubmatch(data, -1)

	for _, match := range matches {
		if len(match) > 1 {
			icalData := string(match[1])
			// Unescape XML entities
			icalData = strings.ReplaceAll(icalData, "&lt;", "<")
			icalData = strings.ReplaceAll(icalData, "&gt;", ">")
			icalData = strings.ReplaceAll(icalData, "&amp;", "&")

			event, err := parseICalEvent(icalData)
			if err != nil {
				c.logger.Debug("failed to parse event", "error", err)
				continue
			}
			events = append(events, event)
		}
	}

	return events, nil
}

// parseICalEvent parses a VEVENT from iCalendar format.
func parseICalEvent(data string) (Event, error) {
	var event Event

	lines := strings.Split(data, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "UID:") {
			event.ID = strings.TrimPrefix(line, "UID:")
		} else if strings.HasPrefix(line, "SUMMARY:") {
			event.Title = unescapeIcal(strings.TrimPrefix(line, "SUMMARY:"))
		} else if strings.HasPrefix(line, "DESCRIPTION:") {
			event.Description = unescapeIcal(strings.TrimPrefix(line, "DESCRIPTION:"))
		} else if strings.HasPrefix(line, "LOCATION:") {
			event.Location = unescapeIcal(strings.TrimPrefix(line, "LOCATION:"))
		} else if strings.HasPrefix(line, "DTSTART") {
			event.Start, event.AllDay = parseICalTime(line)
		} else if strings.HasPrefix(line, "DTEND") {
			event.End, _ = parseICalTime(line)
		}
	}

	if event.Title == "" {
		return event, fmt.Errorf("no title found")
	}

	return event, nil
}

// parseICalTime parses iCalendar datetime formats.
func parseICalTime(line string) (time.Time, bool) {
	// Check for all-day event
	allDay := strings.Contains(line, "VALUE=DATE")

	// Extract the time value
	parts := strings.SplitN(line, ":", 2)
	if len(parts) < 2 {
		return time.Time{}, false
	}
	value := strings.TrimSpace(parts[1])

	if allDay {
		t, _ := time.Parse("20060102", value)
		return t, true
	}

	// Try different formats
	formats := []string{
		"20060102T150405Z",
		"20060102T150405",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, value); err == nil {
			return t, false
		}
	}

	return time.Time{}, false
}

// escapeIcal escapes special characters for iCalendar format.
func escapeIcal(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, ";", "\\;")
	return s
}

// unescapeIcal unescapes iCalendar special characters.
func unescapeIcal(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\,", ",")
	s = strings.ReplaceAll(s, "\\;", ";")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// Unused but required for XML parsing
type multistatusResponse struct {
	XMLName   xml.Name   `xml:"multistatus"`
	Responses []response `xml:"response"`
}

type response struct {
	Href     string   `xml:"href"`
	Propstat propstat `xml:"propstat"`
}

type propstat struct {
	Prop prop `xml:"prop"`
}

type prop struct {
	CalendarData string `xml:"calendar-data"`
}
