package transcript

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func TestReconcileLegacyFrameHistoriesActivatesPlainHistoryWithoutAskUser(t *testing.T) {
	t.Parallel()
	repo, db, _ := newTranscriptRepository(t)
	createHistoryRealtimeTestTable(t, db)
	now := time.Now().UTC()
	var incarnationColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('frames') WHERE name='incarnation_id'`).
		Scan(&incarnationColumns); err != nil {
		t.Fatal(err)
	}
	if incarnationColumns == 0 {
		if _, err := db.Exec(`ALTER TABLE frames ADD COLUMN incarnation_id TEXT NOT NULL DEFAULT ''`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-plain','owner-plain')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,incarnation_id,project_id,root_frame_id,status,updated_at)
		VALUES('frame-plain','incarnation-plain','project-plain','frame-plain','completed',?)`, now); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-plain", OwnerID: "owner-plain", ExternalID: "frame-plain", SessionID: "frame-plain",
		Kind: StreamKindFrameRef, ProjectID: "project-plain", RootFrameID: "frame-plain", FrameID: "frame-plain", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	forceLegacyFrameAuthorityForTest(t, db, stream)
	if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('plain-user','frame-plain',1,'user_message',?,?)`,
		`{"role":"user","content":[{"type":"text","text":"Summarize the assay."}]}`, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, err := tx.AppendHistoricalFrameReferences(context.Background(), stream.UID, stream.OwnerID, []string{"plain-user"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	plainState, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	boundedAudit, _, err := repo.AuditAskUserHistory(context.Background(), AuditAskUserHistoryInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, BranchID: plainState.ActiveBranchID,
		MaxEvents: 8, MaxCandidates: 8, MaxShadowRows: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	streamingAudit, _, err := repo.auditOrdinaryFrameHistory(
		context.Background(), stream.UID, stream.OwnerID, plainState.ActiveBranchID,
	)
	if err != nil || !askUserHistoryAuditsEqual(boundedAudit, streamingAudit) {
		t.Fatalf("bounded=%#v streaming=%#v err=%v", boundedAudit, streamingAudit, err)
	}
	report, err := repo.ReconcileLegacyFrameHistories(context.Background(), testHistoryReconcileInput())
	if err != nil {
		t.Fatal(err)
	}
	if report.Scanned != 1 || report.Activated != 1 || report.Deferred != 0 || report.RemainingLegacy != 0 {
		t.Fatalf("report=%#v", report)
	}
	var runID string
	if err := db.QueryRow(`SELECT lower(hex(run_id)) FROM transcript_history_classification_runs
		WHERE stream_uid=? ORDER BY classified_at DESC LIMIT 1`, stream.UID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	authority, found, err := repo.GetFrameAuthorityBySession(context.Background(), stream.OwnerID, stream.SessionID)
	if err != nil || !found || !authority.TranscriptPayloadActive() || authority.ActiveEpoch != 2 {
		t.Fatalf("authority=%#v found=%t err=%v", authority, found, err)
	}
	var eventType, source string
	var payload []byte
	if err := db.QueryRow(`SELECT event_type,source,payload_json FROM transcript_events
		WHERE stream_uid=? ORDER BY event_id LIMIT 1`, authority.ActiveStreamUID).Scan(&eventType, &source, &payload); err != nil {
		t.Fatal(err)
	}
	if eventType != "history_user_message" || source != string(EventSourcePayload) ||
		string(payload) != `{"role":"user","content":[{"type":"text","text":"Summarize the assay."}]}` {
		t.Fatalf("event type=%q source=%q payload=%s", eventType, source, payload)
	}
	sourceBranch, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	translated, found, err := repo.ResolveActivatedLegacyCursor(
		context.Background(), stream.OwnerID, stream.SessionID, sourceBranch.ActiveBranchID, 1, 1, 0,
	)
	if err != nil || !found || translated.TargetMessageIndex != 0 || translated.StableMessageID != "payload-genesis:plain-user" {
		t.Fatalf("translated=%#v found=%t err=%v", translated, found, err)
	}
	stored, found, err := repo.ResolveActivatedStoredReadCursor(
		context.Background(), stream.OwnerID, stream.SessionID, "payload-genesis:plain-user", 0,
	)
	if err != nil || !found || stored != translated {
		t.Fatalf("stored=%#v translated=%#v found=%t err=%v", stored, translated, found, err)
	}
	retry, created, err := repo.ActivateOrdinaryFrameHistory(context.Background(), runID, stream.UID, stream.OwnerID)
	if err != nil || created || retry.TargetStreamUID != authority.ActiveStreamUID || retry.ActivationSHA256 == "" {
		t.Fatalf("retry=%#v created=%t err=%v", retry, created, err)
	}
	if _, _, err := repo.ActivateOrdinaryFrameHistory(
		context.Background(), runID, "frame:foreign-session", stream.OwnerID,
	); !errors.Is(err, ErrHistoryBackfillBlocked) {
		t.Fatalf("cross-stream retry error=%v", err)
	}
	var ordinaryRuns, askUserBackfills, askUserCutovers, syntheticAskUser, rebaseEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_ordinary_cutover_runs`).Scan(&ordinaryRuns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_backfill_runs`).Scan(&askUserBackfills); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_cutover_runs`).Scan(&askUserCutovers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?
		AND event_type IN (?,?)`, authority.ActiveStreamUID, AskUserPromptEventType, AskUserResultEventType).
		Scan(&syntheticAskUser); err != nil {
		t.Fatal(err)
	}
	if ordinaryRuns != 1 || askUserBackfills != 0 || askUserCutovers != 0 || syntheticAskUser != 0 {
		t.Fatalf("ordinary=%d ask_backfills=%d ask_cutovers=%d synthetic=%d",
			ordinaryRuns, askUserBackfills, askUserCutovers, syntheticAskUser)
	}
	if _, err := db.Exec(`UPDATE transcript_history_ordinary_cursor_map SET target_event_id=999
		WHERE ordinary_cutover_id=(SELECT ordinary_cutover_id FROM transcript_history_ordinary_cutover_runs LIMIT 1)`); err == nil {
		t.Fatal("ordinary cursor accepted a missing target event")
	}
	if _, err := db.Exec(`UPDATE transcript_history_activation_receipts SET event_count=event_count+1
		WHERE provenance_kind='ordinary_v33'`); err == nil {
		t.Fatal("ordinary activation receipt accepted mutation")
	}
	if _, err := db.Exec(`UPDATE transcript_history_ordinary_cutover_runs SET cursor_count=cursor_count+1`); err == nil {
		t.Fatal("activated ordinary cutover accepted mutation")
	}
	if _, err := db.Exec(`DELETE FROM realtime_events WHERE id=?`, "transcript-history-rebase:"+retry.ActivationSHA256); err != nil {
		t.Fatal(err)
	}
	if repaired, err := repo.ReconcileActiveHistoryRealtimeRebases(context.Background(), 16); err != nil || repaired != 1 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM realtime_events WHERE id=?`,
		"transcript-history-rebase:"+retry.ActivationSHA256).Scan(&rebaseEvents); err != nil || rebaseEvents != 1 {
		t.Fatalf("rebase events=%d err=%v", rebaseEvents, err)
	}
	_, targetMutationID, _ := BaseBranchIdentity(authority.ActiveStreamUID)
	if _, err := db.Exec(`UPDATE transcript_branches SET client_mutation_id='tampered'
		WHERE stream_uid=? AND branch_id=?`, authority.ActiveStreamUID, retry.ActiveBranchID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ActivateOrdinaryFrameHistory(context.Background(), runID, stream.UID, stream.OwnerID); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("retry after branch tamper error=%v", err)
	}
	if _, err := db.Exec(`UPDATE transcript_branches SET client_mutation_id=?
		WHERE stream_uid=? AND branch_id=?`, targetMutationID, authority.ActiveStreamUID, retry.ActiveBranchID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM transcript_history_ordinary_cursor_map
		WHERE ordinary_cutover_id=(SELECT ordinary_cutover_id FROM transcript_history_ordinary_cutover_runs LIMIT 1)
			AND source_ordinal=1`); err == nil {
		t.Fatal("active ordinary cursor accepted deletion")
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := DeleteFrameStreamsTx(context.Background(), tx, stream.OwnerID, stream.ProjectID, stream.FrameID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if deleted != 2 {
		_ = tx.Rollback()
		t.Fatalf("deleted streams=%d", deleted)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"transcript_history_activation_receipts",
		"transcript_history_ordinary_cursor_map",
		"transcript_history_ordinary_cutover_runs",
		"transcript_payload_genesis_receipts",
		"transcript_frame_authority",
	} {
		var rows int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("rows in %s=%d err=%v", table, rows, err)
		}
	}
}

func TestActivateOrdinaryFrameHistoryDefersAskUserFactOnInactiveBranch(t *testing.T) {
	t.Parallel()
	repo, db, _ := newTranscriptRepository(t)
	createHistoryRealtimeTestTable(t, db)
	now := time.Now().UTC()
	var incarnationColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('frames') WHERE name='incarnation_id'`).
		Scan(&incarnationColumns); err != nil {
		t.Fatal(err)
	}
	if incarnationColumns == 0 {
		if _, err := db.Exec(`ALTER TABLE frames ADD COLUMN incarnation_id TEXT NOT NULL DEFAULT ''`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-tool','owner-tool')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,incarnation_id,project_id,root_frame_id,status,updated_at)
		VALUES('frame-tool','incarnation-tool','project-tool','frame-tool','completed',?)`, now); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-tool", OwnerID: "owner-tool", ExternalID: "frame-tool", SessionID: "frame-tool",
		Kind: StreamKindFrameRef, ProjectID: "project-tool", RootFrameID: "frame-tool", FrameID: "frame-tool", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	forceLegacyFrameAuthorityForTest(t, db, stream)
	if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('tool-user','frame-tool',1,'user_message',?,?)`,
		`{"role":"user","content":[{"type":"text","text":"search"}]}`, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, err := tx.AppendHistoricalFrameReferences(context.Background(), stream.UID, stream.OwnerID, []string{"tool-user"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	state, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	audit, _, err := repo.AuditAskUserHistory(context.Background(), AuditAskUserHistoryInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, BranchID: state.ActiveBranchID,
		MaxEvents: 16, MaxCandidates: 16, MaxShadowRows: 16,
	})
	if err != nil || audit.Status != AskUserHistoryNotApplicable {
		t.Fatalf("audit=%#v err=%v", audit, err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_branches(
		stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
		request_sha256,source_message_id,created_at,updated_at)
		VALUES(?,'br_deadbeef',?,1,1,'edit','unsupported-branch',zeroblob(32),'tool-user',?,?)`,
		stream.UID, state.ActiveBranchID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_events(
		stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,
		payload_json,frame_event_id,created_at) VALUES(?,2,2,'inactive-ask','message','payload',NULL,
		'{"role":"assistant","content":[{"type":"tool_use","id":"ask-inactive","name":"ask_user","input":{}}]}',NULL,?)`,
		stream.UID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		VALUES(?, 'br_deadbeef',1,1),(?,'br_deadbeef',2,2)`, stream.UID, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_streams SET next_event_id=3,next_publication_seq=3 WHERE stream_uid=?`,
		stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ActivateOrdinaryFrameHistory(
		context.Background(), audit.RunID, stream.UID, stream.OwnerID,
	); !errors.Is(err, errOrdinaryHistoryShapeUnsupported) {
		t.Fatalf("activation error=%v", err)
	}
}

func TestReconcileLegacyFrameHistoriesActivatesRunnerBranchAndArtifactFacts(t *testing.T) {
	t.Parallel()
	repo, db, _ := newTranscriptRepository(t)
	createHistoryRealtimeTestTable(t, db)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-ordinary-rich", "owner-rich")
	seedArtifactVersion(t, db, "owner-rich", "project-a", "root-a", "frame-a", "artifact-rich", "version-rich")
	_, checkpoint, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "ordinary-tool", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"tool":"artifact_register","status":"completed"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, claim.StreamUID, claim.Attempt, checkpoint.EventID, 0,
		"artifact-rich", "version-rich", "produced", now); err != nil {
		t.Fatal(err)
	}
	assistant, refs, created, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "ordinary-assistant", Type: "assistant_message",
		Source: EventSourcePayload, PayloadJSON: []byte(`{"text":"artifact ready"}`),
	})
	if err != nil || !created || len(refs) != 1 || refs[0].SourceEventID != assistant.EventID {
		t.Fatalf("assistant=%#v refs=%#v created=%t err=%v", assistant, refs, created, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "ordinary-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	stream, err := repo.GetStream(context.Background(), claim.StreamUID, claim.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='completed',incarnation_id='incarnation-rich' WHERE id=?`, stream.FrameID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_delivery_intents SET status='delivered',delivered_at=updated_at
		WHERE stream_uid=? AND status IN ('pending','inflight','failed')`, stream.UID); err != nil {
		t.Fatal(err)
	}
	var baseBranch string
	if err := db.QueryRow(`SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`, stream.UID).
		Scan(&baseBranch); err != nil {
		t.Fatal(err)
	}
	requestSHA := sha256.Sum256([]byte("ordinary-rich-branch"))
	if _, err := db.Exec(`INSERT INTO transcript_branches(
		stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
		request_sha256,source_message_id,created_at,updated_at)
		VALUES(?,'br_cafefeed',?,1,1,'edit','ordinary-rich-branch',?,'user-1',?,?);
		INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		SELECT stream_uid,'br_cafefeed',ordinal,event_id FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? ORDER BY ordinal`,
		stream.UID, baseBranch, requestSHA[:], now, now, stream.UID, baseBranch); err != nil {
		t.Fatal(err)
	}
	forceLegacyFrameAuthorityForTest(t, db, stream)
	reconcileInput := testHistoryReconcileInput()
	// Ordinary history activation is streaming and must not inherit the
	// AskUser classifier's caller resource ceiling.
	reconcileInput.MaxEvents = 1
	reconcileInput.MaxShadowRows = 1
	if _, err := db.Exec(`CREATE TRIGGER ordinary_history_artifact_failure
		BEFORE INSERT ON transcript_artifact_refs
		WHEN NEW.stream_uid LIKE 'history-ordinary:%'
		BEGIN SELECT RAISE(ABORT,'injected ordinary artifact failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileLegacyFrameHistories(context.Background(), reconcileInput); err == nil {
		t.Fatal("ordinary activation succeeded through injected artifact failure")
	}
	var legacyAuthority, leakedTargets, leakedCutovers int
	if err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM transcript_frame_authority WHERE active_stream_uid=?
			AND read_authority='legacy_mixed_v1' AND write_authority='legacy_frame_ref_v1'),
		(SELECT COUNT(*) FROM transcript_streams WHERE session_id=? AND epoch=2),
		(SELECT COUNT(*) FROM transcript_history_ordinary_cutover_runs)`, stream.UID, stream.SessionID).
		Scan(&legacyAuthority, &leakedTargets, &leakedCutovers); err != nil {
		t.Fatal(err)
	}
	if legacyAuthority != 1 || leakedTargets != 0 || leakedCutovers != 0 {
		t.Fatalf("rollback authority=%d targets=%d cutovers=%d", legacyAuthority, leakedTargets, leakedCutovers)
	}
	if _, err := db.Exec(`DROP TRIGGER ordinary_history_artifact_failure`); err != nil {
		t.Fatal(err)
	}
	report, err := repo.ReconcileLegacyFrameHistories(context.Background(), reconcileInput)
	if err != nil {
		t.Fatal(err)
	}
	if report.Scanned != 1 || report.Activated != 1 || report.Deferred != 0 || report.RemainingLegacy != 0 {
		t.Fatalf("report=%#v", report)
	}
	authority, found, err := repo.GetFrameAuthorityBySession(context.Background(), stream.OwnerID, stream.SessionID)
	if err != nil || !found || !authority.TranscriptPayloadActive() || authority.ActiveEpoch != 2 {
		t.Fatalf("authority=%#v found=%t err=%v", authority, found, err)
	}
	var attempts, receipts, checkpoints, branches, branchEvents, commits, artifactRefs, frameRefs int
	if err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_runner_checkpoints WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_artifact_commits WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_artifact_refs WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND source='frame_ref')`,
		authority.ActiveStreamUID, authority.ActiveStreamUID, authority.ActiveStreamUID,
		authority.ActiveStreamUID, authority.ActiveStreamUID, authority.ActiveStreamUID,
		authority.ActiveStreamUID, authority.ActiveStreamUID).
		Scan(&attempts, &receipts, &checkpoints, &branches, &branchEvents, &commits, &artifactRefs, &frameRefs); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || receipts != 1 || checkpoints != 1 || branches != 2 || branchEvents == 0 ||
		commits != 1 || artifactRefs != 1 || frameRefs != 0 {
		t.Fatalf("attempts=%d receipts=%d checkpoints=%d branches=%d branch_events=%d commits=%d refs=%d frame_refs=%d",
			attempts, receipts, checkpoints, branches, branchEvents, commits, artifactRefs, frameRefs)
	}
	var cursorCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_ordinary_cursor_map`).Scan(&cursorCount); err != nil {
		t.Fatal(err)
	}
	if cursorCount != 4 {
		t.Fatalf("cursor count=%d", cursorCount)
	}
	for _, branchID := range []string{baseBranch, "br_cafefeed"} {
		for index, stableID := range []string{"user-1", "assistant-session-a-1"} {
			cursor, found, err := repo.ResolveActivatedLegacyCursor(
				context.Background(), stream.OwnerID, stream.SessionID, branchID, 1,
				stream.NextPublication-1, index,
			)
			if err != nil || !found || cursor.TargetBranchID != branchID ||
				cursor.TargetMessageIndex != index || cursor.StableMessageID != stableID {
				t.Fatalf("branch=%s index=%d cursor=%#v found=%t err=%v", branchID, index, cursor, found, err)
			}
		}
	}
	var runID string
	if err := db.QueryRow(`SELECT lower(hex(run_id)) FROM transcript_history_classification_runs
		WHERE stream_uid=? ORDER BY classified_at DESC LIMIT 1`, stream.UID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	retry, created, err := repo.ActivateOrdinaryFrameHistory(context.Background(), runID, stream.UID, stream.OwnerID)
	if err != nil || created || retry.TargetStreamUID != authority.ActiveStreamUID || retry.CursorCount != 4 {
		t.Fatalf("retry=%#v created=%t err=%v", retry, created, err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET phase_sequence=phase_sequence+1
		WHERE stream_uid=? AND attempt=1`, authority.ActiveStreamUID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ActivateOrdinaryFrameHistory(context.Background(), runID, stream.UID, stream.OwnerID); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("retry after runner tamper error=%v", err)
	}
}

