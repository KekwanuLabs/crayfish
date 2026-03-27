package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// RegisterContactTools registers contact_lookup and contact_save.
// Contacts are stored in the private contacts table — NOT in the memory system.
// Phone numbers and personal contact info should only ever live here.
func RegisterContactTools(reg *Registry, db *sql.DB) {
	reg.Register(&Tool{
		Name: "contact_lookup",
		Description: `Look up a contact's phone number, email, or details by name or relationship.
Use this BEFORE making any phone call. Never use memory_search for phone numbers.

Examples:
- contact_lookup(query="wife") → returns Sarah's phone number
- contact_lookup(query="mom") → returns mother's details
- contact_lookup(query="owner") → returns the owner's own phone number
- contact_lookup(query="John") → searches by name

Returns the contact's name, phone, relationship, and notes.`,
		MinTier: security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["query"],
			"properties": {
				"query": {
					"type": "string",
					"description": "Name, relationship (e.g. 'wife', 'mom', 'boss'), or 'owner' for the user themselves"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, params json.RawMessage) (string, error) {
			var input struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(params, &input); err != nil {
				return "", fmt.Errorf("invalid parameters: %w", err)
			}
			if input.Query == "" {
				return "", fmt.Errorf("query is required")
			}

			q := "%" + strings.ToLower(input.Query) + "%"
			isOwnerQuery := strings.ToLower(input.Query) == "owner" ||
				strings.ToLower(input.Query) == "me" ||
				strings.ToLower(input.Query) == "myself"

			var rows *sql.Rows
			var err error
			if isOwnerQuery {
				rows, err = db.QueryContext(ctx,
					`SELECT name, relationship, phone, email, notes, is_owner
					 FROM contacts WHERE is_owner = 1 LIMIT 1`)
			} else {
				rows, err = db.QueryContext(ctx,
					`SELECT name, relationship, phone, email, notes, is_owner
					 FROM contacts
					 WHERE LOWER(name) LIKE ? OR LOWER(relationship) LIKE ?
					 ORDER BY is_owner DESC, id ASC LIMIT 5`,
					q, q)
			}
			if err != nil {
				return "", fmt.Errorf("contact lookup: %w", err)
			}
			defer rows.Close()

			type contact struct {
				Name         string
				Relationship string
				Phone        string
				Email        string
				Notes        string
				IsOwner      bool
			}

			var results []contact
			for rows.Next() {
				var c contact
				var isOwner int
				if err := rows.Scan(&c.Name, &c.Relationship, &c.Phone, &c.Email, &c.Notes, &isOwner); err != nil {
					continue
				}
				c.IsOwner = isOwner == 1
				results = append(results, c)
			}

			if len(results) == 0 {
				return fmt.Sprintf("No contact found for %q. Ask the user to add them at Settings → Contacts in the dashboard, or ask for their number directly.", input.Query), nil
			}

			var sb strings.Builder
			for _, c := range results {
				sb.WriteString(fmt.Sprintf("**%s**", c.Name))
				if c.Relationship != "" {
					sb.WriteString(fmt.Sprintf(" (%s)", c.Relationship))
				}
				if c.IsOwner {
					sb.WriteString(" — *owner*")
				}
				sb.WriteString("\n")
				if c.Phone != "" {
					sb.WriteString(fmt.Sprintf("  📞 %s\n", c.Phone))
				}
				if c.Email != "" {
					sb.WriteString(fmt.Sprintf("  ✉️  %s\n", c.Email))
				}
				if c.Notes != "" {
					sb.WriteString(fmt.Sprintf("  📝 %s\n", c.Notes))
				}
			}
			return sb.String(), nil
		},
	})

	reg.Register(&Tool{
		Name: "contact_save",
		Description: `Save or update a contact in the private contacts table.
Use this when the user explicitly gives you someone's phone number to save.
Do NOT use this to save numbers learned from conversations without explicit permission.
For the user's own number, set is_owner=true.`,
		MinTier: security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["name"],
			"properties": {
				"name":         {"type": "string", "description": "Full name"},
				"relationship": {"type": "string", "description": "e.g. 'wife', 'mom', 'boss', 'self'"},
				"phone":        {"type": "string", "description": "Phone number (E.164 preferred)"},
				"email":        {"type": "string"},
				"notes":        {"type": "string"},
				"is_owner":     {"type": "boolean", "description": "True if this is the device owner themselves"}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, params json.RawMessage) (string, error) {
			var input struct {
				Name         string `json:"name"`
				Relationship string `json:"relationship"`
				Phone        string `json:"phone"`
				Email        string `json:"email"`
				Notes        string `json:"notes"`
				IsOwner      bool   `json:"is_owner"`
			}
			if err := json.Unmarshal(params, &input); err != nil {
				return "", fmt.Errorf("invalid parameters: %w", err)
			}
			if input.Name == "" {
				return "", fmt.Errorf("name is required")
			}

			isOwner := 0
			if input.IsOwner {
				isOwner = 1
			}

			// Normalize phone number.
			phone := normalizePhone(input.Phone)

			_, err := db.ExecContext(ctx, `
				INSERT INTO contacts (name, relationship, phone, email, notes, is_owner, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
				ON CONFLICT(id) DO UPDATE SET
					name=excluded.name, relationship=excluded.relationship,
					phone=excluded.phone, email=excluded.email,
					notes=excluded.notes, is_owner=excluded.is_owner,
					updated_at=datetime('now')`,
				input.Name, input.Relationship, phone, input.Email, input.Notes, isOwner)
			if err != nil {
				return "", fmt.Errorf("save contact: %w", err)
			}

			return fmt.Sprintf("Saved contact: %s (%s) — %s", input.Name, input.Relationship, phone), nil
		},
	})
}
