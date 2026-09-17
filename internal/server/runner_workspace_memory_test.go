package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/memoryconfig"
	"synon-go/internal/memoryprompt"
	runtimekv "synon-go/internal/persistence/runtimekv"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceRunnerWorkspaceMemoryInjectsFactsAndRecallAtTrustedBoundaries(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	if err := store.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	contained, err := store.CreateMemoryCategory(ctx, "user-1", "Restricted Assays", "Only read when explicitly requested.", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.CreateMemoryInput{
		{ID: "mem_profile_visible", UserID: "user-1", Body: "User prefers concise CSV reports.", Origin: "user", Evidence: "stated"},
		{ID: "mem_profile_hidden", UserID: "user-1", Body: "Hidden EGFR assay directive.", Origin: "user", Evidence: "stated", CategoryID: contained.ID},
		{ID: "mem_project_match", UserID: "user-1", SubjectProjectID: "project-1", Body: "EGFR was selected from verified assay evidence.", Origin: "agent_tool", Evidence: "observed"},
		{ID: "mem_project_extra", UserID: "user-1", SubjectProjectID: "project-1", Body: "EGFR assay evidence should be checked against artifacts.", Origin: "agent_tool", Evidence: "observed"},
	} {
		if _, err := store.CreateMemory(input); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{workspaceStore: store, memoryConfig: memoryconfig.Default()}
	session := sessionstore.Session{ID: "frame-current", Project: &sessionstore.Project{ID: "project-1"}}
	messages := []chatCompletionMessage{
		{Role: "system", Content: "system authority"},
		{Role: "system", Content: "runtime skill context"},
		{Role: "user", Content: "Why was EGFR selected from verified assay evidence?"},
	}
	contextValue, err := server.workspaceMemoryContext(ctx, session, messages)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		memoryprompt.WhatNotToSave(), memoryprompt.MemoryWriteNudge(),
		"<memory_facts>", "### Profile", "User prefers concise CSV reports.",
		"Restricted Assays (1 fact · not auto-recalled)", memoryprompt.ContextTrailer,
		"[Memory] <memory_recall signal=\"user_message\">", "project:project-1",
		"EGFR was selected from verified assay evidence.", memoryprompt.RecallTrailer,
	} {
		if !strings.Contains(contextValue, want) {
			t.Fatalf("workspaceMemoryContext() missing %q:\n%s", want, contextValue)
		}
	}
	if strings.Contains(contextValue, "Hidden EGFR assay directive") || strings.Contains(contextValue, "score=") || strings.Contains(contextValue, "origin=") {
		t.Fatalf("workspaceMemoryContext() leaked contained or runtime-only metadata:\n%s", contextValue)
	}

	injected := appendRuntimeWorkspaceMemoryContextMessage(messages, contextValue)
	if len(injected) != 5 {
		t.Fatalf("injected messages = %#v", injected)
	}
	if injected[0].Content != "system authority" || injected[1].Content != "runtime skill context" ||
		injected[2].Role != "system" || !strings.HasPrefix(injected[2].Content, memoryprompt.SystemHeader) ||
		!strings.Contains(injected[2].Content, "<memory_facts>") ||
		injected[3].Role != "user" || !strings.HasPrefix(injected[3].Content, memoryprompt.RecallPrefix) ||
		injected[4].Content != messages[2].Content {
		t.Fatalf("memory boundaries were injected incorrectly: %#v", injected)
	}
	if len(messages) != 3 || messages[2].Content != "Why was EGFR selected from verified assay evidence?" {
		t.Fatalf("source messages were mutated: %#v", messages)
	}

	rows, err := store.ListMemoriesForUser(ctx, "user-1", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		switch row.ID {
		case "mem_project_match", "mem_project_extra":
			if row.LastSurfacedAt == nil {
				t.Fatalf("recalled row was not marked surfaced: %#v", row)
			}
		case "mem_profile_hidden":
			if row.LastSurfacedAt != nil {
				t.Fatalf("contained row was surfaced automatically: %#v", row)
			}
		}
	}
}

func TestWorkspaceRunnerWorkspaceMemoryHonorsPersistedFrameOptOut(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	if err := store.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame-current", workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{"_original_input": map[string]any{"memory_mode": "off"}},
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, memoryConfig: memoryconfig.Default()}
	session := sessionstore.Session{ID: "frame-current", Project: &sessionstore.Project{ID: "project-1"}}
	contextValue, err := server.workspaceMemoryContext(ctx, session, []chatCompletionMessage{{Role: "user", Content: "Recall prior facts"}})
	if err != nil || contextValue != "" {
		t.Fatalf("persisted frame opt-out context = %q, err=%v", contextValue, err)
	}
}

