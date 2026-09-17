package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAgentKernelPreflightStatusSettlesEveryNonExecutedCorrection(t *testing.T) {
	for _, status := range []string{
		"dependency_preflight_required",
		"api_preflight_required",
		"code_preflight_required",
		"resource_preflight_required",
		"source_preflight_required",
		"mcp_evidence_preflight_required",
		"scientific_download_contract_preflight_required",
		"scientific_download_source_preflight_required",
		"governed_compute_required",
	} {
		if !agentKernelPreflightStatus(status) {
			t.Fatalf("preflight status %q must settle the approved operation", status)
		}
	}
	if agentKernelPreflightStatus("running") {
		t.Fatal("ordinary execution state must not be classified as a preflight correction")
	}
	guarded := map[string]any{
		"ok": false, "executed": false,
		"code": "scientific_download_contract_preflight_required",
	}
	if got := agentKernelPreflightReasonCode(guarded); got != "scientific_download_contract_preflight_required" {
		t.Fatalf("guarded preflight reason=%q", got)
	}
	if agentKernelPreflightResult(map[string]any{
		"ok": false, "executed": true, "code": "scientific_download_contract_preflight_required",
	}) {
		t.Fatal("an executed failure must not be reclassified as a non-executing preflight")
	}
}

func TestRecoveredKernelToolCheckpointStatusUsesLogicalToolResult(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation workspace.KernelLocalOperation
		result    map[string]any
		want      string
	}{
		{
			name:      "completed physical preflight with rejected logical result",
			operation: workspace.KernelLocalOperation{State: workspace.KernelLocalOperationStateCompleted},
			result:    map[string]any{"ok": false, "status": "code_preflight_required", "executed": false},
			want:      "failed",
		},
		{
			name:      "completed successful result",
			operation: workspace.KernelLocalOperation{State: workspace.KernelLocalOperationStateCompleted},
			result:    map[string]any{"ok": true, "exit_status": "ok"},
			want:      "completed",
		},
		{
			name: "successful policy preflight",
			operation: workspace.KernelLocalOperation{
				State: workspace.KernelLocalOperationStateCancelled, ReasonCode: "source_preflight_required",
			},
			result: map[string]any{"ok": true, "status": "source_preflight_required", "executed": false},
			want:   "completed",
		},
		{
			name:      "failed physical state cannot be upgraded",
			operation: workspace.KernelLocalOperation{State: workspace.KernelLocalOperationStateFailed},
			result:    map[string]any{"ok": true},
			want:      "failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, phase := recoveredKernelToolCheckpointStatus(test.operation, test.result)
			if status != test.want || phase != test.want {
				t.Fatalf("status=%q phase=%q want=%q", status, phase, test.want)
			}
		})
	}
}

func TestRecoveredKernelForegroundDetachWaitsForDurableTerminalResult(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation workspace.KernelLocalOperation
		result    map[string]any
		want      bool
		wantErr   bool
	}{
		{
			name: "foreground execution still running",
			operation: workspace.KernelLocalOperation{
				State:     workspace.KernelLocalOperationStateStarted,
				InputJSON: []byte(`{"background":false}`),
			},
			result: map[string]any{"status": "running", "exec_id": "exec-foreground"},
			want:   true,
		},
		{
			name: "explicit background execution may publish its running receipt",
			operation: workspace.KernelLocalOperation{
				State:     workspace.KernelLocalOperationStateStarted,
				InputJSON: []byte(`{"background":true}`),
			},
			result: map[string]any{"status": "running", "exec_id": "exec-background"},
		},
		{
			name: "started execution cannot return a terminal-looking result",
			operation: workspace.KernelLocalOperation{
				State:     workspace.KernelLocalOperationStateStarted,
				InputJSON: []byte(`{"background":false}`),
			},
			result:  map[string]any{"status": "completed"},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := recoveredKernelForegroundResultPending(test.operation, test.result)
			if got != test.want || (err != nil) != test.wantErr {
				t.Fatalf("pending=%t err=%v want=%t wantErr=%t", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestCheckpointSessionRunnerToolEventSettlesPendingPreflightWithoutApproval(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-kernel-preflight", "frame-kernel-preflight")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-kernel-preflight", MessageUUID: "message-kernel-preflight",
		ClientMessageID: "client-kernel-preflight", Text: "run a pharmaceutical computation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-kernel-preflight")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-preflight-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	call := agentruntime.ToolCall{
		ID: "kernel-source-preflight-call", Name: "python",
		Arguments: json.RawMessage(`{"code":"print(1)","environment":"synon-biomed-python"}`),
	}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}
	if err := server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolStarted, ToolName: "python", ToolCallID: call.ID,
		Arguments: string(call.Arguments),
	}); err != nil {
		t.Fatal(err)
	}
	preflight := `{"ok":true,"status":"source_preflight_required","executed":false}`
	if err := server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolCompleted, ToolName: "python", ToolCallID: call.ID,
		Arguments: string(call.Arguments), Result: preflight,
	}); err != nil {
		t.Fatal(err)
	}
	operation, found, err := store.GetKernelLocalOperation(
		context.Background(), stream.OwnerID, run.KernelOperationIDs[call.ID],
	)
	if err != nil || !found || operation.State != workspace.KernelLocalOperationStateCancelled ||
		operation.ApprovalDecision != "deny" || operation.ApprovalSource != "policy" ||
		operation.ApprovalActorID != "system" || operation.ApprovedAt != nil ||
		operation.ReasonCode != "source_preflight_required" ||
		operation.ExecutionID != "" {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	batch, found, err := store.GetToolCallBatch(context.Background(), stream.OwnerID, run.ToolBatchIDs[call.ID])
	if err != nil || !found || batch.State != workspace.ToolCallBatchStateSettled {
		t.Fatalf("batch=%#v found=%t err=%v", batch, found, err)
	}
}

