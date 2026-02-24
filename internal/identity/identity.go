// Package identity manages persistent identity files (SOUL.md + USER.md)
// that give the agent a structured personality and knowledge of its owner.
// Files live alongside the config at ~/.config/crayfish/.
package identity

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"
)

const (
	// maxFileSize is the maximum size for identity files on disk (4KB).
	maxFileSize = 4096

	// maxCacheChars is the maximum characters kept in the in-memory cache (~500 tokens).
	maxCacheChars = 2000

	// minUserContent is the threshold for HasUser() — USER.md must have meaningful content.
	minUserContent = 10

	soulFile = "SOUL.md"
	userFile = "USER.md"
)

// Store manages reading and writing of identity markdown files.
type Store struct {
	dir    string
	logger *slog.Logger
	mu     sync.RWMutex
	soulMD string
	userMD string
}

// NewStore creates a new identity store rooted at dir and loads any existing files.
func NewStore(dir string, logger *slog.Logger) *Store {
	s := &Store{
		dir:    dir,
		logger: logger,
	}
	s.Reload()
	return s
}

// Soul returns the cached SOUL.md content (truncated to maxCacheChars).
func (s *Store) Soul() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.soulMD
}

// User returns the cached USER.md content (truncated to maxCacheChars).
func (s *Store) User() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.userMD
}

// HasUser returns true if USER.md has meaningful content (more than minUserContent chars).
func (s *Store) HasUser() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.userMD) > minUserContent
}

// SoulPath returns the absolute path to SOUL.md.
func (s *Store) SoulPath() string {
	return filepath.Join(s.dir, soulFile)
}

// UserPath returns the absolute path to USER.md.
func (s *Store) UserPath() string {
	return filepath.Join(s.dir, userFile)
}

// WriteSoul validates and writes content to SOUL.md, then updates the cache.
func (s *Store) WriteSoul(content string) error {
	if err := validate(content); err != nil {
		return fmt.Errorf("identity.WriteSoul: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeFile(soulFile, content); err != nil {
		return err
	}
	s.soulMD = truncate(content, maxCacheChars)
	return nil
}

// WriteUser validates and writes content to USER.md, then updates the cache.
func (s *Store) WriteUser(content string) error {
	if err := validate(content); err != nil {
		return fmt.Errorf("identity.WriteUser: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeFile(userFile, content); err != nil {
		return err
	}
	s.userMD = truncate(content, maxCacheChars)
	return nil
}

// Reload re-reads both identity files from disk into the cache.
func (s *Store) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.soulMD = s.readFile(soulFile)
	s.userMD = s.readFile(userFile)
}

// readFile reads a file from the identity directory, returning empty string on any error.
func (s *Store) readFile(name string) string {
	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.logger.Warn("failed to read identity file", "path", path, "error", err)
		}
		return ""
	}
	content := string(data)
	if !utf8.ValidString(content) {
		s.logger.Warn("identity file contains invalid UTF-8, ignoring", "path", path)
		return ""
	}
	return truncate(content, maxCacheChars)
}

// writeFile writes content to a file in the identity directory.
func (s *Store) writeFile(name, content string) error {
	// Ensure directory exists.
	if err := os.MkdirAll(s.dir, 0750); err != nil {
		return fmt.Errorf("identity.writeFile: mkdir: %w", err)
	}
	path := filepath.Join(s.dir, name)
	// Truncate to max file size at a rune boundary to avoid splitting multi-byte characters.
	if len(content) > maxFileSize {
		content = truncateBytes(content, maxFileSize)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("identity.writeFile: %w", err)
	}
	s.logger.Info("identity file updated", "path", path, "size", len(content))
	return nil
}

// validate checks that content is non-empty, valid UTF-8, and within size limits.
func validate(content string) error {
	if content == "" {
		return fmt.Errorf("content is empty")
	}
	if !utf8.ValidString(content) {
		return fmt.Errorf("content is not valid UTF-8")
	}
	if len(content) > maxFileSize {
		// Not an error — we'll truncate on write. But warn if it's way over.
		// The write methods handle truncation.
	}
	return nil
}

// truncate cuts a string to maxChars runes at a rune boundary.
func truncate(s string, maxChars int) string {
	if utf8.RuneCountInString(s) <= maxChars {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxChars])
}

// truncateBytes cuts a string to at most maxBytes without splitting a multi-byte UTF-8 rune.
func truncateBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk backward from the limit to find a valid rune boundary.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
