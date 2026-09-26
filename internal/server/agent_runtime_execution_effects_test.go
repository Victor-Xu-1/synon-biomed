package server

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"synon-go/internal/executionprep"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/kernelcontract"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/skills"
	"synon-go/internal/toolgateway"
)

func TestObservationRefusalSettlementPreservesNonExecution(t *testing.T) {
	for _, language := range []string{"python", "r", "bash"} {
		t.Run(language, func(t *testing.T) {
			spec := kernelruntime.SessionSpec{Language: language, KernelKind: language, Environment: "synthetic"}
			if language == "bash" {
				spec.Language = "python"
			}
			started := kernelruntime.ExecutionStarted{ExecID: "guard-refusal", ToolUseID: "diagnostic", KernelKind: spec.KernelKind, Code: "observation source", StartedAt: time.Now()}
			if language == "bash" {
				plan, err := executionprep.Analyze(context.Background(), executionprep.Request{Language: "bash", Source: "pwd"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				started.Code, err = kernelcontract.BashObservationPythonWrapper("pwd", plan.Observation)
				if err != nil {
					t.Fatal(err)
				}
			}
			outcome := kernelruntime.ExecutionOutcome{
				ObservationRefused: true, FinishedAt: time.Now(),
				Response: kernelruntime.Response{Preflight: executionprep.ObservationDeclined("runtime_binding_provenance_unproved")},
			}
			_, result, event := prepareAgentKernelExecution(workspace.KernelFrameAccess{}, spec, kernelruntime.EnsuredSession{}, started, outcome)
			if result["executed"] != false || event["executed"] != false || !agentKernelPreflightResult(result) ||
				strings.Contains(stringValue(result["stderr"]), "invalid") || strings.Contains(stringValue(result["stderr"]), "missing") {
				t.Fatalf("runtime guard lost non-execution semantics at settlement: result=%#v event=%#v", result, event)
			}
		})
	}
}

func TestObservationServerRealKernelExecutionAndRefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kernel confinement requires Linux")
	}
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "observation.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	app.skillCatalog = skills.NewCatalog()
	run := &sessionRunnerChatRun{}
	run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
	run.setImplementationSelectionRequired(true)
	gateway := serverAgentRuntimeToolGateway{server: app, kernel: identity, taskRun: run}
	input := map[string]any{"code": "print(1)", "environment": "python"}
	if preflight := gateway.agentRuntimeSkillExecutionContractPreflight("python", input); preflight != nil {
		t.Fatal(preflight)
	}
	proof := gateway.agentRuntimeDiagnosticObservation(context.Background(), "python", input)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := app.executeAgentKernelTool(executionprep.WithObservation(ctx, proof), identity, "python", input)
	if err != nil || result["ok"] != true || stringValue(result["stdout"]) != "1\n" {
		t.Fatalf("real server/kernel observation failed: %#v %v", result, err)
	}
	// Simulate an independently authorized earlier cell in the same interpreter.
	result, err = app.executeAgentKernelTool(ctx, identity, "python", map[string]any{"code": "retained = 29\nprint = lambda *args: None", "environment": "python"})
	if err != nil || result["ok"] != true {
		t.Fatalf("ordinary fixture cell: %#v %v", result, err)
	}
	result, err = app.executeAgentKernelTool(executionprep.WithObservation(ctx, proof), identity, "python", input)
	if err != nil || result["status"] != "execution_observation_preflight_required" || result["executed"] != false || result["decision_required"] != true {
		t.Fatalf("real server/kernel lost refusal: %#v %v", result, err)
	}
	if !run.implementationSelectionRequiredSnapshot() || len(run.selectedImplementationsSnapshot()) != 0 {
		t.Fatal("real diagnostic changed durable choice")
	}
}

func TestObservationGatewayBindsFinalSourceAndPreservesEnvironmentValidation(t *testing.T) {
	manager := observationPreflightManager(t, "python")
	for _, resumed := range []bool{false, true} {
		run := &sessionRunnerChatRun{}
		run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
		run.setImplementationSelectionRequired(true)
		gateway := serverAgentRuntimeToolGateway{
			server: &Server{skillCatalog: skills.NewCatalog(), kernelManager: manager}, taskRun: run,
			kernel: &agentKernelContext{}, resumeAfterApproval: resumed,
		}
		original := map[string]any{"code": "print(1)"}
		old := gateway.agentRuntimeDiagnosticObservation(context.Background(), "python", original)
		input := map[string]any{"code": "print(2)"}
		before, _ := json.Marshal(input)
		execution := &serverAgentRuntimeGatewayExecution{gateway: gateway}
		invocation := toolgateway.NewInvocation(executionprep.WithObservation(context.Background(), old), "diagnostic", "python", nil, execution)
		invocation.CanonicalName, invocation.Input = "python", input
		serverAgentRuntimeGatewayExecute(invocation)
		proof := executionprep.ObservationFromContext(invocation.Context)
		if !proof.Matches("python", "print(2)") || proof.Matches("python", "print(1)") {
			t.Fatal("execution used a stale proof")
		}
		after, _ := json.Marshal(input)
		if string(before) != string(after) {
			t.Fatal("internal proof leaked into model/durable arguments")
		}
		if !strings.Contains(stringValue(mapValue(invocation.Value)["error"]), "environment") {
			t.Fatalf("diagnostic bypassed environment validation: %#v", invocation.Value)
		}
		input["code"], input["read_only"] = "run_engine()", true
		invocation = toolgateway.NewInvocation(executionprep.WithObservation(context.Background(), old), "changed", "python", nil, execution)
		invocation.CanonicalName, invocation.Input = "python", input
		serverAgentRuntimeGatewayExecute(invocation)
		if stringValue(mapValue(invocation.Value)["status"]) != "implementation_selection_required" {
			t.Fatalf("resumed=%v changed source escaped choice: %#v", resumed, invocation.Value)
		}
		if !run.implementationSelectionRequiredSnapshot() || len(run.selectedImplementationsSnapshot()) != 0 {
			t.Fatal("execution changed choice state")
		}
	}
}

