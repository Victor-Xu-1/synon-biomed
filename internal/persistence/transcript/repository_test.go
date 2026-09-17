package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSchemaErrorPreservesLocalContextDeadlineWithoutClaimingSchemaLoss(t *testing.T) {
	err := schemaError(context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSchemaUnavailable) {
		t.Fatalf("deadline schema classification=%v", err)
	}
	deterministic := schemaError(errors.New("no such table: transcript_streams"))
	if !errors.Is(deterministic, ErrSchemaUnavailable) {
		t.Fatalf("deterministic schema classification=%v", deterministic)
	}
}

const transcriptExternalAuthorityFixture = `
CREATE TABLE projects (id TEXT PRIMARY KEY, user_id TEXT NOT NULL);
CREATE TABLE frames (
  id TEXT PRIMARY KEY,
	incarnation_id TEXT NOT NULL DEFAULT '',
  project_id TEXT NOT NULL,
  root_frame_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'processing',
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE frame_runtime_metadata (
  frame_id TEXT PRIMARY KEY,
  context_data TEXT NOT NULL DEFAULT '{}',
  status_description TEXT,
  completed_at TIMESTAMP
);
CREATE TABLE frame_events (
  id TEXT PRIMARY KEY,
  frame_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL
);
CREATE TABLE frame_task_intents (
  id TEXT PRIMARY KEY,
  frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL,
  source_event_id TEXT NOT NULL UNIQUE REFERENCES frame_events(id) ON DELETE CASCADE,
  source_message_id TEXT NOT NULL,
  origin TEXT NOT NULL,
  language TEXT NOT NULL,
  text TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
  UNIQUE (frame_id, revision),
  UNIQUE (frame_id, source_message_id)
);
CREATE TABLE frame_active_task_intents (
  frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
  task_intent_id TEXT NOT NULL UNIQUE REFERENCES frame_task_intents(id) ON DELETE CASCADE,
  updated_at TIMESTAMP NOT NULL
);
CREATE TABLE artifacts (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, name TEXT NOT NULL DEFAULT '');
CREATE TABLE artifact_versions (id TEXT PRIMARY KEY, artifact_id TEXT NOT NULL);
CREATE TABLE artifact_runtime_metadata (artifact_id TEXT PRIMARY KEY, root_frame_id TEXT, frame_id TEXT);
CREATE TABLE artifact_version_provenance (version_id TEXT PRIMARY KEY, frame_id TEXT);`

func newTranscriptRepository(t *testing.T) (*Repository, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(transcriptExternalAuthorityFixture); err != nil {
		t.Fatal(err)
	}
	statements := SchemaContractStatements()
	for index, statement := range statements {
		if statement == transcriptWebProjectionStateV38Statement {
			statements[index] = transcriptWebProjectionStateV67Statement
		}
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("schema statement %d: %v", index, err)
		}
	}
	return NewRepository(db), db, dsn
}

func seedTranscriptInput(t *testing.T, repo *Repository, uid, owner string) {
	t.Helper()
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: uid, OwnerID: owner, ExternalID: "external-" + uid, Kind: StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: uid, OwnerID: owner, ClientMessageID: "user-1", PayloadJSON: []byte(`{"text":"work"}`),
		Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append user created=%t err=%v", created, err)
	}
}

func TestRepositoryFailsClosedWithoutTranscriptSchema(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "empty.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = NewRepository(db).CreateStream(context.Background(), CreateStreamInput{
		UID: "stream", OwnerID: "owner", ExternalID: "external", Kind: StreamKindStandalone, Epoch: 1,
	})
	if !errors.Is(err, ErrSchemaUnavailable) {
		t.Fatalf("CreateStream error=%v", err)
	}
}

func TestRepositoryGetStreamBySessionIsOwnerScopedAndRejectsAmbiguousAuthority(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	input := CreateStreamInput{
		UID: "standalone-a", OwnerID: "owner-a", ExternalID: "external-a", SessionID: "session-a",
		Kind: StreamKindStandalone, Epoch: 1,
	}
	if _, err := repo.CreateStream(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	foreign := input
	foreign.UID, foreign.OwnerID, foreign.ExternalID = "standalone-b", "owner-b", "external-b"
	if _, err := repo.CreateStream(context.Background(), foreign); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign create error=%v", err)
	}
	stream, found, err := repo.GetStreamBySession(context.Background(), "owner-a", "session-a")
	if err != nil || !found || stream.UID != "standalone-a" {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	if _, found, err := repo.GetStreamBySession(context.Background(), "owner-c", "session-a"); err != nil || found {
		t.Fatalf("foreign lookup found=%t err=%v", found, err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "standalone-a-epoch-2", OwnerID: "owner-a", ExternalID: "external-a-2", SessionID: "session-a",
		Kind: StreamKindStandalone, Epoch: 2,
	}); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("same-session epoch conflict error=%v", err)
	}
	if stream, found, err := repo.GetStreamBySession(context.Background(), "owner-a", "session-a"); err != nil || !found || stream.UID != input.UID {
		t.Fatalf("stable lookup stream=%#v found=%t err=%v", stream, found, err)
	}
}

