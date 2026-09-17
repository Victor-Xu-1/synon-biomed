package server

import (
	"context"
	"encoding/json"
	"runtime"
	"sort"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/harnesscontract"
	"synon-go/internal/skills"
)

func TestProductionRootSnapshotExposesCanonicalWebSearchByDefault(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir()})
	authority := app.agentRuntimeToolSchemasWithContextOptions(
		context.Background(), []string{"web_search", "web_fetch", "web_research"}, true,
	)
	schemas := partitionAgentRuntimeToolSchemasForModel(authority)
	for _, name := range []string{"web_search", "web_fetch"} {
		if !agentRuntimeToolSchemaNamed(schemas, name) {
			t.Fatalf("production root snapshot omitted %s: %#v", name, agentRuntimeToolSchemaNames(schemas))
		}
	}
	for _, alias := range []string{"WebSearch", "WebFetch"} {
		if agentRuntimeToolSchemaNamed(schemas, alias) {
			t.Fatalf("legacy web alias %s entered the production root snapshot", alias)
		}
	}
	foundHiddenResearch := false
	for _, schema := range authority {
		if schema.Name != "web_research" {
			continue
		}
		foundHiddenResearch = true
		if schema.Exposure != agentruntime.ToolExposureHidden ||
			!agentRuntimeToolSchemaHasCapability(schema, "research") {
			t.Fatalf("web_research authority metadata=%#v", schema)
		}
	}
	if !foundHiddenResearch {
		t.Fatal("full compatibility authority omitted hidden web_research")
	}
	for _, schema := range schemas {
		if schema.Name != "web_search" {
			continue
		}
		properties, _ := schema.Parameters["properties"].(map[string]any)
		if properties["query"] == nil || properties["query_variants"] == nil ||
			properties["published_after"] == nil || properties["published_before"] == nil ||
			properties["sort"] == nil || properties["provider_continuations"] == nil ||
			properties["human_description"] != nil {
			t.Fatalf("canonical web_search schema=%#v", schema.Parameters)
		}
		if !strings.Contains(schema.Description, "Chinese and English query variants") {
			t.Fatalf("canonical web_search description=%q", schema.Description)
		}
		return
	}
	t.Fatal("canonical web_search schema was not inspected")
}

func TestCanonicalRootContractDoesNotContainRetiredNames(t *testing.T) {
	for _, name := range harnesscontract.RootModelTools() {
		if retiredAgentRuntimeRequestedName(name) {
			t.Errorf("canonical root tool %s is also marked retired", name)
		}
	}
}

func TestSelectedSkillsExposeOnlyTheirExactGovernedTools(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "web_search"}, {Name: "web_research"}, {Name: "patent_search"},
		{Name: "binding_mode_analysis"}, {Name: "mcp__patents__search"},
	}
	selected := []skills.Skill{
		{Name: "literature-review", Tools: []string{"web_search", "web_research"}},
		{Name: "patent-search", Tools: []string{"patent_search", "mcp__patents__search"}},
	}
	active := partitionAgentRuntimeToolSchemasForModelWithSkills(schemas, selected)
	for _, name := range []string{"web_search", "patent_search"} {
		if !agentRuntimeToolSchemaNamed(active, name) {
			t.Fatalf("selected Skill tool %s missing from %#v", name, agentRuntimeToolSchemaNames(active))
		}
	}
	for _, name := range []string{"web_research", "binding_mode_analysis", "mcp__patents__search"} {
		if agentRuntimeToolSchemaNamed(active, name) {
			t.Fatalf("unselected or flattened tool %s entered %#v", name, agentRuntimeToolSchemaNames(active))
		}
	}
	rootOnly := partitionAgentRuntimeToolSchemasForModel(schemas)
	if agentRuntimeToolSchemaNamed(rootOnly, "web_research") || agentRuntimeToolSchemaNamed(rootOnly, "patent_search") {
		t.Fatalf("task-scoped tools leaked into root snapshot: %#v", agentRuntimeToolSchemaNames(rootOnly))
	}
}

