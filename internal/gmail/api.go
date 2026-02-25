package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	gmailBaseURL   = "https://gmail.googleapis.com/gmail/v1/users/me"
	maxPreviewBody = 4096  // 4KB preview
	maxFullBody    = 65536 // 64KB full body
)

// TokenProvider returns a valid OAuth access token.
type TokenProvider func(ctx context.Context) (string, error)

// APIClient is a stdlib-only Gmail REST API client.
type APIClient struct {
	tokenProvider TokenProvider
	httpClient    *http.Client
}

// NewAPIClient creates a new Gmail REST API client.
func NewAPIClient(tokenProvider TokenProvider) *APIClient {
	return &APIClient{
		tokenProvider: tokenProvider,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// --- JSON response/request types ---

type gmailListResponse struct {
	Messages      []gmailMessageRef `json:"messages"`
	NextPageToken string            `json:"nextPageToken"`
}

type gmailMessageRef struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

type gmailMessage struct {
	ID           string       `json:"id"`
	ThreadID     string       `json:"threadId"`
	LabelIDs     []string     `json:"labelIds"`
	Payload      gmailPayload `json:"payload"`
	InternalDate string       `json:"internalDate"` // millis since epoch
}

type gmailPayload struct {
	MimeType string        `json:"mimeType"`
	Headers  []gmailHeader `json:"headers"`
	Body     gmailBody     `json:"body"`
	Parts    []gmailPart   `json:"parts"`
}

type gmailPart struct {
	MimeType string      `json:"mimeType"`
	Headers  []gmailHeader `json:"headers"`
	Body     gmailBody   `json:"body"`
	Parts    []gmailPart `json:"parts"`
	Filename string      `json:"filename"`
}

type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailBody struct {
	Size int    `json:"size"`
	Data string `json:"data"` // base64url encoded
}

type gmailProfile struct {
	EmailAddress string `json:"emailAddress"`
}

type gmailModifyRequest struct {
	AddLabelIDs    []string `json:"addLabelIds,omitempty"`
	RemoveLabelIDs []string `json:"removeLabelIds,omitempty"`
}

// --- Public methods ---

// GetProfile returns the authenticated user's email address.
func (c *APIClient) GetProfile(ctx context.Context) (string, error) {
	body, err := c.doRequest(ctx, "GET", "/profile", nil)
	if err != nil {
		return "", fmt.Errorf("gmail api: get profile: %w", err)
	}
	var p gmailProfile
	if err := json.Unmarshal(body, &p); err != nil {
		return "", fmt.Errorf("gmail api: parse profile: %w", err)
	}
	return p.EmailAddress, nil
}

// ListMessages returns message IDs matching the given Gmail search query.
func (c *APIClient) ListMessages(ctx context.Context, query string, maxResults int) ([]string, error) {
	params := url.Values{}
	if query != "" {
		params.Set("q", query)
	}
	if maxResults > 0 {
		params.Set("maxResults", strconv.Itoa(maxResults))
	}

	body, err := c.doRequest(ctx, "GET", "/messages?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("gmail api: list messages: %w", err)
	}

	var resp gmailListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("gmail api: parse list: %w", err)
	}

	ids := make([]string, len(resp.Messages))
	for i, m := range resp.Messages {
		ids[i] = m.ID
	}
	return ids, nil
}

// GetMessage fetches a single message by ID. If fullBody is true, the entire
// body is returned (up to 64KB); otherwise only a preview (up to 4KB).
func (c *APIClient) GetMessage(ctx context.Context, id string, fullBody bool) (*Email, error) {
	body, err := c.doRequest(ctx, "GET", "/messages/"+id+"?format=full", nil)
	if err != nil {
		return nil, fmt.Errorf("gmail api: get message %s: %w", id, err)
	}

	var msg gmailMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("gmail api: parse message %s: %w", id, err)
	}

	return c.parseMessage(&msg, fullBody)
}

// ModifyMessage adds or removes labels on a message.
func (c *APIClient) ModifyMessage(ctx context.Context, id string, addLabels, removeLabels []string) error {
	req := gmailModifyRequest{
		AddLabelIDs:    addLabels,
		RemoveLabelIDs: removeLabels,
	}
	payload, _ := json.Marshal(req)
	_, err := c.doRequest(ctx, "POST", "/messages/"+id+"/modify", payload)
	if err != nil {
		return fmt.Errorf("gmail api: modify message %s: %w", id, err)
	}
	return nil
}

// SendMessage sends an RFC 2822 email via Gmail API.
func (c *APIClient) SendMessage(ctx context.Context, from, to, subject, body, inReplyTo string) error {
	// Build RFC 2822 message.
	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("From: %s\r\n", from))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	if inReplyTo != "" {
		msg.WriteString(fmt.Sprintf("In-Reply-To: %s\r\n", inReplyTo))
		msg.WriteString(fmt.Sprintf("References: %s\r\n", inReplyTo))
	}
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	// Base64url encode the message (no padding, URL-safe).
	encoded := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(msg.String()))

	payload, _ := json.Marshal(map[string]string{"raw": encoded})
	_, err := c.doRequest(ctx, "POST", "/messages/send", payload)
	if err != nil {
		return fmt.Errorf("gmail api: send message: %w", err)
	}
	return nil
}

// --- Convenience methods ---

// FetchUnread returns up to max unread inbox messages.
func (c *APIClient) FetchUnread(ctx context.Context, max int) ([]Email, error) {
	ids, err := c.ListMessages(ctx, "is:unread in:inbox", max)
	if err != nil {
		return nil, err
	}

	var emails []Email
	for _, id := range ids {
		e, err := c.GetMessage(ctx, id, false)
		if err != nil {
			continue
		}
		emails = append(emails, *e)
	}
	return emails, nil
}

