package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/skills"
)

func TestCompatibilitySkillCatalogUserStateContentAndPathSafety(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	demoRoot := filepath.Join(skillRoot, "demo")
	if err := os.MkdirAll(filepath.Join(demoRoot, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	skillDocument := "---\nname: demo\ndescription: Demo evidence skill\n---\nUse primary evidence."
	if err := os.WriteFile(filepath.Join(demoRoot, "SKILL.md"), []byte(skillDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(demoRoot, "references", "guide.md"), []byte("reference guide"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(outside, []byte("must not escape"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(demoRoot, "references", "escape.md")); err != nil {
		t.Fatal(err)
	}
	catalog := skills.Load([]string{skillRoot})
	for _, item := range catalog.Skills() {
		item.Category = "research-workflows"
		item.DescriptionI18n = map[string]string{"zh-CN": "核查研究证据。"}
		catalog.UpsertSkill(item)
	}
	for _, skill := range skills.BuiltinRuntimeCatalog().Skills() {
		catalog.AddSkill(skill)
	}
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateAgent(workspace.CreateAgentInput{
		ID: "agent-1", UserID: "user-1", Name: "RESEARCH",
		DisplayName: "Research", Description: "Evidence agent",
		SkillNames: []string{"demo"},
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store, SkillCatalog: catalog})
	app := srv.Handler()

	listed := compatJSONRequest(t, app, http.MethodGet, "/api/skills/catalog", "user-1", nil, http.StatusOK)
	if listed["degraded"] != false {
		t.Fatalf("catalog degraded = %#v", listed)
	}
	counts := listed["catalog_counts"].(map[string]any)
	bySource := counts["by_source"].(map[string]any)
	if counts["total"] != float64(2) || counts["packaged_directory_skills"] != float64(0) ||
		counts["builtin_platform_skills"] != float64(1) || bySource["personal"] != float64(1) ||
		listed["identity_contract"] == "" {
		t.Fatalf("catalog governance metadata=%#v", listed)
	}
	demo := compatibilityCatalogEntryByName(t, listed, "demo")
	if demo["source"] != "personal" || demo["skillId"] != "local:demo" {
		t.Fatalf("demo catalog entry = %#v", demo)
	}
	if demo["category"] != "research-workflows" || demo["description_i18n"].(map[string]any)["zh-CN"] != "核查研究证据。" {
		t.Fatalf("presentation metadata lost by API: %#v", demo)
	}
	agents := demo["attachedAgents"].([]any)
	if len(agents) != 1 || agents[0] != "RESEARCH" {
		t.Fatalf("attached agents = %#v", agents)
	}

	compatJSONRequest(t, app, http.MethodPut, "/api/skills/catalog/demo/enabled", "user-1", map[string]any{
		"enabled": false,
	}, http.StatusOK)
	userOne := compatibilityCatalogEntryByName(t,
		compatJSONRequest(t, app, http.MethodGet, "/api/skills/catalog", "user-1", nil, http.StatusOK),
		"demo",
	)
	if userOne["enabled"] != false {
		t.Fatalf("user-1 disabled entry = %#v", userOne)
	}
	userTwo := compatibilityCatalogEntryByName(t,
		compatJSONRequest(t, app, http.MethodGet, "/api/skills/catalog", "user-2", nil, http.StatusOK),
		"demo",
	)
	if _, configured := userTwo["enabled"]; configured {
		t.Fatalf("user-2 inherited user-1 preference = %#v", userTwo)
	}
	preferences, err := store.ListSkillPreferences("user-1")
	if err != nil || preferences["demo"] {
		t.Fatalf("persisted user-1 preferences = %#v, err=%v", preferences, err)
	}
	otherPreferences, err := store.ListSkillPreferences("user-2")
	if err != nil || len(otherPreferences) != 0 {
		t.Fatalf("persisted user-2 preferences = %#v, err=%v", otherPreferences, err)
	}

	platform := compatibilityCatalogEntryByName(t, listed, "synon-runtime")
	if platform["source"] != "synon_llm" || platform["skillId"] != "bundled:synon-runtime" || platform["userHidden"] != true {
		t.Fatalf("platform entry = %#v", platform)
	}
	compatJSONRequest(t, app, http.MethodPut, "/api/skills/catalog/synon-runtime/enabled", "user-1", map[string]any{
		"enabled": false,
	}, http.StatusForbidden)

	mainContent := compatJSONRequest(t, app, http.MethodGet, "/api/skills/catalog/demo/content", "user-1", nil, http.StatusOK)
	if mainContent["content"] != skillDocument {
		t.Fatalf("main content = %#v", mainContent)
	}
	reference := compatJSONRequest(t, app, http.MethodGet, "/api/skills/catalog/demo/content?path=references%2Fguide.md", "user-1", nil, http.StatusOK)
	if reference["content"] != "reference guide" {
		t.Fatalf("reference content = %#v", reference)
	}
	compatJSONRequest(t, app, http.MethodGet, "/api/skills/catalog/demo/content?path=..%2Fsecret.txt", "user-1", nil, http.StatusBadRequest)
	compatJSONRequest(t, app, http.MethodGet, "/api/skills/catalog/demo/content?path=references%2Fescape.md", "user-1", nil, http.StatusNotFound)
}

func compatibilityCatalogEntryByName(t *testing.T, response map[string]any, name string) map[string]any {
	t.Helper()
	values, ok := response["skills"].([]any)
	if !ok {
		t.Fatalf("skills response = %#v", response)
	}
	for _, value := range values {
		entry, ok := value.(map[string]any)
		if ok && entry["name"] == name {
			return entry
		}
	}
	t.Fatalf("skill %q not found in %#v", name, response)
	return nil
}
