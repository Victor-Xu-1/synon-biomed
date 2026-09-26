package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

type noProgressReceiptFixture struct {
	server  *Server
	repo    *transcriptstore.Repository
	run     *sessionRunnerChatRun
	options SessionRunnerChatOptions
	calls   []agentruntime.ToolCall
}

func newNoProgressReceiptFixture(t *testing.T) noProgressReceiptFixture {
	t.Helper()
	ctx := context.Background()
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "receipt-project", "receipt-frame")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(ctx) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{FrameID: "receipt-frame", MessageUUID: "receipt-message", ClientMessageID: "receipt-client", Text: "Review the existing sources."}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(ctx, "local", "receipt-frame")
	if err != nil || !found {
		t.Fatalf("stream found=%t error=%v", found, err)
	}
	claimed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "receipt-runner", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v error=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken, Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}}
	fixture := noProgressReceiptFixture{server: server, repo: repo, run: run, options: SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID, SessionID: stream.SessionID}}
	for index := 0; index < 10; index++ {
		fixture.calls = append(fixture.calls, receiptProbeCall(fmt.Sprintf("source-%d", index)))
	}
	engine := agentruntime.Engine{Model: &receiptBatchModel{calls: fixture.calls}, Tools: receiptReuseGateway{}, OnEventError: func(event agentruntime.Event) error {
		if event.Type == agentruntime.EventModelResponse {
			return server.checkpointChatModelToolCalls(fixture.options, run, event.ToolCalls)
		}
		return server.checkpointSessionRunnerToolEvent(ctx, fixture.options, run, event)
	}}
	_, err = engine.Run(ctx, agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Review existing receipts."}}, Tools: []agentruntime.ToolSchema{{Name: "web_fetch"}}, MaxConsecutiveIdenticalToolRounds: 1})
	var interruption *agentruntime.ToolRoundNoProgressError
	if !errors.As(err, &interruption) || len(interruption.Calls) != 8 {
		t.Fatalf("expected bounded interruption: %v", err)
	}
	fixture.boundary(t, "initial-boundary", interruption.Calls)
	return fixture
}

type receiptBatchModel struct {
	calls    []agentruntime.ToolCall
	requests int
}

func (model *receiptBatchModel) Complete(context.Context, agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	model.requests++
	if model.requests == 1 {
		return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "Reviewing the recorded results.", ToolCalls: model.calls}}, nil
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "No repeated execution is needed."}}, nil
}

type receiptReuseGateway struct{}

func (receiptReuseGateway) Execute(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	return agentruntime.ToolResult{Value: map[string]any{"ok": true, "reused": true, "result": map[string]any{"body": "existing evidence"}}}, nil
}

func receiptProbeCall(id string) agentruntime.ToolCall {
	return agentruntime.ToolCall{ID: id, Name: "web_fetch", Arguments: json.RawMessage(fmt.Sprintf(`{"url":"https://example.org/%s"}`, id))}
}

func (fixture noProgressReceiptFixture) complete(t *testing.T, call agentruntime.ToolCall, result string) {
	t.Helper()
	if err := fixture.server.checkpointChatModelToolCalls(fixture.options, fixture.run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []agentruntime.EventType{agentruntime.EventToolStarted, agentruntime.EventToolCompleted} {
		if err := fixture.server.checkpointSessionRunnerToolEvent(context.Background(), fixture.options, fixture.run, agentruntime.Event{Type: kind, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments), Result: result}); err != nil {
			t.Fatal(err)
		}
	}
}

func (fixture noProgressReceiptFixture) boundary(t *testing.T, key string, calls []agentruntime.ToolCall) {
	t.Helper()
	state := fixture.run.recordNoProgress(calls)
	appendRunnerToolCheckpoint(t, fixture.repo, fixture.run.Transcript.Claim, key, map[string]any{"status": "interrupted", "reason_code": sessionRunnerToolRoundNoProgressReasonCode, "resume_detail": state.resumeDetail("choose a materially different route")})
}

func (fixture noProgressReceiptFixture) gateway() serverAgentRuntimeToolGateway {
	return serverAgentRuntimeToolGateway{server: fixture.server, taskRun: fixture.run, allowedTools: []string{"web_fetch"}}
}