func TestReconcileLegacyFrameHistoriesActivatesEligibleAndPersistsQuarantine(t *testing.T) {
	t.Parallel()
	t.Run("eligible", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		createHistoryRealtimeTestTable(t, db)
		eligible, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "reconcile-eligible", false)
		makeLegacyAskUserAnswered(t, db, eligible, "reconcile-eligible")
		if _, err := db.Exec(`UPDATE transcript_delivery_intents SET status='delivered',delivered_at=updated_at
			WHERE stream_uid=? AND status IN ('pending','inflight','failed')`, eligible.UID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE transcript_streams SET consumed_input_revision=input_revision
			WHERE stream_uid=?`, eligible.UID); err != nil {
			t.Fatal(err)
		}
		candidates, _, err := repo.listLegacyFrameHistoryCandidates(context.Background(), "", "", 1)
		if err != nil || len(candidates) != 1 {
			t.Fatalf("candidates=%#v err=%v", candidates, err)
		}

		report, err := repo.ReconcileLegacyFrameHistories(context.Background(), testHistoryReconcileInput())
		if err != nil {
			t.Fatal(err)
		}
		if report.Scanned != 1 || report.Activated != 1 || report.Quarantined != 0 ||
			report.Blocked != 0 || report.Deferred != 0 || report.RemainingLegacy != 0 || report.Truncated {
			t.Fatalf("report=%#v", report)
		}
		authority, found, err := repo.GetFrameAuthorityBySession(context.Background(), eligible.OwnerID, eligible.SessionID)
		if err != nil || !found || !authority.TranscriptPayloadActive() || authority.ActiveEpoch != eligible.Epoch+1 {
			t.Fatalf("eligible authority=%#v found=%t err=%v", authority, found, err)
		}
		if completed, blocked, err := repo.classifyLegacyHistoryActivationConflict(
			context.Background(), candidates[0],
		); err != nil || !completed || blocked {
			t.Fatalf("completed=%t blocked=%t err=%v", completed, blocked, err)
		}
	})

	t.Run("quarantine", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		createHistoryRealtimeTestTable(t, db)
		quarantined, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "reconcile-poison", false)
		report, err := repo.ReconcileLegacyFrameHistories(context.Background(), testHistoryReconcileInput())
		if err != nil {
			t.Fatal(err)
		}
		if report.Scanned != 1 || report.Activated != 0 || report.Quarantined != 1 || report.RemainingLegacy != 1 {
			t.Fatalf("report=%#v", report)
		}
		poisonAuthority, found, err := repo.GetFrameAuthorityBySession(
			context.Background(), quarantined.OwnerID, quarantined.SessionID,
		)
		if err != nil || !found || poisonAuthority.TranscriptPayloadActive() {
			t.Fatalf("poison authority=%#v found=%t err=%v", poisonAuthority, found, err)
		}
		var poisonRuns int
		if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs
			WHERE stream_uid=? AND poison_count=1 AND conflict_count=0`, quarantined.UID).Scan(&poisonRuns); err != nil || poisonRuns != 1 {
			t.Fatalf("poison runs=%d err=%v", poisonRuns, err)
		}
		retry, err := repo.ReconcileLegacyFrameHistories(context.Background(), testHistoryReconcileInput())
		if err != nil || retry.Scanned != 1 || retry.Quarantined != 1 || retry.RemainingLegacy != 1 {
			t.Fatalf("retry=%#v err=%v", retry, err)
		}
	})
}

