package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGeneratesAndChecksRealManifestFiles(t *testing.T) {
	source := newCLIBaselineFixture(t)
	target := filepath.Join(t.TempDir(), "v1.1-reuse-manifest.json")
	review := filepath.Join(t.TempDir(), "v1.1-reuse-review.md")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{
		"--source", source,
		"--target", target,
		"--review-target", review,
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run(generate) exit = %d, stderr = %s", exitCode, stderr.String())
	}
	for _, path := range []string{target, review} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s) error = %v", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("generated file %s is empty", path)
		}
	}
	if !strings.Contains(stdout.String(), "agents=14") || !strings.Contains(stdout.String(), "skills=69") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = run([]string{
		"--source", source,
		"--target", target,
		"--review-target", review,
		"--check",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run(check) exit = %d, stderr = %s", exitCode, stderr.String())
	}

	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("tamper target: %v", err)
	}
	stderr.Reset()
	exitCode = run([]string{
		"--source", source,
		"--target", target,
		"--review-target", review,
		"--check",
	}, &stdout, &stderr)
	if exitCode == 0 || !strings.Contains(stderr.String(), "reuse manifest drift") {
		t.Fatalf("run(tampered check) exit = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunRejectsMissingRequiredFlags(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(nil, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--source and --target are required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func newCLIBaselineFixture(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	for _, relative := range []string{
		".editorconfig",
		".gitattributes",
		".gitignore",
		"LICENSE",
		"README.md",
		"bunfig.toml",
		"package.json",
		"synonbiomed.config.toml",
	} {
		writeCLIFile(t, source, relative, relative+"\n")
	}
	for _, relative := range []string{
		".git/fixture",
		".github/fixture",
		".state/fixture",
		"apps/agent-go/fixture",
		"apps/desktop/fixture",
		"docs/fixture",
		"scripts/fixture",
		"scripts/__pycache__/fixture.pyc",
		"synon_llm_core/fixture",
		"tests/fixture",
		"runtime/assets/compute/fixture",
		"runtime/assets/drizzle/fixture",
		"runtime/assets/fonts/fixture",
		"runtime/assets/kernels/fixture",
		"runtime/assets/micromamba/fixture",
		"runtime/assets/seccomp/fixture",
		"runtime/assets/seed/fixture",
		"runtime/assets/sharp-runtime/fixture",
		"runtime/assets/vendor/fixture",
		"runtime/assets/web-dist/fixture",
		"runtime/assets/BUILD.json",
		"runtime/assets/skills/THIRD_PARTY_LICENSES.md",
		"runtime/server/orgsImportWorker.js",
		"runtime/server/sandbox/gitScanWorker.js",
		"runtime/server/sqliteWorkerEntry.js",
		"runtime/server/synonbiomed.bundle.js",
	} {
		writeCLIFile(t, source, relative, relative+"\n")
	}
	for index := 0; index < 14; index++ {
		writeCLIFile(t, source, fmt.Sprintf("runtime/assets/agents/agent-%02d/agent.md", index), "agent\n")
	}
	for index := 0; index < 69; index++ {
		name := fmt.Sprintf("skill-%02d", index)
		if index == 0 {
			name = "cheminfo-render"
		}
		writeCLIFile(t, source, "runtime/assets/skills/"+name+"/SKILL.md", "skill\n")
	}
	writeCLIFile(t, source, "runtime/assets/skills/cheminfo-render/__pycache__/kernel.pyc", "cache\n")
	for index := 0; index < 9; index++ {
		name := fmt.Sprintf("Tool%02d", index)
		if index == 0 {
			name = "WebSearchTool"
		}
		writeCLIFile(t, source, "runtime/tools/"+name+"/backend.mjs", "tool\n")
	}
	writeCLIFile(t, source, "runtime/tools/WebSearchTool/__pycache__/worker.pyc", "cache\n")
	for index := 0; index < 2; index++ {
		writeCLIFile(t, source, fmt.Sprintf("runtime/assets/mcp-servers/mcp-%02d/server.py", index), "mcp\n")
	}
	return source
}

func writeCLIFile(t *testing.T, root string, relative string, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", relative, err)
	}
}