func TestToolExposureMetadataIsTheSingleDeferredActivationAuthority(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "always_available", Exposure: agentruntime.ToolExposureDirect},
		{Name: "cross_source_reader", Exposure: agentruntime.ToolExposureDeferred, Capabilities: []string{"research"}},
		{Name: "internal_executor", Exposure: agentruntime.ToolExposureHidden},
	}
	root := partitionAgentRuntimeToolSchemasForModel(schemas)
	if !agentRuntimeToolSchemaNamed(root, "always_available") ||
		agentRuntimeToolSchemaNamed(root, "cross_source_reader") ||
		agentRuntimeToolSchemaNamed(root, "internal_executor") {
		t.Fatalf("root exposure=%#v", agentRuntimeToolSchemaNames(root))
	}
	selected := []skills.Skill{{Name: "source-workflow", Tools: []string{"cross_source_reader", "internal_executor"}}}
	active := partitionAgentRuntimeToolSchemasForModelWithSkills(schemas, selected)
	if !agentRuntimeToolSchemaNamed(active, "always_available") ||
		!agentRuntimeToolSchemaNamed(active, "cross_source_reader") ||
		agentRuntimeToolSchemaNamed(active, "internal_executor") {
		t.Fatalf("selected exposure=%#v", agentRuntimeToolSchemaNames(active))
	}
}

func TestRegisteredHiddenExposureCannotBePromotedByRuntimeSchema(t *testing.T) {
	active := partitionAgentRuntimeToolSchemasForModelWithSkills(
		[]agentruntime.ToolSchema{{
			Name: "web_research", Exposure: agentruntime.ToolExposureDeferred,
			Capabilities: []string{"research"},
		}},
		[]skills.Skill{{Name: "legacy-research", Tools: []string{"web_research"}}},
	)
	if agentRuntimeToolSchemaNamed(active, "web_research") {
		t.Fatalf("runtime schema promoted registered hidden Tool: %#v", active)
	}
}

func TestCanonicalToolAuthorityRejectsCompetingSchemas(t *testing.T) {
	schema := agentruntime.ToolSchema{
		Name: "analysis", Parameters: map[string]any{"type": "object"},
		Capabilities: []string{"runtime-execution"}, Exposure: agentruntime.ToolExposureDirect,
	}
	canonical, err := canonicalAgentRuntimeToolAuthority([]agentruntime.ToolSchema{schema, schema})
	if err != nil || len(canonical) != 1 {
		t.Fatalf("identical authority was not deduplicated: schemas=%#v err=%v", canonical, err)
	}
	conflicting := schema
	conflicting.Exposure = agentruntime.ToolExposureHidden
	if _, err := canonicalAgentRuntimeToolAuthority([]agentruntime.ToolSchema{schema, conflicting}); err == nil {
		t.Fatal("competing Tool schemas were silently accepted")
	}
}

func TestRuntimeMCPContextUsesLoadOnDemandBridge(t *testing.T) {
	schemas := []agentruntime.ToolSchema{{
		Name: "mcp__remote__echo", Description: "Echo remote MCP text for agent runtime.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"text": map[string]any{"type": "string"},
		}},
	}}
	context := runtimeMCPContext(schemas, nil)
	for _, required := range []string{"Invocation contract:", "mcp__remote__echo", "Preserve JSON Schema types exactly"} {
		if !strings.Contains(context, required) {
			t.Fatalf("MCP context missing %q: %q", required, context)
		}
	}
	if active := partitionAgentRuntimeToolSchemasForModel(schemas); agentRuntimeToolSchemaNamed(active, "mcp__remote__echo") {
		t.Fatalf("flattened MCP method entered the model root snapshot: %#v", agentRuntimeToolSchemaNames(active))
	}
}

