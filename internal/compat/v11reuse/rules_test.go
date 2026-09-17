package v11reuse

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaselineRulesClassifyCompleteExpectedShape(t *testing.T) {
	source := newBaselineShapeFixture(t, 14, 69, 9, 2)

	rules, err := BaselineRules(source)
	if err != nil {
		t.Fatalf("BaselineRules() error = %v", err)
	}
	manifest, err := Build(source, rules)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if got := manifest.Summary.ByCategory["agent"]; got != 14 {
		t.Fatalf("agent entries = %d, want 14", got)
	}
	if got := manifest.Summary.ByCategory["skill"]; got != 69 {
		t.Fatalf("skill entries = %d, want 69", got)
	}
	if got := manifest.Summary.ByCategory["tool"]; got != 9 {
		t.Fatalf("tool entries = %d, want 9", got)
	}
	if got := manifest.Summary.ByCategory["mcp-server"]; got != 2 {
		t.Fatalf("mcp entries = %d, want 2", got)
	}
	if got := manifest.Summary.ByCategory["third-party-license"]; got != 1 {
		t.Fatalf("third-party license entries = %d, want 1", got)
	}
	if manifest.Summary.ExcludedEntries < 4 {
		t.Fatalf("excluded entries = %d, want git, state, and caches", manifest.Summary.ExcludedEntries)
	}
	if manifest.Summary.ReleaseEntries == 0 || manifest.Summary.WiredEntries == 0 {
		t.Fatalf("summary does not identify release/wired entries: %+v", manifest.Summary)
	}
}

func TestBaselineRulesRejectCountDrift(t *testing.T) {
	source := newBaselineShapeFixture(t, 13, 69, 9, 2)

	_, err := BaselineRules(source)
	if err == nil || !strings.Contains(err.Error(), "runtime/assets/agents") || !strings.Contains(err.Error(), "want 14") {
		t.Fatalf("BaselineRules() error = %v, want agent count drift", err)
	}
}

func newBaselineShapeFixture(t *testing.T, agentCount int, skillCount int, toolCount int, mcpCount int) string {
	t.Helper()
	source := t.TempDir()

	rootFiles := []string{
		".editorconfig",
		".gitattributes",
		".gitignore",
		"LICENSE",
		"README.md",
		"bunfig.toml",
		"package.json",
		"synonbiomed.config.toml",
	}
	for _, relative := range rootFiles {
		writeFile(t, filepath.Join(source, relative), relative+"\n")
	}

	directories := []string{
		".git",
		".github",
		".state",
		"apps/agent-go",
		"apps/desktop",
		"docs",
		"scripts",
		"scripts/__pycache__",
		"synon_llm_core",
		"tests",
		"runtime/assets/compute",
		"runtime/assets/drizzle",
		"runtime/assets/fonts",
		"runtime/assets/kernels",
		"runtime/assets/micromamba",
		"runtime/assets/seccomp",
		"runtime/assets/seed",
		"runtime/assets/sharp-runtime",
		"runtime/assets/vendor",
		"runtime/assets/web-dist",
	}
	for _, relative := range directories {
		writeFile(t, filepath.Join(source, relative, "fixture.txt"), relative+"\n")
	}
	writeFile(t, filepath.Join(source, "runtime", "assets", "BUILD.json"), "{}\n")
	writeFile(
		t,
		filepath.Join(source, "runtime", "assets", "skills", "THIRD_PARTY_LICENSES.md"),
		"third-party licenses\n",
	)

	serverFiles := []string{
		"runtime/server/orgsImportWorker.js",
		"runtime/server/sandbox/gitScanWorker.js",
		"runtime/server/sqliteWorkerEntry.js",
		"runtime/server/synonbiomed.bundle.js",
	}
	for _, relative := range serverFiles {
		writeFile(t, filepath.Join(source, relative), relative+"\n")
	}

	for index := 0; index < agentCount; index++ {
		writeFile(
			t,
			filepath.Join(source, "runtime", "assets", "agents", fmt.Sprintf("agent-%02d", index), "agent.md"),
			"agent\n",
		)
	}

	skillNames := make([]string, 0, skillCount)
	if skillCount > 0 {
		skillNames = append(skillNames, "cheminfo-render")
	}
	for index := len(skillNames); index < skillCount; index++ {
		skillNames = append(skillNames, fmt.Sprintf("skill-%02d", index))
	}
	for _, name := range skillNames {
		writeFile(t, filepath.Join(source, "runtime", "assets", "skills", name, "SKILL.md"), "skill\n")
	}
	if skillCount > 0 {
		writeFile(
			t,
			filepath.Join(source, "runtime", "assets", "skills", "cheminfo-render", "__pycache__", "kernel.pyc"),
			"cache\n",
		)
	}

	toolNames := make([]string, 0, toolCount)
	if toolCount > 0 {
		toolNames = append(toolNames, "WebSearchTool")
	}
	for index := len(toolNames); index < toolCount; index++ {
		toolNames = append(toolNames, fmt.Sprintf("Tool%02d", index))
	}
	for _, name := range toolNames {
		writeFile(t, filepath.Join(source, "runtime", "tools", name, "backend.mjs"), "tool\n")
	}
	if toolCount > 0 {
		writeFile(
			t,
			filepath.Join(source, "runtime", "tools", "WebSearchTool", "__pycache__", "worker.pyc"),
			"cache\n",
		)
	}

	for index := 0; index < mcpCount; index++ {
		writeFile(
			t,
			filepath.Join(source, "runtime", "assets", "mcp-servers", fmt.Sprintf("mcp-%02d", index), "server.py"),
			"mcp\n",
		)
	}

	return source
}