func TestReconcileLegacyFrameHistoriesCanonicalizesExactAskUserAliases(t *testing.T) {
	for _, test := range []struct {
		name, alias string
	}{
		{name: "claude", alias: "AskUserQuestion"},
		{name: "go", alias: "ask_user_question"},
		{name: "canonical", alias: "ask_user"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			createHistoryRealtimeTestTable(t, db)
			suffix := "reconcile-alias-" + test.name
			stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, suffix, false)
			if _, err := db.Exec(`UPDATE frame_events
				SET payload=json_set(payload,'$.content[0].name',?) WHERE id=?`,
				test.alias, suffix+"-tool"); err != nil {
				t.Fatal(err)
			}
			makeLegacyAskUserAnswered(t, db, stream, suffix)
			if _, err := db.Exec(`UPDATE transcript_delivery_intents SET status='delivered',delivered_at=updated_at
				WHERE stream_uid=? AND status IN ('pending','inflight','failed')`, stream.UID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE transcript_streams SET consumed_input_revision=input_revision
				WHERE stream_uid=?`, stream.UID); err != nil {
				t.Fatal(err)
			}
			report, err := repo.ReconcileLegacyFrameHistories(context.Background(), testHistoryReconcileInput())
			if err != nil || report.Activated != 1 || report.Quarantined != 0 || report.RemainingLegacy != 0 {
				t.Fatalf("report=%#v err=%v", report, err)
			}
			authority, found, err := repo.GetFrameAuthorityBySession(context.Background(), stream.OwnerID, stream.SessionID)
			if err != nil || !found || !authority.TranscriptPayloadActive() || authority.ActiveEpoch != stream.Epoch+1 {
				t.Fatalf("authority=%#v found=%t err=%v", authority, found, err)
			}
			var promptJSON []byte
			if err := db.QueryRow(`SELECT payload_json FROM transcript_events
				WHERE stream_uid=? AND event_type=?`, authority.ActiveStreamUID, AskUserPromptEventType).
				Scan(&promptJSON); err != nil {
				t.Fatal(err)
			}
			prompt, err := DecodeAskUserPromptV1(promptJSON)
			if err != nil || prompt.ToolName != "ask_user" || prompt.ToolUseID != "ask-"+suffix {
				t.Fatalf("prompt=%#v err=%v", prompt, err)
			}
		})
	}
}

func TestReconcileLegacyFrameHistoriesRetriesQuiescenceBlockWithoutDuplicatingAudit(t *testing.T) {
	t.Parallel()
	repo, db, _ := newTranscriptRepository(t)
	createHistoryRealtimeTestTable(t, db)
	stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "reconcile-blocked", false)
	makeLegacyAskUserAnswered(t, db, stream, "reconcile-blocked")

	blocked, err := repo.ReconcileLegacyFrameHistories(context.Background(), testHistoryReconcileInput())
	if err != nil || blocked.Scanned != 1 || blocked.Blocked != 1 || blocked.Activated != 0 ||
		blocked.RemainingLegacy != 1 {
		t.Fatalf("blocked=%#v err=%v", blocked, err)
	}
	candidates, _, err := repo.listLegacyFrameHistoryCandidates(context.Background(), "", "", 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	if completed, transient, err := repo.classifyLegacyHistoryActivationConflict(
		context.Background(), candidates[0],
	); err != nil || completed || !transient {
		t.Fatalf("completed=%t transient=%t err=%v", completed, transient, err)
	}
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs WHERE stream_uid=?`, stream.UID).
		Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
	if _, err := db.Exec(`UPDATE transcript_delivery_intents SET status='delivered',delivered_at=updated_at
		WHERE stream_uid=? AND status IN ('pending','inflight','failed')`, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_streams SET consumed_input_revision=input_revision WHERE stream_uid=?`,
		stream.UID); err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.ReconcileLegacyFrameHistories(context.Background(), testHistoryReconcileInput())
	if err != nil || recovered.Activated != 1 || recovered.RemainingLegacy != 0 {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs WHERE stream_uid=?`, stream.UID).
		Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("retry audits=%d err=%v", audits, err)
	}
}

