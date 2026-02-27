// Package storage provides SQLite-based persistence for all Crayfish state.
// It uses modernc.org/sqlite (pure Go, no CGo) for cross-compilation to ARM.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a sql.DB with Crayfish-specific operations and lifecycle management.
// It maintains separate writer (single-connection) and reader (multi-connection)
// pools to eliminate SQLITE_BUSY errors while keeping reads concurrent.
type DB struct {
	writer *sql.DB
	reader *sql.DB
	path   string
	mu     sync.RWMutex
	logger *slog.Logger
}

// Open creates or opens a SQLite database at the given path with WAL mode enabled.
// It applies all pending migrations automatically.
func Open(ctx context.Context, dbPath string, logger *slog.Logger) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("storage.Open: create dir: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=15000&_synchronous=NORMAL&_foreign_keys=ON", dbPath)

	// Writer pool: single connection serializes all writes via Go's pool queue,
	// eliminating SQLITE_BUSY errors for concurrent write attempts.
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage.Open: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)

	if err := writer.PingContext(ctx); err != nil {
		writer.Close()
		return nil, fmt.Errorf("storage.Open: ping writer: %w", err)
	}

	// Reader pool: multiple connections for concurrent reads via WAL mode.
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("storage.Open: open reader: %w", err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)
	reader.SetConnMaxLifetime(0)

	if err := reader.PingContext(ctx); err != nil {
		writer.Close()
		reader.Close()
		return nil, fmt.Errorf("storage.Open: ping reader: %w", err)
	}

	db := &DB{
		writer: writer,
		reader: reader,
		path:   dbPath,
		logger: logger,
	}

	if err := db.migrate(ctx); err != nil {
		writer.Close()
		reader.Close()
		return nil, fmt.Errorf("storage.Open: migrate: %w", err)
	}

	logger.Info("storage opened", "path", dbPath)
	return db, nil
}

// Close shuts down both database connection pools.
func (db *DB) Close() error {
	wErr := db.writer.Close()
	rErr := db.reader.Close()
	if wErr != nil {
		return wErr
	}
	return rErr
}

// Inner returns the writer pool for backward compatibility.
// All existing callers serialize writes automatically through the single-connection pool.
func (db *DB) Inner() *sql.DB {
	return db.writer
}

// Writer returns the single-connection write pool.
func (db *DB) Writer() *sql.DB {
	return db.writer
}

// Reader returns the multi-connection read pool for concurrent read-only queries.
func (db *DB) Reader() *sql.DB {
	return db.reader
}

