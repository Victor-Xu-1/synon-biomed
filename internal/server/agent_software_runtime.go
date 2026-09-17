package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"synon-go/internal/failurecontract"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/software"
	"synon-go/internal/software/localconda"
)

const softwareRuntimeToolName = "software_runtime"

func softwareRuntimeKernelID(operationID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(operationID)))
	return fmt.Sprintf("kernel-swr-%x", digest[:12])
}

func decodeSoftwareRuntimeRequest(input map[string]any) (software.Request, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return software.Request{}, errors.New("software runtime input is invalid")
	}
	executionRequest, _, _, executionErr := sciencecapability.DecodeExecutionRequestJSON(encoded)
	if executionErr == nil {
		request, _, _, _, buildErr := sciencecapability.BuildSoftwareRequest(executionRequest)
		return request, buildErr
	}
	if _, packRequest := input["execution_pack_id"]; packRequest {
		return software.Request{}, executionErr
	}
	request, err := software.DecodeRequestJSON(encoded)
	if err != nil {
		return software.Request{}, err
	}
	return request, nil
}

func canonicalSoftwareRuntimeInput(input map[string]any) ([]byte, software.Request, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, software.Request{}, errors.New("software runtime input is invalid")
	}
	executionRequest, _, _, executionErr := sciencecapability.DecodeExecutionRequestJSON(encoded)
	if executionErr == nil {
		canonical, canonicalErr := sciencecapability.CanonicalExecutionRequestJSON(executionRequest)
		request, _, _, _, buildErr := sciencecapability.BuildSoftwareRequest(executionRequest)
		if canonicalErr != nil {
			return nil, software.Request{}, canonicalErr
		}
		return canonical, request, buildErr
	}
	if _, packRequest := input["execution_pack_id"]; packRequest {
		return nil, software.Request{}, executionErr
	}
	request, err := software.DecodeRequestJSON(encoded)
	if err != nil {
		return nil, software.Request{}, err
	}
	encoded, err = software.CanonicalRequestJSON(request)
	return encoded, request, err
}

func (s *Server) resolveSoftwareRuntime(ctx context.Context, input map[string]any) (*software.Controller, software.Plan, error) {
	if s == nil || s.kernelManager == nil || !s.kernelManager.ManagedEnvironmentSupervisorReady() {
		return nil, software.Plan{}, errors.New("local software runtime is unavailable")
	}
	request, err := decodeSoftwareRuntimeRequest(input)
	if err != nil {
		return nil, software.Plan{}, err
	}
	provider, err := localconda.New(s.kernelManager)
	if err != nil {
		return nil, software.Plan{}, err
	}
	controller, err := software.NewController(provider)
	if err != nil {
		return nil, software.Plan{}, err
	}
	plan, err := controller.Resolve(request)
	if err != nil {
		return nil, software.Plan{}, err
	}
	plan, err = provider.SelectCompatibleEnvironment(ctx, plan)
	if err != nil {
		return nil, software.Plan{}, err
	}
	return controller, plan, nil
}

// durableSoftwareRuntimePlan keeps the checkpointed environment identity
// stable for a model-authored tool call. Inventory may discover an arbitrary
// compatible superset, but that runtime choice is not present in the immutable
// model call committed before approval. Task execution therefore retains the
// request's content-addressed environment; the local provider can still clone
// the verified bundled Python generation and skip already-satisfied pip work.
// Non-agent callers may continue to use compatible-superset selection directly.
func durableSoftwareRuntimePlan(plan software.Plan) (software.Plan, error) {
	requestedEnvironment, err := software.EnvironmentName(plan.ProviderID, plan.Request)
	if err != nil {
		return software.Plan{}, err
	}
	plan.Environment = requestedEnvironment
	plan.CompatibleReuse = false
	return plan, nil
}

// softwareRuntimeProviderForComputeSelection is the explicit bridge between
// the dialogue-level compute selector and the unified software provider SPI.
// The first production adapter is local-conda. A selected remote provider
// fails closed until it has its own adapter; it must never fall back to the
// local machine merely because local-conda is available.
func softwareRuntimeProviderForComputeSelection(selected string) (string, error) {
	selected = strings.TrimSpace(selected)
	if selected == localComputeProviderID {
		return software.LocalProviderID, nil
	}
	if selected == "" {
		return "", errors.New("compute provider selection is unavailable")
	}
	return "", fmt.Errorf("selected compute provider %q has no unified software runtime adapter", selected)
}

