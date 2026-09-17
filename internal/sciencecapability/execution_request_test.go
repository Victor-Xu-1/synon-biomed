package sciencecapability

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"synon-go/internal/software"
)

func validVinaExecutionRequest() ExecutionRequest {
	return ExecutionRequest{
		ExecutionPackID: "molecular-docking.autodock-vina",
		Inputs:          map[string]string{"receptor": "inputs/receptor.pdbqt", "ligand": "inputs/ligands.sdf"},
		Parameters: map[string]any{
			"center_x": 1.5, "center_y": 2.5, "center_z": 3.5,
			"size_x": 20, "size_y": 21, "size_z": 22,
		},
		WorkingDir: "run", TimeoutSeconds: 3600,
	}
}

func TestBundledVinaExecutionPackBootstrapsReviewedModules(t *testing.T) {
	request, _, _, _, err := BuildSoftwareRequest(validVinaExecutionRequest())
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", "-c", `
import sys
import types
sys.dont_write_bytecode = True
sys.modules["gemmi"] = types.ModuleType("gemmi")
chem = types.ModuleType("rdkit.Chem")
chem.AllChem = types.SimpleNamespace()
rdkit = types.ModuleType("rdkit")
rdkit.Chem = chem
sys.modules["rdkit"] = rdkit
sys.modules["rdkit.Chem"] = chem
sys.argv = ["autodock_vina.py", "--help"]
exec(compile(sys.stdin.read(), "bundled-autodock-vina.py", "exec"))
`)
	command.Stdin = strings.NewReader(request.Stdin)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--receptor") || !strings.Contains(string(output), "--ligand") {
		t.Fatalf("execute bundled modular Vina pack: %v\n%s", err, output)
	}
}

func TestBuildSoftwareRequestExpandsOnlyRegisteredExecutionPack(t *testing.T) {
	request, definition, engine, scriptSHA, err := BuildSoftwareRequest(validVinaExecutionRequest())
	if err != nil {
		t.Fatal(err)
	}
	if definition.ID != "molecular-docking" || engine.ID != "autodock-vina" || request.Capability != definition.ID ||
		request.Provider != "local-conda" || request.Language != "python" || request.Executable != "python" ||
		len(scriptSHA) != 64 || !strings.Contains(request.Stdin, "Governed AutoDock Vina execution pack") ||
		!strings.Contains(request.Stdin, `ModuleType("autodock_vina_inputs")`) ||
		!strings.Contains(request.Stdin, `ModuleType("autodock_vina_outputs")`) ||
		!strings.Contains(request.Stdin, `ModuleType("autodock_vina_pockets")`) {
		t.Fatalf("expanded request=%#v definition=%#v engine=%#v script_sha=%q", request, definition, engine, scriptSHA)
	}
	joined := strings.Join(request.Arguments, "\x00")
	for _, required := range []string{"-", "--receptor\x00inputs/receptor.pdbqt", "--ligand\x00inputs/ligands.sdf", "--report-language\x00en", "--seed\x0042", "--repeat-count\x003", "--num-modes\x009"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("expanded argv missing %q: %#v", required, request.Arguments)
		}
	}
	if len(request.Packages) != 7 || len(request.Imports) != 7 || len(request.ExpectedOutputs) != 10 ||
		request.ScientificEvidence == nil || len(request.ScientificEvidence.Inputs) != 2 || len(request.ScientificEvidence.Artifacts) != 3 {
		t.Fatalf("expanded execution authority=%#v", request)
	}
}

func TestBuildExecutionPackRuntimeRequestMatchesVinaExecutionEnvironment(t *testing.T) {
	runtimeRequest, definition, engine, err := BuildExecutionPackRuntimeRequest(
		"molecular-docking.autodock-vina",
		15*60,
	)
	if err != nil {
		t.Fatal(err)
	}
	executionRequest, _, _, _, err := BuildSoftwareRequest(validVinaExecutionRequest())
	if err != nil {
		t.Fatal(err)
	}
	runtimeEnvironment, err := software.EnvironmentName(software.LocalProviderID, runtimeRequest)
	if err != nil {
		t.Fatal(err)
	}
	executionEnvironment, err := software.EnvironmentName(software.LocalProviderID, executionRequest)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeEnvironment != executionEnvironment || definition.ID != "molecular-docking" || engine.ID != "autodock-vina" {
		t.Fatalf("runtime=%q execution=%q definition=%q engine=%q", runtimeEnvironment, executionEnvironment, definition.ID, engine.ID)
	}
	packages := make([]string, 0, len(runtimeRequest.Packages))
	for _, requirement := range runtimeRequest.Packages {
		packages = append(packages, requirement.Spec)
	}
	for _, required := range []string{
		"vina=1.2.7", "meeko=0.8.0", "rdkit=2026.03.1", "gemmi=0.7.5",
		"prody=2.6.1", "biopython=1.88", "openbabel=3.2.1",
	} {
		if !strings.Contains(strings.Join(packages, "\x00"), required) {
			t.Fatalf("runtime packages missing %q: %v", required, packages)
		}
	}
}

func TestExecutionRequestRejectsModelAuthoredRuntimeAndUnavailablePack(t *testing.T) {
	raw := []byte(`{"execution_pack_id":"molecular-docking.autodock-vina","inputs":{"receptor":"r.pdb","ligand":"l.sdf"},"parameters":{},"packages":[{"manager":"conda","spec":"invented"}]}`)
	if _, _, _, err := DecodeExecutionRequestJSON(raw); err == nil {
		t.Fatal("model-authored package route was accepted")
	}
	_, _, _, err := NormalizeExecutionRequest(ExecutionRequest{
		ExecutionPackID: "molecular-docking.diffdock",
		Inputs:          map[string]string{"receptor": "r.pdb", "ligand": "l.sdf"}, Parameters: map[string]any{},
	})
	var unavailable *UnavailableExecutionPackError
	if !errors.As(err, &unavailable) || unavailable.PackID != "molecular-docking.diffdock" {
		t.Fatalf("unavailable pack error=%#v err=%v", unavailable, err)
	}
}

func TestCanonicalExecutionRequestIsStableAndFillsDefaults(t *testing.T) {
	first, err := CanonicalExecutionRequestJSON(validVinaExecutionRequest())
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExecutionRequest
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Parameters["seed"] != float64(42) || decoded.Parameters["repeat_count"] != float64(3) || decoded.Parameters["exhaustiveness"] != float64(8) || decoded.Parameters["num_modes"] != float64(9) {
		t.Fatalf("canonical defaults=%#v", decoded.Parameters)
	}
	second, err := CanonicalExecutionRequestJSON(decoded)
	if err != nil || string(first) != string(second) {
		t.Fatalf("canonical execution request drifted first=%s second=%s err=%v", first, second, err)
	}
}
