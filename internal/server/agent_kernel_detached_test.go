package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestDetachedBackgroundCompletionProducesCellResultNotification(t *testing.T) {
	access := workspace.KernelFrameAccess{
		UserID: "owner",
		Frame:  workspace.Frame{ID: "frame-background", RootFrameID: "root-background"},
	}
	started := kernelruntime.ExecutionStarted{
		ExecID: "execution-background", ToolUseID: "tool-background",
	}
	operation := workspace.KernelLocalOperation{
		State: workspace.KernelLocalOperationStateStarted, InputJSON: json.RawMessage(`{"background":true}`),
	}
	notification, err := detachedAgentKernelCompletionNotification(
		access, started, operation,
		json.RawMessage(`{"ok":true,"exit_status":"ok","stdout":"done"}`),
		1<<20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if notification == nil || notification.NotificationType != "cell_result" ||
		notification.SenderFrameID != access.Frame.ID || notification.RecipientFrameID != access.Frame.ID ||
		notification.RootFrameID != access.Frame.RootFrameID || notification.OwnerUserID != access.UserID ||
		notification.Payload["exec_id"] != started.ExecID || notification.Payload["tool_id"] != started.ToolUseID ||
		notification.Payload["status"] != "completed" ||
		!strings.Contains(stringValue(notification.Payload["output"]), `"stdout":"done"`) {
		t.Fatalf("background notification=%#v", notification)
	}

	operation.InputJSON = json.RawMessage(`{"background":false}`)
	notification, err = detachedAgentKernelCompletionNotification(
		access, started, operation,
		json.RawMessage(`{"ok":true,"exit_status":"ok"}`),
		1<<20,
	)
	if err != nil || notification != nil {
		t.Fatalf("foreground notification=%#v err=%v", notification, err)
	}
}

func TestKernelCellResultNotificationKeepsLargeResultOutOfRealtimeEnvelope(t *testing.T) {
	payload := agentKernelCellResultPayload(kernelruntime.ExecutionStarted{
		ExecID: "execution-large", ToolUseID: "tool-large",
	}, map[string]any{"ok": true, "exit_status": "ok", "stdout": strings.Repeat("x", 2<<20)}, 1<<20)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= 256<<10 || numberValue(payload["output_truncated_from_chars"]) <= 0 {
		t.Fatalf("notification bytes=%d truncated_from=%v", len(encoded), payload["output_truncated_from_chars"])
	}
}

func TestDetachedKernelSetupContextSurvivesApprovalRunnerHandoff(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	requestCtx = withTranscriptRunnerChatRun(requestCtx, run)
	executionTimeout := 75 * time.Second
	startedAt := time.Now()
	setupCtx, cancelSetup := detachedKernelSetupContext(requestCtx, executionTimeout)
	defer cancelSetup()
	cancelRequest()
	if err := setupCtx.Err(); err != nil {
		t.Fatalf("detached setup inherited parked runner cancellation: %v", err)
	}
	deadline, hasDeadline := setupCtx.Deadline()
	if !hasDeadline {
		t.Fatal("detached setup must remain bounded")
	}
	remaining := deadline.Sub(startedAt)
	want := executionTimeout + detachedKernelInfrastructureGrace
	if remaining < want-time.Second || remaining > want+time.Second {
		t.Fatalf("detached setup budget=%s want approximately %s", remaining, want)
	}
	if got := setupCtx.Value(transcriptRunnerChatRunContextKey{}); got != run {
		t.Fatalf("detached setup lost runner authority value: %#v", got)
	}
}

func TestDetachedKernelSetupContextKeepsLongExecutionUnbounded(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	requestCtx = withTranscriptRunnerChatRun(requestCtx, run)
	setupCtx, cancelSetup := detachedKernelSetupContext(requestCtx, 0)
	defer cancelSetup()
	cancelRequest()
	if err := setupCtx.Err(); err != nil {
		t.Fatalf("unbounded detached setup inherited runner cancellation: %v", err)
	}
	if _, hasDeadline := setupCtx.Deadline(); hasDeadline {
		t.Fatal("unbounded detached execution received a wall-clock deadline")
	}
	if got := setupCtx.Value(transcriptRunnerChatRunContextKey{}); got != run {
		t.Fatalf("unbounded setup lost runner authority value: %#v", got)
	}
}

func TestSoftwareRuntimeAlwaysUsesTheDetachedExecutionAuthority(t *testing.T) {
	if !agentKernelUsesDetachedExecution(softwareRuntimeToolName, false) ||
		!agentKernelUsesDetachedExecution(softwareRuntimeToolName, true) {
		t.Fatal("software runtime was allowed to use the restart-unsafe inline execution path")
	}
	for _, tool := range []string{"python", "r", "bash"} {
		if !agentKernelUsesDetachedExecution(tool, false) || !agentKernelUsesDetachedExecution(tool, true) {
			t.Fatalf("approved %s execution did not use durable detached supervision", tool)
		}
	}
	if agentKernelUsesDetachedExecution("repl", false) || !agentKernelUsesDetachedExecution("repl", true) {
		t.Fatal("interactive REPL detached routing did not remain background-only")
	}
}

func TestDetachedKernelBackendLivenessUsesHeartbeatWithoutHotLooping(t *testing.T) {
	now := time.Date(2026, time.August, 17, 1, 0, 0, 0, time.UTC)
	recent := now.Add(-detachedKernelBackendLivenessDeadline + time.Second)
	stale := now.Add(-detachedKernelBackendLivenessDeadline)
	backend := workspace.KernelExecutionBackend{UpdatedAt: now.Add(-time.Hour), HeartbeatAt: &recent}
	if detachedKernelBackendLivenessExpired(backend, now) {
		t.Fatal("recent executor heartbeat was treated as expired")
	}
	backend.HeartbeatAt = &stale
	if !detachedKernelBackendLivenessExpired(backend, now) {
		t.Fatal("stale executor heartbeat was not treated as expired")
	}
	backend.HeartbeatAt = nil
	backend.UpdatedAt = recent
	if detachedKernelBackendLivenessExpired(backend, now) {
		t.Fatal("recent starting backend was treated as expired")
	}
	backend.UpdatedAt = stale
	if !detachedKernelBackendLivenessExpired(backend, now) {
		t.Fatal("stale starting backend was not treated as expired")
	}
}

func TestDetachedKernelBackendKnownDeadProcessBypassesHeartbeatGrace(t *testing.T) {
	now := time.Date(2026, time.August, 27, 6, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Second)
	backend := workspace.KernelExecutionBackend{
		ExecutorPID: 1234, ExecutorPIDStartTicks: 5678,
		UpdatedAt: now, HeartbeatAt: &recent,
	}
	dead, err := detachedKernelBackendDefinitivelyDead(
		backend, now, func(pid int64, startTicks int64) (bool, error) {
			if pid != 1234 || startTicks != 5678 {
				t.Fatalf("process identity=%d/%d", pid, startTicks)
			}
			return false, nil
		},
	)
	if err != nil || !dead {
		t.Fatalf("known-dead executor dead=%t err=%v", dead, err)
	}

	dead, err = detachedKernelBackendDefinitivelyDead(
		backend, now, func(int64, int64) (bool, error) { return true, nil },
	)
	if err != nil || dead {
		t.Fatalf("live executor dead=%t err=%v", dead, err)
	}

	backend.ExecutorPID = 0
	backend.ExecutorPIDStartTicks = 0
	dead, err = detachedKernelBackendDefinitivelyDead(backend, now, nil)
	if err != nil || dead {
		t.Fatalf("identity-free recent backend dead=%t err=%v", dead, err)
	}
	stale := now.Add(-detachedKernelBackendLivenessDeadline)
	backend.HeartbeatAt = &stale
	dead, err = detachedKernelBackendDefinitivelyDead(backend, now, nil)
	if err != nil || !dead {
		t.Fatalf("identity-free stale backend dead=%t err=%v", dead, err)
	}
}

func TestDetachedSoftwareCancellationReceiptOverridesHarnessFailureEnvelope(t *testing.T) {
	result := map[string]any{
		"ok":          false,
		"status":      "failed",
		"code":        "launch_failed",
		"exit_status": "error",
		"cleanup": map[string]any{
			"process_tree_terminated": true,
		},
	}
	normalizeCancelledAgentKernelTerminalResult(result, softwareRuntimeToolName)
	if result["status"] != "cancelled" || result["exit_status"] != "cancelled" ||
		result["code"] != "software_runtime_cancelled" || result["ok"] != false {
		t.Fatalf("cancelled detached receipt did not remain terminal authority: %#v", result)
	}
	cleanup, ok := result["cleanup"].(map[string]any)
	if !ok || cleanup["process_tree_terminated"] != true {
		t.Fatalf("cancellation normalization discarded cleanup evidence: %#v", result)
	}
	terminalState, reasonCode := kernelLocalOperationTerminalStatus(stringValue(result["exit_status"]))
	if terminalState != workspace.KernelLocalOperationStateCancelled || reasonCode != "execution_cancelled" {
		t.Fatalf("terminal settlement=%q/%q want cancelled/execution_cancelled", terminalState, reasonCode)
	}
}

func TestDetachedKernelRequestUsesDurableModelToolCallIdentity(t *testing.T) {
	operation := workspace.KernelLocalOperation{
		OperationID: "kop-request-identity", OwnerUserID: "owner", ProjectID: "project",
		RootFrameID: "root", RootFrameIncarnationID: "root-incarnation",
		FrameID: "frame", FrameIncarnationID: "frame-incarnation",
		ToolCallID: "call-model-authority", Tool: "python", Environment: "synon-biomed-python",
		KernelID: "kernel", KernelGeneration: 3,
	}
	request := newDetachedKernelExecutionRequest(
		operation,
		kernelruntime.SessionSpec{Language: "python", KernelKind: "operon"},
		"execution-identity", "print('ready')", "/tmp/workspace", false, 75*time.Second, 1<<20,
	)
	if request.ToolCallID != operation.ToolCallID {
		t.Fatalf("request tool call id=%q want durable model id %q", request.ToolCallID, operation.ToolCallID)
	}
	if request.ExecutionID != "execution-identity" || request.ToolCallID == "agent-"+request.ExecutionID {
		t.Fatalf("request identities were conflated: %#v", request)
	}
	if request.OperationID != operation.OperationID || request.OwnerUserID != operation.OwnerUserID ||
		request.ProjectID != operation.ProjectID || request.RootFrameID != operation.RootFrameID ||
		request.RootFrameIncarnationID != operation.RootFrameIncarnationID || request.FrameID != operation.FrameID ||
		request.FrameIncarnationID != operation.FrameIncarnationID || request.KernelID != operation.KernelID ||
		request.KernelGeneration != operation.KernelGeneration || request.ToolName != operation.Tool ||
		request.Environment != operation.Environment || request.TimeoutMillis != 75000 {
		t.Fatalf("request did not preserve durable operation authority: %#v", request)
	}
}

func TestRecoveredKernelSessionSpecPreservesConfinementAndNetworkPolicy(t *testing.T) {
	input := workspace.KernelExecutionSessionSpecV1{
		Version: 1, KernelID: "kernel", OwnerUserID: "owner", ProjectID: "project",
		RootFrameID: "root", RootFrameIncarnationID: "root-incarnation",
		FrameID: "frame", FrameIncarnationID: "frame-incarnation",
		AgentName: "OPERON", KernelKind: "operon", Language: "python", Environment: "analysis",
		WorkspaceDir: "/tmp/workspace",
		Mounts: []workspace.KernelExecutionMountSpecV1{
			{Path: "/opt/synon/skills", Trusted: true},
			{Path: "/tmp/shared", Writable: true},
		},
		ProtectedPaths:       []string{"/opt/synon"},
		EgressAllowedDomains: []string{"example.org"},
		EgressDeniedDomains:  []string{"blocked.example.org"},
		CABundle:             "/etc/ssl/custom.pem", UpstreamProxy: "http://proxy.example:8080", Fresh: true,
	}
	spec := recoveredKernelSessionSpec(input)
	if len(spec.Mounts) != 2 || !spec.Mounts[0].IsTrustedReadOnlyDirectory() ||
		spec.Mounts[1].IsTrustedReadOnlyDirectory() || !spec.Mounts[1].Writable {
		t.Fatalf("recovered mounts = %#v", spec.Mounts)
	}
	if len(spec.EgressAllowedDomains) != 1 || spec.EgressAllowedDomains[0] != "example.org" ||
		len(spec.EgressDeniedDomains) != 1 || spec.EgressDeniedDomains[0] != "blocked.example.org" ||
		spec.CABundle != input.CABundle || spec.UpstreamProxy != input.UpstreamProxy || !spec.Fresh {
		t.Fatalf("recovered network policy = %#v", spec)
	}
}

func TestDetachedKernelObserversStopAndRejectNewReservations(t *testing.T) {
	server := &Server{}
	observerCtx, observerCancel, reserved := server.reserveDetachedKernelObserver(
		context.Background(), "execution-shutdown",
	)
	if !reserved {
		t.Fatal("observer reservation was rejected before shutdown")
	}
	released := make(chan struct{})
	go func() {
		<-observerCtx.Done()
		server.releaseDetachedKernelObserver("execution-shutdown", observerCancel)
		close(released)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatalf("close server with detached observer: %v", err)
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("detached observer did not release after shutdown")
	}
	if _, _, reserved := server.reserveDetachedKernelObserver(context.Background(), "after-shutdown"); reserved {
		t.Fatal("observer reservation succeeded after shutdown began")
	}
}

func TestFinishedTranscriptRunnerWakesKernelOperationRecovery(t *testing.T) {
	store, repository, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-recovery-wake", "frame-recovery-wake")
	server := New(Options{Workspace: store, Transcript: repository, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-recovery-wake", MessageUUID: "message-recovery-wake",
		ClientMessageID: "client-recovery-wake", Text: "exercise recovery wake",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repository.GetFrameStreamBySession(
		context.Background(), "local", "frame-recovery-wake",
	)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-recovery-wake",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	wake := store.KernelRetentionWake()
	if _, err := server.finishTranscriptRunner(
		context.Background(),
		&transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
		"failed", "pre-start recovery test",
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("kernel operation recovery was not woken by runner terminal transition")
	}
}
