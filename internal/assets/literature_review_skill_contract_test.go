package assets_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestLiteratureReviewSkillUsesBroadQueryTracksAndAuditableSourceLedger(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed", "literature-review")
	content, err := os.ReadFile(filepath.Join(skillRoot, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, marker := range []string{
		"split the question into independent evidence tracks",
		"do not join every population, modality, comparator, endpoint, and date constraint",
		"zero-result or very small first page",
		"two to four evidence tracks",
		"merge and deduplicate by DOI, PMID, trial identifier, accession, or canonical URL",
		"editable source ledger alongside the report",
		"Publish the source ledger as a user-readable CSV",
		"A broad review delivers both the readable report and an editable source-ledger CSV",
		"Every included source-ledger row and cited identifier must come from an actual retrieved record",
		"Write delimited files with a real CSV/TSV writer",
		"Screen candidates before synthesis",
		"Search and connector results are a candidate pool, not the evidence set",
		"wrong route, indication, molecule class, intervention, endpoint, or date",
		"Aggregator and social-repository pages can help discovery but are not the publication venue",
		"Every source identifier cited in the report must resolve to an included ledger row",
		"Never turn search-result volume into evidence strength",
		"server-supplied current_date and retrieval_as_of",
		"never substitute the model knowledge cutoff or a stale compact-summary date",
		"A source class is determined by its authoritative record",
		"never invent weights, composite scores, evidence counts, maturity values, or safety grades",
		"Never use a short alias as an unrestricted substring",
		"screen_records(records, required_concepts, year_from, year_to)",
		"deduplicate the included set by DOI, PMID, trial identifier, patent number, accession, or canonical URL",
		"audit_evidence_ledger(included_rows, required_source_types=[...])",
		"no eligible record was found",
		"Use `web_search` for broad discovery, not as a proxy for reading",
		"Return every search and source-read result to the outer agent loop before choosing the next action",
		"run focused searches in both languages and merge by stable identifier or family",
		"Patent evidence is a separate source class",
		"independent claims, and the examples or data supporting the relevant use",
		"Count families separately from publications",
		"exact identifier lookup returns not found or unavailable",
		"Never keep the identifier as verified, attach a different URL, or relabel it as another source class",
		"Treat those files as the durable source of truth",
		"Build long reviews as durable artifacts",
		"Do not hold the entire report or table in an unsaved assistant draft",
		"Continue through as many later tool rounds as the unchanged task needs",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("literature-review contract is missing marker %q", marker)
		}
	}

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
	found := false
	for _, entry := range manifest.Files {
		if entry.Path != "literature-review/SKILL.md" {
			continue
		}
		found = true
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		if entry.Bytes != len(content) || entry.SHA256 != digest {
			t.Fatalf("literature-review manifest entry is stale")
		}
	}
	if !found {
		t.Fatal("literature-review is missing from the bundled skills manifest")
	}

	catalog := skills.Load([]string{skillRoot}, skills.LoadOptions{MaxBodyBytes: 20000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load literature-review Skill: %v", loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "literature-review" || len(loaded[0].CriticalConstraints) != 10 {
		t.Fatalf("unexpected literature-review catalog: %#v", loaded)
	}
	if strings.Contains(text, "web_research") {
		t.Fatal("literature-review still declares the retired compound model route")
	}
}
