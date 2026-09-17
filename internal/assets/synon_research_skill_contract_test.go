package assets_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestSynonResearchIsOneDiscoverableOrchestratorWithOnDemandReferences(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed", "synon-research")
	catalog := skills.Load([]string{skillRoot}, skills.LoadOptions{MaxBodyBytes: 20_000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) != 0 {
		t.Fatalf("load synon-research Skill: %#v", loadErrors)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "synon-research" {
		t.Fatalf("unexpected catalog: %#v", loaded)
	}
	fullCatalog := skills.Load([]string{filepath.Join(repositoryRoot, "skills", "synonbiomed")}, skills.LoadOptions{MaxBodyBytes: 1 << 20})
	if loadErrors := fullCatalog.LoadErrors(); len(loadErrors) != 0 {
		t.Fatalf("load full Skill catalog: %#v", loadErrors)
	}
	match := fullCatalog.Search("请深度评估一个肿瘤新药靶点并给出专业研究报告", 3)
	if len(match) == 0 || match[0].Name != "synon-research" {
		t.Fatalf("Chinese broad-research discovery = %#v", match)
	}
	english := fullCatalog.Search("deep life science target assessment with evidence synthesis", 3)
	if len(english) == 0 || english[0].Name != "synon-research" {
		t.Fatalf("English broad-research discovery = %#v", english)
	}
	body := loaded[0].Body
	for _, required := range []string{
		"references/source-selection.md",
		"references/evidence-synthesis.md",
		"synon-research",
		"web_search",
		"web_fetch",
		"patent_search",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("synon-research body is missing %q", required)
		}
	}
	if !slices.Contains(loaded[0].RequiredSkills, "mcp-synon-research") {
		t.Errorf("synon-research did not declare its dynamic connector dependency: %#v", loaded[0].RequiredSkills)
	}
	if len(body) > 2_500 {
		t.Fatalf("Synon-research entrypoint is too long for routine prompt use: %d bytes", len(body))
	}
	for _, prohibited := range []string{"USP1", "opentargets-skill", "clinicaltrials-skill", "web_research"} {
		if strings.Contains(body, prohibited) {
			t.Errorf("synon-research body hardcodes test/source-specific instruction %q", prohibited)
		}
	}
	for _, relative := range []string{
		filepath.Join("references", "source-selection.md"),
		filepath.Join("references", "evidence-synthesis.md"),
	} {
		content, err := os.ReadFile(filepath.Join(skillRoot, relative))
		if err != nil || len(content) < 500 {
			t.Errorf("on-demand reference %s is unavailable or empty: bytes=%d err=%v", relative, len(content), err)
		}
		if strings.Contains(body, string(content)) {
			t.Errorf("reference %s was duplicated into the always-loaded body", relative)
		}
	}
}

func TestSynonResearchIsAvailableToGeneralResearchMode(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	agentRoot := filepath.Join(repositoryRoot, "assets", "synonbiomed", "agents")
	var operon struct {
		Skills []string `json:"skills"`
	}
	raw, err := os.ReadFile(filepath.Join(agentRoot, "operon-skills.json"))
	if err != nil || json.Unmarshal(raw, &operon) != nil || !slices.Contains(operon.Skills, "synon-research") {
		t.Fatalf("OPERON Synon-research authority skills=%v err=%v", operon.Skills, err)
	}
}
