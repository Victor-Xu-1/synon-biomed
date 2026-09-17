package transcript

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func prepareHistoryInventoryFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	var incarnationColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('frames') WHERE name='incarnation_id'`).Scan(&incarnationColumns); err != nil {
		t.Fatal(err)
	}
	if incarnationColumns == 0 {
		if _, err := db.Exec(`ALTER TABLE frames ADD COLUMN incarnation_id TEXT NOT NULL DEFAULT ''`); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`CREATE TABLE frame_execution_claims (
			frame_id TEXT PRIMARY KEY,state TEXT NOT NULL
		)`,
		`CREATE TABLE queued_user_messages (
			frame_id TEXT NOT NULL,state TEXT NOT NULL,resolved_at TIMESTAMP
		)`,
		`CREATE TABLE workspace_outbox (
			aggregate_type TEXT NOT NULL,aggregate_id TEXT NOT NULL,status TEXT NOT NULL
		)`,
		`CREATE TABLE frame_branch_archives (frame_id TEXT NOT NULL)`,
		`CREATE TABLE compaction_archives (frame_id TEXT NOT NULL)`,
		`CREATE TABLE frame_read_cursors (root_frame_id TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReconcileNoStreamFrameHistoriesDefersCursorBranchArtifactAndDeadLetterState(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	prepareHistoryInventoryFixture(t, db)
	for _, frameID := range []string{"frame-cursor", "frame-branch", "frame-compacted", "frame-artifact", "frame-dead-letter"} {
		seedHistoryInventoryFrame(t, db, "owner-a", "project-a", frameID, "completed")
	}
	if _, err := db.Exec(`INSERT INTO frame_read_cursors(root_frame_id) VALUES('frame-cursor');
		INSERT INTO frame_branch_archives(frame_id) VALUES('frame-branch');
		INSERT INTO compaction_archives(frame_id) VALUES('frame-compacted');
		INSERT INTO artifacts(id,project_id,name) VALUES('artifact-a','project-a','result');
		INSERT INTO artifact_runtime_metadata(artifact_id,root_frame_id,frame_id)
			VALUES('artifact-a','frame-artifact','frame-artifact');
		INSERT INTO workspace_outbox(aggregate_type,aggregate_id,status)
			VALUES('frame','frame-dead-letter','dead_letter')`); err != nil {
		t.Fatal(err)
	}
	report, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 10})
	if err != nil || report.Scanned != 5 || report.Deferred != 5 || report.PayloadCreated != 0 || report.Census.NoStream != 5 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestReconcileNoStreamFrameHistoriesBlocksLiveAuthorityAndRejectsStaleIncarnation(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	prepareHistoryInventoryFixture(t, db)
	for _, frameID := range []string{"frame-claim", "frame-queue", "frame-outbox", "frame-stale"} {
		seedHistoryInventoryFrame(t, db, "owner-a", "project-a", frameID, "completed")
	}
	if _, err := db.Exec(`INSERT INTO frame_execution_claims(frame_id,state) VALUES('frame-claim','parked');
		INSERT INTO queued_user_messages(frame_id,state) VALUES('frame-queue','queued');
		INSERT INTO workspace_outbox(aggregate_type,aggregate_id,status)
			VALUES('frame','frame-outbox','inflight')`); err != nil {
		t.Fatal(err)
	}
	report, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 3})
	if err != nil || report.Scanned != 3 || report.Blocked != 3 || report.PayloadCreated != 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	stale := noStreamFrameHistoryCandidate{
		ownerID: "owner-a", sessionID: "frame-stale", incarnationID: "old-incarnation",
		projectID: "project-a", rootFrameID: "frame-stale",
	}
	if _, err := repo.reconcileNoStreamFrameHistory(context.Background(), stale); err != ErrEventConflict {
		t.Fatalf("stale incarnation error=%v", err)
	}
	var streams int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_streams WHERE session_id='frame-stale'`).Scan(&streams); err != nil || streams != 0 {
		t.Fatalf("streams=%d err=%v", streams, err)
	}
}

func TestFrameHistoryCensusRejectsBlankIncarnationAndStaleStreamMapping(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	prepareHistoryInventoryFixture(t, db)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-a','owner-a');
		INSERT INTO frames(id,incarnation_id,project_id,root_frame_id,status,updated_at)
			VALUES('frame-blank','','project-a','frame-blank','completed',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	seedHistoryInventoryFrame(t, db, "owner-a", "project-a", "frame-stale-map", "completed")
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-stale-map", OwnerID: "owner-a", ExternalID: "frame-stale-map",
		SessionID: "frame-stale-map", Kind: StreamKindFrameRef, ProjectID: "project-a",
		RootFrameID: "frame-stale-map", FrameID: "frame-stale-map", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_streams SET root_frame_id='wrong-root'
		WHERE stream_uid='frame:frame-stale-map'`); err != nil {
		t.Fatal(err)
	}
	census, err := repo.FrameHistoryCensus(context.Background())
	if err != nil || census.Total != 2 || census.NoStream != 0 || census.PayloadActive != 0 || census.Conflict != 2 {
		t.Fatalf("census=%#v err=%v", census, err)
	}
}

