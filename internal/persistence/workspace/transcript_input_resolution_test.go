package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestResolveAskUserWithTranscriptUsesTypedExactStreamAuthority(t *testing.T) {
	store, repo, stream, claim := newParkAskUserTranscriptFixture(t)
	newer, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
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
			map[string]any{"label": "5FQD", "description": "Use the human CRBN complex."},
			map[string]any{"label": "Predicted", "description": "Use a predicted structure."},
		},
	}}
	if _, _, err := store.ParkAskUserWithTranscript(context.Background(), ParkAskUserInput{
		FrameID: stream.FrameID, ToolID: "ask-typed", ToolName: "AskUserQuestion", Questions: questions,
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "pause-typed", Phase: transcriptstore.RunnerPhaseWaitingUser,
		Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_user_response"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(stream.FrameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	origins := metadata.ContextData[askUserTranscriptOriginsKey].(map[string]any)
	encodedOrigin, err := json.Marshal(origins["ask-typed"])
	if err != nil {
		t.Fatal(err)
	}
	origin, err := transcriptstore.DecodeAskUserOriginV1(encodedOrigin)
	if err != nil {
		t.Fatal(err)
	}
	pendingRequests := compatibilityPendingInputRequests(metadata.ContextData)
	pendingRequests = append(pendingRequests, map[string]any{
		"tool_id": "network-typed", "requestId": "network-typed", "kind": "network",
	})
	metadata.ContextData["_pending_input_requests"] = compatibilityMapsToAny(pendingRequests)
	if _, err := store.SetFrameRuntimeMetadata(stream.FrameID, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{FrameID: stream.FrameID, Type: "user_message", Payload: map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "network-typed", "content": `{"status":"awaiting_user_response"}`,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	answer, err := transcriptstore.NewAskUserResultV1(
		transcriptstore.AskUserActionAnswer, map[string]string{"Which structure?": "5FQD"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := transcriptstore.EncodeAskUserResultV1(answer)
	if err != nil {
		t.Fatal(err)
	}
	resolution := CompatibilityInputResolution{
		ToolID: "ask-typed", Content: string(encoded),
		ModelContinuation: `{"status":"answered","answers":{"Which structure?":"5FQD"}}`,
		AskUserResult:     &answer, AskUserOrigin: &origin,
	}
	staleOrigin := origin
	staleOrigin.BranchGeneration++
	staleResolution := resolution
	staleResolution.AskUserOrigin = &staleOrigin
	if _, err := store.ResolveCompatibilityPendingInputsWithTranscript(
		context.Background(), stream.FrameID, []CompatibilityInputResolution{staleResolution},
	); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("stale expected origin error=%v", err)
	}
	partial, err := store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), stream.FrameID, []CompatibilityInputResolution{resolution})
	if err != nil || partial.Status != "partial" || partial.AlreadyResolved || len(partial.RemainingIDs) != 1 || partial.RemainingIDs[0] != "network-typed" {
		t.Fatalf("partial=%#v err=%v", partial, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	statuses := []transcriptstore.AskUserStatus{}
	for _, event := range projected {
		if event.Event.Type != transcriptstore.AskUserResultEventType {
			continue
		}
		var payload transcriptstore.AskUserResultEventV1
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, payload.Result.Status)
	}
	if len(statuses) != 2 || statuses[0] != transcriptstore.AskUserStatusAwaitingResponse || statuses[1] != transcriptstore.AskUserStatusAnswered {
		t.Fatalf("typed statuses=%#v projected=%#v", statuses, projected)
	}
	newerProjected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: newer.UID, OwnerID: newer.OwnerID, Limit: 30,
	})
	if err != nil || len(newerProjected) != 0 {
		t.Fatalf("newer projected=%#v err=%v", newerProjected, err)
	}
	messages, err := store.CompatibilityFrameMessages(stream.FrameID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	rawMessages, _ := json.Marshal(messages.Messages)
	if !strings.Contains(string(rawMessages), `\"version\":1`) || !strings.Contains(string(rawMessages), `model_continuation`) {
		t.Fatalf("compatibility result is not split into structured state and model continuation: %s", rawMessages)
	}
	repeated, err := store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), stream.FrameID, []CompatibilityInputResolution{resolution})
	if err != nil || !repeated.AlreadyResolved || repeated.Status != "partial" || len(repeated.RemainingIDs) != 1 {
		t.Fatalf("repeated=%#v err=%v", repeated, err)
	}
	differentContinuation := resolution
	differentContinuation.ModelContinuation = "different model continuation"
	_, err = store.ResolveCompatibilityPendingInputsWithTranscript(
		context.Background(), stream.FrameID, []CompatibilityInputResolution{differentContinuation},
	)
	if !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("continuation conflict error=%v", err)
	}
	cancelled, err := transcriptstore.NewAskUserResultV1(transcriptstore.AskUserActionCancel, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	cancelledEncoded, _ := transcriptstore.EncodeAskUserResultV1(cancelled)
	_, err = store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), stream.FrameID, []CompatibilityInputResolution{{
		ToolID: "ask-typed", Content: string(cancelledEncoded), ModelContinuation: "cancelled",
		AskUserResult: &cancelled, AskUserOrigin: &origin,
	}})
	if !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("conflicting retry error=%v", err)
	}
	accepted, err := store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), stream.FrameID, []CompatibilityInputResolution{{
		ToolID: "network-typed", Content: `{"approved":true,"scope":"conversation"}`,
	}})
	if err != nil || accepted.Status != "accepted" || accepted.AlreadyResolved || accepted.InputEvent != nil {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	repeated, err = store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), stream.FrameID, []CompatibilityInputResolution{resolution})
	if err != nil || !repeated.AlreadyResolved || repeated.Status != "already_resolved" {
		t.Fatalf("terminal repeated=%#v err=%v", repeated, err)
	}
}