func TestWorkspaceRunnerWorkspaceMemoryDoesNotResurfaceSameRowsAcrossTurns(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	if err := store.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "mem_project_once", UserID: "user-1", SubjectProjectID: "project-1",
		Body: "EGFR assay selection used verified evidence.", Origin: "agent_tool", Evidence: "observed",
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		workspaceStore: store,
		runtimeStore:   runtimekv.New(filepath.Join(t.TempDir(), "runtime.json")),
		memoryConfig:   memoryconfig.Default(),
	}
	session := sessionstore.Session{ID: "frame-current", Project: &sessionstore.Project{ID: "project-1"}}
	messages := []chatCompletionMessage{{Role: "user", Content: "Why was EGFR assay selection used?"}}

	first, err := server.workspaceMemoryContext(ctx, session, messages)
	if err != nil || !strings.Contains(first, "mem_project_once") {
		t.Fatalf("first recall = %q, %v", first, err)
	}
	second, err := server.workspaceMemoryContext(ctx, session, messages)
	if err != nil || strings.Contains(second, "mem_project_once") || strings.Contains(second, memoryprompt.RecallPrefix+" signal=") {
		t.Fatalf("second recall repeated a served row = %q, %v", second, err)
	}
	entry, found, err := server.runtimeStore.Get(workspaceMemoryServedRuntimeNamespace, runtimeKeyFromSessionID(session.ID))
	values, _ := entry.Value.([]any)
	if err != nil || !found || len(values) != 1 || stringValue(values[0]) != "mem_project_once" {
		t.Fatalf("served memory state = %#v, %v, %v", entry, found, err)
	}
}

func TestWorkspaceRunnerWorkspaceMemoryInheritsParentServedRowsWithoutSharingMutableState(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	if err := store.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.CreateMemoryInput{
		{
			ID: "mem_parent_served", UserID: "user-1", SubjectProjectID: "project-1",
			Body: "EGFR parent evidence was already recalled.", Origin: "agent_tool", Evidence: "observed",
		},
		{
			ID: "mem_child_new", UserID: "user-1", SubjectProjectID: "project-1",
			Body: "EGFR child evidence remains available.", Origin: "agent_tool", Evidence: "observed",
		},
	} {
		if _, err := store.CreateMemory(input); err != nil {
			t.Fatal(err)
		}
	}
	runtimeStore := runtimekv.New(filepath.Join(t.TempDir(), "runtime.json"))
	if _, err := runtimeStore.Set(
		workspaceMemoryServedRuntimeNamespace,
		runtimeKeyFromSessionID("frame-parent"),
		[]string{"mem_parent_served"},
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, runtimeStore: runtimeStore, memoryConfig: memoryconfig.Default()}
	child := sessionstore.Session{
		ID:            "frame-current",
		Project:       &sessionstore.Project{ID: "project-1"},
		Orchestration: map[string]any{"parentSessionId": "frame-parent"},
	}
	messages := []chatCompletionMessage{{Role: "user", Content: "Recall EGFR parent and child evidence."}}

	contextValue, err := server.workspaceMemoryContext(ctx, child, messages)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(contextValue, "mem_parent_served") || !strings.Contains(contextValue, "mem_child_new") {
		t.Fatalf("child recall did not exclude the parent's served row: %q", contextValue)
	}
	childEntry, found, err := runtimeStore.Get(workspaceMemoryServedRuntimeNamespace, runtimeKeyFromSessionID(child.ID))
	if err != nil || !found {
		t.Fatalf("child served state missing: %#v, %v, %v", childEntry, found, err)
	}
	childValues, _ := childEntry.Value.([]any)
	if len(childValues) != 2 || stringValue(childValues[0]) != "mem_child_new" || stringValue(childValues[1]) != "mem_parent_served" {
		t.Fatalf("child served state = %#v", childEntry.Value)
	}
	parentEntry, found, err := runtimeStore.Get(workspaceMemoryServedRuntimeNamespace, runtimeKeyFromSessionID("frame-parent"))
	parentValues, _ := parentEntry.Value.([]any)
	if err != nil || !found || len(parentValues) != 1 || stringValue(parentValues[0]) != "mem_parent_served" {
		t.Fatalf("parent served state was mutated: %#v, %v, %v", parentEntry, found, err)
	}
}

