package server

import (
	"context"
	"path/filepath"
	"testing"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAppendAvailableAgentFrameToolNamesAddsAvailableFrameTools(t *testing.T) {
	allowed := appendAvailableAgentFrameToolNames(
		[]string{"ToolSearch", "web_search"},
		[]string{
			"python",
			"repl",
			softwareRuntimeToolName,
			manageEnvironmentsToolName,
			managePackagesToolName,
			"read_file",
			"edit_file",
			"read_memory",
			"write_memory",
			"search_memory",
			"save_artifacts",
			"search_rcsb_structures",
			"download_rcsb_file",
			"mcp__pubmed__search_articles",
			"wait_for_notification",
			"web_search",
			"unrelated",
		},
	)
	for _, want := range []string{
		"python", "repl", manageEnvironmentsToolName, managePackagesToolName, "read_file", "edit_file",
		"read_memory", "write_memory", "search_memory",
		"save_artifacts", "search_rcsb_structures", "download_rcsb_file", "wait_for_notification",
		"mcp__pubmed__search_articles",
	} {
		if !containsAgentToolName(allowed, want) {
			t.Errorf("appended tools do not contain %q: %v", want, allowed)
		}
	}
	if containsAgentToolName(allowed, "unrelated") {
		t.Fatalf("non-frame tool was appended: %v", allowed)
	}
	if containsAgentToolName(allowed, softwareRuntimeToolName) {
		t.Fatalf("internal scientific-pack runtime was appended to the model tool set: %v", allowed)
	}
}

func TestAppendAvailableAgentFrameToolNamesRetainsDynamicMCPBehindRepl(t *testing.T) {
	universe := []agentruntime.ToolSchema{
		{Name: "ToolSearch"},
		{Name: "repl"},
		{Name: "mcp__pubmed__search_articles"},
	}
	allowed := appendAvailableAgentFrameToolNames([]string{"ToolSearch", "repl"}, agentRuntimeToolSchemaNames(universe))
	filtered := filterAgentRuntimeToolSchemas(universe, allowed)
	if !agentRuntimeToolSchemaNamed(filtered, "mcp__pubmed__search_articles") {
		t.Fatalf("internal runtime snapshot omitted connected MCP method: allowed=%v filtered=%v", allowed, agentRuntimeToolSchemaNames(filtered))
	}
	advertised := partitionAgentRuntimeToolSchemasForModel(filtered)
	if agentRuntimeToolSchemaNamed(advertised, "mcp__pubmed__search_articles") {
		t.Fatalf("dynamic MCP method leaked into model root tools: %v", agentRuntimeToolSchemaNames(advertised))
	}
}

func TestAppendAvailableAgentFrameToolNamesPreservesNoToolsSentinel(t *testing.T) {
	allowed := []string{noChatToolsAllowedSentinel}
	got := appendAvailableAgentFrameToolNames(allowed, []string{"python", "repl"})
	if len(got) != 1 || got[0] != noChatToolsAllowedSentinel {
		t.Fatalf("no-tools sentinel was expanded: %v", got)
	}
}

func TestAppendAvailableModelRootToolNamesCompletesPartialOperonAuthority(t *testing.T) {
	allowed := appendAvailableModelRootToolNames(
		[]string{"web_search", "skill"},
		[]string{
			"web_search", "skill", "repl", manageEnvironmentsToolName,
			"save_artifacts", "edit_file", "web_research", "unrelated",
		},
	)
	for _, want := range []string{"web_search", "skill", "repl", manageEnvironmentsToolName, "save_artifacts", "edit_file"} {
		if !containsAgentToolName(allowed, want) {
			t.Errorf("completed OPERON root authority omitted %q: %v", want, allowed)
		}
	}
	for _, excluded := range []string{"web_research", "unrelated"} {
		if containsAgentToolName(allowed, excluded) {
			t.Errorf("non-root tool %q entered the fixed OPERON base: %v", excluded, allowed)
		}
	}
	if got := appendAvailableModelRootToolNames(
		[]string{noChatToolsAllowedSentinel}, []string{"repl", "save_artifacts"},
	); len(got) != 1 || got[0] != noChatToolsAllowedSentinel {
		t.Fatalf("no-tools sentinel was expanded: %v", got)
	}
}

func TestAppendAvailableAgentFrameToolNamesHonorsExplicitExclusions(t *testing.T) {
	allowed := appendAvailableAgentFrameToolNames([]string{"ToolSearch"}, []string{"python", "repl", "read_file"})
	filtered := filterAgentAllowedTools(allowed, nil, []string{"python", "read_file"})
	if containsAgentToolName(filtered, "python") || containsAgentToolName(filtered, "read_file") {
		t.Fatalf("explicit exclusions were bypassed: %v", filtered)
	}
	if !containsAgentToolName(filtered, "repl") {
		t.Fatalf("non-excluded frame tool was removed: %v", filtered)
	}
}

func TestSessionRunnerAgentModelAuthorityDefersDynamicToolBinding(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project", UserID: "local", Name: "dynamic tool authority",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, FileRoot: t.TempDir(), SkillDirectories: []string{v11SkillsDir(t)}})
	session := sessionstore.Session{ID: "frame"}

	preflight, selected, err := app.applySessionRunnerAgentModelAuthority(
		session, normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected != "OPERON" {
		t.Fatalf("selected agent = %q, want OPERON", selected)
	}
	if len(preflight.AllowedTools) != 0 {
		t.Fatalf("model authority froze a partial tool allowlist: %v", preflight.AllowedTools)
	}
	universe := []agentruntime.ToolSchema{
		{Name: "ToolSearch"},
		{Name: "repl"},
		{Name: "mcp__pubmed__search_articles"},
	}
	bound, _, err := app.applySessionRunnerAgentProfile(session, preflight, agentRuntimeToolSchemaNames(universe))
	if err != nil {
		t.Fatal(err)
	}
	filtered := filterAgentRuntimeToolSchemas(universe, bound.AllowedTools)
	if !agentRuntimeToolSchemaNamed(filtered, "mcp__pubmed__search_articles") {
		t.Fatalf("final tool snapshot omitted dynamic MCP schema: allowed=%v filtered=%v", bound.AllowedTools, filtered)
	}

	catalogSkills := app.skillCatalog.Skills()
	catalogNames := make([]string, 0, len(catalogSkills))
	for _, skill := range catalogSkills {
		catalogNames = append(catalogNames, skill.Name)
	}
	requestedSkillTools := appendAvailableSkillDeclaredToolNames(
		nil, app.allRegisteredNames(), catalogSkills, catalogNames,
	)
	actualUniverse := app.agentRuntimeToolSchemasWithContextOptions(
		context.Background(), requestedSkillTools, false, session.ID,
	)
	actualBound, _, err := app.applySessionRunnerAgentProfile(
		session, preflight, agentRuntimeToolSchemaNames(actualUniverse),
	)
	if err != nil {
		t.Fatal(err)
	}
	actualAuthority := filterAgentRuntimeToolSchemas(actualUniverse, actualBound.AllowedTools)
	if agentRuntimeToolSchemaNamed(actualAuthority, "web_research") {
		t.Fatalf("OPERON authority retained compound web_research: requested=%v universe=%v allowed=%v authority=%v",
			requestedSkillTools, agentRuntimeToolSchemaNames(actualUniverse), actualBound.AllowedTools,
			agentRuntimeToolSchemaNames(actualAuthority))
	}
	for _, name := range []string{"web_search", "web_fetch"} {
		if !agentRuntimeToolSchemaNamed(actualAuthority, name) {
			t.Fatalf("OPERON authority omitted atomic source tool %s: %v", name, agentRuntimeToolSchemaNames(actualAuthority))
		}
	}
	run := &sessionRunnerChatRun{}
	run.addExecutedSkillNames("literature-review")
	gateway := serverAgentRuntimeToolGateway{
		server: app, taskRun: run, toolSchemas: actualAuthority, hasToolSnapshot: true,
	}
	additions := gateway.AdditionalModelToolSchemas(
		partitionAgentRuntimeToolSchemasForModel(actualAuthority),
	)
	if agentRuntimeToolSchemaNamed(additions, "web_research") {
		t.Fatalf("loaded literature Skill expanded compound web_research: %v",
			agentRuntimeToolSchemaNames(additions))
	}
}

func containsAgentToolName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
