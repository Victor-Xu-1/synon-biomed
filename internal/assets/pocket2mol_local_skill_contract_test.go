package assets_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestPocket2MolLocalSkillOwnsManagedSetupAndRepresentativeGeneration(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillDir := filepath.Join(repositoryRoot, "skills", "synonbiomed", "pocket2mol-local")
	skillPath := filepath.Join(skillDir, "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, marker := range []string{
		"manage_environments` in `list` mode",
		"current CPU, memory, and GPU/VRAM",
		"official pengxingang/Pocket2Mol",
		"historical evidence, not a command",
		"https://pytorch-geometric.readthedocs.io/en/stable/install/installation.html",
		"Blackwell `sm_120`",
		"reject CUDA 12.1/12.4 builds",
		"PyTorch 2.7 with CUDA 12.8",
		"not a fixed version",
		"is not an architecture witness",
		"torch.cuda.get_arch_list()",
		"never a silent CPU rewrite",
		"representative real pocket",
		"synon.binding-pocket-handoff.v1",
		"Never derive a pocket center with an ad hoc residue loop",
		"does not publish a universal RAM or VRAM minimum",
		"A folder ID is not a file ID",
		"Install the current `gdown`",
		"`implementation` blank",
		"preserves partial bytes",
		"scripts/run_pocket2mol.py",
		"Do not fall back",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("Pocket2Mol Skill is missing %q", marker)
		}
	}
	catalog := skills.Load([]string{skillDir})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatal(loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 {
		t.Fatalf("Pocket2Mol catalog size=%d", len(loaded))
	}
	if !slices.Equal(loaded[0].PreferredExecutionAssets, []string{
		"scripts/acquire_checkpoint.py", "scripts/run_pocket2mol.py",
	}) {
		t.Fatalf("preferred assets=%#v", loaded[0].PreferredExecutionAssets)
	}
	if !slices.Contains(loaded[0].RequiredCapabilities, "gpu") {
		t.Fatalf("required capabilities=%#v", loaded[0].RequiredCapabilities)
	}

	scriptPath := filepath.Join(skillDir, "scripts", "run_pocket2mol.py")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"torch.cuda.is_available()",
		"torch.cuda.get_arch_list()",
		"torch.mm(left, right)",
		"TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD",
		"finite_conformer",
		"pocket2mol_candidates.sdf",
		"pocket2mol_validation.json",
		"--pocket-validation",
		"--checkpoint-acquisition",
		"load_checkpoint_acquisition",
		"load_pocket_handoff",
		"pocket_handoff_sha256",
		"source_files_sha256",
	} {
		if !strings.Contains(string(script), marker) {
			t.Errorf("Pocket2Mol wrapper is missing %q", marker)
		}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	compilePythonScript := func(path string) ([]byte, error) {
		command := exec.Command(python, "-m", "py_compile", path)
		command.Env = append(os.Environ(), "PYTHONPYCACHEPREFIX="+t.TempDir())
		return command.CombinedOutput()
	}
	if output, err := compilePythonScript(scriptPath); err != nil {
		t.Fatalf("compile Pocket2Mol wrapper: %v\n%s", err, output)
	}
	acquirePath := filepath.Join(skillDir, "scripts", "acquire_checkpoint.py")
	acquire, err := os.ReadFile(acquirePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"/drive/folders/", "download_folder(", "resume=True", "source_kind\": \"google_drive_folder",
		"checkpoint response is HTML/XML", "sha256_file(checkpoint)", "checkpoint_acquisition.json",
	} {
		if !bytes.Contains(acquire, []byte(marker)) {
			t.Fatalf("Pocket2Mol acquisition asset missing %q", marker)
		}
	}
	if output, err := compilePythonScript(acquirePath); err != nil {
		t.Fatalf("compile Pocket2Mol acquisition asset: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "scripts", "__pycache__")); !os.IsNotExist(err) {
		t.Fatalf("Python syntax checks left cache residue in the source tree: %v", err)
	}
	checkpointDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkpointDir, "pretrained.pt"), []byte("PK\x03\x04model"), 0o600); err != nil {
		t.Fatal(err)
	}
	officialFolder := "https://drive.google.com/drive/folders/official-folder-id?usp=sharing"
	validated, err := exec.Command(
		python, acquirePath, "--folder-url", officialFolder,
		"--output-dir", checkpointDir, "--validate-only",
	).CombinedOutput()
	if err != nil || !bytes.Contains(validated, []byte(`"status": "passed"`)) ||
		!bytes.Contains(validated, []byte(`"source_kind": "google_drive_folder"`)) {
		t.Fatalf("validate existing checkpoint: %v\n%s", err, validated)
	}
	invalid, err := exec.Command(
		python, acquirePath, "--folder-url", "https://drive.google.com/uc?id=folder-id",
		"--output-dir", checkpointDir, "--validate-only",
	).CombinedOutput()
	if err == nil || !bytes.Contains(invalid, []byte("a folder ID is not a file ID")) {
		t.Fatalf("direct-file rewrite was not rejected: %v\n%s", err, invalid)
	}
}
