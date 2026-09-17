package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/harnesscontract"
	"synon-go/internal/skills"
	"synon-go/internal/toolcontract"
)

func TestAuditSkillDependencyClosureCoversAllV11Skills(t *testing.T) {
	catalog := skills.Load([]string{filepath.Join("..", "..", "..", "skills", "synonbiomed")})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatalf("skill load errors = %#v", errors)
	}
	reg := Default()
	dynamicAgentTools := map[string]struct{}{
		"python":                          {},
		"r":                               {},
		"bash":                            {},
		"repl":                            {},
		"list_compute":                    {},
		"manage_environments":             {},
		"manage_packages":                 {},
		"read_file":                       {},
		"edit_file":                       {},
		"save_artifacts":                  {},
		"wait_for_notification":           {},
		"compute_provider":                {},
		"compute_provider_modal":          {},
		"compute_provider_inference":      {},
		"search_rcsb_structures":          {},
		"download_rcsb_file":              {},
		"download_public_scientific_file": {},
		"request_network_access":          {},
	}
	for _, name := range harnesscontract.RootModelTools() {
		if _, registered := reg.Get(name); !registered {
			dynamicAgentTools[name] = struct{}{}
		}
	}
	bundledMCPTools := loadBundledBioToolAuthority(t)
	report := AuditSkillDependencyClosure(catalog.Skills(), reg, RuntimeResolverFunc(func(name string, class DependencyClass) RuntimeStatus {
		switch class {
		case DependencyClassTool:
			tool, present := reg.Get(name)
			_, dynamic := dynamicAgentTools[name]
			_, bundledMCP := bundledMCPTools[name]
			route := "test-runtime-probe"
			if dynamic {
				route = "agent-runtime-dynamic-schema"
			} else if bundledMCP {
				route = "bundled-bio-tools-domain-schema"
			}
			return RuntimeStatus{
				Executable:        (present && tool.Executable) || dynamic || bundledMCP,
				AuthorityResolved: dynamic || bundledMCP,
				Route:             route,
			}
		case DependencyClassHostCapability:
			return RuntimeStatus{Executable: name == "host.llm", Route: "runner-model-client-complete"}
		case DependencyClassExternalRuntime:
			return RuntimeStatus{Executable: true, AuthorityResolved: true, Availability: "installable", Route: "managed-environment-supervisor"}
		default:
			return RuntimeStatus{}
		}
	}))

	wantSkills := len(catalog.Skills())
	const minimumSupportedSkillCount = 94
	if wantSkills < minimumSupportedSkillCount || report.TotalSkills != wantSkills || len(report.Skills) != wantSkills {
		t.Fatalf("skill coverage total=%d rows=%d", report.TotalSkills, len(report.Skills))
	}
	for _, required := range []string{"presentations", "single-cell-rna-analysis"} {
		if report.Skill(required).Skill == "" {
			t.Fatalf("required maintained skill %q is missing", required)
		}
	}
	if report.Skill("single-cell-rna-qc").Skill != "" {
		t.Fatal("retired single-cell-rna-qc skill remains alongside its maintained replacement")
	}
	if len(report.Errors) != 0 {
		t.Fatalf("audit errors = %#v", report.Errors)
	}
	if !report.Closed || report.MissingRequired != 0 {
		t.Fatalf("closure = %#v", report)
	}
	counts := map[DependencyClass]int{}
	skillsWithDependencies := 0
	for _, skill := range report.Skills {
		if len(skill.Dependencies) > 0 {
			skillsWithDependencies++
		}
		for _, dependency := range skill.Dependencies {
			counts[dependency.Class]++
		}
	}
	t.Logf("skills=%d skills_with_dependencies=%d tool_entries=%d host_capability_entries=%d external_runtime_entries=%d", wantSkills, skillsWithDependencies, counts[DependencyClassTool], counts[DependencyClassHostCapability], counts[DependencyClassExternalRuntime])

	deep := report.Skill("deep-literature-investigation")
	assertDependency(t, deep, "web_search", DependencyClassTool, true, true)
	assertDependency(t, deep, "web_fetch", DependencyClassTool, true, true)
	assertDependency(t, deep, "repl", DependencyClassTool, false, true)
	assertDependency(t, deep, "fetch_article_fulltext", DependencyClassTool, true, true)
	assertDependency(t, deep, "save_artifacts", DependencyClassTool, false, true)
	if dependency := deep.Dependency("host.llm"); dependency.Name != "" {
		t.Fatalf("deep literature still declares the retired competing host.llm route: %#v", dependency)
	}
	if _, registered := reg.Get("host.llm"); registered {
		t.Fatal("host.llm must remain a host capability, not a registered tool")
	}

	for _, name := range []string{"bash", "read_file", "edit_file", "ask_user"} {
		assertDependency(t, report.Skill("msa-search-nim"), name, DependencyClassTool, name == "ask_user", true)
	}
	if commands := report.Skill("complexa-slurm").ExternalRuntimeNames(); !slices.Contains(commands, "ssh") || !slices.Contains(commands, "jq") || !slices.Contains(commands, "python3") {
		t.Fatalf("complexa-slurm script commands = %#v", commands)
	}
	if commands := report.Skill("parabricks").ExternalRuntimeNames(); !slices.Contains(commands, "docker") || !slices.Contains(commands, "nvidia-smi") {
		t.Fatalf("parabricks script commands = %#v", commands)
	}
	if commands := report.Skill("skill-creator").ExternalRuntimeNames(); slices.Contains(commands, "llm") {
		t.Fatalf("skill-creator retained the competing model CLI: %#v", commands)
	}
	if dependency := report.Skill("skill-creator").Dependency("host.llm"); dependency.Name == "" || dependency.Class != DependencyClassHostCapability || !dependency.RuntimeExecutable {
		t.Fatalf("skill-creator host model authority = %#v", dependency)
	}
	if libraries := report.Skill("electronic-lab-notebook").ExternalRuntimeNames(); !slices.Contains(libraries, "sqlite3") || !slices.Contains(libraries, "os") || !slices.Contains(libraries, "datetime") {
		t.Fatalf("electronic-lab-notebook standard library dependencies = %#v", libraries)
	}
	for _, name := range []string{"read_file", "edit_file", "save_artifacts"} {
		dependency := report.Skill("document-workbench").Dependency(name)
		if dependency.Name == "" || dependency.Class != DependencyClassTool || !dependency.RuntimeExecutable || !dependency.AuthorityResolved || dependency.RuntimeRoute != "agent-runtime-dynamic-schema" {
			t.Fatalf("document-workbench dependency %q = %#v", name, dependency)
		}
	}
	if runtimes := report.Skill("document-workbench").ExternalRuntimeNames(); !slices.Contains(runtimes, "python3") || !slices.Contains(runtimes, "soffice") || !slices.Contains(runtimes, "libreoffice") {
		t.Fatalf("document-workbench external runtimes = %#v", runtimes)
	}
	for _, name := range []string{"python", "read_file", "search_skills", "skill", "ask_user"} {
		dependency := report.Skill("ngs-analysis-router").Dependency(name)
		if dependency.Name == "" || dependency.Class != DependencyClassTool || !dependency.RuntimeExecutable ||
			!dependency.RegistryPresent && !dependency.AuthorityResolved {
			t.Fatalf("ngs-analysis-router dependency %q = %#v", name, dependency)
		}
	}
}

