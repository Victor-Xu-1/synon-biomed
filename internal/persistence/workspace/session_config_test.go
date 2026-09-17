package workspace

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestPatchCompatibilitySessionConfigTargetsRootAndPreservesContext(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "child", ProjectID: "project", ParentFrameID: "root", AgentName: "worker", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("root", FrameRuntimeMetadata{
		ContextData: map[string]any{
			"unrelated":       "preserved",
			"_original_input": map[string]any{"gpu_mode": "off", "existing": true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := store.PatchCompatibilitySessionConfig("child", map[string]any{
		"verifier_mode": "on", "reviewer_model": nil, "rc_context_ceiling": 200000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RootFrameID != "root" || result.Config["gpu_mode"] != "off" || result.Config["existing"] != true ||
		result.Config["verifier_mode"] != "on" || result.Config["reviewer_model"] != nil || result.Config["rc_context_ceiling"] != float64(200000) {
		t.Fatalf("session config result = %#v", result)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("root")
	if err != nil || !found || metadata.ContextData["unrelated"] != "preserved" {
		t.Fatalf("root metadata = %#v, found=%t, err=%v", metadata, found, err)
	}
	if _, found, err := store.GetFrameRuntimeMetadata("child"); err != nil || found {
		t.Fatalf("child metadata found=%t err=%v", found, err)
	}
	if _, err := store.PatchCompatibilitySessionConfig("root", map[string]any{"unknown": true}); err == nil {
		t.Fatal("unsupported session config key was accepted")
	}
}

func TestPatchCompatibilitySessionConfigSerializesConcurrentDistinctKeys(t *testing.T) {
	store := newCompactionTestStore(t)
	patches := []map[string]any{
		{"verifier_mode": "on"},
		{"memory_mode": "off"},
		{"auto_mode": "on"},
		{"reviewer_model": "reviewer"},
		{"rc_context_ceiling": 200000},
		{"python_version": "3.11"},
		{"kernel_idle_timeout": 60},
		{"goal_text": nil},
	}
	errors := make(chan error, len(patches))
	var group sync.WaitGroup
	for index, patch := range patches {
		group.Add(1)
		go func(index int, patch map[string]any) {
			defer group.Done()
			if _, err := store.PatchCompatibilitySessionConfig("frame", patch); err != nil {
				errors <- fmt.Errorf("patch %d: %w", index, err)
			}
		}(index, patch)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame")
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	config := metadata.ContextData["_original_input"].(map[string]any)
	if len(config) != len(patches) {
		t.Fatalf("concurrent session config = %#v", config)
	}
}
