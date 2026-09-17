package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityResolveLocalExecUsesFrameDecisionWithoutResume(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-local-exec", ProjectID: "project", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frame.ID, OwnerID: "local", ExternalID: frame.ID, SessionID: frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: frame.ProjectID,
		RootFrameID: frame.RootFrameID, FrameID: frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "local-exec-task",
		FrameEventID: "local-exec-task-event", MessageUUID: "local-exec-task-message",
		Text: "Run Python.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-local-exec", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	claimDigest := sha256.Sum256([]byte(claimed.Claim.ClaimToken))
	inputDigest := sha256.Sum256([]byte(`{"code":"print(1)","environment":"science"}`))
	requestID := "local-exec-approval"
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), workspace.KernelLocalExecApprovalRequestInput{
		OwnerUserID: "local", ProjectID: frame.ProjectID, FrameID: frame.ID,
		FrameIncarnationID: frame.IncarnationID, RootFrameID: frame.RootFrameID,
		RootFrameIncarnationID: frame.IncarnationID, RequestID: requestID, Tool: "python",
		ToolCallID: "call-local-exec", Environment: "science", InputSHA256: hex.EncodeToString(inputDigest[:]),
		StreamUID: stream.UID, RunnerID: claimed.Claim.RunnerID, RunnerAttempt: claimed.Claim.Attempt,
		ClaimTokenSHA256: hex.EncodeToString(claimDigest[:]), KernelID: "kernel-local-exec",
		ExpectedGeneration: 1, Code: "print(1)", WorkingDir: root,
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store})
	confirmations, err := srv.webConversationPendingConfirmations(frame.ID)
	if err != nil || len(confirmations) != 1 {
		t.Fatalf("confirmations=%#v err=%v", confirmations, err)
	}
	confirmation := confirmations[0]
	if confirmation["kind"] != "local_exec" || confirmation["tool"] != "python" ||
		confirmation["environment"] != "science" || confirmation["code"] != "print(1)" {
		t.Fatalf("confirmation=%#v", confirmation)
	}
	encodedConfirmation, err := json.Marshal(confirmation)
	if err != nil || strings.Contains(string(encodedConfirmation), claimed.Claim.ClaimToken) ||
		strings.Contains(string(encodedConfirmation), hex.EncodeToString(claimDigest[:])) {
		t.Fatalf("confirmation exposed runner authority: %s err=%v", encodedConfirmation, err)
	}
	withoutWaiter := compatJSONRequest(t, srv.Handler(), http.MethodPost,
		"/api/frames/"+frame.ID+"/resolve-input", "local", map[string]any{
			"responses": []any{map[string]any{"requestId": requestID, "action": "allow_once"}},
		}, http.StatusConflict)
	if withoutWaiter["detail"] != "Kernel local execution approval has no active execution waiter." {
		t.Fatalf("without waiter=%#v", withoutWaiter)
	}
	waiter := kernelLocalExecWaiterAuthority{
		OwnerUserID: "local", ProjectID: frame.ProjectID, FrameID: frame.ID,
		FrameIncarnation: frame.IncarnationID, RootFrameID: frame.RootFrameID,
		RootIncarnation: frame.IncarnationID, StreamUID: stream.UID,
		RunnerID: claimed.Claim.RunnerID, RunnerAttempt: claimed.Claim.Attempt,
		KernelID: "kernel-local-exec", KernelGeneration: 1,
	}
	if err := srv.registerKernelLocalExecWaiter(requestID, waiter); err != nil {
		t.Fatal(err)
	}
	defer srv.unregisterKernelLocalExecWaiter(requestID, waiter)
	metadata, found, err := store.GetFrameRuntimeMetadata(frame.ID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	localPending := compatibilityServerPendingInputs(metadata.ContextData)
	metadata.ContextData["_pending_input_requests"] = append([]any{
		map[string]any{"requestId": "ask-mixed", "kind": "ask"},
	}, compatibilityMapsToAnyForTest(localPending)...)
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, metadata); err != nil {
		t.Fatal(err)
	}
	mixed := compatJSONRequest(t, srv.Handler(), http.MethodPost,
		"/api/frames/"+frame.ID+"/resolve-input", "local", map[string]any{
			"responses": []any{
				map[string]any{"requestId": requestID, "action": "allow_once"},
				map[string]any{"requestId": "ask-mixed", "action": "cancel"},
			},
		}, http.StatusBadRequest)
	if mixed["detail"] != "Local execution approvals cannot be mixed with other input responses." {
		t.Fatalf("mixed response=%#v", mixed)
	}
	if _, found, err := store.GetKernelLocalExecApprovalDecision(context.Background(), "local",
		frame.ProjectID, frame.ID, frame.IncarnationID, requestID); err != nil || found {
		t.Fatalf("mixed decision found=%t err=%v", found, err)
	}
	metadata.ContextData["_pending_input_requests"] = compatibilityMapsToAnyForTest(localPending)
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, metadata); err != nil {
		t.Fatal(err)
	}
	response := compatJSONRequest(t, srv.Handler(), http.MethodPost,
		"/api/frames/"+frame.ID+"/resolve-input", "local", map[string]any{
			"responses": []any{map[string]any{"requestId": requestID, "action": "allow_once"}},
		}, http.StatusOK)
	if response["status"] != "processing" || numberValue(response["remaining"]) != 0 {
		t.Fatalf("response=%#v", response)
	}
	repeated := compatJSONRequest(t, srv.Handler(), http.MethodPost,
		"/api/frames/"+frame.ID+"/resolve-input", "local", map[string]any{
			"responses": []any{map[string]any{"requestId": requestID, "action": "allow_once"}},
		}, http.StatusOK)
	if repeated["status"] != "processing" {
		t.Fatalf("repeated response=%#v", repeated)
	}
	decision, found, err := store.GetKernelLocalExecApprovalDecision(context.Background(), "local",
		frame.ProjectID, frame.ID, frame.IncarnationID, requestID)
	if err != nil || !found || !decision.Approved || decision.Scope != "once" {
		t.Fatalf("decision=%#v found=%t err=%v", decision, found, err)
	}
	if _, found, err := store.GetCompatibilityFrameResumeDispatchByFrame(frame.ID); err != nil || found {
		t.Fatalf("resume dispatch found=%t err=%v", found, err)
	}
	current, found, err := store.GetCompatibilityFrame(frame.ID)
	if err != nil || !found || current.Status != "processing" {
		t.Fatalf("frame=%#v found=%t err=%v", current, found, err)
	}
	events, err := store.ListFrameEvents(frame.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	resolvedEvents := 0
	for _, event := range events {
		if event.Type == workspace.KernelLocalExecApprovalResolvedEventType {
			resolvedEvents++
		}
	}
	if resolvedEvents != 1 {
		t.Fatalf("resolved events=%d", resolvedEvents)
	}
}