func TestCheckpointModelVisiblePreflightFailureDoesNotCreateKernelOperation(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-model-feedback", "frame-model-feedback")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-model-feedback", MessageUUID: "message-model-feedback",
		ClientMessageID: "client-model-feedback", Text: "run a computation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-model-feedback")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "model-feedback-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	call := agentruntime.ToolCall{
		ID: "rejected-kernel-call", Name: "python",
		Arguments:               json.RawMessage(`{"code":"print(1)","environment":"synon-biomed-python"}`),
		RejectedBeforeExecution: true,
	}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}
	if err := server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if operationID := run.KernelOperationIDs[call.ID]; operationID != "" {
		t.Fatalf("rejected call created kernel operation %q", operationID)
	}
	result := `{"ok":false,"executed":false,"code":"runtime_preflight_required"}`
	if err := server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolFailed, ToolName: call.Name, ToolCallID: call.ID,
		Arguments: string(call.Arguments), Result: result, Message: "route unavailable",
		RejectedBeforeExecution: true,
	}); err != nil {
		t.Fatal(err)
	}
	batch, found, err := store.GetToolCallBatch(
		context.Background(), stream.OwnerID, run.ToolBatchIDs[call.ID],
	)
	if err != nil || !found || batch.State != workspace.ToolCallBatchStateSettled {
		t.Fatalf("batch=%#v found=%t err=%v", batch, found, err)
	}
}

func TestAgentOwnedAskUserRoutingCompletesWithoutPublicToolFailure(t *testing.T) {
	event := agentruntime.Event{Type: agentruntime.EventToolCompleted, ToolName: "ask_user", RejectedBeforeExecution: true}
	result := map[string]any{"ok": false, "executed": false, "code": "agent_owned_decision"}
	if phase := runnerCompletedToolCheckpointPhase(event, result); phase != "completed" {
		t.Fatalf("agent-owned routing phase=%q", phase)
	}
	other := map[string]any{"ok": false, "executed": false, "code": "invalid_tool_arguments"}
	if phase := runnerCompletedToolCheckpointPhase(event, other); phase != prestartToolFailurePhase {
		t.Fatalf("invalid preflight phase=%q", phase)
	}
}

func TestKernelStartedWithoutDetachedExecutionDefersFailedCheckpointToDurableRecovery(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	server := &Server{workspaceStore: store}
	operation := workspace.KernelLocalOperation{
		OperationID: "kernel-started-recovery-operation",
		State:       workspace.KernelLocalOperationStateStarted,
		ExecutionID: "kernel-started-recovery-execution",
		InputJSON:   []byte(`{"background":false}`),
	}

	terminal, err := server.kernelLocalOperationTerminalCheckpointDisposition(
		context.Background(), operation, "failed", "tool python failed after runner restart",
	)
	var pending *kernelLocalOperationPendingRecoveryError
	if terminal || !errors.As(err, &pending) || !errors.Is(err, errKernelLocalOperationPendingRecovery) {
		t.Fatalf("terminal=%t err=%v pending=%#v, want durable-recovery interruption", terminal, err, pending)
	}
	if pending.operationID != operation.OperationID || pending.state != operation.State {
		t.Fatalf("pending=%#v, want operation=%q state=%q", pending, operation.OperationID, operation.State)
	}
}

