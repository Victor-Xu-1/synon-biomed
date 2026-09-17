package localconda

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"synon-go/internal/software"
)

func TestBuildPythonHarnessKeepsArgumentsOutOfSource(t *testing.T) {
	request := software.Request{
		Capability: "text-processing", Language: "python", Executable: "python",
		Arguments: []string{"-c", "print('sensitive argument')"},
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-1", Executable: "python", Local: true, Verified: true, Preflight: true, Disposition: software.ProvisionDispositionReused}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(harness, "sensitive argument") || !strings.Contains(harness, ResultPrefix) || !strings.Contains(harness, "shell=False") {
		t.Fatal("execution payload was not safely embedded in the fixed argv harness")
	}
}

func TestPythonHarnessRunsWithoutAnImplicitDeadline(t *testing.T) {
	receipt, _, err := runTestPythonHarness(t, software.Request{
		Capability: "unbounded-runtime", Language: "python", Executable: "python3",
		Arguments: []string{"-c", "print('completed-without-deadline')"},
	})
	if err != nil || !receipt.OK || receipt.TimedOut || receipt.Code != "completed" ||
		!strings.Contains(receipt.Stdout.Text, "completed-without-deadline") {
		t.Fatalf("unbounded runtime receipt=%#v err=%v", receipt, err)
	}
}

func TestBuildPythonHarnessReferencesDirectPythonScriptForKernelPreflight(t *testing.T) {
	request := software.Request{
		Capability: "python-script-preflight", Language: "python", Executable: "python3",
		Arguments: []string{"analysis/main.py"}, TimeoutSeconds: 30,
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-script-preflight",
		Executable: "python3", Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionReused,
	}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(harness, "if False:\n    open(\"analysis/main.py\", encoding=\"utf-8\")") {
		t.Fatal("direct Python script is not bound to managed-interpreter preflight")
	}

	moduleRequest := request
	moduleRequest.Arguments = []string{"-m", "analysis"}
	modulePlan, err := resolver.Resolve(moduleRequest)
	if err != nil {
		t.Fatal(err)
	}
	provision.Environment = modulePlan.Environment
	provision.Generation = "gen-module-preflight"
	harness, err = BuildPythonHarness(modulePlan, provision)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(harness, "open(\"analysis/main.py\"") || !strings.Contains(harness, "\npass\n\n_request") {
		t.Fatal("non-script Python invocation received a guessed source sentinel")
	}
}

func TestBuildPythonHarnessRejectsTamperedPlanAuthority(t *testing.T) {
	request := software.Request{Capability: "text-processing", Language: "python", Executable: "python", Arguments: []string{"-V"}}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-1", Executable: "python", Local: true, Verified: true, Preflight: true, Disposition: software.ProvisionDispositionReused}
	plan.Request.Arguments = []string{"-c", "print('tampered')"}
	if _, err := BuildPythonHarness(plan, provision); err == nil {
		t.Fatal("tampered plan request was accepted against the original digest")
	}
}

func TestValidatePythonHarnessBindsCanonicalRequestAndGeneration(t *testing.T) {
	request := software.Request{
		Capability: "launcher-authority", Language: "python", Executable: "python",
		Arguments: []string{"-c", "print('bound')"}, TimeoutSeconds: 45,
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: plan.Environment, Generation: "generation-bound",
		Executable: "python", Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionReused,
	}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePythonHarness(request, plan.Environment, harness); err != nil {
		t.Fatalf("valid launcher rejected: %v", err)
	}
	tamperedRequest := request
	tamperedRequest.Arguments = []string{"-c", "print('changed')"}
	if err := ValidatePythonHarness(tamperedRequest, plan.Environment, harness); err == nil {
		t.Fatal("launcher was accepted for changed argv")
	}
	if err := ValidatePythonHarness(request, plan.Environment, harness+"\nprint('changed')"); err == nil {
		t.Fatal("modified launcher source was accepted")
	}
}

