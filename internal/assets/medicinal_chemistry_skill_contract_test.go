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

func TestMedicinalChemistrySkillOwnsCanonicalRDKitAnalogGeneration(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed", "medicinal-chemistry-optimization")
	skillContent, err := os.ReadFile(filepath.Join(skillRoot, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	scriptContent, err := os.ReadFile(filepath.Join(skillRoot, "scripts", "rdkit_analog_generator.py"))
	if err != nil {
		t.Fatal(err)
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
	want := map[string][]byte{
		"medicinal-chemistry-optimization/SKILL.md":                          skillContent,
		"medicinal-chemistry-optimization/scripts/rdkit_analog_generator.py": scriptContent,
	}
	for path, content := range want {
		found := false
		for _, entry := range manifest.Files {
			if entry.Path != path {
				continue
			}
			found = true
			digest := fmt.Sprintf("%x", sha256.Sum256(content))
			if entry.Bytes != len(content) || entry.SHA256 != digest {
				t.Fatalf("manifest entry %s is stale", path)
			}
		}
		if !found {
			t.Fatalf("manifest entry is missing: %s", path)
		}
	}

	catalog := skills.Load([]string{skillRoot}, skills.LoadOptions{MaxBodyBytes: 12000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load medicinal chemistry Skill: %v", loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "medicinal-chemistry-optimization" {
		t.Fatalf("unexpected Skill catalog: %#v", loaded)
	}
	if !slices.Equal(loaded[0].PreferredExecutionAssets, []string{"scripts/rdkit_analog_generator.py"}) {
		t.Fatalf("preferred execution assets=%v", loaded[0].PreferredExecutionAssets)
	}
	for _, tool := range []string{"search_skills", "skill", "manage_environments", "bash", "read_file", "save_artifacts"} {
		if !slices.Contains(loaded[0].Tools, tool) {
			t.Errorf("canonical RDKit Skill is missing tool %q", tool)
		}
	}
	if slices.Contains(loaded[0].Tools, "python") {
		t.Fatal("canonical RDKit Skill still permits model-authored Python mutation loops")
	}
	if matches := catalog.SearchNames("RDKit analog generation ligand diversity", 3); len(matches) == 0 || matches[0] != loaded[0].Name {
		t.Fatalf("RDKit analog Skill is not discoverable: %v", matches)
	}
	for _, marker := range []string{
		`${SYNON_SKILL_DIR}/scripts/rdkit_analog_generator.py`, "--input-json", "--count 30",
		"MaxMin", "exactly the requested unique 3D molecules", "Do not hardcode", "complete verified parent heavy-atom graph",
		"machine-readable calculation outputs", "Lipinski rule of five", "pre-publication step", "not an automatic review after",
		"QED", "lipinski_violations", "same candidate ID and calculated values",
		"canonical isomeric SMILES<TAB>candidate_id", "header-only", "companion count differs",
		"condition generation on protein-pocket geometry", "accepts the",
	} {
		if !strings.Contains(string(skillContent), marker) {
			t.Errorf("Skill is missing canonical marker %q", marker)
		}
	}
	for _, marker := range []string{
		"rdFingerprintGenerator.GetMorganGenerator", "rdSimDivPickers.MaxMinPicker", "Chem.RWMol(Chem.CombineMols",
		"required_core_preserved", "unique_canonical_smiles", "three_dimensional_sdf", `"overall_pass"`, `"errors"`,
		"QED.qed", "lipinski_violations", "complete_property_table", "synon.rdkit-analog-generation.validation.v2",
		`"generation_class": "analog-enumeration"`, `"engine": "rdkit-analog-generator"`,
		`"kind": "parent-ligand"`, `"input_sha256"`,
	} {
		if !strings.Contains(string(scriptContent), marker) {
			t.Errorf("RDKit generator is missing validation marker %q", marker)
		}
	}
	if strings.Contains(string(scriptContent), `add_argument("--required-smarts"`) {
		t.Fatal("RDKit generator still exposes the redundant model-authored required-core path")
	}
	if strings.Contains(loaded[0].Description, "molecule-generation") || slices.Contains(loaded[0].Keywords, "分子生成") {
		t.Fatal("RDKit analog enumeration still advertises itself as a generic molecular generator")
	}
}