func TestKernelLocalExecApprovalRestartKeepsLivePendingThenDeniesStale(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-local-exec-restart", ProjectID: "project", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frame.ID, OwnerID: "local", ExternalID: frame.ID, SessionID: frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: frame.ProjectID,
		RootFrameID: frame.RootFrameID, FrameID: frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "restart-task",
		FrameEventID: "restart-task-event", MessageUUID: "restart-task-message",
		Text: "Run Python.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-restart", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	claimDigest := sha256.Sum256([]byte(claimed.Claim.ClaimToken))
	inputDigest := sha256.Sum256([]byte(`{"code":"print(1)","environment":"science"}`))
	requestID := "local-exec-restart-approval"
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), workspace.KernelLocalExecApprovalRequestInput{
		OwnerUserID: "local", ProjectID: frame.ProjectID, FrameID: frame.ID,
		FrameIncarnationID: frame.IncarnationID, RootFrameID: frame.RootFrameID,
		RootFrameIncarnationID: frame.IncarnationID, RequestID: requestID, Tool: "python",
		ToolCallID: "call-restart", Environment: "science", InputSHA256: hex.EncodeToString(inputDigest[:]),
		StreamUID: stream.UID, RunnerID: claimed.Claim.RunnerID, RunnerAttempt: claimed.Claim.Attempt,
		ClaimTokenSHA256: hex.EncodeToString(claimDigest[:]), KernelID: "kernel-restart",
		ExpectedGeneration: 1, Code: "print(1)", WorkingDir: root,
	}); err != nil {
		t.Fatal(err)
	}

	restarted := New(Options{FileRoot: root, Workspace: store})
	candidates, moreCandidates, err := store.ListKernelLocalExecApprovalRecoveryCandidates(context.Background(), 10)
	if err != nil || moreCandidates || len(candidates) != 1 || !candidates[0].RunnerLive {
		t.Fatalf("live recovery candidates=%#v more=%t err=%v", candidates, moreCandidates, err)
	}
	confirmations, err := restarted.webConversationPendingConfirmations(frame.ID)
	if err != nil || len(confirmations) != 1 {
		t.Fatalf("live confirmations=%#v err=%v", confirmations, err)
	}
	if _, err := repo.CancelRunner(context.Background(), transcriptstore.CancelRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ExpectedAttempt: claimed.Claim.Attempt,
		ClientMessageID: "cancel-restart-runner", ReasonCode: "test_cancelled",
	}); err != nil {
		t.Fatal(err)
	}
	recoveryCtx, stopRecovery := context.WithCancel(context.Background())
	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- restarted.RunKernelLocalExecApprovalRecovery(recoveryCtx) }()
	t.Cleanup(func() {
		stopRecovery()
		if err := <-recoveryDone; err != nil {
			t.Errorf("kernel local execution recovery shutdown: %v", err)
		}
	})
	var decision workspace.KernelLocalExecApprovalDecision
	var found bool
	deadline := time.Now().Add(5 * time.Second)
	for !found {
		select {
		case recoveryErr := <-recoveryDone:
			t.Fatalf("kernel local execution recovery stopped before settlement: %v", recoveryErr)
		default:
		}
		decision, found, err = store.GetKernelLocalExecApprovalDecision(context.Background(), "local",
			frame.ProjectID, frame.ID, frame.IncarnationID, requestID)
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("stale approval was not settled without a confirmation read")
		}
		if !found {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if decision.Approved || decision.Scope != "once" {
		t.Fatalf("decision=%#v", decision)
	}
	confirmations, err = restarted.webConversationPendingConfirmations(frame.ID)
	if err != nil || len(confirmations) != 0 {
		t.Fatalf("stale confirmations=%#v err=%v", confirmations, err)
	}
	confirmations, err = restarted.webConversationPendingConfirmations(frame.ID)
	if err != nil || len(confirmations) != 0 {
		t.Fatalf("repeat confirmations=%#v err=%v", confirmations, err)
	}
}