func TestRepositoryCreateStreamCannotMutateConflictingOwnerBeforeValidation(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	input := CreateStreamInput{
		UID: "stream-owner-boundary", OwnerID: "owner-a", ExternalID: "external-owner-boundary",
		Kind: StreamKindStandalone, Epoch: 1,
	}
	if _, err := repo.CreateStream(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM transcript_delivery_routes WHERE stream_uid=?`, input.UID); err != nil {
		t.Fatal(err)
	}
	foreign := input
	foreign.OwnerID = "owner-b"
	if _, err := repo.CreateStream(context.Background(), foreign); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign create error=%v", err)
	}
	var routes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_routes WHERE stream_uid=?`, input.UID).Scan(&routes); err != nil || routes != 0 {
		t.Fatalf("foreign request mutated routes=%d err=%v", routes, err)
	}
	conflict := input
	conflict.ExternalID = "different-external"
	if _, err := repo.CreateStream(context.Background(), conflict); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("shape conflict error=%v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_routes WHERE stream_uid=?`, input.UID).Scan(&routes); err != nil || routes != 0 {
		t.Fatalf("conflicting request mutated routes=%d err=%v", routes, err)
	}
	if _, err := repo.CreateStream(context.Background(), input); err != nil {
		t.Fatalf("owned idempotent repair error=%v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_routes WHERE stream_uid=? AND destination='ws'`, input.UID).Scan(&routes); err != nil || routes != 1 {
		t.Fatalf("owned repair routes=%d err=%v", routes, err)
	}
}

func TestRepositoryIMSessionOwnerCannotBeBypassedByCaller(t *testing.T) {
	for _, sessionID := range []string{"im:wechat:exclusive", "IM:wechat:exclusive", "standalone-session"} {
		t.Run(sessionID, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			input := CreateStreamInput{
				UID: "exclusive-a", OwnerID: "owner-a", ExternalID: sessionID, SessionID: sessionID,
				Kind: StreamKindStandalone, Epoch: 1,
			}
			if _, err := repo.CreateStream(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			foreign := input
			foreign.UID = "exclusive-b"
			foreign.OwnerID = "owner-b"
			if _, err := repo.CreateStream(context.Background(), foreign); !errors.Is(err, ErrOwnerMismatch) {
				t.Fatalf("foreign owner error=%v", err)
			}
			sameOwnerConflict := input
			sameOwnerConflict.UID = "exclusive-c"
			if _, err := repo.CreateStream(context.Background(), sameOwnerConflict); !errors.Is(err, ErrEventConflict) {
				t.Fatalf("same-owner conflict error=%v", err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_streams WHERE session_id=?`, input.SessionID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("stream count=%d err=%v", count, err)
			}
		})
	}
}

func TestRepositoryStagedUserEventBecomesClaimableOnlyAfterAdmission(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	ctx := context.Background()
	streamInput := CreateStreamInput{
		UID: "im-stage", OwnerID: "owner-a", ExternalID: "im:wechat:stage", SessionID: "im:wechat:stage",
		Kind: StreamKindStandalone, Epoch: 1,
	}
	if _, err := repo.CreateStream(ctx, streamInput); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ActivateDeliveryRoute(ctx, streamInput.OwnerID, streamInput.UID, streamInput.SessionID); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"text":"staged"}`)
	staged, err := repo.StageUserEvent(ctx, AppendUserEventInput{
		StreamUID: streamInput.UID, OwnerID: streamInput.OwnerID, ClientMessageID: "input-1", PayloadJSON: payload,
	})
	if err != nil || !staged.Created || staged.Admitted || staged.Event.Type != "user_message_staged" {
		t.Fatalf("staged=%#v err=%v", staged, err)
	}
	if next, err := repo.ClaimNextRunner(ctx, ClaimNextRunnerInput{RunnerID: "before", TTL: time.Minute}); err != nil || next.Claimed {
		t.Fatalf("pre-admission claim=%#v err=%v", next, err)
	}
	replayed, err := repo.StageUserEvent(ctx, AppendUserEventInput{
		StreamUID: streamInput.UID, OwnerID: streamInput.OwnerID, ClientMessageID: "input-1", PayloadJSON: payload,
	})
	if err != nil || replayed.Created || replayed.Admitted {
		t.Fatalf("replayed stage=%#v err=%v", replayed, err)
	}
	if _, admitted, err := repo.AdmitUserEvent(ctx, AdmitUserEventInput{
		StreamUID: streamInput.UID, OwnerID: streamInput.OwnerID, ClientMessageID: "input-1",
	}); err != nil || !admitted {
		t.Fatalf("admitted=%t err=%v", admitted, err)
	}
	if _, admitted, err := repo.AdmitUserEvent(ctx, AdmitUserEventInput{
		StreamUID: streamInput.UID, OwnerID: streamInput.OwnerID, ClientMessageID: "input-1",
	}); err != nil || admitted {
		t.Fatalf("duplicate admission=%t err=%v", admitted, err)
	}
	stream, err := repo.GetStream(ctx, streamInput.UID, streamInput.OwnerID)
	if err != nil || stream.InputRevision != 1 {
		t.Fatalf("stream=%#v err=%v", stream, err)
	}
	if next, err := repo.ClaimNextRunner(ctx, ClaimNextRunnerInput{RunnerID: "after", TTL: time.Minute}); err != nil || !next.Claimed {
		t.Fatalf("post-admission claim=%#v err=%v", next, err)
	}
	if _, err := repo.StageUserEvent(ctx, AppendUserEventInput{
		StreamUID: streamInput.UID, OwnerID: streamInput.OwnerID, ClientMessageID: "input-1", PayloadJSON: []byte(`{"text":"conflict"}`),
	}); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("payload conflict error=%v", err)
	}
}

func TestRepositoryStagedAdmissionIsPrefixOrderedAndSystemOwned(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	ctx := context.Background()
	stream := CreateStreamInput{
		UID: "im-prefix", OwnerID: "owner-a", ExternalID: "im:wechat:prefix", SessionID: "im:wechat:prefix",
		Kind: StreamKindStandalone, Epoch: 1,
	}
	if _, err := repo.CreateStream(ctx, stream); err != nil {
		t.Fatal(err)
	}
	if staged, err := repo.StageUserEvent(ctx, AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "input-a",
		PayloadJSON: []byte(`{"text":"input-a"}`),
	}); err != nil || !staged.Created {
		t.Fatalf("stage input-a=%#v err=%v", staged, err)
	}
	if staged, err := repo.StageUserEvent(ctx, AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "input-b",
		PayloadJSON: []byte(`{"text":"input-b"}`),
	}); !errors.Is(err, ErrEventConflict) || staged.Created {
		t.Fatalf("out-of-order stage=%#v err=%v", staged, err)
	}
	for _, id := range []string{"input-a", "input-b"} {
		if id == "input-b" {
			if staged, err := repo.StageUserEvent(ctx, AppendUserEventInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: id,
				PayloadJSON: []byte(`{"text":"input-b"}`),
			}); err != nil || !staged.Created {
				t.Fatalf("stage %s=%#v err=%v", id, staged, err)
			}
		}
		if _, admitted, err := repo.AdmitUserEvent(ctx, AdmitUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: id,
		}); err != nil || !admitted {
			t.Fatalf("admit %s=%t err=%v", id, admitted, err)
		}
	}

	claimed, err := repo.ClaimRunner(ctx, ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-forge", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if staged, err := repo.StageUserEvent(ctx, AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "forged-stage",
		PayloadJSON: []byte(`{"text":"forged"}`),
	}); err != nil {
		t.Fatal(err)
	} else if _, err := db.Exec(`UPDATE transcript_events SET runner_attempt=? WHERE stream_uid=? AND event_id=?`, claimed.Claim.Attempt, stream.UID, staged.Event.EventID); err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := repo.AdmitUserEvent(ctx, AdmitUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "forged-stage",
	}); !errors.Is(err, ErrEventConflict) || admitted {
		t.Fatalf("runner-attributed admission admitted=%t err=%v", admitted, err)
	}
}