func TestReconcileLegacyFrameHistoriesUsesStableOwnerSessionPages(t *testing.T) {
	t.Parallel()
	repo, db, _ := newTranscriptRepository(t)
	createHistoryRealtimeTestTable(t, db)
	seedAskUserHistoryClassificationFrame(t, repo, db, "page-a", false)
	seedAskUserHistoryClassificationFrame(t, repo, db, "page-b", false)

	input := testHistoryReconcileInput()
	input.Limit = 1
	first, err := repo.ReconcileLegacyFrameHistories(context.Background(), input)
	if err != nil || first.Scanned != 1 || first.Quarantined != 1 || !first.Truncated ||
		first.NextOwnerID == "" || first.NextSessionID == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	input.AfterOwnerID, input.AfterSessionID = first.NextOwnerID, first.NextSessionID
	second, err := repo.ReconcileLegacyFrameHistories(context.Background(), input)
	if err != nil || second.Scanned != 1 || second.Quarantined != 1 || second.Truncated {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs WHERE poison_count=1`).
		Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}

func TestReconcileLegacyFrameHistoriesHonorsCancellationBeforeDatabaseWork(t *testing.T) {
	t.Parallel()
	repo, _, _ := newTranscriptRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := repo.ReconcileLegacyFrameHistories(ctx, testHistoryReconcileInput())
	if !errors.Is(err, context.Canceled) || report != (LegacyFrameHistoryReconciliation{}) {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func testHistoryReconcileInput() ReconcileLegacyFrameHistoriesInput {
	return ReconcileLegacyFrameHistoriesInput{
		Limit: 16, MaxEvents: 1000, MaxCandidates: 256, MaxShadowRows: 1000,
		MaxBranches: 64, MaxCursorRows: 1000, MaxAttempts: 64, MaxCheckpoints: 1000,
		MaxBranchEvents: 1000, MaxArtifactCommits: 1000, MaxArtifactRefs: 1000, MaxRoutes: 64,
	}
}