// migrate runs all schema migrations in order. Uses a simple version table.
func (db *DB) migrate(ctx context.Context) error {
	// Create the migrations tracking table first.
	if _, err := db.writer.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS _migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`); err != nil {
		return fmt.Errorf("create _migrations table: %w", err)
	}

	var currentVersion int
	row := db.writer.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM _migrations")
	if err := row.Scan(&currentVersion); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}

	for i, m := range migrations {
		ver := i + 1
		if ver <= currentVersion {
			continue
		}
		db.logger.Info("applying migration", "version", ver, "name", m.name)
		tx, err := db.writer.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", ver, err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", ver, m.name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO _migrations (version) VALUES (?)", ver); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", ver, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", ver, err)
		}
	}

	return nil
}

// Compact performs database maintenance: VACUUM if above threshold, ANALYZE for query planning.
func (db *DB) Compact(ctx context.Context, maxSizeMB int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	info, err := os.Stat(db.path)
	if err != nil {
		return fmt.Errorf("storage.Compact: stat: %w", err)
	}

	sizeMB := info.Size() / (1024 * 1024)
	db.logger.Info("storage compact check", "size_mb", sizeMB, "max_mb", maxSizeMB)

	if sizeMB > maxSizeMB {
		db.logger.Warn("database exceeds size limit, running VACUUM", "size_mb", sizeMB)
		if _, err := db.writer.ExecContext(ctx, "VACUUM"); err != nil {
			return fmt.Errorf("vacuum: %w", err)
		}
	}

	if _, err := db.writer.ExecContext(ctx, "ANALYZE"); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	return nil
}

type migration struct {
	name string
	sql  string
}

// migrations is the ordered list of schema changes. Append-only — never modify existing entries.
var migrations = []migration{
	{
		name: "initial schema",
		sql: `
		-- Event log: the backbone of CrayfishBus.
		CREATE TABLE events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			type       TEXT    NOT NULL,
			channel    TEXT    NOT NULL DEFAULT '',
			session_id TEXT    NOT NULL DEFAULT '',
			payload    TEXT    NOT NULL DEFAULT '{}',
			created_at TEXT    NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX idx_events_type ON events(type);
		CREATE INDEX idx_events_session ON events(session_id);
		CREATE INDEX idx_events_created ON events(created_at);

		-- Sessions: trust state and user tracking.
		CREATE TABLE sessions (
			id            TEXT PRIMARY KEY,
			channel       TEXT NOT NULL,
			user_id       TEXT NOT NULL,
			trust_tier    INTEGER NOT NULL DEFAULT 0,
			paired        INTEGER NOT NULL DEFAULT 0,
			allowed_tools TEXT NOT NULL DEFAULT '[]',
			created_at    TEXT NOT NULL DEFAULT (datetime('now')),
			last_active   TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE UNIQUE INDEX idx_sessions_channel_user ON sessions(channel, user_id);

		-- Conversation messages for context assembly.
		CREATE TABLE messages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role       TEXT NOT NULL,
			content    TEXT NOT NULL,
			tokens     INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE INDEX idx_messages_session ON messages(session_id);

		-- Memory store with FTS5 for keyword search.
		CREATE VIRTUAL TABLE memory_fts USING fts5(
			key,
			content,
			session_id,
			created_at
		);

		-- Config key-value store.
		CREATE TABLE config (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);

		-- Message cache for response dedup.
		CREATE TABLE message_cache (
			hash       TEXT PRIMARY KEY,
			response   TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			expires_at TEXT NOT NULL
		);
		`,
	},
	{
		name: "pairing, queue, summary, and todos tables",
		sql: `
		-- OTPs for trust-tier pairing flow.
		CREATE TABLE IF NOT EXISTS pairing_otps (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			otp                 TEXT NOT NULL,
			operator_session_id TEXT NOT NULL,
			created_at          TEXT NOT NULL DEFAULT (datetime('now')),
			expires_at          TEXT NOT NULL,
			redeemed_by         TEXT,
			redeemed_at         TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_pairing_otps_otp ON pairing_otps(otp);

		-- Pairing attempt log for rate limiting.
		CREATE TABLE IF NOT EXISTS pairing_attempts (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id   TEXT    NOT NULL,
			attempted_at TEXT    NOT NULL DEFAULT (datetime('now')),
			success      INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_pairing_attempts_session ON pairing_attempts(session_id);

		-- Offline message queue with retry.
		CREATE TABLE IF NOT EXISTS offline_queue (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type    TEXT    NOT NULL,
			channel       TEXT    NOT NULL DEFAULT '',
			session_id    TEXT    NOT NULL DEFAULT '',
			payload       TEXT    NOT NULL DEFAULT '{}',
			priority      INTEGER NOT NULL DEFAULT 0,
			status        TEXT    NOT NULL DEFAULT 'pending',
			retries       INTEGER NOT NULL DEFAULT 0,
			max_retries   INTEGER NOT NULL DEFAULT 10,
			next_retry_at TEXT    NOT NULL DEFAULT (datetime('now')),
			created_at    TEXT    NOT NULL DEFAULT (datetime('now')),
			updated_at    TEXT    NOT NULL DEFAULT (datetime('now')),
			error_message TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_offline_queue_status ON offline_queue(status);
		CREATE INDEX IF NOT EXISTS idx_offline_queue_next_retry ON offline_queue(next_retry_at);

		-- Conversation summaries for bandwidth savings.
		CREATE TABLE IF NOT EXISTS conversation_summaries (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id      TEXT    NOT NULL,
			summary         TEXT    NOT NULL,
			message_count   INTEGER NOT NULL,
			last_message_id INTEGER NOT NULL,
			created_at      TEXT    NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_conv_summaries_session ON conversation_summaries(session_id);

		-- Todos table for built-in tools.
		CREATE TABLE IF NOT EXISTS todos (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			text       TEXT    NOT NULL,
			completed  INTEGER NOT NULL DEFAULT 0,
			created_at TEXT    NOT NULL DEFAULT (datetime('now'))
		);
		`,
	},
	{
		name: "gmail email integration",
		sql: `
		-- Email metadata (no binary attachment storage).
		CREATE TABLE IF NOT EXISTS emails (
			id            TEXT PRIMARY KEY,
			message_id    TEXT NOT NULL,
			thread_id     TEXT NOT NULL DEFAULT '',
			from_addr     TEXT NOT NULL,
			to_addrs      TEXT NOT NULL DEFAULT '',
			cc_addrs      TEXT NOT NULL DEFAULT '',
			subject       TEXT NOT NULL DEFAULT '',
			body_preview  TEXT NOT NULL DEFAULT '',
			body_full     TEXT,
			is_read       INTEGER NOT NULL DEFAULT 0,
			is_starred    INTEGER NOT NULL DEFAULT 0,
			labels        TEXT NOT NULL DEFAULT '["INBOX"]',
			has_attachments INTEGER NOT NULL DEFAULT 0,
			received_at   TEXT NOT NULL,
			stored_at     TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_emails_received ON emails(received_at DESC);
		CREATE INDEX IF NOT EXISTS idx_emails_read ON emails(is_read);
		CREATE INDEX IF NOT EXISTS idx_emails_message_id ON emails(message_id);

		-- Full-text search on emails.
		CREATE VIRTUAL TABLE IF NOT EXISTS emails_fts USING fts5(
			subject,
			from_addr,
			body_preview,
			content='emails',
			content_rowid='rowid'
		);

		-- Triggers to keep FTS in sync.
		CREATE TRIGGER IF NOT EXISTS emails_ai AFTER INSERT ON emails BEGIN
			INSERT INTO emails_fts(rowid, subject, from_addr, body_preview)
			VALUES (new.rowid, new.subject, new.from_addr, new.body_preview);
		END;
		CREATE TRIGGER IF NOT EXISTS emails_ad AFTER DELETE ON emails BEGIN
			INSERT INTO emails_fts(emails_fts, rowid, subject, from_addr, body_preview)
			VALUES ('delete', old.rowid, old.subject, old.from_addr, old.body_preview);
		END;

		-- Gmail IMAP sync state.
		CREATE TABLE IF NOT EXISTS gmail_sync_state (
			id                INTEGER PRIMARY KEY DEFAULT 1,
			last_sync_at      TEXT NOT NULL DEFAULT (datetime('now')),
			last_uid_validity INTEGER NOT NULL DEFAULT 0,
			last_uid          INTEGER NOT NULL DEFAULT 0,
			sync_in_progress  INTEGER NOT NULL DEFAULT 0,
			error_message     TEXT
		);
		INSERT OR IGNORE INTO gmail_sync_state (id) VALUES (1);

		-- Attachment metadata (no binary).
		CREATE TABLE IF NOT EXISTS email_attachments (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			email_id  TEXT NOT NULL,
			filename  TEXT NOT NULL,
			mime_type TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (email_id) REFERENCES emails(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_attach_email ON email_attachments(email_id);
		`,
	},
	{
		name: "enhanced memory system",
		sql: `
		-- Memory metadata for automatic persistent memory system.
		-- Links to memory_fts entries via rowid.
		CREATE TABLE IF NOT EXISTS memory_metadata (
			id INTEGER PRIMARY KEY,
			session_id TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'general',
			importance INTEGER NOT NULL DEFAULT 5,
			source_context TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			last_accessed TEXT,
			access_count INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_memory_meta_session ON memory_metadata(session_id);
		CREATE INDEX IF NOT EXISTS idx_memory_meta_importance ON memory_metadata(importance DESC);
		CREATE INDEX IF NOT EXISTS idx_memory_meta_accessed ON memory_metadata(last_accessed DESC);
		`,
	},
	{
		name: "fabric protocol future-proofing",
		sql: `
		-- Fabric Protocol columns for cryptographic identity binding.
		-- These are NULL until Fabric is enabled (v0.5.0+).
		ALTER TABLE sessions ADD COLUMN fabric_agent_id TEXT;
		ALTER TABLE sessions ADD COLUMN fabric_delegation BLOB;

		-- Identities table for extended identity management.
		CREATE TABLE IF NOT EXISTS identities (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			display_name TEXT,
			trust_tier INTEGER NOT NULL DEFAULT 0,
			paired_at TEXT,
			fabric_agent_id TEXT,
			fabric_human_root TEXT,
			fabric_delegation BLOB,
			fabric_scopes TEXT DEFAULT '[]',
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE INDEX IF NOT EXISTS idx_identities_session ON identities(session_id);
		CREATE INDEX IF NOT EXISTS idx_identities_fabric ON identities(fabric_agent_id);
		`,
	},
	{
		name: "session continuity snapshots",
		sql: `
		-- Session snapshots for preserving conversational texture across summarization.
		CREATE TABLE IF NOT EXISTS session_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			trigger TEXT NOT NULL DEFAULT 'auto',
			active_task TEXT NOT NULL DEFAULT '',
			active_task_context TEXT NOT NULL DEFAULT '',
			last_exchanges TEXT NOT NULL DEFAULT '[]',
			pending_proposals TEXT NOT NULL DEFAULT '[]',
			decisions_in_flight TEXT NOT NULL DEFAULT '[]',
			conversational_tone TEXT NOT NULL DEFAULT '',
			key_resources TEXT NOT NULL DEFAULT '[]',
			message_count INTEGER NOT NULL DEFAULT 0,
			is_current INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE INDEX IF NOT EXISTS idx_snapshots_session ON session_snapshots(session_id);
		CREATE INDEX IF NOT EXISTS idx_snapshots_current ON session_snapshots(session_id, is_current);
		`,
	},
	{
		name: "todo list categories",
		sql: `
		ALTER TABLE todos ADD COLUMN list_name TEXT NOT NULL DEFAULT 'default';
		CREATE INDEX IF NOT EXISTS idx_todos_list ON todos(list_name);
		`,
	},
	{
		name: "urgency tracking and auto-reply threads",
		sql: `
		CREATE TABLE IF NOT EXISTS urgency_notified (
			email_id    TEXT PRIMARY KEY,
			notified_at TEXT NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS tracked_threads (
			thread_id          TEXT PRIMARY KEY,
			last_email_id      TEXT NOT NULL,
			to_addr            TEXT NOT NULL,
			subject            TEXT NOT NULL,
			reply_count        INTEGER NOT NULL DEFAULT 0,
			last_auto_reply_at TEXT,
			created_at         TEXT NOT NULL DEFAULT (datetime('now')),
			active             INTEGER NOT NULL DEFAULT 1
		);
		CREATE INDEX IF NOT EXISTS idx_tracked_active ON tracked_threads(active);
		`,
	},
	{
		name: "suggestions table for proactive agent",
		sql: `
		CREATE TABLE IF NOT EXISTS suggestions (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			type       TEXT NOT NULL,
			content    TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 0.5,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_suggestions_user ON suggestions(user_id);
		CREATE INDEX IF NOT EXISTS idx_suggestions_type ON suggestions(type);
		`,
	},
}

// Now returns the current time formatted for SQLite storage.
func Now() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}
