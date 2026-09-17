package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestTranscriptRebaseFixtureUsesProductionCutoverAndActivation(t *testing.T) {
	store := fixtureStore(t, "processing")
	ctx := context.Background()
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareTranscriptRebaseFixture(store, fixtureRequest{
		Action: "prepare-transcript-rebase", FrameID: "frame", HistoryCount: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	cutoverID, _ := prepared["cutoverId"].(string)
	if prepared["ok"] != true || cutoverID == "" || prepared["eventCount"].(int) < 120 || prepared["cursorCount"].(int) < 120 {
		t.Fatalf("prepared=%#v", prepared)
	}
	for {
		delivery, err := repository.ClaimNextDelivery(ctx, transcriptstore.ClaimDeliveryInput{
			OwnerID: stream.OwnerID, Destination: "ws", WorkerID: "fixture-test", TTL: time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !delivery.Claimed {
			break
		}
		if _, err := repository.AcknowledgeDelivery(ctx, transcriptstore.AcknowledgeDeliveryInput{Claim: delivery.Claim}); err != nil {
			t.Fatal(err)
		}
	}
	activated, err := activateTranscriptRebaseFixture(store, fixtureRequest{
		Action: "activate-transcript-rebase", FrameID: "frame", CutoverID: cutoverID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if activated["ok"] != true || activated["authorityGeneration"].(int64) != 2 || activated["targetEpoch"].(int64) != 2 {
		t.Fatalf("activated=%#v", activated)
	}
	authority, found, err := repository.GetFrameAuthorityBySession(ctx, stream.OwnerID, stream.SessionID)
	if err != nil || !found || !authority.TranscriptPayloadActive() || authority.ActiveStreamUID == stream.UID {
		t.Fatalf("authority=%#v found=%v err=%v", authority, found, err)
	}
	rebases, err := repository.ListActiveFrameRealtimeRebases(ctx, stream.OwnerID, stream.SessionID, 0, 10)
	if err != nil || len(rebases) != 1 || rebases[0].ActivationSHA256 != activated["activationId"] {
		t.Fatalf("rebases=%#v err=%v", rebases, err)
	}
	before := authority
	if _, err := prepareTranscriptRebaseFixture(store, fixtureRequest{
		Action: "prepare-transcript-rebase", FrameID: "frame", HistoryCount: 120,
	}); err == nil || !strings.Contains(err.Error(), "requires a new epoch-one conversation") {
		t.Fatalf("activated preparation error=%v", err)
	}
	after, found, err := repository.GetFrameAuthorityBySession(ctx, stream.OwnerID, stream.SessionID)
	if err != nil || !found || after.ActiveStreamUID != before.ActiveStreamUID ||
		after.ActiveEpoch != before.ActiveEpoch || after.AuthorityGeneration != before.AuthorityGeneration ||
		after.ReadAuthority != before.ReadAuthority || after.WriteAuthority != before.WriteAuthority ||
		string(after.ActivationID) != string(before.ActivationID) || string(after.GenesisID) != string(before.GenesisID) {
		t.Fatalf("activated authority changed: before=%#v after=%#v found=%t err=%v", before, after, found, err)
	}
}

func TestTranscriptRebaseFixtureRejectsNonEmptyPayloadConversationWithoutMutation(t *testing.T) {
	store := fixtureStore(t, "processing")
	ctx := context.Background()
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	frameEvent, err := store.AppendFrameEvent(workspace.FrameEventInput{
		ID: "occupied-frame-event", FrameID: "frame", Type: "user_message",
		Payload: map[string]any{"role": "user", "content": "Do not replace this history.", "uuid": "occupied-message"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "occupied-client",
		FrameEventID: frameEvent.ID, MessageUUID: "occupied-message", Text: "Do not replace this history.",
	}); err != nil || !created {
		t.Fatalf("append occupied history created=%t err=%v", created, err)
	}
	if _, err := prepareTranscriptRebaseFixture(store, fixtureRequest{
		Action: "prepare-transcript-rebase", FrameID: "frame", HistoryCount: 120,
	}); err == nil || !strings.Contains(err.Error(), "requires an empty conversation") {
		t.Fatalf("nonempty preparation error=%v", err)
	}
	authority, found, err := repository.GetFrameAuthorityBySession(ctx, stream.OwnerID, stream.SessionID)
	if err != nil || !found || !authority.TranscriptPayloadActive() || authority.ActiveStreamUID != stream.UID {
		t.Fatalf("authority=%#v found=%t err=%v", authority, found, err)
	}
	var events, receipts int
	if err := repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, stream.UID).Scan(&events); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE stream_uid=?`, stream.UID).Scan(&receipts)
	}); err != nil {
		t.Fatal(err)
	}
	if events != 1 || receipts != 1 {
		t.Fatalf("nonempty preparation mutated authority: events=%d receipts=%d", events, receipts)
	}
}

func TestSeedAskUserFixtureUsesDurableWorkspaceProjection(t *testing.T) {
	store := fixtureStore(t, "processing")
	ctx := context.Background()
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	frameEvent, err := store.AppendFrameEvent(workspace.FrameEventInput{
		ID: "fixture-task-event", FrameID: "frame", Type: "user_message",
		Payload: map[string]any{"role": "user", "content": "Choose a candidate.", "uuid": "fixture-task-message"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "fixture-task",
		FrameEventID: frameEvent.ID, MessageUUID: "fixture-task-message", Text: "Choose a candidate.",
		Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append fixture task created=%t err=%v", created, err)
	}
	for index := 0; index < 260; index++ {
		suffix := fmt.Sprintf("%03d", index)
		frameEvent, err := store.AppendFrameEvent(workspace.FrameEventInput{
			ID: "fixture-history-event-" + suffix, FrameID: "frame", Type: "user_message",
			Payload: map[string]any{
				"role": "user", "content": "Historical fixture input " + suffix, "uuid": "fixture-history-message-" + suffix,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, created, err := repository.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "fixture-history-client-" + suffix,
			FrameEventID: frameEvent.ID, MessageUUID: "fixture-history-message-" + suffix,
			Text: "Historical fixture input " + suffix, Destinations: []string{"ws"},
		}); err != nil || !created {
			t.Fatalf("append fixture history %s created=%t err=%v", suffix, created, err)
		}
	}
	request, err := decodeRequest(strings.NewReader(`{
		"action":"seed-ask-user","frameId":"frame","toolId":"ask-1",
		"questions":[{"header":"Candidate","question":"Choose one","options":[
			{"label":"A","description":"Choose candidate A."},
			{"label":"B","description":"Choose candidate B."}
		]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := seedAskUserFixture(store, request)
	if err != nil || response["alreadyPending"] != false || response["frameId"] != "frame" || response["toolId"] != "ask-1" {
		t.Fatalf("seed response=%#v err=%v", response, err)
	}
	frame, found, err := store.GetFrame("frame")
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("frame=%#v found=%v err=%v", frame, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame")
	if err != nil || !found || len(metadata.ContextData["_pending_input_requests"].([]any)) != 1 {
		t.Fatalf("metadata=%#v found=%v err=%v", metadata, found, err)
	}
	assertAskUserMessages(t, store, `{"status":"awaiting_user_response"}`)
	events, err := store.ListFrameEvents("frame", 0, 500)
	if err != nil || len(events) != 264 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	duplicate, err := seedAskUserFixture(store, request)
	if err != nil || duplicate["alreadyPending"] != true {
		t.Fatalf("duplicate=%#v err=%v", duplicate, err)
	}
	events, _ = store.ListFrameEvents("frame", 0, 500)
	if len(events) != 264 {
		t.Fatalf("duplicate created %d events", len(events))
	}
	snapshot, err := repository.GetProjectionSnapshot(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := repository.ListProjectedEvents(ctx, transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
		AfterPublicationSequence: 250, ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	var origin transcriptstore.AskUserOriginV1
	for _, event := range projected {
		if event.Event.Type != transcriptstore.AskUserPromptEventType {
			continue
		}
		prompt, decodeErr := transcriptstore.DecodeAskUserPromptV1(event.ResolvedPayloadJSON)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		origin = prompt.Origin
	}
	if origin.ToolUseID != "ask-1" {
		t.Fatalf("ask-user origin=%#v", origin)
	}
	answer, err := transcriptstore.NewAskUserResultV1(
		transcriptstore.AskUserActionAnswer, map[string]string{"Choose one": "A"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	encodedAnswer, err := transcriptstore.EncodeAskUserResultV1(answer)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveCompatibilityPendingInputsWithTranscript(ctx, "frame", []workspace.CompatibilityInputResolution{{
		ToolID: "ask-1", Content: string(encodedAnswer), ModelContinuation: "Use candidate A.",
		AskUserResult: &answer, AskUserOrigin: &origin,
	}})
	if err != nil || resolved.Status != "accepted" {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	assertAskUserMessages(t, store, string(encodedAnswer))
}

func TestSeedAskUserFixtureCreatesItsTaskIntentWithoutRunnerCompetition(t *testing.T) {
	store := fixtureStore(t, "processing")
	ctx := context.Background()
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := decodeRequest(strings.NewReader(`{
		"action":"seed-ask-user","frameId":"frame","toolId":"ask-standalone",
		"questions":[{"header":"Candidate","question":"Choose one","options":[
			{"label":"A","description":"Choose candidate A."},
			{"label":"B","description":"Choose candidate B."}
		]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := seedAskUserFixture(store, request)
	if err != nil || response["alreadyPending"] != false {
		t.Fatalf("seed response=%#v err=%v", response, err)
	}
	intent, found, err := repository.GetActiveFrameTaskIntent(ctx, stream.UID, stream.OwnerID)
	if err != nil || !found || intent.Origin != "user" ||
		intent.Text != "Compare the candidate compounds and ask which option should continue." {
		t.Fatalf("task intent=%#v found=%t err=%v", intent, found, err)
	}
}

func TestSeedScrollHistoryFixtureUsesCanonicalTranscriptEvents(t *testing.T) {
	store := fixtureStore(t, "processing")
	ctx := context.Background()
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := decodeRequest(strings.NewReader(`{"action":"seed-scroll-history","frameId":"frame","historyCount":8}`))
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		response, seedErr := seedScrollHistoryFixture(store, request)
		ids, _ := response["messageIds"].([]string)
		if seedErr != nil || response["ok"] != true || len(ids) != 8 || ids[0] != "e2e-scroll-message:frame:000" {
			t.Fatalf("attempt=%d response=%#v err=%v", attempt, response, seedErr)
		}
	}
	projected, err := repository.ListProjectedEvents(ctx, transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 20,
	})
	if err != nil || len(projected) != 8 {
		t.Fatalf("projected=%d err=%v", len(projected), err)
	}
}

func TestSeedAskUserFixtureRejectsInvalidInputWithoutPartialState(t *testing.T) {
	store := fixtureStore(t, "completed")
	for _, input := range []string{
		`{"action":"seed-ask-user","frameId":"frame","toolId":"ask-1","questions":[],"extra":true}`,
		`{"action":"seed-ask-user","frameId":"frame","toolId":"ask-1","questions":[{"question":7}]}`,
		`{"action":"seed-ask-user","frameId":"frame","toolId":"","questions":[{"question":"Q"}]}`,
	} {
		if _, err := decodeRequest(strings.NewReader(input)); err == nil {
			t.Fatalf("invalid input accepted: %s", input)
		}
	}
	request, err := decodeRequest(strings.NewReader(
		`{"action":"seed-ask-user","frameId":"frame","toolId":"ask-1","questions":[{"question":"Q"}]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedAskUserFixture(store, request); err == nil {
		t.Fatal("terminal frame accepted ask-user seed")
	}
	missing := request
	missing.FrameID = "missing"
	if _, err := seedAskUserFixture(store, missing); err == nil {
		t.Fatal("missing frame accepted ask-user seed")
	}
	events, err := store.ListFrameEvents("frame", 0, 20)
	if err != nil || len(events) != 0 {
		t.Fatalf("partial events=%d err=%v", len(events), err)
	}
}

func fixtureStore(t *testing.T, status string) *workspace.Store {
	t.Helper()
	store, err := workspace.Open(t.TempDir() + "/workspace.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "owner", Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: status, ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func assertAskUserMessages(t *testing.T, store *workspace.Store, resultContent string) {
	t.Helper()
	page, err := store.CompatibilityFrameMessages("frame", 250, 20)
	if err != nil || len(page.Messages) < 2 {
		t.Fatalf("messages=%#v err=%v", page, err)
	}
	assistant := page.Messages[len(page.Messages)-2]["content"].([]any)[0].(map[string]any)
	result := page.Messages[len(page.Messages)-1]["content"].([]any)[0].(map[string]any)
	if assistant["type"] != "tool_use" || assistant["name"] != "ask_user" || assistant["id"] != "ask-1" ||
		result["type"] != "tool_result" || result["tool_use_id"] != "ask-1" || result["content"] != resultContent {
		t.Fatalf("assistant=%#v result=%#v", assistant, result)
	}
}