func TestWorkspaceChildRunnerRecallsRootScratchpadAndKeepsServedStateOnChild(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	if err := store.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-root", ProjectID: "project-1", AgentName: "GENERAL", Status: "running", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-child", ProjectID: "project-1", ParentFrameID: root.ID,
		AgentName: "SPECIALIST", Status: "running", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "mem_root_scratchpad", UserID: "user-1", SubjectFrameID: root.ID,
		SourceFrameID: child.ID, Body: "CRBN root scratchpad records verified binding evidence.",
		Origin: "agent_tool", Evidence: "observed",
	}); err != nil {
		t.Fatal(err)
	}
	runtimeStore := runtimekv.New(filepath.Join(t.TempDir(), "runtime.json"))
	server := &Server{workspaceStore: store, runtimeStore: runtimeStore, memoryConfig: memoryconfig.Default()}
	session := sessionstore.Session{
		ID: "session-child", Project: &sessionstore.Project{ID: "project-1"},
		Orchestration: map[string]any{"frame_id": child.ID},
	}
	contextValue, err := server.workspaceMemoryContext(ctx, session, []chatCompletionMessage{{
		Role: "user", Content: "Use the verified CRBN binding evidence from the scratchpad.",
	}})
	if err != nil || !strings.Contains(contextValue, "mem_root_scratchpad") {
		t.Fatalf("child root-frame recall = %q, %v", contextValue, err)
	}
	if _, found, err := runtimeStore.Get(workspaceMemoryServedRuntimeNamespace, runtimeKeyFromSessionID(child.ID)); err != nil || !found {
		t.Fatalf("child served state missing: found=%v err=%v", found, err)
	}
	if _, found, err := runtimeStore.Get(workspaceMemoryServedRuntimeNamespace, runtimeKeyFromSessionID(root.ID)); err != nil || found {
		t.Fatalf("root served state was mutated: found=%v err=%v", found, err)
	}
}

func TestWorkspaceRunnerWorkspaceMemoryDefaultsOffAndHonorsProjectOverride(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "mem_profile", UserID: "user-1", Body: "Profile fact", Origin: "user", Evidence: "stated",
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, memoryConfig: memoryconfig.Default()}
	session := sessionstore.Session{ID: "frame-current", Project: &sessionstore.Project{ID: "project-1"}}
	messages := []chatCompletionMessage{{Role: "user", Content: "Please recall this profile fact"}}

	if got, err := server.workspaceMemoryContext(ctx, session, messages); err != nil || got != "" {
		t.Fatalf("default-disabled context=%q err=%v", got, err)
	}
	if err := store.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	if got, err := server.workspaceMemoryContext(ctx, session, messages); err != nil || !strings.Contains(got, "Profile fact") {
		t.Fatalf("enabled context=%q err=%v", got, err)
	}
	if err := store.SetProjectMemoryEnabled(ctx, "project-1", "user-1", false); err != nil {
		t.Fatal(err)
	}
	if got, err := server.workspaceMemoryContext(ctx, session, messages); err != nil || got != "" {
		t.Fatalf("project-disabled context=%q err=%v", got, err)
	}
}

