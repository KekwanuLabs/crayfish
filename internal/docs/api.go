package docs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const docsBaseURL = "https://docs.googleapis.com/v1"

// TokenProvider returns a valid OAuth access token.
type TokenProvider func(ctx context.Context) (string, error)

// Client is a stdlib-only Google Docs REST API client.
type Client struct {
	tokenProvider TokenProvider
	httpClient    *http.Client
}

// NewClient creates a new Google Docs REST API client.
func NewClient(tokenProvider TokenProvider) *Client {
	return &Client{
		tokenProvider: tokenProvider,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Create creates a new Google Doc with the given title.
// Returns the document ID and its web view link.
func (c *Client) Create(ctx context.Context, title string) (docID, webViewLink string, err error) {
	body, _ := json.Marshal(map[string]string{"title": title})
	resp, err := c.doRequest(ctx, "POST", "/documents", body)
	if err != nil {
		return "", "", fmt.Errorf("docs: create: %w", err)
	}
	var r struct {
		DocumentID string `json:"documentId"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return "", "", fmt.Errorf("docs: parse create response: %w", err)
	}
	link := "https://docs.google.com/document/d/" + r.DocumentID + "/edit"
	return r.DocumentID, link, nil
}

// AppendText appends text to the end of an existing Google Doc.
func (c *Client) AppendText(ctx context.Context, docID, text string) error {
	_, _, endIndex, err := c.GetDoc(ctx, docID)
	if err != nil {
		return fmt.Errorf("docs: append (get doc): %w", err)
	}
	// Insert before the final paragraph end marker.
	insertAt := endIndex - 1
	if insertAt < 1 {
		insertAt = 1
	}
	req := map[string]interface{}{
		"requests": []map[string]interface{}{
			{
				"insertText": map[string]interface{}{
					"location": map[string]interface{}{
						"index": insertAt,
					},
					"text": text,
				},
			},
		},
	}
	body, _ := json.Marshal(req)
	_, err = c.doRequest(ctx, "POST", "/documents/"+docID+":batchUpdate", body)
	if err != nil {
		return fmt.Errorf("docs: append text: %w", err)
	}
	return nil
}

// GetDoc fetches a document's title, plain text content, and the end index of the body.
// endIndex is needed by AppendText to know where to insert.
func (c *Client) GetDoc(ctx context.Context, docID string) (title, bodyText string, endIndex int, err error) {
	resp, err := c.doRequest(ctx, "GET", "/documents/"+docID, nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("docs: get doc: %w", err)
	}
	var doc struct {
		Title string `json:"title"`
		Body  struct {
			Content []struct {
				EndIndex  int `json:"endIndex"`
				Paragraph *struct {
					Elements []struct {
						TextRun *struct {
							Content string `json:"content"`
						} `json:"textRun"`
					} `json:"elements"`
				} `json:"paragraph"`
			} `json:"content"`
		} `json:"body"`
	}
	if err := json.Unmarshal(resp, &doc); err != nil {
		return "", "", 0, fmt.Errorf("docs: parse doc: %w", err)
	}

	var sb strings.Builder
	lastEndIndex := 1
	for _, el := range doc.Body.Content {
		if el.EndIndex > lastEndIndex {
			lastEndIndex = el.EndIndex
		}
		if el.Paragraph != nil {
			for _, pe := range el.Paragraph.Elements {
				if pe.TextRun != nil {
					sb.WriteString(pe.TextRun.Content)
				}
			}
		}
	}
	return doc.Title, sb.String(), lastEndIndex, nil
}

// doRequest executes an authenticated HTTP request against the Docs API.
func (c *Client) doRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	token, err := c.tokenProvider(ctx)
	if err != nil {
		return nil, fmt.Errorf("docs api: get token: %w", err)
	}

	fullURL := docsBaseURL + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("docs api: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docs api: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("docs api: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("docs api: %s %s returned %d: %s", method, path, resp.StatusCode, truncate(string(respBody), 200))
	}

	return respBody, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
