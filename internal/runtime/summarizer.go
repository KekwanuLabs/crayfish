package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/provider"
)

const (
	// SummarizationThreshold is the number of messages at which summarization is triggered
	SummarizationThreshold = 40

	// KeepRecentDefault is the default number of recent messages to keep unsummarized
	KeepRecentDefault = 15

	// SummarizationPrompt is the template for summarizing conversations
	SummarizationPrompt = `Summarize this conversation in 2-3 sentences, preserving key facts, decisions, and user preferences. Be concise — this summary replaces the full history to save bandwidth.

Conversation:
%s`
)

// Summarizer handles conversation summarization for bandwidth savings.
type Summarizer struct {
	db       *sql.DB
	provider provider.Provider
	logger   *slog.Logger

	// summaryCache stores recently computed summaries to avoid re-summarization
	// key: session_id, value: *CachedSummary
	summaryCache map[string]*CachedSummary

	// snapshotMgr captures session state before summarization compresses messages.
	snapshotMgr *SnapshotManager
}

// CachedSummary represents a cached summary with its metadata
type CachedSummary struct {
	Summary       string
	MessageCount  int
	LastMessageID int
	CreatedAt     time.Time
}

// NewSummarizer creates a new Summarizer instance.
func NewSummarizer(db *sql.DB, prov provider.Provider, logger *slog.Logger) *Summarizer {
	return &Summarizer{
		db:           db,
		provider:     prov,
		logger:       logger,
		summaryCache: make(map[string]*CachedSummary),
	}
}

// SetSnapshotManager configures the summarizer to capture session state before compaction.
func (s *Summarizer) SetSnapshotManager(mgr *SnapshotManager) {
	s.snapshotMgr = mgr
}

// SummarizeIfNeeded checks if the conversation needs summarization and performs it.
// It returns the optimized message list for context assembly.
// If the message count exceeds SummarizationThreshold, messages older than the
// most recent keepRecent messages are summarized and replaced with a single
// system-injected summary message.
func (s *Summarizer) SummarizeIfNeeded(
	ctx context.Context,
	sessionID string,
	messages []provider.Message,
	keepRecent int,
) ([]provider.Message, error) {
	// If we haven't reached the threshold, return messages as-is
	if len(messages) <= SummarizationThreshold {
		return messages, nil
	}

	// Ensure keepRecent is valid
	if keepRecent <= 0 {
		keepRecent = KeepRecentDefault
	}
	if keepRecent >= len(messages) {
		return messages, nil
	}

	// Check if we have a cached summary for this session
	cached, exists := s.summaryCache[sessionID]
	if exists {
		s.logger.Debug("using cached summary", "session_id", sessionID, "message_count", cached.MessageCount)
		return s.buildOptimizedMessages(messages, cached.Summary, keepRecent)
	}

	// Capture a session snapshot before summarization compresses the messages.
	// This runs in a background goroutine so summarization is not blocked.
	if s.snapshotMgr != nil {
		msgCopy := make([]provider.Message, len(messages))
		copy(msgCopy, messages)
		go func() {
			snapCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := s.snapshotMgr.GenerateAndSave(snapCtx, sessionID, "auto", msgCopy); err != nil {
				s.logger.Warn("pre-summarization snapshot failed", "error", err, "session_id", sessionID)
			}
		}()
	}

	// Determine how many messages to summarize
	numToSummarize := len(messages) - keepRecent

	// Build the conversation text to summarize (excluding the recent keepRecent messages)
	conversationText := s.buildConversationText(messages[:numToSummarize])

	// Call the LLM to generate the summary
	summary, err := s.generateSummary(ctx, conversationText)
	if err != nil {
		s.logger.Error("failed to generate summary", "error", err, "session_id", sessionID)
		// On error, return original messages rather than failing the entire request
		return messages, nil
	}

	// Store the summary in the database
	err = s.storeSummary(ctx, sessionID, summary, numToSummarize)
	if err != nil {
		s.logger.Error("failed to store summary", "error", err, "session_id", sessionID)
		// On error, cache and return anyway to avoid blocking
	}

	// Cache the summary
	s.summaryCache[sessionID] = &CachedSummary{
		Summary:       summary,
		MessageCount:  numToSummarize,
		LastMessageID: numToSummarize - 1,
		CreatedAt:     time.Now(),
	}

	s.logger.Info(
		"summarized conversation",
		"session_id", sessionID,
		"messages_summarized", numToSummarize,
		"summary_length", len(summary),
	)

	return s.buildOptimizedMessages(messages, summary, keepRecent)
}

