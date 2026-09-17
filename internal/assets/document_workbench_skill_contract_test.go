package assets_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestDocumentWorkbenchSkillIsWorkspaceBoundedAndUsesManagedExecution(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed", "document-workbench")
	manifestContent, err := os.ReadFile(filepath.Join(repositoryRoot, "assets", "synonbiomed", "skills.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	manifestFiles := make(map[string]struct {
		SHA256 string
		Bytes  int
	}, len(manifest.Files))
	for _, entry := range manifest.Files {
		manifestFiles[entry.Path] = struct {
			SHA256 string
			Bytes  int
		}{SHA256: entry.SHA256, Bytes: entry.Bytes}
	}
	requiredFiles := []string{
		"SKILL.md", "THIRD_PARTY_NOTICES.md", "requirements.lock",
		"evals/evals.json", "examples/deck.json", "examples/notebook.json",
		"examples/report.json", "examples/workbook.json", "references/contract.md",
		"scripts/document_workbench.py", "scripts/test_document_workbench.py",
	}
	for _, relative := range requiredFiles {
		content, err := os.ReadFile(filepath.Join(skillRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Errorf("required document-workbench file %s: %v", relative, err)
			continue
		}
		entry, found := manifestFiles["document-workbench/"+relative]
		if !found {
			t.Errorf("document-workbench/%s is absent from the skills manifest", relative)
			continue
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		if entry.Bytes != len(content) || entry.SHA256 != digest {
			t.Errorf("document-workbench/%s manifest entry is stale", relative)
		}
	}

	catalog := skills.Load([]string{skillRoot}, skills.LoadOptions{MaxBodyBytes: 12000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load document-workbench skill: %v", loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "document-workbench" {
		t.Fatalf("unexpected document-workbench catalog: %#v", loaded)
	}
	for _, tool := range []string{"search_skills", "skill", "manage_environments", "manage_packages", "python", "bash", "read_file", "edit_file", "save_artifacts"} {
		if !slices.Contains(loaded[0].Tools, tool) {
			t.Errorf("document-workbench tool dependency is missing: %s", tool)
		}
	}
	for _, retired := range []string{"software_runtime"} {
		if slices.Contains(loaded[0].Tools, retired) {
			t.Errorf("document-workbench still exposes split runtime tool: %s", retired)
		}
	}
	skillText, err := os.ReadFile(filepath.Join(skillRoot, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`manage_environments(mode="list"`, `manage_packages(..., use_pip=true)`,
		`${SYNON_SKILL_DIR}/scripts/document_workbench.py --help`, "at most one materially corrected call",
	} {
		if !strings.Contains(string(skillText), marker) {
			t.Errorf("document-workbench managed execution boundary is missing: %s", marker)
		}
	}
	for _, query := range []string{"create Word document", "edit Excel spreadsheet", "PowerPoint slides", "render PDF", "Jupyter notebook"} {
		matches := catalog.SearchNames(query, 5)
		if !slices.Contains(matches, "document-workbench") {
			t.Errorf("document-workbench is not discoverable for %q: %v", query, matches)
		}
	}

	scriptContent, err := os.ReadFile(filepath.Join(skillRoot, "scripts", "document_workbench.py"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContent)
	for _, marker := range []string{
		"def resolve_workspace_path(", "resolved.relative_to(root)",
		"def atomic_target(", "os.replace(temporary, output)",
		"MAX_ZIP_ENTRIES", "MAX_ZIP_UNCOMPRESSED", "MAX_ZIP_RATIO",
		"macro-enabled Office files are rejected", "notebook execution requires explicit --allow-execution",
		"shell=False", "preview-manifest.json",
	} {
		if !strings.Contains(script, marker) {
			t.Errorf("document-workbench safety or delivery marker is missing: %s", marker)
		}
	}
	for _, forbidden := range []string{"shell=True", "os.system(", "eval(", "exec("} {
		if strings.Contains(script, forbidden) {
			t.Errorf("document-workbench contains unsafe execution marker: %s", forbidden)
		}
	}

	testContent, err := os.ReadFile(filepath.Join(skillRoot, "scripts", "test_document_workbench.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"test_create_validate_inspect_edit_and_render_all_native_formats",
		"test_notebook_execution_requires_explicit_authorization",
		"test_rejects_macro_active_html_and_unsafe_packages",
		"test_rejects_paths_outside_workspace_and_symlink_escape",
		"test_rejects_unbounded_timeouts_and_render_dpi",
	} {
		if !strings.Contains(string(testContent), marker) {
			t.Errorf("document-workbench regression coverage is missing: %s", marker)
		}
	}
}
