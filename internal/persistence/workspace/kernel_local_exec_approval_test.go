package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestKernelLocalExecApprovalOnceIsDurableAndSingleConsumption(t *testing.T) {
	store, dbPath, request := newKernelLocalExecApprovalFixture(t)
	event, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request)
	if err != nil || event.Type != KernelLocalExecApprovalRequestedEventType {
		t.Fatalf("request event=%#v err=%v", event, err)
	}
	repeatedRequest, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request)
	if err != nil || repeatedRequest.ID != event.ID {
		t.Fatalf("repeated request event=%#v want=%q err=%v", repeatedRequest, event.ID, err)
	}
	metadata, _, err := store.GetFrameRuntimeMetadata(request.FrameID)
	if err != nil {
		t.Fatal(err)
	}
	rawMetadata, err := json.Marshal(metadata.ContextData)
	if err != nil || strings.Contains(string(rawMetadata), request.ClaimTokenSHA256) {
		t.Fatalf("pending approval exposed claim fingerprint: %s err=%v", rawMetadata, err)
	}
	rawEvent, err := json.Marshal(event.Payload)
	if err != nil || strings.Contains(string(rawEvent), request.ClaimTokenSHA256) {
		t.Fatalf("approval event exposed claim fingerprint: %s err=%v", rawEvent, err)
	}
	resolution := kernelLocalExecResolutionForTest(request, true, "once")
	resolution.HandoffLeaseTTL = 4 * time.Minute
	decision, resolved, created, err := store.ResolveKernelLocalExecApproval(context.Background(), resolution)
	if err != nil || !created || !decision.Approved || decision.Scope != "once" || resolved.Type != KernelLocalExecApprovalResolvedEventType {
		t.Fatalf("decision=%#v event=%#v created=%t err=%v", decision, resolved, created, err)
	}
	if retryEvent, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err != nil || retryEvent.ID != "" {
		t.Fatalf("approved unconsumed retry event=%#v err=%v", retryEvent, err)
	}
	var grants int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM approval_policy_grants WHERE kind='local_exec'`).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("grants=%d err=%v", grants, err)
	}

	secondStore, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	start := make(chan struct{})
	errorsByWorker := make(chan error, 2)
	var wait sync.WaitGroup
	for _, candidate := range []*Store{store, secondStore} {
		wait.Add(1)
		go func(candidate *Store) {
			defer wait.Done()
			<-start
			_, consumeErr := candidate.ConsumeKernelLocalExecApproval(context.Background(), resolution)
			errorsByWorker <- consumeErr
		}(candidate)
	}
	close(start)
	wait.Wait()
	close(errorsByWorker)
	succeeded, consumed := 0, 0
	for consumeErr := range errorsByWorker {
		switch {
		case consumeErr == nil:
			succeeded++
		case errors.Is(consumeErr, ErrKernelLocalExecApprovalConsumed):
			consumed++
		default:
			t.Fatalf("consume error=%v", consumeErr)
		}
	}
	if succeeded != 1 || consumed != 1 {
		t.Fatalf("consume success=%d already=%d", succeeded, consumed)
	}
	var receipts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE event_type=?`, KernelLocalExecApprovalConsumedEventType).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipts=%d err=%v", receipts, err)
	}
	for _, realtimeID := range []string{
		"frame-event:" + event.ID,
		"frame-event:" + resolved.ID,
		"frame-event:kernel-local-exec-consumed:" + request.RequestID,
	} {
		outboxID := DeriveOutboxEventID(RealtimeOutboxTopic, realtimeID)
		var outboxRows int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM workspace_outbox WHERE event_id=?`, outboxID).Scan(&outboxRows); err != nil || outboxRows != 1 {
			t.Fatalf("outbox %q rows=%d err=%v", realtimeID, outboxRows, err)
		}
	}
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err == nil {
		t.Fatal("resolved approval was re-added")
	}
}

func TestKernelLocalExecApprovalRecoveryIgnoresDurableOperationProtocol(t *testing.T) {
	store, _, request := newKernelLocalExecApprovalFixture(t)
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	metadata, _, err := store.GetFrameRuntimeMetadata(request.FrameID)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := metadata.ContextData["_pending_input_requests"].([]any)
	if !ok || len(pending) != 1 {
		t.Fatalf("legacy pending inputs=%#v", metadata.ContextData["_pending_input_requests"])
	}
	// The web projection is allowed to retain only display fields. Recovery
	// must reconstruct runner and kernel authority from the immutable request
	// event rather than trusting this mutable metadata projection.
	original := pending[0].(map[string]any)
	pending[0] = map[string]any{
		"version": 1, "kind": "local_exec", "requestId": request.RequestID,
		"operation_id": original["operation_id"], "state_version": original["state_version"],
		"tool": request.Tool, "tool_name": request.Tool, "tool_call_id": request.ToolCallID,
		"environment": request.Environment, "input_sha256": request.InputSHA256,
		"stream_uid": request.StreamUID,
	}
	metadata.ContextData["_pending_input_requests"] = append(pending, map[string]any{
		"version": 2, "kind": "local_exec", "requestId": "operation-approval",
		"operation_id": "operation-id", "state_version": 1,
		"tool": "python", "environment": "synon-biomed-python",
		"input_sha256": strings.Repeat("a", 64), "stream_uid": request.StreamUID,
	})
	if _, err := store.SetFrameRuntimeMetadata(request.FrameID, metadata); err != nil {
		t.Fatal(err)
	}
	candidates, more, err := store.ListKernelLocalExecApprovalRecoveryCandidates(context.Background(), 10)
	if err != nil || more || len(candidates) != 1 || candidates[0].Resolution.RequestID != request.RequestID ||
		candidates[0].Resolution.RunnerID != request.RunnerID || candidates[0].Resolution.RunnerAttempt != request.RunnerAttempt ||
		candidates[0].Resolution.KernelID != request.KernelID || candidates[0].Resolution.ExpectedGeneration != request.ExpectedGeneration {
		t.Fatalf("recovery candidates=%#v more=%t err=%v", candidates, more, err)
	}
}

func TestKernelLocalExecApprovalConsumptionRenewsExactRunnerInSameCommit(t *testing.T) {
	store, _, request := newKernelLocalExecApprovalFixture(t)
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	resolution := kernelLocalExecResolutionForTest(request, true, "once")
	if _, _, _, err := store.ResolveKernelLocalExecApproval(context.Background(), resolution); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(5 * time.Second)
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		before, request.StreamUID, request.RunnerAttempt); err != nil {
		t.Fatal(err)
	}
	handoffFloor := time.Now().UTC().Add(3*time.Minute + 50*time.Second)
	resolution.HandoffLeaseTTL = 4 * time.Minute
	if _, err := store.ConsumeKernelLocalExecApproval(context.Background(), resolution); err != nil {
		t.Fatal(err)
	}
	var expiresAt time.Time
	if err := store.db.QueryRow(`SELECT expires_at FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`,
		request.StreamUID, request.RunnerAttempt).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	if expiresAt.Before(handoffFloor) {
		t.Fatalf("runner lease=%s want at least %s", expiresAt, handoffFloor)
	}
}

func TestKernelLocalExecApprovalStaleReconciliationRejectsLiveRunnerThenDeniesExpired(t *testing.T) {
	store, _, request := newKernelLocalExecApprovalFixture(t)
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	resolution := kernelLocalExecResolutionForTest(request, false, "once")
	resolution.RequireStaleRunner = true
	if _, _, _, err := store.ResolveKernelLocalExecApproval(context.Background(), resolution); !errors.Is(err, ErrKernelLocalExecApprovalRunnerLive) {
		t.Fatalf("live reconcile error=%v", err)
	}
	metadata, _, err := store.GetFrameRuntimeMetadata(request.FrameID)
	if err != nil || len(compatibilityPendingInputRequests(metadata.ContextData)) != 1 {
		t.Fatalf("live reconcile metadata=%#v err=%v", metadata, err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), request.StreamUID, request.RunnerAttempt); err != nil {
		t.Fatal(err)
	}
	decision, _, created, err := store.ResolveKernelLocalExecApproval(context.Background(), resolution)
	if err != nil || !created || decision.Approved || decision.Scope != "once" {
		t.Fatalf("stale decision=%#v created=%t err=%v", decision, created, err)
	}
	metadata, _, err = store.GetFrameRuntimeMetadata(request.FrameID)
	if err != nil || len(compatibilityPendingInputRequests(metadata.ContextData)) != 0 {
		t.Fatalf("stale reconcile metadata=%#v err=%v", metadata, err)
	}
}

func TestKernelLocalExecApprovalScopesPersistOnlyApprovedRememberedDecisions(t *testing.T) {
	for _, test := range []struct {
		name     string
		approved bool
		scope    string
		wantRows int
		wantTier string
	}{
		{name: "deny", approved: false, scope: "once"},
		{name: "once", approved: true, scope: "once"},
		{name: "conversation", approved: true, scope: "conversation", wantRows: 1, wantTier: "allow"},
		{name: "project", approved: true, scope: "project", wantRows: 1, wantTier: "allow"},
		{name: "always", approved: true, scope: "always", wantRows: 1, wantTier: "allow"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _, request := newKernelLocalExecApprovalFixture(t)
			if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			resolution := kernelLocalExecResolutionForTest(request, test.approved, test.scope)
			if _, _, _, err := store.ResolveKernelLocalExecApproval(context.Background(), resolution); err != nil {
				t.Fatal(err)
			}
			var rows int
			var originRoot string
			if err := store.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(origin_root_frame_id),'') FROM approval_policy_grants WHERE kind='local_exec'`).
				Scan(&rows, &originRoot); err != nil {
				t.Fatal(err)
			}
			if rows != test.wantRows || rows > 0 && originRoot != request.RootFrameID {
				t.Fatalf("rows=%d originRoot=%q", rows, originRoot)
			}
			policy, err := store.KernelLocalExecApprovalPolicy(context.Background(), request.OwnerUserID,
				request.ProjectID, request.RootFrameID, request.Tool, request.Environment)
			if err != nil || policy.Found != (test.wantRows == 1) || policy.Tier != test.wantTier {
				t.Fatalf("policy=%#v err=%v", policy, err)
			}
			if test.wantRows == 1 {
				otherEnvironment, err := store.KernelLocalExecApprovalPolicy(context.Background(), request.OwnerUserID,
					request.ProjectID, request.RootFrameID, request.Tool, "another-environment")
				if err != nil || !otherEnvironment.Found || otherEnvironment.Tier != test.wantTier {
					t.Fatalf("cross-environment policy=%#v err=%v", otherEnvironment, err)
				}
			}
		})
	}
}

