package v11reuse

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const privateBaselineLicense = "SynonBiomed-v1.1-private-all-rights-reserved"

func BaselineRules(source string) ([]Rule, error) {
	root, err := canonicalRoot(source)
	if err != nil {
		return nil, err
	}

	rules := []Rule{
		stateRule("git-history", ".git", "source-control-state", "Git repository history is not runtime capability"),
		stateRule("local-process-state", ".state", "runtime-state", "v1.1 local logs and PID files"),
		componentRule("github-workflows", ".github", "engineering", "yaml", DirectCopy, ".github", false, false, "release-engineering", nil, "v1.1 source workflow inventory"),
		componentRule("agent-go-reference", "apps/agent-go", "reference-runtime", "go", MechanicalPort, "internal", false, false, "runtime", []string{"Go toolchain"}, "v1.1 agent-go README marks this as a partial non-main runtime"),
		componentRule("desktop-frontend", "apps/desktop", "frontend", "typescript", FrontendOwned, "external-frontend-project", false, false, "frontend", []string{"frontend toolchain"}, "v1.1 desktop source tree"),
		componentRule("baseline-documentation", "docs", "documentation", "markdown", DirectCopy, "docs/reference/synonbiomed-v1.1", false, false, "engineering", nil, "v1.1 source documentation"),
		componentRuleWithExcludes(
			"baseline-scripts",
			"scripts",
			[]string{"scripts/__pycache__"},
			"verification-script",
			"shell-javascript-python",
			DirectCopy,
			"scripts/compat/v1.1",
			false,
			false,
			"compatibility",
			[]string{"shell", "Python or JavaScript for selected probes"},
			"v1.1 smoke and setup script inventory",
		),
		stateRule("scripts-python-cache", "scripts/__pycache__", "generated-cache", "compiled Python cache is reproducible state"),
		componentRule("synon-llm-core", "synon_llm_core", "llm-runtime-adapter", "javascript", ThinAdapter, "assets/optional/synon-llm-core", true, true, "providers", []string{"managed JavaScript runtime until ported"}, "v1.1 LLM core protocol and injection source"),
		componentRule("baseline-tests", "tests", "compatibility-test", "javascript", DirectCopy, "testdata/v1.1/probes", false, false, "compatibility", []string{"test-only JavaScript runtime"}, "v1.1 real integration test inventory"),

		componentRule("server-org-import-worker", "runtime/server/orgsImportWorker.js", "runtime-worker", "javascript", ThinAdapter, "assets/optional/v1.1-workers/orgsImportWorker.js", true, true, "runtime", []string{"managed JavaScript runtime"}, "v1.1 main runtime worker asset"),
		componentRule("server-git-scan-worker", "runtime/server/sandbox/gitScanWorker.js", "runtime-worker", "javascript", ThinAdapter, "assets/optional/v1.1-workers/gitScanWorker.js", true, true, "runtime", []string{"managed JavaScript runtime", "sandbox policy"}, "v1.1 main runtime worker asset"),
		componentRule("server-sqlite-worker", "runtime/server/sqliteWorkerEntry.js", "persistence-worker", "javascript", MechanicalPort, "internal/persistence/sqlite", false, true, "persistence", []string{"SQLite"}, "v1.1 SQLite worker behavior"),
		componentRule("server-main-bundle", "runtime/server/synonbiomed.bundle.js", "main-runtime-bundle", "javascript-bun", BlackBoxReimplementation, "internal/compat/oracle", false, true, "compatibility", []string{"Bun required only to run baseline differential probes"}, "shipped v1.1 Bun main runtime"),

		componentRule("assets-build-metadata", "runtime/assets/BUILD.json", "asset-metadata", "json", DirectCopy, "assets/manifests/v1.1-build.json", true, true, "assets", nil, "v1.1 runtime asset build metadata"),
		componentRule("assets-skills-third-party-licenses", "runtime/assets/skills/THIRD_PARTY_LICENSES.md", "third-party-license", "markdown", DirectCopy, "assets/synonbiomed/skills/THIRD_PARTY_LICENSES.md", true, false, "legal", nil, "v1.1 bundled skill third-party license inventory"),
		componentRule("assets-compute", "runtime/assets/compute", "compute", "mixed", ThinAdapter, "assets/optional/compute", true, true, "compute", []string{"SSH and configured compute providers"}, "v1.1 compute asset tree"),
		componentRule("assets-drizzle", "runtime/assets/drizzle", "database-migration", "sql-javascript", MechanicalPort, "internal/migration/v1_1", false, true, "persistence", []string{"SQLite"}, "v1.1 migration asset tree"),
		componentRule("assets-fonts", "runtime/assets/fonts", "font", "binary", DirectCopy, "assets/fonts", true, true, "artifacts", nil, "v1.1 report and rendering font assets"),
		componentRule("assets-kernels", "runtime/assets/kernels", "kernel", "python", ThinAdapter, "assets/optional/kernels", true, true, "kernel", []string{"Python", "optional R, Bash, and Operon runtimes"}, "v1.1 kernel worker assets"),
		componentRule("assets-micromamba", "runtime/assets/micromamba", "environment-runtime", "native-binary", UpstreamIntegration, "assets/optional/micromamba", true, true, "environments", []string{"pinned micromamba distribution"}, "v1.1 micromamba asset tree"),
		componentRule("assets-seccomp", "runtime/assets/seccomp", "sandbox-runtime", "native-library", UpstreamIntegration, "assets/optional/seccomp", true, true, "kernel", []string{"Linux seccomp"}, "v1.1 seccomp asset tree"),
		componentRule("assets-seed", "runtime/assets/seed", "seed-data", "mixed", DirectCopy, "assets/seed", true, true, "runtime", nil, "v1.1 runtime seed assets"),
		componentRule("assets-sharp-runtime", "runtime/assets/sharp-runtime", "image-runtime", "native-library", UpstreamIntegration, "assets/optional/sharp-runtime", true, true, "artifacts", []string{"official Sharp runtime"}, "v1.1 image processing runtime assets"),
		componentRule("assets-vendor", "runtime/assets/vendor", "vendor-runtime", "mixed", UpstreamIntegration, "assets/optional/vendor", true, true, "assets", []string{"component-specific official distributions"}, "v1.1 vendor asset tree"),
		componentRule("assets-web-dist", "runtime/assets/web-dist", "frontend", "javascript-css-html", FrontendOwned, "external-frontend-project", false, true, "frontend", []string{"frontend build toolchain"}, "v1.1 shipped Web frontend bundle"),
	}

	rootFiles := []struct {
		id       string
		path     string
		category string
		language string
		decision Decision
		target   string
		release  bool
		wired    bool
		owner    string
		evidence string
	}{
		{id: "root-editorconfig", path: ".editorconfig", category: "engineering", language: "config", decision: DirectCopy, target: ".editorconfig", owner: "engineering", evidence: "v1.1 source metadata"},
		{id: "root-gitattributes", path: ".gitattributes", category: "engineering", language: "config", decision: DirectCopy, target: ".gitattributes", owner: "engineering", evidence: "v1.1 source metadata"},
		{id: "root-gitignore", path: ".gitignore", category: "engineering", language: "config", decision: DirectCopy, target: ".gitignore", owner: "engineering", evidence: "v1.1 source metadata"},
		{id: "root-license", path: "LICENSE", category: "license", language: "text", decision: DirectCopy, target: "LICENSE", release: true, owner: "legal", evidence: "v1.1 repository license"},
		{id: "root-readme", path: "README.md", category: "documentation", language: "markdown", decision: DirectCopy, target: "docs/reference/synonbiomed-v1.1/README.md", owner: "engineering", evidence: "v1.1 source documentation"},
		{id: "root-bun-config", path: "bunfig.toml", category: "baseline-build", language: "toml", decision: BlackBoxReimplementation, target: "docs/compatibility/v1.1-build", owner: "compatibility", evidence: "v1.1 Bun runtime configuration"},
		{id: "root-package", path: "package.json", category: "baseline-build", language: "json", decision: BlackBoxReimplementation, target: "docs/compatibility/v1.1-build", owner: "compatibility", evidence: "v1.1 package and script inventory"},
		{id: "root-runtime-config", path: "synonbiomed.config.toml", category: "runtime-config", language: "toml", decision: MechanicalPort, target: "internal/config", wired: true, owner: "runtime", evidence: "v1.1 runtime configuration surface"},
	}
	for _, rootFile := range rootFiles {
		rules = append(rules, componentRule(
			rootFile.id,
			rootFile.path,
			rootFile.category,
			rootFile.language,
			rootFile.decision,
			rootFile.target,
			rootFile.release,
			rootFile.wired,
			rootFile.owner,
			nil,
			rootFile.evidence,
		))
	}

	agentRules, err := expandModuleRules(root, "runtime/assets/agents", 14, func(name string) Rule {
		return componentRule(
			"agent-"+safeRuleID(name),
			"runtime/assets/agents/"+name,
			"agent",
			"markdown-json",
			DirectCopy,
			"assets/synonbiomed/agents/"+name,
			true,
			true,
			"agent-runtime",
			[]string{"agent runtime", "referenced tools, skills, and MCP servers"},
			"v1.1 bundled agent asset",
		)
	})
	if err != nil {
		return nil, err
	}
	rules = append(rules, agentRules...)

	skillRules, err := expandModuleRules(root, "runtime/assets/skills", 69, func(name string) Rule {
		rule := componentRule(
			"skill-"+safeRuleID(name),
			"runtime/assets/skills/"+name,
			"skill",
			"markdown-python-shell",
			DirectCopy,
			"assets/synonbiomed/skills/"+name,
			true,
			true,
			"skills",
			[]string{"skill loader", "referenced tools and optional native runtimes"},
			"v1.1 bundled skill asset",
		)
		if name == "cheminfo-render" && pathExists(root, "runtime/assets/skills/cheminfo-render/__pycache__") {
			rule.ExcludePaths = []string{"runtime/assets/skills/cheminfo-render/__pycache__"}
		}
		return rule
	})
	if err != nil {
		return nil, err
	}
	rules = append(rules, skillRules...)
	if pathExists(root, "runtime/assets/skills/cheminfo-render/__pycache__") {
		rules = append(rules, stateRule(
			"skill-cheminfo-render-python-cache",
			"runtime/assets/skills/cheminfo-render/__pycache__",
			"generated-cache",
			"compiled Python cache is reproducible state",
		))
	}

	toolRules, err := expandModuleRules(root, "runtime/tools", 9, func(name string) Rule {
		rule := componentRule(
			"tool-"+safeRuleID(name),
			"runtime/tools/"+name,
			"tool",
			"javascript-python",
			ThinAdapter,
			"assets/optional/v1.1-tools/"+name,
			true,
			true,
			"tools",
			[]string{"managed JavaScript or Python runtime", "network and tool permissions"},
			"v1.1 runtime tool implementation",
		)
		if name == "WebSearchTool" && pathExists(root, "runtime/tools/WebSearchTool/__pycache__") {
			rule.ExcludePaths = []string{"runtime/tools/WebSearchTool/__pycache__"}
		}
		return rule
	})
	if err != nil {
		return nil, err
	}
	rules = append(rules, toolRules...)
	if pathExists(root, "runtime/tools/WebSearchTool/__pycache__") {
		rules = append(rules, stateRule(
			"tool-websearch-python-cache",
			"runtime/tools/WebSearchTool/__pycache__",
			"generated-cache",
			"compiled Python cache is reproducible state",
		))
	}

	mcpRules, err := expandModuleRules(root, "runtime/assets/mcp-servers", 2, func(name string) Rule {
		return componentRule(
			"mcp-"+safeRuleID(name),
			"runtime/assets/mcp-servers/"+name,
			"mcp-server",
			"mixed",
			ThinAdapter,
			"assets/optional/mcp-servers/"+name,
			true,
			true,
			"mcp",
			[]string{"managed MCP process runtime"},
			"v1.1 bundled MCP server asset",
		)
	})
	if err != nil {
		return nil, err
	}
	rules = append(rules, mcpRules...)

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].ID < rules[j].ID
	})
	return rules, nil
}

