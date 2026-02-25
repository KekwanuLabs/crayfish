package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	hubCacheTTL    = 15 * time.Minute
	hubIndexLimit  = 256 * 1024 // 256KB max index size
	hubSkillLimit  = 64 * 1024  // 64KB max skill YAML size
	hubHTTPTimeout = 10 * time.Second
)

// HubClient fetches and caches the remote skill index.
type HubClient struct {
	indexURL string
	logger   *slog.Logger
	client   *http.Client

	mu       sync.RWMutex
	cache    *HubIndex
	cacheTTL time.Time
}

// HubIndex is the top-level structure of the remote skill index.
type HubIndex struct {
	Version int        `json:"version"`
	Skills  []HubSkill `json:"skills"`
}

// HubSkill represents a skill available in the hub.
type HubSkill struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	LongDescription string   `json:"long_description,omitempty"`
	Author          string   `json:"author,omitempty"`
	Version         int      `json:"version"`
	Type            string   `json:"type"`
	Tags            []string `json:"tags,omitempty"`
	URL             string   `json:"url"`
	Requires        []string `json:"requires,omitempty"`
}

// NewHubClient creates a new Skill Hub client.
func NewHubClient(indexURL string, logger *slog.Logger) *HubClient {
	return &HubClient{
		indexURL: indexURL,
		logger:   logger,
		client:   &http.Client{Timeout: hubHTTPTimeout},
	}
}

// FetchIndex retrieves the skill index, using a 15-minute cache.
func (h *HubClient) FetchIndex(ctx context.Context) (*HubIndex, error) {
	h.mu.RLock()
	if h.cache != nil && time.Now().Before(h.cacheTTL) {
		idx := h.cache
		h.mu.RUnlock()
		return idx, nil
	}
	h.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, "GET", h.indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("hub: create request: %w", err)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hub: fetch index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub: index returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, hubIndexLimit))
	if err != nil {
		return nil, fmt.Errorf("hub: read index: %w", err)
	}

	var index HubIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("hub: parse index: %w", err)
	}

	h.mu.Lock()
	h.cache = &index
	h.cacheTTL = time.Now().Add(hubCacheTTL)
	h.mu.Unlock()

	h.logger.Info("hub index fetched", "skills", len(index.Skills))
	return &index, nil
}

// Search returns hub skills matching the query (case-insensitive match on name, description, tags).
func (h *HubClient) Search(ctx context.Context, query string) ([]HubSkill, error) {
	index, err := h.FetchIndex(ctx)
	if err != nil {
		return nil, err
	}

	if query == "" {
		return index.Skills, nil
	}

	lower := strings.ToLower(query)
	var results []HubSkill
	for _, s := range index.Skills {
		if strings.Contains(strings.ToLower(s.Name), lower) ||
			strings.Contains(strings.ToLower(s.Description), lower) {
			results = append(results, s)
			continue
		}
		for _, tag := range s.Tags {
			if strings.Contains(strings.ToLower(tag), lower) {
				results = append(results, s)
				break
			}
		}
	}

	return results, nil
}

// FindByName looks up a single skill in the hub index by name.
func (h *HubClient) FindByName(ctx context.Context, name string) (*HubSkill, error) {
	index, err := h.FetchIndex(ctx)
	if err != nil {
		return nil, err
	}

	lower := strings.ToLower(name)
	for _, s := range index.Skills {
		if strings.ToLower(s.Name) == lower {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("hub: skill %q not found in index", name)
}

// FetchSkill downloads a skill YAML from a URL, parses, and validates it.
func (h *HubClient) FetchSkill(ctx context.Context, skillURL string) (*Skill, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", skillURL, nil)
	if err != nil {
		return nil, fmt.Errorf("hub: create skill request: %w", err)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hub: fetch skill: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub: skill URL returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, hubSkillLimit))
	if err != nil {
		return nil, fmt.Errorf("hub: read skill: %w", err)
	}

	skill, err := ParseSkill(body)
	if err != nil {
		return nil, fmt.Errorf("hub: parse skill: %w", err)
	}

	return skill, nil
}
