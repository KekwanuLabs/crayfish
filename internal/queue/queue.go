package queue

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	maxRetries      = 10
	maxBackoffDelay = 60 * time.Second
)

// backoffDelays defines exponential backoff: 1s, 2s, 4s, 8s, 16s, 32s, 60s (capped)
var backoffDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	60 * time.Second,
}

// QueueItemStatus represents the status of a queue item
type QueueItemStatus string

const (
	StatusPending    QueueItemStatus = "pending"
	StatusProcessing QueueItemStatus = "processing"
	StatusFailed     QueueItemStatus = "failed"
	StatusSuccess    QueueItemStatus = "success"
)

// QueueItem represents a message in the offline queue
type QueueItem struct {
	ID        int64  `json:"id"`
	EventType string `json:"event_type"`
	Channel   string `json:"channel"`
	SessionID string `json:"session_id"`
	Payload   []byte `json:"payload"`
	Priority  int    `json:"priority"`
}

// queueRecord represents an internal database record
type queueRecord struct {
	ID           int64
	EventType    string
	Channel      string
	SessionID    string
	Payload      []byte
	Priority     int
	Status       QueueItemStatus
	Retries      int
	MaxRetries   int
	NextRetryAt  time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ErrorMessage sql.NullString
}

// OfflineQueue manages a persistent, offline message queue with exponential backoff retry
type OfflineQueue struct {
	db             *sql.DB
	logger         *slog.Logger
	mu             sync.RWMutex
	stopChan       chan struct{}
	wg             sync.WaitGroup
	workerInterval time.Duration
	maxConcurrency int
	processFn      ProcessFunc
}

// ProcessFunc is the callback function that processes queue items
type ProcessFunc func(ctx context.Context, item QueueItem) error

// New creates a new offline queue instance
func New(db *sql.DB, logger *slog.Logger) *OfflineQueue {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(nil, nil))
	}

	return &OfflineQueue{
		db:             db,
		logger:         logger,
		stopChan:       make(chan struct{}),
		workerInterval: 5 * time.Second,
		maxConcurrency: 5,
		processFn:      nil,
	}
}

// RegisterProcessor sets the processor function for queue items
func (q *OfflineQueue) RegisterProcessor(fn ProcessFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.processFn = fn
}

// Enqueue adds a message to the offline queue
func (q *OfflineQueue) Enqueue(ctx context.Context, item QueueItem) error {
	if item.EventType == "" {
		return fmt.Errorf("event_type is required")
	}

	payloadJSON := string(item.Payload)
	if payloadJSON == "" {
		payloadJSON = "{}"
	}

	query := `
	INSERT INTO offline_queue
		(event_type, channel, session_id, payload, priority, status, retries, max_retries, next_retry_at)
	VALUES
		(?, ?, ?, ?, ?, ?, 0, ?, datetime('now'))
	`

	result, err := q.db.ExecContext(ctx, query,
		item.EventType,
		item.Channel,
		item.SessionID,
		payloadJSON,
		item.Priority,
		StatusPending,
		maxRetries,
	)

	if err != nil {
		q.logger.Error("failed to enqueue item", "error", err, "event_type", item.EventType)
		return fmt.Errorf("enqueue: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		q.logger.Error("failed to get inserted item id", "error", err)
		return fmt.Errorf("last insert id: %w", err)
	}

	q.logger.Debug("item enqueued", "id", id, "event_type", item.EventType, "priority", item.Priority)
	return nil
}

// Start begins the background retry worker
func (q *OfflineQueue) Start(ctx context.Context) error {
	q.mu.Lock()
	processFn := q.processFn
	q.mu.Unlock()

	if processFn == nil {
		return fmt.Errorf("processor function not registered; call RegisterProcessor first")
	}

	q.wg.Add(1)
	go q.workerLoop(processFn)

	q.logger.Info("offline queue worker started")
	return nil
}

// Stop gracefully shuts down the worker
func (q *OfflineQueue) Stop() error {
	close(q.stopChan)
	q.wg.Wait()
	q.logger.Info("offline queue worker stopped")
	return nil
}

// workerLoop processes queue items in the background
func (q *OfflineQueue) workerLoop(processFn ProcessFunc) {
	defer q.wg.Done()

	ticker := time.NewTicker(q.workerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-q.stopChan:
			q.logger.Debug("worker loop received stop signal")
			return
		case <-ticker.C:
			q.processReadyItems(context.Background(), processFn)
		}
	}
}

// processReadyItems processes items that are ready for retry
func (q *OfflineQueue) processReadyItems(ctx context.Context, processFn ProcessFunc) {
	items, err := q.getReadyItems(ctx)
	if err != nil {
		q.logger.Error("failed to fetch ready items", "error", err)
		return
	}

	if len(items) == 0 {
		return
	}

	q.logger.Debug("processing ready items", "count", len(items))

	// Process items with concurrency control
	semaphore := make(chan struct{}, q.maxConcurrency)
	var wg sync.WaitGroup

	for _, record := range items {
		wg.Add(1)
		semaphore <- struct{}{} // Acquire semaphore

		go func(rec queueRecord) {
			defer wg.Done()
			defer func() { <-semaphore }() // Release semaphore

			q.processItem(ctx, rec, processFn)
		}(record)
	}

	wg.Wait()
}

