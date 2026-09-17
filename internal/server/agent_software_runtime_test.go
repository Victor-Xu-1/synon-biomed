package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"synon-go/internal/failurecontract"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/kernelcontract"
	"synon-go/internal/software"
	"synon-go/internal/software/localconda"
)

func softwareRuntimeTestInput() map[string]any {
	return map[string]any{
		"capability": "sequence-alignment", "language": "native", "executable": "minimap2",
		"packages": []any{map[string]any{"manager": "conda", "spec": "minimap2"}},
		"args":     []any{"--version"},
	}
}

func softwareRuntimePackTestInput() map[string]any {
	return map[string]any{
		"execution_pack_id": "molecular-docking.autodock-vina",
		"inputs":            map[string]any{"receptor": "inputs/receptor.pdb", "ligand": "inputs/ligands.sdf"},
		"parameters": map[string]any{
			"center_x": 1.0, "center_y": 2.0, "center_z": 3.0,
			"size_x": 20.0, "size_y": 20.0, "size_z": 20.0,
		},
	}
}

type blockingSoftwareProvisioner struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (p *blockingSoftwareProvisioner) Descriptor() software.ProviderDescriptor {
	return software.ProviderDescriptor{
		ID: software.LocalProviderID, Priority: 1000, Local: true,
		Languages: []string{"native"}, PackageManagers: []software.PackageManager{software.PackageManagerConda},
		Capabilities: []string{"*"},
	}
}

func (p *blockingSoftwareProvisioner) Ensure(ctx context.Context, _ software.Plan, _ string) (software.ProvisionReceipt, error) {
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	return software.ProvisionReceipt{}, ctx.Err()
}

func TestInternalSoftwarePackCanonicalizationRemainsAvailableToProductAPIs(t *testing.T) {
	canonical, request, err := canonicalSoftwareRuntimeInput(softwareRuntimePackTestInput())
	wantPackages := []software.PackageRequirement{
		{Manager: software.PackageManagerConda, Spec: "vina=1.2.7"},
		{Manager: software.PackageManagerConda, Spec: "meeko=0.8.0"},
		{Manager: software.PackageManagerConda, Spec: "rdkit=2026.03.1"},
		{Manager: software.PackageManagerConda, Spec: "gemmi=0.7.5"},
		{Manager: software.PackageManagerConda, Spec: "prody=2.6.1"},
		{Manager: software.PackageManagerConda, Spec: "biopython=1.88"},
		{Manager: software.PackageManagerConda, Spec: "openbabel=3.2.1"},
	}
	if err != nil || request.TimeoutSeconds != 0 || !strings.Contains(string(canonical), `"execution_pack_id":"molecular-docking.autodock-vina"`) ||
		strings.Contains(string(canonical), `"packages"`) || request.Executable != "python" ||
		!slices.Equal(request.Packages, wantPackages) ||
		!slices.Equal(request.Channels, []string{"conda-forge"}) ||
		!slices.Equal(request.Imports, []string{"vina", "meeko", "rdkit", "gemmi", "prody", "Bio", "openbabel"}) {
		t.Fatalf("canonical software input=%s request=%#v err=%v", canonical, request, err)
	}
}

func TestSoftwareRuntimePackValidationNeverFallsThroughTheRetiredRequestDecoder(t *testing.T) {
	missingLigand := softwareRuntimePackTestInput()
	missingLigand["inputs"] = map[string]any{"receptor": "inputs/receptor.pdb"}
	for _, decode := range []func(map[string]any) error{
		func(input map[string]any) error {
			_, err := decodeSoftwareRuntimeRequest(input)
			return err
		},
		func(input map[string]any) error {
			_, _, err := canonicalSoftwareRuntimeInput(input)
			return err
		},
	} {
		err := decode(missingLigand)
		if err == nil || !strings.Contains(err.Error(), "scientific execution inputs do not match the pack") ||
			strings.Contains(err.Error(), "unknown field \"execution_pack_id\"") {
			t.Fatalf("pack validation error=%v", err)
		}
	}

	unknownParameter := softwareRuntimePackTestInput()
	unknownParameter["parameters"].(map[string]any)["only_prepare_receptor"] = true
	if _, err := decodeSoftwareRuntimeRequest(unknownParameter); err == nil ||
		!strings.Contains(err.Error(), `scientific execution parameter "only_prepare_receptor" is not registered`) {
		t.Fatalf("unknown pack parameter error=%v", err)
	}
}