func TestPythonHarnessBindsCompatibleInventoryReuse(t *testing.T) {
	request := software.Request{
		Capability: "compatible-reuse", Language: "python", Executable: "python3",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "numpy"}},
		Imports:  []string{"numpy"},
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	plan.Environment = "swr-existing"
	plan.CompatibleReuse = true
	provision := software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: plan.Environment, Generation: "generation-existing",
		Executable: request.Executable, Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionReused,
	}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePythonHarness(request, plan.Environment, harness); err != nil {
		t.Fatalf("compatible reuse launcher did not retain authority: %v", err)
	}
	plan.CompatibleReuse = false
	if _, err := BuildPythonHarness(plan, provision); err == nil {
		t.Fatal("foreign environment was accepted without compatible inventory authority")
	}
}

type testDescriptorProvider struct{}

func (testDescriptorProvider) Descriptor() software.ProviderDescriptor {
	return software.ProviderDescriptor{ID: ProviderID, Priority: 1, Local: true, Languages: []string{"python"}, PackageManagers: []software.PackageManager{software.PackageManagerConda, software.PackageManagerPip}, Capabilities: []string{"*"}}
}

func TestParseExecutionReceipt(t *testing.T) {
	stdout := "noise\n" + ResultPrefix + `{"ok":true,"code":"completed","provider_id":"local-conda","environment":"swr-a","generation":"g","request_digest":"d","provisioning":"reused","executable":"python","exit_code":0,"timed_out":false,"started_at":"a","finished_at":"b","stdout":{"text":"42","bytes":2,"sha256":"x","truncated":false},"stderr":{"text":"","bytes":0,"sha256":"y","truncated":false},"outputs":[],"cleanup":{"process_group_terminated":true,"process_tree_terminated":true,"temporary_streams_closed":true}}` + "\n"
	receipt, found, err := ParseExecutionReceipt(stdout)
	if err != nil || !found || !receipt.OK || receipt.Stdout.Text != "42" {
		t.Fatalf("receipt parse failed: receipt=%#v found=%v err=%v", receipt, found, err)
	}
}

func TestPythonHarnessRunsArgvValidatesOutputAndClosesStreams(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	request := software.Request{
		Capability: "text-processing", Language: "python", Executable: "python3",
		Arguments:       []string{"-c", "from pathlib import Path; Path('out.txt').write_text('real-result', encoding='utf-8'); print('42')"},
		ExpectedOutputs: []software.OutputWitness{{Path: "out.txt", MinBytes: 4}}, TimeoutSeconds: 30,
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-live", Executable: "python3", Local: true, Verified: true, Preflight: true, Disposition: software.ProvisionDispositionReused}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", harness)
	command.Dir = t.TempDir()
	prefix := filepath.Dir(filepath.Dir(python))
	command.Env = append(os.Environ(), "CONDA_PREFIX="+prefix)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("harness failed: %v\n%s", err, output)
	}
	receipt, found, err := ParseExecutionReceipt(string(output))
	if err != nil || !found || !receipt.OK || receipt.Code != "completed" || !receipt.Cleanup.ProcessGroupTerminated ||
		!receipt.Cleanup.ProcessTreeTerminated ||
		!receipt.Cleanup.TemporaryStreamsClosed || !strings.Contains(receipt.Stdout.Text, "42") || len(receipt.Outputs) != 1 {
		t.Fatalf("invalid live harness receipt: receipt=%#v found=%v err=%v raw=%s", receipt, found, err, output)
	}
}