func TestKernelLocalExecApprovalRejectsExpiredRunnerBeforeMutation(t *testing.T) {
	store, _, request := newKernelLocalExecApprovalFixture(t)
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), request.StreamUID, request.RunnerAttempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err == nil {
		t.Fatal("expired runner created an approval request")
	}
	metadata, _, err := store.GetFrameRuntimeMetadata(request.FrameID)
	if err != nil || len(compatibilityPendingInputRequests(metadata.ContextData)) != 0 {
		t.Fatalf("metadata=%#v err=%v", metadata, err)
	}
}

func TestKernelLocalExecApprovalDenialCleansPendingAfterRunnerExpires(t *testing.T) {
	store, _, request := newKernelLocalExecApprovalFixture(t)
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), request.StreamUID, request.RunnerAttempt); err != nil {
		t.Fatal(err)
	}
	decision, _, created, err := store.ResolveKernelLocalExecApproval(context.Background(),
		kernelLocalExecResolutionForTest(request, false, "once"))
	if err != nil || !created || decision.Approved {
		t.Fatalf("decision=%#v created=%t err=%v", decision, created, err)
	}
	metadata, _, err := store.GetFrameRuntimeMetadata(request.FrameID)
	if err != nil || len(compatibilityPendingInputRequests(metadata.ContextData)) != 0 {
		t.Fatalf("metadata=%#v err=%v", metadata, err)
	}
}

