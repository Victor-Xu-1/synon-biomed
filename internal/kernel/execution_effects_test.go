package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"synon-go/internal/executionprep"
)

func observationSessionSpec(t *testing.T) SessionSpec {
	t.Helper()
	return SessionSpec{
		OwnerID: "observation-owner", ProjectID: "observation-project", FrameID: "observation-frame",
		FrameIncarnationID: "observation-incarnation", RootFrameID: "observation-root",
		RootFrameIncarnationID: "observation-root-incarnation", AgentName: "OPERON",
		KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	}
}

func prepareObservationRequest(t *testing.T, manager *Manager, spec SessionSpec, id, source string) SubmitRequest {
	t.Helper()
	result, err := manager.PrepareExecutionSource(context.Background(), executionprep.Request{Language: "python", Source: source})
	if err != nil || !result.Observation.Matches("python", source) {
		t.Fatalf("native observation plan: %#v %v", result, err)
	}
	return SubmitRequest{
		OwnerID: spec.OwnerID, ProjectID: spec.ProjectID, FrameID: spec.FrameID,
		FrameIncarnationID: spec.FrameIncarnationID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		KernelKind: spec.KernelKind, Language: spec.Language, Environment: spec.Environment,
		ExecID: id, ToolUseID: id, ToolName: "python", Code: source, Origin: "agent", Observation: result.Observation,
	}
}

func assertObservationRefusal(t *testing.T, handle *ExecutionHandle, reason string) {
	t.Helper()
	outcome := waitForLifecycleOutcome(t, handle)
	if outcome.Err != nil || !outcome.StartedAt.IsZero() || outcome.Response.Stdout != "" || len(outcome.FilesWritten) != 0 ||
		outcome.Response.Preflight["executed"] != false || outcome.Response.Preflight["reason"] != reason {
		t.Fatalf("not a non-executing observation refusal: %#v", outcome)
	}
	if started, ok := <-handle.Started(); ok {
		t.Fatalf("refused source published an execution start: %#v", started)
	}
}

func TestObservationManagerUsesRealWorkerAndPreservesNamespace(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	spec := observationSessionSpec(t)
	session, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	request := prepareObservationRequest(t, manager, spec, "observed-1", `print('ready')`)
	request.ExpectedGeneration = session.Worker.Generation()
	handle, err := manager.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	outcome := waitForLifecycleOutcome(t, handle)
	if outcome.Err != nil || outcome.Response.Error != "" || outcome.Response.Preflight != nil || outcome.Response.Stdout != "ready\n" {
		t.Fatalf("native observation failed: %#v", outcome)
	}
	plain := request
	plain.ExecID, plain.ToolUseID, plain.Code, plain.Observation = "ordinary", "ordinary", "retained = 31", nil
	handle, err = manager.Submit(plain)
	if err != nil {
		t.Fatal(err)
	}
	if outcome := waitForLifecycleOutcome(t, handle); outcome.Err != nil || outcome.Response.Error != "" {
		t.Fatal(outcome)
	}
	request.ExecID, request.ToolUseID = "observed-2", "observed-2"
	handle, err = manager.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	assertObservationRefusal(t, handle, "runtime_binding_provenance_unproved")
	plain.ExecID, plain.ToolUseID, plain.Code = "ordinary-check", "ordinary-check", "print(retained)"
	handle, err = manager.Submit(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForLifecycleOutcome(t, handle).Response.Stdout; got != "31\n" {
		t.Fatalf("namespace changed: %q", got)
	}
	if session.Worker.Generation() != request.ExpectedGeneration {
		t.Fatal("observation reset the worker generation")
	}
}

func TestObservationManagerRejectsChangedSourceAndGeneration(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	spec := observationSessionSpec(t)
	session, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	request := prepareObservationRequest(t, manager, spec, "changed", "print(1)")
	request.Code = "print(2)"
	handle, err := manager.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	assertObservationRefusal(t, handle, "diagnostic_source_binding_mismatch")
	request.ExpectedGeneration = session.Worker.Generation() + 1
	if _, err := manager.Submit(request); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale generation was accepted: %v", err)
	}
}

func TestObservationManagerChecksPollutionUnderExecutionLock(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	spec := observationSessionSpec(t)
	_, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	request := prepareObservationRequest(t, manager, spec, "observation-after-ordinary", "print(1)")
	plain := request
	plain.Observation = nil
	plain.ExecID, plain.ToolUseID = "ordinary-running", "ordinary-running"
	plain.Code = "import time\nprint = lambda *args: None\ntime.sleep(0.25)"
	ordinary, err := manager.Submit(plain)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ordinary.Started():
		if !ok {
			t.Fatal("ordinary execution did not start")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	handle, err := manager.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	assertObservationRefusal(t, handle, "runtime_binding_provenance_unproved")
	if outcome := waitForLifecycleOutcome(t, ordinary); outcome.Err != nil || outcome.Response.Error != "" {
		t.Fatal(outcome)
	}
}

func TestObservationRequestFreezesCallerOwnedPlan(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	spec := observationSessionSpec(t)
	_, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	request := prepareObservationRequest(t, manager, spec, "frozen-observation", "print(1)")
	gate := make(chan error, 1)
	request.StartAuthorization = gate
	handle, err := manager.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Observation.Operations[0] = "python.unknown"
	request.Observation.SourceSHA256 = "forged"
	gate <- nil
	close(gate)
	outcome := waitForLifecycleOutcome(t, handle)
	if outcome.Response.Stdout != "1\n" || outcome.Response.Preflight != nil || outcome.Err != nil {
		t.Fatalf("mutable proof leaked into queue: %#v", outcome)
	}
}

func TestObservationCancelledUndispatchedCellDoesNotTaintWorker(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	spec := observationSessionSpec(t)
	_, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	request := prepareObservationRequest(t, manager, spec, "after-cancel", "print(1)")
	plain := request
	plain.ExecID, plain.ToolUseID, plain.Observation = "cancel-before-dispatch", "cancel-before-dispatch", nil
	plain.StartAuthorization = make(chan error)
	handle, err := manager.Submit(plain)
	if err != nil {
		t.Fatal(err)
	}
	if interrupted := manager.Interrupt(spec.FrameID, plain.ExecID); !interrupted.Dequeued {
		t.Fatalf("not dequeued: %#v", interrupted)
	}
	if outcome := waitForLifecycleOutcome(t, handle); !outcome.Dequeued {
		t.Fatal(outcome)
	}
	handle, err = manager.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	outcome := waitForLifecycleOutcome(t, handle)
	if outcome.Response.Stdout != "1\n" || outcome.ObservationRefused || outcome.Err != nil {
		t.Fatalf("an undispatched cell polluted provenance: %#v", outcome)
	}
}

func TestObservationRawWorkerAPIAlsoTaintsProvenance(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	spec := observationSessionSpec(t)
	session, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	response, err := session.Worker.Execute(context.Background(), "retained = 2", "user")
	if err != nil || response.Error != "" {
		t.Fatalf("raw execution: %#v %v", response, err)
	}
	handle, err := manager.Submit(prepareObservationRequest(t, manager, spec, "after-raw", "print(1)"))
	if err != nil {
		t.Fatal(err)
	}
	assertObservationRefusal(t, handle, "runtime_binding_provenance_unproved")
}