func TestImmediateTransactionCreatesFrameStreamWithHistoricalPayloadContext(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-history','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES
		('frame-history','project-history','frame-history'),('frame-other','project-history','frame-other')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES
		('history-assistant','frame-history',1,'assistant_message','{"role":"assistant","content":"prior"}',?),
		('history-system','frame-history',2,'system_message','{"role":"system","content":"notice"}',?),
		('history-other','frame-other',1,'assistant_message','{"role":"assistant","content":"foreign frame"}',?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err := repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		stream, err := tx.CreateStream(ctx, CreateStreamInput{
			UID: "frame:frame-history", OwnerID: "owner-a", ExternalID: "frame-history", SessionID: "frame-history",
			Kind: StreamKindFrameRef, ProjectID: "project-history", RootFrameID: "frame-history", FrameID: "frame-history", Epoch: 1,
		})
		if err != nil {
			return err
		}
		_, _, created, err := tx.AppendFrameUserEvent(ctx, AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "history-user",
			FrameEventID: "history-user-event", MessageUUID: "history-user-message", Text: "new work",
			Destinations: []string{"ws"},
		})
		if err != nil || !created {
			return fmt.Errorf("append new input created=%t: %w", created, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.GetStream(ctx, "frame:frame-history", "owner-a")
	if err != nil || stream.InputRevision != 1 || stream.ConsumedInputRevision != 0 {
		t.Fatalf("stream=%#v err=%v", stream, err)
	}
	events, err := repo.ListProjectedEvents(ctx, ListProjectedEventsInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 10})
	if err != nil || len(events) != 3 || events[0].Event.Type != "history_assistant_message" ||
		events[0].Event.Source != EventSourcePayload || events[1].Event.Type != "history_system_message" ||
		events[1].Event.Source != EventSourcePayload || events[2].Event.Type != "user_message" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	err = repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		_, err := tx.AppendHistoricalFrameReferences(ctx, stream.UID, stream.OwnerID, []string{"history-other"})
		return err
	})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("cross-frame history error=%v", err)
	}
}