func TestPythonHarnessCreatesDeclaredOutputDirectoriesBeforeExecution(t *testing.T) {
	receipt, raw, err := runTestPythonHarness(t, software.Request{
		Capability: "nested-output", Language: "python", Executable: "python3",
		Arguments: []string{
			"-c", "from pathlib import Path; Path('nested/results/out.txt').write_text('ready', encoding='utf-8')",
		},
		ExpectedOutputs: []software.OutputWitness{{Path: "nested/results/out.txt", MinBytes: 5, Format: "text"}},
		TimeoutSeconds:  30,
	})
	if err != nil || !receipt.OK || receipt.Code != "completed" || len(receipt.Outputs) != 1 {
		t.Fatalf("declared output directory was not prepared: receipt=%#v err=%v raw=%s", receipt, err, raw)
	}
}

func runTestPythonHarness(t *testing.T, request software.Request) (ExecutionReceipt, string, error) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-quality-contract",
		Executable: request.Executable, Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionReused,
	}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", harness)
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(), "CONDA_PREFIX="+filepath.Dir(filepath.Dir(python)))
	output, runErr := command.CombinedOutput()
	receipt, found, parseErr := ParseExecutionReceipt(string(output))
	if parseErr != nil || !found {
		t.Fatalf("quality harness receipt parse=%v found=%v raw=%s", parseErr, found, output)
	}
	return receipt, string(output), runErr
}

func TestPythonHarnessParsesTypedOutputsAndExecutableAssertions(t *testing.T) {
	t.Run("malformed XYZ", func(t *testing.T) {
		request := software.Request{
			Capability: "typed-output", Language: "python", Executable: "python3",
			Arguments:       []string{"-c", "from pathlib import Path; Path('bad.xyz').write_text('2\\nbad\\n2\\n\\nH 0 0 0\\nH 0 0 1\\n', encoding='utf-8')"},
			ExpectedOutputs: []software.OutputWitness{{Path: "bad.xyz", MinRecords: 1}}, TimeoutSeconds: 30,
		}
		receipt, raw, runErr := runTestPythonHarness(t, request)
		if runErr == nil || receipt.OK || receipt.Code != "output_validation_failed" || !strings.Contains(receipt.Stderr.Text, "xyz_atom_record_invalid") {
			t.Fatalf("malformed XYZ receipt=%#v runErr=%v raw=%s", receipt, runErr, raw)
		}
	})

	t.Run("false JSON assertion", func(t *testing.T) {
		request := software.Request{
			Capability: "executable-assertion", Language: "python", Executable: "python3",
			Arguments:       []string{"-c", "from pathlib import Path; Path('validation.json').write_text('{\\\"checks\\\":{\\\"finite\\\":false}}', encoding='utf-8')"},
			ExpectedOutputs: []software.OutputWitness{{Path: "validation.json", RequiredJSONTrue: []string{"/checks/finite"}}}, TimeoutSeconds: 30,
		}
		receipt, raw, runErr := runTestPythonHarness(t, request)
		if runErr == nil || receipt.OK || receipt.Code != "output_validation_failed" || !strings.Contains(receipt.Stderr.Text, "json_assertion_not_true:/checks/finite") {
			t.Fatalf("false assertion receipt=%#v runErr=%v raw=%s", receipt, runErr, raw)
		}
	})
}

