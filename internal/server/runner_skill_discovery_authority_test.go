package server

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestRuntimeSkillDiscoveryDoesNotTurnLexicalScoreIntoAuthority(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	app := New(Options{FileRoot: t.TempDir()})
	app.skillCatalog = skills.Load([]string{filepath.Join(filepath.Dir(file), "..", "..", "skills", "synonbiomed")})
	authority := map[string]struct{}{"skill": {}, "search_skills": {}, "read_file": {}, "repl": {}, "python": {}, "web_search": {}, "web_fetch": {}, "save_artifacts": {}}
	for _, prompt := range []string{
		"请调研 DNA 聚合酶θ（POLQ）抑制剂的研发价值，生成一份专业报告和证据表。",
		"请深度评估一个靶点的公开研究和专利证据。",
		"请调研 JAK 抑制剂。",
	} {
		discovery := app.runtimeSkillDiscovery(prompt, nil, nil, nil, false, authority)
		if len(discovery.autoReference) != 0 {
			t.Errorf("lexical search loaded a binding Skill for %q: %v", prompt, discovery.autoReference[0].Name)
		}
		metadata := app.runtimeSkillCandidateContext(prompt, nil, nil, nil, false, authority)
		for _, name := range []string{"deep-literature-investigation", "literature-review", "patent-search"} {
			if !strings.Contains(metadata, name) {
				t.Errorf("catalog hid %s behind query wording", name)
			}
		}
	}
}

func TestRuntimeSkillDiscoveryRanksBroadBiomedicalResearchOrchestrator(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	app := New(Options{FileRoot: t.TempDir()})
	app.skillCatalog = skills.Load([]string{filepath.Join(filepath.Dir(file), "..", "..", "skills", "synonbiomed")})
	prompt := "请调研 DNA 聚合酶θ（POLQ）抑制剂的研发价值，生成一份专业报告和证据表。"
	ranked := app.skillCatalog.SearchActivationRanked(prompt, 8)
	if len(ranked) == 0 || ranked[0].Skill.Name != "synon-research" {
		t.Fatalf("broad biomedical research ranking=%#v", ranked)
	}
}

func TestRuntimeSkillActivationMetadataRoutesAcrossTaskDomains(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	app := New(Options{FileRoot: t.TempDir()})
	app.skillCatalog = skills.Load([]string{filepath.Join(filepath.Dir(file), "..", "..", "skills", "synonbiomed")})
	for _, test := range []struct {
		prompt string
		want   string
	}{
		{
			prompt: "请为难溶性口服小分子设计制剂开发和增溶策略。",
			want:   "formulation-development",
		},
		{
			prompt: "请评估暴露疗效和暴露毒性关系，并提出治疗药物监测采样策略。",
			want:   "dmpk-adme-strategy",
		},
		{
			prompt: "请设计表位特异性纳米抗体并进行 CDR 优化。",
			want:   "antibody-design-strategy",
		},
		{
			prompt: "Design an ICH stability protocol with storage conditions, time points, and a shelf-life proposal.",
			want:   "stability-shelf-life",
		},
		{
			prompt: "Audit an IND-enabling toxicology package covering safety pharmacology, genotoxicity, and reproductive toxicity.",
			want:   "nonclinical-safety-strategy",
		},
		{
			prompt: "Search patent families and Markush disclosures for a freedom-to-operate landscape.",
			want:   "patent-search",
		},
	} {
		ranked := app.skillCatalog.SearchActivationRanked(test.prompt, 4)
		if len(ranked) == 0 || ranked[0].Skill.Name != test.want {
			t.Errorf("activation ranking for %q=%#v, want %s", test.prompt, ranked, test.want)
		}
	}
}

// Keep the existing domain/constraint regression coverage, but use explicit
// selection as its authority instead of promoting a lexical score to a load.
func selectDiscoveredSkills(t *testing.T, app *Server, prompt string, names []string, authority map[string]struct{}) []skills.Skill {
	t.Helper()
	discovery := app.runtimeSkillDiscovery(prompt, nil, nil, nil, false, authority)
	if len(discovery.autoReference) != 0 {
		t.Fatal("discovery unexpectedly loaded Skill bodies")
	}
	metadata := app.runtimeSkillCandidateContext(prompt, nil, nil, nil, false, authority)
	for _, name := range names {
		if !strings.Contains(metadata, name) {
			t.Fatalf("selected Skill is not discoverable: %s", name)
		}
	}
	selected, err := app.runtimeSkillsByName(names, nil)
	if err != nil {
		t.Fatal(err)
	}
	return selected
}
