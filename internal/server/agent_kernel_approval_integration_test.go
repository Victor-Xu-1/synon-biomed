package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type kernelApprovalExecutionResult struct {
	value map[string]any
	err   error
}

func TestKernelLocalOperationDefaultApprovalHonorsConversationPolicy(t *testing.T) {
	tests := []struct {
		name             string
		tool             string
		defaultMode      string
		conversationMode string
		want             string
	}{
		{name: "governed software runtime remains interactive", tool: softwareRuntimeToolName, want: "ask"},
		{name: "software runtime honors a global allow default", tool: softwareRuntimeToolName, defaultMode: "allow", want: "allow"},
		{name: "software runtime honors a conversation allow override", tool: softwareRuntimeToolName, conversationMode: "allow", want: "allow"},
		{name: "ordinary python remains interactive", tool: "python", want: "ask"},
		{name: "ordinary python may honor an explicit allow", tool: "python", defaultMode: "allow", want: "allow"},
		{name: "smart mode auto approves governed software runtime", tool: softwareRuntimeToolName, conversationMode: "smart", want: "allow"},
		{name: "raw confirmation remains approval required", tool: softwareRuntimeToolName, conversationMode: "confirm", want: "ask"},
		{name: "explicit conversation denial", tool: softwareRuntimeToolName, conversationMode: "deny", want: "deny"},
		{name: "global denial", tool: softwareRuntimeToolName, defaultMode: "deny", want: "deny"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := kernelLocalOperationDefaultApprovalDecision(
				test.tool, test.defaultMode, test.conversationMode,
			)
			if err != nil || got != test.want {
				t.Fatalf("decision=%q err=%v want=%q", got, err, test.want)
			}
		})
	}
	for _, scope := range []string{"conversation", "project", "always"} {
		if kernelLocalOperationApprovalScopeAllowed(softwareRuntimeToolName, scope) {
			t.Fatalf("software runtime accepted persistent approval scope %q", scope)
		}
	}
	if !kernelLocalOperationApprovalScopeAllowed(softwareRuntimeToolName, "once") ||
		!kernelLocalOperationApprovalScopeAllowed("python", "always") {
		t.Fatal("approval scope policy rejected a valid boundary")
	}
}

func TestKernelLocalOperationConversationDenyOverridesRememberedAllow(t *testing.T) {
	decision, err := kernelLocalOperationApprovalDecision(workspace.ApprovalPolicyDecision{
		Found: true, Tier: "allow", Scope: "always",
	}, "ask", "deny")
	if err != nil || decision != "deny" {
		t.Fatalf("decision=%q err=%v want deny", decision, err)
	}
}

func TestKernelApprovalConversationFrameAuthoritySurvivesResumeSessionGap(t *testing.T) {
	for _, runSessionID := range []string{"", "frame-a"} {
		got, err := kernelApprovalConversationFrameID(runSessionID, "frame-a")
		if err != nil || got != "frame-a" {
			t.Fatalf("session=%q frame authority=%q err=%v", runSessionID, got, err)
		}
	}
	if _, err := kernelApprovalConversationFrameID("other-frame", "frame-a"); err == nil {
		t.Fatal("mismatched runner session was allowed to choose another approval authority")
	}
}

func TestKernelSettlementAdmissionRetryHasNoAttemptLimitAndRespectsCancellation(t *testing.T) {
	tests := []struct {
		attempt uint64
		want    time.Duration
	}{
		{attempt: 0, want: 100 * time.Millisecond},
		{attempt: 4, want: 1600 * time.Millisecond},
		{attempt: 8, want: 25600 * time.Millisecond},
		{attempt: 9, want: 30 * time.Second},
		{attempt: 1_000_000, want: 30 * time.Second},
	}
	for _, test := range tests {
		if got := kernelSettlementAdmissionRetryDelay(test.attempt); got != test.want {
			t.Fatalf("attempt %d delay=%s want=%s", test.attempt, got, test.want)
		}
	}
	for _, attempt := range []uint64{0, 1, 2, 3, 7, 15, 119} {
		if !logKernelSettlementAdmissionAttempt(attempt) {
			t.Fatalf("attempt %d was not selected for bounded diagnostics", attempt)
		}
	}
	if logKernelSettlementAdmissionAttempt(4) || logKernelSettlementAdmissionAttempt(121) {
		t.Fatal("ordinary retry attempts were not log-throttled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitKernelSettlementAdmissionRetry(ctx, 1_000_000); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retry error=%v", err)
	}
}