func TestKernelLocalExecResolveErrorMappingDoesNotHideStorageFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "stale", err: workspace.ErrKernelLocalExecApprovalStale, wantStatus: http.StatusConflict},
		{name: "conflict", err: workspace.ErrKernelLocalExecApprovalConflict, wantStatus: http.StatusConflict},
		{name: "unavailable", err: workspace.ErrKernelLocalExecApprovalUnavailable, wantStatus: http.StatusConflict},
		{name: "storage", err: errors.New("database read failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			mapped := kernelLocalExecResolveError(test.err)
			var requestErr *compatibilityResolveInputError
			if test.wantStatus == 0 {
				if !errors.Is(mapped, test.err) || errors.As(mapped, &requestErr) {
					t.Fatalf("mapped storage error=%T %v", mapped, mapped)
				}
				return
			}
			if !errors.As(mapped, &requestErr) || requestErr.Status != test.wantStatus ||
				requestErr.Detail != "Kernel local execution approval authority changed." {
				t.Fatalf("mapped semantic error=%T %v", mapped, mapped)
			}
		})
	}
}

func compatibilityMapsToAnyForTest(items []map[string]any) []any {
	result := make([]any, len(items))
	for index := range items {
		result[index] = items[index]
	}
	return result
}

func TestCompatibilityResolveInputLegacyAskUserFailsBeforeEarlierBatchGrant(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project", UserID: "local", Name: "Project",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON",
		Status: "awaiting_user_response", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	pending := []any{
		map[string]any{
			"tool_id": "ask-1", "kind": "ask",
			"questions": []any{map[string]any{"question": "Which route?"}},
		},
		map[string]any{
			"requestId": "network-1", "kind": "network", "domain": "example.org",
		},
	}
	if _, err := store.SetFrameRuntimeMetadata("frame", workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{"_pending_input_requests": pending},
	}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []map[string]any{
		{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Start"}}},
		{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "ask-1", "name": "ask_user"},
			map[string]any{"type": "tool_use", "id": "network-1", "name": "request_network"},
		}},
		{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "ask-1", "content": `{"status":"awaiting_user_response"}`},
			map[string]any{"type": "tool_result", "tool_use_id": "network-1", "content": `{"status":"awaiting_user_response"}`},
		}},
	} {
		if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: "frame", Type: "user_message", Payload: message,
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(Options{FileRoot: root, Workspace: store})
	app := srv.Handler()
	beforeFrame, found, err := store.GetCompatibilityFrame("frame")
	if err != nil || !found {
		t.Fatalf("before frame=%#v found=%t err=%v", beforeFrame, found, err)
	}
	beforeMetadata, found, err := store.GetFrameRuntimeMetadata("frame")
	if err != nil || !found {
		t.Fatalf("before metadata=%#v found=%t err=%v", beforeMetadata, found, err)
	}
	beforeMessages, err := store.CompatibilityFrameMessages("frame", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, err := json.Marshal(beforeMessages.Messages)
	if err != nil {
		t.Fatal(err)
	}
	response := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/resolve-input", "local", map[string]any{
		"responses": []any{
			map[string]any{"requestId": "network-1", "action": "allow_always"},
			map[string]any{
				"tool_id": "ask-1", "action": "answer",
				"answers": map[string]any{"Which route?": "Direct"},
			},
		},
	}, http.StatusServiceUnavailable)
	if response["detail"] != "AskUser transcript authority is unavailable." {
		t.Fatalf("response=%#v", response)
	}
	afterFrame, found, err := store.GetCompatibilityFrame("frame")
	if err != nil || !found || afterFrame.Status != beforeFrame.Status {
		t.Fatalf("after frame=%#v before=%#v found=%t err=%v", afterFrame, beforeFrame, found, err)
	}
	afterMetadata, found, err := store.GetFrameRuntimeMetadata("frame")
	if err != nil || !found || len(compatibilityServerPendingInputs(afterMetadata.ContextData)) != len(compatibilityServerPendingInputs(beforeMetadata.ContextData)) {
		t.Fatalf("after metadata=%#v before=%#v found=%t err=%v", afterMetadata, beforeMetadata, found, err)
	}
	afterMessages, err := store.CompatibilityFrameMessages("frame", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, err := json.Marshal(afterMessages.Messages)
	if err != nil || string(afterJSON) != string(beforeJSON) {
		t.Fatalf("messages changed before=%s after=%s err=%v", beforeJSON, afterJSON, err)
	}
	if _, found, err := srv.settingsStore.Get("compatibility.inputGrants"); err != nil || found {
		t.Fatalf("input grant found=%t err=%v", found, err)
	}
	if domains, err := srv.loadAllowedDomains(); err != nil || len(domains) != 0 {
		t.Fatalf("allowed domains=%#v err=%v", domains, err)
	}
	if _, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("frame"); err != nil || found {
		t.Fatalf("dispatch found=%t err=%v", found, err)
	}
}