// buildConversationText constructs a readable conversation string from messages.
func (s *Summarizer) buildConversationText(messages []provider.Message) string {
	var text string
	for i, msg := range messages {
		if i > 0 {
			text += "\n"
		}
		text += fmt.Sprintf("[%s]: %s", msg.Role, msg.Content)
	}
	return text
}

// generateSummary calls the LLM to produce a summary of the conversation.
func (s *Summarizer) generateSummary(ctx context.Context, conversationText string) (string, error) {
	req := provider.CompletionRequest{
		Messages: []provider.Message{
			{
				Role:    provider.RoleSystem,
				Content: "You are a concise summarizer. Create brief, factual summaries.",
			},
			{
				Role:    provider.RoleUser,
				Content: fmt.Sprintf(SummarizationPrompt, conversationText),
			},
		},
	}

	response, err := s.provider.Complete(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to call provider.Complete: %w", err)
	}

	if response.Content == "" {
		return "", fmt.Errorf("empty response from provider")
	}

	return response.Content, nil
}

// storeSummary saves the summary to the database for persistence.
func (s *Summarizer) storeSummary(
	ctx context.Context,
	sessionID string,
	summary string,
	messageCount int,
) error {
	query := `
	INSERT INTO conversation_summaries (session_id, summary, message_count, last_message_id, created_at)
	VALUES (?, ?, ?, ?, datetime('now'))
	`

	_, err := s.db.ExecContext(ctx, query, sessionID, summary, messageCount, messageCount-1)
	if err != nil {
		return fmt.Errorf("failed to insert summary: %w", err)
	}

	return nil
}

// buildOptimizedMessages constructs the final message list with the summary injected.
func (s *Summarizer) buildOptimizedMessages(
	messages []provider.Message,
	summary string,
	keepRecent int,
) ([]provider.Message, error) {
	// Create the summary message to inject
	summaryMessage := provider.Message{
		Role: provider.RoleSystem,
		Content: fmt.Sprintf(
			"[Previous conversation summary]\n%s",
			summary,
		),
	}

	// Build the optimized list: summary + recent messages
	optimized := make([]provider.Message, 0, keepRecent+1)
	optimized = append(optimized, summaryMessage)
	optimized = append(optimized, messages[len(messages)-keepRecent:]...)

	return optimized, nil
}

// ClearCache clears the in-memory summary cache. Useful for testing or memory management.
func (s *Summarizer) ClearCache() {
	s.summaryCache = make(map[string]*CachedSummary)
}

// LoadSummaryFromDB retrieves a cached summary from the database for a session.
// This can be used during initialization to repopulate the cache.
func (s *Summarizer) LoadSummaryFromDB(ctx context.Context, sessionID string) error {
	query := `
	SELECT summary, message_count, last_message_id, created_at
	FROM conversation_summaries
	WHERE session_id = ?
	ORDER BY created_at DESC
	LIMIT 1
	`

	var summary string
	var messageCount int
	var lastMessageID int
	var createdAtStr string

	err := s.db.QueryRowContext(ctx, query, sessionID).Scan(&summary, &messageCount, &lastMessageID, &createdAtStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil // No summary found, not an error
		}
		return fmt.Errorf("failed to query summary: %w", err)
	}

	createdAt, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		createdAt = time.Now()
	}

	s.summaryCache[sessionID] = &CachedSummary{
		Summary:       summary,
		MessageCount:  messageCount,
		LastMessageID: lastMessageID,
		CreatedAt:     createdAt,
	}

	return nil
}