func componentRule(
	id string,
	sourcePath string,
	category string,
	language string,
	decision Decision,
	targetPath string,
	releaseRequired bool,
	mainRuntimeWired bool,
	owner string,
	dependencies []string,
	evidence string,
) Rule {
	return componentRuleWithExcludes(
		id,
		sourcePath,
		nil,
		category,
		language,
		decision,
		targetPath,
		releaseRequired,
		mainRuntimeWired,
		owner,
		dependencies,
		evidence,
	)
}

func componentRuleWithExcludes(
	id string,
	sourcePath string,
	excludePaths []string,
	category string,
	language string,
	decision Decision,
	targetPath string,
	releaseRequired bool,
	mainRuntimeWired bool,
	owner string,
	dependencies []string,
	evidence string,
) Rule {
	licenses := []string{privateBaselineLicense}
	risks := []string{}
	if decision == UpstreamIntegration {
		licenses = append(licenses, "component-license-review-required")
		risks = append(risks, "replace copied payload with verified official distribution when possible")
	}
	if decision == ThinAdapter {
		risks = append(risks, "managed runtime dependency must be pinned, health-checked, and cancellable")
	}
	if decision == BlackBoxReimplementation {
		risks = append(risks, "behavior requires differential probes against the shipped main runtime")
	}
	return Rule{
		ID:                  id,
		SourcePath:          sourcePath,
		ExcludePaths:        excludePaths,
		Category:            category,
		Language:            language,
		Decision:            decision,
		TargetPath:          targetPath,
		RuntimeDependencies: dependencies,
		Evidence:            []string{evidence},
		Licenses:            licenses,
		Owner:               owner,
		Risks:               risks,
		MainRuntimeWired:    mainRuntimeWired,
		ReleaseRequired:     releaseRequired,
	}
}

