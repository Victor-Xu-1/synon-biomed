package workspace

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestCloneConversationWithTranscriptCommitsOneCanonicalHistoryAndRetries(t *testing.T) {
	store, source := newCanonicalConversationCloneFixture(t)
	input := CloneConversationInput{
		OwnerUserID: "owner-a", SourceFrameID: source.ID,
		ExpectedSourceIncarnationID: source.IncarnationID,
		TargetFrameID:               "clone-target", TargetName: "Source conversation copy",
	}
	first, err := store.CloneConversationWithTranscript(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Frame.ID != input.TargetFrameID || first.Transcript.EventCount != 1 ||
		first.Transcript.TargetStream.UID != "frame:"+input.TargetFrameID || first.FrameEvent.Type != "frame_created" {
		t.Fatalf("clone result=%#v", first)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(input.TargetFrameID)
	if err != nil || !found {
		t.Fatalf("target metadata found=%v err=%v", found, err)
	}
	if metadata.TaskSummary != "Source summary" || metadata.DelegateName != "reviewer" ||
		metadata.ContextData["web_extra"] == nil || metadata.ContextData["web_assistant"] == nil ||
		metadata.ContextData["_pending_input_requests"] != nil || len(metadata.ContextData) != 2 {
		t.Fatalf("target metadata=%#v", metadata)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	events, err := repository.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: first.Transcript.TargetStream.UID, OwnerID: "owner-a", Limit: 10,
	})
	if err != nil || len(events) != 1 || events[0].Event.Source != transcriptstore.EventSourcePayload ||
		events[0].Event.Type != "history_user_message" {
		t.Fatalf("target events=%#v err=%v", events, err)
	}
	assertCanonicalConversationCloneRows(t, store, input.TargetFrameID, 1, 2)

	again, err := store.CloneConversationWithTranscript(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if again.Transcript.GenesisSHA256 != first.Transcript.GenesisSHA256 ||
		again.Transcript.SourceSHA256 != first.Transcript.SourceSHA256 ||
		again.Frame.IncarnationID != first.Frame.IncarnationID || again.FrameEvent.ID != first.FrameEvent.ID {
		t.Fatalf("retry=%#v first=%#v", again, first)
	}
	assertCanonicalConversationCloneRows(t, store, input.TargetFrameID, 1, 2)
	conflict := input
	conflict.TargetName = "Different clone identity"
	if _, err := store.CloneConversationWithTranscript(context.Background(), conflict); err == nil {
		t.Fatal("same target with different clone identity unexpectedly succeeded")
	}

	const concurrentRetries = 32
	start := make(chan struct{})
	errorsByRetry := make(chan error, concurrentRetries)
	var wait sync.WaitGroup
	for index := 0; index < concurrentRetries; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.CloneConversationWithTranscript(context.Background(), input)
			errorsByRetry <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByRetry)
	for err := range errorsByRetry {
		if err != nil {
			t.Fatalf("concurrent clone retry: %v", err)
		}
	}
	assertCanonicalConversationCloneRows(t, store, input.TargetFrameID, 1, 2)
}

func TestCloneConversationWithTranscriptRollsBackEveryTargetAuthorityOnOutboxFailure(t *testing.T) {
	store, source := newCanonicalConversationCloneFixture(t)
	if _, err := store.db.Exec(`CREATE TRIGGER fail_clone_list_changed
		BEFORE INSERT ON workspace_outbox
		WHEN NEW.event_type='conversation.listChanged'
		BEGIN SELECT RAISE(ABORT,'forced clone list failure'); END`); err != nil {
		t.Fatal(err)
	}
	input := CloneConversationInput{
		OwnerUserID: "owner-a", SourceFrameID: source.ID,
		ExpectedSourceIncarnationID: source.IncarnationID,
		TargetFrameID:               "clone-rollback", TargetName: "Rollback copy",
	}
	if _, err := store.CloneConversationWithTranscript(context.Background(), input); err == nil {
		t.Fatal("clone unexpectedly succeeded through outbox failure")
	}
	for name, query := range map[string]string{
		"frame":       `SELECT COUNT(*) FROM frames WHERE id='clone-rollback'`,
		"metadata":    `SELECT COUNT(*) FROM frame_runtime_metadata WHERE frame_id='clone-rollback'`,
		"frame event": `SELECT COUNT(*) FROM frame_events WHERE frame_id='clone-rollback'`,
		"stream":      `SELECT COUNT(*) FROM transcript_streams WHERE session_id='clone-rollback'`,
		"authority":   `SELECT COUNT(*) FROM transcript_frame_authority WHERE session_id='clone-rollback'`,
		"genesis":     `SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE session_id='clone-rollback'`,
		"outbox":      `SELECT COUNT(*) FROM workspace_outbox WHERE aggregate_id='clone-rollback'`,
	} {
		var count int
		if err := store.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", name, count, err)
		}
	}
	var sourceEvents, sourceGenesis int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid='frame:clone-source'`).Scan(&sourceEvents); err != nil || sourceEvents != 1 {
		t.Fatalf("source events=%d err=%v", sourceEvents, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE stream_uid='frame:clone-source'`).Scan(&sourceGenesis); err != nil || sourceGenesis != 1 {
		t.Fatalf("source genesis=%d err=%v", sourceGenesis, err)
	}
}

func newCanonicalConversationCloneFixture(t *testing.T) (*Store, Frame) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "clone-project", UserID: "owner-a", Name: "Clone Project"}); err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateFrame(CreateFrameInput{
		ID: "clone-source", ProjectID: "clone-project", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Source conversation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(source.ID, FrameRuntimeMetadata{
		DelegateName: "reviewer", TaskSummary: "Source summary", ContextData: map[string]any{
			"web_extra":               map[string]any{"project_id": "clone-project", "selected_view": "timeline"},
			"web_assistant":           map[string]any{"id": "synonbiomed:OPERON"},
			"_pending_input_requests": []any{map[string]any{"request_id": "stale"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{
		ID: "clone-source-user", FrameID: source.ID, Type: "user_message",
		Payload: map[string]any{"role": "user", "text": "immutable source history"},
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + source.ID, OwnerID: "owner-a", ExternalID: source.ID, SessionID: source.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: source.ProjectID,
		RootFrameID: source.RootFrameID, FrameID: source.ID, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.GetFrame(source.ID)
	if err != nil || !found {
		t.Fatalf("source frame found=%v err=%v", found, err)
	}
	return store, stored
}

func assertCanonicalConversationCloneRows(t *testing.T, store *Store, targetID string, frameEvents, outbox int) {
	t.Helper()
	for name, query := range map[string]string{
		"frame event": `SELECT COUNT(*) FROM frame_events WHERE frame_id=?`,
		"outbox":      `SELECT COUNT(*) FROM workspace_outbox WHERE aggregate_id=?`,
	} {
		var count int
		want := frameEvents
		if name == "outbox" {
			want = outbox
		}
		if err := store.db.QueryRow(query, targetID).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", name, count, want, err)
		}
	}
	var messageEvents int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=? AND event_type IN
		('message','user_message','assistant_message','system_message','tool_use','tool_result','ask_user_answer')`, targetID).
		Scan(&messageEvents); err != nil || messageEvents != 0 {
		t.Fatalf("target message frame events=%d err=%v", messageEvents, err)
	}
	var cloneReceipts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_payload_genesis_receipts
		WHERE stream_uid=? AND source_kind='canonical_clone'`, "frame:"+targetID).Scan(&cloneReceipts); err != nil || cloneReceipts != 1 {
		t.Fatalf("target clone receipts=%d err=%v", cloneReceipts, err)
	}
}