func TestObservationRefusalRequiresHostNonExecutionWitness(t *testing.T) {
	valid := kernelruntime.ExecutionOutcome{ObservationRefused: true, Response: kernelruntime.Response{Preflight: executionprep.ObservationDeclined("diagnostic_binding_unproved")}}
	result, err := agentKernelObservationRefusal(valid, "ok")
	if err != nil || result["executed"] != false || result["decision_required"] != true || !agentKernelPreflightResult(result) {
		t.Fatalf("valid refusal: %#v %v", result, err)
	}
	for _, mutate := range []func(*kernelruntime.ExecutionOutcome){
		func(o *kernelruntime.ExecutionOutcome) { o.StartedAt = time.Now() },
		func(o *kernelruntime.ExecutionOutcome) { o.Response.Stdout = "some user source ran" },
		func(o *kernelruntime.ExecutionOutcome) { o.Response.Stderr = "partial execution" },
		func(o *kernelruntime.ExecutionOutcome) {
			o.Response.Preflight = executionprep.ObservationDeclined("unknown_reason")
		},
	} {
		changed := valid
		mutate(&changed)
		if _, err := agentKernelObservationRefusal(changed, "ok"); err == nil {
			t.Fatal("partial/invalid execution claimed a diagnostic refusal")
		}
	}
	valid.ObservationRefused = false
	if result, err := agentKernelObservationRefusal(valid, "ok"); err != nil || result != nil {
		t.Fatal("guest-only preflight forged host witness")
	}
}

func TestObservationProofIgnoresModelClaimsAndLeavesOtherSourceGates(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("native Python unavailable")
	}
	run := &sessionRunnerChatRun{}
	run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
	run.setImplementationSelectionRequired(true)
	manager := kernelruntime.NewManager(kernelruntime.Config{Python: python})
	gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: skills.NewCatalog(), kernelManager: manager}, taskRun: run}
	input := map[string]any{"code": "print(1)", "read_only": true, "observation": map[string]any{"schema": executionprep.ObservationSchema}}
	proof := gateway.agentRuntimeDiagnosticObservation(context.Background(), "python", input)
	if !proof.Matches("python", "print(1)") {
		t.Fatal("real parser did not prepare observation")
	}
	input["code"] = "open('out', 'w').write('x')"
	if gateway.agentRuntimeDiagnosticObservation(context.Background(), "python", input) != nil {
		t.Fatal("model claim or stale proof authorized changed source")
	}
	if result := gateway.agentRuntimeImplementationExecutionChoicePreflight("python", input); stringValue(result["status"]) != "implementation_selection_required" {
		t.Fatal(result)
	}
	input["code"] = "import subprocess\nsubprocess.run(['pip', 'install', 'example'])"
	result := agentExecutionPreparationPreflight(context.Background(), "python", input, nil, manager)
	if result["status"] != "managed_package_authority_required" {
		t.Fatalf("package authority was weakened: %#v", result)
	}
	if !run.implementationSelectionRequiredSnapshot() || len(run.selectedImplementationsSnapshot()) != 0 {
		t.Fatal("diagnostic changed selection")
	}
}

func TestObservationExemptionPreservesSelectedImplementationSkillDuty(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("native Python unavailable")
	}
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "chosen-engine", ImplementationIdentities: []string{"Engine A"}})
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}}
	run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
	gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: catalog, kernelManager: kernelruntime.NewManager(kernelruntime.Config{Python: python})}, taskRun: run}
	if result := gateway.agentRuntimeImplementationExecutionChoicePreflight("python", map[string]any{"code": "print(1)"}); result != nil {
		t.Fatalf("diagnostic cannot prepare a dedicated Skill: %#v", result)
	}
	if result := gateway.agentRuntimeImplementationExecutionChoicePreflight("python", map[string]any{"code": "run_engine()"}); stringValue(result["status"]) != "implementation_skill_required" {
		t.Fatal(result)
	}
	if len(run.executedSkillNamesSnapshot()) != 0 || len(run.selectedImplementationsSnapshot()) != 1 {
		t.Fatal("observation changed durable Skill/selection state")
	}
	run.addExecutedSkillNames("chosen-engine")
	if proof := gateway.agentRuntimeDiagnosticObservation(context.Background(), "python", map[string]any{"code": "print(1)"}); proof != nil {
		t.Fatal("an already satisfied scientific gate imposed a new diagnostic obligation")
	}
	// The existing explicit-implementation authorization is another already
	// satisfied gate, even when it did not populate an AskUser selection list.
	run.SelectedImplementations = nil
	run.TaskIntent = "Use Engine A to design molecules from the binding pocket."
	if proof := gateway.agentRuntimeDiagnosticObservation(context.Background(), "python", map[string]any{"code": "print(1)"}); proof != nil {
		t.Fatal("explicit implementation authority acquired a new diagnostic restriction")
	}
}