func TestRuntimeMCPContextRanksSelectedSkillTools(t *testing.T) {
	schemas := []agentruntime.ToolSchema{{Name: "mcp__remote__echo", Description: "Echo remote MCP text for agent runtime."}}
	selected := []skills.Skill{{Name: "remote-browser", Tools: []string{"mcp__remote__echo"}}}
	context := runtimeMCPContext(schemas, selected)
	if !strings.Contains(context, "selectedBySkill=remote-browser") || !strings.Contains(context, "mcp__remote__echo") {
		t.Fatalf("selected MCP context lost skill provenance: %q", context)
	}
	if active := partitionAgentRuntimeToolSchemasForModelWithSkills(schemas, selected); agentRuntimeToolSchemaNamed(active, "mcp__remote__echo") {
		t.Fatalf("flattened selected MCP method entered the model snapshot: %#v", agentRuntimeToolSchemaNames(active))
	}
}

func TestSkillDeclaredToolsEnterRunnerAuthorityBeforeTaskScopedExposure(t *testing.T) {
	allowed := appendAvailableSkillDeclaredToolNames(
		[]string{"web_search", "skill"},
		[]string{"web_search", "skill", "web_research", "patent_search", "mcp__patents__search"},
		[]skills.Skill{
			{Name: "literature-review", Tools: []string{"web_research", "unavailable_tool"}},
			{Name: "patent-search", Tools: []string{"patent_search", "mcp__patents__search"}},
			{Name: "unrelated", Tools: []string{"unavailable_tool"}},
		},
		[]string{"literature-review", "patent-search"},
	)
	allowedSet := chatRunnerAllowedToolSet(allowed)
	for _, name := range []string{"web_search", "skill", "web_research", "patent_search"} {
		if _, exists := allowedSet[name]; !exists {
			t.Fatalf("Skill-declared runtime authority omitted %s: %#v", name, allowed)
		}
	}
	for _, name := range []string{"unavailable_tool", "mcp__patents__search"} {
		if _, exists := allowedSet[name]; exists {
			t.Fatalf("unavailable or flattened Skill tool %s entered authority: %#v", name, allowed)
		}
	}
}

func TestCanonicalWebSearchIsAdmittedWhileLegacyAliasIsRetired(t *testing.T) {
	if retiredAgentRuntimeRequestedName("web_search") {
		t.Fatal("canonical web_search is still marked retired")
	}
	if !retiredAgentRuntimeRequestedName("WebSearch") {
		t.Fatal("legacy WebSearch alias is no longer retired")
	}
	if retiredAgentRuntimeRequestedName("web_research") {
		t.Fatal("canonical compatibility web_research must remain callable by explicit non-model consumers")
	}
	if !retiredAgentRuntimeRequestedName("WebResearch") {
		t.Fatal("legacy WebResearch alias is no longer retired")
	}
}

func TestPartitionAgentRuntimeToolSchemasMatchesReferenceRootHarnessSet(t *testing.T) {
	want := []string{
		"ask_about_compute", "ask_user", "compute_details", "compute_provider", "delete_host_files",
		"edit_file", "fetch_article_fulltext", "generate_plan", "list_compute", "list_host_grants", "download_public_scientific_file",
		"manage_environments", "manage_packages", "python", "r", "read_file", "read_memory", "repl",
		"request_host_access", "request_network_access", "save_artifacts", "search_memory", "search_skills",
		"skill", "update_step_status", "wait_for_notification", "web_fetch", "web_search", "write_memory",
	}
	if runtime.GOOS == "windows" {
		want = append(want, "powershell")
	} else {
		want = append(want, "bash")
	}
	schemas := make([]agentruntime.ToolSchema, 0, len(want)+8)
	for _, name := range want {
		schemas = append(schemas, agentruntime.ToolSchema{Name: name})
	}
	for _, extra := range []string{
		"ToolSearch", "software_runtime", "VisualReview",
		"Agent", "WebSearch", "WebFetch", "binding_mode_analysis", "mcp__chemistry__lookup",
	} {
		schemas = append(schemas, agentruntime.ToolSchema{Name: extra})
	}
	active := partitionAgentRuntimeToolSchemasForModel(schemas)
	got := agentRuntimeToolSchemaNames(active)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("active=%#v want=%#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("active=%#v want=%#v", got, want)
		}
	}
}

