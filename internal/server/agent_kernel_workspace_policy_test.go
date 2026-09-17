package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func TestPrepareAgentKernelExecutionDoesNotTurnKernelReuseIntoToolReuse(t *testing.T) {
	now := time.Now().UTC()
	started := kernelruntime.ExecutionStarted{
		ExecID: "exec-new-cell", ToolUseID: "tool-new-cell", KernelID: "kernel-reused-session",
		FrameID: "frame-new-cell", Language: "python", Environment: "repl",
		KernelKind: "operon", Code: "print('new evidence')", StartedAt: now,
	}
	outcome := kernelruntime.ExecutionOutcome{
		Response:  kernelruntime.Response{ID: started.ExecID, Stdout: "new evidence\n"},
		StartedAt: now, FinishedAt: now.Add(time.Second),
	}
	_, result, _ := prepareAgentKernelExecution(
		workspace.KernelFrameAccess{},
		kernelruntime.SessionSpec{KernelKind: "operon", Language: "python", Environment: "repl"},
		kernelruntime.EnsuredSession{ID: started.KernelID, Reused: true},
		started, outcome,
	)
	if result["kernel_reused"] != true {
		t.Fatalf("kernel reuse was not preserved as execution metadata: %#v", result)
	}
	if _, leaked := result["reused"]; leaked || agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		t.Fatalf("fresh cell was projected as an idempotent tool replay: %#v", result)
	}
}

func TestPrepareAgentKernelExecutionSurfacesWorkspaceJSONAsRecoverableFailure(t *testing.T) {
	now := time.Now().UTC()
	spec := kernelruntime.SessionSpec{KernelKind: "analysis", Language: "python", Environment: "synon-biomed-python"}
	started := kernelruntime.ExecutionStarted{
		ExecID: "exec-json-policy", ToolUseID: "tool-json-policy", KernelID: "kernel-json-policy",
		FrameID: "frame-json-policy", Language: "python", Environment: "synon-biomed-python",
		KernelKind: "analysis", Code: "open('results.json', 'w').write('{}')", StartedAt: now,
	}
	outcome := kernelruntime.ExecutionOutcome{
		Response: kernelruntime.Response{ID: started.ExecID, Stdout: "calculation completed"},
		FilesWritten: []kernelruntime.FileWrite{{
			Path: "results.json", PolicyCode: "json_workspace_file_forbidden", PolicyAction: "removed",
			PolicyMessage: "new .json/.jsonl files are not permitted",
		}},
		StartedAt: now, FinishedAt: now.Add(time.Second),
	}
	logInput, result, eventPayload := prepareAgentKernelExecution(
		workspace.KernelFrameAccess{}, spec, kernelruntime.EnsuredSession{ID: started.KernelID}, started, outcome,
	)
	if result["ok"] != false || result["exit_status"] != "error" {
		t.Fatalf("result=%#v", result)
	}
	if result["partial"] != true || result["status"] != "partial" || result["recoverable"] != true {
		t.Fatalf("recoverable policy envelope=%#v", result)
	}
	stderr, _ := result["stderr"].(string)
	if !strings.Contains(stderr, "Workspace policy rejected new JSON/JSONL scientific output") ||
		!strings.Contains(stderr, "results.json") {
		t.Fatalf("stderr=%q", stderr)
	}
	if _, found := result["recovery"]; found {
		t.Fatalf("workspace policy duplicated recovery guidance outside stderr: %#v", result)
	}
	violations, ok := result["workspace_policy_violations"].([]map[string]any)
	if !ok || len(violations) != 1 || violations[0]["code"] != "json_workspace_file_forbidden" {
		t.Fatalf("violations=%#v", result["workspace_policy_violations"])
	}
	if logInput.Record.ExitStatus != "error" || !strings.Contains(logInput.Record.Stderr, "Workspace policy") {
		t.Fatalf("log=%#v", logInput.Record)
	}
	if _, ok := eventPayload["workspace_policy_violations"]; !ok {
		t.Fatalf("event payload=%#v", eventPayload)
	}
}

