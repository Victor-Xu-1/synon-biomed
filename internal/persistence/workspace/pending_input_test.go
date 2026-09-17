package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestParkAskUserPersistsRestartSafePendingState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	questions := []any{map[string]any{
		"question": "Which structure should be used?", "header": "Structure",
		"options": []any{map[string]any{"label": "5FQD", "description": "Use the human CRBN complex."}},
	}}
	result, err := store.ParkAskUser(ParkAskUserInput{FrameID: "frame", ToolID: "ask-1", ToolName: "AskUserQuestion", Questions: questions})
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyPending || len(result.Events) != 3 {
		t.Fatalf("result = %#v", result)
	}
	frame, found, err := store.GetCompatibilityFrame("frame")
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("frame=%#v found=%v err=%v", frame, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame")
	if err != nil || !found {
		t.Fatalf("metadata found=%v err=%v", found, err)
	}
	pending := compatibilityPendingInputRequests(metadata.ContextData)
	if len(pending) != 1 || compatibilityPendingInputID(pending[0]) != "ask-1" {
		t.Fatalf("pending = %#v", pending)
	}
	messages, err := store.CompatibilityFrameMessages("frame", 0, 10)
	if err != nil || len(messages.Messages) != 2 {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	repeated, err := store.ParkAskUser(ParkAskUserInput{FrameID: "frame", ToolID: "ask-1", ToolName: "AskUserQuestion", Questions: questions})
	if err != nil || !repeated.AlreadyPending || len(repeated.Events) != 0 {
		t.Fatalf("repeated=%#v err=%v", repeated, err)
	}
}

func TestParkAskUserCanonicalizesKnownAliasesAndRejectsPaddedNames(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-alias", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	questions := []any{map[string]any{
		"question": "Continue?", "header": "Choice",
		"options": []any{map[string]any{"label": "Yes", "description": "Continue."}},
	}}
	for index, alias := range []string{"AskUserQuestion", "ask_user_question", "ask_user"} {
		frameID := fmt.Sprintf("frame-alias-%d", index)
		if _, err := store.CreateFrame(CreateFrameInput{
			ID: frameID, ProjectID: "project-alias", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
		parked, err := store.ParkAskUser(ParkAskUserInput{
			FrameID: frameID, ToolID: fmt.Sprintf("ask-%d", index), ToolName: alias, Questions: questions,
		})
		if err != nil || len(parked.Events) != 3 {
			t.Fatalf("alias=%q parked=%#v err=%v", alias, parked, err)
		}
		metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
		if err != nil || !found {
			t.Fatalf("alias=%q metadata=%#v found=%t err=%v", alias, metadata, found, err)
		}
		pending := compatibilityPendingInputRequests(metadata.ContextData)
		if len(pending) != 1 || compatibilityStringValue(pending[0]["tool_name"]) != "ask_user" {
			t.Fatalf("alias=%q pending=%#v", alias, pending)
		}
	}
	if _, err := store.ParkAskUser(ParkAskUserInput{
		FrameID: "frame-alias-0", ToolID: "padded", ToolName: " ask_user", Questions: questions,
	}); err == nil {
		t.Fatal("padded AskUser tool name was accepted")
	}
}

func TestParkAskUserWithTranscriptAtomicallyBindsActiveBranchAndPausesClaim(t *testing.T) {
	store, repo, stream, claim := newParkAskUserTranscriptFixture(t)
	newerStream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame:epoch-2", OwnerID: stream.OwnerID, ExternalID: stream.ExternalID,
		SessionID: stream.SessionID, Kind: transcriptstore.StreamKindFrameRef, ProjectID: stream.ProjectID,
		RootFrameID: stream.RootFrameID, FrameID: stream.FrameID, Epoch: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	questions := []any{map[string]any{
		"question": "Which structure?", "header": "Structure",
		"options": []any{
			map[string]any{
				"label": "5FQD", "description": "Use the human CRBN complex.",
				"pros": "Experimental ligand pose", "cons": "Older deposition",
				"metadata": map[string]any{"smiles": "O=C1CCC(N2C(=O)CCC2=O)N1"},
			},
			map[string]any{"label": "Predicted", "cons": "No experimental ligand pose"},
		},
	}}
	pausePayload := []byte(`{"status":"awaiting_user_response","detail":"waiting for the user"}`)
	parked, pauseEvent, err := store.ParkAskUserWithTranscript(context.Background(), ParkAskUserInput{
		FrameID: stream.FrameID, ToolID: "ask-1", ToolName: "AskUserQuestion", Questions: questions,
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "pause-ask-1", Phase: transcriptstore.RunnerPhaseWaitingUser,
		Resumable: true, PayloadJSON: pausePayload, Destinations: []string{"ws"},
	})
	if err != nil || parked.AlreadyPending || len(parked.Events) != 3 || pauseEvent.Type != "runner_checkpoint" {
		t.Fatalf("parked=%#v pause=%#v err=%v", parked, pauseEvent, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 20,
	})
	if err != nil || len(projected) != 6 || projected[1].Event.Type != "assistant_message" ||
		projected[2].Event.Type != "user_message" || projected[3].Event.Type != transcriptstore.AskUserPromptEventType ||
		projected[4].Event.Type != transcriptstore.AskUserResultEventType || projected[5].Event.Type != "runner_checkpoint" {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
	newerProjected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: newerStream.UID, OwnerID: newerStream.OwnerID, Limit: 20,
	})
	if err != nil || len(newerProjected) != 0 {
		t.Fatalf("newer epoch projected=%#v err=%v", newerProjected, err)
	}
	var resolvedResult map[string]any
	if err := json.Unmarshal(projected[2].ResolvedPayloadJSON, &resolvedResult); err != nil {
		t.Fatal(err)
	}
	if resolvedResult["role"] != "user" {
		t.Fatalf("AskUser result payload=%#v", resolvedResult)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || intent.Text != "Analyze CRBN." {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	runtime, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Attempt)
	if err != nil || runtime.Status != "running" || runtime.Phase != transcriptstore.RunnerPhaseWaitingUser ||
		runtime.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	repeated, repeatedPause, err := store.ParkAskUserWithTranscript(context.Background(), ParkAskUserInput{
		FrameID: stream.FrameID, ToolID: "ask-1", ToolName: "AskUserQuestion", Questions: questions,
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "pause-ask-1", Phase: transcriptstore.RunnerPhaseWaitingUser,
		Resumable: true, PayloadJSON: pausePayload, Destinations: []string{"ws"},
	})
	if err != nil || !repeated.AlreadyPending || len(repeated.Events) != 3 || repeatedPause.EventID != pauseEvent.EventID {
		t.Fatalf("repeated=%#v pause=%#v err=%v", repeated, repeatedPause, err)
	}
	projected, err = repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 20,
	})
	if err != nil || len(projected) != 6 {
		t.Fatalf("idempotent projected=%#v err=%v", projected, err)
	}
	branchState, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	forked, err := repo.ForkFrameAskUserAnswerBranch(context.Background(), transcriptstore.ForkFrameAskUserAnswerBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: branchState.ActiveBranchID, ExpectedActiveBranchID: branchState.ActiveBranchID,
		ExpectedGeneration: branchState.Generation, ClientMutationID: "answer-ask-1", ToolUseID: "ask-1",
		SourceMessageIndex: 1, Response: transcriptstore.AskUserBranchResponse{
			Action: "answer", Answers: map[string]string{"Which structure?": "5FQD"},
		}, Destinations: []string{"ws"},
	})
	if err != nil || !forked.Created || forked.ForkPoint != 1 {
		t.Fatalf("forked=%#v err=%v", forked, err)
	}
}

