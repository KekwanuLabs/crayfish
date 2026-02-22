package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// BraveSearchConfig holds the Brave Search API configuration.
type BraveSearchConfig struct {
	APIKey string
}

// RegisterSearchTools adds web search tools to the registry.
func RegisterSearchTools(reg *Registry, cfg BraveSearchConfig) {
	reg.logger.Info("registering web search tools")

	httpClient := &http.Client{Timeout: 15 * time.Second}

	// web_search — search the web using Brave Search API.
	reg.Register(&Tool{
		Name:        "web_search",
		Description: "Search the web for current information using Brave Search. Use this when the user asks about recent events, needs factual information you're unsure about, wants to look something up, or needs real-time data.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "The search query"
				},
				"count": {
					"type": "integer",
					"description": "Number of results to return (default: 5, max: 10)"
				}
			},
			"required": ["query"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Query string `json:"query"`
				Count int    `json:"count"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("web_search: parse input: %w", err)
			}
			if params.Query == "" {
				return "", fmt.Errorf("web_search: query is required")
			}

			count := 5
			if params.Count > 0 && params.Count <= 10 {
				count = params.Count
			}

			// Call Brave Search API.
			apiURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
				url.QueryEscape(params.Query), count)

			req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
			if err != nil {
				return "", fmt.Errorf("web_search: create request: %w", err)
			}
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Accept-Encoding", "gzip")
			req.Header.Set("X-Subscription-Token", cfg.APIKey)

			resp, err := httpClient.Do(req)
			if err != nil {
				return "", fmt.Errorf("web_search: request failed: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				return "", fmt.Errorf("web_search: API returned %d: %s", resp.StatusCode, string(body))
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			if err != nil {
				return "", fmt.Errorf("web_search: read response: %w", err)
			}

			// Parse Brave Search response.
			var braveResp braveSearchResponse
			if err := json.Unmarshal(body, &braveResp); err != nil {
				return "", fmt.Errorf("web_search: parse response: %w", err)
			}

			// Format results for the LLM.
			var results []searchResult
			for _, r := range braveResp.Web.Results {
				results = append(results, searchResult{
					Title:       r.Title,
					URL:         r.URL,
					Description: r.Description,
				})
			}

			if len(results) == 0 {
				return "No results found for: " + params.Query, nil
			}

			// Return as formatted text for the LLM to synthesize.
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Search results for \"%s\":\n\n", params.Query))
			for i, r := range results {
				sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Description))
			}

			return sb.String(), nil
		},
	})
}

// Brave Search API response types.
type braveSearchResponse struct {
	Web struct {
		Results []braveWebResult `json:"results"`
	} `json:"web"`
}

type braveWebResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

type searchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}