func (s *Server) provisionSoftwareRuntime(
	ctx context.Context,
	controller *software.Controller,
	plan software.Plan,
	operationID string,
) (software.ProvisionReceipt, string, error) {
	if controller == nil {
		return software.ProvisionReceipt{}, "", errors.New("software controller is unavailable")
	}
	provisioningContext := ctx
	cancelProvisioning := func() {}
	if plan.Request.TimeoutSeconds > 0 {
		provisioningTimeout := time.Duration(plan.Request.TimeoutSeconds) * time.Second
		provisioningContext, cancelProvisioning = context.WithTimeout(ctx, provisioningTimeout)
	}
	defer cancelProvisioning()
	provision, err := controller.Ensure(provisioningContext, plan, operationID)
	if err != nil {
		if plan.Request.TimeoutSeconds > 0 && errors.Is(provisioningContext.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return software.ProvisionReceipt{}, "", software.NewOperationError(
				"software_install_timeout",
				"software provisioning exceeded timeout_seconds; the installer process tree was cancelled",
				"retry_this_same_provider_plan_with_a_larger_timeout_seconds",
				true,
			)
		}
		return software.ProvisionReceipt{}, "", err
	}
	harness, err := localconda.BuildPythonHarness(plan, provision)
	if err != nil {
		return software.ProvisionReceipt{}, "", err
	}
	return provision, harness, nil
}

func softwareRuntimeVisibleResult(
	rawInput []byte,
	environment, runtimeGeneration, durableSource string,
	result map[string]any,
) (map[string]any, error) {
	request, err := softwareRuntimeRequestFromDurableInput(rawInput)
	if err != nil {
		return nil, errors.New("software runtime durable input is invalid")
	}
	providerID := software.LocalProviderID
	if request.Provider != "" {
		providerID = request.Provider
	}
	wantEnvironment, err := software.EnvironmentName(providerID, request)
	compatibleReuse := wantEnvironment != environment
	if err != nil || (compatibleReuse && !strings.HasPrefix(environment, "swr-")) {
		return nil, errors.New("software runtime durable environment conflicts with its request")
	}
	wantDigest, err := software.RequestDigest(request)
	if err != nil {
		return nil, err
	}
	rawStdout := stringValue(result["stdout"])
	receipt, found, err := localconda.ParseExecutionReceipt(rawStdout)
	if err != nil {
		return nil, err
	}
	if !found {
		value := map[string]any{
			"ok": false, "status": "failed", "code": "software_runtime_host_failed",
			"message":     "The governed command host failed before it could emit an execution receipt.",
			"provider_id": providerID, "environment": environment, "runtime_generation": runtimeGeneration,
			"stdout": rawStdout, "stderr": stringValue(result["stderr"]),
			"exit_status": result["exit_status"], "exec_id": result["exec_id"], "kernel_id": result["kernel_id"],
			"recovery": "repair_the_local_command_host_then_resume_the_same_durable_tool_call",
		}
		failurecontract.ApplyTerminalJobFailure(value, "software_runtime_host_failed")
		return value, nil
	}
	if compatibleReuse && receipt.Provisioning != software.ProvisionDispositionReused {
		return nil, errors.New("software runtime compatible environment was not reused")
	}
	if runtimeGeneration == "" {
		runtimeGeneration = receipt.Generation
	}
	if receipt.ProviderID != providerID || receipt.Environment != environment || receipt.Generation != runtimeGeneration ||
		receipt.RequestDigest != wantDigest || receipt.Executable != request.Executable {
		return nil, errors.New("software runtime execution receipt conflicts with durable authority")
	}
	if durableSource != "" {
		plan := software.Plan{
			ProviderID: providerID, Request: request, RequestDigest: wantDigest,
			Environment: environment, CompatibleReuse: compatibleReuse,
		}
		provision := software.ProvisionReceipt{
			ProviderID: providerID, Environment: environment, Generation: receipt.Generation,
			Executable: request.Executable, Local: true, Verified: true,
			Preflight: true, Disposition: receipt.Provisioning,
		}
		expectedSource, sourceErr := localconda.BuildPythonHarness(plan, provision)
		if sourceErr != nil || expectedSource != durableSource {
			return nil, errors.New("software runtime launcher source conflicts with durable authority")
		}
	}
	status := "failed"
	exitStatus := "error"
	if receipt.OK {
		status = "completed"
		exitStatus = "ok"
	}
	visible := map[string]any{
		"ok": receipt.OK, "status": status, "code": receipt.Code,
		"exit_status": exitStatus,
		"provider_id": receipt.ProviderID, "environment": receipt.Environment,
		"preflight_checked": true, "provisioning": receipt.Provisioning,
		"runtime_generation": receipt.Generation, "request_digest": receipt.RequestDigest,
		"executable": receipt.Executable, "exit_code": receipt.ExitCode, "timed_out": receipt.TimedOut,
		"started_at": receipt.StartedAt, "finished_at": receipt.FinishedAt,
		"stdout": receipt.Stdout.Text, "stderr": receipt.Stderr.Text,
		"stdout_bytes": receipt.Stdout.Bytes, "stdout_sha256": receipt.Stdout.SHA256, "stdout_truncated": receipt.Stdout.Truncated,
		"stderr_bytes": receipt.Stderr.Bytes, "stderr_sha256": receipt.Stderr.SHA256, "stderr_truncated": receipt.Stderr.Truncated,
		"outputs": receipt.Outputs, "cleanup": receipt.Cleanup,
		"exec_id": result["exec_id"], "kernel_id": result["kernel_id"], "cell_index": result["cell_index"],
	}
	if softwareRuntimeNetworkIsolationFailure(receipt) {
		visible["code"] = "network_denied"
		visible["recovery"] = "stage_successful_source_evidence_as_local_input_and_start_a_new_registered_execution_without_network_calls"
	}
	if receipt.Recovery != "" {
		visible["recovery"] = receipt.Recovery
	}
	if !receipt.OK {
		code := strings.TrimSpace(stringValue(visible["code"]))
		if code == "" {
			code = "execution_failed"
			visible["code"] = code
		}
		failurecontract.ApplyTerminalJobFailure(visible, code)
	}
	if launcherStderr := strings.TrimSpace(stringValue(result["stderr"])); launcherStderr != "" {
		visible["launcher_stderr"] = truncateUTF8ByBytes(launcherStderr, 4096)
	}
	if inputArtifacts := result["input_artifacts"]; inputArtifacts != nil {
		visible["input_artifacts"] = inputArtifacts
	}
	witness, hasWitness, err := softwareRuntimeScientificWitness(request, receipt, result)
	if err != nil {
		return nil, err
	}
	if hasWitness {
		visible["scientific_witness"] = witness
	}
	return visible, nil
}

