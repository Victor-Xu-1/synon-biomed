package assets_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestP2RankPocketPackBuildsAHashedDockingHandoff(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	packPath := filepath.Join(repositoryRoot, "internal", "sciencecapability", "executionpacks", "p2rank_binding_pockets.py")
	skillPath := filepath.Join(repositoryRoot, "skills", "synonbiomed", "p2rank-pocket-detection", "scripts", "p2rank_binding_pockets.py")
	pack, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pack, skill) {
		t.Fatal("P2Rank execution pack and materialized Skill asset differ")
	}
	if bytes.Contains(pack, []byte("pred_max_pockets")) || bytes.Contains(pack, []byte("pred_min_pockets")) {
		t.Fatal("P2Rank 2.5.1 execution still contains unsupported pocket-count parameters")
	}
	if output, err := exec.Command("python3", packPath, "--help").CombinedOutput(); err != nil {
		t.Fatalf("P2Rank pack help failed: %v\n%s", err, output)
	}

	temporary := t.TempDir()
	pdbPath := filepath.Join(temporary, "protein.pdb")
	pdb := strings.Join([]string{
		"EXPDTA    X-RAY DIFFRACTION",
		fmt.Sprintf("ATOM  %5d  CA  ALA A   1    %8.3f%8.3f%8.3f  1.00%6.2f           C", 1, 0.0, 0.0, 0.0, 21.0),
		fmt.Sprintf("ATOM  %5d  CA  GLY A   2    %8.3f%8.3f%8.3f  1.00%6.2f           C", 2, 2.0, 3.0, 4.0, 42.0),
		fmt.Sprintf("ATOM  %5d  CA  SER A   3    %8.3f%8.3f%8.3f  1.00%6.2f           C", 3, 20.0, 1.0, 2.0, 63.0),
		"END",
		"",
	}, "\n")
	if err := os.WriteFile(pdbPath, []byte(pdb), 0o600); err != nil {
		t.Fatal(err)
	}
	alphaFoldPath := filepath.Join(temporary, "alphafold.pdb")
	alphaFold := strings.Join([]string{
		"TITLE     ALPHAFOLD MONOMER PREDICTION",
		fmt.Sprintf("ATOM  %5d  CA  ALA A   1    %8.3f%8.3f%8.3f  1.00%6.2f           C", 1, 0.0, 0.0, 0.0, 61.0),
		fmt.Sprintf("ATOM  %5d  CA  GLY A   2    %8.3f%8.3f%8.3f  1.00%6.2f           C", 2, 2.0, 3.0, 4.0, 82.0),
		fmt.Sprintf("ATOM  %5d  CA  SER A   3    %8.3f%8.3f%8.3f  1.00%6.2f           C", 3, 20.0, 1.0, 2.0, 94.0),
		"END", "",
	}, "\n")
	if err := os.WriteFile(alphaFoldPath, []byte(alphaFold), 0o600); err != nil {
		t.Fatal(err)
	}
	experimentalAlphaFoldMentionPath := filepath.Join(temporary, "experimental-alphafold-mention.pdb")
	experimentalAlphaFoldMention := strings.Join([]string{
		"TITLE     X-RAY STRUCTURE COMPARED WITH AN ALPHAFOLD PREDICTION",
		"REMARK   1 ALPHAFOLD IS DISCUSSED ONLY AS A REFERENCE MODEL",
		"EXPDTA    X-RAY DIFFRACTION",
		fmt.Sprintf("ATOM  %5d  CA  ALA A   1    %8.3f%8.3f%8.3f  1.00%6.2f           C", 1, 0.0, 0.0, 0.0, 21.0),
		"END", "",
	}, "\n")
	if err := os.WriteFile(experimentalAlphaFoldMentionPath, []byte(experimentalAlphaFoldMention), 0o600); err != nil {
		t.Fatal(err)
	}
	conflictingExperimentPath := filepath.Join(temporary, "conflicting-experiment.pdb")
	conflictingExperiment := strings.Join([]string{
		"EXPDTA    X-RAY DIFFRACTION; SOLUTION NMR",
		fmt.Sprintf("ATOM  %5d  CA  ALA A   1    %8.3f%8.3f%8.3f  1.00%6.2f           C", 1, 0.0, 0.0, 0.0, 21.0),
		"END", "",
	}, "\n")
	if err := os.WriteFile(conflictingExperimentPath, []byte(conflictingExperiment), 0o600); err != nil {
		t.Fatal(err)
	}
	alphaFoldReferenceOnlyPath := filepath.Join(temporary, "alphafold-reference-only.pdb")
	alphaFoldReferenceOnly := strings.Join([]string{
		"TITLE     EXPERIMENTAL TARGET COMPARED WITH AN ALPHAFOLD PREDICTION",
		"REMARK   1 REFERENCE ARTICLE ABOUT ALPHAFOLD",
		fmt.Sprintf("ATOM  %5d  CA  ALA A   1    %8.3f%8.3f%8.3f  1.00%6.2f           C", 1, 0.0, 0.0, 0.0, 21.0),
		"END", "",
	}, "\n")
	if err := os.WriteFile(alphaFoldReferenceOnlyPath, []byte(alphaFoldReferenceOnly), 0o600); err != nil {
		t.Fatal(err)
	}
	ambiguousPath := filepath.Join(temporary, "ambiguous.pdb")
	if err := os.WriteFile(ambiguousPath, []byte(strings.Join(strings.Split(pdb, "\n")[1:], "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	predictionsPath := filepath.Join(temporary, "protein_predictions.csv")
	predictions := "name,rank,score,probability,sas_points,surf_atoms,center_x,center_y,center_z,residue_ids,surf_atom_ids\n" +
		"pocket1,1,12.5,0.75,8,3,1.0,1.5,2.0,\"A_1 A_2 A_3\",\"1 2 3\"\n"
	if err := os.WriteFile(predictionsPath, []byte(predictions), 0o600); err != nil {
		t.Fatal(err)
	}
	python := `
import importlib.util, json, sys, tempfile
from pathlib import Path
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("p2rank_pack", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
atoms, b_factors = module.pdb_atoms(module.Path(sys.argv[2]))
assert module.choose_profile("auto", module.Path(sys.argv[2]))[0] == "default"
assert module.choose_profile("auto", module.Path(sys.argv[3]))[0] == "alphafold"
profile, reason = module.choose_profile("auto", module.Path(sys.argv[4]))
assert profile == "default"
assert "EXPDTA" in reason
for candidate, expected in ((sys.argv[5], "conflicting"), (sys.argv[6], "ambiguous"), (sys.argv[7], "ambiguous")):
    try:
        module.choose_profile("auto", module.Path(candidate))
    except ValueError as error:
        assert expected in str(error)
    else:
        raise AssertionError(f"unsafe automatic profile accepted for {candidate}")
rows = module.parse_predictions(module.Path(sys.argv[8]))
candidates = module.build_candidates(rows, atoms, 20.0, 6.0)
print(json.dumps(candidates[0]))

with tempfile.TemporaryDirectory() as root_text:
    root = Path(root_text).resolve()
    structure = root / "protein.pdb"
    archive = root / "p2rank.tar.gz"
    structure.write_text("ATOM\n", encoding="utf-8")
    archive.write_bytes(b"archive")
    (root / module.DEFAULT_OUTPUT_DIR).mkdir()
    (root / f"{module.DEFAULT_OUTPUT_DIR}-2").mkdir()
    target = module.output_target(root, module.DEFAULT_OUTPUT_DIR, (structure, archive))
    assert target == root / f"{module.DEFAULT_OUTPUT_DIR}-3"
    custom = root / "custom-output"
    custom.mkdir()
    try:
        module.output_target(root, custom.name, (structure, archive))
    except ValueError as error:
        assert "not owned" in str(error)
    else:
        raise AssertionError("an explicit unowned output directory was silently redirected")

with tempfile.TemporaryDirectory() as root_text, tempfile.TemporaryDirectory() as outside_text:
    root = Path(root_text).resolve()
    outside = Path(outside_text).resolve()
    target = root / "pocket_detection"
    target.mkdir()
    (target / module.OUTPUT_MARKER).write_text(
        '{"execution_pack_id":"binding-pocket-prediction.p2rank","schema":"synon.execution-pack-output-owner.v1"}\n',
        encoding="utf-8",
    )
    (target / "old.txt").write_text("old", encoding="utf-8")
    staging = root / ".p2rank-output-next"
    staging.mkdir()
    (staging / "new.txt").write_text("new", encoding="utf-8")
    (root / ".p2rank-generations").symlink_to(outside, target_is_directory=True)
    try:
        module.promote_output(root, staging, target, "token")
    except ValueError as error:
        assert "symbolic link" in str(error) or "escapes" in str(error)
    else:
        raise AssertionError("P2Rank history symlink escape was accepted")
    assert (target / "old.txt").read_text(encoding="utf-8") == "old"
    assert list(outside.iterdir()) == []

with tempfile.TemporaryDirectory() as root_text, tempfile.TemporaryDirectory() as outside_text:
    root = Path(root_text).resolve()
    outside = Path(outside_text).resolve()
    (root / ".p2rank-failures").symlink_to(outside, target_is_directory=True)
    try:
        module.validated_internal_state_directory(root, root / ".p2rank-failures", "failure")
    except ValueError as error:
        assert "symbolic link" in str(error) or "escapes" in str(error)
    else:
        raise AssertionError("P2Rank failure symlink escape was accepted")
    assert list(outside.iterdir()) == []
`
	output, err := exec.Command(
		"python3", "-c", python, packPath, pdbPath, alphaFoldPath, experimentalAlphaFoldMentionPath,
		conflictingExperimentPath, alphaFoldReferenceOnlyPath, ambiguousPath, predictionsPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("P2Rank parser behavior failed: %v\n%s", err, output)
	}
	var candidate struct {
		Rank        int       `json:"rank"`
		Probability float64   `json:"probability"`
		Center      []float64 `json:"center"`
		Size        []float64 `json:"size"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &candidate); err != nil {
		t.Fatal(err)
	}
	if candidate.Rank != 1 || candidate.Probability != 0.75 || len(candidate.Center) != 3 ||
		len(candidate.Size) != 3 || candidate.Size[0] != 50 {
		t.Fatalf("candidate=%#v", candidate)
	}

	receptorSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(pdb)))
	selection := map[string]any{
		"schema": "synon.binding-pocket-prediction.v1", "status": "passed",
		"source": map[string]any{"file": "protein.pdb", "sha256": receptorSHA},
		"method": map[string]any{
			"name": "P2Rank", "version": "2.5.1",
			"archive_sha256": "d243f2d9036ac053fefb9407b5fe1c85f4fe077c519fd975ac585e995feab274",
		},
		"selection": map[string]any{
			"rank": 1, "score": 12.5, "probability": 0.75,
			"center_angstrom": map[string]any{"x": 1.0, "y": 1.5, "z": 2.0},
			"size_angstrom":   map[string]any{"x": 50.0, "y": 20.0, "z": 20.0},
			"box_definition":  "P2Rank centroid with surface-atom extent plus explicit padding",
		},
	}
	selectionBytes, _ := json.MarshalIndent(selection, "", "  ")
	selectionBytes = append(selectionBytes, '\n')
	selectionPath := filepath.Join(temporary, "pocket_selection.json")
	if err := os.WriteFile(selectionPath, selectionBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	selectionSHA := fmt.Sprintf("%x", sha256.Sum256(selectionBytes))
	validation := map[string]any{
		"schema":            "synon.execution-pack-validation.v4",
		"execution_pack_id": "binding-pocket-prediction.p2rank", "overall_pass": true,
		"checks": map[string]any{
			"source_integrity": true, "archive_integrity": true, "engine_identity": true,
			"rank_integrity": true, "probability_integrity": true, "box_integrity": true,
			"output_integrity": true,
		},
		"inputs": map[string]any{
			"structure":      receptorSHA,
			"p2rank_archive": "d243f2d9036ac053fefb9407b5fe1c85f4fe077c519fd975ac585e995feab274",
		},
		"pocket_selection_sha256": selectionSHA,
	}
	validationBytes, _ := json.MarshalIndent(validation, "", "  ")
	validationBytes = append(validationBytes, '\n')
	validationPath := filepath.Join(temporary, "pocket_validation.json")
	if err := os.WriteFile(validationPath, validationBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	pocketModule := filepath.Join(repositoryRoot, "internal", "sciencecapability", "executionpacks", "autodock_vina_pockets.py")
	consumer := `
import importlib.util, json, sys
from pathlib import Path
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("pocket_consumer", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
value = module.load_validated_pocket_selection(Path(sys.argv[2]), Path(sys.argv[3]), Path(sys.argv[4]))
assert tuple(value["center"]) == (1.0, 1.5, 2.0)
assert tuple(value["size"]) == (50.0, 20.0, 20.0)
print(json.dumps({"rank": value["rank"], "probability": value["probability"]}))
`
	output, err = exec.Command("python3", "-c", consumer, pocketModule, selectionPath, validationPath, pdbPath).CombinedOutput()
	if err != nil {
		t.Fatalf("Vina pocket-receipt consumption failed: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte(`"rank": 1`)) {
		t.Fatalf("unexpected receipt output: %s", output)
	}
}
