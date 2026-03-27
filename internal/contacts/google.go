// Package contacts handles contact sync from cloud providers.
package contacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const peopleAPIURL = "https://people.googleapis.com/v1/people/me/connections" +
	"?personFields=names,phoneNumbers,emailAddresses,organizations,biographies" +
	"&pageSize=1000&sortOrder=LAST_NAME_ASCENDING"

// GoogleContact is a contact from the Google People API.
type GoogleContact struct {
	Name  string
	Phone string
	Email string
	Notes string
}

// SyncFromGoogle fetches contacts from Google People API and upserts them
// into the local contacts table. Returns the number of contacts synced.
// Only syncs contacts that have at least a name and phone or email.
func SyncFromGoogle(ctx context.Context, db *sql.DB, accessToken string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", peopleAPIURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Google People API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return 0, fmt.Errorf("Google Contacts not authorized — grant contacts.readonly permission via Google Connect")
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return 0, fmt.Errorf("Google People API error %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Connections []struct {
			Names []struct {
				DisplayName string `json:"displayName"`
			} `json:"names"`
			PhoneNumbers []struct {
				Value string `json:"value"`
			} `json:"phoneNumbers"`
			EmailAddresses []struct {
				Value string `json:"value"`
			} `json:"emailAddresses"`
			Biographies []struct {
				Value string `json:"value"`
			} `json:"biographies"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("parse response: %w", err)
	}

	synced := 0
	for _, conn := range result.Connections {
		if len(conn.Names) == 0 {
			continue
		}
		name := conn.Names[0].DisplayName
		if name == "" {
			continue
		}

		phone := ""
		if len(conn.PhoneNumbers) > 0 {
			phone = normalizePhone(conn.PhoneNumbers[0].Value)
		}
		email := ""
		if len(conn.EmailAddresses) > 0 {
			email = conn.EmailAddresses[0].Value
		}
		notes := ""
		if len(conn.Biographies) > 0 {
			notes = truncate(conn.Biographies[0].Value, 200)
		}

		// Skip contacts with neither phone nor email.
		if phone == "" && email == "" {
			continue
		}

		// Upsert by name — if a contact with this name exists, update phone/email.
		_, err := db.ExecContext(ctx, `
			INSERT INTO contacts (name, phone, email, notes, updated_at)
			VALUES (?, ?, ?, ?, datetime('now'))
			ON CONFLICT DO NOTHING`,
			name, phone, email, notes)
		if err != nil {
			continue
		}
		// Update existing if phone changed.
		db.ExecContext(ctx, `
			UPDATE contacts SET phone=?, email=?, updated_at=datetime('now')
			WHERE name=? AND is_owner=0 AND (phone='' OR phone=?)`,
			phone, email, name, phone)
		synced++
	}

	return synced, nil
}

func normalizePhone(s string) string {
	clean := ""
	for _, c := range s {
		if (c >= '0' && c <= '9') || c == '+' {
			clean += string(c)
		}
	}
	if len(clean) == 10 {
		return "+1" + clean
	}
	if len(clean) == 11 && clean[0] == '1' {
		return "+" + clean
	}
	if len(clean) > 0 && clean[0] != '+' {
		return "+" + clean
	}
	return clean
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