// FetchByID fetches a single message with full body.
func (c *APIClient) FetchByID(ctx context.Context, id string) (*Email, error) {
	return c.GetMessage(ctx, id, true)
}

// MarkRead marks a message as read by removing the UNREAD label.
func (c *APIClient) MarkRead(ctx context.Context, id string) error {
	return c.ModifyMessage(ctx, id, nil, []string{"UNREAD"})
}

// Archive removes the INBOX label from a message.
func (c *APIClient) Archive(ctx context.Context, id string) error {
	return c.ModifyMessage(ctx, id, nil, []string{"INBOX"})
}

// AddLabel adds a label to a message.
func (c *APIClient) AddLabel(ctx context.Context, id string, label string) error {
	return c.ModifyMessage(ctx, id, []string{label}, nil)
}

// --- Internal helpers ---

// doRequest executes an authenticated HTTP request against the Gmail API.
// Includes a single retry on HTTP 429.
func (c *APIClient) doRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	resp, respBody, err := c.executeRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}

	// Retry once on rate limit.
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := 5 * time.Second
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				retryAfter = time.Duration(secs) * time.Second
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryAfter):
		}

		resp, respBody, err = c.executeRequest(ctx, method, path, body)
		if err != nil {
			return nil, err
		}
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("gmail api: %s %s returned %d: %s", method, path, resp.StatusCode, truncate(string(respBody), 200))
	}

	return respBody, nil
}

func (c *APIClient) executeRequest(ctx context.Context, method, path string, body []byte) (*http.Response, []byte, error) {
	token, err := c.tokenProvider(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("gmail api: get token: %w", err)
	}

	fullURL := gmailBaseURL + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("gmail api: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("gmail api: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max response
	if err != nil {
		return nil, nil, fmt.Errorf("gmail api: read response: %w", err)
	}

	return resp, respBody, nil
}

// parseMessage converts a Gmail API message to our Email type.
func (c *APIClient) parseMessage(msg *gmailMessage, fullBody bool) (*Email, error) {
	e := Email{
		ID:       msg.ID,
		ThreadID: msg.ThreadID,
	}

	// Parse headers.
	for _, h := range msg.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "from":
			e.From = h.Value
		case "to":
			e.To = h.Value
		case "cc":
			e.Cc = h.Value
		case "subject":
			e.Subject = h.Value
		case "message-id":
			e.MessageID = h.Value
		}
	}

	if e.MessageID == "" {
		e.MessageID = msg.ID
	}

	// Parse timestamp.
	if millis, err := strconv.ParseInt(msg.InternalDate, 10, 64); err == nil {
		e.ReceivedAt = time.UnixMilli(millis)
	}

	// Parse labels.
	e.IsRead = true
	for _, l := range msg.LabelIDs {
		if l == "UNREAD" {
			e.IsRead = false
		}
		if l == "STARRED" {
			e.IsStarred = true
		}
	}
	labelsJSON, _ := json.Marshal(msg.LabelIDs)
	e.Labels = string(labelsJSON)

	// Extract body text.
	limit := maxPreviewBody
	if fullBody {
		limit = maxFullBody
	}
	bodyText := extractBodyText(&msg.Payload, limit)

	e.BodyPreview = cleanPreview(bodyText)
	if fullBody {
		e.BodyFull = bodyText
	}

	// Check for attachments.
	e.HasAttachments = hasAttachments(&msg.Payload)

	return &e, nil
}

// extractBodyText walks the MIME payload tree, preferring text/plain over text/html.
func extractBodyText(payload *gmailPayload, limit int) string {
	// Single-part message.
	if payload.MimeType == "text/plain" && payload.Body.Data != "" {
		return decodeBody(payload.Body.Data, limit)
	}

	// Multipart: walk parts tree.
	if len(payload.Parts) > 0 {
		text := walkPartsForText(payload.Parts, limit)
		if text != "" {
			return text
		}
	}

	// Fallback: try to decode the top-level body regardless of MIME type.
	if payload.Body.Data != "" {
		return decodeBody(payload.Body.Data, limit)
	}

	return ""
}

// walkPartsForText recursively searches for text/plain, falling back to text/html.
func walkPartsForText(parts []gmailPart, limit int) string {
	var htmlFallback string

	for _, p := range parts {
		if p.MimeType == "text/plain" && p.Body.Data != "" {
			return decodeBody(p.Body.Data, limit)
		}
		if p.MimeType == "text/html" && p.Body.Data != "" && htmlFallback == "" {
			htmlFallback = decodeBody(p.Body.Data, limit)
		}
		// Recurse into nested parts (e.g. multipart/alternative inside multipart/mixed).
		if len(p.Parts) > 0 {
			if text := walkPartsForText(p.Parts, limit); text != "" {
				return text
			}
		}
	}

	return htmlFallback
}

func hasAttachments(payload *gmailPayload) bool {
	for _, p := range payload.Parts {
		if p.Filename != "" {
			return true
		}
		if len(p.Parts) > 0 {
			sub := gmailPayload{Parts: partsToPayloadParts(p.Parts)}
			if hasAttachments(&sub) {
				return true
			}
		}
	}
	return false
}

func partsToPayloadParts(parts []gmailPart) []gmailPart {
	return parts
}

// decodeBody decodes base64url-encoded data from the Gmail API.
func decodeBody(data string, limit int) string {
	decoded, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(data)
	if err != nil {
		return ""
	}
	if len(decoded) > limit {
		decoded = decoded[:limit]
	}
	return string(decoded)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