func TestWorkspaceRunnerWorkspaceMemoryUsesRuntimeConfig(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	for _, input := range []workspace.CreateMemoryInput{
		{ID: "mem_profile", UserID: "user-1", Body: "Profile fact must stay out of the system prompt.", Origin: "user", Evidence: "stated"},
		{ID: "mem_first", UserID: "user-1", SubjectProjectID: "project-1", Body: "EGFR assay evidence alpha.", Origin: "agent_tool", Evidence: "observed"},
		{ID: "mem_second", UserID: "user-1", SubjectProjectID: "project-1", Body: "EGFR assay evidence beta.", Origin: "agent_tool", Evidence: "observed"},
	} {
		if _, err := store.CreateMemory(input); err != nil {
			t.Fatal(err)
		}
	}
	config := memoryconfig.Default()
	config.Enabled = true
	config.ContextInSystemPrompt = false
	config.RecallInjectMax = 1
	server := &Server{workspaceStore: store, memoryConfig: config}
	session := sessionstore.Session{ID: "frame-current", Project: &sessionstore.Project{ID: "project-1"}}

	contextValue, err := server.workspaceMemoryContext(ctx, session, []chatCompletionMessage{{
		Role: "user", Content: "Compare the EGFR assay evidence alpha and beta.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(contextValue, memoryprompt.SystemHeader) ||
		!strings.Contains(contextValue, memoryprompt.WhatNotToSave()) ||
		strings.Contains(contextValue, "Profile fact") || strings.Contains(contextValue, "<memory_facts>") {
		t.Fatalf("runtime config did not retain rules while suppressing system memory facts: %q", contextValue)
	}
	_, recall := splitWorkspaceMemoryContext(contextValue)
	if strings.Count(recall, "[mem_") != 1 || !strings.HasPrefix(recall, memoryprompt.RecallPrefix+" signal=") {
		t.Fatalf("runtime recall limit was not applied: %q", contextValue)
	}
}

func TestWorkspaceRunnerWorkspaceMemoryInjectsRulesWithoutStoredFacts(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	if err := store.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, memoryConfig: memoryconfig.Default()}
	session := sessionstore.Session{ID: "frame-current", Project: &sessionstore.Project{ID: "project-1"}}
	messages := []chatCompletionMessage{{Role: "user", Content: "Start a new unrelated task."}}

	contextValue, err := server.workspaceMemoryContext(ctx, session, messages)
	if err != nil {
		t.Fatal(err)
	}
	if contextValue != memoryprompt.SystemRules() ||
		!strings.Contains(contextValue, memoryprompt.MemoryWriteNudge()) {
		t.Fatalf("memory rules context = %q", contextValue)
	}
	injected := appendRuntimeWorkspaceMemoryContextMessage(messages, contextValue)
	if len(injected) != 2 || injected[0].Role != "system" ||
		injected[0].Content != memoryprompt.SystemRules() ||
		injected[1].Role != messages[0].Role || injected[1].Content != messages[0].Content {
		t.Fatalf("memory rules injection = %#v", injected)
	}
}

func TestWorkspaceRunnerWorkspaceMemoryUsesOneSignalAwareRecallPath(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	if err := store.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "mem_spawn", UserID: "user-1", SubjectProjectID: "project-1",
		Body: "CRBN structural evidence should be checked before delegation.", Origin: "agent_tool", Evidence: "observed",
	}); err != nil {
		t.Fatal(err)
	}
	runtimeStore := runtimekv.New(filepath.Join(t.TempDir(), "runtime.json"))
	server := &Server{workspaceStore: store, runtimeStore: runtimeStore, memoryConfig: memoryconfig.Default()}
	for _, kind := range []string{"subagent_spawn", "plan_generated"} {
		rendered := server.workspaceMemoryRecallSignal(
			ctx, "user-1", "project-1", "frame-current", "frame-current", "",
			"Investigate the CRBN structural evidence before delegation", kind, nil,
		)
		if !strings.Contains(rendered, `signal="`+kind+`"`) || !strings.Contains(rendered, "mem_spawn") {
			t.Fatalf("%s recall = %q", kind, rendered)
		}
	}
	entry, found, err := runtimeStore.Get(workspaceMemoryServedRuntimeNamespace, runtimeKeyFromSessionID("frame-current"))
	if err != nil || !found {
		t.Fatalf("served state = %#v found=%v err=%v", entry, found, err)
	}
	values, _ := entry.Value.([]any)
	if len(values) != 1 || stringValue(values[0]) != "mem_spawn" {
		t.Fatalf("served state value = %#v", entry.Value)
	}
}