func TestCompatibilityResolveAskUserWithoutTranscriptFailsBeforeMutation(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-resolve-authority", "frame-resolve-authority")
	questions := []any{map[string]any{
		"question": "Continue?", "header": "Choice",
		"options": []any{
			map[string]any{"label": "Continue", "description": "Continue."},
			map[string]any{"label": "Stop", "description": "Stop."},
		},
	}}
	parked, err := store.ParkAskUser(workspace.ParkAskUserInput{
		FrameID: "frame-resolve-authority", ToolID: "ask-legacy", ToolName: "ask_user", Questions: questions,
	})
	if err != nil || len(parked.Events) != 3 {
		t.Fatalf("parked=%#v err=%v", parked, err)
	}
	srv := New(Options{Workspace: store, FileRoot: t.TempDir()})
	response := compatJSONRequest(t, srv.Handler(), http.MethodPost,
		"/api/frames/frame-resolve-authority/resolve-input", "local", map[string]any{
			"responses": []any{map[string]any{
				"tool_id": "ask-legacy", "action": "answer", "answers": map[string]any{"Continue?": "Continue"},
			}},
		}, http.StatusServiceUnavailable)
	if response["detail"] != "AskUser transcript authority is unavailable." {
		t.Fatalf("response=%#v", response)
	}
	frame, found, err := store.GetFrame("frame-resolve-authority")
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-resolve-authority")
	if err != nil || !found || len(compatibilityServerPendingInputs(metadata.ContextData)) != 1 {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	messages, err := store.CompatibilityFrameMessages("frame-resolve-authority", 0, 10)
	if err != nil || len(messages.Messages) != 2 {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func TestCompatibilityResolveLegacyAskUserWithTranscriptFailsWithoutTypedOrigin(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-resolve-origin", "frame-resolve-origin")
	questions := []any{map[string]any{
		"question": "Continue?", "header": "Choice",
		"options": []any{
			map[string]any{"label": "Continue", "description": "Continue."},
			map[string]any{"label": "Stop", "description": "Stop."},
		},
	}}
	parked, err := store.ParkAskUser(workspace.ParkAskUserInput{
		FrameID: "frame-resolve-origin", ToolID: "ask-legacy", ToolName: "ask_user", Questions: questions,
	})
	if err != nil || len(parked.Events) != 3 {
		t.Fatalf("parked=%#v err=%v", parked, err)
	}
	srv := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	response := compatJSONRequest(t, srv.Handler(), http.MethodPost,
		"/api/frames/frame-resolve-origin/resolve-input", "local", map[string]any{
			"responses": []any{map[string]any{
				"tool_id": "ask-legacy", "action": "answer", "answers": map[string]any{"Continue?": "Continue"},
			}},
		}, http.StatusConflict)
	if response["detail"] != "AskUser request authority conflicts with durable state." {
		t.Fatalf("response=%#v", response)
	}
	frame, found, err := store.GetFrame("frame-resolve-origin")
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-resolve-origin")
	if err != nil || !found || len(compatibilityServerPendingInputs(metadata.ContextData)) != 1 {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
}

func TestCompatibilityResolveInputTypedAskUserRetryUsesExactPromptAndConflict(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-ask-retry", "frame-ask-retry")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-ask-retry", OwnerID: "local", ExternalID: "frame-ask-retry", SessionID: "frame-ask-retry",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-ask-retry",
		RootFrameID: "frame-ask-retry", FrameID: "frame-ask-retry", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "ask-retry-task",
		FrameEventID: "ask-retry-task-frame", MessageUUID: "ask-retry-message", MessageOrigin: "task_intent",
		Text: "Analyze CRBN.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "ask-retry-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	questions := []any{map[string]any{
		"question": "Which structure?", "header": "Structure",
		"options": []any{
			map[string]any{"label": "5FQD", "description": "Use the human CRBN complex."},
			map[string]any{"label": "Predicted", "description": "Use a predicted structure."},
		},
		"multiSelect": false,
	}}
	if _, _, err := store.ParkAskUserWithTranscript(context.Background(), workspace.ParkAskUserInput{
		FrameID: stream.FrameID, ToolID: "ask-retry", ToolName: "AskUserQuestion", Questions: questions,
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim.Claim, ClientMessageID: "pause-ask-retry", Phase: transcriptstore.RunnerPhaseWaitingUser,
		Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_user_response"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(stream.FrameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	pending := compatibilityServerPendingInputs(metadata.ContextData)
	pending = append(pending, map[string]any{
		"tool_id": "network-retry", "requestId": "network-retry", "kind": "network", "domain": "example.org",
	})
	metadata.ContextData["_pending_input_requests"] = pending
	if _, err := store.SetFrameRuntimeMetadata(stream.FrameID, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: stream.FrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": "network-retry", "content": `{"status":"awaiting_user_response"}`,
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	app := srv.Handler()
	answer := func(value string, status int) map[string]any {
		return compatJSONRequest(t, app, http.MethodPost, "/api/frames/"+stream.FrameID+"/resolve-input", "local", map[string]any{
			"responses": []any{map[string]any{
				"tool_id": "ask-retry", "action": "answer", "answers": map[string]any{"Which structure?": value},
			}},
		}, status)
	}
	if partial := answer("5FQD", http.StatusOK); partial["status"] != "partial" {
		t.Fatalf("partial=%#v", partial)
	}
	currentMetadata, found, err := store.GetFrameRuntimeMetadata(stream.FrameID)
	if err != nil || !found {
		t.Fatalf("current metadata found=%t err=%v", found, err)
	}
	validOrigins := currentMetadata.ContextData["_ask_user_transcript_origins"]
	currentMetadata.ContextData["_ask_user_transcript_origins"] = "invalid"
	if _, err := store.SetFrameRuntimeMetadata(stream.FrameID, currentMetadata); err != nil {
		t.Fatal(err)
	}
	answer("5FQD", http.StatusConflict)
	currentMetadata.ContextData["_ask_user_transcript_origins"] = map[string]any{}
	if _, err := store.SetFrameRuntimeMetadata(stream.FrameID, currentMetadata); err != nil {
		t.Fatal(err)
	}
	answer("5FQD", http.StatusConflict)
	currentMetadata.ContextData["_ask_user_transcript_origins"] = validOrigins
	if _, err := store.SetFrameRuntimeMetadata(stream.FrameID, currentMetadata); err != nil {
		t.Fatal(err)
	}
	if repeated := answer("5FQD", http.StatusOK); repeated["status"] != "partial" {
		t.Fatalf("partial retry=%#v", repeated)
	}
	answer("Predicted", http.StatusConflict)
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	resultCount := 0
	terminalEventID := int64(0)
	var terminalPayload []byte
	for _, event := range projected {
		if event.Event.Type == transcriptstore.AskUserResultEventType {
			resultCount++
			decoded, decodeErr := transcriptstore.DecodeAskUserResultEventV1(event.ResolvedPayloadJSON)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if decoded.Result.Status != transcriptstore.AskUserStatusAwaitingResponse {
				terminalEventID = event.Event.EventID
				terminalPayload = append([]byte(nil), event.ResolvedPayloadJSON...)
			}
		}
	}
	if resultCount != 2 || terminalEventID <= 0 || len(terminalPayload) == 0 {
		t.Fatalf("AskUser result count=%d projected=%#v", resultCount, projected)
	}
	accepted := compatJSONRequest(t, app, http.MethodPost, "/api/frames/"+stream.FrameID+"/resolve-input", "local", map[string]any{
		"responses": []any{map[string]any{"tool_id": "network-retry", "action": "allow_once"}},
	}, http.StatusOK)
	if accepted["status"] != "accepted" {
		t.Fatalf("accepted=%#v", accepted)
	}
	var inputResponsePublication int64
	if err := db.QueryRow(`
		SELECT publication_seq FROM transcript_events
		WHERE stream_uid=? AND event_type='user_input_response' ORDER BY event_id DESC LIMIT 1`,
		stream.UID,
	).Scan(&inputResponsePublication); err != nil {
		t.Fatal(err)
	}
	if err := srv.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	var inputResponseDeliveryStatus string
	if err := db.QueryRow(`
		SELECT status FROM transcript_delivery_intents
		WHERE stream_uid=? AND publication_seq=? AND destination='ws'`,
		stream.UID, inputResponsePublication,
	).Scan(&inputResponseDeliveryStatus); err != nil {
		t.Fatal(err)
	}
	if inputResponseDeliveryStatus != "delivered" {
		t.Fatalf("typed AskUser model continuation delivery status=%q", inputResponseDeliveryStatus)
	}
	inputResponseRealtimeID := fmt.Sprintf("transcript-web:%s:%d:user", stream.UID, inputResponsePublication)
	if _, found, err := store.GetRealtimeEventByID(inputResponseRealtimeID); err != nil || found {
		t.Fatalf("typed AskUser model continuation realtime found=%t err=%v", found, err)
	}
	if repeated := answer("5FQD", http.StatusOK); repeated["status"] != "already_resolved" {
		t.Fatalf("terminal retry=%#v", repeated)
	}
	answer("Predicted", http.StatusConflict)
	originMap := validOrigins.(map[string]any)["ask-retry"].(map[string]any)
	toolUseFrameEventID := originMap["tool_use_frame_event_id"].(string)
	pendingFrameEventID := originMap["pending_frame_event_id"].(string)
	mutatedToolUse, err := json.Marshal(map[string]any{
		"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": "mutated-call", "name": "other_tool", "input": map[string]any{},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mutatedLegacy, err := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-retry", "content": "User cancelled this request",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frame_events SET payload=? WHERE id=?`, string(mutatedToolUse), toolUseFrameEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frame_events SET payload=? WHERE id=?`, string(mutatedLegacy), pendingFrameEventID); err != nil {
		t.Fatal(err)
	}
	history, found, err := srv.loadTranscriptWebHistory(context.Background(), "local", stream.FrameID)
	if err != nil || !found {
		t.Fatalf("history found=%t err=%v", found, err)
	}
	matching := make([]map[string]any, 0, 1)
	rightText := make([]string, 0, 1)
	for _, message := range history {
		content, _ := message["content"].(map[string]any)
		if message["type"] == "tool_call" && content["call_id"] == "ask-retry" {
			matching = append(matching, message)
		}
		if message["type"] == "text" && message["position"] == "right" {
			rightText = append(rightText, webString(content["content"]))
		}
	}
	if len(matching) != 1 {
		t.Fatalf("typed AskUser history=%#v", history)
	}
	content := matching[0]["content"].(map[string]any)
	output, _ := content["output"].(string)
	if matching[0]["id"] != "ask-retry" || matching[0]["status"] != "finish" || content["status"] != "completed" ||
		!strings.Contains(output, `"version":1`) || !strings.Contains(output, `"action":"answer"`) ||
		strings.Contains(output, "cancelled") {
		t.Fatalf("typed AskUser message=%#v", matching[0])
	}
	if len(rightText) != 1 || rightText[0] != "Analyze CRBN." {
		t.Fatalf("typed AskUser model continuation leaked into user history: %#v", rightText)
	}
	malformedTerminal := map[string]any{}
	if json.Unmarshal(terminalPayload, &malformedTerminal) != nil {
		t.Fatal("decode terminal fixture")
	}
	malformedTerminal["unexpected"] = true
	malformedTerminalJSON, _ := json.Marshal(malformedTerminal)
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json=? WHERE stream_uid=? AND event_id=?`,
		string(malformedTerminalJSON), stream.UID, terminalEventID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := srv.loadTranscriptWebHistory(context.Background(), "local", stream.FrameID); err == nil || !found {
		t.Fatalf("malformed typed AskUser history found=%t err=%v", found, err)
	}
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json=? WHERE stream_uid=? AND event_id=?`,
		string(terminalPayload), stream.UID, terminalEventID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := srv.loadTranscriptWebHistory(context.Background(), "local", stream.FrameID); err != nil || !found {
		t.Fatalf("restored typed AskUser history found=%t err=%v", found, err)
	}
}

func TestCompatibilityInputGrantStoresAndCompensatingRollback(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project", UserID: "local", Name: "Project",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "mcp-1", UserID: "local", Name: "Evidence",
		URL: "https://mcp.example.test", Transport: "streamable-http",
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store})
	request := httptest.NewRequest(http.MethodPost, "/api/frames/frame/resolve-input", nil)
	request.Header.Set("X-Synon-User-Id", "local")
	frame := workspace.CompatibilityFrame{Frame: workspace.Frame{
		ID: "frame", RootFrameID: "frame", ProjectID: "project", AgentName: "OPERON",
	}}
	hostPath := t.TempDir()
	tests := []struct {
		name             string
		item             map[string]any
		mode             string
		assertGranted    func()
		assertRolledBack func()
	}{
		{
			name: "network", item: map[string]any{"kind": "network", "domain": "example.org"},
			assertGranted: func() {
				domains, err := srv.loadAllowedDomains()
				if err != nil || len(domains) != 1 || domains[0] != "example.org" {
					t.Fatalf("network grants = %#v, err=%v", domains, err)
				}
			},
			assertRolledBack: func() {
				domains, err := srv.loadAllowedDomains()
				if err != nil || len(domains) != 0 {
					t.Fatalf("rolled back network grants = %#v, err=%v", domains, err)
				}
			},
		},
		{
			name: "host", item: map[string]any{"kind": "host", "path": hostPath}, mode: "rw",
			assertGranted: func() {
				grants, err := srv.loadHostGrants("local")
				if err != nil || len(grants) != 1 || grants[0].Path != hostPath || grants[0].Mode != "read_write" {
					t.Fatalf("host grants = %#v, err=%v", grants, err)
				}
			},
			assertRolledBack: func() {
				grants, err := srv.loadHostGrants("local")
				if err != nil || len(grants) != 0 {
					t.Fatalf("rolled back host grants = %#v, err=%v", grants, err)
				}
			},
		},
		{
			name: "mcp", item: map[string]any{
				"kind": "mcp_tool", "server_id": "mcp-1", "tool_name": "search",
			},
			assertGranted: func() {
				grants, err := store.ListMCPToolGrants("mcp-1", "local")
				if err != nil || len(grants) != 1 || !grants[0].Enabled {
					t.Fatalf("mcp grants = %#v, err=%v", grants, err)
				}
			},
			assertRolledBack: func() {
				grants, err := store.ListMCPToolGrants("mcp-1", "local")
				if err != nil || len(grants) != 1 || grants[0].Enabled {
					t.Fatalf("rolled back mcp grants = %#v, err=%v", grants, err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rollback, err := srv.persistCompatibilityInputGrant(request, frame, test.item, "always", test.mode)
			if err != nil {
				t.Fatal(err)
			}
			test.assertGranted()
			rollback()
			test.assertRolledBack()
		})
	}
}

func TestCompatibilityInputAllowWithoutScopeIsOnceAndDoesNotPersistGrant(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	request := httptest.NewRequest(http.MethodPost, "/api/frames/frame/resolve-input", nil)
	request.Header.Set("X-Synon-User-Id", "local")
	approved := true
	content, _, denied, result, rollback, err := srv.resolveCompatibilityInputItem(
		request,
		workspace.CompatibilityFrame{Frame: workspace.Frame{
			ID: "frame", RootFrameID: "frame", ProjectID: "project", AgentName: "OPERON",
		}},
		map[string]any{"requestId": "exec-1", "kind": "local_exec", "tool_name": "python"},
		compatibilityInputResponse{Action: "allow", Approved: &approved},
		false,
	)
	if err != nil || denied || result != nil {
		t.Fatalf("content=%q denied=%t result=%#v err=%v", content, denied, result, err)
	}
	if rollback != nil {
		t.Fatal("one-time approval returned a persistent-grant rollback")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["approved"] != true || payload["scope"] != "once" {
		t.Fatalf("approval payload=%#v", payload)
	}
	if _, found, err := srv.settingsStore.Get("compatibility.inputGrants"); err != nil || found {
		t.Fatalf("one-time input grant found=%t err=%v", found, err)
	}
}

func TestCompatibilityAskInputActionsAndValidation(t *testing.T) {
	item := map[string]any{
		"question": "Proceed?",
		"options": []any{map[string]any{
			"label": "yes", "metadata": map[string]any{"implementation": "Engine A"},
		}},
	}
	answeredContinuation, err := compatibilityAnsweredAskUserContinuation(
		item, map[string]string{"Proceed?": "yes"}, map[string]string{"Proceed?": "Engine A"},
	)
	if err != nil {
		t.Fatal(err)
	}
	delegatedContinuation, err := transcriptstore.EncodeDelegatedAskUserModelContinuation(
		map[string]string{"Proceed?": "Engine A"},
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		response compatibilityInputResponse
		want     string
		wantErr  bool
	}{
		{name: "answer", response: compatibilityInputResponse{Action: "answer", Answers: map[string]string{"Proceed?": "yes"}}, want: answeredContinuation},
		{name: "decide", response: compatibilityInputResponse{Action: "decide_for_me"}, want: delegatedContinuation},
		{name: "discuss", response: compatibilityInputResponse{Action: "discuss", Message: "Need evidence"}, want: "The user wants to discuss these questions further. Their message: Need evidence. Respond to their input, then use ask_user again if you still need answers."},
		{name: "cancel", response: compatibilityInputResponse{Action: "cancel"}, want: "User cancelled the question. Continue without an answer — use your best judgment or skip this step."},
		{name: "missing", response: compatibilityInputResponse{Action: "answer", Answers: map[string]string{}}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := compatibilityAskInputContent(item, test.response)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("content = %q, err=%v", got, err)
			}
			structured, continuation, result, structuredErr := compatibilityAskInputResult(item, test.response)
			if test.wantErr {
				if structuredErr == nil {
					t.Fatal("invalid AskUser response produced a structured result")
				}
				return
			}
			if structuredErr != nil || continuation != test.want || result == nil || !strings.Contains(structured, `"version":1`) ||
				!strings.Contains(structured, `"status":"`+string(result.Status)+`"`) {
				t.Fatalf("structured=%q continuation=%q result=%#v err=%v", structured, continuation, result, structuredErr)
			}
		})
	}
}

func TestCompatibilityAskInputPersistsSelectedEvidenceResolver(t *testing.T) {
	question := "How should the binding pocket be resolved?"
	item := map[string]any{
		"question": question,
		"options": []any{map[string]any{
			"label": "Use P2Rank detection",
			"metadata": map[string]any{"evidence_resolver": map[string]any{
				"evidence_group": "binding-site-center",
				"skill":          "p2rank-pocket-detection", "implementation": "P2Rank",
			}},
		}},
	}
	_, continuation, _, err := compatibilityAskInputResult(item, compatibilityInputResponse{
		Action: "answer", Answers: map[string]string{question: "Use P2Rank detection"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(continuation, `"implementations"`) || !strings.Contains(continuation, `"evidence_resolvers"`) ||
		!strings.Contains(continuation, `"p2rank-pocket-detection"`) {
		t.Fatalf("continuation=%s", continuation)
	}
}

func TestCompatibilityDecideForMeResolvesTheRecommendedExactImplementation(t *testing.T) {
	item := map[string]any{"questions": []any{map[string]any{
		"question": "Which engine?",
		"options": []any{
			map[string]any{"label": "Engine A", "metadata": map[string]any{
				"implementation": "Engine A", "reported_recommended": true,
			}},
			map[string]any{"label": "Engine B", "metadata": map[string]any{
				"implementation": "Engine B", "recommended": true,
			}},
		},
	}}}
	structured, continuation, result, err := compatibilityAskInputResult(
		item, compatibilityInputResponse{Action: "decide_for_me"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(continuation), &payload); err != nil {
		t.Fatal(err)
	}
	implementations, _ := payload["implementations"].(map[string]any)
	if payload["status"] != "delegated" || payload["provenance"] != "model-delegated-choice" ||
		payload["answers"] != nil || payload["evidence"] != nil || implementations["Which engine?"] != "Engine B" {
		t.Fatalf("delegated continuation=%#v", payload)
	}
	if result == nil || result.Action != transcriptstore.AskUserActionDecideForMe ||
		result.Status != transcriptstore.AskUserStatusDeferred || len(result.Answers) != 0 ||
		!strings.Contains(structured, `"action":"decide_for_me"`) {
		t.Fatalf("structured=%q result=%#v", structured, result)
	}

	fallback, err := compatibilityAskInputContent(
		map[string]any{"question": "Describe the desired route"},
		compatibilityInputResponse{Action: "decide_for_me"},
	)
	if err != nil || !strings.HasPrefix(fallback, "User delegated this choice.") {
		t.Fatalf("open delegation=%q err=%v", fallback, err)
	}
}

func TestCompatibilityAskInputBindsOnlyTheSelectedVisibleOptionAsEvidence(t *testing.T) {
	question := "Which center?"
	item := map[string]any{"questions": []any{map[string]any{
		"question": question,
		"options": []any{
			map[string]any{
				"label":       "Confirmed center",
				"description": "Use binding-site center (1.0, 2.0, 3.0).",
				"metadata":    map[string]any{"private_value": "99, 98, 97"},
			},
			map[string]any{
				"label":       "Manual center",
				"description": "Ask me to provide the center.",
			},
		},
	}}}
	for answer, markers := range map[string][2]string{
		"Confirmed center": {"1.0, 2.0, 3.0", "99, 98, 97"},
		"Manual center":    {"Ask me to provide the center.", "1.0, 2.0, 3.0"},
	} {
		continuation, err := compatibilityAnsweredAskUserContinuation(
			item, map[string]string{question: answer}, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Evidence          map[string]string `json:"evidence"`
			ParameterEvidence map[string]string `json:"parameter_evidence"`
		}
		if err := json.Unmarshal([]byte(continuation), &payload); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(payload.Evidence[question], markers[0]) || strings.Contains(payload.Evidence[question], markers[1]) {
			t.Fatalf("answer=%q evidence=%q", answer, payload.Evidence[question])
		}
		if len(payload.ParameterEvidence) != 0 {
			t.Fatalf("closed option became controlled parameter evidence: %#v", payload.ParameterEvidence)
		}
	}

	customAnswer := "Use center 4.0 / 5.0 / 6.0"
	continuation, err := compatibilityAnsweredAskUserContinuation(
		item, map[string]string{question: customAnswer}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var customPayload struct {
		Evidence          map[string]string `json:"evidence"`
		ParameterEvidence map[string]string `json:"parameter_evidence"`
	}
	if err := json.Unmarshal([]byte(continuation), &customPayload); err != nil {
		t.Fatal(err)
	}
	if customPayload.Evidence[question] != customAnswer || customPayload.ParameterEvidence[question] != customAnswer {
		t.Fatalf("direct answer evidence=%#v parameter_evidence=%#v", customPayload.Evidence, customPayload.ParameterEvidence)
	}
}
