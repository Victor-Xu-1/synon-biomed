package assets_test

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDrugDiscoveryAssemblerAcceptsPocketGeneratorProperties(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	assembler := filepath.Join(
		repositoryRoot, "skills", "synonbiomed", "drug-discovery-pipeline", "scripts", "assemble_docking_results.py",
	)
	workspace := t.TempDir()
	writeFixture(t, workspace, "pocket_candidates.csv", `candidate_id,canonical_smiles,molecular_weight,clogp,tpsa,qed,hbd,hba,rotatable_bonds,lipinski_violations,generator_score,generator_score_kind
POCKET-001,CCO,46.0419,-0.0014,20.23,0.4068,1,1,0,0,0.83,pocket-model-confidence
POCKET-002,CCN,45.0578,-0.0350,26.02,0.4112,1,1,0,0,0.79,pocket-model-confidence
`)
	writeFixture(t, workspace, "pocket_candidates.smi", "CCO\tPOCKET-001\nCCN\tPOCKET-002\n")
	writeFixture(t, workspace, "pocket_candidates.sdf", `POCKET-001
fixture

  0  0  0  0  0  0  0  0  0  0999 V2000
M  END
>  <candidate_id>
POCKET-001

>  <canonical_smiles>
CCO

$$$$
POCKET-002
fixture

  0  0  0  0  0  0  0  0  0  0999 V2000
M  END
>  <candidate_id>
POCKET-002

>  <canonical_smiles>
CCN

$$$$
`)
	writeFixture(t, workspace, "docking_scores.csv", `ligand_id,best_affinity_kcal_mol,mode_count,repeat_count,poses_per_candidate,primary_pose_run,primary_pose_mode,reference_centroid_distance_angstrom,reference_axis_cosine,primary_pose_selection,receptor_sha256,center_x,center_y,center_z,size_x,size_y,size_z,seed,exhaustiveness,num_modes,rank
POCKET-001,-8.4,27,3,1,2,1,0.8,0.95,best_affinity_then_reference_geometry_tiebreak,aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa,1,2,3,20,20,20,42,8,9,1
POCKET-002,-7.9,27,3,1,1,3,1.1,0.90,best_affinity_then_reference_geometry_tiebreak,aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa,1,2,3,20,20,20,42,8,9,2
`)
	writeFixture(t, workspace, "generation_validation.json", `{
  "schema": "synon.pocket-generation.validation.v1",
  "overall_pass": true,
  "checks": {"requested_count": true, "valid_3d_coordinates": true},
  "provenance": {
    "generation_class": "pocket-conditioned-model",
    "engine": "fixture-pocket-diffusion",
    "provider": "local-managed-environment",
    "engine_version": "1.2.3",
    "conditioning": {
      "kind": "protein-pocket",
      "input_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    }
  }
}`)
	writeFixture(t, workspace, "docking_validation.json", `{
  "schema": "synon.execution-pack-validation.v4",
  "overall_pass": true,
  "sampling": {
    "primary_selection": "minimum_affinity_then_reference_geometry_on_exact_ties_else_stable_run_mode",
    "observed_selection_bases": ["best_affinity_then_reference_geometry_tiebreak"]
  },
  "complex_ensemble": {
    "selection_contract": "minimum_affinity_then_reference_geometry_on_exact_ties_else_stable_run_mode",
    "observed_selection_bases": ["best_affinity_then_reference_geometry_tiebreak"]
  }
}`)

	command := exec.Command(
		"python3", assembler,
		"--properties", "pocket_candidates.csv",
		"--structures-sdf", "pocket_candidates.sdf",
		"--structures-smiles", "pocket_candidates.smi",
		"--docking", "docking_scores.csv",
		"--generation-validation", "generation_validation.json",
		"--docking-validation", "docking_validation.json",
		"--target-label", "Generic pocket target",
		"--structure-id", "fixture-structure",
		"--reference-ligand", "REF",
		"--report-language", "en-US",
		"--output-dir", "final_results",
	)
	command.Dir = workspace
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run generator-agnostic assembler: %v\n%s", err, output)
	}

	rankingFile, err := os.Open(filepath.Join(workspace, "final_results", "candidate_ranking.csv"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(rankingFile).ReadAll()
	closeErr := rankingFile.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read ranking CSV: read=%v close=%v", err, closeErr)
	}
	if len(rows) != 3 {
		t.Fatalf("ranking rows=%d want=3", len(rows))
	}
	header := make(map[string]int, len(rows[0]))
	for index, name := range rows[0] {
		header[name] = index
	}
	for _, column := range []string{
		"generation_class", "generation_engine", "generation_provider", "generation_version",
		"conditioning_kind", "conditioning_sha256", "parent_tanimoto", "murcko_scaffold",
		"generator_score", "generator_score_kind", "repeat_count", "poses_per_candidate",
		"primary_pose_run", "primary_pose_mode", "reference_centroid_distance_angstrom",
		"reference_axis_cosine", "primary_pose_selection",
	} {
		if _, found := header[column]; !found {
			t.Errorf("ranking is missing column %q", column)
		}
	}
	if rows[1][header["generation_class"]] != "pocket-conditioned-model" ||
		rows[1][header["generation_engine"]] != "fixture-pocket-diffusion" ||
		rows[1][header["conditioning_kind"]] != "protein-pocket" {
		t.Fatalf("ranking lost professional generator provenance: %#v", rows[1])
	}
	if rows[1][header["parent_tanimoto"]] != "" || rows[1][header["murcko_scaffold"]] != "" {
		t.Fatalf("de novo generator optional parent fields were invented: %#v", rows[1])
	}
	if rows[1][header["repeat_count"]] != "3" || rows[1][header["poses_per_candidate"]] != "1" {
		t.Fatalf("ranking lost repeated-sampling primary-pose contract: %#v", rows[1])
	}

	var summary struct {
		Schema                string            `json:"schema"`
		GenerationProvenance  map[string]string `json:"generation_provenance"`
		StructureSetIntegrity struct {
			SDFCount             int  `json:"sdf_count"`
			SMILESCount          int  `json:"smiles_count"`
			CanonicalSMILESMatch bool `json:"canonical_smiles_match"`
		} `json:"structure_set_integrity"`
		Docking struct {
			Method struct {
				SelectionContract      string   `json:"selection_contract"`
				ObservedSelectionBases []string `json:"observed_selection_bases"`
			} `json:"method"`
		} `json:"docking"`
	}
	summaryContent, err := os.ReadFile(filepath.Join(workspace, "final_results", "results_summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(summaryContent, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Schema != "synon.structure-guided-candidate-summary.v4" ||
		summary.GenerationProvenance["generation_engine"] != "fixture-pocket-diffusion" ||
		summary.GenerationProvenance["conditioning_sha256"] != strings.Repeat("b", 64) {
		t.Fatalf("summary provenance=%#v schema=%q", summary.GenerationProvenance, summary.Schema)
	}
	if summary.StructureSetIntegrity.SDFCount != 2 || summary.StructureSetIntegrity.SMILESCount != 2 ||
		!summary.StructureSetIntegrity.CanonicalSMILESMatch {
		t.Fatalf("structure-set integrity=%#v", summary.StructureSetIntegrity)
	}
	if summary.Docking.Method.SelectionContract !=
		"minimum_affinity_then_reference_geometry_on_exact_ties_else_stable_run_mode" ||
		len(summary.Docking.Method.ObservedSelectionBases) != 1 ||
		summary.Docking.Method.ObservedSelectionBases[0] !=
			"best_affinity_then_reference_geometry_tiebreak" {
		t.Fatalf("summary primary-pose selection evidence=%#v", summary.Docking.Method)
	}
	report, err := os.ReadFile(filepath.Join(workspace, "final_results", "project_report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"pocket-conditioned-model", "fixture-pocket-diffusion", "protein-pocket"} {
		if !strings.Contains(string(report), marker) {
			t.Errorf("report is missing generator provenance %q", marker)
		}
	}
	if !strings.Contains(string(report), "`pocket_candidates.sdf` / `pocket_candidates.smi`") {
		t.Fatalf("report does not name the validated structure companions: %s", report)
	}
	writeFixture(t, workspace, "pocket_candidates.smi", "candidate_id\tcanonical_smiles\n")
	invalidCommand := exec.Command(command.Args[0], command.Args[1:]...)
	invalidCommand.Dir = workspace
	output, err = invalidCommand.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "contains a header but no molecule rows") {
		t.Fatalf("header-only SMILES companion was accepted: err=%v output=%s", err, output)
	}
}

func writeFixture(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