func TestParkAskUserWithTranscriptRollsBackAllFrameFactsForStaleClaim(t *testing.T) {
	store, repo, stream, claim := newParkAskUserTranscriptFixture(t)
	claim.ClaimToken += "-stale"
	_, _, err := store.ParkAskUserWithTranscript(context.Background(), ParkAskUserInput{
		FrameID: stream.FrameID, ToolID: "ask-stale", ToolName: "AskUserQuestion",
		Questions: []any{map[string]any{
			"question": "Continue?", "header": "Choice",
			"options": []any{
				map[string]any{"label": "Continue", "description": "Continue the analysis."},
				map[string]any{"label": "Stop", "description": "Stop the analysis."},
			},
		}},
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "pause-stale", Phase: transcriptstore.RunnerPhaseWaitingUser,
		Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_user_response"}`), Destinations: []string{"ws"},
	})
	if !errors.Is(err, transcriptstore.ErrClaimStale) {
		t.Fatalf("stale park error=%v", err)
	}
	frame, found, err := store.GetFrame(stream.FrameID)
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	messages, err := store.CompatibilityFrameMessages(stream.FrameID, 0, 20)
	if err != nil || len(messages.Messages) != 0 {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	metadata, _, err := store.GetFrameRuntimeMetadata(stream.FrameID)
	if err != nil || len(compatibilityPendingInputRequests(metadata.ContextData)) != 0 {
		t.Fatalf("metadata=%#v err=%v", metadata, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 20,
	})
	if err != nil || len(projected) != 1 {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
}

func newParkAskUserTranscriptFixture(
	t *testing.T,
) (*Store, *transcriptstore.Repository, transcriptstore.Stream, transcriptstore.RunnerClaim) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
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
		MessageUUID: "task-message", Text: "Analyze CRBN.", Destinations: []string{"ws"},
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
	return store, repo, stream, claimed.Claim
}