func TestPrepareAgentKernelExecutionClassifiesPythonFailureWithoutLeakingTraceback(t *testing.T) {
	now := time.Now().UTC()
	spec := kernelruntime.SessionSpec{KernelKind: "analysis", Language: "python", Environment: "managed-python"}
	started := kernelruntime.ExecutionStarted{
		ExecID: "exec-file-missing", ToolUseID: "tool-file-missing", KernelID: "kernel-file-missing",
		FrameID: "frame-file-missing", Language: "python", Environment: "managed-python",
		KernelKind: "analysis", Code: "redacted", StartedAt: now,
	}
	outcome := kernelruntime.ExecutionOutcome{
		Response: kernelruntime.Response{
			ID: started.ExecID, Stderr: "Traceback\nFileNotFoundError: /private/input.csv", Error: "execution failed",
		},
		StartedAt: now, FinishedAt: now.Add(time.Second),
	}
	_, result, _ := prepareAgentKernelExecution(
		workspace.KernelFrameAccess{}, spec, kernelruntime.EnsuredSession{ID: started.KernelID}, started, outcome,
	)
	if result["ok"] != false || result["code"] != "python_file_not_found" {
		t.Fatalf("classified Python failure=%#v", result)
	}
	message := agentruntime.ToolFailureEventMessage(result)
	if message != "tool result reported failure: python_file_not_found" || strings.Contains(message, "/private") {
		t.Fatalf("safe Python lifecycle summary=%q", message)
	}
}

func TestPrepareAgentKernelExecutionSurfacesTimeoutWithRepairGuidance(t *testing.T) {
	now := time.Now().UTC()
	spec := kernelruntime.SessionSpec{KernelKind: "analysis", Language: "python", Environment: "managed-python"}
	started := kernelruntime.ExecutionStarted{
		ExecID: "exec-timeout", ToolUseID: "tool-timeout", KernelID: "kernel-timeout",
		FrameID: "frame-timeout", Language: "python", Environment: "managed-python",
		KernelKind: "analysis", Code: "while True: pass", StartedAt: now,
	}
	outcome := kernelruntime.ExecutionOutcome{
		Response: kernelruntime.Response{ID: started.ExecID, Interrupted: true}, TimedOut: true,
		StartedAt: now, FinishedAt: now.Add(2 * time.Minute),
	}
	logInput, result, eventPayload := prepareAgentKernelExecution(
		workspace.KernelFrameAccess{}, spec, kernelruntime.EnsuredSession{ID: started.KernelID}, started, outcome,
	)
	if result["ok"] != false || result["exit_status"] != "cancelled" || result["timed_out"] != true ||
		result["code"] != "kernel_execution_timeout" {
		t.Fatalf("timeout result=%#v", result)
	}
	for _, value := range []string{stringValue(result["stderr"]), stringValue(result["recovery"])} {
		if !strings.Contains(value, "bounded") {
			t.Fatalf("timeout guidance=%q", value)
		}
	}
	if logInput.Record.ExitStatus != "cancelled" || eventPayload["cancelled"] != true {
		t.Fatalf("timeout log=%#v event=%#v", logInput.Record, eventPayload)
	}
}

func TestAgentKernelExecutionFailureCodeDoesNotClassifyEmbeddedExceptionText(t *testing.T) {
	if code := agentKernelExecutionFailureCode(
		"python", "error", "application log mentions FileNotFoundError: as documentation only\nRuntimeError: actual failure",
	); code != "python_execution_failed" {
		t.Fatalf("embedded exception text classified as %q", code)
	}
}

func TestAgentKernelExecutionFailureCodeClassifiesCommonRepairablePythonErrors(t *testing.T) {
	tests := []struct {
		stderr string
		want   string
	}{
		{"Traceback\nSyntaxError: invalid syntax", "python_syntax_error"},
		{"Traceback\nIndentationError: unexpected indent", "python_indentation_error"},
		{"Traceback\nAttributeError: object has no attribute", "python_attribute_error"},
		{"Traceback\nTypeError: invalid operand", "python_type_error"},
		{"Traceback\nValueError: invalid value", "python_value_error"},
		{"kernel execution failed: kernel worker exited unexpectedly with exit code 137", "python_memory_exhausted"},
	}
	for _, test := range tests {
		if got := agentKernelExecutionFailureCode("python", "error", test.stderr); got != test.want {
			t.Errorf("stderr=%q got=%q want=%q", test.stderr, got, test.want)
		}
	}
}

func TestAgentKernelEnvironmentFailureCodeClassifiesRuntimeIncompatibilityNotTaskErrors(t *testing.T) {
	for _, stderr := range []string{
		"RuntimeError: installed framework does not contain the observed GPU architecture sm_120",
		"CUDA error: no kernel image is available for execution on the device",
		"RuntimeError: invalid device function",
	} {
		if got := agentKernelEnvironmentFailureCode("error", stderr); got != "managed_environment_accelerator_incompatible" {
			t.Errorf("stderr=%q got=%q", stderr, got)
		}
	}
	if got := agentKernelEnvironmentFailureCode("error", "ValueError: ligand has no conformer"); got != "" {
		t.Fatalf("task-domain failure classified as environment failure %q", got)
	}
}