func TestPackagedSkillsDeclareCanonicalToolNames(t *testing.T) {
	catalog := skills.Load([]string{filepath.Join("..", "..", "..", "skills", "synonbiomed")})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatalf("skill load errors = %#v", errors)
	}
	aliases := toolcontract.RuntimeAliases()
	for _, skill := range catalog.Skills() {
		seen := map[string]struct{}{}
		for _, name := range skill.Tools {
			if canonical, legacy := aliases[name]; legacy {
				t.Errorf("%s declares legacy tool %q; use %q", skill.Path, name, canonical)
			}
			normalized, ok := toolcontract.NormalizeRuntimeName(name)
			if !ok || normalized != name {
				t.Errorf("%s declares non-canonical tool %q", skill.Path, name)
			}
			if _, duplicate := seen[name]; duplicate {
				t.Errorf("%s declares duplicate tool %q", skill.Path, name)
			}
			seen[name] = struct{}{}
		}
	}
}

func loadBundledBioToolAuthority(t *testing.T) map[string]struct{} {
	t.Helper()
	path := filepath.Join("..", "..", "..", "assets", "optional", "mcp-servers", "bio-tools", "lib", "mcp_bio", "domains.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundled bio-tools domains: %v", err)
	}
	var domains map[string][]string
	if err := json.Unmarshal(raw, &domains); err != nil {
		t.Fatalf("decode bundled bio-tools domains: %v", err)
	}
	authority := make(map[string]struct{})
	for domain, tools := range domains {
		for _, tool := range tools {
			name := "mcp__" + strings.TrimSpace(domain) + "__" + strings.TrimSpace(tool)
			if name != "mcp____" {
				authority[name] = struct{}{}
			}
		}
	}
	return authority
}

