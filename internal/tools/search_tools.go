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

// BraveConnectDeps holds dependencies for the brave_connect tool.
type BraveConnectDeps struct {
	IsConfigured func() bool          // Check if Brave is already configured.
	SaveKey      func(key string)     // Persist key to config and update in-memory state.
	Registry     *Registry            // Tool registry for dynamic registration.
}

// RegisterBraveConnectTool adds the brave_connect tool so users can set up
// web search conversationally. Always registered regardless of whether Brave
// is already configured.
func RegisterBraveConnectTool(reg *Registry, deps BraveConnectDeps) {
	reg.logger.Info("registering brave_connect tool")

	reg.Register(&Tool{
		Name:        "brave_connect",
		Description: "Set up web search by adding a Brave Search API key. Walk the user through getting a free key from brave.com/search/api, then verify and activate it. If the user provides a key, verify it and enable web search.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"api_key": {
					"type": "string",
					"description": "The Brave Search API key to verify and save"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				APIKey string `json:"api_key"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("brave_connect: parse input: %w", err)
			}

			// If no key provided, return instructions for the agent to relay.
			if params.APIKey == "" {
				if deps.IsConfigured() {
					return "Web search is already configured and working. No action needed.", nil
				}
				return `Web search is not set up yet. To enable it, the user needs a free Brave Search API key:

1. Go to https://brave.com/search/api/
2. Click "Get Started" and create a free account
3. The free tier gives 2,000 searches per month
4. Copy the API key and paste it here

Once the user has the key, call this tool again with the api_key parameter to verify and activate it.`, nil
			}

			// Verify the key by making a test search.
			apiKey := strings.TrimSpace(params.APIKey)
			httpClient := &http.Client{Timeout: 10 * time.Second}
			testURL := "https://api.search.brave.com/res/v1/web/search?q=test&count=1"

			req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
			if err != nil {
				return "", fmt.Errorf("brave_connect: create test request: %w", err)
			}
			req.Header.Set("Accept", "application/json")
			req.Header.Set("X-Subscription-Token", apiKey)

			resp, err := httpClient.Do(req)
			if err != nil {
				return "Could not reach the Brave Search API. Check your internet connection and try again.", nil
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				return "That API key was rejected by Brave. Please double-check it and try again. Make sure you're copying the full key from https://brave.com/search/api/", nil
			}

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				return fmt.Sprintf("Brave API returned an unexpected error (HTTP %d): %s. Try again in a moment.", resp.StatusCode, string(body)), nil
			}

			// Key works — save it and register the search tools.
			deps.SaveKey(apiKey)
			RegisterSearchTools(deps.Registry, BraveSearchConfig{APIKey: apiKey})

			return "Web search is now active! The API key has been verified and saved. You can now use the web_search tool to look things up.", nil
		},
	})
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