func TestRepositoryAppendFrameUserEventIsAtomicIdempotentAndOwnerScoped(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame','project','frame')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner-a", ExternalID: "frame", SessionID: "frame",
		Kind: StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	input := AppendFrameUserEventInput{
		StreamUID: "frame:frame", OwnerID: "owner-a", ClientMessageID: "client-1",
		FrameEventID: "frame-event-1", MessageUUID: "message-1", Text: "run the analysis",
		Destinations: []string{"ws"},
	}
	event, frameEvent, created, err := repo.AppendFrameUserEvent(context.Background(), input)
	if err != nil || !created || event.Source != EventSourcePayload || event.FrameEventID != nil ||
		frameEvent.ID != "transcript:frame:frame:1" {
		t.Fatalf("append event=%#v frame=%#v created=%t err=%v", event, frameEvent, created, err)
	}
	if len(event.PayloadJSON) == 0 || frameEvent.FrameID != "frame" || frameEvent.Sequence != 1 || frameEvent.Type != "user_message" ||
		!json.Valid(frameEvent.PayloadJSON) || !strings.Contains(string(frameEvent.PayloadJSON), "run the analysis") {
		t.Fatalf("unexpected frame-backed event=%#v frame=%#v", event, frameEvent)
	}
	again, againFrame, created, err := repo.AppendFrameUserEvent(context.Background(), input)
	if err != nil || created || again.EventID != event.EventID || againFrame.ID != frameEvent.ID {
		t.Fatalf("idempotent append event=%#v frame=%#v created=%t err=%v", again, againFrame, created, err)
	}
	conflict := input
	conflict.Text = "different"
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), conflict); !errors.Is(err, ErrEventConflict) || created {
		t.Fatalf("conflicting append created=%t err=%v", created, err)
	}
	foreign := input
	foreign.OwnerID = "owner-b"
	foreign.ClientMessageID = "client-foreign"
	foreign.FrameEventID = "frame-event-foreign"
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), foreign); !errors.Is(err, ErrOwnerMismatch) || created {
		t.Fatalf("foreign append created=%t err=%v", created, err)
	}
	failed := input
	failed.ClientMessageID = "client-no-route"
	failed.FrameEventID = "frame-event-no-route"
	failed.MessageUUID = "message-no-route"
	failed.Destinations = []string{"im:missing"}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), failed); !errors.Is(err, ErrDeliveryRouteInactive) || created {
		t.Fatalf("missing route append created=%t err=%v", created, err)
	}
	var events, frames, revision int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid='frame:frame'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame'`).Scan(&frames); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT input_revision FROM transcript_streams WHERE stream_uid='frame:frame'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if events != 1 || frames != 0 || revision != 1 {
		t.Fatalf("atomic state events=%d frames=%d revision=%d", events, frames, revision)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), "frame:frame", "owner-a")
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	if intent.FrameID != "frame" || intent.Revision != 1 || intent.SourceEventID != input.ClientMessageID ||
		intent.SourceMessageID != input.MessageUUID || intent.Origin != "user" || intent.Language != "en" ||
		intent.Text != input.Text {
		t.Fatalf("unexpected active task intent %#v", intent)
	}
	if _, found, err := repo.GetActiveFrameTaskIntent(context.Background(), "frame:frame", "owner-b"); !errors.Is(err, ErrOwnerMismatch) || found {
		t.Fatalf("foreign active task intent found=%t err=%v", found, err)
	}
	followup := input
	followup.ClientMessageID = "client-2"
	followup.FrameEventID = "frame-event-2"
	followup.MessageUUID = "message-2"
	followup.Text = "继续完成专利检索"
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), followup); err != nil || !created {
		t.Fatalf("append follow-up created=%t err=%v", created, err)
	}
	intent, found, err = repo.GetActiveFrameTaskIntent(context.Background(), "frame:frame", "owner-a")
	if err != nil || !found || intent.Revision != 2 || intent.SourceEventID != followup.ClientMessageID ||
		intent.SourceMessageID != followup.MessageUUID || intent.Language != "zh" || intent.Text != followup.Text {
		t.Fatalf("follow-up active task intent=%#v found=%t err=%v", intent, found, err)
	}
	continuation := input
	continuation.ClientMessageID = "client-continue"
	continuation.FrameEventID = "frame-event-continue"
	continuation.MessageUUID = "message-continue"
	continuation.Text = "继续运行"
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), continuation); err != nil || !created {
		t.Fatalf("append continuation created=%t err=%v", created, err)
	}
	intent, found, err = repo.GetActiveFrameTaskIntent(context.Background(), "frame:frame", "owner-a")
	if err != nil || !found || intent.Revision != 2 || intent.SourceEventID != followup.ClientMessageID || intent.Text != followup.Text {
		t.Fatalf("continuation replaced canonical task intent=%#v found=%t err=%v", intent, found, err)
	}
	intents, err := repo.ListActiveFrameTaskIntents(context.Background(), "frame:frame", "owner-a")
	if err != nil || len(intents) != 2 || intents[0].SourceEventID != input.ClientMessageID ||
		intents[1].SourceEventID != followup.ClientMessageID {
		t.Fatalf("durable task intent history=%#v err=%v", intents, err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), input); err != nil || created {
		t.Fatalf("old idempotent append created=%t err=%v", created, err)
	}
	intent, found, err = repo.GetActiveFrameTaskIntent(context.Background(), "frame:frame", "owner-a")
	if err != nil || !found || intent.Revision != 2 || intent.SourceEventID != followup.ClientMessageID {
		t.Fatalf("old retry regressed active task intent=%#v found=%t err=%v", intent, found, err)
	}
	explicitResponse := input
	explicitResponse.ClientMessageID = "client-response"
	explicitResponse.FrameEventID = "frame-event-response"
	explicitResponse.MessageUUID = "message-response"
	explicitResponse.Text = "Approved."
	explicitResponse.MessageOrigin = "input_response"
	responseEvent, responseFrame, created, err := repo.AppendFrameUserEvent(context.Background(), explicitResponse)
	if err != nil || !created || responseEvent.Type != "user_input_response" || responseFrame.Type != "user_input_response" {
		t.Fatalf("explicit response event=%#v frame=%#v created=%t err=%v", responseEvent, responseFrame, created, err)
	}
	intent, found, err = repo.GetActiveFrameTaskIntent(context.Background(), "frame:frame", "owner-a")
	if err != nil || !found || intent.Revision != 2 || intent.SourceEventID != followup.ClientMessageID {
		t.Fatalf("explicit response changed task intent=%#v found=%t err=%v", intent, found, err)
	}
	conflictingOrigin := explicitResponse
	conflictingOrigin.MessageOrigin = "task_intent"
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), conflictingOrigin); !errors.Is(err, ErrEventConflict) || created {
		t.Fatalf("origin conflict created=%t err=%v", created, err)
	}
	invalidOrigin := explicitResponse
	invalidOrigin.ClientMessageID = "invalid-origin"
	invalidOrigin.FrameEventID = "invalid-origin-event"
	invalidOrigin.MessageUUID = "invalid-origin-message"
	invalidOrigin.MessageOrigin = "system"
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), invalidOrigin); err == nil || created {
		t.Fatalf("invalid origin created=%t err=%v", created, err)
	}
}

