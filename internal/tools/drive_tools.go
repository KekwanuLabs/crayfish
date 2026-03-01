package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KekwanuLabs/crayfish/internal/drive"
	"github.com/KekwanuLabs/crayfish/internal/security"
)

// RegisterDriveTools adds Google Drive and Docs tools to the registry.
// Called when drive.file scope is present on the Google token.
// Docs are created via Drive API (mimeType trick) — no Docs API scope required.
func RegisterDriveTools(reg *Registry, driveClient *drive.Client) {
	reg.logger.Info("registering drive/docs tools")

	// drive_create_folder — create a folder in Drive
	reg.Register(&Tool{
		Name:        "drive_create_folder",
		Description: "Create a new folder in Google Drive.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Folder name"
				},
				"parent_folder_id": {
					"type": "string",
					"description": "Parent folder ID. Leave empty to create in My Drive root."
				}
			},
			"required": ["name"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Name           string `json:"name"`
				ParentFolderID string `json:"parent_folder_id"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("drive_create_folder: parse input: %w", err)
			}
			if params.Name == "" {
				return "", fmt.Errorf("drive_create_folder: name is required")
			}
			id, webLink, err := driveClient.CreateFolder(ctx, params.Name, params.ParentFolderID)
			if err != nil {
				return "", fmt.Errorf("drive_create_folder: %w", err)
			}
			result, _ := json.Marshal(map[string]string{
				"id":       id,
				"name":     params.Name,
				"web_link": webLink,
			})
			return string(result), nil
		},
	})

	// drive_list_files — list/search files in Drive
	reg.Register(&Tool{
		Name:        "drive_list_files",
		Description: "List or search files and folders in Google Drive.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Drive search query (e.g., 'name contains \"Trip\"' or 'mimeType = \"application/vnd.google-apps.folder\"'). Leave empty to list recent files."
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of results to return (default: 20)"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			json.Unmarshal(input, &params)
			limit := 20
			if params.Limit > 0 {
				limit = params.Limit
			}
			files, err := driveClient.ListFiles(ctx, params.Query, limit)
			if err != nil {
				return "", fmt.Errorf("drive_list_files: %w", err)
			}
			if len(files) == 0 {
				return "No files found.", nil
			}
			type fileResult struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Type     string `json:"type"`
				WebLink  string `json:"web_link"`
				Modified string `json:"modified"`
			}
			var results []fileResult
			for _, f := range files {
				fileType := "file"
				switch f.MimeType {
				case "application/vnd.google-apps.folder":
					fileType = "folder"
				case "application/vnd.google-apps.document":
					fileType = "doc"
				case "application/vnd.google-apps.spreadsheet":
					fileType = "sheet"
				}
				results = append(results, fileResult{
					ID:       f.ID,
					Name:     f.Name,
					Type:     fileType,
					WebLink:  f.WebViewLink,
					Modified: f.ModifiedTime,
				})
			}
			out, _ := json.Marshal(results)
			return string(out), nil
		},
	})

	// drive_share — share a file or folder with someone
	reg.Register(&Tool{
		Name:        "drive_share",
		Description: "Share a Google Drive file or folder with someone by email.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_id": {
					"type": "string",
					"description": "The file or folder ID to share"
				},
				"email": {
					"type": "string",
					"description": "Email address of the person to share with"
				},
				"role": {
					"type": "string",
					"description": "Permission level: 'reader', 'writer', or 'commenter'"
				}
			},
			"required": ["file_id", "email", "role"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				FileID string `json:"file_id"`
				Email  string `json:"email"`
				Role   string `json:"role"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("drive_share: parse input: %w", err)
			}
			if params.FileID == "" {
				return "", fmt.Errorf("drive_share: file_id is required")
			}
			if params.Email == "" {
				return "", fmt.Errorf("drive_share: email is required")
			}
			role := params.Role
			if role == "" {
				role = "reader"
			}
			if err := driveClient.ShareFile(ctx, params.FileID, params.Email, role); err != nil {
				return "", fmt.Errorf("drive_share: %w", err)
			}
			result, _ := json.Marshal(map[string]string{
				"status":      "shared",
				"file_id":     params.FileID,
				"shared_with": params.Email,
				"role":        role,
			})
			return string(result), nil
		},
	})

	// docs_create — create a Google Doc via Drive API (no Docs API scope needed)
	reg.Register(&Tool{
		Name:        "docs_create",
		Description: "Create a new Google Doc inside a Drive folder.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {
					"type": "string",
					"description": "Document title"
				},
				"folder_id": {
					"type": "string",
					"description": "Drive folder ID to place the doc in (optional)"
				}
			},
			"required": ["title"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Title    string `json:"title"`
				FolderID string `json:"folder_id"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("docs_create: parse input: %w", err)
			}
			if params.Title == "" {
				return "", fmt.Errorf("docs_create: title is required")
			}
			// Create a Google Doc via Drive API using the Docs mimeType.
			// This only requires drive.file scope — no Docs API scope needed.
			docID, webLink, err := driveClient.CreateFile(ctx, params.Title,
				"application/vnd.google-apps.document", params.FolderID)
			if err != nil {
				return "", fmt.Errorf("docs_create: %w", err)
			}
			result, _ := json.Marshal(map[string]string{
				"id":       docID,
				"title":    params.Title,
				"web_link": webLink,
			})
			return string(result), nil
		},
	})

	// sheets_create — create a Google Sheet via Drive API (no Sheets API scope needed)
	reg.Register(&Tool{
		Name:        "sheets_create",
		Description: "Create a new Google Sheet inside a Drive folder.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {
					"type": "string",
					"description": "Spreadsheet title"
				},
				"folder_id": {
					"type": "string",
					"description": "Drive folder ID to place the sheet in (optional)"
				}
			},
			"required": ["title"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Title    string `json:"title"`
				FolderID string `json:"folder_id"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("sheets_create: parse input: %w", err)
			}
			if params.Title == "" {
				return "", fmt.Errorf("sheets_create: title is required")
			}
			sheetID, webLink, err := driveClient.CreateFile(ctx, params.Title,
				"application/vnd.google-apps.spreadsheet", params.FolderID)
			if err != nil {
				return "", fmt.Errorf("sheets_create: %w", err)
			}
			result, _ := json.Marshal(map[string]string{
				"id":       sheetID,
				"title":    params.Title,
				"web_link": webLink,
			})
			return string(result), nil
		},
	})
}
