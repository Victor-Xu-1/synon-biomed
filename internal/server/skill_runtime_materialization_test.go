package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	runtimekv "synon-go/internal/persistence/runtimekv"
	sessionstore "synon-go/internal/persistence/sessions"
	"synon-go/internal/skills"
)

func TestBundledAutoDockVinaSkillReturnsWhenInvocationAuditStoreIsUnavailable(t *testing.T) {
	catalog := skills.Load([]string{filepath.Join("..", "..", "skills", "synonbiomed")})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) != 0 {
		t.Fatalf("load bundled skills: %#v", loadErrors)
	}
	runtimeRoot := t.TempDir()
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Micromamba: filepath.Join(runtimeRoot, "micromamba"), CondaHome: filepath.Join(runtimeRoot, "conda"),
		CondaEnvsPath: filepath.Join(runtimeRoot, "conda", "envs"), CondaRuntimeCatalog: filepath.Join(runtimeRoot, "catalog.json"),
	})
	supervisorCtx, cancelSupervisor := context.WithCancel(context.Background())
	supervisorDone := make(chan error, 1)
	go func() { supervisorDone <- manager.RunManagedEnvironmentSupervisor(supervisorCtx) }()
	t.Cleanup(func() {
		cancelSupervisor()
		select {
		case <-supervisorDone:
		case <-time.After(2 * time.Second):
			t.Error("managed environment supervisor did not stop")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for !manager.ManagedEnvironmentSupervisorReady() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	fixture := newAgentSaveArtifactsFixtureWithKernelManager(t, manager)
	server := fixture.server
	server.skillCatalog = catalog
	ownedRuntimeStore := server.runtimeStore
	server.runtimeStore = runtimekv.New("")
	t.Cleanup(func() {
		server.runtimeStore = ownedRuntimeStore
	})
	_, ok := findCatalogSkill(catalog, "autodock-vina")
	if !ok {
		t.Fatal("autodock-vina skill missing from bundled catalog")
	}
	result, err := server.executeSkillToolWithContext(context.Background(), map[string]any{"skill": "autodock-vina"})
	if err != nil {
		t.Fatal(err)
	}
	payload := mapValue(result)
	if payload["data"] == nil || !strings.Contains(stringValue(mapValue(payload["skill"])["body"]), "AutoDock Vina") {
		t.Fatalf("autodock-vina invocation = %#v", payload)
	}
	persistence := mapValue(payload["invocation_persistence"])
	if persistence["ok"] != false || persistence["recoverable"] != true {
		t.Fatalf("missing recoverable invocation persistence diagnostic = %#v", payload)
	}
}

func TestAgentUnknownSkillReturnsCatalogPreflightWithoutLoadingIt(t *testing.T) {
	catalog := skills.Load([]string{filepath.Join("..", "..", "skills", "synonbiomed")})
	server := New(Options{FileRoot: t.TempDir()})
	server.skillCatalog = catalog
	run := &sessionRunnerChatRun{}
	ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)
	result, err := server.executeSkillToolWithContext(ctx, map[string]any{"skill": "fda-drug-label"})
	if err != nil {
		t.Fatal(err)
	}
	payload := mapValue(result)
	if payload["success"] != false || payload["ok"] != false || payload["executed"] != false || payload["status"] != "skill_not_found" ||
		!strings.Contains(stringValue(payload["recovery"]), "search_skills") {
		t.Fatalf("unknown Skill preflight=%#v", payload)
	}
	if len(run.executedSkillNamesSnapshot()) != 0 {
		t.Fatalf("unknown Skill was recorded as loaded: %v", run.executedSkillNamesSnapshot())
	}
}

func TestMaterializeAgentSkillRuntimeBundleCopiesAndRepairsResources(t *testing.T) {
	skillRoot := filepath.Join(t.TempDir(), "document-workbench")
	if err := os.MkdirAll(filepath.Join(skillRoot, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: document-workbench\n---\n\nRun ${SYNON_SKILL_DIR}/scripts/tool.py.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "scripts", "tool.py"), []byte("print('verified')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := skills.Skill{Name: "document-workbench", Path: skillFile}
	bundle, hasResources, err := loadAgentSkillRuntimeBundle(skill)
	if err != nil {
		t.Fatal(err)
	}
	if !hasResources {
		t.Fatal("script resource was not detected")
	}
	workspace := t.TempDir()
	first, err := materializeAgentSkillRuntimeBundle(workspace, skill.Name, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !hostPathWithin(workspace, first) || !strings.Contains(first, filepath.Join(".synon", "runtime", "skills")) {
		t.Fatalf("materialized path escaped workspace: %q", first)
	}
	script := filepath.Join(first, "scripts", "tool.py")
	if raw, readErr := os.ReadFile(script); readErr != nil || string(raw) != "print('verified')\n" {
		t.Fatalf("materialized script = %q, err=%v", raw, readErr)
	}
	if err := os.Chmod(script, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("print('tampered')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := materializeAgentSkillRuntimeBundle(workspace, skill.Name, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("content-addressed runtime changed path: %q != %q", second, first)
	}
	if raw, readErr := os.ReadFile(script); readErr != nil || string(raw) != "print('verified')\n" {
		t.Fatalf("repaired script = %q, err=%v", raw, readErr)
	}
}

func TestMaterializeAgentSkillRuntimeBundleNormalizesPortableShebangAndMode(t *testing.T) {
	skillRoot := filepath.Join(t.TempDir(), "portable-script")
	if err := os.MkdirAll(filepath.Join(skillRoot, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: portable-script\n---\nRun ${SYNON_SKILL_DIR}/scripts/run.py.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "scripts", "run.py"), []byte("#!/usr/bin/env python3\r\nprint('ok')\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle, hasResources, err := loadAgentSkillRuntimeBundle(skills.Skill{Name: "portable-script", Path: skillFile})
	if err != nil || !hasResources {
		t.Fatalf("bundle resources=%t err=%v", hasResources, err)
	}
	directory, err := materializeAgentSkillRuntimeBundle(t.TempDir(), "portable-script", bundle)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(directory, "scripts", "run.py")
	raw, err := os.ReadFile(script)
	if err != nil || strings.Contains(string(raw), "\r") {
		t.Fatalf("materialized script=%q err=%v", raw, err)
	}
	info, err := os.Stat(script)
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("materialized mode=%#o err=%v", info.Mode().Perm(), err)
	}
}

func TestLoadAgentSkillRuntimeBundleLeavesInstructionOnlySkillInline(t *testing.T) {
	skillRoot := t.TempDir()
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: inline-only\n---\n\nNo resources.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, hasResources, err := loadAgentSkillRuntimeBundle(skills.Skill{Name: "inline-only", Path: skillFile})
	if err != nil {
		t.Fatal(err)
	}
	if hasResources {
		t.Fatal("instruction-only skill should remain inline")
	}
}

func TestRenderSkillPromptSurfacesPreferredExecutionAssets(t *testing.T) {
	skill := skills.Skill{
		Name: "analysis-workflow", Path: "/workspace/.synon/runtime/skills/analysis-workflow-abcd/SKILL.md",
		Body: "Analyze the supplied cohort.", PreferredExecutionAssets: []string{"scripts/pipeline.py", "templates/report.md"},
	}
	prompt := renderSkillPrompt(skill, "")
	for _, expected := range []string{
		"Default tested execution route", "/workspace/.synon/runtime/skills/analysis-workflow-abcd/scripts/pipeline.py",
		"/workspace/.synon/runtime/skills/analysis-workflow-abcd/templates/report.md", "Analyze the supplied cohort.",
		"Inspect the first applicable asset's help and preflight", "preserve bounded-memory loading",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("rendered skill prompt omitted %q: %s", expected, prompt)
		}
	}
}

func TestRuntimeSkillExecutionPriorityIsCompactAndAssetDriven(t *testing.T) {
	context := runtimeSkillExecutionPriorityContext([]skills.Skill{
		{Name: "analysis-workflow", Path: "/runtime/analysis/SKILL.md", Body: strings.Repeat("large body ", 100), PreferredExecutionAssets: []string{"scripts/pipeline.py"}},
		{Name: "guidance-only", Path: "/runtime/guidance/SKILL.md", Body: "No executable asset."},
	})
	if !strings.Contains(context, "primary implementation") ||
		!strings.Contains(context, "/runtime/analysis/scripts/pipeline.py") ||
		strings.Contains(context, "large body") || strings.Contains(context, "guidance-only") {
		t.Fatalf("execution priority context=%q", context)
	}
}

func TestMaterializeAgentSkillRuntimeLeavesUnreferencedResourcesInline(t *testing.T) {
	skillRoot := filepath.Join(t.TempDir(), "reference-only")
	if err := os.MkdirAll(filepath.Join(skillRoot, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: reference-only\n---\n\nRead the inline guidance.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "references", "guide.md"), []byte("reference\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := skills.Skill{Name: "reference-only", Path: skillFile, Body: "Read the inline guidance."}
	materialized, err := (&Server{}).materializeAgentSkillRuntime(context.Background(), "session", skill)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Path != skill.Path {
		t.Fatalf("unreferenced resources changed skill path: %q", materialized.Path)
	}
}

func TestPrepareAgentSkillRuntimeContextsResolvesResourcesBeforeFirstModelRequest(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(
		t,
		filepath.Join(t.TempDir(), "runtime.db"),
		true,
	)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	sessionID := "document-workbench-first-turn"
	if err := app.sessionStore.Upsert(sessionstore.Session{
		ID:      sessionID,
		WorkDir: identity.workspaceDir,
		Orchestration: map[string]any{
			"frame_id": "root-routine",
		},
	}); err != nil {
		t.Fatal(err)
	}

	skillRoot := filepath.Join(t.TempDir(), "document-workbench")
	if err := os.MkdirAll(filepath.Join(skillRoot, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(`---
name: document-workbench
description: Generate native documents
---

Run ${SYNON_SKILL_DIR}/scripts/document_workbench.py before answering.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillRoot, "scripts", "document_workbench.py"),
		[]byte("print('DOCUMENT_WORKBENCH_READY')\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	loaded := skills.Load([]string{skillRoot})
	if loadErrors := loaded.LoadErrors(); len(loadErrors) != 0 {
		t.Fatalf("load skill errors = %#v", loadErrors)
	}
	selected := loaded.Skills()
	if len(selected) != 1 {
		t.Fatalf("selected skills = %#v", selected)
	}

	prepared, err := app.prepareAgentSkillRuntimeContexts(context.Background(), sessionID, selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 1 {
		t.Fatalf("prepared skills = %#v", prepared)
	}
	runtimeDirectory := filepath.ToSlash(filepath.Dir(prepared[0].Path))
	contextText, err := app.runtimeSkillContextFromSkills(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(contextText, "${SYNON_SKILL_DIR}") {
		t.Fatalf("first-turn skill context retained unresolved runtime placeholder: %s", contextText)
	}
	if !strings.Contains(contextText, "Base directory for this skill: "+runtimeDirectory) ||
		!strings.Contains(contextText, runtimeDirectory+"/scripts/document_workbench.py") {
		t.Fatalf("first-turn skill context lacks materialized runtime path %q: %s", runtimeDirectory, contextText)
	}
	if strings.Contains(contextText, filepath.ToSlash(skillRoot)) {
		t.Fatalf("first-turn skill context leaked source bundle path %q: %s", skillRoot, contextText)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(prepared[0].Path), "scripts", "document_workbench.py")); err != nil {
		t.Fatalf("materialized helper is unavailable: %v", err)
	}
}
