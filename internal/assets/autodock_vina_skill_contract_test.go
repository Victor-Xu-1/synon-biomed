package assets_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestAutoDockVinaSkillUsesTheManagedHarnessExecutionContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillPath := filepath.Join(repositoryRoot, "skills", "synonbiomed", "autodock-vina", "SKILL.md")
	content, err := os.ReadFile(skillPath)
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
	foundManifestEntry := false
	for _, entry := range manifest.Files {
		if entry.Path != "autodock-vina/SKILL.md" {
			continue
		}
		foundManifestEntry = true
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		if entry.Bytes != len(content) || entry.SHA256 != digest {
			t.Fatalf("autodock-vina manifest entry is stale: bytes=%d/%d sha256=%s/%s",
				entry.Bytes, len(content), entry.SHA256, digest)
		}
	}
	if !foundManifestEntry {
		t.Fatal("autodock-vina is missing from the bundled skills manifest")
	}
	skillScript, err := os.ReadFile(filepath.Join(filepath.Dir(skillPath), "scripts", "autodock_vina.py"))
	if err != nil {
		t.Fatal(err)
	}
	compatibilityScript, err := os.ReadFile(filepath.Join(repositoryRoot, "internal", "sciencecapability", "executionpacks", "autodock_vina.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(skillScript, compatibilityScript) {
		t.Fatal("AutoDock Vina compatibility API and managed Skill do not use the same workflow script bytes")
	}
	foundScriptEntry := false
	for _, entry := range manifest.Files {
		if entry.Path == "autodock-vina/scripts/autodock_vina.py" {
			foundScriptEntry = true
			digest := fmt.Sprintf("%x", sha256.Sum256(skillScript))
			if entry.Bytes != len(skillScript) || entry.SHA256 != digest {
				t.Fatal("autodock-vina workflow script manifest entry is stale")
			}
		}
	}
	if !foundScriptEntry {
		t.Fatal("autodock-vina workflow script is missing from the bundled skills manifest")
	}
	implementationSource := append([]byte(nil), skillScript...)
	for _, moduleName := range []string{"autodock_vina_inputs.py", "autodock_vina_outputs.py", "autodock_vina_pockets.py"} {
		skillModule, readErr := os.ReadFile(filepath.Join(filepath.Dir(skillPath), "scripts", moduleName))
		if readErr != nil {
			t.Fatal(readErr)
		}
		compatibilityModule, readErr := os.ReadFile(filepath.Join(repositoryRoot, "internal", "sciencecapability", "executionpacks", moduleName))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(skillModule, compatibilityModule) {
			t.Fatalf("AutoDock Vina module %s differs between Skill and compatibility pack", moduleName)
		}
		implementationSource = append(implementationSource, skillModule...)
		manifestPath := "autodock-vina/scripts/" + moduleName
		manifestMatch := false
		for _, entry := range manifest.Files {
			if entry.Path != manifestPath {
				continue
			}
			manifestMatch = true
			digest := fmt.Sprintf("%x", sha256.Sum256(skillModule))
			if entry.Bytes != len(skillModule) || entry.SHA256 != digest {
				t.Fatalf("AutoDock Vina module manifest entry is stale: %s", moduleName)
			}
		}
		if !manifestMatch {
			t.Fatalf("AutoDock Vina module is missing from manifest: %s", moduleName)
		}
	}

	catalog := skills.Load([]string{filepath.Dir(skillPath)}, skills.LoadOptions{MaxBodyBytes: 12000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load autodock-vina skill: %v", loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "autodock-vina" {
		t.Fatalf("unexpected autodock-vina catalog: %#v", loaded)
	}
	if !slices.Equal(loaded[0].RequiredCapabilities, []string{"molecular-docking"}) {
		t.Fatalf("required capabilities=%v", loaded[0].RequiredCapabilities)
	}
	if !slices.Equal(loaded[0].PreferredExecutionAssets, []string{"scripts/autodock_vina.py"}) {
		t.Fatalf("preferred execution assets=%v", loaded[0].PreferredExecutionAssets)
	}
	capabilityCatalog, err := sciencecapability.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	executionPacks := capabilityCatalog.LocalExecutionPacksForSkill(loaded[0].Name)
	if len(executionPacks) != 1 || executionPacks[0].ExecutionPack.MaterializedSkillEntrypoint() != "scripts/autodock_vina.py" {
		t.Fatalf("registered execution packs=%#v", executionPacks)
	}
	for _, identifier := range []string{"autodock-vina", "vina", "mk_prepare_ligand.py", "mk_prepare_receptor.py", "obabel", "openbabel", "meeko", "rdkit"} {
		if !slices.Contains(executionPacks[0].ManagedExecutionIdentifiers(), identifier) {
			t.Fatalf("registered managed execution identifier is missing: %s", identifier)
		}
	}
	if len(loaded[0].CriticalConstraints) != 3 ||
		!strings.Contains(loaded[0].CriticalConstraints[0], "never hand-edit the quantitative report") ||
		!strings.Contains(loaded[0].CriticalConstraints[1], "never split ranked_poses.pdbqt") ||
		!strings.Contains(loaded[0].CriticalConstraints[2], "never substitute the whole-protein centroid") {
		t.Fatalf("critical constraints=%v", loaded[0].CriticalConstraints)
	}
	for _, requiredTool := range []string{
		"search_skills", "skill", "repl", "download_public_scientific_file", "manage_environments", "manage_packages", "python", "bash",
		"read_file", "edit_file", "save_artifacts",
	} {
		if !slices.Contains(loaded[0].Tools, requiredTool) {
			t.Errorf("governed tool dependency is missing from frontmatter: %s", requiredTool)
		}
	}
	if matches := catalog.SearchNames("virtual screening with reproducible docking poses", 3); len(matches) == 0 || matches[0] != "autodock-vina" {
		t.Fatalf("autodock-vina is not discoverable for docking: %v", matches)
	}

	text := string(content)
	for _, requiredMarker := range []string{
		`manage_environments(mode="list"`, `manage_environments(mode="create"`, `manage_packages`,
		`--seed 42`, `pdb_select_latest_liganded_structure`, `release-date-descending`, `rejected_newer_candidates`,
		`${SYNON_SKILL_DIR}/scripts/autodock_vina.py`, "A second", "managed environment generation",
		"process cleanup", "ranked candidates", "unsupported `--log` flag", "--reference-ligand",
		"derives and records that ligand centroid", "docking_complex_ensemble.pdb", "docking_components.csv",
		"docking_pose_scores.csv", "docking_pose_samples.csv", "--repeat-count 3", "exactly one primary pose",
		"lowest Vina affinity always wins", "reference-ligand", "geometric",
		"preserved exactly", `load ` + "`drug-discovery-pipeline`" + ` again`, "assemble_docking_results.py",
		"do not author quantitative Markdown with `edit_file`", "openbabel", "CDX", "primary_pose_manifest.csv",
		"Never split `ranked_poses.pdbqt`", "pack-generated readable report",
		"raw apo receptor", "P2Rank resolver",
	} {
		if !strings.Contains(text, requiredMarker) {
			t.Errorf("governed execution marker is missing: %s", requiredMarker)
		}
	}
	for _, requiredScriptMarker := range []string{
		"remove_ligands_and_waters", "--allow_bad_res", "--reference-ligand", "reference_ligand_centroid", "--output-dir",
		"build_docking_complex_ensemble", "docking_complex_ensemble.pdb", "docking_components.csv", "candidate_id_fidelity",
		"docking_pose_scores.csv", "docking_pose_samples.csv", "pose_count_fidelity", "POSE 1",
		"select_primary_pose", "repeat_count", "primary_pose_selection", "convert_ligand_source",
		"write_primary_pose_artifacts", "primary_pose_manifest.csv", "write_docking_report",
	} {
		if !strings.Contains(string(implementationSource), requiredScriptMarker) {
			t.Errorf("governed execution script is missing receptor preparation marker: %s", requiredScriptMarker)
		}
	}
	for _, forbiddenMarker := range []string{
		"software_runtime", "host.compute", "compute.submit_job(",
		"subprocess.run", "shutil.which", "pip install",
		`"execution_pack_id"`, `"provider"`, `"expected_outputs"`, `"scientific_evidence"`,
		"bounded self-repair", "then rerun the unchanged scientific task",
		"--poses-per-candidate",
	} {
		if strings.Contains(text, forbiddenMarker) {
			t.Errorf("skill duplicates or weakens the governed runtime contract: %q", forbiddenMarker)
		}
	}
	if strings.Contains(string(skillScript), "--poses-per-candidate") {
		t.Fatal("workflow script still exposes the retired multi-pose presentation path")
	}
}