func TestPythonHarnessRequiresAndEnforcesComparisonBasis(t *testing.T) {
	script := "from pathlib import Path; Path('results.csv').write_text('name,formula,method,relative_energy\\na,C2H6O,b3lyp,1.0\\nb,C3H6O,b3lyp,2.0\\n', encoding='utf-8')"
	base := software.Request{
		Capability: "derived-comparison", Language: "python", Executable: "python3",
		Arguments:       []string{"-c", script},
		ExpectedOutputs: []software.OutputWitness{{Path: "results.csv", MinRecords: 2}}, TimeoutSeconds: 30,
	}
	receipt, raw, runErr := runTestPythonHarness(t, base)
	if runErr == nil || receipt.OK || receipt.Code != "quality_contract_failed" || !strings.Contains(receipt.Stderr.Text, "comparison_contract_missing") {
		t.Fatalf("missing comparison contract receipt=%#v runErr=%v raw=%s", receipt, runErr, raw)
	}
	base.Comparisons = []software.TabularComparisonContract{{
		Path: "results.csv", DerivedColumns: []string{"relative_energy"}, BasisColumns: []string{"formula", "method"},
	}}
	receipt, raw, runErr = runTestPythonHarness(t, base)
	if runErr == nil || receipt.OK || receipt.Code != "quality_contract_failed" ||
		!strings.Contains(receipt.Stderr.Text, "comparison_basis_not_identical") ||
		!strings.Contains(receipt.Stderr.Text, "group_columns=<all_rows>") ||
		!strings.Contains(receipt.Stderr.Text, "max_basis_variants=2") {
		t.Fatalf("incompatible comparison receipt=%#v runErr=%v raw=%s", receipt, runErr, raw)
	}
	nonNumeric := base
	nonNumeric.Arguments = []string{"-c", "from pathlib import Path; Path('results.csv').write_text('name,normalized_smiles\\na,CCO\\nb,CCN\\n', encoding='utf-8')"}
	nonNumeric.Comparisons = nil
	receipt, raw, runErr = runTestPythonHarness(t, nonNumeric)
	if runErr != nil || !receipt.OK || receipt.Code != "completed" {
		t.Fatalf("non-numeric normalized label was mistaken for a comparison: receipt=%#v runErr=%v raw=%s", receipt, runErr, raw)
	}
}

func TestPythonHarnessRecognizesPercentAbbreviationComparisonColumns(t *testing.T) {
	request := software.Request{
		Capability: "percent-comparison-diagnostics", Language: "python", Executable: "python3",
		Arguments:       []string{"-c", "from pathlib import Path; Path('results.csv').write_text('name,change_pct_low,auc_dev_pct\\na,-5.0,1.0\\nb,3.0,-2.0\\n', encoding='utf-8')"},
		ExpectedOutputs: []software.OutputWitness{{Path: "results.csv", MinRecords: 2}},
		TimeoutSeconds:  30,
	}
	receipt, raw, runErr := runTestPythonHarness(t, request)
	if runErr == nil || receipt.OK || receipt.Code != "quality_contract_failed" ||
		!strings.Contains(receipt.Stderr.Text, "results.csv:change_pct_low,auc_dev_pct") {
		t.Fatalf("percent comparison contract receipt=%#v runErr=%v raw=%s", receipt, runErr, raw)
	}
}

func TestPythonHarnessDoesNotTreatIterationIdentifierAsRatio(t *testing.T) {
	receipt, raw, err := runTestPythonHarness(t, software.Request{
		Capability: "identifier-column", Language: "python", Executable: "python3",
		Arguments: []string{
			"-c", "from pathlib import Path; Path('rows.csv').write_text('iteration,value\\n1,2.0\\n2,3.0\\n', encoding='utf-8')",
		},
		ExpectedOutputs: []software.OutputWitness{{Path: "rows.csv", Format: "csv", MinRecords: 2}},
		TimeoutSeconds:  30,
	})
	if err != nil || !receipt.OK || receipt.Code != "completed" {
		t.Fatalf("iteration identifier triggered a comparison contract: receipt=%#v err=%v raw=%s", receipt, err, raw)
	}
}

func TestPythonHarnessProvidesCJKMatplotlibDefaults(t *testing.T) {
	request := software.Request{
		Capability: "matplotlib-font-defaults", Language: "python", Executable: "python3",
		Arguments:      []string{"-c", "import os; from pathlib import Path; print((Path(os.environ['MPLCONFIGDIR'])/'matplotlibrc').read_text(encoding='utf-8'))"},
		TimeoutSeconds: 30,
	}
	receipt, raw, runErr := runTestPythonHarness(t, request)
	if runErr != nil || !receipt.OK || receipt.Code != "completed" ||
		!strings.Contains(receipt.Stdout.Text, "Noto Sans CJK SC") ||
		!strings.Contains(receipt.Stdout.Text, "axes.unicode_minus: False") {
		t.Fatalf("matplotlib defaults receipt=%#v runErr=%v raw=%s", receipt, runErr, raw)
	}
}