func softwareRuntimeRequestFromDurableInput(rawInput []byte) (software.Request, error) {
	if executionRequest, _, _, executionErr := sciencecapability.DecodeExecutionRequestJSON(rawInput); executionErr == nil {
		request, _, _, _, err := sciencecapability.BuildSoftwareRequest(executionRequest)
		return request, err
	}
	return software.DecodeRequestJSON(rawInput)
}

func softwareRuntimeVisibleExecutionResult(
	rawInput []byte,
	environment, runtimeGeneration, durableSource string,
	result map[string]any,
) (map[string]any, error) {
	// The exact managed interpreter may safely defer a directly referenced
	// Python script before the provider harness starts. Preserve that successful
	// non-executing result; trying to parse it as a missing harness receipt turns
	// a code_preflight_required correction into a false host failure.
	if agentKernelPreflightResult(result) {
		return result, nil
	}
	return softwareRuntimeVisibleResult(rawInput, environment, runtimeGeneration, durableSource, result)
}

func softwareRuntimeScientificWitness(
	request software.Request,
	receipt localconda.ExecutionReceipt,
	rawResult map[string]any,
) (sciencecapability.Witness, bool, error) {
	// A scientific witness certifies a successful computation. Failed runs may
	// legitimately carry only the preflight portion of their evidence receipt;
	// keep their bounded stderr/recovery result visible so the agent can repair
	// the same plan instead of turning a normal command failure into a stranded
	// durable operation.
	if !receipt.OK {
		return sciencecapability.Witness{}, false, nil
	}
	declaration := request.ScientificEvidence
	observed := receipt.ScientificEvidence
	if declaration == nil && observed == nil {
		return sciencecapability.Witness{}, false, nil
	}
	if declaration == nil || observed == nil {
		return sciencecapability.Witness{}, false, errors.New("software runtime scientific evidence conflicts with its admitted request")
	}
	profileSHA256, err := software.ScientificEvidenceDigest(*declaration)
	if err != nil || observed.Engine != declaration.Engine || observed.EnginePackage != declaration.EnginePackage ||
		observed.ScoreKind != declaration.ScoreKind || observed.ProfileSHA256 != profileSHA256 ||
		!isSHA256Hex(observed.ProfileSHA256) || !isSHA256Hex(observed.CodeSHA256) ||
		strings.TrimSpace(observed.EngineVersion) == "" {
		return sciencecapability.Witness{}, false, errors.New("software runtime scientific evidence identity is invalid")
	}
	if declaration.WeightsPath == "" {
		if observed.WeightsSHA256 != "" {
			return sciencecapability.Witness{}, false, errors.New("software runtime scientific weights evidence is unexpected")
		}
	} else if !isSHA256Hex(observed.WeightsSHA256) {
		return sciencecapability.Witness{}, false, errors.New("software runtime scientific weights evidence is invalid")
	}
	inputs, err := bindSoftwareRuntimeScientificFileReceipts(declaration.Inputs, observed.Inputs, nil)
	if err != nil {
		return sciencecapability.Witness{}, false, err
	}
	outputDigests := make(map[string]string, len(receipt.Outputs))
	for _, output := range receipt.Outputs {
		outputDigests[output.Path] = output.SHA256
	}
	_, err = bindSoftwareRuntimeScientificFileReceipts(declaration.Artifacts, observed.Artifacts, outputDigests)
	if err != nil {
		return sciencecapability.Witness{}, false, err
	}
	artifacts := make([]sciencecapability.ArtifactWitness, 0, len(observed.Artifacts))
	for _, artifact := range observed.Artifacts {
		artifacts = append(artifacts, sciencecapability.ArtifactWitness{Kind: artifact.Kind, SHA256: artifact.SHA256})
	}
	completedAt, err := time.Parse(time.RFC3339Nano, receipt.FinishedAt)
	if err != nil {
		return sciencecapability.Witness{}, false, errors.New("software runtime scientific completion time is invalid")
	}
	jobID := strings.TrimSpace(stringValue(rawResult["exec_id"]))
	if jobID == "" {
		return sciencecapability.Witness{}, false, errors.New("software runtime scientific job identity is unavailable")
	}
	return sciencecapability.Witness{
		Version: sciencecapability.WitnessVersion, Capability: request.Capability,
		Engine: observed.Engine, EnginePackage: observed.EnginePackage, EngineVersion: observed.EngineVersion,
		ProfileSHA256: observed.ProfileSHA256, CodeSHA256: observed.CodeSHA256,
		WeightsSHA256: observed.WeightsSHA256, EnvironmentSHA256: receipt.Generation,
		Provider: receipt.ProviderID, JobID: jobID, State: "completed", ScoreKind: observed.ScoreKind,
		Inputs: inputs, Artifacts: artifacts, CompletedAt: completedAt.UTC(),
	}, true, nil
}

