package skills

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestDefaultCategories_SortedAndContainsGeneral(t *testing.T) {
	if len(DefaultCategories) == 0 {
		t.Fatal("DefaultCategories must not be empty")
	}

	// Must be sorted for consistent dropdown ordering.
	if !sort.StringsAreSorted(DefaultCategories) {
		t.Error("DefaultCategories must be sorted alphabetically")
	}

	// Must contain "general" since it's the default fallback everywhere.
	found := false
	for _, c := range DefaultCategories {
		if c == "general" {
			found = true
			break
		}
	}
	if !found {
		t.Error("DefaultCategories must contain 'general'")
	}

	// No duplicates.
	seen := make(map[string]bool)
	for _, c := range DefaultCategories {
		if seen[c] {
			t.Errorf("duplicate category: %q", c)
		}
		seen[c] = true
	}
}

func TestLoadFromDir_SubfoldersOnly(t *testing.T) {
	dir := t.TempDir()

	// Create a top-level YAML — should be ignored.
	topLevel := filepath.Join(dir, "flat-skill.yaml")
	if err := os.WriteFile(topLevel, []byte("name: flat_skill\ndescription: flat\nprompt: hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a subfolder skill — should load.
	subDir := filepath.Join(dir, "productivity")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	subFile := filepath.Join(subDir, "my-skill.yaml")
	if err := os.WriteFile(subFile, []byte("name: my_skill\ndescription: sub\nprompt: hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(testLogger())
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}

	if reg.Get("flat_skill") != nil {
		t.Error("top-level YAML should NOT be loaded")
	}
	if s := reg.Get("my_skill"); s == nil {
		t.Error("subfolder skill should be loaded")
	} else if s.Category != "productivity" {
		t.Errorf("expected category 'productivity', got %q", s.Category)
	}
}

func TestSaveToFile_DefaultCategory(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(testLogger())

	skill := &Skill{Name: "test_skill", Prompt: "hello"}
	if err := reg.SaveToFile(skill, dir); err != nil {
		t.Fatal(err)
	}

	if skill.Category != "general" {
		t.Errorf("expected category 'general', got %q", skill.Category)
	}

	expectedPath := filepath.Join(dir, "general", "test_skill.yaml")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("expected file at %s, got error: %v", expectedPath, err)
	}
}

func TestSaveToFile_ExplicitCategory(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(testLogger())

	skill := &Skill{Name: "prayer_times", Category: "faith", Prompt: "hello"}
	if err := reg.SaveToFile(skill, dir); err != nil {
		t.Fatal(err)
	}

	expectedPath := filepath.Join(dir, "faith", "prayer_times.yaml")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("expected file at %s, got error: %v", expectedPath, err)
	}
}

func TestDeleteFile_SubfoldersOnly(t *testing.T) {
	dir := t.TempDir()

	// Create a top-level file — should NOT be found/deleted.
	topLevel := filepath.Join(dir, "orphan.yaml")
	if err := os.WriteFile(topLevel, []byte("name: orphan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a subfolder file — should be found and deleted.
	subDir := filepath.Join(dir, "faith")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	subFile := filepath.Join(subDir, "target.yaml")
	if err := os.WriteFile(subFile, []byte("name: target\n"), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(testLogger())

	// Delete from subfolders.
	if err := reg.DeleteFile("target", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(subFile); !os.IsNotExist(err) {
		t.Error("subfolder file should be deleted")
	}

	// Top-level file should remain untouched.
	if _, err := os.Stat(topLevel); err != nil {
		t.Error("top-level file should NOT be deleted by DeleteFile")
	}
}

func TestParseSkill_DefaultCategory(t *testing.T) {
	data := []byte("name: test_skill\ndescription: test\nprompt: hello\n")
	skill, err := ParseSkill(data)
	if err != nil {
		t.Fatal(err)
	}
	if skill.Category != "general" {
		t.Errorf("expected category 'general', got %q", skill.Category)
	}
}

func TestParseSkill_ExplicitCategory(t *testing.T) {
	data := []byte("name: test_skill\ncategory: faith\ndescription: test\nprompt: hello\n")
	skill, err := ParseSkill(data)
	if err != nil {
		t.Fatal(err)
	}
	if skill.Category != "faith" {
		t.Errorf("expected category 'faith', got %q", skill.Category)
	}
}

func TestRegister_DefaultCategory(t *testing.T) {
	reg := NewRegistry(testLogger())
	skill := &Skill{Name: "no_cat", Prompt: "hello"}
	if err := reg.Register(skill); err != nil {
		t.Fatal(err)
	}
	if skill.Category != "general" {
		t.Errorf("expected category 'general', got %q", skill.Category)
	}
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(testLogger())

	original := &Skill{
		Name:        "round_trip",
		Version:     3,
		Description: "A round-trip test skill",
		Category:    "travel",
		Type:        TypeWorkflow,
		Prompt:      "Plan a trip to {{destination}}",
		Trigger: Trigger{
			Command:  "/trip",
			Keywords: []string{"plan trip", "travel"},
		},
		Vars: map[string]string{"destination": "unknown"},
	}

	if err := reg.SaveToFile(original, dir); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	// Load from the same directory.
	reg2 := NewRegistry(testLogger())
	if err := reg2.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	loaded := reg2.Get("round_trip")
	if loaded == nil {
		t.Fatal("skill not found after round-trip")
	}

	if loaded.Category != "travel" {
		t.Errorf("category: want 'travel', got %q", loaded.Category)
	}
	if loaded.Version != 3 {
		t.Errorf("version: want 3, got %d", loaded.Version)
	}
	if loaded.Description != "A round-trip test skill" {
		t.Errorf("description: want 'A round-trip test skill', got %q", loaded.Description)
	}
	if loaded.Type != TypeWorkflow {
		t.Errorf("type: want 'workflow', got %q", loaded.Type)
	}
	if loaded.Trigger.Command != "/trip" {
		t.Errorf("trigger.command: want '/trip', got %q", loaded.Trigger.Command)
	}
	if loaded.Prompt != "Plan a trip to {{destination}}" {
		t.Errorf("prompt: want 'Plan a trip to {{destination}}', got %q", loaded.Prompt)
	}
}

func TestLoadFromDir_InfersCategory(t *testing.T) {
	dir := t.TempDir()

	// Skill YAML with NO category field — should be inferred from subfolder name.
	subDir := filepath.Join(dir, "faith")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	yamlData := "name: inferred_cat\ndescription: test\nprompt: hello\n"
	if err := os.WriteFile(filepath.Join(subDir, "inferred_cat.yaml"), []byte(yamlData), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(testLogger())
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}

	s := reg.Get("inferred_cat")
	if s == nil {
		t.Fatal("skill not loaded")
	}
	if s.Category != "faith" {
		t.Errorf("expected inferred category 'faith', got %q", s.Category)
	}
}

func TestLoadFromDir_ExplicitCategoryPreserved(t *testing.T) {
	dir := t.TempDir()

	// Skill YAML with explicit category matching its subfolder.
	subDir := filepath.Join(dir, "faith")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	yamlData := "name: explicit_cat\ncategory: faith\ndescription: test\nprompt: hello\n"
	if err := os.WriteFile(filepath.Join(subDir, "explicit_cat.yaml"), []byte(yamlData), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(testLogger())
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}

	s := reg.Get("explicit_cat")
	if s == nil {
		t.Fatal("skill not loaded")
	}
	if s.Category != "faith" {
		t.Errorf("expected category 'faith', got %q", s.Category)
	}
}