func TestNoProgressReceiptMembershipSurvivesClippingAndBoundedReplay(t *testing.T) {
	fixture := newNoProgressReceiptFixture(t)
	for index := 0; index < 1240; index++ {
		appendRunnerToolCheckpoint(t, fixture.repo, fixture.run.Transcript.Claim, fmt.Sprintf("receipt-audit-%d", index), map[string]any{"status": "running", "audit": index})
	}
	entries, err := fixture.server.loadTranscriptRunnerReplay(context.Background(), fixture.run.Transcript, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	state, found := sessionRunnerNoProgressRecoveryFromReplay(entries)
	if !found || len(state.ClosedActions) != 8 {
		t.Fatalf("bounded display contract changed: found=%t state=%#v", found, state)
	}
	restarted := sessionRunnerChatRun{
		SessionID: fixture.run.SessionID, Attempt: fixture.run.Attempt,
		ClaimToken: fixture.run.ClaimToken, Transcript: fixture.run.Transcript,
		CorrectionReason: fixture.run.CorrectionReason, CorrectionDetail: fixture.run.CorrectionDetail,
	}
	restarted.restoreNoProgressRecovery(state)
	fixture.run = &restarted
	proposed := append([]agentruntime.ToolCall(nil), fixture.calls...)
	proposed = append(proposed, receiptProbeCall("new-source"))
	proposed = append(proposed, agentruntime.ToolCall{ID: "retitled", Name: "web_fetch", Arguments: json.RawMessage(`{"human_description":"Retitled source","url":"https://example.org/source-0"}`)})
	started := time.Now()
	diagnostics, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), proposed)
	t.Logf("batch_calls=%d closed=%d lookup_elapsed=%s", len(proposed), len(diagnostics), time.Since(started))
	if err != nil {
		t.Fatal(err)
	}
	for index := range fixture.calls {
		if !strings.Contains(diagnostics[index], "durable_no_progress_route_closed") {
			t.Errorf("unchanged execution %d reopened: %v", index, diagnostics[index])
		}
	}
	if diagnostics[10] != "" {
		t.Fatalf("materially changed call closed: %s", diagnostics[10])
	}
	if diagnostics[11] == "" {
		t.Fatal("cosmetic argument change reopened the oldest receipt")
	}
}

func TestNoProgressReceiptMembershipBlocksEngineRedispatch(t *testing.T) {
	fixture := newNoProgressReceiptFixture(t)
	model := &receiptBatchModel{calls: fixture.calls}
	starts := 0
	engine := agentruntime.Engine{Model: model, Tools: fixture.gateway(), OnEvent: func(event agentruntime.Event) {
		if event.Type == agentruntime.EventToolStarted {
			starts++
		}
	}}
	_, err := engine.Run(context.Background(), agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Continue from the existing results."}}, Tools: []agentruntime.ToolSchema{{Name: "web_fetch"}}})
	if err != nil || starts != 0 || model.requests != 2 {
		t.Fatalf("error=%v tool_starts=%d requests=%d", err, starts, model.requests)
	}
}

func TestReusableNoProgressReceiptRequiresExecutionProvenance(t *testing.T) {
	for _, test := range []struct {
		name     string
		result   map[string]any
		rejected bool
		want     bool
	}{
		{"reused", map[string]any{"ok": true, "reused": true}, false, true},
		{"unchanged", map[string]any{"ok": true, "effect": map[string]any{"state": "unchanged"}}, false, true},
		{"failed", map[string]any{"ok": false, "reused": true}, false, false},
		{"unavailable", map[string]any{"sourceUnavailable": true, "reused": true}, false, false},
		{"partial", map[string]any{"partial": true, "reused": true}, false, false},
		{"rejected proposal", map[string]any{"ok": true, "reused": true}, true, false},
		{"body is not metadata", map[string]any{"ok": true, "body": map[string]any{"reused": true}}, false, false},
		{"empty result", nil, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := eventjournal.Message{"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolName": "web_fetch", "toolInput": map[string]any{"url": "https://example.org/source"}, "toolResult": test.result, "rejectedBeforeExecution": test.rejected}
			if got := runnerCheckpointHasReusableNoProgressReceipt(message); got != test.want {
				t.Fatalf("reusable=%t want=%t", got, test.want)
			}
			delete(message, "toolInput")
			if runnerCheckpointHasReusableNoProgressReceipt(message) {
				t.Fatal("receipt without exact execution input was accepted")
			}
		})
	}
}