// processItem processes a single queue item
func (q *OfflineQueue) processItem(ctx context.Context, record queueRecord, processFn ProcessFunc) {
	// Mark as processing
	if err := q.updateStatus(ctx, record.ID, StatusProcessing); err != nil {
		q.logger.Error("failed to mark item as processing", "error", err, "id", record.ID)
		return
	}

	// Create QueueItem from record
	item := QueueItem{
		ID:        record.ID,
		EventType: record.EventType,
		Channel:   record.Channel,
		SessionID: record.SessionID,
		Payload:   record.Payload,
		Priority:  record.Priority,
	}

	// Call the processor function
	err := processFn(ctx, item)

	if err == nil {
		// Success: mark as success
		if err := q.updateStatus(ctx, record.ID, StatusSuccess); err != nil {
			q.logger.Error("failed to mark item as success", "error", err, "id", record.ID)
		}
		q.logger.Debug("item processed successfully", "id", record.ID, "event_type", record.EventType)
		return
	}

	// Failure: handle retry logic
	errMsg := err.Error()
	record.Retries++
	if record.Retries >= record.MaxRetries {
		// Max retries reached: mark as failed (dead letter)
		if markErr := q.markFailed(ctx, record.ID, errMsg); markErr != nil {
			q.logger.Error("failed to mark item as failed", "error", markErr, "id", record.ID)
		}
		q.logger.Error("item max retries exceeded, moving to dead letter",
			"id", record.ID,
			"event_type", record.EventType,
			"retries", record.Retries,
			"error", errMsg)
		return
	}

	// Calculate next retry time with exponential backoff
	nextRetryTime := q.calculateNextRetryTime(record.Retries)
	if schedErr := q.scheduleRetry(ctx, record.ID, record.Retries, nextRetryTime, errMsg); schedErr != nil {
		q.logger.Error("failed to schedule retry", "error", schedErr, "id", record.ID)
	}

	q.logger.Debug("item scheduled for retry",
		"id", record.ID,
		"event_type", record.EventType,
		"retry_count", record.Retries,
		"next_retry_in", nextRetryTime.Sub(time.Now()).Seconds(),
		"error", errMsg)
}

// calculateNextRetryTime calculates the next retry time based on retry count
func (q *OfflineQueue) calculateNextRetryTime(retryCount int) time.Time {
	var delay time.Duration

	if retryCount-1 < len(backoffDelays) {
		delay = backoffDelays[retryCount-1]
	} else {
		delay = maxBackoffDelay
	}

	return time.Now().Add(delay)
}

// getReadyItems fetches items that are ready for processing
func (q *OfflineQueue) getReadyItems(ctx context.Context) ([]queueRecord, error) {
	query := `
	SELECT
		id, event_type, channel, session_id, payload, priority,
		status, retries, max_retries, next_retry_at, created_at, updated_at, error_message
	FROM offline_queue
	WHERE status IN ('pending', 'processing')
	AND next_retry_at <= datetime('now')
	ORDER BY priority DESC, created_at ASC
	LIMIT 100
	`

	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query ready items: %w", err)
	}
	defer rows.Close()

	var items []queueRecord
	for rows.Next() {
		var rec queueRecord
		var nextRetryAtStr, createdAtStr, updatedAtStr string
		var payloadStr string

		err := rows.Scan(
			&rec.ID,
			&rec.EventType,
			&rec.Channel,
			&rec.SessionID,
			&payloadStr,
			&rec.Priority,
			&rec.Status,
			&rec.Retries,
			&rec.MaxRetries,
			&nextRetryAtStr,
			&createdAtStr,
			&updatedAtStr,
			&rec.ErrorMessage,
		)

		if err != nil {
			q.logger.Error("failed to scan row", "error", err)
			continue
		}

		// Parse timestamps
		nextRetryAt, _ := time.Parse("2006-01-02 15:04:05", nextRetryAtStr)
		createdAt, _ := time.Parse("2006-01-02 15:04:05", createdAtStr)
		updatedAt, _ := time.Parse("2006-01-02 15:04:05", updatedAtStr)

		rec.NextRetryAt = nextRetryAt
		rec.CreatedAt = createdAt
		rec.UpdatedAt = updatedAt
		rec.Payload = []byte(payloadStr)

		items = append(items, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return items, nil
}

// updateStatus updates the status of a queue item
func (q *OfflineQueue) updateStatus(ctx context.Context, id int64, status QueueItemStatus) error {
	query := `
	UPDATE offline_queue
	SET status = ?, updated_at = datetime('now')
	WHERE id = ?
	`

	_, err := q.db.ExecContext(ctx, query, status, id)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	return nil
}

// scheduleRetry reschedules an item for retry
func (q *OfflineQueue) scheduleRetry(ctx context.Context, id int64, retries int, nextRetryAt time.Time, errMsg string) error {
	query := `
	UPDATE offline_queue
	SET status = ?, retries = ?, next_retry_at = ?, error_message = ?, updated_at = datetime('now')
	WHERE id = ?
	`

	nextRetryAtStr := nextRetryAt.Format("2006-01-02 15:04:05")
	_, err := q.db.ExecContext(ctx, query, StatusPending, retries, nextRetryAtStr, errMsg, id)
	if err != nil {
		return fmt.Errorf("schedule retry: %w", err)
	}

	return nil
}

// markFailed marks an item as permanently failed (dead letter)
func (q *OfflineQueue) markFailed(ctx context.Context, id int64, errMsg string) error {
	query := `
	UPDATE offline_queue
	SET status = ?, error_message = ?, updated_at = datetime('now')
	WHERE id = ?
	`

	_, err := q.db.ExecContext(ctx, query, StatusFailed, errMsg, id)
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}

	return nil
}
