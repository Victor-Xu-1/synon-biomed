package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/agentruntime"
	assetmanifest "synon-go/internal/assets"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestBundledOperonIgnoresStaleFixedSkillDefaults(t *testing.T) {
	record := webAssistantRecord{
		Bundled: true,
		Agent: workspace.Agent{
			Name:       "OPERON",
			SkillNames: []string{"current-skill"},
		},
		Config: webAssistantStoredConfig{
			Defaults: map[string]any{
				"skills": map[string]any{
					"mode":  "fixed",
					"value": []any{"removed-skill", "stale-skill"},
				},
			},
		},
	}

	selected, fixed := webAssistantDefaultSkillSelection(record)
	if len(selected) != 0 || fixed {
		t.Fatalf("stale OPERON defaults selected=%v fixed=%t", selected, fixed)
	}
}

func TestOperonDoesNotInjectLockedCatalogIntoEachTurn(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "operon"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte("agent_name: OPERON\ndisplay_name: OPERON\ndescription: test\nskills_locked: true\nidentity_prompt: test identity\nworking_style_prompt: test style\n")
	operonSkills := []byte("{\"schemaVersion\":1,\"agent\":\"operon\",\"skills\":[\"current-skill\"]}\n")
	capabilities := []byte("{\"schemaVersion\":1,\"agents\":[]}\n")
	metadataPath := filepath.Join(root, "operon", "metadata.yaml")
	skillsPath := filepath.Join(root, "operon-skills.json")
	capabilitiesPath := filepath.Join(root, "capabilities.json")
	if err := os.WriteFile(metadataPath, metadata, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillsPath, operonSkills, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capabilitiesPath, capabilities, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := assetmanifest.Manifest{
		SchemaVersion: 1,
		Agents:        []string{"operon"},
		Files: []assetmanifest.File{
			{Path: "capabilities.json", SHA256: testSHA256(capabilities), Bytes: int64(len(capabilities))},
			{Path: "operon/metadata.yaml", SHA256: testSHA256(metadata), Bytes: int64(len(metadata))},
			{Path: "operon-skills.json", SHA256: testSHA256(operonSkills), Bytes: int64(len(operonSkills))},
		},
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "agents.manifest.json")
	if err := os.WriteFile(manifestPath, manifestRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := agentruntime.LoadCatalog(agentruntime.CatalogOptions{
		Root:            root,
		ManifestPath:    manifestPath,
		AvailableSkills: []string{"current-skill"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, found := catalog.Agent("OPERON")
	if !found || !agent.SkillsLocked {
		t.Fatalf("fixture agent found=%v locked=%v", found, agent.SkillsLocked)
	}

	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, AgentCatalog: catalog, FileRoot: t.TempDir()})
	options, selected := app.applySessionRunnerBundledAgent(sessionstore.Session{ID: "frame"}, SessionRunnerChatOptions{})
	if selected != "OPERON" {
		t.Fatalf("selected agent=%q", selected)
	}
	if len(options.SelectedSkillNames) != 0 || len(options.AllowedSkillNames) != 0 || options.DisableSkillDiscovery || options.RestrictSkillDiscovery {
		t.Fatalf("OPERON per-turn skill injection selected=%v allowed=%v disabled=%t restricted=%t", options.SelectedSkillNames, options.AllowedSkillNames, options.DisableSkillDiscovery, options.RestrictSkillDiscovery)
	}
}

func testSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