func TestNoProgressReceiptMembershipScopesAndOutcomes(t *testing.T) {
	for _, kind := range []string{"unavailable", "partial", "new-obligation", "changed-input", "new-task", "branch-edit"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newNoProgressReceiptFixture(t)
			extra := receiptProbeCall("extra")
			wantOld := true
			wantExtra := false
			switch kind {
			case "unavailable":
				fixture.complete(t, extra, `{"ok":true,"sourceUnavailable":true,"complete":true}`)
			case "partial":
				fixture.complete(t, extra, `{"partial":true,"content":"new usable evidence"}`)
				wantOld = false
			case "new-obligation":
				fixture.run.CorrectionReason = "artifact_reference_correction_required"
				fixture.run.CorrectionDetail = "a different required deliverable is missing"
				appendRunnerToolCheckpoint(t, fixture.repo, fixture.run.Transcript.Claim, "new-obligation", map[string]any{"status": "interrupted", "reason_code": fixture.run.CorrectionReason, "resume_detail": fixture.run.CorrectionDetail})
				fixture.complete(t, extra, `{"ok":true,"reused":true}`)
				fixture.boundary(t, "new-obligation-boundary", []agentruntime.ToolCall{extra})
				wantOld = false
				wantExtra = true
			case "changed-input":
				authority := *fixture.run.Transcript
				authority.Claim.ClaimedInputRevision++
				fixture.run.Transcript = &authority
				wantOld = false
			case "new-task":
				if _, _, err := fixture.server.submitFrameMessage(fixture.server.workspaceStore, frameMessageSubmission{FrameID: fixture.run.SessionID, MessageUUID: "next-message", ClientMessageID: "next-client", Text: "Start a different source review."}); err != nil {
					t.Fatal(err)
				}
				wantOld = false
			case "branch-edit":
				authority := fixture.run.Transcript
				branch, err := fixture.repo.GetBranchState(context.Background(), authority.Stream.UID, authority.Stream.OwnerID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.repo.ForkFrameUserMessageBranch(context.Background(), transcriptstore.ForkFrameUserMessageBranchInput{
					StreamUID: authority.Stream.UID, OwnerID: authority.Stream.OwnerID, SourceBranchID: branch.ActiveBranchID, ExpectedActiveBranchID: branch.ActiveBranchID, ExpectedGeneration: branch.Generation,
					ClientMutationID: "replace-task", SourceClientMessageID: "receipt-client", SourceMessageIndex: 0, ReplacementText: "Review the corrected sources.", Destinations: []string{"ws"},
				}); err != nil {
					t.Fatal(err)
				}
				wantOld = false
			}
			diagnostics, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), []agentruntime.ToolCall{fixture.calls[0], extra})
			if err != nil {
				t.Fatal(err)
			}
			if (diagnostics[0] != "") != wantOld || (diagnostics[1] != "") != wantExtra {
				t.Fatalf("diagnostics=%v wantOld=%t wantExtra=%t", diagnostics, wantOld, wantExtra)
			}
		})
	}
}

func TestNoProgressReceiptMembershipCannotFallBackAcrossAuthorityFailure(t *testing.T) {
	fixture := newNoProgressReceiptFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.gateway().ToolCallPreflightDiagnostics(ctx, fixture.calls); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	authority := *fixture.run.Transcript
	authority.Stream.OwnerID = "another-owner"
	fixture.run.Transcript = &authority
	if _, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), fixture.calls); err == nil {
		t.Fatal("foreign authority fell back to cached preview")
	}
}

func TestNoProgressReceiptMembershipRetainsRejectedRoutesAcrossBoundaries(t *testing.T) {
	fixture := newNoProgressReceiptFixture(t)
	rejected := make([]agentruntime.ToolCall, 10)
	for index := range rejected {
		rejected[index] = receiptProbeCall(fmt.Sprintf("rejected-%d", index))
		rejected[index].RejectedBeforeExecution = true
	}
	if err := fixture.server.checkpointChatModelToolCalls(fixture.options, fixture.run, rejected); err != nil {
		t.Fatal(err)
	}
	for _, call := range rejected {
		if err := fixture.server.checkpointSessionRunnerToolEvent(context.Background(), fixture.options, fixture.run, agentruntime.Event{
			Type: agentruntime.EventToolCompleted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments), RejectedBeforeExecution: true,
			Result: `{"ok":false,"executed":false,"code":"runtime_preflight_required","message":"The route requires corrected inputs."}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	fixture.boundary(t, "rejection-boundary", rejected[2:])
	proposals := []agentruntime.ToolCall{fixture.calls[0], fixture.calls[9], rejected[0], rejected[9], receiptProbeCall("different")}
	diagnostics, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), proposals)
	if err != nil || len(diagnostics) != 4 {
		t.Fatalf("diagnostics=%v error=%v", diagnostics, err)
	}
	for index := 0; index < 4; index++ {
		if diagnostics[index] == "" || strings.Contains(diagnostics[index], "execution already completed") {
			t.Fatalf("lost route or false success at %d: %s", index, diagnostics[index])
		}
	}
	if diagnostics[4] != "" {
		t.Fatal("different execution was closed")
	}
}

func TestRejectedRouteReceiptRequiresTerminalAdmissionProvenance(t *testing.T) {
	base := eventjournal.Message{"type": "runner_checkpoint", "status": "failed", "toolPhase": prestartToolFailurePhase, "toolName": "web_fetch", "toolInput": map[string]any{}, "rejectedBeforeExecution": true, "toolResult": map[string]any{"ok": false, "executed": false, "code": "runtime_preflight_required"}}
	if !runnerCheckpointHasRejectedRouteReceipt(base) {
		t.Fatal("typed admission receipt was lost")
	}
	for _, code := range []string{"correction_route_closed", "durable_no_progress_route_closed"} {
		copy := eventjournal.Message{}
		for name, value := range base {
			copy[name] = value
		}
		copy["toolResult"] = map[string]any{"ok": false, "executed": false, "code": code}
		if runnerCheckpointHasRejectedRouteReceipt(copy) {
			t.Fatalf("derived closure %s became a new admission rejection", code)
		}
	}
	for _, key := range []string{"type", "status", "toolPhase", "toolName", "toolInput", "rejectedBeforeExecution", "toolResult"} {
		copy := eventjournal.Message{}
		for name, value := range base {
			copy[name] = value
		}
		delete(copy, key)
		if runnerCheckpointHasRejectedRouteReceipt(copy) {
			t.Fatalf("missing %s accepted as terminal admission evidence", key)
		}
	}
}