func TestResolveCompatibilityPendingInputsWithTranscriptCommitsOneCanonicalResponse(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "agent", Status: "processing", ConversationType: "task",
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
		MessageUUID: "task-message", Text: "Analyze CRBN and ask for the structure.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	seedPendingTranscriptInput(t, store)

	partial, err := store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), "frame", []CompatibilityInputResolution{{
		ToolID: "ask-1", Content: `{"status":"answered","answers":{"Which structure?":"5FQD"}}`,
	}})
	if err != nil || partial.Status != "partial" {
		t.Fatalf("partial=%#v err=%v", partial, err)
	}
	current, err := repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || current.InputRevision != 1 {
		t.Fatalf("partial stream=%#v err=%v", current, err)
	}

	accepted, err := store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), "frame", []CompatibilityInputResolution{{
		ToolID: "network-1", Content: `{"approved":true,"scope":"conversation"}`,
	}})
	if err != nil || accepted.Status != "accepted" || accepted.InputEvent != nil {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	current, err = repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || current.InputRevision != 2 {
		t.Fatalf("accepted stream=%#v err=%v", current, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil || len(projected) != 2 || projected[1].Event.Type != "user_input_response" ||
		projected[1].Event.Source != transcriptstore.EventSourcePayload || projected[1].Event.FrameEventID != nil {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
	var frameInputResponses int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame' AND event_type='user_input_response'`).
		Scan(&frameInputResponses); err != nil || frameInputResponses != 0 {
		t.Fatalf("frame input responses=%d err=%v", frameInputResponses, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(projected[1].ResolvedPayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	text, _ := payload["text"].(string)
	if payload["messageOrigin"] != "input_response" || !strings.Contains(text, "5FQD") || !strings.Contains(text, `"approved":true`) {
		t.Fatalf("response payload=%#v", payload)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || intent.Revision != 1 || intent.Text != "Analyze CRBN and ask for the structure." {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	repeated, err := store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), "frame", []CompatibilityInputResolution{{
		ToolID: "network-1", Content: `{"approved":true,"scope":"conversation"}`,
	}})
	if err != nil || !repeated.AlreadyResolved {
		t.Fatalf("repeated=%#v err=%v", repeated, err)
	}
	projected, err = repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil || len(projected) != 2 {
		t.Fatalf("idempotent projected=%#v err=%v", projected, err)
	}
	replay, err := repo.ListRunnerReplay(context.Background(), transcriptstore.ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, MessageLimit: 10, CheckpointLimit: 10,
	})
	if err != nil || len(replay) != 2 || replay[1].Event.Type != "user_input_response" ||
		string(replay[1].ResolvedPayloadJSON) != string(projected[1].ResolvedPayloadJSON) {
		t.Fatalf("runner replay=%#v err=%v", replay, err)
	}
}

func TestResolveCompatibilityPendingInputsWithTranscriptPreservesLegacyFrameReference(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "agent", Status: "processing", ConversationType: "task",
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
	if _, err := store.db.Exec(`DELETE FROM transcript_frame_authority WHERE owner_id='owner' AND session_id='frame';
		DELETE FROM transcript_payload_genesis_receipts WHERE stream_uid='frame:frame';
		INSERT INTO transcript_frame_authority(
			owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
			read_authority,write_authority,activation_id,genesis_id,updated_at)
		VALUES('owner','frame','frame:frame',1,1,'legacy_mixed_v1','legacy_frame_ref_v1',NULL,NULL,?)`,
		store.now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task", FrameEventID: "task-event",
		MessageUUID: "task-message", Text: "Ask before continuing.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	seedPendingTranscriptInput(t, store)
	resolved := []CompatibilityInputResolution{
		{ToolID: "ask-1", Content: `{"status":"answered","answers":{"Proceed?":"yes"}}`},
		{ToolID: "network-1", Content: `{"approved":true,"scope":"conversation"}`},
	}
	accepted, err := store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), "frame", resolved)
	if err != nil || accepted.Status != "accepted" || accepted.InputEvent == nil ||
		accepted.InputEvent.Type != "user_input_response" {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 10,
	})
	if err != nil || len(projected) != 2 || projected[1].Event.Source != transcriptstore.EventSourceFrameRef ||
		projected[1].Event.FrameEventID == nil || *projected[1].Event.FrameEventID != accepted.InputEvent.ID {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
	var frameInputResponses int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame' AND event_type='user_input_response'`).
		Scan(&frameInputResponses); err != nil || frameInputResponses != 1 {
		t.Fatalf("frame input responses=%d err=%v", frameInputResponses, err)
	}
	repeated, err := store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), "frame", resolved)
	if err != nil || !repeated.AlreadyResolved {
		t.Fatalf("repeated=%#v err=%v", repeated, err)
	}
}

func TestResolveCompatibilityPendingInputsWithTranscriptRollsBackWithoutDeliveryAuthority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "agent", Status: "processing", ConversationType: "task",
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
		MessageUUID: "task-message", Text: "Ask before continuing.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame", FrameRuntimeMetadata{ContextData: map[string]any{
		"_pending_input_requests": []any{map[string]any{"tool_id": "ask-1", "kind": "ask"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{FrameID: "frame", Type: "user_message", Payload: map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-1", "content": `{"status":"awaiting_user_response"}`,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE frames SET status='awaiting_user_response' WHERE id='frame';
		UPDATE transcript_delivery_routes SET status='revoked' WHERE stream_uid=? AND destination='ws'`, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveCompatibilityPendingInputsWithTranscript(context.Background(), "frame", []CompatibilityInputResolution{{
		ToolID: "ask-1", Content: `{"status":"answered","answers":{"Proceed?":"yes"}}`,
	}}); err == nil {
		t.Fatal("resolution succeeded without an active delivery authority")
	}
	frame, found, err := store.GetFrame("frame")
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	page, err := store.CompatibilityFrameMessages("frame", 0, 10)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("messages=%#v err=%v", page, err)
	}
	raw, _ := json.Marshal(page.Messages[0])
	if !strings.Contains(string(raw), "awaiting_user_response") || strings.Contains(string(raw), "Proceed?") {
		t.Fatalf("resolution mutation escaped rollback: %s", raw)
	}
	current, err := repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || current.InputRevision != 1 {
		t.Fatalf("stream=%#v err=%v", current, err)
	}
}

func seedPendingTranscriptInput(t *testing.T, store *Store) {
	t.Helper()
	pending := []any{
		map[string]any{"tool_id": "ask-1", "kind": "ask"},
		map[string]any{"tool_id": "network-1", "kind": "network"},
	}
	if _, err := store.SetFrameRuntimeMetadata("frame", FrameRuntimeMetadata{ContextData: map[string]any{
		"_pending_input_requests": pending,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{FrameID: "frame", Type: "user_message", Payload: map[string]any{
		"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "ask-1", "content": `{"status":"awaiting_user_response"}`},
			map[string]any{"type": "tool_result", "tool_use_id": "network-1", "content": `{"status":"awaiting_user_response"}`},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE frames SET status='awaiting_user_response' WHERE id='frame'`); err != nil {
		t.Fatal(err)
	}
}