func TestRepositoryAppendFrameUserEventCanDeferTerminalFrameActivation(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-deferred','owner')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id,status)
		VALUES('frame-deferred','project-deferred','frame-deferred','failed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:deferred", OwnerID: "owner", ExternalID: "frame-deferred", SessionID: "frame-deferred",
		Kind: StreamKindFrameRef, ProjectID: "project-deferred", RootFrameID: "frame-deferred", FrameID: "frame-deferred", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	input := AppendFrameUserEventInput{
		StreamUID: "frame:deferred", OwnerID: "owner", ClientMessageID: "deferred-client",
		FrameEventID: "deferred-frame-event", MessageUUID: "deferred-message", Text: "resume after recovery",
		Destinations: []string{"ws"}, DeferFrameActivation: true,
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), input); err != nil || !created {
		t.Fatalf("deferred append created=%t err=%v", created, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM frames WHERE id='frame-deferred'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("deferred append activated frame: status=%q", status)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), input); err != nil || created {
		t.Fatalf("deferred append retry created=%t err=%v", created, err)
	}
	if err := db.QueryRow(`SELECT status FROM frames WHERE id='frame-deferred'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("deferred append retry activated frame: status=%q", status)
	}
}

func TestRepositoryEnsureActiveFrameTaskIntentRepairsAllowlistedLegacyFrameEvent(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project','owner')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame','project','frame')`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	forceLegacyFrameAuthorityForTest(t, db, stream)
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: "frame:frame", OwnerID: "owner", ClientMessageID: "client",
		FrameEventID: "frame-event", MessageUUID: "message", Text: "restore canonical intent", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append created=%t err=%v", created, err)
	}
	if _, err := db.Exec(`DELETE FROM frame_task_intents WHERE frame_id='frame'`); err != nil {
		t.Fatal(err)
	}
	intent, found, err := repo.EnsureActiveFrameTaskIntent(context.Background(), "frame:frame", "owner")
	if err != nil || !found || intent.SourceEventID != "frame-event" || intent.SourceMessageID != "message" ||
		intent.Text != "restore canonical intent" || intent.Origin != "user" || intent.Language != "en" {
		t.Fatalf("repaired intent=%#v found=%t err=%v", intent, found, err)
	}
	if _, err := db.Exec(`UPDATE frame_events SET payload='{"role":"system","text":"forged","messageUuid":"message"}' WHERE id='frame-event'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM frame_task_intents WHERE frame_id='frame'`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.EnsureActiveFrameTaskIntent(context.Background(), "frame:frame", "owner"); !errors.Is(err, ErrEventConflict) || found {
		t.Fatalf("forged legacy intent found=%t err=%v", found, err)
	}
}

func TestRepositoryPauseRunnerForInputWaitsForNewRevisionAndResumesCheckpoint(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 23, 5, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project','owner')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame','project','frame')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: "frame:frame", OwnerID: "owner", ClientMessageID: "client-1", FrameEventID: "frame-event-1",
		MessageUUID: "message-1", Text: "ask before continuing", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append initial input created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "frame:frame", OwnerID: "owner", RunnerID: "runner-1", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	pauseInput := AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "pause-1", Phase: RunnerPhaseWaitingUser, Resumable: true,
		PayloadJSON: []byte(`{"status":"awaiting_user_response","detail":"choose structure"}`), Destinations: []string{"ws"},
	}
	pause, _, created, err := repo.PauseRunnerForInput(context.Background(), pauseInput)
	if err != nil || !created || pause.Sequence <= 0 {
		t.Fatalf("pause=%#v created=%t err=%v", pause, created, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), "frame:frame", "owner", 1)
	if err != nil || state.Status != "running" || state.Phase != RunnerPhaseWaitingUser || !state.ExpiresAt.Equal(now) {
		t.Fatalf("paused state=%#v err=%v", state, err)
	}
	stream, err := repo.GetStream(context.Background(), "frame:frame", "owner")
	if err != nil || stream.InputRevision != 1 || stream.ConsumedInputRevision != 1 {
		t.Fatalf("paused stream=%#v err=%v", stream, err)
	}
	if next, err := repo.ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{RunnerID: "runner-2", TTL: time.Minute}); err != nil || next.Claimed {
		t.Fatalf("claim without answer=%#v err=%v", next, err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='awaiting_user_response' WHERE id='frame'`); err != nil {
		t.Fatal(err)
	}
	answerEvent, answerFrameEvent, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: "frame:frame", OwnerID: "owner", ClientMessageID: "client-2", FrameEventID: "frame-event-2",
		MessageUUID: "message-2", Text: "use 5FQD", Destinations: []string{"ws"},
	})
	if err != nil || !created || answerEvent.Type != "user_input_response" || answerFrameEvent.Type != "user_input_response" ||
		!strings.Contains(string(answerFrameEvent.PayloadJSON), `"messageOrigin":"input_response"`) {
		t.Fatalf("append answer created=%t err=%v", created, err)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), "frame:frame", "owner")
	if err != nil || !found || intent.Revision != 1 || intent.Text != "ask before continuing" {
		t.Fatalf("active intent after answer=%#v found=%t err=%v", intent, found, err)
	}
	if retry, retryFrame, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: "frame:frame", OwnerID: "owner", ClientMessageID: "client-2", FrameEventID: "frame-event-2",
		MessageUUID: "message-2", Text: "use 5FQD", Destinations: []string{"ws"},
	}); err != nil || created || retry.Type != "user_input_response" || retryFrame.Type != "user_input_response" {
		t.Fatalf("retry answer event=%#v frame=%#v created=%t err=%v", retry, retryFrame, created, err)
	}
	next, err := repo.ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{RunnerID: "runner-2", TTL: time.Minute})
	if err != nil || !next.Claimed || next.Claim.Attempt != claimed.Claim.Attempt || next.Claim.ResumeSource != ResumeSourceCheckpoint ||
		next.Claim.ResumeCheckpoint != pause.Sequence || next.Claim.ClaimedInputRevision != 2 {
		t.Fatalf("resumed claim=%#v err=%v", next, err)
	}
	if _, _, _, err := repo.PauseRunnerForInput(context.Background(), pauseInput); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("stale pause error=%v", err)
	}
}

func TestRepositoryClaimNextFrameRunnerUsesCanonicalPendingOrderAndSingleWinner(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-a','owner-a'),('project-b','owner-b')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
		('frame-a','project-a','frame-a','processing'),('frame-b','project-b','frame-b','processing')`); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return clock }
	for _, value := range []struct {
		uid, owner, project, frame string
	}{
		{uid: "stream-a", owner: "owner-a", project: "project-a", frame: "frame-a"},
		{uid: "stream-b", owner: "owner-b", project: "project-b", frame: "frame-b"},
	} {
		if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
			UID: value.uid, OwnerID: value.owner, ExternalID: value.frame, SessionID: value.frame,
			Kind: StreamKindFrameRef, ProjectID: value.project, RootFrameID: value.frame, FrameID: value.frame, Epoch: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
			StreamUID: value.uid, OwnerID: value.owner, ClientMessageID: "client-" + value.frame,
			FrameEventID: "event-" + value.frame, MessageUUID: "message-" + value.frame,
			Text: "work " + value.frame, Destinations: []string{"ws"},
		}); err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", value.frame, created, err)
		}
		clock = clock.Add(time.Second)
	}

	first, err := repo.ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{RunnerID: "runner", TTL: time.Minute})
	if err != nil || !first.Claimed || first.Stream.UID != "stream-a" || first.Claim.Attempt != 1 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := repo.ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{RunnerID: "runner", TTL: time.Minute})
	if err != nil || !second.Claimed || second.Stream.UID != "stream-b" || second.Claim.Attempt != 1 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	none, err := repo.ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{RunnerID: "other", TTL: time.Minute})
	if err != nil || none.Claimed {
		t.Fatalf("none=%#v err=%v", none, err)
	}
}