func TestSoftwareRuntimeNetworkIsolationIsAnActionableStructuredFailure(t *testing.T) {
	if !softwareRuntimeNetworkIsolationFailure(localconda.ExecutionReceipt{
		Stderr: localconda.StreamReceipt{Text: "URLError: <urlopen error [Errno -3] Temporary failure in name resolution>"},
	}) {
		t.Fatal("network-isolated execution was not recognized")
	}
	if softwareRuntimeNetworkIsolationFailure(localconda.ExecutionReceipt{
		OK:     false,
		Stderr: localconda.StreamReceipt{Text: "Traceback: invalid input format"},
	}) {
		t.Fatal("ordinary command failure was misclassified as network isolation")
	}
}

func TestSoftwareRuntimeRecoveryBudgetCoversProvisionAndExecution(t *testing.T) {
	if unlimited, err := kernelLocalOperationRecoveryTimeout(softwareRuntimeToolName, softwareRuntimePackTestInput()); err != nil || unlimited != 0 {
		t.Fatalf("unlimited software recovery budget=%s err=%v", unlimited, err)
	}
	input := softwareRuntimePackTestInput()
	input["timeout_seconds"] = float64(3600)
	got, err := kernelLocalOperationRecoveryTimeout(softwareRuntimeToolName, input)
	want := 2*time.Hour + softwareRuntimeExecutionTimeoutGrace
	if err != nil || got != want {
		t.Fatalf("software recovery budget=%s err=%v want=%s", got, err, want)
	}
	if ordinary, err := kernelLocalOperationRecoveryTimeout("python", map[string]any{}); err != nil || ordinary != defaultSessionRunnerKernelRecoveryTimeout {
		t.Fatalf("ordinary recovery budget=%s err=%v", ordinary, err)
	}
}