func TestAuditSkillDependencyClosureRequiresExternalRuntimeAdmission(t *testing.T) {
	reg := Default()
	skill := skills.Skill{Name: "runtime-dependent", Path: "/skills/runtime-dependent/SKILL.md", Body: "pip install vina"}
	report := AuditSkillDependencyClosure([]skills.Skill{skill}, reg, RuntimeResolverFunc(func(string, DependencyClass) RuntimeStatus {
		return RuntimeStatus{Availability: "unavailable", Route: "managed-environment-supervisor"}
	}))
	if report.Closed || report.MissingRequired != 1 || report.Skills[0].Closed {
		t.Fatalf("unavailable runtime report=%#v", report)
	}
	dependency := report.Skills[0].Dependency("vina")
	if dependency.Class != DependencyClassExternalRuntime || !dependency.ClosureRequired || dependency.RuntimeExecutable {
		t.Fatalf("external runtime dependency=%#v", dependency)
	}
}

func TestAuditSkillDependencyClosureFailsClosedOnUnknownToolDeclaration(t *testing.T) {
	reg := Default()
	report := AuditSkillDependencyClosure([]skills.Skill{{
		Name: "unknown-tool", Path: "/skills/unknown-tool/SKILL.md",
		Body: "Standard tools: DefinitelyNotATool",
	}}, reg, RuntimeResolverFunc(func(string, DependencyClass) RuntimeStatus {
		return RuntimeStatus{Executable: true}
	}))
	if report.Closed || len(report.Errors) != 1 || len(report.Skills) != 1 || report.Skills[0].Closed {
		t.Fatalf("unknown dependency report = %#v", report)
	}
}

func TestAuditSkillDependencyClosureAcceptsExplicitDynamicToolAuthority(t *testing.T) {
	reg := Default()
	report := AuditSkillDependencyClosure([]skills.Skill{{
		Name: "dynamic-mcp", Path: "/skills/dynamic-mcp/SKILL.md", Tools: []string{"mcp__demo__query"},
	}}, reg, RuntimeResolverFunc(func(name string, class DependencyClass) RuntimeStatus {
		return RuntimeStatus{
			Executable:        name == "mcp__demo__query" && class == DependencyClassTool,
			AuthorityResolved: name == "mcp__demo__query" && class == DependencyClassTool,
			Route:             "test-dynamic-schema",
		}
	}))
	if !report.Closed || len(report.Errors) != 0 || len(report.Skills) != 1 || !report.Skills[0].Closed {
		t.Fatalf("dynamic dependency report = %#v", report)
	}
	dependency := report.Skills[0].Dependency("mcp__demo__query")
	if dependency.RegistryPresent || !dependency.AuthorityResolved || !dependency.RuntimeExecutable {
		t.Fatalf("dynamic dependency = %#v", dependency)
	}
}

