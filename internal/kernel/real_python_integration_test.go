package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validEthanolSDF = `
     RDKit          2D

  3  2  0  0  0  0  0  0  0  0999 V2000
    0.0000    0.0000    0.0000 C   0  0  0  0  0  0  0  0  0  0  0  0
    1.2990    0.7500    0.0000 C   0  0  0  0  0  0  0  0  0  0  0  0
    2.5981   -0.0000    0.0000 O   0  0  0  0  0  0  0  0  0  0  0  0
  1  2  1  0
  2  3  1  0
M  END
$$$$
`

func TestRealBundledManagedPythonRuntimeProvisionsPinnedRDKitAndStrictSDFValidator(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_CONDA_RUNTIME") != "1" {
		t.Skip("set SYNON_RUN_REAL_CONDA_RUNTIME=1 to provision the real content-addressed Python runtime")
	}
	root := repositoryRootForCondaRuntimeTest(t)
	optional := filepath.Join(root, "assets", "optional")
	state := t.TempDir()
	manager := NewManager(Config{
		Python:                   "/usr/bin/python3",
		Micromamba:               filepath.Join(optional, "micromamba", "linux-x86_64", "micromamba"),
		CondaHome:                filepath.Join(state, "conda"),
		CondaEnvsPath:            filepath.Join(state, "conda", "envs"),
		CondaRuntimeCatalog:      filepath.Join(optional, "conda-runtimes", "manifest.json"),
		ManagedPythonEnvironment: defaultManagedPythonEnvironment,
		AssetRoot:                optional,
		ManifestPath:             filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath:               filepath.Join(optional, "kernels", "kernel_worker.py"),
		PythonHelperPath:         filepath.Join(optional, "kernels", "cheminfo_render_helpers.py"),
		SDFValidatorPath:         filepath.Join(optional, "kernels", "sdf_artifact_validator.py"),
		ExecutionTimeout:         15 * time.Second,
	})
	if err := manager.Verify(); err != nil {
		t.Fatalf("verify kernel assets: %v", err)
	}
	provisionContext := t.Context()
	if rawTimeout := strings.TrimSpace(os.Getenv("SYNON_REAL_CONDA_PROVISION_TIMEOUT")); rawTimeout != "" {
		provisionTimeout, err := time.ParseDuration(rawTimeout)
		if err != nil || provisionTimeout <= 0 {
			t.Fatalf("invalid SYNON_REAL_CONDA_PROVISION_TIMEOUT=%q: %v", rawTimeout, err)
		}
		var cancel context.CancelFunc
		provisionContext, cancel = context.WithTimeout(provisionContext, provisionTimeout)
		defer cancel()
	}
	if err := manager.EnsureManagedPythonEnvironment(provisionContext); err != nil {
		t.Fatalf("ensure managed Python runtime: %v", err)
	}
	if !manager.RuntimeReady("python", defaultManagedPythonEnvironment) {
		t.Fatal("managed Python runtime is not ready after verified provisioning")
	}
	if generation, err := manager.ManagedPythonActiveGeneration(); err != nil || generation == "" {
		t.Fatalf("managed Python active generation=%q err=%v", generation, err)
	}
	if err := manager.VerifyManagedEnvironmentImports(provisionContext, defaultManagedPythonEnvironment, []string{"rdkit"}); err != nil {
		t.Fatalf("verify bundled managed Python import witness: %v", err)
	}
	python, validator, generation, rdkitVersion, err := manager.ScientificArtifactValidator()
	if err != nil {
		t.Fatal(err)
	}
	if generation == "" || rdkitVersion != "2024.03.5" {
		t.Fatalf("generation=%q rdkit=%q", generation, rdkitVersion)
	}

	valid := runSDFValidatorForKernelTest(t, python, validator, validEthanolSDF)
	if valid["ok"] != true || valid["code"] != "valid_sdf" || valid["parsedCount"] != float64(1) {
		t.Fatalf("valid SDF result = %#v", valid)
	}
	malformed := strings.Replace(validEthanolSDF, "\n  3  2  0", "\nextra header\n  3  2  0", 1)
	invalid := runSDFValidatorForKernelTest(t, python, validator, malformed)
	if invalid["ok"] != false || invalid["code"] == "valid_sdf" || invalid["parsedCount"] != float64(0) {
		t.Fatalf("malformed SDF result = %#v", invalid)
	}
	validSMILES := runSDFValidatorForKernelTest(t, python, validator, "CCO\tETHANOL\n", "--format", "smi")
	if validSMILES["ok"] != true || validSMILES["code"] != "valid_smiles" || validSMILES["parsedCount"] != float64(1) {
		t.Fatalf("valid SMILES result = %#v", validSMILES)
	}
	if validSMILES["rdkitVersion"] != "2024.03.5" {
		t.Fatalf("valid SMILES omitted canonical rdkitVersion: %#v", validSMILES)
	}
	if _, exists := validSMILES["rdKitVersion"]; exists {
		t.Fatalf("valid SMILES exposed non-contract rdKitVersion: %#v", validSMILES)
	}

	workspace := t.TempDir()
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "real-managed-python-worker", FrameID: "real-managed-python-frame",
		RootFrameID: "real-managed-python-frame", AgentName: "OPERON", KernelKind: "analysis",
		Language: "python", Environment: defaultManagedPythonEnvironment, WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("start real managed Python worker: %v", err)
	}
	executeContext, cancelExecute := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelExecute()
	execution, err := worker.Execute(executeContext, `
from rdkit import Chem
from cheminfo_render_helpers import render_molecule_images
assert Chem.MolFromSmiles("CCO") is not None
result = render_molecule_images(["CCO"], ["ethanol"], out_dir=".")
print(result["grid"])
`, "agent")
	if err != nil {
		t.Fatalf("execute real managed Python helper import: %v", err)
	}
	if execution.Error != "" || !strings.Contains(execution.Stdout, "molecules_2d_grid.png") {
		t.Fatalf("real managed Python helper execution = %#v", execution)
	}
	if info, err := os.Stat(filepath.Join(workspace, "molecules_2d_grid.png")); err != nil || info.Size() == 0 {
		t.Fatalf("real managed Python rendered artifact info=%#v err=%v", info, err)
	}
	if jsonArtifacts, err := filepath.Glob(filepath.Join(workspace, "*.json")); err != nil {
		t.Fatalf("find default JSON artifacts: %v", err)
	} else if len(jsonArtifacts) != 0 {
		t.Fatalf("default cheminfo helper created JSON artifacts: %v", jsonArtifacts)
	}
	closeContext, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClose()
	if _, err := manager.CloseAll(closeContext); err != nil {
		t.Fatalf("close real managed Python worker: %v", err)
	}
}

func runSDFValidatorForKernelTest(t *testing.T, python, validator, input string, arguments ...string) map[string]any {
	t.Helper()
	commandArguments := append([]string{"-I", validator}, arguments...)
	command := exec.Command(python, commandArguments...)
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err != nil {
		var exitError *exec.ExitError
		if !strings.Contains(input, "extra header") || !errors.As(err, &exitError) || exitError.ExitCode() != 2 {
			t.Fatalf("validator failed: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
		}
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode validator output: %v output=%q", err, stdout.String())
	}
	return result
}
