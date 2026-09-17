package assets_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestPatentSearchSkillUsesCurrentBoundedDownloadPath(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillPath := filepath.Join(repositoryRoot, "skills", "synonbiomed", "patent-search", "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, marker := range []string{
		`requests.get(url, timeout=(10, 120))`,
		`response.content.startswith(b"%PDF-")`,
		"write a path-free filename",
		"Call `save_artifacts`",
		"Do not refetch, rewrite, or rename",
		"Size discovery to the question rather than a fixed display count",
		"preserve every source/query receipt",
		"continue while the result reports truncation",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("patent download contract is missing marker %q", marker)
		}
	}
	if strings.Contains(text, "download_public_scientific_file") {
		t.Fatal("patent Skill still references the retired downloader")
	}
	if strings.Contains(text, "Start with 12 results") {
		t.Fatal("patent Skill still carries the retired fixed 12-result discovery window")
	}

	catalog := skills.Load([]string{filepath.Dir(skillPath)})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatal(loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 {
		t.Fatalf("unexpected patent-search catalog size: %d", len(loaded))
	}
	for _, required := range []string{"patent_search", "manage_environments", "python", "save_artifacts"} {
		if !containsString(loaded[0].Tools, required) {
			t.Errorf("patent-search Skill is missing allowed tool %q: %v", required, loaded[0].Tools)
		}
	}
}
