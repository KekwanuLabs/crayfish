package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const driveBaseURL = "https://www.googleapis.com/drive/v3"

// TokenProvider returns a valid OAuth access token.
type TokenProvider func(ctx context.Context) (string, error)

// Client is a stdlib-only Google Drive REST API client.
type Client struct {
	tokenProvider TokenProvider
	httpClient    *http.Client
}

// NewClient creates a new Drive REST API client.
func NewClient(tokenProvider TokenProvider) *Client {
	return &Client{
		tokenProvider: tokenProvider,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// FileInfo holds metadata for a Drive file or folder.
type FileInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	WebViewLink  string `json:"webViewLink"`
	ModifiedTime string `json:"modifiedTime"`
}

type createFileRequest struct {
	Name     string   `json:"name"`
	MimeType string   `json:"mimeType"`
	Parents  []string `json:"parents,omitempty"`
}

type createFileResponse struct {
	ID          string `json:"id"`
	WebViewLink string `json:"webViewLink"`
}

type listFilesResponse struct {
	Files []FileInfo `json:"files"`
}

type fileParentsResponse struct {
	Parents []string `json:"parents"`
}

// CreateFolder creates a new folder in Drive. Use parentID="" for My Drive root.
func (c *Client) CreateFolder(ctx context.Context, name, parentID string) (id, webViewLink string, err error) {
	if parentID == "" {
		parentID = "root"
	}
	body, _ := json.Marshal(createFileRequest{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentID},
	})
	resp, err := c.doRequest(ctx, "POST", "/files?fields=id,webViewLink", body)
	if err != nil {
		return "", "", fmt.Errorf("drive: create folder: %w", err)
	}
	var r createFileResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		return "", "", fmt.Errorf("drive: parse create folder response: %w", err)
	}
	return r.ID, r.WebViewLink, nil
}

// CreateFile creates a new empty file in Drive with the given MIME type.
func (c *Client) CreateFile(ctx context.Context, name, mimeType, parentID string) (id, webViewLink string, err error) {
	parents := []string{"root"}
	if parentID != "" {
		parents = []string{parentID}
	}
	body, _ := json.Marshal(createFileRequest{
		Name:     name,
		MimeType: mimeType,
		Parents:  parents,
	})
	resp, err := c.doRequest(ctx, "POST", "/files?fields=id,webViewLink", body)
	if err != nil {
		return "", "", fmt.Errorf("drive: create file: %w", err)
	}
	var r createFileResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		return "", "", fmt.Errorf("drive: parse create file response: %w", err)
	}
	return r.ID, r.WebViewLink, nil
}

// ListFiles returns files matching the given Drive search query.
// Use query="" to list recent files. limit=0 defaults to 20.
func (c *Client) ListFiles(ctx context.Context, query string, limit int) ([]FileInfo, error) {
	params := url.Values{}
	params.Set("fields", "files(id,name,mimeType,webViewLink,modifiedTime)")
	if query != "" {
		params.Set("q", query)
	}
	if limit > 0 {
		params.Set("pageSize", strconv.Itoa(limit))
	}
	resp, err := c.doRequest(ctx, "GET", "/files?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("drive: list files: %w", err)
	}
	var r listFilesResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		return nil, fmt.Errorf("drive: parse list files response: %w", err)
	}
	return r.Files, nil
}

// ShareFile shares a file or folder with a user. role must be "reader", "writer", or "commenter".
func (c *Client) ShareFile(ctx context.Context, fileID, email, role string) error {
	body, _ := json.Marshal(map[string]string{
		"type":         "user",
		"role":         role,
		"emailAddress": email,
	})
	_, err := c.doRequest(ctx, "POST", "/files/"+fileID+"/permissions", body)
	if err != nil {
		return fmt.Errorf("drive: share file: %w", err)
	}
	return nil
}

// MoveFile moves a file to a new parent folder, removing all existing parents.
func (c *Client) MoveFile(ctx context.Context, fileID, newParentID string) error {
	// Fetch current parents so we can remove them.
	resp, err := c.doRequest(ctx, "GET", "/files/"+fileID+"?fields=parents", nil)
	if err != nil {
		return fmt.Errorf("drive: move file (get parents): %w", err)
	}
	var fp fileParentsResponse
	if err := json.Unmarshal(resp, &fp); err != nil {
		return fmt.Errorf("drive: move file (parse parents): %w", err)
	}
	oldParents := strings.Join(fp.Parents, ",")
	path := fmt.Sprintf("/files/%s?addParents=%s&removeParents=%s&fields=id",
		fileID,
		url.QueryEscape(newParentID),
		url.QueryEscape(oldParents),
	)
	_, err = c.doRequest(ctx, "PATCH", path, []byte("{}"))
	if err != nil {
		return fmt.Errorf("drive: move file: %w", err)
	}
	return nil
}

// doRequest executes an authenticated HTTP request against the Drive API.
func (c *Client) doRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	token, err := c.tokenProvider(ctx)
	if err != nil {
		return nil, fmt.Errorf("drive api: get token: %w", err)
	}

	fullURL := driveBaseURL + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("drive api: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("drive api: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("drive api: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("drive api: %s %s returned %d: %s", method, path, resp.StatusCode, truncate(string(respBody), 200))
	}

	return respBody, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