func TestPythonHarnessReportsEveryMissingComparisonContract(t *testing.T) {
	script := "from pathlib import Path; Path('first.csv').write_text('name,score_ratio\\na,1.0\\nb,2.0\\n', encoding='utf-8'); Path('second.csv').write_text('name,delta_value\\na,0.0\\nb,1.0\\n', encoding='utf-8')"
	request := software.Request{
		Capability: "derived-comparison-diagnostics", Language: "python", Executable: "python3",
		Arguments: []string{"-c", script},
		ExpectedOutputs: []software.OutputWitness{
			{Path: "first.csv", MinRecords: 2},
			{Path: "second.csv", MinRecords: 2},
		},
		TimeoutSeconds: 30,
	}
	receipt, raw, runErr := runTestPythonHarness(t, request)
	if runErr == nil || receipt.OK || receipt.Code != "quality_contract_failed" {
		t.Fatalf("missing comparison contracts receipt=%#v runErr=%v raw=%s", receipt, runErr, raw)
	}
	for _, diagnostic := range []string{
		"first.csv:score_ratio",
		"second.csv:delta_value",
	} {
		if !strings.Contains(receipt.Stderr.Text, diagnostic) {
			t.Fatalf("missing aggregate diagnostic %q in stderr=%q", diagnostic, receipt.Stderr.Text)
		}
	}
}

func TestPythonHarnessBoundsModelVisibleStreamsAndKeepsFullDigest(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	request := software.Request{
		Capability: "bounded-output", Language: "python", Executable: "python3",
		Arguments: []string{"-c", "print('x' * 70000)"}, TimeoutSeconds: 30,
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-bounded-output",
		Executable: "python3", Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionReused,
	}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", harness)
	command.Dir = t.TempDir()
	prefix := filepath.Dir(filepath.Dir(python))
	command.Env = append(os.Environ(), "CONDA_PREFIX="+prefix)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("bounded output harness failed: %v\n%s", err, output)
	}
	receipt, found, err := ParseExecutionReceipt(string(output))
	if err != nil || !found || !receipt.OK || !receipt.Stdout.Truncated ||
		receipt.Stdout.Bytes != 70001 || len([]byte(receipt.Stdout.Text)) != 65536 || len(receipt.Stdout.SHA256) != 64 {
		t.Fatalf("bounded stream receipt=%#v found=%v err=%v", receipt.Stdout, found, err)
	}
}