func TestKernelLocalExecApprovalRejectsForgedAuthorityWithoutPendingState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*KernelLocalExecApprovalRequestInput)
	}{
		{name: "owner", mutate: func(input *KernelLocalExecApprovalRequestInput) { input.OwnerUserID = "other" }},
		{name: "frame incarnation", mutate: func(input *KernelLocalExecApprovalRequestInput) { input.FrameIncarnationID = "stale" }},
		{name: "root incarnation", mutate: func(input *KernelLocalExecApprovalRequestInput) { input.RootFrameIncarnationID = "stale" }},
		{name: "stream", mutate: func(input *KernelLocalExecApprovalRequestInput) { input.StreamUID = "frame:other" }},
		{name: "runner", mutate: func(input *KernelLocalExecApprovalRequestInput) { input.RunnerID = "other" }},
		{name: "attempt", mutate: func(input *KernelLocalExecApprovalRequestInput) { input.RunnerAttempt++ }},
		{name: "claim token", mutate: func(input *KernelLocalExecApprovalRequestInput) { input.ClaimTokenSHA256 = strings.Repeat("0", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _, request := newKernelLocalExecApprovalFixture(t)
			test.mutate(&request)
			if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err == nil {
				t.Fatal("forged request was accepted")
			}
			metadata, _, err := store.GetFrameRuntimeMetadata("frame")
			if err != nil || len(compatibilityPendingInputRequests(metadata.ContextData)) != 0 {
				t.Fatalf("metadata=%#v err=%v", metadata, err)
			}
		})
	}
}