func TestAuditRuntimeSkillDependencyClosureTreatsAllowedToolsAsOptionalRoutes(t *testing.T) {
	reg := Default()
	report := AuditRuntimeSkillDependencyClosure([]skills.Skill{{
		Name: "alternative-routes", Path: "/skills/alternative-routes/SKILL.md",
		Tools: []string{"bash", "repl"},
		Body:  "Choose the compatible execution route for the current task.",
	}}, reg, RuntimeResolverFunc(func(name string, class DependencyClass) RuntimeStatus {
		return RuntimeStatus{
			Executable:        name == "bash" && class == DependencyClassTool,
			AuthorityResolved: name == "bash" && class == DependencyClassTool,
		}
	}))
	if !report.Closed || report.MissingRequired != 0 || len(report.Skills) != 1 || !report.Skills[0].Closed {
		t.Fatalf("runtime optional-route report=%#v", report)
	}
	for _, name := range []string{"bash", "repl"} {
		dependency := report.Skills[0].Dependency(name)
		if dependency.Name == "" || dependency.ClosureRequired {
			t.Fatalf("allowed tool %q was treated as required: %#v", name, dependency)
		}
	}
}

func TestAuditRuntimeSkillDependencyClosureStillRequiresInvokedTool(t *testing.T) {
	reg := Default()
	report := AuditRuntimeSkillDependencyClosure([]skills.Skill{{
		Name: "required-route", Path: "/skills/required-route/SKILL.md",
		Tools: []string{"ask_user"},
		Body:  "Call `ask_user` before continuing.",
	}}, reg, RuntimeResolverFunc(func(string, DependencyClass) RuntimeStatus {
		return RuntimeStatus{Availability: "unavailable"}
	}))
	if report.Closed || report.MissingRequired != 1 || len(report.Skills) != 1 || report.Skills[0].Closed {
		t.Fatalf("runtime required-route report=%#v", report)
	}
	dependency := report.Skills[0].Dependency("ask_user")
	if !dependency.ClosureRequired || dependency.RuntimeExecutable {
		t.Fatalf("invoked tool was not kept required: %#v", dependency)
	}
}

func TestAuditSkillDependencyClosureDetectsLowercaseBodyInvocation(t *testing.T) {
	reg := Default()
	report := AuditSkillDependencyClosure([]skills.Skill{{
		Name: "body-only-write", Path: "/skills/body-only-write/SKILL.md",
		Body: `Call file_write({"path":"report.md","content":"result"}) to persist the report.`,
	}}, reg, RuntimeResolverFunc(func(name string, class DependencyClass) RuntimeStatus {
		return RuntimeStatus{Executable: false, AuthorityResolved: name == "file_write" && class == DependencyClassTool}
	}))
	if report.Closed || report.MissingRequired != 1 || len(report.Skills) != 1 || report.Skills[0].Closed {
		t.Fatalf("lowercase body invocation report = %#v", report)
	}
	dependency := report.Skills[0].Dependency("file_write")
	if dependency.Name != "file_write" || dependency.Class != DependencyClassTool || !dependency.RegistryPresent {
		t.Fatalf("lowercase body invocation dependency = %#v", dependency)
	}
}

func TestAuditSkillDependencyClosureDoesNotMatchQualifiedHostCallAsRegistryTool(t *testing.T) {
	reg := Default()
	report := AuditSkillDependencyClosure([]skills.Skill{{
		Name: "host-runtime", Path: "/skills/host-runtime/SKILL.md",
		Body: "Use host.mcp(server, method) through the pre-injected runtime host.\nCatalog methods include (`query_db`/`llm`/`mcp`/`list_frames`).",
	}}, reg, RuntimeResolverFunc(func(string, DependencyClass) RuntimeStatus {
		return RuntimeStatus{Availability: "unavailable"}
	}))
	if !report.Closed || report.MissingRequired != 0 || len(report.Errors) != 0 || len(report.Skills) != 1 || len(report.Skills[0].Dependencies) != 0 {
		t.Fatalf("qualified host call report = %#v", report)
	}
}

func assertDependency(t *testing.T, skill SkillDependencyStatus, name string, class DependencyClass, registryPresent bool, runtimeExecutable bool) {
	t.Helper()
	dependency := skill.Dependency(name)
	if dependency.Name == "" || dependency.Class != class || dependency.RegistryPresent != registryPresent || dependency.RuntimeExecutable != runtimeExecutable {
		t.Fatalf("%s dependency %q = %#v", skill.Skill, name, dependency)
	}
}
