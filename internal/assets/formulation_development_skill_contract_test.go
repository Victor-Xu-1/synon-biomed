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

func TestFormulationDevelopmentSkillPreservesEvidenceGatedDecisions(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed", "formulation-development")
	content, err := os.ReadFile(filepath.Join(skillRoot, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, marker := range []string{
		"Do not infer BCS permeability or class from lipophilicity alone",
		"A pKa difference is a screen-design input, not a selection result",
		"Amorphous dispersions, lipid systems, particle-size reduction, complexation",
		"Thermal process ranges are hypotheses",
		"Unit size and container fit require",
		"Design the early clinical presentation for dose flexibility",
		"claim-to-source table",
		"proposed screening conditions",
		"governing equation, units, sign and direction, limiting or boundary behavior",
		"For a monoprotic weak base, the ideal free-form relationship is S = S0 × (1 + 10^(pKa - pH))",
		"For a monoprotic weak acid, the corresponding ideal relationship is S = S0 × (1 + 10^(pH - pKa))",
		"do not publish a result and then start a separate automatic review turn",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("formulation-development contract is missing marker %q", marker)
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
		if entry.Path != "formulation-development/SKILL.md" {
			continue
		}
		found = true
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		if entry.Bytes != len(content) || entry.SHA256 != digest {
			t.Fatalf("formulation-development manifest entry is stale")
		}
	}
	if !found {
		t.Fatal("formulation-development is missing from the bundled skills manifest")
	}

	catalog := skills.Load([]string{skillRoot}, skills.LoadOptions{MaxBodyBytes: 12000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load formulation-development Skill: %v", loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "formulation-development" {
		t.Fatalf("unexpected formulation-development catalog: %#v", loaded)
	}
}