func stateRule(id string, sourcePath string, category string, evidence string) Rule {
	return Rule{
		ID:                  id,
		SourcePath:          sourcePath,
		Category:            category,
		Language:            "generated-data",
		Decision:            RuntimeStateExcluded,
		RuntimeDependencies: []string{},
		Evidence:            []string{evidence},
		Licenses:            []string{"not-applicable-generated-state"},
		Owner:               "operations",
		Risks:               []string{},
		MainRuntimeWired:    false,
		ReleaseRequired:     false,
	}
}

func expandModuleRules(root string, container string, expected int, makeRule func(string) Rule) ([]Rule, error) {
	absolute := filepath.Join(root, filepath.FromSlash(container))
	directoryEntries, err := os.ReadDir(absolute)
	if err != nil {
		return nil, fmt.Errorf("read baseline module container %s: %w", container, err)
	}
	names := make([]string, 0, len(directoryEntries))
	for _, directoryEntry := range directoryEntries {
		if !directoryEntry.IsDir() {
			continue
		}
		names = append(names, directoryEntry.Name())
	}
	sort.Strings(names)
	if len(names) != expected {
		return nil, fmt.Errorf("baseline module count drift at %s: got %d, want %d", container, len(names), expected)
	}
	rules := make([]Rule, 0, len(names))
	for _, name := range names {
		rules = append(rules, makeRule(name))
	}
	return rules, nil
}

func pathExists(root string, relative string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
	return err == nil
}

var unsafeRuleIDCharacters = regexp.MustCompile("[^a-z0-9._-]+")

func safeRuleID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = unsafeRuleIDCharacters.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "unnamed"
	}
	return value
}
