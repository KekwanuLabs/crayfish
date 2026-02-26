package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// mutableHub is a test HTTP handler that serves a hub index and skill YAMLs.
// The index and skills can be swapped after the server starts (for URL fixups).
type mutableHub struct {
	mu         sync.RWMutex
	index      *HubIndex
	skillYAMLs map[string]string // URL path → YAML content
}

func (m *mutableHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if r.URL.Path == "/index.json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(m.index)
		return
	}
	if yaml, ok := m.skillYAMLs[r.URL.Path]; ok {
		w.Header().Set("Content-Type", "text/yaml")
		fmt.Fprint(w, yaml)
		return
	}
	http.NotFound(w, r)
}

// startTestHub starts a test server and returns the hub client plus a cleanup function.
// skillYAMLs maps path suffixes (e.g. "/skills/foo.yaml") to YAML content.
// buildIndex receives the server URL so skill URLs can include the full host.
func startTestHub(t *testing.T, skillYAMLs map[string]string, buildIndex func(baseURL string) HubIndex) (*HubClient, func()) {
	t.Helper()
	h := &mutableHub{skillYAMLs: skillYAMLs}
	ts := httptest.NewServer(h)

	idx := buildIndex(ts.URL)
	h.mu.Lock()
	h.index = &idx
	h.mu.Unlock()

	hub := NewHubClient(ts.URL+"/index.json", testLogger())
	return hub, ts.Close
}

func TestSyncAll_SavesInCategorySubfolder(t *testing.T) {
	skillYAML := "name: test_skill\ndescription: A test skill\nprompt: hello world\ncategory: faith\n"

	hub, cleanup := startTestHub(t,
		map[string]string{"/skills/test_skill.yaml": skillYAML},
		func(base string) HubIndex {
			return HubIndex{
				Version: 1,
				Skills: []HubSkill{
					{Name: "test_skill", Version: 1, Category: "faith", URL: base + "/skills/test_skill.yaml"},
				},
			}
		},
	)
	defer cleanup()

	reg := NewRegistry(testLogger())
	dir := t.TempDir()

	installed, err := hub.SyncAll(context.Background(), reg, dir)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}
	if installed != 1 {
		t.Fatalf("expected 1 installed, got %d", installed)
	}

	// Verify file lands in faith/ subfolder.
	expectedPath := filepath.Join(dir, "faith", "test_skill.yaml")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("expected file at %s, got error: %v", expectedPath, err)
	}

	// Verify skill is registered with correct category.
	s := reg.Get("test_skill")
	if s == nil {
		t.Fatal("skill not registered")
	}
	if s.Category != "faith" {
		t.Errorf("expected category 'faith', got %q", s.Category)
	}
}

func TestSyncAll_PropagatesHubCategory(t *testing.T) {
	// Skill YAML has NO explicit category — should get it from the hub index.
	skillYAML := "name: prayer_times\ndescription: Prayer schedule\nprompt: show times\n"

	hub, cleanup := startTestHub(t,
		map[string]string{"/skills/prayer_times.yaml": skillYAML},
		func(base string) HubIndex {
			return HubIndex{
				Version: 1,
				Skills: []HubSkill{
					{Name: "prayer_times", Version: 1, Category: "faith", URL: base + "/skills/prayer_times.yaml"},
				},
			}
		},
	)
	defer cleanup()

	reg := NewRegistry(testLogger())
	dir := t.TempDir()

	installed, err := hub.SyncAll(context.Background(), reg, dir)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}
	if installed != 1 {
		t.Fatalf("expected 1 installed, got %d", installed)
	}

	s := reg.Get("prayer_times")
	if s == nil {
		t.Fatal("skill not registered")
	}
	if s.Category != "faith" {
		t.Errorf("expected category 'faith' (from hub), got %q", s.Category)
	}

	// Verify saved in correct subfolder.
	expectedPath := filepath.Join(dir, "faith", "prayer_times.yaml")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("expected file at %s, got error: %v", expectedPath, err)
	}
}

func TestSyncAll_SkipsSameVersion(t *testing.T) {
	skillYAML := "name: stable_skill\ndescription: Already installed\nprompt: hi\n"

	hub, cleanup := startTestHub(t,
		map[string]string{"/skills/stable_skill.yaml": skillYAML},
		func(base string) HubIndex {
			return HubIndex{
				Version: 1,
				Skills: []HubSkill{
					{Name: "stable_skill", Version: 2, Category: "productivity", URL: base + "/skills/stable_skill.yaml"},
				},
			}
		},
	)
	defer cleanup()

	reg := NewRegistry(testLogger())
	dir := t.TempDir()

	// Pre-register the same skill with same version.
	existing := &Skill{Name: "stable_skill", Version: 2, Category: "productivity", Prompt: "hi"}
	if err := reg.Register(existing); err != nil {
		t.Fatal(err)
	}

	installed, err := hub.SyncAll(context.Background(), reg, dir)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}
	if installed != 0 {
		t.Errorf("expected 0 installed (same version), got %d", installed)
	}
}

func TestSyncAll_UpgradesNewerVersion(t *testing.T) {
	skillYAMLv2 := "name: evolving_skill\nversion: 2\ndescription: Updated\nprompt: hello v2\n"

	hub, cleanup := startTestHub(t,
		map[string]string{"/skills/evolving_skill.yaml": skillYAMLv2},
		func(base string) HubIndex {
			return HubIndex{
				Version: 1,
				Skills: []HubSkill{
					{Name: "evolving_skill", Version: 2, Category: "travel", URL: base + "/skills/evolving_skill.yaml"},
				},
			}
		},
	)
	defer cleanup()

	reg := NewRegistry(testLogger())
	dir := t.TempDir()

	// Pre-register v1.
	existing := &Skill{Name: "evolving_skill", Version: 1, Category: "travel", Prompt: "hello v1"}
	if err := reg.Register(existing); err != nil {
		t.Fatal(err)
	}

	installed, err := hub.SyncAll(context.Background(), reg, dir)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}
	if installed != 1 {
		t.Errorf("expected 1 installed (upgrade), got %d", installed)
	}

	s := reg.Get("evolving_skill")
	if s == nil {
		t.Fatal("skill not registered")
	}
	if s.Version != 2 {
		t.Errorf("expected version 2, got %d", s.Version)
	}
	if s.Description != "Updated" {
		t.Errorf("expected description 'Updated', got %q", s.Description)
	}
}
