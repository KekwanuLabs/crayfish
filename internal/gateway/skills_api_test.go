package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KekwanuLabs/crayfish/internal/skills"
)

func testRegistry(t *testing.T) *skills.Registry {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return skills.NewRegistry(logger)
}

func TestHandleCategoriesReturnsDefaults(t *testing.T) {
	reg := testRegistry(t)
	api := NewSkillsAPI(reg, "", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/skills/categories", nil)
	api.handleCategories(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Categories []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"categories"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Categories) != len(skills.DefaultCategories) {
		t.Fatalf("categories count = %d, want %d", len(resp.Categories), len(skills.DefaultCategories))
	}

	// Should be sorted and match DefaultCategories exactly.
	for i, c := range resp.Categories {
		if c.Name != skills.DefaultCategories[i] {
			t.Errorf("category[%d] = %q, want %q", i, c.Name, skills.DefaultCategories[i])
		}
		if c.Count != 0 {
			t.Errorf("category[%d] count = %d, want 0 (empty registry)", i, c.Count)
		}
	}
}

func TestHandleCategoriesCountsSkills(t *testing.T) {
	reg := testRegistry(t)

	// Register two skills in "travel" category.
	reg.Register(&skills.Skill{Name: "trip-plan", Category: "travel", Prompt: "plan trip"})
	reg.Register(&skills.Skill{Name: "pack-list", Category: "travel", Prompt: "pack list"})
	// Register one in "general".
	reg.Register(&skills.Skill{Name: "hello", Prompt: "hi"}) // defaults to general

	api := NewSkillsAPI(reg, "", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/skills/categories", nil)
	api.handleCategories(w, r)

	var resp struct {
		Categories []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"categories"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	catCounts := make(map[string]int)
	for _, c := range resp.Categories {
		catCounts[c.Name] = c.Count
	}

	if catCounts["travel"] != 2 {
		t.Errorf("travel count = %d, want 2", catCounts["travel"])
	}
	if catCounts["general"] != 1 {
		t.Errorf("general count = %d, want 1", catCounts["general"])
	}
	// Other defaults should still be present with 0.
	if catCounts["fitness"] != 0 {
		t.Errorf("fitness count = %d, want 0", catCounts["fitness"])
	}
}

func TestHandleCategoriesIncludesNonDefault(t *testing.T) {
	reg := testRegistry(t)

	// Register a skill with a category NOT in DefaultCategories.
	reg.Register(&skills.Skill{Name: "custom-skill", Category: "custom-cat", Prompt: "test"})

	api := NewSkillsAPI(reg, "", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/skills/categories", nil)
	api.handleCategories(w, r)

	var resp struct {
		Categories []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"categories"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	// Should have DefaultCategories + 1 custom.
	if len(resp.Categories) != len(skills.DefaultCategories)+1 {
		t.Errorf("categories count = %d, want %d", len(resp.Categories), len(skills.DefaultCategories)+1)
	}

	found := false
	for _, c := range resp.Categories {
		if c.Name == "custom-cat" && c.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Error("custom-cat not found in categories response")
	}
}

func TestCreateSkillWithCategory(t *testing.T) {
	reg := testRegistry(t)
	dir := t.TempDir()
	api := NewSkillsAPI(reg, dir, nil)

	body := `{"name":"trip-finder","type":"prompt","category":"travel","description":"Find trips","prompt":"find trips"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	api.handleSkills(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}

	// Verify the skill was registered with correct category.
	skill := reg.Get("trip-finder")
	if skill == nil {
		t.Fatal("skill not found in registry")
	}
	if skill.Category != "travel" {
		t.Errorf("category = %q, want 'travel'", skill.Category)
	}

	// Verify file saved in correct subfolder.
	expectedPath := filepath.Join(dir, "travel", "trip-finder.yaml")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("expected file at %s: %v", expectedPath, err)
	}
}

func TestCreateSkillWithoutCategoryDefaultsToGeneral(t *testing.T) {
	reg := testRegistry(t)
	dir := t.TempDir()
	api := NewSkillsAPI(reg, dir, nil)

	body := `{"name":"my-skill","type":"prompt","description":"Test","prompt":"test prompt"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	api.handleSkills(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}

	skill := reg.Get("my-skill")
	if skill == nil {
		t.Fatal("skill not found in registry")
	}
	if skill.Category != "general" {
		t.Errorf("category = %q, want 'general'", skill.Category)
	}

	// Verify file in general/ subfolder.
	expectedPath := filepath.Join(dir, "general", "my-skill.yaml")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("expected file at %s: %v", expectedPath, err)
	}
}

func TestGetSkillReturnsCategory(t *testing.T) {
	reg := testRegistry(t)
	reg.Register(&skills.Skill{Name: "test-skill", Category: "faith", Prompt: "test"})

	api := NewSkillsAPI(reg, "", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/skills/test-skill", nil)
	api.handleSkill(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["category"] != "faith" {
		t.Errorf("category = %v, want 'faith'", resp["category"])
	}
}

func TestListSkillsIncludesCategory(t *testing.T) {
	reg := testRegistry(t)
	reg.Register(&skills.Skill{Name: "a-skill", Category: "travel", Prompt: "test"})
	reg.Register(&skills.Skill{Name: "b-skill", Category: "fitness", Prompt: "test"})

	api := NewSkillsAPI(reg, "", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	api.handleSkills(w, r)

	var resp struct {
		Skills []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"skills"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	cats := make(map[string]string)
	for _, s := range resp.Skills {
		cats[s.Name] = s.Category
	}

	if cats["a-skill"] != "travel" {
		t.Errorf("a-skill category = %q, want 'travel'", cats["a-skill"])
	}
	if cats["b-skill"] != "fitness" {
		t.Errorf("b-skill category = %q, want 'fitness'", cats["b-skill"])
	}
}

func TestListSkillsCategoryFilter(t *testing.T) {
	reg := testRegistry(t)
	reg.Register(&skills.Skill{Name: "s1", Category: "travel", Prompt: "test"})
	reg.Register(&skills.Skill{Name: "s2", Category: "fitness", Prompt: "test"})
	reg.Register(&skills.Skill{Name: "s3", Category: "travel", Prompt: "test"})

	api := NewSkillsAPI(reg, "", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/skills?category=travel", nil)
	api.handleSkills(w, r)

	var resp struct {
		Skills []struct {
			Name string `json:"name"`
		} `json:"skills"`
		Count int `json:"count"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Count != 2 {
		t.Errorf("filtered count = %d, want 2", resp.Count)
	}
}