func TestPrepareAgentKernelExecutionPreservesGenerationOnAcceleratorIncompatibility(t *testing.T) {
	now := time.Now().UTC()
	spec := kernelruntime.SessionSpec{
		KernelKind: "bash", Language: "python", Environment: "selected-environment",
		RuntimeGeneration: "generation-sha256",
	}
	started := kernelruntime.ExecutionStarted{
		ExecID: "exec-architecture", ToolUseID: "tool-architecture", KernelID: "kernel-architecture",
		FrameID: "frame-architecture", Language: "python", Environment: spec.Environment,
		KernelKind: "bash", Code: "run-workload", StartedAt: now,
	}
	outcome := kernelruntime.ExecutionOutcome{
		Response:  kernelruntime.Response{ID: started.ExecID, Stderr: "RuntimeError: installed framework does not contain the observed GPU architecture sm_120"},
		StartedAt: now, FinishedAt: now.Add(time.Second),
	}
	_, result, _ := prepareAgentKernelExecution(
		workspace.KernelFrameAccess{}, spec, kernelruntime.EnsuredSession{ID: started.KernelID}, started, outcome,
	)
	if result["ok"] != false || result["code"] != "managed_environment_accelerator_incompatible" ||
		result["recoverable"] != true || result["environment_incompatible"] != true ||
		result["environment"] != spec.Environment || result["environment_generation"] != spec.RuntimeGeneration {
		t.Fatalf("environment incompatibility result=%#v", result)
	}
	for _, required := range []string{
		"Do not add downstream packages to or reuse this same generation",
		"change authority or installation phases",
		"one real accelerator operation",
	} {
		if recovery := stringValue(result["recovery"]); !strings.Contains(recovery, required) {
			t.Fatalf("environment recovery=%q; missing %q", recovery, required)
		}
	}
}

func TestPrepareAgentKernelExecutionClassifiesWorkerOOMAsRecoverable(t *testing.T) {
	now := time.Now().UTC()
	spec := kernelruntime.SessionSpec{KernelKind: "analysis", Language: "python", Environment: "managed-python"}
	started := kernelruntime.ExecutionStarted{
		ExecID: "exec-oom", ToolUseID: "tool-oom", KernelID: "kernel-oom", FrameID: "frame-oom",
		Language: "python", Environment: "managed-python", KernelKind: "analysis", Code: "run_large_analysis()", StartedAt: now,
	}
	outcome := kernelruntime.ExecutionOutcome{
		Response:  kernelruntime.Response{ID: started.ExecID},
		Err:       errors.New("kernel worker was killed — possibly out of memory"),
		StartedAt: now, FinishedAt: now.Add(time.Minute),
	}
	_, result, _ := prepareAgentKernelExecution(
		workspace.KernelFrameAccess{}, spec, kernelruntime.EnsuredSession{ID: started.KernelID}, started, outcome,
	)
	if result["ok"] != false || result["code"] != "python_memory_exhausted" || result["recoverable"] != true {
		t.Fatalf("OOM result=%#v", result)
	}
	for _, required := range []string{"Do not rerun the same working set", "prior peak memory", "one independent input unit", "chunked"} {
		if recovery := stringValue(result["recovery"]); !strings.Contains(recovery, required) {
			t.Fatalf("OOM recovery=%q; missing %q", recovery, required)
		}
	}
}