func TestForegroundDetachedKernelWaitDoesNotCommitProvisionalTerminalReceipt(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-foreground-detached", "frame-foreground-detached")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-foreground-detached", MessageUUID: "message-foreground-detached",
		ClientMessageID: "client-foreground-detached", Text: "run one long durable operation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-foreground-detached")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "foreground-detached-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	call := agentruntime.ToolCall{
		ID: "foreground-detached-call", Name: "python",
		Arguments: json.RawMessage(`{"code":"print('terminal-result')","environment":"python","background":false}`),
	}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}
	if err := server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	operation, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), stream.OwnerID, stream.UID, call.ID,
	)
	if err != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	operation, err = store.ResolveKernelLocalOperationApproval(context.Background(), workspace.ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: stream.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "foreground-detached-decision", Scope: "once",
		Source: "user", ActorID: stream.OwnerID, CurrentClaim: claimed.Claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointChatTool(options, run, "running", "tool python started", call.ID, "start", map[string]any{
		"toolName": "python", "toolInput": map[string]any{
			"code": "print('terminal-result')", "environment": "python", "background": false,
		},
	}); err != nil {
		t.Fatal(err)
	}
	operation, err = store.PrepareKernelLocalOperation(context.Background(), workspace.PrepareKernelLocalOperationInput{
		OwnerUserID: stream.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, Claim: claimed.Claim,
		BootID: "foreground-detached-boot", KernelID: "foreground-detached-kernel", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation, err = store.StartKernelLocalOperation(context.Background(), workspace.StartKernelLocalOperationInput{
		OwnerUserID: stream.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, Claim: claimed.Claim,
		BootID: "foreground-detached-boot", ExecutionID: "foreground-detached-execution",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	waited := make(chan workspace.KernelLocalOperation, 1)
	waitErr := make(chan error, 1)
	go func() {
		terminal, waitError := server.waitForStartedKernelLocalOperation(waitCtx, operation)
		if waitError != nil {
			waitErr <- waitError
			return
		}
		waited <- terminal
	}()
	select {
	case terminal := <-waited:
		t.Fatalf("started operation returned before terminal transition: %#v", terminal)
	case err := <-waitErr:
		t.Fatalf("started operation wait failed early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	resumed := make(chan bool, 1)
	resumeErr := make(chan error, 1)
	go func() {
		madeProgress, resumeError := server.resumePendingAgentToolCalls(waitCtx, options, run)
		if resumeError != nil {
			resumeErr <- resumeError
			return
		}
		resumed <- madeProgress
	}()
	select {
	case madeProgress := <-resumed:
		t.Fatalf("foreground recovery returned without a detached record before terminal transition: progress=%t", madeProgress)
	case err := <-resumeErr:
		t.Fatalf("foreground recovery failed without a detached record before terminal transition: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO kernel_execution_backends(
		backend_id,owner_user_id,project_id,root_frame_id,root_frame_incarnation_id,
		frame_id,frame_incarnation_id,kernel_id,kernel_generation,protocol_version,
		executor_instance_id,machine_boot_id,cgroup_path,socket_path,backend_generation,
		state,state_version,created_at,updated_at,session_spec_json,session_spec_sha256
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"foreground-detached-backend", operation.OwnerUserID, operation.ProjectID,
		operation.RootFrameID, operation.RootFrameIncarnationID, operation.FrameID,
		operation.FrameIncarnationID, operation.KernelID, operation.KernelGeneration, 1,
		"foreground-detached-executor", "foreground-detached-machine", "", "/tmp/foreground-detached.sock", 1,
		"starting", 1, now, now, `{"version":1}`, strings.Repeat("d", 64),
	); err != nil {
		t.Fatal(err)
	}
	detachedRequest, err := json.Marshal(workspace.KernelDetachedExecutionRequestV1{
		Version: 1, OperationID: operation.OperationID, ExecutionID: operation.ExecutionID,
		OwnerUserID: operation.OwnerUserID, ProjectID: operation.ProjectID,
		RootFrameID: operation.RootFrameID, RootFrameIncarnationID: operation.RootFrameIncarnationID,
		FrameID: operation.FrameID, FrameIncarnationID: operation.FrameIncarnationID,
		KernelID: operation.KernelID, KernelGeneration: operation.KernelGeneration,
		ToolCallID: operation.ToolCallID, ToolName: operation.Tool,
		Language: "python", KernelKind: "python", Environment: operation.Environment,
		Code: "print('terminal-result')", Background: false, TimeoutMillis: 1000,
		OutputLimitBytes: 1024, Origin: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kernel_detached_executions(
		execution_id,operation_id,backend_id,backend_generation,request_sha256,confinement_sha256,
		state,state_version,dispatch_sequence,accepted_at,dispatch_committed_at,request_written_at,
		worker_started_at,created_at,updated_at,request_json
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		operation.ExecutionID, operation.OperationID, "foreground-detached-backend", 1,
		kernelMCPEvidenceSHA256(detachedRequest), strings.Repeat("c", 64), "started", 3, 1,
		now, now, now, now, now, now, string(detachedRequest),
	); err != nil {
		t.Fatal(err)
	}

	provisional := map[string]any{
		"status": "running", "exec_id": operation.ExecutionID,
		"message": "foreground wait ended",
	}
	err = server.checkpointChatTool(options, run, "completed", "tool python completed", call.ID, "completed", map[string]any{
		"toolName": "python", "toolResult": provisional,
	})
	if !errors.Is(err, errKernelLocalOperationPendingRecovery) {
		t.Fatalf("provisional checkpoint error=%v, want durable pending recovery", err)
	}
	batch, found, err := store.GetToolCallBatch(context.Background(), stream.OwnerID, run.ToolBatchIDs[call.ID])
	if err != nil || !found || batch.State != workspace.ToolCallBatchStateRunning {
		t.Fatalf("batch=%#v found=%t err=%v", batch, found, err)
	}
	items, err := store.ListToolCallBatchItems(context.Background(), stream.OwnerID, batch.BatchID)
	if err != nil || len(items) != 1 || items[0].State != workspace.ToolCallBatchItemStateRunning {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	var provisionalEvents, provisionalReceipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_checkpoint'
		AND json_extract(payload_json,'$.toolCallId')=? AND json_extract(payload_json,'$.toolPhase')='completed'`,
		stream.UID, call.ID).Scan(&provisionalEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operation_protocol_receipts WHERE operation_id=?`,
		operation.OperationID).Scan(&provisionalReceipts); err != nil {
		t.Fatal(err)
	}
	if provisionalEvents != 0 || provisionalReceipts != 0 {
		t.Fatalf("provisional terminal events=%d receipts=%d", provisionalEvents, provisionalReceipts)
	}
	terminalResult := json.RawMessage(`{"ok":true,"exec_id":"foreground-detached-execution","stdout":"terminal-result\n","exit_status":"ok"}`)
	finished, err := store.FinishKernelLocalOperation(context.Background(), workspace.FinishKernelLocalOperationInput{
		OwnerUserID: stream.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, Claim: claimed.Claim,
		BootID: "foreground-detached-boot", ExecutionID: operation.ExecutionID,
		TerminalState: workspace.KernelLocalOperationStateCompleted, ReasonCode: "execution_completed",
		TerminalResultJSON: terminalResult,
		ExecutionLog: workspace.SaveExecutionLogInput{
			Record: workspace.ExecutionLogRecord{
				ID: operation.ExecutionID, FrameID: operation.FrameID, CellIndex: 0,
				KernelID: operation.KernelID, CondaEnv: operation.Environment, Language: "python",
				Source: "print('terminal-result')", Stdout: "terminal-result\n",
				ExitStatus: "ok", Origin: "agent", ExecutedAt: time.Now().UTC(),
			},
			ExpectedOwnerID: stream.OwnerID, ExpectedProjectID: operation.ProjectID,
			ExpectedFrameIncarnationID:     operation.FrameIncarnationID,
			ExpectedRootFrameIncarnationID: operation.RootFrameIncarnationID,
		},
	})
	if err != nil || finished.Operation.State != workspace.KernelLocalOperationStateCompleted {
		t.Fatalf("finished=%#v err=%v", finished, err)
	}
	select {
	case terminal := <-waited:
		if terminal.OperationID != operation.OperationID ||
			terminal.State != workspace.KernelLocalOperationStateCompleted {
			t.Fatalf("waited terminal operation=%#v", terminal)
		}
	case err := <-waitErr:
		t.Fatalf("started operation wait failed: %v", err)
	case <-time.After(time.Second):
		t.Fatal("terminal kernel transition did not wake the waiting runner")
	}
	select {
	case madeProgress := <-resumed:
		if !madeProgress {
			t.Fatal("foreground recovery did not reconcile the terminal operation")
		}
	case err := <-resumeErr:
		t.Fatalf("foreground recovery failed after terminal transition: %v", err)
	case <-time.After(time.Second):
		t.Fatal("terminal kernel transition did not resume the durable tool batch")
	}
	batch, found, err = store.GetToolCallBatch(context.Background(), stream.OwnerID, batch.BatchID)
	items, itemsErr := store.ListToolCallBatchItems(context.Background(), stream.OwnerID, batch.BatchID)
	if err != nil || itemsErr != nil || !found || batch.State != workspace.ToolCallBatchStateSettled ||
		len(items) != 1 || items[0].State != workspace.ToolCallBatchItemStateCompleted {
		t.Fatalf("settled batch=%#v items=%#v found=%t err=%v itemsErr=%v", batch, items, found, err, itemsErr)
	}
	var receipts, executionLogs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operation_protocol_receipts WHERE operation_id=?`,
		operation.OperationID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM execution_log WHERE id=?`, operation.ExecutionID).Scan(&executionLogs); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || executionLogs != 1 {
		t.Fatalf("terminal receipts=%d execution logs=%d, want one exact result and no duplicate execution", receipts, executionLogs)
	}
}

func TestKernelTerminalCheckpointBudgetUsesExactTranscriptEnvelope(t *testing.T) {
	claim := transcriptstore.RunnerClaim{
		StreamUID: "stream-budget", OwnerID: "owner", RunnerID: "runner-budget",
		Attempt: 7, ClaimToken: "claim-budget",
	}
	run := &sessionRunnerChatRun{
		SessionID: "frame-budget", Attempt: int(claim.Attempt), ClaimToken: claim.ClaimToken,
		AfterEventID: 19,
		Transcript: &transcriptRunnerAuthority{
			Stream: transcriptstore.Stream{UID: claim.StreamUID, OwnerID: claim.OwnerID, SessionID: "frame-budget"},
			Claim:  claim,
		},
		RequiredScientificCapabilities: []string{"python"},
	}
	operation := workspace.KernelLocalOperation{ToolCallID: "call-budget", Tool: "python"}
	budget, err := kernelTerminalCheckpointResultBudget(
		withTranscriptRunnerChatRun(context.Background(), run), operation,
		map[string]any{"ok": true}, int64(transcriptstore.MaxEventPayloadBytes),
	)
	if err != nil || budget <= 8 || budget >= int64(transcriptstore.MaxEventPayloadBytes) {
		t.Fatalf("budget=%d err=%v", budget, err)
	}
	resultJSON := json.RawMessage(`{"x":"` + strings.Repeat("a", int(budget)-8) + `"}`)
	input := chatToolCheckpointInput(
		SessionRunnerChatOptions{RunnerID: claim.RunnerID}, run,
		"completed", "tool python completed", operation.ToolCallID, "completed",
		map[string]any{"toolName": operation.Tool, "toolResult": resultJSON},
	)
	payload := transcriptCheckpointPayload(input)
	payload["visibility"] = "provisional"
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) != transcriptstore.MaxEventPayloadBytes {
		t.Fatalf("payload bytes=%d want=%d err=%v", len(encoded), transcriptstore.MaxEventPayloadBytes, err)
	}
	input["toolResult"] = append(resultJSON[:len(resultJSON)-2], 'a', '"', '}')
	tooLargePayload := transcriptCheckpointPayload(input)
	tooLargePayload["visibility"] = "provisional"
	tooLarge, err := json.Marshal(tooLargePayload)
	if err != nil || len(tooLarge) <= transcriptstore.MaxEventPayloadBytes {
		t.Fatalf("oversize payload bytes=%d err=%v", len(tooLarge), err)
	}
}

func TestKernelTerminalCheckpointBudgetUsesDurableOperationAfterRunnerExit(t *testing.T) {
	operation := workspace.KernelLocalOperation{
		FrameID: "frame-durable-budget", RunnerID: "runner-before-restart",
		ToolCallID: "call-durable-budget", Tool: "python",
	}
	budget, err := kernelTerminalCheckpointResultBudget(
		context.Background(), operation, map[string]any{"ok": true},
		int64(transcriptstore.MaxEventPayloadBytes),
	)
	if err != nil || budget <= 8 || budget >= int64(transcriptstore.MaxEventPayloadBytes) {
		t.Fatalf("durable budget=%d err=%v", budget, err)
	}
	run, err := durableKernelTerminalCheckpointBudgetRun(operation)
	if err != nil {
		t.Fatal(err)
	}
	resultJSON := json.RawMessage(`{"x":"` + strings.Repeat("a", int(budget)-8) + `"}`)
	input := chatToolCheckpointInput(
		SessionRunnerChatOptions{RunnerID: run.Transcript.Claim.RunnerID}, run,
		"completed", "tool python completed", operation.ToolCallID, "completed",
		map[string]any{"toolName": operation.Tool, "toolResult": resultJSON},
	)
	payload := transcriptCheckpointPayload(input)
	payload["visibility"] = "provisional"
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) != transcriptstore.MaxEventPayloadBytes {
		t.Fatalf("durable payload bytes=%d want=%d err=%v", len(encoded), transcriptstore.MaxEventPayloadBytes, err)
	}
}

func TestCheckpointChatModelToolCallsCreatesDurableKernelOperationAtomically(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-kernel-operation", "frame-kernel-operation")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-kernel-operation", MessageUUID: "message-kernel-operation",
		ClientMessageID: "client-kernel-operation", Text: "run a durable kernel operation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-kernel-operation")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-operation-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	calls := []agentruntime.ToolCall{
		{ID: "kernel-call", Name: "python", Arguments: json.RawMessage(`{"code":"print(1)","environment":"synon-biomed-python"}`)},
		{ID: "reviewer-repl-call", Name: "repl", Arguments: json.RawMessage(`{"code":"print('review')","fresh":"True","background":"false","human_description":"Checking review evidence"}`)},
		{ID: "software-runtime-call", Name: softwareRuntimeToolName, Arguments: json.RawMessage(`{"capability":"sequence-alignment","language":"native","packages":[{"manager":"conda","spec":"minimap2"}],"executable":"minimap2","args":["--version"]}`)},
		{ID: "invalid-software-runtime-call", Name: softwareRuntimeToolName, Arguments: json.RawMessage(`{"capability":"r-summary","language":"r","packages":[{"manager":"conda","spec":"r-jsonlite=2.0.0"}],"imports":["jsonlite"],"executable":"Rscript"}`)},
		{ID: "read-call", Name: "read_file", Arguments: json.RawMessage(`{"path":"notes.txt"}`)},
	}
	if err := server.checkpointChatModelToolCalls(SessionRunnerChatOptions{RunnerID: "kernel-operation-runner"}, run, calls); err != nil {
		t.Fatal(err)
	}
	var operationID string
	if err := db.QueryRow(`SELECT operation_id FROM kernel_local_operations WHERE stream_uid=?`, stream.UID).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	operation, found, err := store.GetKernelLocalOperation(context.Background(), "local", operationID)
	if err != nil || !found || operation.Tool != "python" || operation.ToolCallID != "kernel-call" ||
		operation.SourceRunnerAttempt != claimed.Claim.Attempt || operation.State != "pending_approval" {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	reviewerOperation, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), "local", stream.UID, "reviewer-repl-call",
	)
	if err != nil || !found || reviewerOperation.Tool != "repl" ||
		!strings.Contains(string(reviewerOperation.InputJSON), `"fresh":true`) ||
		!strings.Contains(string(reviewerOperation.InputJSON), `"background":false`) {
		t.Fatalf("reviewer operation=%#v found=%t err=%v", reviewerOperation, found, err)
	}
	softwareOperation, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), "local", stream.UID, "software-runtime-call",
	)
	if err != nil || !found || softwareOperation.Tool != softwareRuntimeToolName ||
		softwareOperation.State != workspace.KernelLocalOperationStatePendingApproval {
		t.Fatalf("software operation=%#v found=%t err=%v", softwareOperation, found, err)
	}
	if rejectedOperation, rejectedFound, rejectedErr := store.GetKernelLocalOperationByToolCall(
		context.Background(), "local", stream.UID, "invalid-software-runtime-call",
	); rejectedErr != nil || rejectedFound {
		t.Fatalf("rejected software operation=%#v found=%t err=%v", rejectedOperation, rejectedFound, rejectedErr)
	}
	if err := server.checkpointChatModelToolCalls(SessionRunnerChatOptions{RunnerID: "kernel-operation-runner"}, run, calls); err != nil {
		t.Fatal(err)
	}
	var operationCount, checkpointCount, toolItemCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operations WHERE stream_uid=?`, stream.UID).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_checkpoint'
		AND json_extract(payload_json,'$.modelToolCalls') IS NOT NULL`, stream.UID).Scan(&checkpointCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_tool_call_items item
		JOIN transcript_tool_call_batches batch ON batch.batch_id=item.batch_id WHERE batch.stream_uid=?`, stream.UID).Scan(&toolItemCount); err != nil {
		t.Fatal(err)
	}
	if operationCount != 3 || checkpointCount != 1 || toolItemCount != 5 {
		t.Fatalf("operations=%d checkpoints=%d tool_items=%d", operationCount, checkpointCount, toolItemCount)
	}
}

func TestCheckpointChatToolDefersPreparedKernelFailureToDurableRecovery(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-kernel-prestart", "frame-kernel-prestart")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-kernel-prestart", MessageUUID: "message-kernel-prestart",
		ClientMessageID: "client-kernel-prestart", Text: "run a durable kernel operation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-kernel-prestart")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-prestart-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	call := agentruntime.ToolCall{
		ID: "kernel-prestart-call", Name: "python",
		Arguments: json.RawMessage(`{"code":"print(1)","environment":"synon-biomed-python"}`),
	}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}
	if err := server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	operationID := run.KernelOperationIDs[call.ID]
	operation, found, err := store.GetKernelLocalOperation(context.Background(), stream.OwnerID, operationID)
	if err != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	operation, err = store.ResolveKernelLocalOperationApproval(context.Background(), workspace.ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: stream.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "kernel-prestart-decision", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: claimed.Claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointChatTool(options, run, "running", "tool python started", call.ID, "start", map[string]any{
		"toolName": "python", "toolInput": map[string]any{"code": "print(1)", "environment": "synon-biomed-python"},
	}); err != nil {
		t.Fatal(err)
	}
	operation, err = store.PrepareKernelLocalOperation(context.Background(), workspace.PrepareKernelLocalOperationInput{
		OwnerUserID: stream.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, Claim: claimed.Claim,
		BootID: "kernel-prestart-boot", KernelID: "kernel-prestart-id", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("a", 64),
	})
	if err != nil || operation.State != workspace.KernelLocalOperationStatePrepared {
		t.Fatalf("prepared=%#v err=%v", operation, err)
	}
	failureMessage := "tool python failed: transcript schema is unavailable"
	err = server.checkpointChatTool(options, run, "failed", failureMessage, call.ID, "failed", map[string]any{
		"toolName": "python", "toolResult": map[string]any{"ok": false, "error": "transcript schema is unavailable"},
	})
	if !errors.Is(err, errKernelLocalOperationPendingRecovery) || err.Error() != failureMessage {
		t.Fatalf("pre-start checkpoint error=%v", err)
	}
	operation, found, err = store.GetKernelLocalOperation(context.Background(), stream.OwnerID, operationID)
	if err != nil || !found || operation.State != workspace.KernelLocalOperationStatePrepared {
		t.Fatalf("operation after failure=%#v found=%t err=%v", operation, found, err)
	}
	batch, found, err := store.GetToolCallBatch(context.Background(), stream.OwnerID, run.ToolBatchIDs[call.ID])
	if err != nil || !found || batch.State != workspace.ToolCallBatchStateRunning {
		t.Fatalf("batch=%#v found=%t err=%v", batch, found, err)
	}
	items, err := store.ListToolCallBatchItems(context.Background(), stream.OwnerID, batch.BatchID)
	if err != nil || len(items) != 1 || items[0].State != workspace.ToolCallBatchItemStateRunning {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	var terminalCheckpoints, receipts, materializations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_checkpoint'
		AND json_extract(payload_json,'$.toolCallId')=? AND json_extract(payload_json,'$.toolPhase')='failed'`,
		stream.UID, call.ID).Scan(&terminalCheckpoints); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operation_protocol_receipts WHERE operation_id=?`, operationID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operation_materializations WHERE operation_id=?`, operationID).Scan(&materializations); err != nil {
		t.Fatal(err)
	}
	if terminalCheckpoints != 0 || receipts != 0 || materializations != 0 {
		t.Fatalf("terminal checkpoints=%d receipts=%d materializations=%d", terminalCheckpoints, receipts, materializations)
	}
}