func TestReconcileNoStreamFrameHistoriesRejectsUnknownUnsettledQueue(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	prepareHistoryInventoryFixture(t, db)
	seedHistoryInventoryFrame(t, db, "owner-a", "project-a", "frame-unknown-queue", "completed")
	if _, err := db.Exec(`INSERT INTO queued_user_messages(frame_id,state,resolved_at)
		VALUES('frame-unknown-queue','unknown',NULL)`); err != nil {
		t.Fatal(err)
	}
	report, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 10})
	if err != nil || report.Blocked != 1 || report.PayloadCreated != 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	seedHistoryInventoryFrame(t, db, "owner-b", "project-b", "frame-unrelated-outbox", "completed")
	if _, err := db.Exec(`INSERT INTO workspace_outbox(aggregate_type,aggregate_id,status)
		VALUES('project','frame-unrelated-outbox','pending')`); err != nil {
		t.Fatal(err)
	}
	unrelated, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{
		Limit: 10, AfterOwnerID: "owner-a", AfterSessionID: "frame-unknown-queue",
	})
	if err != nil || unrelated.PayloadCreated != 1 {
		t.Fatalf("unrelated outbox report=%#v err=%v", unrelated, err)
	}
}

func TestFrameHistoryCensusPlanHasNoCorrelatedStreamScan(t *testing.T) {
	_, db, _ := newTranscriptRepository(t)
	prepareHistoryInventoryFixture(t, db)
	rows, err := db.Query(`EXPLAIN QUERY PLAN ` + frameHistoryCensusQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(strings.ToLower(detail))
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.String(), "correlated scalar subquery") || !strings.Contains(plan.String(), "materialize stream_sessions") {
		t.Fatalf("unexpected census query plan:\n%s", plan.String())
	}
}

