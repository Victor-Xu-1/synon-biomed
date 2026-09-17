package assets_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestDeepLiteratureSkillExtendsCanonicalRetrievalWithoutCompetingRoute(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillPath := filepath.Join(repositoryRoot, "skills", "synonbiomed", "deep-literature-investigation", "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	catalog := skills.Load([]string{filepath.Dir(skillPath)})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) != 0 {
		t.Fatalf("load deep-literature Skill: %#v", loadErrors)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || len(loaded[0].RequiredSkills) != 1 || loaded[0].RequiredSkills[0] != "literature-review" {
		t.Fatalf("deep-literature dependency contract=%#v", loaded)
	}
	for _, marker := range []string{
		"The Harness composes the required `literature-review` contract before this one",
		"host.mcp.search",
		"provider totals, returned counts, duplicate counts, truncation, and stop reason",
		"zero or small page as a query diagnostic",
		"directly from retrieved normalized records",
		"not a fixed number of pages or records",
		"citation-graph step backward and forward",
		"Maintain a claim-evidence map before drafting",
		"Search for negative, null, failed, and non-replicating results",
		"exact supporting excerpt or structured response field path",
		"strongest direct evidence, independent corroboration, conflicting or limiting evidence",
		"screen the candidate pool",
		"wrong-route, wrong-indication, wrong-intervention, wrong-endpoint, out-of-window",
		"report may cite only included rows",
		"Resolve relative time windows from the server-supplied current date",
		"Keep long work durable",
		"one coherent report section or one bounded ledger batch",
		"sets no task-duration, attempt, source-count, or quality ceiling",
		"publish both with `save_artifacts`",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("deep-literature contract is missing marker %q", marker)
		}
	}
	for _, retired := range []string{
		"Use host.llm() to extract structured evidence",
		"stop after 3 consecutive empty pages",
		"web_research",
	} {
		if strings.Contains(text, retired) {
			t.Errorf("deep-literature contract still contains retired route %q", retired)
		}
	}
}