func TestPartitionAgentRuntimeToolSchemasHidesLegacySearchAndKeepsBoundedCanonicalSet(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "ToolSearch"}, {Name: "ask_user"}, {Name: "Read"}, {Name: "Edit"},
		{Name: manageEnvironmentsToolName}, {Name: managePackagesToolName}, {Name: "python"}, {Name: "bash"},
		{Name: "save_artifacts"}, {Name: "VisualReview"},
		{Name: "WebSearch"}, {Name: "web_search"}, {Name: "patent_search", Description: "Search patent chemistry records."},
		{Name: "mcp__chemistry__lookup"},
	}
	for index := 0; index < 30; index++ {
		schemas = append(schemas, agentruntime.ToolSchema{Name: "unrelated_" + string(rune('a'+index))})
	}
	active := partitionAgentRuntimeToolSchemasForModel(schemas)
	if agentRuntimeToolSchemaNamed(active, "ToolSearch") {
		t.Fatalf("legacy ToolSearch remains model-visible: active=%#v", agentRuntimeToolSchemaNames(active))
	}
	for _, name := range []string{
		"ask_user", manageEnvironmentsToolName, managePackagesToolName, "python", "bash", "save_artifacts",
		"web_search",
	} {
		if !agentRuntimeToolSchemaNamed(active, name) {
			t.Fatalf("active tools missing %s: %#v", name, agentRuntimeToolSchemaNames(active))
		}
	}
	for _, removed := range []string{"ToolSearch", "Read", "Edit", "VisualReview", "WebSearch", "patent_search", "mcp__chemistry__lookup"} {
		if agentRuntimeToolSchemaNamed(active, removed) {
			t.Fatalf("legacy/non-reference tool %s remains visible", removed)
		}
	}
}

func TestPartitionAgentRuntimeToolSchemasDropsNonReferenceToolsWithoutDeferredBackdoor(t *testing.T) {
	schemas := []agentruntime.ToolSchema{{Name: "ToolSearch"}, {Name: "ask_user"}}
	for index := 0; index < 80; index++ {
		schemas = append(schemas, agentruntime.ToolSchema{Name: "tool_" + string(rune('a'+index))})
	}
	active := partitionAgentRuntimeToolSchemasForModel(schemas)
	if len(active) != 1 || agentRuntimeToolSchemaNamed(active, "ToolSearch") ||
		!agentRuntimeToolSchemaNamed(active, "ask_user") {
		t.Fatalf("active=%d names=%#v", len(active), agentRuntimeToolSchemaNames(active))
	}
}

func TestPartitionAgentRuntimeToolSchemasDoesNotTreatArbitraryCatalogAsModelAuthority(t *testing.T) {
	schemas := make([]agentruntime.ToolSchema, 0, 32)
	for index := 0; index < 32; index++ {
		schemas = append(schemas, agentruntime.ToolSchema{Name: "tool_" + string(rune('a'+index))})
	}
	active := partitionAgentRuntimeToolSchemasForModel(schemas)
	if len(active) != 0 {
		t.Fatalf("active=%d", len(active))
	}
	if _, err := json.Marshal(active); err != nil {
		t.Fatal(err)
	}
}

func TestPartitionAgentRuntimeToolSchemasKeepsCanonicalSourcesWithoutIntentPromotion(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "ToolSearch"}, {Name: "python"}, {Name: "WebSearch"}, {Name: "web_search"},
		{Name: "WebFetch"}, {Name: "web_fetch"},
	}
	for index := 0; index < 30; index++ {
		schemas = append(schemas, agentruntime.ToolSchema{Name: "unrelated_" + string(rune('a'+index))})
	}
	active := partitionAgentRuntimeToolSchemasForModel(schemas)
	for _, name := range []string{"web_fetch", "web_search"} {
		if !agentRuntimeToolSchemaNamed(active, name) {
			t.Fatalf("authoritative source tool %s was omitted: active=%#v", name,
				agentRuntimeToolSchemaNames(active))
		}
	}
	for _, alias := range []string{"WebSearch", "WebFetch"} {
		if agentRuntimeToolSchemaNamed(active, alias) {
			t.Fatalf("legacy source alias %s remained model-visible", alias)
		}
	}
}