func TestResumePendingKernelToolBatchPreparedOperationInterruptsForDurableRecovery(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-kernel-prepared-resume", "frame-kernel-prepared-resume")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-kernel-prepared-resume", MessageUUID: "message-kernel-prepared-resume",
		ClientMessageID: "client-kernel-prepared-resume", Text: "run a durable kernel operation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-kernel-prepared-resume")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	first, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-prepared-resume-runner-a",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(first.Claim.Attempt), ClaimToken: first.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: first.Claim},
	}
	call := agentruntime.ToolCall{
		ID: "kernel-prepared-resume-call", Name: "python",
		Arguments: json.RawMessage(`{"code":"print(1)","environment":"synon-biomed-python"}`),
	}
	firstOptions := SessionRunnerChatOptions{RunnerID: first.Claim.RunnerID}
	if err := server.checkpointChatModelToolCalls(firstOptions, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	operationID := run.KernelOperationIDs[call.ID]
	operation, found, err := store.GetKernelLocalOperation(context.Background(), stream.OwnerID, operationID)
	if err != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	operation, err = store.ResolveKernelLocalOperationApproval(context.Background(), workspace.ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: stream.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "kernel-prepared-resume-decision", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: first.Claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointChatTool(firstOptions, run, "running", "tool python started", call.ID, "start", map[string]any{
		"toolName": "python", "toolInput": map[string]any{"code": "print(1)", "environment": "synon-biomed-python"},
	}); err != nil {
		t.Fatal(err)
	}
	operation, err = store.PrepareKernelLocalOperation(context.Background(), workspace.PrepareKernelLocalOperationInput{
		OwnerUserID: stream.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, Claim: first.Claim,
		BootID: "kernel-prepared-resume-boot", KernelID: "kernel-prepared-resume-id", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("b", 64),
	})
	if err != nil || operation.State != workspace.KernelLocalOperationStatePrepared {
		t.Fatalf("prepared=%#v err=%v", operation, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "kernel-prepared-resume-old-runner-failed",
		Status: "failed", PayloadJSON: []byte(`{"status":"failed","detail":"simulated restart before kernel start"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ClientMessageID: "kernel-prepared-resume-continue", PayloadJSON: []byte(`{"text":"continue"}`),
	}); err != nil || !created {
		t.Fatalf("append continuation created=%t err=%v", created, err)
	}
	second, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID:             "frame-kernel-prepared-resume",
		RunnerID:              "kernel-prepared-resume-runner-b",
		Endpoint:              BuiltinSessionRunnerChatEndpoint,
		Model:                 BuiltinSessionRunnerChatModel,
		LeaseTTL:              time.Minute,
		ReplayLimit:           100,
		OutputLimitBytes:      1024,
		DisableSkillDiscovery: true,
		DisableMCPDiscovery:   true,
	})
	if err != nil {
		t.Fatalf("recovered runner error=%v", err)
	}
	if !second.Claimed || int64(second.Attempt) != first.Claim.Attempt+1 || second.Status != "interrupted" ||
		second.FinishEventID != 0 || second.InterruptionReasonCode != sessionRunnerKernelOperationPendingRecoveryReasonCode ||
		!second.InterruptionAutoResume || second.KernelOperationID != operationID {
		t.Fatalf("recovered runner result=%#v, want same-task auto-resumable interruption", second)
	}
	operation, found, err = store.GetKernelLocalOperation(context.Background(), stream.OwnerID, operationID)
	if err != nil || !found || operation.State != workspace.KernelLocalOperationStateApproved ||
		operation.RunnerID != "" || operation.PreparedAt != nil || operation.ExecutionID != "" {
		t.Fatalf("prepared handoff was not reclaimed before redispatch: operation=%#v found=%t err=%v", operation, found, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, int64(second.Attempt))
	if err != nil || state.Status != "running" || state.ExpiresAt.After(time.Now().Add(100*time.Millisecond)) {
		t.Fatalf("durable interrupted runtime state=%#v err=%v", state, err)
	}
	var interruptionCheckpoints int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_checkpoint'
		AND json_extract(payload_json,'$.reason_code')=? AND json_extract(payload_json,'$.status')='interrupted'`,
		stream.UID, sessionRunnerKernelOperationPendingRecoveryReasonCode).Scan(&interruptionCheckpoints); err != nil {
		t.Fatal(err)
	}
	if interruptionCheckpoints != 1 {
		t.Fatalf("pending-recovery interruption checkpoints=%d want=1", interruptionCheckpoints)
	}
	if strings.Contains(second.InterruptionReasonCode, "reconciliation") {
		t.Fatalf("interruption reason exposed obsolete reconciliation wording: %q", second.InterruptionReasonCode)
	}
	if !runnerInterruptionMayContinueSameTask(sessionRunnerKernelOperationPendingRecoveryReasonCode) ||
		!runnerInterruptionAutoResume(sessionRunnerKernelOperationPendingRecoveryReasonCode) {
		t.Fatal("kernel pending recovery must remain same-task auto-resumable")
	}
}

func TestCompatibilityResolveKernelOperationApprovalSurvivesWithoutProcessWaiter(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-kernel-approval", "frame-kernel-approval")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-kernel-approval", MessageUUID: "message-kernel-approval",
		ClientMessageID: "client-kernel-approval", Text: "run a durable kernel operation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-kernel-approval")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-approval-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	if err := server.checkpointChatModelToolCalls(SessionRunnerChatOptions{RunnerID: "kernel-approval-runner"}, run,
		[]agentruntime.ToolCall{{
			ID: "kernel-approval-call", Name: "python",
			Arguments: json.RawMessage(`{"code":"print(1)","environment":"synon-biomed-python"}`),
		}}); err != nil {
		t.Fatal(err)
	}
	var operationID, requestID string
	if err := db.QueryRow(`SELECT operation_id,approval_request_id FROM kernel_local_operations WHERE stream_uid=?`,
		stream.UID).Scan(&operationID, &requestID); err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccess("frame-kernel-approval")
	if err != nil || !found {
		t.Fatalf("access=%#v found=%t err=%v", access, found, err)
	}
	toolInput := map[string]any{"code": "print(1)", "environment": "synon-biomed-python"}
	_, err = server.authorizeAgentKernelLocalOperation(
		withTranscriptRunnerChatRun(context.Background(), run),
		&agentKernelContext{access: access, workspaceDir: t.TempDir()},
		agentruntime.ToolCall{ID: "kernel-approval-call", Name: "python"},
		"python", toolInput, "synon-biomed-python",
	)
	var pause *agentruntime.PauseError
	if !errors.As(err, &pause) || pause.Status != "awaiting_approval" ||
		stringValue(pause.Data["operation_id"]) != operationID {
		t.Fatalf("pause=%#v err=%v", pause, err)
	}
	pauseEvent, err := server.pauseTranscriptRunnerForApproval(context.Background(), run.Transcript, pause)
	if err != nil || pauseEvent.EventID <= 0 {
		t.Fatalf("pause event=%#v err=%v", pauseEvent, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claimed.Claim.Attempt)
	if err != nil || state.Phase != transcriptstore.RunnerPhaseWaitingApproval || state.ExpiresAt.After(time.Now()) {
		t.Fatalf("paused state=%#v err=%v", state, err)
	}

	response := compatJSONRequest(t, server.Handler(), http.MethodPost,
		"/api/frames/frame-kernel-approval/resolve-input", "local", map[string]any{
			"responses": []any{map[string]any{"requestId": requestID, "action": "allow_once"}},
		}, http.StatusOK)
	if response["status"] != "processing" || numberValue(response["remaining"]) != 0 {
		t.Fatalf("response=%#v", response)
	}
	repeated := compatJSONRequest(t, server.Handler(), http.MethodPost,
		"/api/frames/frame-kernel-approval/resolve-input", "local", map[string]any{
			"responses": []any{map[string]any{"requestId": requestID, "action": "allow_once"}},
		}, http.StatusOK)
	if repeated["status"] != "processing" {
		t.Fatalf("repeated response=%#v", repeated)
	}
	operation, found, err := store.GetKernelLocalOperation(context.Background(), "local", operationID)
	if err != nil || !found || operation.State != workspace.KernelLocalOperationStateApproved ||
		operation.ApprovalDecision != "allow" || operation.ApprovalScope != "once" ||
		operation.ApprovalSource != "user" || operation.ApprovalActorID != "local" {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-kernel-approval")
	if err != nil || !found || len(compatibilityServerPendingInputs(metadata.ContextData)) != 0 {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	next, err := repo.ClaimNextRunner(context.Background(), transcriptstore.ClaimNextRunnerInput{
		RunnerID: "kernel-approval-resume", TTL: time.Minute,
	})
	if err != nil || !next.Claimed || next.Stream.UID != stream.UID ||
		next.Claim.ResumeSource != transcriptstore.ResumeSourceCheckpoint {
		t.Fatalf("resume=%#v err=%v", next, err)
	}
	var resolvedEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=? AND event_type=?`,
		"frame-kernel-approval", workspace.KernelLocalExecApprovalResolvedEventType).Scan(&resolvedEvents); err != nil || resolvedEvents != 1 {
		t.Fatalf("resolved events=%d err=%v", resolvedEvents, err)
	}
}

func TestResumePendingKernelOperationHonorsFullAccessBeforePausing(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-kernel-full-access", "frame-kernel-full-access")
	seedPermissionTestAgent(t, store, "local")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, err := store.SetFrameRuntimeMetadata("frame-kernel-full-access", workspace.FrameRuntimeMetadata{
		FrameID: "frame-kernel-full-access",
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "allow"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-kernel-full-access", MessageUUID: "message-kernel-full-access",
		ClientMessageID: "client-kernel-full-access", Text: "run a durable kernel operation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-kernel-full-access")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-full-access-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	call := agentruntime.ToolCall{
		ID: "kernel-full-access-call", Name: "python",
		Arguments: json.RawMessage(`{"code":"print(1)","environment":"synon-biomed-python"}`),
	}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}
	if err := server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	checkpointOperation, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), stream.OwnerID, stream.UID, call.ID,
	)
	if err != nil || !found || checkpointOperation.State != workspace.KernelLocalOperationStateApproved ||
		checkpointOperation.ApprovalSource != "policy" || checkpointOperation.ApprovalActorID != "system" {
		t.Fatalf("checkpoint operation=%#v found=%t err=%v", checkpointOperation, found, err)
	}
	if server.TryDrainIdle() {
		t.Fatal("deployment drain crossed an approved operation before execution began")
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-kernel-full-access")
	if err != nil || !found || len(compatibilityServerPendingInputs(metadata.ContextData)) != 0 {
		t.Fatalf("checkpoint metadata=%#v found=%t err=%v", metadata, found, err)
	}
	if err := server.checkpointChatTool(options, run, "running", "tool python started", call.ID, "start", map[string]any{
		"toolName": "python", "toolInput": map[string]any{"code": "print(1)", "environment": "synon-biomed-python"},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = server.resumePendingAgentToolCalls(context.Background(), options, run)
	var pause *agentruntime.PauseError
	if errors.As(err, &pause) {
		t.Fatalf("full-access recovery unexpectedly paused: %#v", pause)
	}
	if err != nil && !errors.Is(err, errKernelLocalOperationPendingRecovery) {
		t.Fatalf("full-access recovery error=%v", err)
	}
	operation, found, loadErr := store.GetKernelLocalOperationByToolCall(
		context.Background(), stream.OwnerID, stream.UID, call.ID,
	)
	if loadErr != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, loadErr)
	}
	if operation.State == workspace.KernelLocalOperationStatePendingApproval {
		t.Fatalf("full-access recovery left operation pending approval: %#v", operation)
	}
}

func TestKernelApprovalRecoveryDoesNotForkLiveTranscriptRunner(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-kernel-live-runner", "frame-kernel-live-runner")
	if _, _, err := (&Server{workspaceStore: store, transcriptStore: repo}).submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-kernel-live-runner", MessageUUID: "message-kernel-live-runner",
		ClientMessageID: "client-kernel-live-runner", Text: "run one local operation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-kernel-live-runner")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-live-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo}
	live, err := server.kernelLocalOperationRunnerAttemptLive(context.Background(), workspace.KernelLocalOperation{
		OwnerUserID: "local", StreamUID: stream.UID, RunnerID: claimed.Claim.RunnerID,
		RunnerAttempt: claimed.Claim.Attempt,
	})
	if err != nil || !live {
		t.Fatalf("live=%t err=%v", live, err)
	}
	server.transcriptStore = nil
	server.sessionRuns = map[string]*activeSessionRun{
		"frame-kernel-live-runner": {runnerID: claimed.Claim.RunnerID},
	}
	live, err = server.kernelLocalOperationRunnerAttemptLive(context.Background(), workspace.KernelLocalOperation{
		FrameID: "frame-kernel-live-runner",
	})
	if err != nil || !live {
		t.Fatalf("process-local live=%t err=%v", live, err)
	}
}
