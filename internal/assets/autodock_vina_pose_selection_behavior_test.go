package assets_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAutoDockVinaPrimaryPoseSelectionKeepsBestScoreAndUsesReferenceOnlyForTies(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	scriptPath := filepath.Join(
		repositoryRoot, "skills", "synonbiomed", "autodock-vina", "scripts", "autodock_vina.py",
	)
	command := exec.Command("python3", "-c", `
import importlib.util
import os
import sys
import tempfile
import types
from pathlib import Path

sys.dont_write_bytecode = True
sys.modules["gemmi"] = types.ModuleType("gemmi")
chem = types.ModuleType("rdkit.Chem")
chem.AllChem = types.SimpleNamespace()
rdkit = types.ModuleType("rdkit")
rdkit.Chem = chem
sys.modules["rdkit"] = rdkit
sys.modules["rdkit.Chem"] = chem
sys.path.insert(0, str(Path(sys.argv[1]).parent))
spec = importlib.util.spec_from_file_location("autodock_vina", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
select_primary_pose = module.select_primary_pose
primary_selection_evidence = module.primary_selection_evidence
write_primary_pose_artifacts = module.write_primary_pose_artifacts
write_docking_report = module.write_docking_report
validate_docking_center_contract = module.validate_docking_center_contract
validate_box_size_contract = module.validate_box_size_contract
initialize_receptor_entities = module.initialize_receptor_entities
normalize_receptor_residue_aliases = module.normalize_receptor_residue_aliases
validate_output_target = module.validate_output_target
promote_execution_output = module.promote_execution_output
validated_internal_state_directory = module.validated_internal_state_directory
pdb_reference_site_scores = module.pdb_reference_site_scores

class EntityFixture:
    def __init__(self):
        self.initialized = False
    def setup_entities(self):
        self.initialized = True

entity_fixture = EntityFixture()
initialize_receptor_entities(entity_fixture)
assert entity_fixture.initialized

class SequenceID:
    def __init__(self, number):
        self.num = number
class ResidueFixture:
    def __init__(self, name, number):
        self.name = name
        self.seqid = SequenceID(number)
class ChainFixture(list):
    def __init__(self, name, residues):
        super().__init__(residues)
        self.name = name

alias_model = [ChainFixture("A", [ResidueFixture("HSD", 7), ResidueFixture("ALA", 8), ResidueFixture("HSE", 9)])]
alias_changes = normalize_receptor_residue_aliases(alias_model)
assert [residue.name for residue in alias_model[0]] == ["HID", "ALA", "HIE"]
assert alias_changes == [
    {"chain_id": "A", "residue_number": 7, "source_name": "HSD", "target_name": "HID"},
    {"chain_id": "A", "residue_number": 9, "source_name": "HSE", "target_name": "HIE"},
]

try:
    validate_docking_center_contract(Path("apo.pdb"), None, False, False, (1.0, 2.0, 3.0), None)
except ValueError as error:
    assert "validated binding-site evidence" in str(error)
else:
    raise AssertionError("raw apo receptor accepted an unbound explicit center")
validate_docking_center_contract(Path("prepared.pdbqt"), None, False, False, (1.0, 2.0, 3.0), "resolved-user-input")
validate_docking_center_contract(Path("apo.pdb"), None, False, False, (1.0, 2.0, 3.0), "resolved-user-input")
validate_docking_center_contract(Path("complex.pdb"), "LIG", False, False, (None, None, None), None)
validate_docking_center_contract(Path("apo.pdb"), None, True, True, (None, None, None), None)
validate_box_size_contract(False, (20.0, 20.0, 20.0))
validate_box_size_contract(True, (None, None, None))
for receptor, reference, center in [
    (Path("prepared.pdbqt"), None, (None, None, None)),
    (Path("complex.pdb"), "LIG", (1.0, 2.0, 3.0)),
]:
    try:
        validate_docking_center_contract(receptor, reference, False, False, center, None)
    except ValueError:
        pass
    else:
        raise AssertionError("invalid docking-center provenance was accepted")

with tempfile.TemporaryDirectory() as temporary:
    root = Path(temporary).resolve()
    inputs = root / "inputs"
    inputs.mkdir()
    receptor = inputs / "receptor.pdb"
    ligand = inputs / "ligand.sdf"
    receptor.write_text("ATOM\n", encoding="utf-8")
    ligand.write_text("ligand\n", encoding="utf-8")
    try:
        validate_output_target(root, inputs, (receptor, ligand))
    except ValueError as error:
        assert "must not overlap" in str(error)
    else:
        raise AssertionError("input ancestor was accepted as output")
    assert receptor.read_text(encoding="utf-8") == "ATOM\n"
    assert ligand.read_text(encoding="utf-8") == "ligand\n"

    prior = root / "out"
    prior.mkdir()
    (prior / module.OUTPUT_OWNERSHIP_MARKER).write_text(
        '{"execution_pack_id":"molecular-docking.autodock-vina","schema":"synon.execution-pack-output-owner.v1"}\n',
        encoding="utf-8",
    )
    (prior / "completed.txt").write_text("preserve", encoding="utf-8")
    redirected = validate_output_target(root, prior, (receptor, ligand), True)
    assert redirected == root / "out-2"
    try:
        validate_output_target(root, prior, (receptor, ligand))
    except ValueError as error:
        assert "must not already exist" in str(error)
    else:
        raise AssertionError("a matching workspace marker authorized explicit output reuse")
    original_which = module.shutil.which
    original_argv = sys.argv
    original_cwd = Path.cwd()
    module.shutil.which = lambda _name: None
    sys.argv = [
        str(Path(sys.argv[1]).name), "--receptor", "inputs/receptor.pdb", "--ligand", "inputs/ligand.sdf",
        "--center-x", "1", "--center-y", "2", "--center-z", "3",
        "--center-authority", "resolved-user-input", "--size-x", "20", "--size-y", "20", "--size-z", "20",
        "--output-dir", "out",
    ]
    os.chdir(root)
    try:
        module.main()
    except RuntimeError as error:
        assert "documented CLI is unavailable" in str(error)
    else:
        raise AssertionError("missing runtime unexpectedly completed")
    finally:
        os.chdir(original_cwd)
        sys.argv = original_argv
        module.shutil.which = original_which
    assert (prior / "completed.txt").read_text(encoding="utf-8") == "preserve"
    assert receptor.read_text(encoding="utf-8") == "ATOM\n"
    assert ligand.read_text(encoding="utf-8") == "ligand\n"

    staging = root / ".vina-pack-output-rerun"
    staging.mkdir()
    (staging / "new-result.txt").write_text("new", encoding="utf-8")
    try:
        promote_execution_output(staging, prior, "rerun-token", root)
    except RuntimeError as error:
        assert "created before promotion" in str(error)
    else:
        raise AssertionError("Vina promotion replaced an existing output target")
    assert (prior / "completed.txt").read_text(encoding="utf-8") == "preserve"
    assert (staging / "new-result.txt").read_text(encoding="utf-8") == "new"

with tempfile.TemporaryDirectory() as temporary, tempfile.TemporaryDirectory() as outside_temporary:
    root = Path(temporary).resolve()
    outside = Path(outside_temporary).resolve()
    (root / ".vina-pack-failures").symlink_to(outside, target_is_directory=True)
    try:
        validated_internal_state_directory(root, root / ".vina-pack-failures", "failure")
    except ValueError as error:
        assert "symbolic link" in str(error) or "escapes" in str(error)
    else:
        raise AssertionError("failure root symlink was accepted")
    assert list(outside.iterdir()) == []

def atom(x, y, z):
    return {"element": "C", "x": x, "y": y, "z": z}

reference = [atom(0, 0, 0), atom(2, 0, 0)]
better_score = {
    "affinity_kcal_mol": -8.1,
    "atoms": [atom(10, 0, 0), atom(10, 2, 0)],
    "run_index": 2,
    "mode_index": 3,
}
better_alignment = {
    "affinity_kcal_mol": -8.0,
    "atoms": [atom(0, 0, 0), atom(2, 0, 0)],
    "run_index": 1,
    "mode_index": 1,
}
unique_with_reference = select_primary_pose([better_alignment, better_score], reference)
assert unique_with_reference["run_index"] == 2
assert unique_with_reference["selection_basis"] == "best_affinity"

tied_far = dict(better_score, affinity_kcal_mol=-8.0)
tied_with_reference = select_primary_pose([tied_far, better_alignment], reference)
assert tied_with_reference["run_index"] == 1
assert tied_with_reference["selection_basis"] == "best_affinity_then_reference_geometry_tiebreak"

unique_without_reference = select_primary_pose(
    [dict(better_alignment), dict(better_score)],
    None,
)
assert unique_without_reference["run_index"] == 2
assert unique_without_reference["selection_basis"] == "best_affinity"

tied_without_reference = select_primary_pose(
    [dict(tied_far, run_index=2, mode_index=1), dict(better_alignment, run_index=1, mode_index=4)],
    None,
)
assert tied_without_reference["run_index"] == 1
assert tied_without_reference["selection_basis"] == "best_affinity_then_stable_run_mode_tiebreak"

primary_poses = {
    "unique-reference": unique_with_reference,
    "tied-reference": tied_with_reference,
    "unique-no-reference": unique_without_reference,
    "tied-no-reference": tied_without_reference,
}
row_bases = sorted({pose["selection_basis"] for pose in primary_poses.values()})
selection_evidence = primary_selection_evidence(primary_poses)
validation_sampling = {
    "primary_selection": selection_evidence["selection_contract"],
    "observed_selection_bases": selection_evidence["observed_selection_bases"],
}
complex_summary = dict(selection_evidence)
assert row_bases == validation_sampling["observed_selection_bases"]
assert row_bases == complex_summary["observed_selection_bases"]
assert validation_sampling["primary_selection"] == complex_summary["selection_contract"]

model_text = (
    "MODEL 1\n"
    "REMARK VINA RESULT: -7.500 0.000 0.000\n"
    "ATOM      1  C1  LIG A   1       1.000   2.000   3.000  1.00  0.00      0.000 C\n"
    "ENDMDL\n"
)
rows = [
    {"ligand_id": "candidate/A", "rank": 1, "best_affinity_kcal_mol": -7.5, "center_x": 1.0, "center_y": 2.0, "center_z": 3.0},
    {"ligand_id": "candidate B", "rank": 2, "best_affinity_kcal_mol": -6.5, "center_x": 1.0, "center_y": 2.0, "center_z": 3.0},
]
primary = {
    "candidate/A": {"pdbqt_text": model_text, "affinity_kcal_mol": -7.5, "run_index": 1, "mode_index": 1},
    "candidate B": {"pdbqt_text": model_text, "affinity_kcal_mol": -6.5, "run_index": 1, "mode_index": 2},
}
with tempfile.TemporaryDirectory() as temporary:
    output = Path(temporary)
    site_fixture = output / "site-fixture.pdb"
    site_fixture.write_text(
        "REMARK 800 SITE_IDENTIFIER: AC1\n"
        "REMARK 800 SITE_DESCRIPTION: BINDING SITE FOR RESIDUE ERM A 2001\n"
        "REMARK 800 SITE_IDENTIFIER: AC2\n"
        "REMARK 800 SITE_DESCRIPTION: BINDING SITE FOR RESIDUE OLB A 2002\n"
        "SITE     1 AC1 16 ASP A 129  ILE A 130\n"
        "SITE     1 AC2  4 ILE A 367  ILE A 368\n",
        encoding="utf-8",
    )
    assert pdb_reference_site_scores(site_fixture) == {
        ("ERM", "A", 2001): 16,
        ("OLB", "A", 2002): 4,
    }
    paths, manifest, manifest_rows = write_primary_pose_artifacts(output, rows, primary)
    assert len(paths) == 2
    assert len(manifest_rows) == 2
    assert manifest.is_file()
    assert all(path.is_file() and "rank-0000-" not in path.name for path in paths)
    report = output / "docking_report.md"
    args = types.SimpleNamespace(size_x=20, size_y=20, size_z=20, seed=42, repeat_count=1, exhaustiveness=8, num_modes=9)
    write_docking_report(report, Path("receptor.pdbqt"), Path("ligands.cdx"), rows, "explicit", None, None, manifest_rows, args)
    report_text = report.read_text(encoding="utf-8")
    assert "does not by itself prove" in report_text
    assert "primary_poses/" in report_text
    args.report_language = "zh"
    write_docking_report(report, Path("receptor.pdbqt"), Path("ligands.cdx"), rows, "explicit", None, None, manifest_rows, args)
    report_text = report.read_text(encoding="utf-8")
    assert report_text.startswith("# 分子对接报告")
    assert "由用户确认并传入执行包的显式对接盒坐标" in report_text
    assert "Vina 分数是计算评分估计值" in report_text
`, scriptPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run primary-pose selector behavior: %v\n%s", err, output)
	}
}