func bindSoftwareRuntimeScientificFileReceipts(
	declared []software.ScientificFileWitness,
	observed []localconda.ScientificFileReceipt,
	expectedDigests map[string]string,
) (map[string]string, error) {
	if len(declared) == 0 || len(observed) != len(declared) {
		return nil, errors.New("software runtime scientific file evidence is incomplete")
	}
	byKind := make(map[string]localconda.ScientificFileReceipt, len(observed))
	for _, receipt := range observed {
		if receipt.Kind == "" || receipt.Path == "" || !isSHA256Hex(receipt.SHA256) || byKind[receipt.Kind].Kind != "" {
			return nil, errors.New("software runtime scientific file evidence is invalid")
		}
		byKind[receipt.Kind] = receipt
	}
	digests := make(map[string]string, len(declared))
	for _, declaration := range declared {
		receipt, found := byKind[declaration.Kind]
		if !found || receipt.Path != declaration.Path ||
			(expectedDigests != nil && expectedDigests[receipt.Path] != receipt.SHA256) {
			return nil, errors.New("software runtime scientific file evidence conflicts with its declaration")
		}
		digests[declaration.Kind] = receipt.SHA256
	}
	return digests, nil
}

func softwareRuntimeFailureMessage(err error) error {
	if err == nil {
		return nil
	}
	var operationErr *software.OperationError
	if errors.As(err, &operationErr) && operationErr != nil {
		return err
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	if errors.Is(err, software.ErrNoProvider) || strings.Contains(lower, "no software provider satisfies the request") {
		return &software.OperationError{
			Kind:        failurecontract.InvalidRequest,
			Code:        "software_request_unsupported",
			Message:     "no registered software provider satisfies the declared request",
			Recovery:    "correct_the_declared_language_package_manager_or_executable_or_choose_a_registered_capability_with_a_documented_provider_plan; do_not_retry_the_same_unsupported_request",
			Retryable:   false,
			RepairScope: "same_task_registered_alternative",
			Cause:       err,
		}
	}
	if strings.Contains(lower, "managed environment import witness returned an invalid result") {
		return &software.OperationError{
			Kind:        failurecontract.ResultRejected,
			Code:        "software_environment_witness_failed",
			Message:     "the managed environment import witness did not emit a valid execution sentinel",
			Recovery:    "repair_or_rebuild_the_same_managed_environment_and_rerun_its_import_kernel_and_documented_invocation_witness; do_not_repeat_an_unchanged_broken_generation",
			Retryable:   false,
			RepairScope: "same_provider_plan",
			Cause:       err,
		}
	}
	return fmt.Errorf("unified software runtime failed: %w", err)
}

func softwareRuntimeNetworkIsolationFailure(receipt localconda.ExecutionReceipt) bool {
	if receipt.OK {
		return false
	}
	text := strings.ToLower(receipt.Stderr.Text + "\n" + receipt.Stdout.Text)
	for _, marker := range []string{
		"temporary failure in name resolution",
		"name or service not known",
		"nodename nor servname provided",
		"network is unreachable",
		"network is disabled",
		"urlopen error",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