func TestEnvironmentManagementSchemasRequireReadySupervisorAndHideInternalPack(t *testing.T) {
	root := t.TempDir()
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Micromamba:          filepath.Join(root, "micromamba"),
		CondaHome:           filepath.Join(root, "conda"),
		CondaEnvsPath:       filepath.Join(root, "conda", "envs"),
		CondaRuntimeCatalog: filepath.Join(root, "runtime-catalog.json"),
	})
	server := &Server{kernelManager: manager}
	identity := &agentKernelContext{workspaceDir: root}
	before := agentRuntimeToolSchemaNames(server.agentKernelToolSchemas(identity, nil))
	if containsAgentToolName(before, softwareRuntimeToolName) || containsAgentToolName(before, manageEnvironmentsToolName) ||
		containsAgentToolName(before, managePackagesToolName) {
		t.Fatalf("software tools were advertised before the supervisor was ready: %v", before)
	}

	supervisorContext, cancelSupervisor := context.WithCancel(context.Background())
	supervisorDone := make(chan error, 1)
	go func() {
		supervisorDone <- manager.RunManagedEnvironmentSupervisor(supervisorContext)
	}()
	t.Cleanup(func() {
		cancelSupervisor()
		select {
		case <-supervisorDone:
		case <-time.After(2 * time.Second):
			t.Fatal("managed environment supervisor did not stop")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for !manager.ManagedEnvironmentSupervisorReady() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	after := agentRuntimeToolSchemaNames(server.agentKernelToolSchemas(identity, nil))
	if !containsAgentToolName(after, manageEnvironmentsToolName) || !containsAgentToolName(after, managePackagesToolName) {
		t.Fatalf("reference-compatible environment tools were not advertised after supervisor readiness: %v", after)
	}
	if containsAgentToolName(after, softwareRuntimeToolName) {
		t.Fatalf("internal scientific-pack runtime leaked into the model tool set: %v", after)
	}
}

func TestSoftwareRuntimeRejectsNonDurableExecutionBypass(t *testing.T) {
	_, err := (&Server{}).executeAgentKernelTool(
		context.Background(), &agentKernelContext{}, softwareRuntimeToolName, softwareRuntimeTestInput(),
	)
	if err == nil || !strings.Contains(err.Error(), "durable agent tool-call authority") {
		t.Fatalf("non-durable software runtime bypass error=%v", err)
	}
}

func TestSoftwareRuntimeKernelIDIsStableAndOperationScoped(t *testing.T) {
	first := softwareRuntimeKernelID("operation-a")
	if first != softwareRuntimeKernelID("operation-a") || first == softwareRuntimeKernelID("operation-b") ||
		!strings.HasPrefix(first, "kernel-swr-") {
		t.Fatalf("software runtime kernel ids are not stable and operation-scoped: %q", first)
	}
}

func TestSoftwareRuntimeRelativeWorkingDirKeepsDurableLauncherAuthority(t *testing.T) {
	workspace := t.TempDir()
	relativeWorkingDir := filepath.Join("analysis", "model")
	absoluteWorkingDir := filepath.Join(workspace, relativeWorkingDir)
	if err := os.MkdirAll(absoluteWorkingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authorityInput := softwareRuntimePackTestInput()
	authorityInput["working_dir"] = relativeWorkingDir
	executionInput, resolvedWorkingDir, err := (&Server{}).normalizeAgentKernelWorkingDirInput(
		"owner", workspace, authorityInput,
	)
	if err != nil || resolvedWorkingDir != absoluteWorkingDir ||
		stringValue(executionInput["working_dir"]) != absoluteWorkingDir ||
		stringValue(authorityInput["working_dir"]) != relativeWorkingDir {
		t.Fatalf("authority=%#v execution=%#v resolved=%q err=%v", authorityInput, executionInput, resolvedWorkingDir, err)
	}

	raw, request, err := canonicalSoftwareRuntimeInput(authorityInput)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := software.RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := software.EnvironmentName(software.LocalProviderID, request)
	if err != nil {
		t.Fatal(err)
	}
	plan := software.Plan{
		ProviderID: software.LocalProviderID, Request: request, RequestDigest: digest, Environment: environment,
	}
	provision := software.ProvisionReceipt{
		ProviderID: software.LocalProviderID, Environment: environment, Generation: "generation-relative-cwd",
		Executable: request.Executable, Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionReused,
	}
	launcher, err := localconda.BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernelcontract.ValidateSoftwareRuntimeExecutionSource(raw, environment, launcher); err != nil {
		t.Fatalf("model-authored relative working directory lost launcher authority: %v", err)
	}

	// The physical absolute cwd is a confinement detail and is not a valid pack
	// authority. It cannot be re-canonicalized into a competing request.
	if _, _, err := canonicalSoftwareRuntimeInput(executionInput); err == nil {
		t.Fatal("absolute execution cwd was accepted as a registered pack authority")
	}
}

func TestDurableSoftwareRuntimePlanRetainsCheckpointedEnvironmentIdentity(t *testing.T) {
	request, err := software.NormalizeRequest(software.Request{
		Capability: "report-generation", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "pandas"}},
		Imports:  []string{"pandas"},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := software.RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	requested, err := software.EnvironmentName(software.LocalProviderID, request)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := durableSoftwareRuntimePlan(software.Plan{
		ProviderID: software.LocalProviderID, Request: request, RequestDigest: digest,
		Environment: "swr-compatible-superset", CompatibleReuse: true,
	})
	if err != nil || plan.Environment != requested || plan.CompatibleReuse {
		t.Fatalf("durable plan=%#v err=%v requested=%q", plan, err, requested)
	}
}

func TestSoftwareRuntimeProviderFollowsDialogueComputeSelectionWithoutFallback(t *testing.T) {
	provider, err := softwareRuntimeProviderForComputeSelection(localComputeProviderID)
	if err != nil || provider != software.LocalProviderID {
		t.Fatalf("local selection provider=%q err=%v", provider, err)
	}
	for _, selected := range []string{"", "ssh:gpu-cluster", "byoc:modal"} {
		provider, err = softwareRuntimeProviderForComputeSelection(selected)
		if err == nil || provider != "" {
			t.Fatalf("unsupported selection %q silently resolved to provider=%q err=%v", selected, provider, err)
		}
	}
}

func TestSoftwareRuntimeProvisioningTimeoutCancelsSelectedProvider(t *testing.T) {
	provider := &blockingSoftwareProvisioner{started: make(chan struct{}), cancelled: make(chan struct{})}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	request := software.Request{
		Capability: "semiempirical-quantum", Provider: software.LocalProviderID, Language: "native",
		Packages:   []software.PackageRequirement{{Manager: software.PackageManagerConda, Spec: "xtb"}},
		Executable: "xtb", Arguments: []string{"--version"}, TimeoutSeconds: 1,
	}
	plan, err := controller.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	_, _, err = (&Server{}).provisionSoftwareRuntime(context.Background(), controller, plan, "operation-timeout")
	var operationErr *software.OperationError
	if err == nil || !errors.As(err, &operationErr) || operationErr.Code != "software_install_timeout" ||
		operationErr.Retryable || operationErr.Kind != failurecontract.Transient || operationErr.RepairScope != "same_provider_plan" ||
		!strings.Contains(operationErr.Recovery, "larger_timeout_seconds") ||
		!strings.Contains(err.Error(), "installer process tree was cancelled") {
		t.Fatalf("provisioning timeout error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed < time.Second || elapsed > 3*time.Second {
		t.Fatalf("provisioning timeout elapsed = %s", elapsed)
	}
	select {
	case <-provider.started:
	default:
		t.Fatal("provider did not start")
	}
	select {
	case <-provider.cancelled:
	default:
		t.Fatal("provider context was not cancelled")
	}
}

func TestSoftwareRuntimeVisibleResultValidatesDurablePlanAndLauncher(t *testing.T) {
	raw, request, err := canonicalSoftwareRuntimeInput(softwareRuntimeTestInput())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := software.RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := software.EnvironmentName(software.LocalProviderID, request)
	if err != nil {
		t.Fatal(err)
	}
	plan := software.Plan{ProviderID: software.LocalProviderID, Request: request, RequestDigest: digest, Environment: environment}
	provision := software.ProvisionReceipt{
		ProviderID: software.LocalProviderID, Environment: environment, Generation: "generation-a",
		Executable: request.Executable, Local: true, Verified: true,
		Preflight: true, Disposition: software.ProvisionDispositionReused,
	}
	launcher, err := localconda.BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	receipt := localconda.ExecutionReceipt{
		OK: true, Code: "completed", ProviderID: software.LocalProviderID, Environment: environment,
		Generation: "generation-a", RequestDigest: digest, Provisioning: software.ProvisionDispositionReused,
		Executable: request.Executable, ExitCode: 0,
		StartedAt: "2026-08-15T00:00:00Z", FinishedAt: "2026-08-15T00:00:01Z",
		Stdout:  localconda.StreamReceipt{Text: "minimap2 2.28", Bytes: 13, SHA256: "stdout"},
		Stderr:  localconda.StreamReceipt{SHA256: "stderr"},
		Cleanup: localconda.CleanupReceipt{ProcessGroupTerminated: true, ProcessTreeTerminated: true, TemporaryStreamsClosed: true},
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := softwareRuntimeVisibleResult(raw, environment, "generation-a", launcher, map[string]any{
		"stdout": localconda.ResultPrefix + string(encoded), "stderr": "", "exec_id": "exec-a", "kernel_id": "kernel-a", "cell_index": 1,
	})
	if err != nil || visible["ok"] != true || visible["stdout"] != "minimap2 2.28" || visible["exit_status"] != "ok" {
		t.Fatalf("visible=%#v err=%v", visible, err)
	}
	if _, err := softwareRuntimeVisibleResult(raw, environment, "generation-b", launcher, map[string]any{"stdout": localconda.ResultPrefix + string(encoded)}); err == nil {
		t.Fatal("runtime generation conflict was accepted")
	}
	if _, err := softwareRuntimeVisibleResult(raw, environment, "generation-a", launcher+"\n# tampered", map[string]any{"stdout": localconda.ResultPrefix + string(encoded)}); err == nil {
		t.Fatal("durable launcher conflict was accepted")
	}
}

func TestSoftwareRuntimeVisibleExecutionResultPreservesWorkerCodePreflight(t *testing.T) {
	preflight := map[string]any{
		"ok": true, "status": "code_preflight_required", "executed": false,
		"preflight": map[string]any{
			"schema": "synon.python-code-preflight.v1", "status": "code_preflight_required",
			"executed": false, "diagnostics": []any{map[string]any{"code": "python_imported_api_unavailable"}},
		},
	}
	visible, err := softwareRuntimeVisibleExecutionResult(nil, "", "", "", preflight)
	if err != nil || visible["status"] != "code_preflight_required" || visible["executed"] != false ||
		visible["code"] == "software_runtime_host_failed" {
		t.Fatalf("worker preflight was misclassified: visible=%#v err=%v", visible, err)
	}
}

func TestSoftwareRuntimeVisibleResultAcceptsBoundCompatibleReuse(t *testing.T) {
	request, err := software.NormalizeRequest(software.Request{
		Capability: "compatible-reuse", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "numpy"}},
		Imports:  []string{"numpy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := software.CanonicalRequestJSON(request)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := software.RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	plan := software.Plan{
		ProviderID: software.LocalProviderID, Request: request, RequestDigest: digest,
		Environment: "swr-existing", CompatibleReuse: true,
	}
	provision := software.ProvisionReceipt{
		ProviderID: software.LocalProviderID, Environment: plan.Environment, Generation: "generation-existing",
		Executable: request.Executable, Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionReused,
	}
	launcher, err := localconda.BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	receipt := localconda.ExecutionReceipt{
		OK: true, Code: "completed", ProviderID: software.LocalProviderID, Environment: plan.Environment,
		Generation: provision.Generation, RequestDigest: digest, Provisioning: software.ProvisionDispositionReused,
		Executable: request.Executable, ExitCode: 0,
		StartedAt: "2026-08-17T00:00:00Z", FinishedAt: "2026-08-17T00:00:01Z",
		Stdout:  localconda.StreamReceipt{SHA256: strings.Repeat("1", 64)},
		Stderr:  localconda.StreamReceipt{SHA256: strings.Repeat("2", 64)},
		Cleanup: localconda.CleanupReceipt{ProcessGroupTerminated: true, ProcessTreeTerminated: true, TemporaryStreamsClosed: true},
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := softwareRuntimeVisibleResult(raw, plan.Environment, provision.Generation, launcher, map[string]any{
		"stdout": localconda.ResultPrefix + string(encoded), "stderr": "", "exec_id": "exec-compatible",
	})
	if err != nil || visible["ok"] != true || visible["environment"] != "swr-existing" || visible["provisioning"] != "reused" {
		t.Fatalf("compatible reuse visible=%#v err=%v", visible, err)
	}
}

func TestSoftwareRuntimeFailedScientificRunRemainsVisibleForRepair(t *testing.T) {
	request, err := software.NormalizeRequest(software.Request{
		Capability: "generic-scientific-compute", Language: "python", Executable: "python",
		Packages:        []software.PackageRequirement{{Manager: software.PackageManagerConda, Spec: "python"}},
		ExpectedOutputs: []software.OutputWitness{{Path: "result.dat", MinBytes: 1}},
		ScientificEvidence: &software.ScientificEvidenceRequest{
			Engine: "generic-engine", EnginePackage: "python", ScoreKind: "energy-kcal-mol",
			Inputs:    []software.ScientificFileWitness{{Kind: "input-data", Path: "input.dat"}},
			Artifacts: []software.ScientificFileWitness{{Kind: "result-data", Path: "result.dat"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := software.CanonicalRequestJSON(request)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := software.RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := software.EnvironmentName(software.LocalProviderID, request)
	if err != nil {
		t.Fatal(err)
	}
	plan := software.Plan{ProviderID: software.LocalProviderID, Request: request, RequestDigest: digest, Environment: environment}
	provision := software.ProvisionReceipt{
		ProviderID: software.LocalProviderID, Environment: environment, Generation: "generation-failed",
		Executable: request.Executable, Local: true, Verified: true,
		Preflight: true, Disposition: software.ProvisionDispositionInstalled,
	}
	launcher, err := localconda.BuildPythonHarness(plan, provision)
	if err != nil {
		t.Fatal(err)
	}
	receipt := localconda.ExecutionReceipt{
		OK: false, Code: "nonzero_exit", ProviderID: software.LocalProviderID, Environment: environment,
		Generation: "generation-failed", RequestDigest: digest, Provisioning: software.ProvisionDispositionInstalled,
		Executable: request.Executable, ExitCode: 2,
		StartedAt: "2026-08-16T00:00:00Z", FinishedAt: "2026-08-16T00:00:01Z",
		Stdout: localconda.StreamReceipt{SHA256: strings.Repeat("1", 64)},
		Stderr: localconda.StreamReceipt{Text: "ValueError: invalid input", Bytes: 25, SHA256: strings.Repeat("2", 64)},
		ScientificEvidence: &localconda.ScientificEvidenceReceipt{
			Engine: "generic-engine", EnginePackage: "python", EngineVersion: "3.11",
			ScoreKind: "energy-kcal-mol",
		},
		Cleanup: localconda.CleanupReceipt{
			ProcessGroupTerminated: true, ProcessTreeTerminated: true, TemporaryStreamsClosed: true,
		},
		Recovery: "inspect_stderr_then_repair_inputs_or_packages_without_switching_provider",
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := softwareRuntimeVisibleResult(raw, environment, provision.Generation, launcher, map[string]any{
		"stdout": localconda.ResultPrefix + string(encoded), "stderr": "",
		"exec_id": "exec-failed", "kernel_id": "kernel-failed", "cell_index": 1,
	})
	if err != nil || visible["ok"] != false || visible["status"] != "failed" ||
		visible["code"] != "nonzero_exit" || visible["stderr"] != "ValueError: invalid input" ||
		visible["failure_kind"] != string(failurecontract.ResultRejected) || visible["terminal"] != true ||
		visible["retryable"] != false || visible["scientific_witness"] != nil {
		t.Fatalf("visible failed scientific result=%#v err=%v", visible, err)
	}
}

func TestSoftwareRuntimeFailureMessageClassifiesUnsupportedPlanAndEnvironmentWitness(t *testing.T) {
	unsupported := softwareRuntimeFailureMessage(software.ErrNoProvider)
	var unsupportedOperation *software.OperationError
	if !errors.As(unsupported, &unsupportedOperation) || unsupportedOperation.Code != "software_request_unsupported" ||
		unsupportedOperation.Retryable || unsupportedOperation.Kind != failurecontract.InvalidRequest ||
		unsupportedOperation.RepairScope != "same_task_registered_alternative" {
		t.Fatalf("unsupported plan contract=%#v", unsupported)
	}
	witness := softwareRuntimeFailureMessage(errors.New("software installation failed: managed environment import witness returned an invalid result"))
	var witnessOperation *software.OperationError
	if !errors.As(witness, &witnessOperation) || witnessOperation.Code != "software_environment_witness_failed" ||
		witnessOperation.Retryable || witnessOperation.Kind != failurecontract.ResultRejected || witnessOperation.RepairScope != "same_provider_plan" {
		t.Fatalf("environment witness contract=%#v", witness)
	}
}

func TestSoftwareRuntimeScientificWitnessBindsObservedInputsAndOutputs(t *testing.T) {
	request, err := software.NormalizeRequest(software.Request{
		Capability: "molecular-docking", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerConda, Spec: "vina=1.2.7"}},
		ExpectedOutputs: []software.OutputWitness{
			{Path: "ranked_poses.pdbqt", MinBytes: 64}, {Path: "vina.log", MinBytes: 64},
		},
		ScientificEvidence: &software.ScientificEvidenceRequest{
			Engine: "autodock-vina", EnginePackage: "vina", ScoreKind: "affinity-kcal-mol",
			Inputs: []software.ScientificFileWitness{
				{Kind: "receptor", Path: "receptor.pdbqt"}, {Kind: "ligand", Path: "ligands.sdf"},
			},
			Artifacts: []software.ScientificFileWitness{
				{Kind: "ranked-pose", Path: "ranked_poses.pdbqt"}, {Kind: "execution-log", Path: "vina.log"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	profileSHA256, err := software.ScientificEvidenceDigest(*request.ScientificEvidence)
	if err != nil {
		t.Fatal(err)
	}
	digests := []string{
		strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64),
	}
	receipt := localconda.ExecutionReceipt{
		OK: true, ProviderID: software.LocalProviderID, Generation: strings.Repeat("a", 64),
		FinishedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Outputs: []localconda.OutputReceipt{
			{Path: "ranked_poses.pdbqt", Bytes: 128, SHA256: digests[2]},
			{Path: "vina.log", Bytes: 128, SHA256: digests[3]},
		},
		ScientificEvidence: &localconda.ScientificEvidenceReceipt{
			Engine: "autodock-vina", EnginePackage: "vina", EngineVersion: "1.2.7",
			ProfileSHA256: profileSHA256, CodeSHA256: strings.Repeat("b", 64), ScoreKind: "affinity-kcal-mol",
			Inputs: []localconda.ScientificFileReceipt{
				{Kind: "receptor", Path: "receptor.pdbqt", SHA256: digests[0]},
				{Kind: "ligand", Path: "ligands.sdf", SHA256: digests[1]},
			},
			Artifacts: []localconda.ScientificFileReceipt{
				{Kind: "ranked-pose", Path: "ranked_poses.pdbqt", SHA256: digests[2]},
				{Kind: "execution-log", Path: "vina.log", SHA256: digests[3]},
			},
		},
	}
	witness, found, err := softwareRuntimeScientificWitness(request, receipt, map[string]any{"exec_id": "exec-docking"})
	if err != nil || !found || witness.EnginePackage != "vina" || witness.EngineVersion != "1.2.7" ||
		witness.Inputs["receptor"] != digests[0] || len(witness.Artifacts) != 2 {
		t.Fatalf("scientific witness=%#v found=%t err=%v", witness, found, err)
	}
	tampered := receipt
	tampered.ScientificEvidence = &localconda.ScientificEvidenceReceipt{}
	*tampered.ScientificEvidence = *receipt.ScientificEvidence
	tampered.ScientificEvidence.Artifacts = append([]localconda.ScientificFileReceipt(nil), receipt.ScientificEvidence.Artifacts...)
	tampered.ScientificEvidence.Artifacts[0].SHA256 = strings.Repeat("f", 64)
	if _, _, err := softwareRuntimeScientificWitness(request, tampered, map[string]any{"exec_id": "exec-docking"}); err == nil {
		t.Fatal("tampered scientific artifact digest was accepted")
	}
}
