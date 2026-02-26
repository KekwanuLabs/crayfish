package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// SettingsDeps holds dependencies for the settings_update tool.
type SettingsDeps struct {
	// GetSettings returns the current user-configurable settings.
	GetSettings func() map[string]any
	// UpdateSettings applies changes and returns a summary of what changed.
	UpdateSettings func(updates map[string]any) error
}

// RegisterSettingsTool adds the settings_update tool to the registry.
func RegisterSettingsTool(reg *Registry, deps SettingsDeps) {
	reg.logger.Info("registering settings_update tool")

	reg.Register(&Tool{
		Name: "settings_update",
		Description: `View or update Crayfish's behavior settings. Call with no parameters to see current settings. Configurable fields:
- heartbeat_interval_minutes (int): How often to check email/calendar (default: 30)
- heartbeat_work_hour_start (int): Start of work hours, 0-23 (default: 9)
- heartbeat_work_hour_end (int): End of work hours, 0-23 (default: 18)
- heartbeat_weekdays_only (bool): Only check during weekdays (default: true)
- urgency_keywords (string[]): Keywords that trigger urgent email notifications (default: urgent, asap, important, action required, deadline, reminder)
- auto_reply_enabled (bool): Auto-reply to threads Crayfish started (default: false)

Use this tool when the user asks to change check-in frequency, work hours, urgency keywords, or auto-reply behavior.`,
		MinTier: security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"heartbeat_interval_minutes": {
					"type": "integer",
					"description": "Check-in interval in minutes (1-1440)"
				},
				"heartbeat_work_hour_start": {
					"type": "integer",
					"description": "Work hours start (0-23)"
				},
				"heartbeat_work_hour_end": {
					"type": "integer",
					"description": "Work hours end (0-23)"
				},
				"heartbeat_weekdays_only": {
					"type": "boolean",
					"description": "Only check during weekdays"
				},
				"urgency_keywords": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Keywords for urgent email detection"
				},
				"auto_reply_enabled": {
					"type": "boolean",
					"description": "Enable auto-reply to threads Crayfish started"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				HeartbeatIntervalMins  *int      `json:"heartbeat_interval_minutes"`
				HeartbeatWorkHourStart *int      `json:"heartbeat_work_hour_start"`
				HeartbeatWorkHourEnd   *int      `json:"heartbeat_work_hour_end"`
				HeartbeatWeekdaysOnly  *bool     `json:"heartbeat_weekdays_only"`
				UrgencyKeywords        []string  `json:"urgency_keywords"`
				AutoReplyEnabled       *bool     `json:"auto_reply_enabled"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("settings_update: parse input: %w", err)
			}

			// If no params provided, return current settings.
			hasUpdates := params.HeartbeatIntervalMins != nil ||
				params.HeartbeatWorkHourStart != nil ||
				params.HeartbeatWorkHourEnd != nil ||
				params.HeartbeatWeekdaysOnly != nil ||
				params.UrgencyKeywords != nil ||
				params.AutoReplyEnabled != nil

			if !hasUpdates {
				current := deps.GetSettings()
				out, _ := json.Marshal(current)
				return string(out), nil
			}

			// Validate and build updates.
			updates := make(map[string]any)
			var changes []string

			if params.HeartbeatIntervalMins != nil {
				v := *params.HeartbeatIntervalMins
				if v < 1 || v > 1440 {
					return "", fmt.Errorf("settings_update: heartbeat_interval_minutes must be 1-1440")
				}
				updates["heartbeat_interval_minutes"] = v
				changes = append(changes, fmt.Sprintf("check-in interval → %d minutes", v))
			}

			if params.HeartbeatWorkHourStart != nil {
				v := *params.HeartbeatWorkHourStart
				if v < 0 || v > 23 {
					return "", fmt.Errorf("settings_update: heartbeat_work_hour_start must be 0-23")
				}
				updates["heartbeat_work_hour_start"] = v
				changes = append(changes, fmt.Sprintf("work hours start → %d:00", v))
			}

			if params.HeartbeatWorkHourEnd != nil {
				v := *params.HeartbeatWorkHourEnd
				if v < 0 || v > 23 {
					return "", fmt.Errorf("settings_update: heartbeat_work_hour_end must be 0-23")
				}
				updates["heartbeat_work_hour_end"] = v
				changes = append(changes, fmt.Sprintf("work hours end → %d:00", v))
			}

			if params.HeartbeatWeekdaysOnly != nil {
				updates["heartbeat_weekdays_only"] = *params.HeartbeatWeekdaysOnly
				if *params.HeartbeatWeekdaysOnly {
					changes = append(changes, "weekdays only → on")
				} else {
					changes = append(changes, "weekdays only → off (checking every day)")
				}
			}

			if params.UrgencyKeywords != nil {
				updates["urgency_keywords"] = params.UrgencyKeywords
				changes = append(changes, fmt.Sprintf("urgency keywords → [%s]", strings.Join(params.UrgencyKeywords, ", ")))
			}

			if params.AutoReplyEnabled != nil {
				updates["auto_reply_enabled"] = *params.AutoReplyEnabled
				if *params.AutoReplyEnabled {
					changes = append(changes, "auto-reply → enabled")
				} else {
					changes = append(changes, "auto-reply → disabled")
				}
			}

			if err := deps.UpdateSettings(updates); err != nil {
				return "", fmt.Errorf("settings_update: %w", err)
			}

			return fmt.Sprintf("Settings updated:\n• %s", strings.Join(changes, "\n• ")), nil
		},
	})
}