func TestAgentKernelLocalExecApprovalIDBindsKernelGeneration(t *testing.T) {
	claim := transcriptstore.RunnerClaim{
		StreamUID: "stream-a", OwnerID: "owner-a", RunnerID: "runner-a", Attempt: 2, ClaimToken: "claim-token-a",
	}
	base := agentKernelLocalExecApprovalID(claim, "kernel-a", 3, "call-a", "python", "input-sha")
	if base == "" || base != agentKernelLocalExecApprovalID(claim, "kernel-a", 3, "call-a", "python", "input-sha") {
		t.Fatalf("approval ID is not deterministic: %q", base)
	}
	if base == agentKernelLocalExecApprovalID(claim, "kernel-a", 4, "call-a", "python", "input-sha") {
		t.Fatal("approval ID reused across kernel generations")
	}
	if base == agentKernelLocalExecApprovalID(claim, "kernel-b", 3, "call-a", "python", "input-sha") {
		t.Fatal("approval ID reused across kernel identities")
	}
}

func TestAgentKernelDurableOperationExecutesAndSettlesAtomically(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real durable kernel operation requires Linux confinement")
	}
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	workspaceDir := identity.workspaceDir
	if _, err := store.UpdateProject("project-routine", workspace.UpdateProjectInput{Path: &workspaceDir}); err != nil {
		t.Fatal(err)
	}
	processing := "processing"
	if _, err := store.UpdateFrame(identity.access.Frame.ID, workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccess(identity.access.Frame.ID)
	if err != nil || !found {
		t.Fatalf("access=%#v found=%t err=%v", access, found, err)
	}
	identity.access = access
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app.transcriptStore = repository
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + identity.access.Frame.ID, OwnerID: identity.access.UserID,
		ExternalID: identity.access.Frame.ID, SessionID: identity.access.Frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: identity.access.Frame.ProjectID,
		RootFrameID: identity.access.Frame.RootFrameID, FrameID: identity.access.Frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity = app.resolveAgentKernelContext(context.Background(), identity.access.Frame.ID)
	if identity == nil {
		t.Fatal("resolve canonical kernel identity")
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "durable-operation-task",
		FrameEventID: "durable-operation-task-event", MessageUUID: "durable-operation-task-message",
		Text: "Run durable Python.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "durable-operation-runner",
		TTL: 2 * time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: identity.access.Frame.ID, Attempt: int(claimed.Claim.Attempt),
		ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	durableRelativeDir := filepath.Join(identity.workspaceDir, "durable-relative")
	if err := os.MkdirAll(durableRelativeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const artifactMarker = "{{artifact:3e2b479c-6478-43ea-97ba-97afba075030}}"
	exactSource := `open("cwd-marker.txt","w").write("ok"); open("report-marker.md","w").write("` +
		artifactMarker + `"); print("x"*8192)`
	callArguments, err := json.Marshal(map[string]any{
		"code": exactSource, "environment": "python", "working_dir": "durable-relative", "background": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := agentruntime.ToolCall{
		ID: "durable-operation-call", Name: "python", Arguments: callArguments,
	}
	if err := app.checkpointChatModelToolCalls(
		SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}, run, []agentruntime.ToolCall{call},
	); err != nil {
		t.Fatal(err)
	}
	operation, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), identity.access.UserID, stream.UID, call.ID,
	)
	if err != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	if _, err := app.pauseTranscriptRunnerForApproval(context.Background(), run.Transcript, &agentruntime.PauseError{
		Status: "awaiting_approval", Message: "Kernel local execution is awaiting approval.", Data: map[string]any{
			"operation_id": operation.OperationID, "request_id": operation.ApprovalRequestID,
			"state_version": operation.StateVersion, "tool_call_id": operation.ToolCallID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	pauseCheckpoint, found, err := repository.LatestResumableCheckpoint(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || pauseCheckpoint.Sequence <= 0 {
		t.Fatalf("pause checkpoint found=%t err=%v", found, err)
	}
	operation, err = store.ResolveKernelLocalOperationApproval(context.Background(),
		workspace.ResolveKernelLocalOperationApprovalInput{
			OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
			ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
			Approved: true, DecisionID: kernelLocalOperationDecisionID(
				operation.OwnerUserID, operation.OperationID, operation.ApprovalRequestID, true, "once",
			), Scope: "once", Source: "user", ActorID: operation.OwnerUserID, AdmitRunnerRevision: true,
		})
	if err != nil {
		t.Fatal(err)
	}
	reclaimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "durable-operation-runner-resume",
		TTL: 2 * time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: pauseCheckpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed {
		t.Fatalf("reclaim=%#v err=%v", reclaimed, err)
	}
	run.Attempt = int(reclaimed.Claim.Attempt)
	run.ClaimToken = reclaimed.Claim.ClaimToken
	run.Transcript.Claim = reclaimed.Claim
	// A service restart may restore the durable Frame under a legacy session
	// alias. Kernel recovery must use the operation's canonical Frame authority,
	// not this in-memory alias.
	run.SessionID = "legacy-session-alias"
	// Model a real service restart: all process-local call/batch bindings are
	// gone and must be rebuilt from the durable batch authority.
	run.ToolBatchIDs = nil
	run.ToolBatchOrdinals = nil
	run.KernelOperationIDs = nil
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	resumed, err := app.resumeApprovedAgentKernelOperations(ctx, SessionRunnerChatOptions{
		RunnerID: claimed.Claim.RunnerID, OutputLimitBytes: 1024,
	}, run)
	if err != nil || !resumed {
		t.Fatalf("resumed=%t err=%v", resumed, err)
	}
	if got := strings.Join(run.trustedScientificReviewSignalsSnapshot(), ","); got != "execution-tool:python" {
		t.Fatalf("approval-resumed scientific execution signals=%q", got)
	}
	projected, err := repository.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveredStartFound := false
	terminalExecutionSignalFound := false
	for _, event := range projected {
		if event.Event.Type != "runner_checkpoint" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(event.ResolvedPayloadJSON, &payload) != nil {
			continue
		}
		if stringValue(payload["status"]) == "completed" && stringValue(payload["toolPhase"]) == "completed" &&
			foldedSetContains(foldedSet(stringArrayValue(payload[trustedScientificReviewSignalsField])...), "execution-tool:python") {
			terminalExecutionSignalFound = true
		}
		if stringValue(payload["message"]) != "tool python resumed" {
			continue
		}
		input, _ := payload["toolInput"].(map[string]any)
		if input["background"] != false || payload["recoveryReplay"] != true {
			t.Fatalf("recovered start payload=%#v", payload)
		}
		recoveredStartFound = true
	}
	if !recoveredStartFound {
		t.Fatal("durable recovered start checkpoint was not found")
	}
	if !terminalExecutionSignalFound {
		t.Fatal("approval-resumed terminal checkpoint omitted the trusted Python execution signal")
	}
	settled, found, err := store.GetKernelLocalOperation(context.Background(), operation.OwnerUserID, operation.OperationID)
	if err != nil || !found || settled.State != workspace.KernelLocalOperationStateCompleted ||
		settled.ExecutionLogID == "" || settled.ExecutionLogRef != "execution-log:"+settled.ExecutionLogID {
		t.Fatalf("settled=%#v found=%t err=%v", settled, found, err)
	}
	if logRecord, found, err := store.GetExecutionLog(settled.FrameID, settled.ExecutionLogID); err != nil || !found ||
		len(logRecord.Stdout) < 8192 {
		t.Fatalf("log stdout bytes=%d found=%t err=%v", len(logRecord.Stdout), found, err)
	}
	if marker, err := os.ReadFile(filepath.Join(durableRelativeDir, "cwd-marker.txt")); err != nil || string(marker) != "ok" {
		t.Fatalf("relative cwd marker=%q err=%v", marker, err)
	}
	if marker, err := os.ReadFile(filepath.Join(durableRelativeDir, "report-marker.md")); err != nil || string(marker) != artifactMarker {
		t.Fatalf("artifact presentation marker=%q err=%v", marker, err)
	}
	materialized, found, err := store.GetKernelToolResultMaterialization(context.Background(), settled.OperationID)
	if err != nil || !found || !strings.HasPrefix(materialized.ResultRef, "artifact-version:") ||
		len(materialized.TerminalResultJSON) > 1024 {
		t.Fatalf("materialized=%#v found=%t err=%v", materialized, found, err)
	}
	batchID := strings.TrimSpace(run.ToolBatchIDs[call.ID])
	batch, found, err := store.GetToolCallBatch(context.Background(), identity.access.UserID, batchID)
	if err != nil || !found || batch.State != workspace.ToolCallBatchStateSettled || batch.NextOrdinal != 1 {
		t.Fatalf("tool batch=%#v found=%t err=%v", batch, found, err)
	}
	batchItems, err := store.ListToolCallBatchItems(context.Background(), identity.access.UserID, batchID)
	if err != nil || len(batchItems) != 1 || batchItems[0].State != workspace.ToolCallBatchItemStateCompleted ||
		batchItems[0].TerminalEventID <= 0 || batchItems[0].TerminalResultSHA256 == "" ||
		batchItems[0].ResultRef != materialized.ResultRef {
		t.Fatalf("tool batch items=%#v err=%v", batchItems, err)
	}
	run.TrustedScientificReviewSignals = nil
	if err := app.restoreTrustedScientificStateForReconciledKernelOperation(run, batchItems[0], settled); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(run.trustedScientificReviewSignalsSnapshot(), ","); got != "execution-tool:python" {
		t.Fatalf("protocol-reconciled scientific execution signals=%q", got)
	}
	replayed, err := app.executeAgentKernelToolWithApproval(
		ctx, identity, call, "python", map[string]any{
			"code":        exactSource,
			"environment": "python", "working_dir": "durable-relative", "background": false,
		},
		1024,
	)
	if err != nil || stringValue(replayed["exec_id"]) != settled.ExecutionID ||
		len(stringValue(replayed["stdout"])) < 8192 {
		t.Fatalf("replayed stdout bytes=%d err=%v", len(stringValue(replayed["stdout"])), err)
	}
	executionLogs, err := store.ListExecutionLog(settled.FrameID, "")
	if err != nil || len(executionLogs) != 1 || executionLogs[0].ID != settled.ExecutionID {
		t.Fatalf("execution logs=%#v err=%v", executionLogs, err)
	}
}

func TestAgentKernelApprovalResumePreservesOracleRuntimeFailureTerminalState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real durable kernel operation requires Linux confinement")
	}
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	workspaceDir := identity.workspaceDir
	if _, err := store.UpdateProject("project-routine", workspace.UpdateProjectInput{Path: &workspaceDir}); err != nil {
		t.Fatal(err)
	}
	processing := "processing"
	if _, err := store.UpdateFrame(identity.access.Frame.ID, workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccess(identity.access.Frame.ID)
	if err != nil || !found {
		t.Fatalf("access=%#v found=%t err=%v", access, found, err)
	}
	identity.access = access
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app.transcriptStore = repository
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + identity.access.Frame.ID, OwnerID: identity.access.UserID,
		ExternalID: identity.access.Frame.ID, SessionID: identity.access.Frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: identity.access.Frame.ProjectID,
		RootFrameID: identity.access.Frame.RootFrameID, FrameID: identity.access.Frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity = app.resolveAgentKernelContext(context.Background(), identity.access.Frame.ID)
	if identity == nil {
		t.Fatal("resolve canonical kernel identity")
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "worker-preflight-task",
		FrameEventID: "worker-preflight-task-event", MessageUUID: "worker-preflight-task-message",
		Text: "Run Python through the Oracle worker.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "worker-preflight-runner",
		TTL: 2 * time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: identity.access.Frame.ID, Attempt: int(claimed.Claim.Attempt),
		ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	call := agentruntime.ToolCall{
		ID: "oracle-runtime-failure-call", Name: "python",
		Arguments: json.RawMessage(`{"code":"def score(value):\n    return value\nscore(1, 2)","environment":"python","background":false}`),
	}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID, OutputLimitBytes: 4096}
	if err := app.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	operation, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), identity.access.UserID, stream.UID, call.ID,
	)
	if err != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	if _, err := app.pauseTranscriptRunnerForApproval(context.Background(), run.Transcript, &agentruntime.PauseError{
		Status: "awaiting_approval", Message: "Kernel local execution is awaiting approval.", Data: map[string]any{
			"operation_id": operation.OperationID, "request_id": operation.ApprovalRequestID,
			"state_version": operation.StateVersion, "tool_call_id": operation.ToolCallID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	pauseCheckpoint, found, err := repository.LatestResumableCheckpoint(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("pause checkpoint found=%t err=%v", found, err)
	}
	operation, err = store.ResolveKernelLocalOperationApproval(context.Background(),
		workspace.ResolveKernelLocalOperationApprovalInput{
			OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
			ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
			Approved: true, DecisionID: "worker-preflight-decision", Scope: "once",
			Source: "user", ActorID: operation.OwnerUserID, AdmitRunnerRevision: true,
		})
	if err != nil {
		t.Fatal(err)
	}
	reclaimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "worker-preflight-runner-resume",
		TTL: 2 * time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: pauseCheckpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed {
		t.Fatalf("reclaim=%#v err=%v", reclaimed, err)
	}
	run.Attempt = int(reclaimed.Claim.Attempt)
	run.ClaimToken = reclaimed.Claim.ClaimToken
	run.Transcript.Claim = reclaimed.Claim
	run.ToolBatchIDs = nil
	run.ToolBatchOrdinals = nil
	run.KernelOperationIDs = nil
	resumed, err := app.resumeApprovedAgentKernelOperations(
		withTranscriptRunnerChatRun(context.Background(), run), options, run,
	)
	if err != nil || !resumed {
		t.Fatalf("resumed=%t err=%v", resumed, err)
	}
	settled, found, err := store.GetKernelLocalOperation(
		context.Background(), operation.OwnerUserID, operation.OperationID,
	)
	if err != nil || !found || settled.State != workspace.KernelLocalOperationStateFailed ||
		settled.ReasonCode != "execution_failed" {
		t.Fatalf("settled=%#v found=%t err=%v", settled, found, err)
	}
	result, err := app.kernelLocalOperationPersistedResult(settled)
	if err != nil || boolValue(result["ok"], true) || stringValue(result["exit_status"]) != "error" ||
		stringValue(result["code"]) != "python_type_error" || result["preflight"] != nil {
		t.Fatalf("persisted Oracle failure=%#v err=%v", result, err)
	}
}

func TestAgentKernelApprovedRunningBatchPreflightFailureSettlesWithoutRecoveryLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real durable kernel operation requires Linux confinement")
	}
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	workspaceDir := identity.workspaceDir
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProject(identity.access.Frame.ProjectID, workspace.UpdateProjectInput{Path: &workspaceDir}); err != nil {
		t.Fatal(err)
	}
	processing := "processing"
	if _, err := store.UpdateFrame(identity.access.Frame.ID, workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app.transcriptStore = repository
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + identity.access.Frame.ID, OwnerID: identity.access.UserID,
		ExternalID: identity.access.Frame.ID, SessionID: identity.access.Frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: identity.access.Frame.ProjectID,
		RootFrameID: identity.access.Frame.RootFrameID, FrameID: identity.access.Frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "preflight-failure-task",
		FrameEventID: "preflight-failure-task-event", MessageUUID: "preflight-failure-task-message",
		Text: "Run Python in the project workspace.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "preflight-failure-runner",
		TTL: 2 * time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: identity.access.Frame.ID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	outsideWorkspace := filepath.Join(root, "outside-workspace")
	if err := os.MkdirAll(outsideWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]any{
		"code": "print(1)", "environment": "python", "working_dir": outsideWorkspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := agentruntime.ToolCall{ID: "preflight-failure-call", Name: "python", Arguments: arguments}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID, OutputLimitBytes: 1024}
	if err := app.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	operation, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), identity.access.UserID, stream.UID, call.ID,
	)
	if err != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	operation, err = store.ResolveKernelLocalOperationApproval(context.Background(), workspace.ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "preflight-failure-decision", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: claimed.Claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.checkpointChatTool(options, run, "running", "tool python started", call.ID, "start", map[string]any{
		"toolName": "python", "toolInput": map[string]any{
			"code": "print(1)", "environment": "python", "working_dir": outsideWorkspace,
		},
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := app.resumeApprovedAgentKernelOperations(withTranscriptRunnerChatRun(context.Background(), run), options, run)
	if err != nil || !resumed {
		t.Fatalf("resumed=%t err=%v", resumed, err)
	}
	settled, found, err := store.GetKernelLocalOperation(context.Background(), operation.OwnerUserID, operation.OperationID)
	if err != nil || !found || settled.State != workspace.KernelLocalOperationStateCancelled ||
		settled.ReasonCode != "execution_preflight_failed" || settled.ExecutionID != "" {
		t.Fatalf("settled=%#v found=%t err=%v", settled, found, err)
	}
	batch, found, err := store.GetToolCallBatch(context.Background(), identity.access.UserID, run.ToolBatchIDs[call.ID])
	if err != nil || !found || batch.State != workspace.ToolCallBatchStateSettled || batch.NextOrdinal != 1 {
		t.Fatalf("batch=%#v found=%t err=%v", batch, found, err)
	}
	items, err := store.ListToolCallBatchItems(context.Background(), identity.access.UserID, batch.BatchID)
	if err != nil || len(items) != 1 || items[0].State != workspace.ToolCallBatchItemStateFailed {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	projected, err := repository.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	interruptions := 0
	for _, event := range projected {
		var payload map[string]any
		if event.Event.Type == "runner_checkpoint" && json.Unmarshal(event.ResolvedPayloadJSON, &payload) == nil &&
			stringValue(payload["reason_code"]) == sessionRunnerKernelOperationPendingRecoveryReasonCode {
			interruptions++
		}
	}
	if interruptions != 0 {
		t.Fatalf("kernel pending-recovery interruptions=%d want=0", interruptions)
	}
}

func TestAgentKernelApprovalSubmitBeforeConsumeAndRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real kernel approval handoff requires Linux confinement")
	}

	root := t.TempDir()
	databasePath := filepath.Join(root, "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	workspaceDir := identity.workspaceDir
	if _, err := store.UpdateProject("project-routine", workspace.UpdateProjectInput{Path: &workspaceDir}); err != nil {
		t.Fatal(err)
	}
	processing := "processing"
	if _, err := store.UpdateFrame(identity.access.Frame.ID, workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccess(identity.access.Frame.ID)
	if err != nil || !found {
		t.Fatalf("refresh kernel frame access found=%t err=%v", found, err)
	}
	identity.access = access

	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app.transcriptStore = repository
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + identity.access.Frame.ID, OwnerID: identity.access.UserID,
		ExternalID: identity.access.Frame.ID, SessionID: identity.access.Frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: identity.access.Frame.ProjectID,
		RootFrameID: identity.access.Frame.RootFrameID, FrameID: identity.access.Frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity = app.resolveAgentKernelContext(context.Background(), identity.access.Frame.ID)
	if identity == nil {
		t.Fatal("resolve canonical kernel identity after transcript stream creation")
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ClientMessageID: "approval-handoff-task", FrameEventID: "approval-handoff-task-event",
		MessageUUID: "approval-handoff-task-message", Text: "Run approved Python.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append approval task created=%t err=%v", created, err)
	}
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-approval-handoff",
		TTL: 2 * time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim approval runner=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: identity.access.Frame.ID, Attempt: int(claimed.Claim.Attempt),
		ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}

	blocker, err := app.executeAgentKernelTool(context.Background(), identity, "Python", map[string]any{
		"code": "import time; time.sleep(0.4)", "environment": "python", "background": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stringValue(blocker["exec_id"])) == "" {
		t.Fatalf("background blocker result=%#v", blocker)
	}

	markerPath := filepath.Join(identity.workspaceDir, "approval-handoff-count.txt")
	input := map[string]any{
		"code":        "from pathlib import Path\np=Path('approval-handoff-count.txt')\nn=int(p.read_text()) if p.exists() else 0\np.write_text(str(n+1))\nprint('executed')",
		"environment": "python",
	}
	call := agentruntime.ToolCall{
		ID: "approval-handoff-call", Name: "python",
		Arguments: json.RawMessage(`{"code":"from pathlib import Path\np=Path('approval-handoff-count.txt')\nn=int(p.read_text()) if p.exists() else 0\np.write_text(str(n+1))\nprint('executed')","environment":"python"}`),
	}
	if err := app.checkpointChatModelToolCalls(
		SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}, run, []agentruntime.ToolCall{call},
	); err != nil {
		t.Fatal(err)
	}
	operation, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), identity.access.UserID, stream.UID, call.ID,
	)
	if err != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	if _, err := app.pauseTranscriptRunnerForApproval(context.Background(), run.Transcript, &agentruntime.PauseError{
		Status: "awaiting_approval", Message: "Kernel local execution is awaiting approval.", Data: map[string]any{
			"operation_id": operation.OperationID, "request_id": operation.ApprovalRequestID,
			"state_version": operation.StateVersion, "tool_call_id": operation.ToolCallID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	pauseCheckpoint, found, err := repository.LatestResumableCheckpoint(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || pauseCheckpoint.Sequence <= 0 {
		t.Fatalf("pause checkpoint found=%t err=%v", found, err)
	}
	runApprovedCall := func() <-chan kernelApprovalExecutionResult {
		result := make(chan kernelApprovalExecutionResult, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		ctx = withTranscriptRunnerChatRun(ctx, run)
		go func() {
			defer cancel()
			value, err := app.executeAgentKernelToolWithApproval(
				ctx, identity, call, "Python", input, defaultSessionRunnerOutputLimitBytes,
			)
			result <- kernelApprovalExecutionResult{value: value, err: err}
		}()
		return result
	}

	requestID := waitForKernelLocalExecConfirmation(t, app, identity.access.Frame.ID)
	compatJSONRequest(t, app.Handler(), http.MethodPost,
		"/api/frames/"+identity.access.Frame.ID+"/resolve-input", identity.access.UserID,
		map[string]any{"responses": []any{map[string]any{"requestId": requestID, "action": "allow_once"}}},
		http.StatusOK)
	reclaimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-approval-handoff-resume",
		TTL: 2 * time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: pauseCheckpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed {
		t.Fatalf("reclaim approval runner=%#v err=%v", reclaimed, err)
	}
	run.Attempt = int(reclaimed.Claim.Attempt)
	run.ClaimToken = reclaimed.Claim.ClaimToken
	run.Transcript.Claim = reclaimed.Claim
	first := runApprovedCall()
	firstResult := waitForKernelApprovalExecution(t, first)
	if firstResult.err != nil || !strings.Contains(stringValue(firstResult.value["stdout"]), "executed") {
		t.Fatalf("queued approval result=%#v err=%v", firstResult.value, firstResult.err)
	}
	assertKernelApprovalEventCounts(t, store, identity.access.Frame.ID, 1, 1, 1)
	marker, err := os.ReadFile(markerPath)
	if err != nil || string(marker) != "1" {
		t.Fatalf("approved execution marker=%q err=%v", marker, err)
	}
	waitForKernelExecutionCount(t, manager, 0)
	replayed := waitForKernelApprovalExecution(t, runApprovedCall())
	if replayed.err != nil || stringValue(replayed.value["exec_id"]) != stringValue(firstResult.value["exec_id"]) {
		t.Fatalf("durable replay=%#v err=%v", replayed.value, replayed.err)
	}
	marker, err = os.ReadFile(markerPath)
	if err != nil || string(marker) != "1" {
		t.Fatalf("durable replay executed side effect marker=%q err=%v", marker, err)
	}
	confirmations, err := app.webConversationPendingConfirmations(identity.access.Frame.ID)
	if err != nil || len(confirmations) != 0 {
		t.Fatalf("terminal approval confirmations=%#v err=%v", confirmations, err)
	}
}

func waitForKernelLocalExecConfirmation(t *testing.T, app *Server, frameID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		confirmations, err := app.webConversationPendingConfirmations(frameID)
		if err != nil {
			t.Fatal(err)
		}
		if len(confirmations) == 1 {
			requestID := strings.TrimSpace(firstNonEmpty(
				stringValue(confirmations[0]["id"]), stringValue(confirmations[0]["call_id"]),
			))
			if requestID == "" || stringValue(confirmations[0]["kind"]) != "local_exec" {
				t.Fatalf("local execution confirmation=%#v", confirmations[0])
			}
			return requestID
		}
		if len(confirmations) > 1 {
			t.Fatalf("local execution confirmations=%#v", confirmations)
		}
		if time.Now().After(deadline) {
			t.Fatal("local execution approval confirmation did not appear")
		}
		<-ticker.C
	}
}

func waitForKernelApprovalExecution(t *testing.T, result <-chan kernelApprovalExecutionResult) kernelApprovalExecutionResult {
	t.Helper()
	select {
	case resolved := <-result:
		return resolved
	case <-time.After(10 * time.Second):
		t.Fatal("kernel approval execution did not finish")
		return kernelApprovalExecutionResult{}
	}
}

func waitForKernelApprovalExecutionWithReapproval(
	t *testing.T,
	result <-chan kernelApprovalExecutionResult,
	app *Server,
	frameID, userID string,
	seen map[string]bool,
) (kernelApprovalExecutionResult, int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	reapprovals := 0
	for {
		select {
		case resolved := <-result:
			return resolved, reapprovals
		case <-ticker.C:
			confirmations, err := app.webConversationPendingConfirmations(frameID)
			if err != nil {
				t.Fatal(err)
			}
			for _, confirmation := range confirmations {
				requestID := strings.TrimSpace(firstNonEmpty(
					stringValue(confirmation["id"]), stringValue(confirmation["call_id"]),
				))
				if requestID == "" || seen[requestID] {
					continue
				}
				seen[requestID] = true
				reapprovals++
				compatJSONRequest(t, app.Handler(), http.MethodPost, "/api/frames/"+frameID+"/resolve-input", userID,
					map[string]any{"responses": []any{map[string]any{"requestId": requestID, "action": "allow_once"}}},
					http.StatusOK)
			}
			if time.Now().After(deadline) {
				t.Fatal("kernel approval execution did not finish after authority-specific reapproval")
			}
		}
	}
}

func assertKernelApprovalEventCounts(
	t *testing.T,
	store *workspace.Store,
	frameID string,
	wantRequested, wantResolved, wantConsumed int,
) {
	t.Helper()
	events, err := store.ListFrameEvents(frameID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Type]++
	}
	if counts[workspace.KernelLocalExecApprovalRequestedEventType] != wantRequested ||
		counts[workspace.KernelLocalExecApprovalResolvedEventType] != wantResolved ||
		counts[workspace.KernelLocalExecApprovalConsumedEventType] != wantConsumed {
		t.Fatalf("approval event counts=%#v want requested=%d resolved=%d consumed=%d",
			counts, wantRequested, wantResolved, wantConsumed)
	}
}

func waitForKernelExecutionCount(t *testing.T, manager interface{ ActiveExecutionCount() int }, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for manager.ActiveExecutionCount() != want {
		if time.Now().After(deadline) {
			t.Fatalf("active kernel executions=%d want=%d", manager.ActiveExecutionCount(), want)
		}
		<-ticker.C
	}
}