func TestKernelLocalExecApprovalConcurrentConflictingResolutionHasOneTerminalWinner(t *testing.T) {
	store, dbPath, request := newKernelLocalExecApprovalFixture(t)
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	secondStore, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	approve := kernelLocalExecResolutionForTest(request, true, "once")
	deny := kernelLocalExecResolutionForTest(request, false, "once")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, candidate := range []struct {
		store      *Store
		resolution KernelLocalExecApprovalResolutionInput
	}{{store: store, resolution: approve}, {store: secondStore, resolution: deny}} {
		wait.Add(1)
		go func(candidateStore *Store, resolution KernelLocalExecApprovalResolutionInput) {
			defer wait.Done()
			<-start
			_, _, _, resolveErr := candidateStore.ResolveKernelLocalExecApproval(context.Background(), resolution)
			results <- resolveErr
		}(candidate.store, candidate.resolution)
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for resolveErr := range results {
		if resolveErr == nil {
			succeeded++
		} else if strings.Contains(resolveErr.Error(), "conflicts with durable state") ||
			strings.Contains(resolveErr.Error(), "unavailable") {
			conflicted++
		} else {
			t.Fatalf("unexpected resolve error=%v", resolveErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("resolve success=%d conflicts=%d", succeeded, conflicted)
	}
	var terminalEvents int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE event_type=?`,
		KernelLocalExecApprovalResolvedEventType).Scan(&terminalEvents); err != nil || terminalEvents != 1 {
		t.Fatalf("terminal events=%d err=%v", terminalEvents, err)
	}
}

func newKernelLocalExecApprovalFixture(
	t *testing.T,
) (*Store, string, KernelLocalExecApprovalRequestInput) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task", FrameEventID: "task-event",
		MessageUUID: "task-message", Text: "Analyze data.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	claimDigest := sha256.Sum256([]byte(claimed.Claim.ClaimToken))
	inputDigest := sha256.Sum256([]byte(`{"code":"print(1)","environment":"science"}`))
	return store, dbPath, KernelLocalExecApprovalRequestInput{
		OwnerUserID: "owner", ProjectID: "project", FrameID: frame.ID,
		FrameIncarnationID: frame.IncarnationID, RootFrameID: frame.RootFrameID,
		RootFrameIncarnationID: frame.IncarnationID, RequestID: "approval-1", Tool: "python",
		ToolCallID: "call-1", Environment: "science", InputSHA256: hex.EncodeToString(inputDigest[:]),
		StreamUID: stream.UID, RunnerID: claimed.Claim.RunnerID, RunnerAttempt: claimed.Claim.Attempt,
		ClaimTokenSHA256: hex.EncodeToString(claimDigest[:]), KernelID: "kernel-1", ExpectedGeneration: 1,
		Code: "print(1)", WorkingDir: "C:/workspace",
	}
}

func kernelLocalExecResolutionForTest(
	request KernelLocalExecApprovalRequestInput,
	approved bool,
	scope string,
) KernelLocalExecApprovalResolutionInput {
	return KernelLocalExecApprovalResolutionInput{
		OwnerUserID: request.OwnerUserID, ProjectID: request.ProjectID, FrameID: request.FrameID,
		FrameIncarnationID: request.FrameIncarnationID, RootFrameID: request.RootFrameID,
		RootIncarnationID: request.RootFrameIncarnationID, RequestID: request.RequestID,
		Tool: request.Tool, Environment: request.Environment, InputSHA256: request.InputSHA256,
		StreamUID: request.StreamUID, RunnerID: request.RunnerID, RunnerAttempt: request.RunnerAttempt,
		ClaimTokenSHA256: request.ClaimTokenSHA256, KernelID: request.KernelID,
		ExpectedGeneration: request.ExpectedGeneration, Approved: approved, Scope: scope,
	}
}
