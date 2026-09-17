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

func TestBindingModeSkillUsesOneValidatedCoordinateDownload(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillPath := filepath.Join(repositoryRoot, "skills", "synonbiomed", "binding-mode-analysis", "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, marker := range []string{
		"download_public_scientific_file",
		"retaining its artifact version",
		"gemmi.read_structure",
		"not `read_cif`",
		"write the final literal bytes once",
		"save_artifacts",
		"docking_complex_ensemble.pdb",
		"docking_components.csv",
		"editable long-form CSV",
		"scripts/analyze_binding_pocket.py",
		"retains chain plus residue-number identity",
		"pocket_box.csv",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("binding-mode download contract is missing marker %q", marker)
		}
	}
	for _, forbidden := range []string{"edit_file", "web_fetch", "{{...}}"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("binding-mode Skill retains competing file route %q", forbidden)
		}
	}

	catalog := skills.Load([]string{filepath.Dir(skillPath)})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatal(loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 {
		t.Fatalf("unexpected binding-mode catalog size: %d", len(loaded))
	}
	if !slices.Equal(loaded[0].PreferredExecutionAssets, []string{"scripts/analyze_binding_pocket.py"}) {
		t.Fatalf("preferred execution assets=%#v", loaded[0].PreferredExecutionAssets)
	}
	for _, required := range []string{"repl", "download_public_scientific_file", "manage_environments", "python", "read_file", "save_artifacts"} {
		if !containsString(loaded[0].Tools, required) {
			t.Errorf("binding-mode Skill is missing allowed tool %q: %v", required, loaded[0].Tools)
		}
	}

	scriptPath := filepath.Join(filepath.Dir(skillPath), "scripts", "analyze_binding_pocket.py")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"def find_ligand(",
		"ligand selection is ambiguous",
		"residue_identity(",
		"protein_chain",
		"ligand heavy-atom bounding box plus explicit padding",
	} {
		if !strings.Contains(string(script), marker) {
			t.Errorf("binding-mode execution asset is missing %q", marker)
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
