package assets_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestSBDDPPIWorkflowIsASingleResponsibilityRouter(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed", "sbdd-ppi-workflow")
	content, err := os.ReadFile(filepath.Join(skillRoot, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skillRoot, "kernel.py")); !os.IsNotExist(err) {
		t.Fatalf("retired duplicate PPI kernel still exists: %v", err)
	}

	catalog := skills.Load([]string{skillRoot}, skills.LoadOptions{MaxBodyBytes: 12000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatal(loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "sbdd-ppi-workflow" {
		t.Fatalf("unexpected SBDD PPI catalog: %#v", loaded)
	}
	for _, tool := range []string{"search_skills", "skill", "repl", "download_public_scientific_file", "save_artifacts"} {
		if !slices.Contains(loaded[0].Tools, tool) {
			t.Errorf("PPI router is missing tool %q", tool)
		}
	}
	text := string(content)
	for _, marker := range []string{
		"drug-discovery-pipeline", "structure-based-molecule-generation", "medicinal-chemistry-optimization", "autodock-vina",
		"binding-mode-analysis", "chem-physical-properties", "cheminfo-render",
		"docking_complex_ensemble.pdb", "docking_components.csv", "exact identifier continuity",
		"explicitly selected deterministic analog-enumeration fallback", "not equivalent to a pocket-conditioned",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("PPI router is missing canonical delegation marker %q", marker)
		}
	}
	for _, forbidden := range []string{
		"download_alphafold_structure", "generate_dimer_by_symmetry", "parse_vina_log",
		"validate_molecule", "generate_interactive_viewer", "requests.get", "web_search(",
		"TARGET_CONFIG",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("PPI router retains competing implementation %q", forbidden)
		}
	}
}