func TestPythonHarnessEmitsScientificEvidenceFromObservedFiles(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	workspace := t.TempDir()
	for name, content := range map[string]string{
		"receptor.pdbqt": "ATOM receptor\n",
		"ligands.sdf":    "ligand\n$$$$\n",
		"pipeline.py": `from pathlib import Path
Path("ranked_poses.pdbqt").write_text("MODEL 1\\nENDMDL\\n", encoding="utf-8")
Path("execution.log").write_text("engine completed\\n", encoding="utf-8")
`,
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request := software.Request{
		Capability: "molecular-docking", Language: "python", Executable: "python3",
		Packages:  []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "pip"}},
		Arguments: []string{"pipeline.py"}, TimeoutSeconds: 30,
		ExpectedOutputs: []software.OutputWitness{
			{Path: "ranked_poses.pdbqt", MinBytes: 8}, {Path: "execution.log", MinBytes: 8},
		},
		ScientificEvidence: &software.ScientificEvidenceRequest{
			Engine: "test-engine", EnginePackage: "pip", ScoreKind: "test-score",
			Inputs: []software.ScientificFileWitness{
				{Kind: "receptor", Path: "receptor.pdbqt"}, {Kind: "ligand", Path: "ligands.sdf"},
			},
			Artifacts: []software.ScientificFileWitness{
				{Kind: "ranked-pose", Path: "ranked_poses.pdbqt"}, {Kind: "execution-log", Path: "execution.log"},
			},
			CodePaths: []string{"pipeline.py"},
		},
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: plan.Environment, Generation: strings.Repeat("a", 64),
		Executable: "python3", Local: true, Verified: true, Preflight: true, Disposition: software.ProvisionDispositionReused,
	}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", harness)
	command.Dir = workspace
	prefix := filepath.Dir(filepath.Dir(python))
	command.Env = append(os.Environ(), "CONDA_PREFIX="+prefix)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("scientific harness failed: %v\n%s", err, output)
	}
	receipt, found, err := ParseExecutionReceipt(string(output))
	if err != nil || !found || !receipt.OK || receipt.ScientificEvidence == nil {
		t.Fatalf("scientific receipt=%#v found=%v err=%v raw=%s", receipt, found, err, output)
	}
	evidence := receipt.ScientificEvidence
	if evidence.Engine != "test-engine" || evidence.EnginePackage != "pip" || evidence.EngineVersion == "" ||
		len(evidence.ProfileSHA256) != 64 || len(evidence.CodeSHA256) != 64 ||
		len(evidence.Inputs) != 2 || len(evidence.Artifacts) != 2 ||
		evidence.Artifacts[0].SHA256 != receipt.Outputs[0].SHA256 {
		t.Fatalf("observed scientific evidence=%#v outputs=%#v", evidence, receipt.Outputs)
	}
}

func TestPythonHarnessRunsWithoutOutputWitnesses(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	request := software.Request{
		Capability: "version-probe", Language: "python", Executable: "python3",
		Arguments: []string{"--version"}, TimeoutSeconds: 30,
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-no-outputs",
		Executable: "python3", Local: true, Verified: true, Preflight: true, Disposition: software.ProvisionDispositionReused,
	}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", harness)
	command.Dir = t.TempDir()
	prefix := filepath.Dir(filepath.Dir(python))
	command.Env = append(os.Environ(), "CONDA_PREFIX="+prefix)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("harness without output witnesses failed: %v\n%s", err, output)
	}
	receipt, found, err := ParseExecutionReceipt(string(output))
	if err != nil || !found || !receipt.OK || receipt.Code != "completed" || len(receipt.Outputs) != 0 {
		t.Fatalf("invalid no-output receipt: receipt=%#v found=%v err=%v raw=%s", receipt, found, err, output)
	}
}

func TestPythonHarnessRunsStdinWithNilArguments(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	request := software.Request{
		Capability: "stdin-execution", Language: "python", Executable: "python3",
		Stdin: "print('stdin-ok')\n", TimeoutSeconds: 30,
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-stdin-no-args",
		Executable: "python3", Local: true, Verified: true, Preflight: true, Disposition: software.ProvisionDispositionReused,
	}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", harness)
	command.Dir = t.TempDir()
	prefix := filepath.Dir(filepath.Dir(python))
	command.Env = append(os.Environ(), "CONDA_PREFIX="+prefix)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("stdin harness with nil arguments failed: %v\n%s", err, output)
	}
	receipt, found, err := ParseExecutionReceipt(string(output))
	if err != nil || !found || !receipt.OK || receipt.Code != "completed" || !strings.Contains(receipt.Stdout.Text, "stdin-ok") {
		t.Fatalf("invalid stdin/no-args receipt: receipt=%#v found=%v err=%v raw=%s", receipt, found, err, output)
	}
}

