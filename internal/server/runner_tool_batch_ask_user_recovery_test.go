package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestResumePendingAskUserReconstructsInterruptedParkFromDurableBatch(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-ask-recovery", "frame-ask-recovery")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-ask-recovery", OwnerID: "local", ExternalID: "frame-ask-recovery",
		SessionID: "frame-ask-recovery", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-ask-recovery", RootFrameID: "frame-ask-recovery", FrameID: "frame-ask-recovery", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "ask-recovery-user",
		FrameEventID: "ask-recovery-user-frame", MessageUUID: "ask-recovery-message",
		MessageOrigin: "task_intent", Text: "Ask a second clarification before continuing.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("user event created=%t err=%v", created, err)
	}
	first, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "ask-recovery-runner-1",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}

	question := map[string]any{
		"question": "Which deliverable should be prepared?", "header": "Deliverable",
		"options": []any{
			askUserDecisionOption(
				"Full package", "Prepare compounds and experiments.", "Complete evidence", "Longer runtime",
				"Ready from the completed preparation checkpoint.", []any{"checkpoint:ask-recovery"},
				"Requires the prepared analysis environment.", "Compounds with experimental evidence.",
				"Recommended when a complete decision package is required.", true,
			),
			askUserDecisionOption(
				"Compounds only", "Prepare the editable compound set only.", "Faster delivery", "No docking evidence",
				"Ready from the completed preparation checkpoint.", []any{"checkpoint:ask-recovery"},
				"Requires only the prepared compound data.", "An editable compound set without docking evidence.",
				"Choose when downstream teams will run their own validation.", false,
			),
		},
		"multi_select": false,
	}
	rootPayload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{map[string]any{
			"id": "ask-recovery", "type": "function", "name": "ask_user",
			"arguments": question,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var batch workspace.ToolCallBatch
	var items []workspace.ToolCallBatchItem
	_, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "ask-recovery-batch", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: rootPayload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var hookErr error
			batch, items, _, hookErr = store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if hookErr != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, hookErr
			}
			return transcriptstore.RunnerCheckpointCommitReceipt{ToolBatch: &transcriptstore.RunnerCheckpointToolBatchReceipt{
				BatchID: batch.BatchID, CallCount: batch.CallCount,
			}}, nil
		},
	})
	if err != nil || !created || len(items) != 1 {
		t.Fatalf("batch=%#v items=%#v created=%t err=%v", batch, items, created, err)
	}
	batch, err = store.ClaimToolCallBatch(context.Background(), workspace.ClaimToolCallBatchInput{
		Claim: first.Claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}

	startPayload, _ := json.Marshal(map[string]any{
		"status": "running", "toolCallId": "ask-recovery", "toolName": "ask_user", "toolPhase": "start",
	})
	_, _, _, err = repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "ask-recovery-start", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: startPayload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			updatedBatch, updatedItem, hookErr := store.StartToolCallBatchItemTx(ctx, tx, workspace.StartToolCallBatchItemInput{
				Claim: first.Claim, BatchID: batch.BatchID, Ordinal: items[0].Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: items[0].StateVersion,
				StartedEvent: event,
			})
			batch, items[0] = updatedBatch, updatedItem
			return transcriptstore.RunnerCheckpointCommitReceipt{}, hookErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	waitPayload, _ := json.Marshal(map[string]any{
		"status": "waiting", "toolCallId": "ask-recovery", "toolName": "ask_user", "toolPhase": "waiting",
	})
	waitCheckpoint, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "ask-recovery-waiting", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: waitPayload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			updatedBatch, updatedItem, hookErr := store.WaitToolCallBatchItemTx(ctx, tx, workspace.WaitToolCallBatchItemInput{
				Claim: first.Claim, BatchID: batch.BatchID, Ordinal: items[0].Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: items[0].StateVersion,
				WaitingEvent: event,
			})
			batch, items[0] = updatedBatch, updatedItem
			return transcriptstore.RunnerCheckpointCommitReceipt{}, hookErr
		},
	})
	if err != nil || waitCheckpoint.Sequence <= 0 || items[0].State != workspace.ToolCallBatchItemStateWaiting {
		t.Fatalf("waiting checkpoint=%#v batch=%#v item=%#v err=%v", waitCheckpoint, batch, items[0], err)
	}
	srv := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: first.Claim}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(first.Claim.Attempt), ClaimToken: first.Claim.ClaimToken,
		Transcript: authority,
	}
	resumed, resumeErr := srv.resumePendingAgentToolCalls(context.Background(), SessionRunnerChatOptions{}, run)
	var pause *agentruntime.PauseError
	if resumed || !errors.As(resumeErr, &pause) {
		t.Fatalf("resumed=%t pause=%#v err=%v", resumed, pause, resumeErr)
	}
	if pause.Data["session_id"] != stream.SessionID || pause.Data["tool_id"] != "ask-recovery" ||
		pause.Data["tool_name"] != "ask_user" {
		t.Fatalf("recovered AskUser authority=%#v", pause.Data)
	}
	parked, pauseEvent, err := srv.parkTranscriptAskUser(context.Background(), authority, pause)
	if err != nil || len(parked.Events) != 3 || pauseEvent.EventID <= 0 {
		t.Fatalf("parked=%#v pause_event=%#v err=%v", parked, pauseEvent, err)
	}
	frame, found, err := store.GetFrame(stream.FrameID)
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	confirmations, err := srv.webConversationPendingConfirmations(stream.FrameID)
	if err != nil || len(confirmations) != 1 || confirmations[0]["id"] != "ask-recovery" {
		t.Fatalf("confirmations=%#v err=%v", confirmations, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, first.Claim.Attempt)
	if err != nil || state.Status != "running" || state.Phase != transcriptstore.RunnerPhaseWaitingUser {
		t.Fatalf("runner state=%#v err=%v", state, err)
	}
}
