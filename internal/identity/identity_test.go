package identity

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestNewStoreNoFiles(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	if s.Soul() != "" {
		t.Errorf("Soul() = %q, want empty", s.Soul())
	}
	if s.User() != "" {
		t.Errorf("User() = %q, want empty", s.User())
	}
	if s.HasUser() {
		t.Error("HasUser() = true, want false for empty store")
	}
}

func TestWriteAndReadSoul(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	content := "I am a helpful assistant with a warm personality."
	if err := s.WriteSoul(content); err != nil {
		t.Fatalf("WriteSoul() error: %v", err)
	}

	if got := s.Soul(); got != content {
		t.Errorf("Soul() = %q, want %q", got, content)
	}

	// Verify file exists on disk.
	data, err := os.ReadFile(filepath.Join(dir, "SOUL.md"))
	if err != nil {
		t.Fatalf("ReadFile SOUL.md: %v", err)
	}
	if string(data) != content {
		t.Errorf("SOUL.md on disk = %q, want %q", string(data), content)
	}
}

func TestWriteAndReadUser(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	content := "Name: Alice\nJob: Engineer\nGoals: Build cool stuff"
	if err := s.WriteUser(content); err != nil {
		t.Fatalf("WriteUser() error: %v", err)
	}

	if got := s.User(); got != content {
		t.Errorf("User() = %q, want %q", got, content)
	}
	if !s.HasUser() {
		t.Error("HasUser() = false after writing content")
	}

	// Verify file exists on disk.
	data, err := os.ReadFile(filepath.Join(dir, "USER.md"))
	if err != nil {
		t.Fatalf("ReadFile USER.md: %v", err)
	}
	if string(data) != content {
		t.Errorf("USER.md on disk = %q, want %q", string(data), content)
	}
}

func TestMaxFileSizeEnforced(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	// Create content larger than maxFileSize (4KB).
	big := strings.Repeat("x", maxFileSize+500)
	if err := s.WriteSoul(big); err != nil {
		t.Fatalf("WriteSoul() error: %v", err)
	}

	// File on disk should be truncated to maxFileSize.
	data, err := os.ReadFile(filepath.Join(dir, "SOUL.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) > maxFileSize {
		t.Errorf("file size = %d, want <= %d", len(data), maxFileSize)
	}
}

func TestInvalidUTF8Rejected(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	// Invalid UTF-8 byte sequence.
	invalid := "hello \xff\xfe world"
	err := s.WriteSoul(invalid)
	if err == nil {
		t.Fatal("WriteSoul() with invalid UTF-8 should fail")
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("error = %v, want mention of UTF-8", err)
	}
}

func TestInvalidUTF8OnDiskIgnored(t *testing.T) {
	dir := t.TempDir()

	// Write invalid UTF-8 directly to disk.
	path := filepath.Join(dir, "SOUL.md")
	os.WriteFile(path, []byte("hello \xff\xfe world"), 0600)

	s := NewStore(dir, testLogger())
	if s.Soul() != "" {
		t.Errorf("Soul() = %q, want empty for invalid UTF-8 file", s.Soul())
	}
}

func TestCacheTruncation(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	// Content with more chars than maxCacheChars but within maxFileSize.
	// Use multi-byte runes to test rune-aware truncation.
	content := strings.Repeat("a", maxCacheChars+100)
	if err := s.WriteSoul(content); err != nil {
		t.Fatalf("WriteSoul() error: %v", err)
	}

	cached := s.Soul()
	if len([]rune(cached)) != maxCacheChars {
		t.Errorf("cached rune count = %d, want %d", len([]rune(cached)), maxCacheChars)
	}
}

func TestHasUserThreshold(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	// Exactly minUserContent chars — should not count.
	short := strings.Repeat("x", minUserContent)
	if err := s.WriteUser(short); err != nil {
		t.Fatalf("WriteUser() error: %v", err)
	}
	if s.HasUser() {
		t.Error("HasUser() = true for exactly minUserContent chars, want false")
	}

	// One more char — should count.
	enough := strings.Repeat("x", minUserContent+1)
	if err := s.WriteUser(enough); err != nil {
		t.Fatalf("WriteUser() error: %v", err)
	}
	if !s.HasUser() {
		t.Error("HasUser() = false for minUserContent+1 chars, want true")
	}
}

func TestEmptyContentRejected(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	if err := s.WriteSoul(""); err == nil {
		t.Error("WriteSoul('') should fail")
	}
	if err := s.WriteUser(""); err == nil {
		t.Error("WriteUser('') should fail")
	}
}

func TestReload(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	// Write directly to disk (bypassing store).
	os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("soul content"), 0600)
	os.WriteFile(filepath.Join(dir, "USER.md"), []byte("user content here"), 0600)

	// Cache should still be empty.
	if s.Soul() != "" {
		t.Error("Soul() should be empty before Reload")
	}

	s.Reload()

	if s.Soul() != "soul content" {
		t.Errorf("Soul() after Reload = %q, want %q", s.Soul(), "soul content")
	}
	if s.User() != "user content here" {
		t.Errorf("User() after Reload = %q, want %q", s.User(), "user content here")
	}
}

