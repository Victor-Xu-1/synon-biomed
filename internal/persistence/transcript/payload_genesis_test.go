package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func forceLegacyFrameAuthorityForTest(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, stream Stream) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`,
		stream.OwnerID, stream.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM transcript_payload_genesis_receipts WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id,updated_at)
		VALUES(?,?,?,?,1,'legacy_mixed_v1','legacy_frame_ref_v1',NULL,NULL,?)`,
		stream.OwnerID, stream.SessionID, stream.UID, stream.Epoch, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestFrameStreamPayloadGenesisIsDeterministicAndWritesNoLegacyMessage(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-genesis','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES
		('frame-genesis','project-genesis','frame-genesis')`); err != nil {
		t.Fatal(err)
	}
	input := CreateStreamInput{
		UID: "frame:frame-genesis", OwnerID: "owner-a", ExternalID: "frame-genesis", SessionID: "frame-genesis",
		Kind: StreamKindFrameRef, ProjectID: "project-genesis", RootFrameID: "frame-genesis",
		FrameID: "frame-genesis", Epoch: 1,
	}
	const workers = 32
	start := make(chan struct{})
	errorsByWorker := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := repo.CreateStream(context.Background(), input)
			errorsByWorker <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatal(err)
		}
	}
	authority, found, err := repo.GetFrameAuthorityBySession(context.Background(), "owner-a", "frame-genesis")
	if err != nil || !found || !authority.TranscriptPayloadActive() || authority.LegacyActivationActive() ||
		len(authority.ActivationID) != 0 || len(authority.GenesisID) != sha256.Size {
		t.Fatalf("authority=%#v found=%t err=%v", authority, found, err)
	}
	var receipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_payload_genesis_receipts
		WHERE stream_uid='frame:frame-genesis'`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("genesis receipts=%d err=%v", receipts, err)
	}
	appendInput := AppendFrameUserEventInput{
		StreamUID: input.UID, OwnerID: input.OwnerID, ClientMessageID: "client-genesis",
		FrameEventID: "legacy-event-must-not-exist", MessageUUID: "message-genesis", Text: "new analysis",
		Destinations: []string{"ws"},
	}
	event, projection, created, err := repo.AppendFrameUserEvent(context.Background(), appendInput)
	if err != nil || !created || event.Source != EventSourcePayload || event.FrameEventID != nil ||
		projection.ID != "transcript:frame:frame-genesis:1" {
		t.Fatalf("event=%#v projection=%#v created=%t err=%v", event, projection, created, err)
	}
	var legacyMessages int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame-genesis'
		AND event_type IN ('message','user_message','assistant_message','system_message','tool_use','tool_result','ask_user_answer')`).
		Scan(&legacyMessages); err != nil || legacyMessages != 0 {
		t.Fatalf("legacy messages=%d err=%v", legacyMessages, err)
	}
	again, _, created, err := repo.AppendFrameUserEvent(context.Background(), appendInput)
	if err != nil || created || again.EventID != event.EventID {
		t.Fatalf("retry event=%#v created=%t err=%v", again, created, err)
	}
}

func TestPayloadGenesisInputResponseDirectRetryIsIdempotent(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-response','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame-response','project-response','frame-response')`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-response", OwnerID: "owner-a", ExternalID: "frame-response", SessionID: "frame-response",
		Kind: StreamKindFrameRef, ProjectID: "project-response", RootFrameID: "frame-response",
		FrameID: "frame-response", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := AppendFrameInputResponseInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, FrameID: stream.FrameID,
		ClientMessageID: "input-response:stable", PayloadJSON: []byte(`{"role":"user","messageOrigin":"input_response","text":"resolved"}`),
		Destinations: []string{"ws"},
	}
	var first Event
	var firstCreated bool
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		var err error
		first, firstCreated, err = tx.AppendFrameInputResponse(context.Background(), input)
		return err
	}); err != nil || !firstCreated || first.Source != EventSourcePayload || first.FrameEventID != nil {
		t.Fatalf("first=%#v created=%t err=%v", first, firstCreated, err)
	}
	var repeated Event
	var repeatedCreated bool
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		var err error
		repeated, repeatedCreated, err = tx.AppendFrameInputResponse(context.Background(), input)
		return err
	}); err != nil || repeatedCreated || repeated.EventID != first.EventID || repeated.ClientMessageID != first.ClientMessageID {
		t.Fatalf("repeated=%#v created=%t err=%v", repeated, repeatedCreated, err)
	}
	current, err := repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || current.InputRevision != 1 {
		t.Fatalf("stream=%#v err=%v", current, err)
	}
	var transcriptResponses, frameResponses int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_input_response'`, stream.UID).
		Scan(&transcriptResponses); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=? AND event_type='user_input_response'`, stream.FrameID).
		Scan(&frameResponses); err != nil {
		t.Fatal(err)
	}
	if transcriptResponses != 1 || frameResponses != 0 {
		t.Fatalf("transcript responses=%d frame responses=%d", transcriptResponses, frameResponses)
	}
}

func TestFrameStreamWithPreseededMessagesImportsPayloadHistory(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-legacy','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES
		('frame-legacy','project-legacy','frame-legacy')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('legacy-user','frame-legacy',1,'user_message','{"role":"user","text":"prior"}',?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-legacy", OwnerID: "owner-a", ExternalID: "frame-legacy", SessionID: "frame-legacy",
		Kind: StreamKindFrameRef, ProjectID: "project-legacy", RootFrameID: "frame-legacy",
		FrameID: "frame-legacy", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	authority, found, err := repo.GetFrameAuthorityBySession(context.Background(), "owner-a", "frame-legacy")
	if err != nil || !found || !authority.TranscriptPayloadActive() || authority.LegacyActivationActive() ||
		len(authority.ActivationID) != 0 || len(authority.GenesisID) != sha256.Size {
		t.Fatalf("authority=%#v found=%t err=%v", authority, found, err)
	}
	var imported int64
	var sourceSHA []byte
	if err := db.QueryRow(`SELECT source_event_count,source_sha256 FROM transcript_payload_genesis_receipts
		WHERE stream_uid='frame:frame-legacy'`).Scan(&imported, &sourceSHA); err != nil || imported != 1 || len(sourceSHA) != sha256.Size {
		t.Fatalf("imported=%d source_sha=%x err=%v", imported, sourceSHA, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: "frame:frame-legacy", OwnerID: "owner-a", Limit: 10,
	})
	if err != nil || len(events) != 1 || events[0].Event.Type != "history_user_message" ||
		events[0].Event.Source != EventSourcePayload || events[0].Event.FrameEventID != nil ||
		string(events[0].ResolvedPayloadJSON) != `{"role":"user","text":"prior"}` {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	stream, err := repo.GetStream(context.Background(), "frame:frame-legacy", "owner-a")
	if err != nil || stream.InputRevision != 0 || stream.ConsumedInputRevision != 0 {
		t.Fatalf("stream=%#v err=%v", stream, err)
	}
	var intents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid='frame:frame-legacy'`).
		Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("delivery intents=%d err=%v", intents, err)
	}
}