func TestReplayAgentKernelOperationResultRestoresPythonFailureCode(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createKernelAPIProjectAndFrame(t, store, "project-replay", "user-replay", "frame-replay", "OPERON", "")
	if _, err := store.SaveExecutionLog(workspace.SaveExecutionLogInput{Record: workspace.ExecutionLogRecord{
		ID: "exec-replay", FrameID: "frame-replay", CellIndex: 1,
		KernelID: "kernel-replay", KernelKind: "analysis", CondaEnv: "managed-python", Language: "python",
		Source: "redacted", Stderr: "Traceback\nFileNotFoundError: /private/input.csv",
		ExitStatus: "error", Origin: "agent", ExecutedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store})
	result, err := app.replayAgentKernelOperationResult(workspace.KernelLocalOperation{
		FrameID: "frame-replay", ExecutionID: "exec-replay", ExecutionLogID: "exec-replay",
		KernelID: "kernel-replay", Environment: "managed-python", ToolCallID: "tool-replay",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["ok"] != false || result["code"] != "python_file_not_found" || result["reused"] != true {
		t.Fatalf("replayed Python failure=%#v", result)
	}
	if message := agentruntime.ToolFailureEventMessage(result); message != "tool result reported failure: python_file_not_found" {
		t.Fatalf("replayed lifecycle summary=%q", message)
	}
}

func TestReplayAgentKernelOperationResultRestoresEnvironmentIncompatibility(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createKernelAPIProjectAndFrame(t, store, "project-runtime-replay", "user-runtime-replay", "frame-runtime-replay", "OPERON", "")
	if _, err := store.SaveExecutionLog(workspace.SaveExecutionLogInput{Record: workspace.ExecutionLogRecord{
		ID: "exec-runtime-replay", FrameID: "frame-runtime-replay", CellIndex: 1,
		KernelID: "kernel-runtime-replay", KernelKind: "bash", CondaEnv: "selected-environment", Language: "python",
		Source: "redacted", Stderr: "RuntimeError: installed framework does not contain the observed GPU architecture sm_120",
		ExitStatus: "error", Origin: "agent", ExecutedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store})
	result, err := app.replayAgentKernelOperationResult(workspace.KernelLocalOperation{
		FrameID: "frame-runtime-replay", ExecutionID: "exec-runtime-replay", ExecutionLogID: "exec-runtime-replay",
		KernelID: "kernel-runtime-replay", Environment: "selected-environment", ToolCallID: "tool-runtime-replay",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["ok"] != false || result["code"] != "managed_environment_accelerator_incompatible" ||
		result["recoverable"] != true || result["environment"] != "selected-environment" {
		t.Fatalf("replayed environment failure=%#v", result)
	}
}

func TestAgentKernelWorkspacePolicyRecoveryDoesNotTreatHandoffAsJSONExemption(t *testing.T) {
	recovery := agentKernelWorkspacePolicyRecovery([]kernelruntime.FileWrite{{
		Path: "handoff/results.json", PolicyCode: "json_workspace_file_forbidden", PolicyAction: "removed",
	}})
	for _, required := range []string{
		"handoff/ or any other persistent workspace directory does not make JSON allowed",
		"do not retry the same content under another .json/.jsonl path",
		"Markdown (.md)",
		"CSV (.csv)",
		"outside the workspace",
		"continue the same task",
	} {
		if !strings.Contains(recovery, required) {
			t.Fatalf("recovery=%q; missing %q", recovery, required)
		}
	}
}

func TestAgentSavedArtifactLineageIgnoresRejectedWorkspaceJSON(t *testing.T) {
	writes := agentSavedArtifactFileWrites([]any{
		map[string]any{"path": "results.json", "policy_code": "json_workspace_file_forbidden"},
		map[string]any{"path": "report.csv", "sha256": strings.Repeat("a", 64)},
	})
	if len(writes) != 1 || writes[0].Path != "report.csv" {
		t.Fatalf("writes=%#v", writes)
	}
}

func TestPrepareAgentKernelExecutionPreservesLegacyExecutorJSONWrites(t *testing.T) {
	now := time.Now().UTC()
	workspaceDir := t.TempDir()
	path := filepath.Join(workspaceDir, "legacy_executor_output.json")
	if err := os.WriteFile(path, []byte(`{"legacy":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := kernelruntime.SessionSpec{KernelKind: "analysis", Language: "python", Environment: "synon-biomed-python", WorkspaceDir: workspaceDir}
	started := kernelruntime.ExecutionStarted{
		ExecID: "exec-legacy-json-policy", ToolUseID: "tool-legacy-json-policy", KernelID: "kernel-legacy-json-policy",
		FrameID: "frame-legacy-json-policy", Language: "python", Environment: "synon-biomed-python",
		KernelKind: "analysis", Code: "open('legacy_executor_output.json', 'w').write('{}')", StartedAt: now,
	}
	outcome := kernelruntime.ExecutionOutcome{
		Response:     kernelruntime.Response{ID: started.ExecID, Stdout: "completed"},
		FilesWritten: []kernelruntime.FileWrite{{Path: "legacy_executor_output.json", SHA256: "old-executor-digest"}},
		StartedAt:    now, FinishedAt: now.Add(time.Second),
	}
	_, result, _ := prepareAgentKernelExecution(
		workspace.KernelFrameAccess{}, spec, kernelruntime.EnsuredSession{ID: started.KernelID}, started, outcome,
	)
	if result["ok"] != true || result["exit_status"] != "ok" {
		t.Fatalf("result=%#v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("legacy executor JSON was removed, err=%v", err)
	}
	if _, found := result["workspace_policy_violations"]; found {
		t.Fatalf("unexpected JSON policy violation=%#v", result["workspace_policy_violations"])
	}
}