func TestWorkspaceSessionMemoryModeUsesExactOnOffStrings(t *testing.T) {
	cases := []struct {
		name          string
		configuration map[string]any
		enabled       bool
	}{
		{name: "missing", enabled: true},
		{name: "off", configuration: map[string]any{"memory_mode": "off"}, enabled: false},
		{name: "on", configuration: map[string]any{"memory_mode": "on"}, enabled: true},
		{name: "uppercase is undefined", configuration: map[string]any{"memory_mode": "OFF"}, enabled: true},
		{name: "boolean is undefined", configuration: map[string]any{"memory_mode": false}, enabled: true},
		{name: "legacy synonym is undefined", configuration: map[string]any{"memory_mode": "disabled"}, enabled: true},
		{name: "camel transport alias", configuration: map[string]any{"memoryMode": "off"}, enabled: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			session := sessionstore.Session{}
			if test.configuration != nil {
				session.Orchestration = map[string]any{"sessionConfig": test.configuration}
			}
			if got := sessionWorkspaceMemoryEnabledWithFrame(session, workspace.FrameRuntimeMetadata{}, false); got != test.enabled {
				t.Fatalf("sessionWorkspaceMemoryEnabledWithFrame() = %v, want %v", got, test.enabled)
			}
		})
	}
}

func TestWorkspaceExplicitMemoryToolsShareTheExactSessionModePolicy(t *testing.T) {
	store := openWorkspaceRunnerMemoryStore(t)
	ctx := context.Background()
	if err := store.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, memoryConfig: memoryconfig.Default()}
	for _, test := range []struct {
		mode    string
		enabled bool
	}{{mode: "off", enabled: false}, {mode: "on", enabled: true}, {mode: "OFF", enabled: true}, {mode: "disabled", enabled: true}, {mode: "", enabled: true}} {
		enabled, err := server.workspaceMemoryAccessEnabled(ctx, "user-1", "project-1", test.mode, true)
		if err != nil || enabled != test.enabled {
			t.Fatalf("mode=%q enabled=%v want=%v err=%v", test.mode, enabled, test.enabled, err)
		}
	}
}

func TestWorkspaceSessionMemoryModeFallsBackToPersistedFrameInput(t *testing.T) {
	cases := []struct {
		name     string
		session  sessionstore.Session
		metadata workspace.FrameRuntimeMetadata
		enabled  bool
	}{
		{
			name: "original input opt out",
			metadata: workspace.FrameRuntimeMetadata{ContextData: map[string]any{
				"_original_input": map[string]any{"memory_mode": "off"},
			}},
			enabled: false,
		},
		{
			name: "submission input opt out",
			metadata: workspace.FrameRuntimeMetadata{
				ContextData: map[string]any{}, InputData: map[string]any{"memory_mode": "off"},
			},
			enabled: false,
		},
		{
			name: "original input overrides submission input",
			metadata: workspace.FrameRuntimeMetadata{
				ContextData: map[string]any{"_original_input": map[string]any{"memory_mode": "on"}},
				InputData:   map[string]any{"memory_mode": "off"},
			},
			enabled: true,
		},
		{
			name: "session config overrides persisted input",
			session: sessionstore.Session{Orchestration: map[string]any{
				"sessionConfig": map[string]any{"memory_mode": "on"},
			}},
			metadata: workspace.FrameRuntimeMetadata{
				ContextData: map[string]any{"_original_input": map[string]any{"memory_mode": "off"}},
			},
			enabled: true,
		},
		{
			name: "invalid original value blocks stale submission fallback",
			metadata: workspace.FrameRuntimeMetadata{
				ContextData: map[string]any{"_original_input": map[string]any{"memory_mode": false}},
				InputData:   map[string]any{"memory_mode": "off"},
			},
			enabled: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := sessionWorkspaceMemoryEnabledWithFrame(test.session, test.metadata, true); got != test.enabled {
				t.Fatalf("sessionWorkspaceMemoryEnabledWithFrame() = %v, want %v", got, test.enabled)
			}
		})
	}
}

func TestWorkspaceWorkspaceMemoryServedIDsOnlyReadsRecallBlocks(t *testing.T) {
	messages := []chatCompletionMessage{
		{Role: "user", Content: "ordinary text [mem_not_served]"},
		{Role: "user", Content: "[Memory] <memory_recall signal=\"user_message\">\nprofile\n  - [recently] [stated] fact [mem_ABC123]\n</memory_recall>"},
	}
	served := workspaceMemoryServedIDs(messages)
	if _, exists := served["mem_abc123"]; !exists || len(served) != 1 {
		t.Fatalf("served IDs = %#v", served)
	}
}

func openWorkspaceRunnerMemoryStore(t *testing.T) *workspace.Store {
	t.Helper()
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-current", ProjectID: "project-1", AgentName: "GENERAL", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	return store
}