func TestPythonHarnessBoundsNestedNativeThreadPools(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	request := software.Request{
		Capability: "resource-envelope", Language: "python", Executable: "python3",
		Arguments:      []string{"-c", "import json,os; print(json.dumps({k:os.environ.get(k) for k in ['OMP_NUM_THREADS','OMP_THREAD_LIMIT','OPENBLAS_NUM_THREADS','MKL_NUM_THREADS','BLIS_NUM_THREADS','VECLIB_MAXIMUM_THREADS','NUMEXPR_NUM_THREADS','RAYON_NUM_THREADS','OMP_DYNAMIC','OMP_MAX_ACTIVE_LEVELS']},sort_keys=True))"},
		TimeoutSeconds: 30,
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-thread-bounds", Executable: "python3", Local: true, Verified: true, Preflight: true, Disposition: software.ProvisionDispositionReused}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", harness)
	command.Dir = t.TempDir()
	prefix := filepath.Dir(filepath.Dir(python))
	command.Env = append(os.Environ(), "CONDA_PREFIX="+prefix, "OMP_NUM_THREADS=64", "OPENBLAS_NUM_THREADS=64")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("bounded harness failed: %v\n%s", err, output)
	}
	receipt, found, err := ParseExecutionReceipt(string(output))
	if err != nil || !found || !receipt.OK {
		t.Fatalf("bounded harness receipt=%#v found=%v err=%v raw=%s", receipt, found, err, output)
	}
	for _, name := range []string{"OMP_NUM_THREADS", "OMP_THREAD_LIMIT", "OPENBLAS_NUM_THREADS", "MKL_NUM_THREADS", "BLIS_NUM_THREADS", "VECLIB_MAXIMUM_THREADS", "NUMEXPR_NUM_THREADS", "RAYON_NUM_THREADS", "OMP_MAX_ACTIVE_LEVELS"} {
		if !strings.Contains(receipt.Stdout.Text, `"`+name+`": "1"`) {
			t.Fatalf("%s was not bounded in child environment: %s", name, receipt.Stdout.Text)
		}
	}
	if !strings.Contains(receipt.Stdout.Text, `"OMP_DYNAMIC": "FALSE"`) {
		t.Fatalf("OpenMP dynamic expansion remained enabled: %s", receipt.Stdout.Text)
	}
}

func TestPythonHarnessKillsSessionEscapedDescendant(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux subreaper semantics are required")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	script := "import os,time\nfrom pathlib import Path\nchild=os.fork()\nif child==0:\n os.setsid()\n grandchild=os.fork()\n if grandchild==0:\n  Path('escaped.pid').write_text(str(os.getpid()), encoding='ascii')\n  time.sleep(30)\n  os._exit(0)\n while not Path('escaped.pid').exists(): time.sleep(0.01)\n os._exit(0)\nos.waitpid(child,0)\nprint('parent-complete')"
	request := software.Request{
		Capability: "process-cleanup", Language: "python", Executable: "python3",
		Arguments: []string{"-c", script}, ExpectedOutputs: []software.OutputWitness{{Path: "escaped.pid", MinBytes: 1}},
		TimeoutSeconds: 30,
	}
	resolver, err := software.NewResolver(testDescriptorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	provision := software.ProvisionReceipt{ProviderID: ProviderID, Environment: plan.Environment, Generation: "gen-cleanup", Executable: "python3", Local: true, Verified: true, Preflight: true, Disposition: software.ProvisionDispositionReused}
	harness, err := BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	command := exec.Command(python, "-c", harness)
	command.Dir = workdir
	prefix := filepath.Dir(filepath.Dir(python))
	command.Env = append(os.Environ(), "CONDA_PREFIX="+prefix)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("harness failed: %v\n%s", err, output)
	}
	receipt, found, err := ParseExecutionReceipt(string(output))
	if err != nil || !found || !receipt.OK || !receipt.Cleanup.ProcessTreeTerminated {
		t.Fatalf("escaped descendant cleanup receipt=%#v found=%v err=%v raw=%s", receipt, found, err, output)
	}
	rawPID, err := os.ReadFile(filepath.Join(workdir, "escaped.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("escaped descendant %d remains alive: %v", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
