package kernelcontract

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/software"
	"synon-go/internal/software/localconda"
)

func TestCanonicalInputAdmitsOnlyExecutableDurableCalls(t *testing.T) {
	tests := []struct {
		name string
		tool string
		raw  json.RawMessage
		want bool
	}{
		{
			name: "python",
			tool: PythonTool,
			raw:  json.RawMessage(`{"code":"print(1)","environment":"science"}`),
			want: true,
		},
		{
			name: "bash",
			tool: BashTool,
			raw:  json.RawMessage(`{"command":"python analysis.py","environment":"science","human_description":"Running analysis script"}`),
			want: true,
		},
		{
			name: "native software",
			tool: SoftwareRuntimeTool,
			raw:  json.RawMessage(`{"capability":"sequence-stats","language":"native","packages":[{"manager":"conda","spec":"seqkit=2.10.1"}],"executable":"seqkit"}`),
			want: true,
		},
		{
			name: "r import witness is rejected before execution",
			tool: SoftwareRuntimeTool,
			raw:  json.RawMessage(`{"capability":"r-summary","language":"r","packages":[{"manager":"conda","spec":"r-jsonlite=2.0.0"}],"imports":["jsonlite"],"executable":"Rscript"}`),
			want: false,
		},
		{
			name: "unavailable provider",
			tool: SoftwareRuntimeTool,
			raw:  json.RawMessage(`{"capability":"sequence-stats","provider":"fallback-provider","language":"native","packages":[{"manager":"conda","spec":"seqkit"}],"executable":"seqkit"}`),
			want: false,
		},
		{
			name: "duplicate key",
			tool: PythonTool,
			raw:  json.RawMessage(`{"code":"print(1)","code":"print(2)","environment":"science"}`),
			want: false,
		},
		{
			name: "ordinary tool",
			tool: "read_file",
			raw:  json.RawMessage(`{"file_path":"result.json"}`),
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Admitted(test.tool, test.raw); got != test.want {
				t.Fatalf("Admitted(%q)=%t want=%t", test.tool, got, test.want)
			}
		})
	}
}

func TestSoftwareRuntimeWorkingDirBindingIsExactOrFrameOwned(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "synon-frame-workspace")
	alias := filepath.Join(string(filepath.Separator), "tmp", "model-guess", "workspace", "frame-1")
	if !SoftwareRuntimeWorkingDirBound(root, root, root) {
		t.Fatal("exact authorized working directory was rejected")
	}
	if !SoftwareRuntimeWorkingDirBound(alias, root, root) {
		t.Fatal("narrow legacy alias was not bound to the frame workspace")
	}
	if !SoftwareRuntimeWorkingDirBound("relative/workspace", filepath.Join(root, "relative", "workspace"), root) {
		t.Fatal("task-relative working directory was not bound to the frame workspace")
	}
	for _, test := range []struct{ requested, effective, workspace string }{
		{filepath.Join(string(filepath.Separator), "tmp", "arbitrary"), root, root},
		{alias, filepath.Join(root, "nested"), root},
		{alias, root, filepath.Join(string(filepath.Separator), "tmp", "other-frame")},
		{"../escape", filepath.Join(root, "escape"), root},
	} {
		if SoftwareRuntimeWorkingDirBound(test.requested, test.effective, test.workspace) {
			t.Fatalf("unauthorized working-directory equivalence accepted: %#v", test)
		}
	}
}

func TestCanonicalInputNormalizesSoftwareRuntimeOnce(t *testing.T) {
	raw := json.RawMessage(`{"capability":"sequence-stats","language":"native","packages":[{"manager":"conda","spec":"seqkit=2.10.1"}],"executable":"seqkit"}`)
	canonical, environment, err := CanonicalInput(SoftwareRuntimeTool, raw)
	if err != nil || !strings.HasPrefix(environment, "swr-") ||
		strings.Contains(string(canonical), `"timeout_seconds"`) {
		t.Fatalf("canonical=%s environment=%q err=%v", canonical, environment, err)
	}
	second, secondEnvironment, err := CanonicalInput(SoftwareRuntimeTool, canonical)
	if err != nil || string(second) != string(canonical) || secondEnvironment != environment {
		t.Fatalf("second=%s environment=%q err=%v", second, secondEnvironment, err)
	}
}

func TestCanonicalInputAndLauncherBindRegisteredExecutionPack(t *testing.T) {
	execution := sciencecapability.ExecutionRequest{
		ExecutionPackID: "molecular-docking.autodock-vina",
		Inputs:          map[string]string{"receptor": "inputs/receptor.pdb", "ligand": "inputs/ligands.sdf"},
		Parameters: map[string]any{
			"center_x": 1.0, "center_y": 2.0, "center_z": 3.0,
			"size_x": 20.0, "size_y": 20.0, "size_z": 20.0,
		},
		WorkingDir: "run",
	}
	raw, err := sciencecapability.CanonicalExecutionRequestJSON(execution)
	if err != nil {
		t.Fatal(err)
	}
	canonical, environment, err := CanonicalInput(SoftwareRuntimeTool, raw)
	if err != nil || string(canonical) != string(raw) || !strings.HasPrefix(environment, "swr-") {
		t.Fatalf("pack canonical=%s environment=%q err=%v", canonical, environment, err)
	}
	request, _, _, _, err := sciencecapability.BuildSoftwareRequest(execution)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := software.RequestDigest(request)
	launcher, err := localconda.BuildPythonHarness(software.Plan{
		ProviderID: software.LocalProviderID, Request: request, RequestDigest: digest, Environment: environment,
	}, software.ProvisionReceipt{
		ProviderID: software.LocalProviderID, Environment: environment, Generation: "generation-pack",
		Executable: request.Executable, Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionInstalled,
	})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := ValidateSoftwareRuntimeExecutionSource(canonical, environment, launcher)
	if err != nil || validated.Executable != "python" || !strings.Contains(validated.Stdin, "AutoDock Vina execution pack") {
		t.Fatalf("validated request=%#v err=%v", validated, err)
	}
}
