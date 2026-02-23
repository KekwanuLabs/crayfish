package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/provider"
)

const (
	// extractionDebounceInterval prevents rapid successive extractions
	extractionDebounceInterval = 5 * time.Second

	// minMessageLength is the minimum length for a message to trigger extraction
	minMessageLength = 20

	// extractionPromptTemplate is the template for memory extraction
	extractionPromptTemplate = `You are a memory extraction assistant. Analyze this conversation turn and extract ONLY significant facts worth remembering long-term.

Categories:
- preference: User's likes/dislikes, habits, choices
- personal: Personal info, background, interests, context about the user
- decision: Decisions made, conclusions reached, plans
- context: Important context for understanding future conversations
- session_state: Active task, what we're working on right now
- pending: Unresolved proposals, questions, or requests not yet addressed
- general: Other memorable facts

Extract 0-3 facts. Only extract if information is:
1. Likely useful in future conversations
2. Specific and actionable
3. Not trivial small talk

Conversation:
User: %s
Assistant: %s

Return JSON: {"facts": [{"category": "...", "importance": 1-10, "key": "short title", "content": "full content"}]}
If nothing significant, return: {"facts": []}`
)

// MemoryExtractor extracts important facts from conversations and stores them in memory.
type MemoryExtractor struct {
	db       *sql.DB
	provider provider.Provider
	logger   *slog.Logger

	// Debouncing state
	mu             sync.Mutex
	lastExtraction map[string]time.Time
}

// ExtractedFact represents a single extracted memory fact.
type ExtractedFact struct {
	Category   string `json:"category"`
	Importance int    `json:"importance"`
	Key        string `json:"key"`
	Content    string `json:"content"`
}

// ExtractionResponse is the JSON response from the LLM.
type ExtractionResponse struct {
	Facts []ExtractedFact `json:"facts"`
}

// NewMemoryExtractor creates a new memory extractor instance.
func NewMemoryExtractor(db *sql.DB, prov provider.Provider, logger *slog.Logger) *MemoryExtractor {
	return &MemoryExtractor{
		db:             db,
		provider:       prov,
		logger:         logger,
		lastExtraction: make(map[string]time.Time),
	}
}

// ExtractFromTurn extracts memorable facts from a conversation turn.
// It uses the LLM to identify significant information and stores it in memory_fts + memory_metadata.
func (m *MemoryExtractor) ExtractFromTurn(ctx context.Context, sessionID, userMsg, assistantMsg string) error {
	// Check debounce
	if !m.shouldExtract(sessionID) {
		m.logger.Debug("skipping extraction due to debounce", "session_id", sessionID)
		return nil
	}

	// Skip if messages are too short
	if len(userMsg) < minMessageLength && len(assistantMsg) < minMessageLength {
		m.logger.Debug("skipping extraction, messages too short", "session_id", sessionID)
		return nil
	}

	// Skip trivial greetings and confirmations
	lowerUser := strings.ToLower(strings.TrimSpace(userMsg))
	if isTrivialMessage(lowerUser) {
		m.logger.Debug("skipping extraction, trivial message", "session_id", sessionID)
		return nil
	}

	// Call LLM to extract facts
	facts, err := m.extractFacts(ctx, userMsg, assistantMsg)
	if err != nil {
		return fmt.Errorf("extract facts: %w", err)
	}

	if len(facts) == 0 {
		m.logger.Debug("no facts extracted", "session_id", sessionID)
		return nil
	}

	// Store extracted facts
	for _, fact := range facts {
		if err := m.storeFact(ctx, sessionID, fact); err != nil {
			m.logger.Warn("failed to store fact", "error", err, "key", fact.Key)
			continue
		}
		m.logger.Info("memory fact stored",
			"session_id", sessionID,
			"key", fact.Key,
			"category", fact.Category,
			"importance", fact.Importance)
	}

	// Update debounce timestamp
	m.mu.Lock()
	m.lastExtraction[sessionID] = time.Now()
	m.mu.Unlock()

	return nil
}

// shouldExtract checks if enough time has passed since the last extraction for this session.
func (m *MemoryExtractor) shouldExtract(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	lastTime, exists := m.lastExtraction[sessionID]
	if !exists {
		return true
	}

	return time.Since(lastTime) >= extractionDebounceInterval
}

// extractFacts calls the LLM to extract facts from the conversation turn.
func (m *MemoryExtractor) extractFacts(ctx context.Context, userMsg, assistantMsg string) ([]ExtractedFact, error) {
	prompt := fmt.Sprintf(extractionPromptTemplate, userMsg, assistantMsg)

	req := provider.CompletionRequest{
		Messages: []provider.Message{
			{
				Role:    provider.RoleSystem,
				Content: "You are a precise memory extraction assistant. Return only valid JSON.",
			},
			{
				Role:    provider.RoleUser,
				Content: prompt,
			},
		},
		MaxTokens: 500,
	}

	resp, err := m.provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	if resp.Content == "" {
		return nil, fmt.Errorf("empty response from LLM")
	}

	// Parse JSON response
	var extractionResp ExtractionResponse
	if err := json.Unmarshal([]byte(resp.Content), &extractionResp); err != nil {
		m.logger.Warn("failed to parse extraction response as JSON",
			"error", err, "response", resp.Content)
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	// Validate and filter facts
	var validFacts []ExtractedFact
	for _, fact := range extractionResp.Facts {
		if fact.Key == "" || fact.Content == "" {
			continue
		}
		// Clamp importance to 1-10 range
		if fact.Importance < 1 {
			fact.Importance = 1
		}
		if fact.Importance > 10 {
			fact.Importance = 10
		}
		// Validate category
		if !isValidCategory(fact.Category) {
			fact.Category = "general"
		}
		validFacts = append(validFacts, fact)
	}

	return validFacts, nil
}

// storeFact stores a single extracted fact in memory_fts and memory_metadata.
func (m *MemoryExtractor) storeFact(ctx context.Context, sessionID string, fact ExtractedFact) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Insert into memory_fts
	result, err := tx.ExecContext(ctx,
		"INSERT INTO memory_fts (key, content, session_id, created_at) VALUES (?, ?, ?, datetime('now'))",
		fact.Key, fact.Content, sessionID)
	if err != nil {
		return fmt.Errorf("insert into memory_fts: %w", err)
	}

	// Get the rowid of the inserted FTS entry
	rowID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}

	// Insert into memory_metadata with the same rowid
	sourceContext := fmt.Sprintf("Extracted from conversation on %s", time.Now().UTC().Format("2006-01-02"))
	_, err = tx.ExecContext(ctx,
		`INSERT INTO memory_metadata (id, session_id, category, importance, source_context, created_at, access_count)
		VALUES (?, ?, ?, ?, ?, datetime('now'), 0)`,
		rowID, sessionID, fact.Category, fact.Importance, sourceContext)
	if err != nil {
		return fmt.Errorf("insert into memory_metadata: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// isTrivialMessage checks if a message is too trivial to extract memories from.
func isTrivialMessage(msg string) bool {
	trivialPatterns := []string{
		"hi", "hello", "hey", "thanks", "thank you", "ok", "okay",
		"yes", "no", "yeah", "nope", "sure", "got it", "cool",
	}

	for _, pattern := range trivialPatterns {
		if msg == pattern || msg == pattern+"." || msg == pattern+"!" {
			return true
		}
	}

	return false
}

// isValidCategory checks if a category is one of the allowed values.
func isValidCategory(category string) bool {
	validCategories := []string{"preference", "personal", "decision", "context", "session_state", "pending", "general"}
	for _, valid := range validCategories {
		if category == valid {
			return true
		}
	}
	return false
}