func TestRepositoryClaimNextRunnerIncludesStandaloneWithoutChangingFrameOnlySelector(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "standalone-next", OwnerID: "owner-a", ExternalID: "im:wechat:next",
		SessionID: "im:wechat:next", Kind: StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "standalone-next", OwnerID: "owner-a", ClientMessageID: "user-next",
		PayloadJSON: []byte(`{"text":"run without an explicit session id"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ActivateDeliveryRoute(context.Background(), "owner-a", "standalone-next", "im:wechat:next"); err != nil {
		t.Fatal(err)
	}
	frameOnly, err := repo.ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{
		RunnerID: "frame-only", TTL: time.Minute,
	})
	if err != nil || frameOnly.Claimed {
		t.Fatalf("frame-only selector claimed standalone: %#v err=%v", frameOnly, err)
	}
	next, err := repo.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{
		RunnerID: "all-runnable", TTL: time.Minute,
	})
	if err != nil || !next.Claimed || next.Stream.UID != "standalone-next" || next.Stream.Kind != StreamKindStandalone {
		t.Fatalf("runnable selector=%#v err=%v", next, err)
	}
}

func TestRepositoryClaimNextFrameRunnerHasOneConcurrentWinner(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project','owner')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id,status) VALUES('frame','project','frame','processing')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream", OwnerID: "owner", ExternalID: "frame", SessionID: "frame", Kind: StreamKindFrameRef,
		ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: "stream", OwnerID: "owner", ClientMessageID: "client", FrameEventID: "event",
		MessageUUID: "message", Text: "concurrent work", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append created=%t err=%v", created, err)
	}

	results := make(chan ClaimNextFrameRunnerResult, 32)
	errorsCh := make(chan error, 32)
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			result, err := NewRepository(db).ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{
				RunnerID: fmt.Sprintf("runner-%02d", index), TTL: time.Minute,
			})
			results <- result
			errorsCh <- err
		}(index)
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	winners := 0
	for result := range results {
		if result.Claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent winners=%d, want 1", winners)
	}
}

func TestRepositoryClaimNextFrameRunnerResumesExpiredAttemptFromCheckpoint(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-resume','owner-resume')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id,status) VALUES('frame-resume','project-resume','frame-resume','processing')`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 1, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-resume", OwnerID: "owner-resume", ExternalID: "frame-resume", SessionID: "frame-resume",
		Kind: StreamKindFrameRef, ProjectID: "project-resume", RootFrameID: "frame-resume", FrameID: "frame-resume", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: "stream-resume", OwnerID: "owner-resume", ClientMessageID: "user-resume",
		FrameEventID: "frame-event-resume", MessageUUID: "message-resume", Text: "resume after crash", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append created=%t err=%v", created, err)
	}
	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-resume", OwnerID: "owner-resume", RunnerID: "runner-crashed", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	checkpoint, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "checkpoint-resume", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"status":"running","detail":"durable step"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created || checkpoint.Sequence <= 0 {
		t.Fatalf("checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}
	now = now.Add(2 * time.Minute)
	resumed, err := repo.ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{
		RunnerID: "runner-restarted", TTL: time.Minute,
	})
	if err != nil || !resumed.Claimed || resumed.Claim.Attempt != first.Claim.Attempt ||
		resumed.Claim.ResumeSource != ResumeSourceCheckpoint || resumed.Claim.ResumeCheckpoint != checkpoint.Sequence {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
	var resumeAttempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid='stream-resume'`).Scan(&resumeAttempts); err != nil {
		t.Fatal(err)
	}
	if resumeAttempts != 1 {
		t.Fatalf("resume attempts=%d, want one stable parent attempt", resumeAttempts)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "finish-stale-lease", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("stale lease finish error=%v, want ErrClaimStale", err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: resumed.Claim, ClientMessageID: "finish-resumed", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id,status) VALUES('frame-retry','project-resume','frame-retry','processing')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-retry", OwnerID: "owner-resume", ExternalID: "frame-retry", SessionID: "frame-retry",
		Kind: StreamKindFrameRef, ProjectID: "project-resume", RootFrameID: "frame-retry", FrameID: "frame-retry", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: "stream-retry", OwnerID: "owner-resume", ClientMessageID: "user-retry",
		FrameEventID: "frame-event-retry", MessageUUID: "message-retry", Text: "retry after crash", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append retry created=%t err=%v", created, err)
	}
	crashed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-retry", OwnerID: "owner-resume", RunnerID: "runner-no-checkpoint", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !crashed.Claimed {
		t.Fatalf("crashed=%#v err=%v", crashed, err)
	}
	now = now.Add(2 * time.Minute)
	retried, err := repo.ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{
		RunnerID: "runner-retry", TTL: time.Minute,
	})
	if err != nil || retried.Claimed {
		t.Fatalf("automatic retry=%#v err=%v, want no claim without a durable checkpoint", retried, err)
	}
	var retryAttempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid='stream-retry'`).Scan(&retryAttempts); err != nil {
		t.Fatal(err)
	}
	if retryAttempts != 1 {
		t.Fatalf("retry attempts=%d, want no automatic parent attempt", retryAttempts)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: "stream-retry", OwnerID: "owner-resume", ClientMessageID: "user-retry-explicit-second",
		FrameEventID: "frame-event-retry-explicit-second", MessageUUID: "message-retry-explicit-second",
		Text: "explicitly dispatch a second task", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append explicit second task created=%t err=%v", created, err)
	}
	explicitSecond, err := repo.ClaimNextFrameRunner(context.Background(), ClaimNextFrameRunnerInput{
		RunnerID: "runner-explicit-second", TTL: time.Minute,
	})
	if err != nil || !explicitSecond.Claimed || explicitSecond.Stream.UID != "stream-retry" ||
		explicitSecond.Claim.Attempt != 2 || explicitSecond.Claim.ClaimedInputRevision != 2 ||
		explicitSecond.Claim.ResumeSource != ResumeSourceFresh {
		t.Fatalf("explicit second task=%#v err=%v", explicitSecond, err)
	}
}

