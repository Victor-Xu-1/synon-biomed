package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/sciencecapability"
)

func TestLegacyExecutionPlanBindsOnlyExplicitVerifiedPackReceipt(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	input := revisionPlanInput("Run computation")
	stepInput := mapValue(anySliceValue(mapValue(anySliceValue(mapValue(anySliceValue(input["phases"])[0])["delegations"])[0])["steps"])[0])
	stepInput["kind"], stepInput["execution_tool"] = generatedPlanStepKindExecution, "python"
	plan := revisePlanForTest(t, f, "legacy-plan", input)
	stepID := stringValue(mapValue(anySliceValue(plan["steps"])[0])["id"])
	metadata, found, err := f.store.GetFrameRuntimeMetadata(f.stream.SessionID)
	if err != nil || !found {
		t.Fatal(err)
	}
	// An older runtime admitted a capability label instead of an executor.
	legacyStep := mapValue(anySliceValue(mapValue(anySliceValue(mapValue(anySliceValue(mapValue(metadata.ContextData["_plan_json"])["phases"])[0])["delegations"])[0])["steps"])[0])
	legacyStep["execution_tool"] = "unregistered_capability_label"
	if _, err := f.store.SetFrameRuntimeMetadata(f.stream.SessionID, metadata); err != nil {
		t.Fatal(err)
	}
	f.server.scienceCapabilities = &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "capability.engine", Mode: "local", Skill: "managed-workflow", Executable: "python", Script: "executionpacks/run.py",
				Outputs: []sciencecapability.ExecutionOutput{{Path: "out/report.md", Delivery: "snapshot"}},
			},
		}},
	}}}
	output := filepath.Join(f.projectPath, "results")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	writes := []kernelruntime.FileWrite{}
	for name, content := range map[string]string{
		managedExecutionOutputOwnershipMarker: `{"schema":"synon.execution-pack-output-owner.v1","execution_pack_id":"capability.engine"}`,
		"report.md":                           "validated result\n",
	} {
		if err := os.WriteFile(filepath.Join(output, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(content))
		writes = append(writes, kernelruntime.FileWrite{Path: filepath.ToSlash(filepath.Join("results", name)), SHA256: hex.EncodeToString(digest[:])})
	}
	access := f.identity.access
	kernelID, err := kernelruntime.StableSessionID(kernelruntime.SessionSpec{
		OwnerID: access.UserID, ProjectID: access.Frame.ProjectID, FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: access.Frame.AgentName, KernelKind: "bash", Language: "python", Environment: "python", WorkspaceDir: f.projectPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(f.projectPath, ".synon", "runtime", "skills", "managed-workflow-abcd", "scripts", "run.py")
	command := `python "` + script + `"`
	if _, err := f.store.SaveExecutionLog(workspace.SaveExecutionLogInput{
		Record: workspace.ExecutionLogRecord{ID: "pack-execution", FrameID: f.stream.SessionID, KernelID: kernelID,
			KernelKind: "bash", CondaEnv: "python", Language: "python", Source: command, ExitStatus: "ok", Origin: "agent", FilesWritten: writes},
		ExpectedOwnerID: access.UserID, ExpectedProjectID: access.Frame.ProjectID,
		ExpectedFrameIncarnationID: access.Frame.IncarnationID, ExpectedRootFrameIncarnationID: access.RootFrameIncarnationID,
	}); err != nil {
		t.Fatal(err)
	}
	appendAdaptiveToolReceipt(t, f, "unrelated-shell", "bash", map[string]any{"ok": true, "executed": true, "exit_code": 0})
	appendAdaptiveToolReceiptInput(t, f, "verified-pack", "bash", map[string]any{"command": command},
		map[string]any{"ok": true, "executed": true, "exit_code": 0, "exec_id": "pack-execution"})
	ctx, _ := appendLargeToolResultSource(t, f, "bind-legacy-result", updateStepStatusToolName)
	for _, ref := range []string{"", "unrelated-shell"} {
		result, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "inspect-binding", map[string]any{
			"step": stepID, "status": "completed", "execution_ref": ref,
		})
		if err != nil || mapValue(result)["status"] == "completed" {
			t.Fatalf("unselected or unrelated execution completed legacy plan: %#v %v", result, err)
		}
		if ref == "" {
			choices := mapValue(mapValue(result)["execution_continuation"])["eligible_execution_receipts"].([]generatedPlanExecutionBinding)
			if len(choices) != 1 || choices[0].CallID != "verified-pack" {
				t.Fatalf("verified candidate missing from repair contract: %#v", choices)
			}
		}
	}
	result, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "bind-legacy-result", map[string]any{
		"step": stepID, "status": "completed", "execution_ref": "verified-pack",
	})
	if err != nil || mapValue(result)["status"] != "completed" ||
		mapValue(mapValue(result)["execution_binding"])["execution_pack_id"] != "capability.engine" {
		t.Fatalf("verified pack did not close legacy obligation: %#v %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(output, "report.md"), []byte("changed result"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "reverify-legacy-result", map[string]any{
		"step": stepID, "status": "completed", "execution_ref": "verified-pack",
	}); err == nil {
		t.Fatal("changed output retained pack execution authority")
	}
}

func TestExecutionReceiptAmbiguityAndPlanFence(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	ctx, run := appendLargeToolResultSource(t, f, "resolve-executions", updateStepStatusToolName)
	step := generatedPlanStepIdentity{ID: "compute", Kind: generatedPlanStepKindExecution, ExecutionTool: "python"}
	// Use the real plan metadata so admission and receipt resolution share scope.
	input := revisionPlanInput("Run computation")
	_ = revisePlanForTest(t, f, "fenced-plan", input)
	metadata, _, err := f.store.GetFrameRuntimeMetadata(f.stream.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	plan := metadata.ContextData
	for _, id := range []string{"first", "second"} {
		appendAdaptiveToolReceipt(t, f, id, "python", map[string]any{"ok": true, "executed": true, "exit_code": 0})
	}
	bound, choices, err := f.server.resolveGeneratedPlanExecutionReceipt(ctx, run, step, "", plan)
	if err != nil || bound.CallID != "" || len(choices) != 2 {
		t.Fatalf("ambiguous executions auto-bound: %#v %#v %v", bound, choices, err)
	}
	bound, _, err = f.server.resolveGeneratedPlanExecutionReceipt(ctx, run, step, "second", plan)
	if err != nil || bound.CallID != "second" {
		t.Fatalf("explicit scoped execution rejected: %#v %v", bound, err)
	}
	plan["_plan_source_event_id"] = bound.EventID + 1
	bound, choices, err = f.server.resolveGeneratedPlanExecutionReceipt(ctx, run, step, "second", plan)
	if err != nil || bound.CallID != "" || len(choices) != 0 {
		t.Fatalf("prior plan execution crossed revision fence: %#v %#v %v", bound, choices, err)
	}
	plan["_step_statuses"] = map[string]any{step.ID: map[string]any{"status": "completed", "execution_ref": "second"}}
	bound, _, err = f.server.resolveGeneratedPlanExecutionReceipt(context.Background(), run, step, "", plan)
	if err != nil || bound.CallID != "second" {
		t.Fatalf("carried completed step lost its existing receipt: %#v %v", bound, err)
	}
}