func TestFrameStreamPayloadGenesisRejectsUnsupportedRichPreseedAtomically(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-rich','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES
		('frame-rich','project-rich','frame-rich')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('rich-assistant','frame-rich',1,'assistant_message',
		'{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"ask_user","input":{}}]}',?)`,
		time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-rich", OwnerID: "owner-a", ExternalID: "frame-rich", SessionID: "frame-rich",
		Kind: StreamKindFrameRef, ProjectID: "project-rich", RootFrameID: "frame-rich", FrameID: "frame-rich", Epoch: 1,
	})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("create error=%v", err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM transcript_streams WHERE stream_uid='frame:frame-rich'`,
		`SELECT COUNT(*) FROM transcript_frame_authority WHERE session_id='frame-rich'`,
		`SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE session_id='frame-rich'`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("query=%s count=%d err=%v", query, count, err)
		}
	}
}

func TestFrameStreamPayloadGenesisRejectsInvalidSourceSequenceAtomically(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-sequence','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame-sequence','project-sequence','frame-sequence');
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('invalid-sequence','frame-sequence',0,'user_message','{"role":"user","text":"prior"}',?)`,
		time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-sequence", OwnerID: "owner-a", ExternalID: "frame-sequence", SessionID: "frame-sequence",
		Kind: StreamKindFrameRef, ProjectID: "project-sequence", RootFrameID: "frame-sequence",
		FrameID: "frame-sequence", Epoch: 1,
	})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("create error=%v", err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM transcript_streams WHERE stream_uid='frame:frame-sequence'`,
		`SELECT COUNT(*) FROM transcript_frame_authority WHERE session_id='frame-sequence'`,
		`SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE session_id='frame-sequence'`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("query=%s count=%d err=%v", query, count, err)
		}
	}
}

func TestPayloadGenesisAuthorityRejectsDualProvenanceAndMutation(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-tamper','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES
		('frame-tamper','project-tamper','frame-tamper')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-tamper", OwnerID: "owner-a", ExternalID: "frame-tamper", SessionID: "frame-tamper",
		Kind: StreamKindFrameRef, ProjectID: "project-tamper", RootFrameID: "frame-tamper", FrameID: "frame-tamper", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_payload_genesis_receipts SET status='active'
		WHERE stream_uid='frame:frame-tamper'`); err == nil {
		t.Fatal("immutable genesis receipt was updated")
	}
	bad := FrameAuthority{ReadAuthority: "transcript_payload_v1", WriteAuthority: "transcript_payload_v1",
		ActivationID: make([]byte, sha256.Size), GenesisID: make([]byte, sha256.Size)}
	if bad.TranscriptPayloadActive() {
		t.Fatal("dual provenance was treated as active")
	}
	if _, err := db.Exec(`UPDATE transcript_frame_authority SET activation_id=zeroblob(32)
		WHERE owner_id='owner-a' AND session_id='frame-tamper'`); err == nil {
		t.Fatal("dual provenance authority update succeeded")
	}
	if _, _, err := repo.GetFrameAuthorityBySession(context.Background(), "owner-b", "frame-tamper"); !errors.Is(err, nil) {
		t.Fatalf("foreign lookup err=%v", err)
	}
}

func TestPayloadGenesisFrameDeleteRemovesAuthorityReceiptAndStream(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-delete','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame-delete','project-delete','frame-delete')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-delete", OwnerID: "owner-a", ExternalID: "frame-delete", SessionID: "frame-delete",
		Kind: StreamKindFrameRef, ProjectID: "project-delete", RootFrameID: "frame-delete", FrameID: "frame-delete", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := DeleteFrameStreamsTx(context.Background(), tx, "owner-a", "project-delete", "frame-delete")
	if err != nil || deleted != 1 {
		_ = tx.Rollback()
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM transcript_frame_authority WHERE session_id='frame-delete'`,
		`SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE session_id='frame-delete'`,
		`SELECT COUNT(*) FROM transcript_streams WHERE session_id='frame-delete'`,
		`SELECT COUNT(*) FROM transcript_branches WHERE stream_uid='frame:frame-delete'`,
		`SELECT COUNT(*) FROM transcript_branch_state WHERE stream_uid='frame:frame-delete'`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("query=%s count=%d err=%v", query, count, err)
		}
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
}