func TestRepositoryCheckpointLeaseRecoveryHasOneWinnerAndOneStableAttempt(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-concurrent-recovery", "owner-a")
	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-concurrent-recovery", OwnerID: "owner-a", RunnerID: "runner-original",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	checkpoint, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "checkpoint-recovery", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"status":"running"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	const workers = 16
	start := make(chan struct{})
	results := make(chan ClaimRunnerResult, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			result, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
				StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID,
				RunnerID: fmt.Sprintf("runner-recovery-%02d", index), TTL: time.Minute,
				ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
			})
			results <- result
			errs <- err
		}(index)
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	winners := 0
	for result := range results {
		if result.Claimed {
			winners++
			if result.Claim.Attempt != first.Claim.Attempt || result.Claim.ClaimToken == first.Claim.ClaimToken {
				t.Fatalf("winner=%#v", result)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("recovery winners=%d, want 1", winners)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?`, first.Claim.StreamUID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want one stable parent attempt", attempts)
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "stale-after-recovery", Phase: RunnerPhaseExecuting,
		PayloadJSON: []byte(`{"status":"stale"}`),
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("stale claim error=%v", err)
	}
}

func TestRepositoryClaimTokenPersistsByDigestAndFencesReclaim(t *testing.T) {
	repo, db, dsn := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-claim", "owner-a")

	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-claim", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed || first.Claim.Attempt != 1 || first.Claim.ClaimToken == "" {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT hex(claim_token_sha256) FROM transcript_runner_attempts WHERE stream_uid='stream-claim' AND attempt=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "" || stored == first.Claim.ClaimToken {
		t.Fatalf("claim token was not stored as a digest: %q", stored)
	}

	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewRepository(reopenedDB)
	reopened.now = func() time.Time { return now }
	checkpoint, _, _, err := reopened.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "checkpoint-1", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"status":"running"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatalf("reopened append error=%v", err)
	}

	now = now.Add(2 * time.Minute)
	second, err := reopened.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-claim", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !second.Claimed || second.Claim.Attempt != first.Claim.Attempt || second.Claim.ClaimToken == first.Claim.ClaimToken {
		t.Fatalf("reclaim=%#v err=%v", second, err)
	}
	if _, _, _, err := reopened.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "stale", Phase: RunnerPhaseExecuting,
		PayloadJSON: []byte(`{"status":"stale"}`),
	}); !errors.Is(err, ErrClaimStale) || stringsContain(err, first.Claim.ClaimToken) {
		t.Fatalf("stale append error=%v", err)
	}
}

func TestRepositoryFinishIsAtomicIdempotentAndPreservesNewInput(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-finish", "owner-a")
	if _, _, err := repo.ActivateDeliveryRoute(context.Background(), "owner-a", "stream-finish", "im:route-a"); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-finish", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "stream-finish", OwnerID: "owner-a", ClientMessageID: "user-2",
		PayloadJSON: []byte(`{"text":"newer work"}`), Destinations: []string{"ws", "im:route-a"},
	}); err != nil || !created {
		t.Fatalf("append newer input created=%t err=%v", created, err)
	}

	input := FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-1", Status: "completed",
		PayloadJSON: []byte(`{"summary":"done"}`), Destinations: []string{"ws", "im:route-a"},
	}
	event, receipt, created, err := repo.FinishRunner(context.Background(), input)
	if err != nil || !created || receipt.EventID != event.EventID || receipt.Status != "completed" {
		t.Fatalf("finish event=%#v receipt=%#v created=%t err=%v", event, receipt, created, err)
	}
	againEvent, againReceipt, created, err := repo.FinishRunner(context.Background(), input)
	if err != nil || created || againEvent.EventID != event.EventID || againReceipt != receipt {
		t.Fatalf("idempotent finish event=%#v receipt=%#v created=%t err=%v", againEvent, againReceipt, created, err)
	}
	conflict := input
	conflict.Destinations = []string{"ws"}
	if _, _, created, err := repo.FinishRunner(context.Background(), conflict); !errors.Is(err, ErrEventConflict) || created {
		t.Fatalf("destination conflict created=%t err=%v", created, err)
	}
	stream, err := repo.GetStream(context.Background(), "stream-finish", "owner-a")
	if err != nil || stream.InputRevision != 2 || stream.ConsumedInputRevision != 1 {
		t.Fatalf("stream watermark=%#v err=%v", stream, err)
	}
	next, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-finish", OwnerID: "owner-a", RunnerID: "runner-b", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !next.Claimed || next.Claim.Attempt != 2 || next.Claim.ClaimedInputRevision != 2 {
		t.Fatalf("next claim=%#v err=%v", next, err)
	}
	var receipts, intents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid='stream-finish'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid='stream-finish' AND publication_seq=?`, event.PublicationSeq).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || intents != 2 {
		t.Fatalf("receipts=%d intents=%d", receipts, intents)
	}
}

func TestRepositoryClaimHasOneWinnerAcrossConnections(t *testing.T) {
	repo, _, dsn := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-race", "owner-a")
	start := make(chan struct{})
	results := make(chan ClaimRunnerResult, 32)
	errs := make(chan error, 32)
	var workers sync.WaitGroup
	for index := 0; index < 32; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			<-start
			result, err := NewRepository(db).ClaimRunner(context.Background(), ClaimRunnerInput{
				StreamUID: "stream-race", OwnerID: "owner-a", RunnerID: fmt.Sprintf("runner-%d", index), TTL: time.Minute,
				ResumeSource: ResumeSourceFresh,
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent claim error=%v", err)
	}
	winners := 0
	for result := range results {
		if result.Claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners=%d, want 1", winners)
	}
}

func TestRepositoryFinishRollsBackAuthorityWhenDeliveryIntentFails(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-rollback", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-rollback", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, err := db.Exec(`DROP TABLE transcript_delivery_intents`); err != nil {
		t.Fatal(err)
	}
	input := FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-rollback", Status: "completed",
		PayloadJSON: []byte(`{"summary":"must roll back"}`), Destinations: []string{"ws"},
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), input); err == nil || created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	var status string
	var finishEvents, receipts int
	if err := db.QueryRow(`SELECT status FROM transcript_runner_attempts WHERE stream_uid='stream-rollback' AND attempt=1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid='stream-rollback' AND event_type='runner_finished'`).Scan(&finishEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid='stream-rollback'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if status != "running" || finishEvents != 0 || receipts != 0 {
		t.Fatalf("rollback status=%q finishEvents=%d receipts=%d", status, finishEvents, receipts)
	}
}

func TestRepositoryFrameReferenceNeverCopiesFramePayload(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-frame", "owner-a")
	frameEventID := "frame-event-42"
	if _, err := db.Exec(`
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?, 'frame-a', 42, 'assistant_message', '{"text":"canonical"}', ?)`, frameEventID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	event, created, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "frame-ref-1", Type: "assistant_message",
		Source: EventSourceFrameRef, FrameEventID: &frameEventID, Destinations: []string{"ws"},
	})
	if err != nil || !created || event.FrameEventID == nil || *event.FrameEventID != frameEventID || len(event.PayloadJSON) != 0 {
		t.Fatalf("frame event=%#v created=%t err=%v", event, created, err)
	}
	var payload []byte
	if err := db.QueryRow(`SELECT payload_json FROM transcript_events WHERE stream_uid='stream-frame' AND event_id=?`, event.EventID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		t.Fatalf("frame payload was copied: %q", payload)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame-b','project-a','root-a')`); err != nil {
		t.Fatal(err)
	}
	foreignEventID := "frame-event-foreign"
	if _, err := db.Exec(`
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?, 'frame-b', 1, 'assistant_message', '{}', ?)`, foreignEventID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, invalidID := range []string{"frame-event-missing", foreignEventID} {
		if _, created, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
			Claim: claim, ClientMessageID: "invalid-" + invalidID, Type: "assistant_message",
			Source: EventSourceFrameRef, FrameEventID: &invalidID, Destinations: []string{"ws"},
		}); !errors.Is(err, ErrEventConflict) || created {
			t.Fatalf("invalid frame event %q created=%t err=%v", invalidID, created, err)
		}
	}
	if _, created, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "invalid-type", Type: "tool_result",
		Source: EventSourceFrameRef, FrameEventID: &frameEventID, Destinations: []string{"ws"},
	}); !errors.Is(err, ErrEventConflict) || created {
		t.Fatalf("mismatched frame event type created=%t err=%v", created, err)
	}
	invalidPayloadID := "frame-event-invalid-payload"
	if _, err := db.Exec(`
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?, 'frame-a', 43, 'assistant_message', 'not-json', ?)`, invalidPayloadID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "invalid-payload", Type: "assistant_message",
		Source: EventSourceFrameRef, FrameEventID: &invalidPayloadID, Destinations: []string{"ws"},
	}); !errors.Is(err, ErrEventConflict) || created {
		t.Fatalf("invalid frame payload created=%t err=%v", created, err)
	}
}

func TestRepositoryGenericRunnerEventsCannotBypassTypedAuthorities(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-reserved-events", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-reserved-events", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	for _, eventType := range []string{"user_message", "user_message_staged", "user_input_response", "runner_checkpoint", "runner_finished", "runner_reclaimed"} {
		if _, created, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
			Claim: claimed.Claim, ClientMessageID: "forged-" + eventType, Type: eventType,
			Source: EventSourcePayload, PayloadJSON: []byte(`{"forged":true}`), Destinations: []string{"ws"},
		}); !errors.Is(err, ErrReservedEventType) || created {
			t.Fatalf("type=%q created=%t err=%v", eventType, created, err)
		}
	}
	var events, receipts, checkpoints int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, claimed.Claim.StreamUID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, claimed.Claim.StreamUID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_checkpoints WHERE stream_uid=?`, claimed.Claim.StreamUID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if events != 1 || receipts != 0 || checkpoints != 0 {
		t.Fatalf("events=%d receipts=%d checkpoints=%d", events, receipts, checkpoints)
	}
}

func stringsContain(err error, value string) bool {
	return err != nil && value != "" && strings.Contains(err.Error(), value)
}