func seedHistoryInventoryFrame(
	t *testing.T,
	db *sql.DB,
	ownerID, projectID, frameID, status string,
) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO projects(id,user_id) VALUES(?,?)`, projectID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,incarnation_id,project_id,root_frame_id,status,updated_at)
		VALUES(?,?,?,?,?,?)`, frameID, "incarnation-"+frameID, projectID, frameID, status, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileNoStreamFrameHistoriesClassifiesAndMaterializesReachableFrames(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	prepareHistoryInventoryFixture(t, db)
	seedHistoryInventoryFrame(t, db, "owner-a", "project-a", "frame-empty", "completed")
	seedHistoryInventoryFrame(t, db, "owner-a", "project-a", "frame-plain", "completed")
	seedHistoryInventoryFrame(t, db, "owner-a", "project-a", "frame-rich", "failed")
	seedHistoryInventoryFrame(t, db, "owner-a", "project-a", "frame-ask-user", "failed")
	seedHistoryInventoryFrame(t, db, "owner-a", "project-a", "frame-live", "processing")
	seedHistoryInventoryFrame(t, db, "owner-b", "project-b", "frame-unsupported", "cancelled")
	now := time.Now().UTC()
	for _, statement := range []struct {
		id, frameID, eventType, payload string
	}{
		{"plain-user", "frame-plain", "user_message", `{"role":"user","text":"plain"}`},
		{"rich-assistant", "frame-rich", "assistant_message", `{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{"path":"result.txt"}}]}`},
		{"rich-result", "frame-rich", "user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"yes"}]}`},
		{"ask-assistant", "frame-ask-user", "assistant_message", `{"role":"assistant","content":[{"type":"tool_use","id":"ask-1","name":"ask_user","input":{"question":"Continue?"}}]}`},
		{"ask-result", "frame-ask-user", "user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"ask-1","content":"yes"}]}`},
		{"raw-tool", "frame-unsupported", "tool_use", `{"id":"call-raw","name":"ask_user"}`},
	} {
		if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
			VALUES(?,?,(SELECT COALESCE(MAX(sequence),0)+1 FROM frame_events WHERE frame_id=?),?,?,?)`,
			statement.id, statement.frameID, statement.frameID, statement.eventType, statement.payload, now); err != nil {
			t.Fatal(err)
		}
	}
	before, err := repo.FrameHistoryCensus(context.Background())
	if err != nil || before.Total != 6 || before.NoStream != 6 || before.Conflict != 0 {
		t.Fatalf("before=%#v err=%v", before, err)
	}
	report, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if report.Scanned != 6 || report.PayloadCreated != 3 || report.Blocked != 1 || report.Deferred != 2 ||
		report.Truncated || report.Census != (FrameHistoryAuthorityCensus{Total: 6, NoStream: 3, PayloadActive: 3}) {
		t.Fatalf("report=%#v", report)
	}
	for _, frameID := range []string{"frame-empty", "frame-plain"} {
		authority, found, err := repo.GetFrameAuthorityBySession(context.Background(), "owner-a", frameID)
		if err != nil || !found || !authority.TranscriptPayloadActive() {
			t.Fatalf("frame=%s authority=%#v found=%t err=%v", frameID, authority, found, err)
		}
	}
	var richStreams, bootstrapReceipts, richEvents, intents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_streams WHERE session_id='frame-rich'`).Scan(&richStreams); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_typed_history_bootstrap_receipts
		WHERE session_id='frame-rich' AND status='active'`).Scan(&bootstrapReceipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events
		WHERE stream_uid='frame:frame-rich' AND source='payload' AND runner_attempt IS NULL`).Scan(&richEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents`).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if richStreams != 1 || bootstrapReceipts != 1 || richEvents != 2 || intents != 0 {
		t.Fatalf("rich streams=%d receipts=%d events=%d delivery intents=%d",
			richStreams, bootstrapReceipts, richEvents, intents)
	}
	retry, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 10})
	if err != nil || retry.Scanned != 3 || retry.PayloadCreated != 0 ||
		retry.Blocked != 1 || retry.Deferred != 2 || retry.Census != report.Census {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
}

func TestReconcileNoStreamFrameHistoriesPagesAndRollsBackAtomically(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	prepareHistoryInventoryFixture(t, db)
	for _, frameID := range []string{"frame-a", "frame-b", "frame-c"} {
		seedHistoryInventoryFrame(t, db, "owner-a", "project-a", frameID, "completed")
	}
	first, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 2})
	if err != nil || !first.Truncated || first.Scanned != 2 || first.PayloadCreated != 2 ||
		first.NextOwnerID != "owner-a" || first.NextSessionID != "frame-b" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{
		Limit: 2, AfterOwnerID: first.NextOwnerID, AfterSessionID: first.NextSessionID,
	})
	if err != nil || second.Truncated || second.Scanned != 1 || second.PayloadCreated != 1 || second.Census.NoStream != 0 {
		t.Fatalf("second=%#v err=%v", second, err)
	}

	seedHistoryInventoryFrame(t, db, "owner-z", "project-z", "frame-rollback", "completed")
	if _, err := db.Exec(`CREATE TRIGGER fail_history_inventory_authority BEFORE INSERT ON transcript_frame_authority
		WHEN NEW.session_id='frame-rollback' BEGIN SELECT RAISE(ABORT,'injected inventory failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{
		Limit: 10, AfterOwnerID: "owner-a", AfterSessionID: "frame-c",
	}); err == nil {
		t.Fatal("injected authority failure succeeded")
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM transcript_streams WHERE session_id='frame-rollback'`,
		`SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE session_id='frame-rollback'`,
		`SELECT COUNT(*) FROM transcript_frame_authority WHERE session_id='frame-rollback'`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("query=%s count=%d err=%v", query, count, err)
		}
	}
}
