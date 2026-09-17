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

func TestStructureBasedMoleculeGenerationRunsProfessionalRouteBeforeFallbackChoice(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillsRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed")
	skillPath := filepath.Join(skillsRoot, "structure-based-molecule-generation", "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}

	manifestContent, err := os.ReadFile(filepath.Join(repositoryRoot, "assets", "synonbiomed", "skills.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Skills []string `json:"skills"`
		Files  []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(manifest.Skills, "structure-based-molecule-generation") {
		t.Fatal("structure-based molecular generation router is missing from the bundled manifest")
	}
	manifestPaths := map[string][]byte{
		"structure-based-molecule-generation/SKILL.md":                         content,
		"structure-based-molecule-generation/references/generation-methods.md": []byte(mustRead(t, filepath.Join(skillsRoot, "structure-based-molecule-generation", "references", "generation-methods.md"))),
	}
	found := map[string]bool{}
	for _, entry := range manifest.Files {
		manifestBytes, expected := manifestPaths[entry.Path]
		if !expected {
			continue
		}
		found[entry.Path] = true
		digest := fmt.Sprintf("%x", sha256.Sum256(manifestBytes))
		if entry.Bytes != len(manifestBytes) || entry.SHA256 != digest {
			t.Errorf("%s manifest entry is stale", entry.Path)
		}
	}
	for manifestPath := range manifestPaths {
		if !found[manifestPath] {
			t.Errorf("%s is missing from the manifest", manifestPath)
		}
	}

	catalog := skills.Load([]string{skillsRoot}, skills.LoadOptions{MaxBodyBytes: 12000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load bundled Skills: %v", loadErrors[0].Err)
	}
	for _, query := range []string{
		"AI molecule generation conditioned on a protein pocket",
		"基于蛋白口袋的 AI 分子设计与结构导向生成",
	} {
		matches := catalog.Search(query, 5)
		if len(matches) == 0 || matches[0].Name != "structure-based-molecule-generation" {
			t.Fatalf("query %q routed to %#v", query, matches)
		}
		for _, match := range matches[:min(3, len(matches))] {
			if match.Name == "medicinal-chemistry-optimization" && matches[0].Name == match.Name {
				t.Fatalf("query %q silently routed pocket generation to RDKit", query)
			}
		}
	}

	var loaded skills.Skill
	for _, candidate := range catalog.Skills() {
		if candidate.Name == "structure-based-molecule-generation" {
			loaded = candidate
			break
		}
	}
	if loaded.Name == "" {
		t.Fatal("structure-based molecular generation Skill is unavailable")
	}
	if !slices.Contains(loaded.RequiredCapabilities, "pocket-conditioned-molecule-generation") {
		t.Fatalf("structure-based molecular generation capability contract=%v", loaded.RequiredCapabilities)
	}
	renderedContext := catalog.BuildSkillContext([]skills.Skill{loaded}, 0, skillsRoot)
	for _, marker := range []string{
		"## Decision snapshot",
		"PocketXMol",
		"CPU, memory",
		"GPU/VRAM",
		"Do not write only",
	} {
		if !strings.Contains(renderedContext, marker) {
			t.Errorf("runtime-rendered generation guidance is missing %q", marker)
		}
	}
	for _, tool := range []string{
		"search_skills", "skill", "ask_user", "repl", "web_search", "list_compute",
		"manage_environments", "manage_packages", "bash", "download_public_scientific_file", "save_artifacts",
	} {
		if !slices.Contains(loaded.Tools, tool) {
			t.Errorf("structure-based molecular generation Skill is missing tool %q", tool)
		}
	}
	criticalConstraints := strings.Join(loaded.CriticalConstraints, "\n")
	for _, marker := range []string{
		"establish a live professional generation route",
		"distinct evidence lanes",
		"chain-aware non-polymer ligand receipt",
		"never accept a center obtained by averaging every atom",
		"never downgrade silently",
		"Present three to four concrete routes",
		"generic dependency bundle is not a pocket-conditioned molecular generator",
		"observed or official CPU, memory, and GPU/VRAM",
		"conversation language",
		"no executable kernel.py", "never import an undocumented module",
	} {
		if !strings.Contains(criticalConstraints, marker) {
			t.Errorf("professional generation critical constraints are missing %q", marker)
		}
	}
	combinedInstructions := strings.Join(strings.Fields(
		string(content)+
			mustRead(t, filepath.Join(skillsRoot, "structure-based-molecule-generation", "references", "generation-methods.md"))+
			mustRead(t, filepath.Join(skillsRoot, "capability-acquisition", "SKILL.md")),
	), " ")
	for _, marker := range []string{
		"Route a structure-conditioned generation request by capability, not by a fixed",
		"pocket_native_3d", "pocket_substructure", "ligand_scaffold_conditioned",
		"current machine resources", "CPU/GPU/memory/disk", "Reuse a verified immutable engine",
		"real readiness decision", "A clone, environment creation, `--help`, or zero-exit setup command is not a generation result",
		"Only after no viable professional route remains", "use `ask_user`", "downgrade silently",
		"deterministic RDKit enumeration",
		"Ask at a material route choice", "three to four concrete routes", "Recommend one route",
		"use `manage_environments` in `preflight` mode", "provider list alone is not a local hardware inventory",
		"Do not duplicate the same options in a prose table",
		"main benefit", "scientific limitation", "expected editable structures",
		"Decision snapshot", "PocketXMol", "DiffSBDD", "Pocket2Mol", "TargetDiff",
		"Resource facts for an informed choice", "observed or official resource facts",
		"exact receptor and pocket input hashes", "model or service identity", "actual generation class",
		"binding-mode-analysis", "passing chain-aware pocket receipt",
	} {
		if !strings.Contains(combinedInstructions, strings.Join(strings.Fields(marker), " ")) {
			t.Errorf("dynamic generation route is missing marker %q", marker)
		}
	}
	generationBody := normalizedSkillBody(t, string(content))
	assertTermsFollowGate(t, generationBody, "Only after no viable professional route remains", []string{
		"If the user selects deterministic RDKit enumeration", "load `medicinal-chemistry-optimization`",
	})
	acquisitionBody := normalizedSkillBody(
		t, mustRead(t, filepath.Join(skillsRoot, "capability-acquisition", "SKILL.md")),
	)
	assertTermsFollowGate(t, acquisitionBody, "Only after no professional route can be executed", []string{
		"scientifically weaker fallback",
	})
	for _, forbidden := range []string{"CRBN", "KRAS", "12BP", "6GJ7", "Candidate generation belongs only"} {
		if strings.Contains(string(content), forbidden) {
			t.Errorf("structure-based generation router contains task-specific hardcoding %q", forbidden)
		}
	}
}

func normalizedSkillBody(t *testing.T, content string) string {
	t.Helper()
	parts := strings.SplitN(content, "---", 3)
	if len(parts) != 3 {
		t.Fatal("Skill frontmatter boundary is invalid")
	}
	return strings.Join(strings.Fields(parts[2]), " ")
}

func assertTermsFollowGate(t *testing.T, body, gate string, terms []string) {
	t.Helper()
	gateIndex := strings.Index(body, gate)
	if gateIndex < 0 {
		t.Fatalf("professional readiness gate %q is missing", gate)
	}
	for _, term := range terms {
		termIndex := strings.Index(body, term)
		if termIndex < 0 {
			t.Errorf("post-gate term %q is missing", term)
		} else if termIndex < gateIndex {
			t.Errorf("term %q appears before professional readiness evidence gate", term)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