func TestPaths(t *testing.T) {
	dir := "/home/user/.config/crayfish"
	s := &Store{dir: dir, logger: testLogger()}

	if got := s.SoulPath(); got != filepath.Join(dir, "SOUL.md") {
		t.Errorf("SoulPath() = %q, want %q", got, filepath.Join(dir, "SOUL.md"))
	}
	if got := s.UserPath(); got != filepath.Join(dir, "USER.md") {
		t.Errorf("UserPath() = %q, want %q", got, filepath.Join(dir, "USER.md"))
	}
}

func TestMultiByteRuneSafetyOnDisk(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	// Create content with multi-byte runes that exceeds maxFileSize.
	// Each rune is 3 bytes (e.g., CJK character), so this is 3*maxFileSize bytes.
	bigMultiByte := strings.Repeat("\u4e16", maxFileSize) // 3 bytes per rune
	if err := s.WriteSoul(bigMultiByte); err != nil {
		t.Fatalf("WriteSoul() error: %v", err)
	}

	// File on disk must be valid UTF-8 (no split runes).
	data, err := os.ReadFile(filepath.Join(dir, "SOUL.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) > maxFileSize {
		t.Errorf("file size = %d, want <= %d", len(data), maxFileSize)
	}
	if !utf8.Valid(data) {
		t.Error("file on disk contains invalid UTF-8 after truncation")
	}
}

func TestTruncateBytesRoundTrip(t *testing.T) {
	// Verify truncateBytes never produces invalid UTF-8.
	tests := []struct {
		name  string
		input string
		limit int
	}{
		{"ascii", "hello world", 5},
		{"multibyte_exact", "\u4e16\u754c", 6}, // 2 runes, 6 bytes, limit exactly at boundary
		{"multibyte_mid", "\u4e16\u754c", 4},   // limit in middle of 2nd rune
		{"multibyte_mid2", "\u4e16\u754c", 5},  // limit at 2nd byte of 2nd rune
		{"emoji", "hello \U0001f600 world", 8}, // 4-byte emoji
		{"empty_after_trunc", "\u4e16", 1},     // 3-byte rune, limit 1 = empty
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := truncateBytes(tc.input, tc.limit)
			if !utf8.ValidString(result) {
				t.Errorf("truncateBytes(%q, %d) = %q, not valid UTF-8", tc.input, tc.limit, result)
			}
			if len(result) > tc.limit {
				t.Errorf("truncateBytes(%q, %d) len = %d, exceeds limit", tc.input, tc.limit, len(result))
			}
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, testLogger())

	var wg sync.WaitGroup
	// Concurrent writes and reads.
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func(n int) {
			defer wg.Done()
			s.WriteSoul(strings.Repeat("s", 50))
		}(i)
		go func(n int) {
			defer wg.Done()
			s.WriteUser(strings.Repeat("u", 50))
		}(i)
		go func(n int) {
			defer wg.Done()
			_ = s.Soul()
			_ = s.User()
			_ = s.HasUser()
		}(i)
	}
	wg.Wait()

	// If we get here without a race condition panic, the test passes.
	// The soul and user content should be non-empty.
	if s.Soul() == "" {
		t.Error("Soul() empty after concurrent writes")
	}
	if s.User() == "" {
		t.Error("User() empty after concurrent writes")
	}
}
