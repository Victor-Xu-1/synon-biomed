package transcript

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestActivateAskUserHistoryCutoverCreatesOneServingPayloadEpoch(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	createHistoryRealtimeTestTable(t, db)
	if _, err := db.Exec(`INSERT INTO realtime_events(
		id,user_id,project_id,root_frame_id,frame_id,event_type,event_kind,payload,invalidations,created_at)
		VALUES('legacy-1','owner-activate','project-activate','frame-activate','frame-activate','message.stream','fanout','{}','[]',?),
		('legacy-2','owner-activate','project-activate','frame-activate','frame-activate','message.stream','fanout','{}','[]',?)`,
		time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "activate", false)
	makeLegacyAskUserAnswered(t, db, stream, "activate")
	audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
	if err != nil {
		t.Fatal(err)
	}
	backfill, _, err := repo.StageAskUserHistoryBackfill(
		context.Background(), stageAskUserHistoryBackfillInput(stream, audit),
	)
	if err != nil {
		t.Fatal(err)
	}
	cutover, _, err := repo.PrepareAskUserHistoryCutover(
		context.Background(), prepareAskUserHistoryCutoverInput(stream, backfill),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_streams SET consumed_input_revision=input_revision WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatal(err)
	}
	input := ActivateAskUserHistoryCutoverInput{
		CutoverID: cutover.CutoverID, OwnerID: stream.OwnerID,
		MaxBranches: 64, MaxEvents: 1000, MaxAttempts: 64, MaxCheckpoints: 1000, MaxRoutes: 64,
		MaxBranchEvents: 1000, MaxArtifactCommits: 1000, MaxArtifactRefs: 1000,
	}
	activated, created, err := repo.ActivateAskUserHistoryCutover(context.Background(), input)
	if err != nil || !created || !activated.Active || activated.SourceStreamUID != stream.UID ||
		activated.TargetStreamUID == stream.UID || activated.SourceEpoch != stream.Epoch ||
		activated.TargetEpoch != stream.Epoch+1 || activated.ActivationSHA256 == "" {
		t.Fatalf("activation=%#v created=%t err=%v", activated, created, err)
	}
	latest, found, err := repo.GetFrameStreamBySession(context.Background(), stream.OwnerID, stream.SessionID)
	if err != nil || !found || latest.UID != activated.TargetStreamUID || latest.Epoch != activated.TargetEpoch {
		t.Fatalf("latest=%#v found=%t err=%v", latest, found, err)
	}
	authority, found, err := repo.GetFrameAuthorityBySession(context.Background(), stream.OwnerID, stream.SessionID)
	if err != nil || !found || !authority.TranscriptPayloadActive() ||
		authority.ActiveStreamUID != activated.TargetStreamUID || authority.ActiveEpoch != activated.TargetEpoch ||
		authority.AuthorityGeneration != activated.AuthorityGeneration || len(authority.ActivationID) != 32 {
		t.Fatalf("authority=%#v found=%t err=%v", authority, found, err)
	}
	snapshot, err := repo.GetProjectionSnapshot(context.Background(), activated.TargetStreamUID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		return tx.ValidateFrameProjectionAuthority(context.Background(), stream.OwnerID, stream.SessionID, authority, snapshot)
	}); err != nil {
		t.Fatalf("validate exact projection authority: %v", err)
	}
	staleSnapshot := snapshot
	staleSnapshot.BranchGeneration++
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		return tx.ValidateFrameProjectionAuthority(context.Background(), stream.OwnerID, stream.SessionID, authority, staleSnapshot)
	}); !errors.Is(err, ErrBranchStateStale) {
		t.Fatalf("stale branch generation error=%v", err)
	}
	staleSnapshot = snapshot
	staleSnapshot.ThroughPublicationSequence++
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		return tx.ValidateFrameProjectionAuthority(context.Background(), stream.OwnerID, stream.SessionID, authority, staleSnapshot)
	}); !errors.Is(err, ErrBranchStateStale) {
		t.Fatalf("stale projection through error=%v", err)
	}
	rebases, err := repo.ListActiveFrameRealtimeRebases(context.Background(), stream.OwnerID, stream.SessionID, 0, 10)
	if err != nil || len(rebases) != 1 || rebases[0].ActivationSHA256 != activated.ActivationSHA256 ||
		rebases[0].SessionID != stream.SessionID || rebases[0].ActiveStreamUID != activated.TargetStreamUID ||
		rebases[0].ActiveEpoch != activated.TargetEpoch || rebases[0].AuthorityGeneration != activated.AuthorityGeneration ||
		rebases[0].RealtimeHighWater != 2 || rebases[0].ActiveBranchID == "" || rebases[0].BranchGeneration <= 0 {
		t.Fatalf("realtime rebases=%#v err=%v", rebases, err)
	}
	if foreignRebases, err := repo.ListActiveFrameRealtimeRebases(context.Background(), "foreign", "", 0, 10); err != nil || len(foreignRebases) != 0 {
		t.Fatalf("foreign realtime rebases=%#v err=%v", foreignRebases, err)
	}
	var sourceBranchID string
	var sourceGeneration, sourceThrough int64
	var sourceIndex, targetIndex int
	var stableID string
	if err := db.QueryRow(`SELECT source_branch_id,source_generation,source_through_publication_seq,
		source_message_index,target_message_index,stable_message_id
		FROM transcript_history_cutover_cursor_map WHERE cutover_id=? ORDER BY source_message_index LIMIT 1`,
		mustDecodeHistoryDigest(t, cutover.CutoverID)).Scan(
		&sourceBranchID, &sourceGeneration, &sourceThrough, &sourceIndex, &targetIndex, &stableID,
	); err != nil {
		t.Fatal(err)
	}
	translated, found, err := repo.ResolveActivatedLegacyCursor(
		context.Background(), stream.OwnerID, stream.SessionID, sourceBranchID, sourceGeneration, sourceThrough, sourceIndex,
	)
	if err != nil || !found || translated.TargetMessageIndex != targetIndex || translated.StableMessageID != stableID {
		t.Fatalf("translated cursor=%#v found=%t err=%v", translated, found, err)
	}
	storedTarget, found, err := repo.ResolveActivatedStoredReadCursor(
		context.Background(), stream.OwnerID, stream.SessionID, stableID, targetIndex,
	)
	if err != nil || !found || storedTarget.TargetMessageIndex != targetIndex || storedTarget.StableMessageID != stableID {
		t.Fatalf("stored target cursor=%#v found=%t err=%v", storedTarget, found, err)
	}
	storedLegacy, found, err := repo.ResolveActivatedStoredReadCursor(
		context.Background(), stream.OwnerID, stream.SessionID, stableID, sourceIndex,
	)
	if err != nil || !found || storedLegacy.TargetMessageIndex != targetIndex || storedLegacy.StableMessageID != stableID {
		t.Fatalf("stored legacy cursor=%#v found=%t err=%v", storedLegacy, found, err)
	}
	if _, found, err := repo.ResolveActivatedStoredReadCursor(
		context.Background(), stream.OwnerID, stream.SessionID, "unknown-message", sourceIndex,
	); err != nil || found {
		t.Fatalf("unknown stored cursor found=%t err=%v", found, err)
	}
	if _, found, err := repo.ResolveActivatedLegacyCursor(
		context.Background(), stream.OwnerID, stream.SessionID, sourceBranchID, sourceGeneration, sourceThrough, sourceIndex+1000,
	); err != nil || found {
		t.Fatalf("unknown translated cursor found=%t err=%v", found, err)
	}
	if _, found, err := repo.ResolveActivatedLegacyCursor(
		context.Background(), "foreign", stream.SessionID, sourceBranchID, sourceGeneration, sourceThrough, sourceIndex,
	); err != nil || found {
		t.Fatalf("foreign translated cursor found=%t err=%v", found, err)
	}
	var frameRefs, intents, sourceEvents, targetEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND source='frame_ref'`,
		activated.TargetStreamUID).Scan(&frameRefs); err != nil || frameRefs != 0 {
		t.Fatalf("target frame refs=%d err=%v", frameRefs, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid=?`,
		activated.TargetStreamUID).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("target delivery intents=%d err=%v", intents, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, stream.UID).Scan(&sourceEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, activated.TargetStreamUID).
		Scan(&targetEvents); err != nil || targetEvents != cutover.EventCount || sourceEvents == 0 {
		t.Fatalf("source events=%d target events=%d want=%d err=%v", sourceEvents, targetEvents, cutover.EventCount, err)
	}
	var promptPayload []byte
	if err := db.QueryRow(`SELECT payload_json FROM transcript_events
		WHERE stream_uid=? AND event_type=? ORDER BY publication_seq LIMIT 1`,
		activated.TargetStreamUID, AskUserPromptEventType).Scan(&promptPayload); err != nil {
		t.Fatal(err)
	}
	prompt, err := DecodeAskUserPromptV1(promptPayload)
	if err != nil || prompt.Origin.StreamUID != activated.TargetStreamUID || prompt.Origin.Epoch != activated.TargetEpoch {
		t.Fatalf("rebound prompt=%#v err=%v", prompt, err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "late-source-write",
		PayloadJSON: []byte(`{"text":"late"}`), Destinations: []string{"ws"},
	}); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("late source write error=%v", err)
	}
	var frameEventCountBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=?`, stream.FrameID).Scan(&frameEventCountBefore); err != nil {
		t.Fatal(err)
	}
	payloadInput := AppendFrameUserEventInput{
		StreamUID: activated.TargetStreamUID, OwnerID: stream.OwnerID, ClientMessageID: "payload-task",
		FrameEventID: "unused-after-activation", MessageUUID: "payload-message", Text: "Analyze CRBN.",
		MessageOrigin: "task_intent",
	}
	payloadEvent, projection, created, err := repo.AppendFrameUserEvent(context.Background(), payloadInput)
	if err != nil || !created || payloadEvent.Source != EventSourcePayload || payloadEvent.FrameEventID != nil ||
		projection.ID == "" || projection.FrameID != stream.FrameID || projection.Type != "user_message" {
		t.Fatalf("payload event=%#v projection=%#v created=%t err=%v", payloadEvent, projection, created, err)
	}
	var frameEventCountAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=?`, stream.FrameID).Scan(&frameEventCountAfter); err != nil || frameEventCountAfter != frameEventCountBefore {
		t.Fatalf("frame event count before=%d after=%d err=%v", frameEventCountBefore, frameEventCountAfter, err)
	}
	if repeated, repeatedProjection, repeatedCreated, err := repo.AppendFrameUserEvent(context.Background(), payloadInput); err != nil || repeatedCreated || repeated.EventID != payloadEvent.EventID || repeatedProjection.ID != projection.ID {
		t.Fatalf("payload retry event=%#v projection=%#v created=%t err=%v", repeated, repeatedProjection, repeatedCreated, err)
	}
	duplicateUUID := payloadInput
	duplicateUUID.ClientMessageID = "different-client"
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), duplicateUUID); created || !errors.Is(err, ErrEventConflict) {
		t.Fatalf("duplicate payload message uuid created=%t err=%v", created, err)
	}
	intent, found, err := repo.EnsureActiveFrameTaskIntent(context.Background(), activated.TargetStreamUID, stream.OwnerID)
	if err != nil || !found || intent.Text != payloadInput.Text || intent.SourceMessageID != payloadInput.MessageUUID ||
		intent.Origin != "user" || intent.Language != "en" {
		t.Fatalf("payload task intent=%#v found=%t err=%v", intent, found, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: activated.TargetStreamUID, OwnerID: stream.OwnerID, ClientMessageID: "target-write",
		PayloadJSON: []byte(`{"text":"new work"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("target write created=%t err=%v", created, err)
	}
	if _, err := db.Exec(`UPDATE transcript_history_cutover_runs SET status='superseded' WHERE cutover_id=?`,
		mustDecodeHistoryDigest(t, cutover.CutoverID)); err == nil {
		t.Fatal("activated cutover was superseded directly")
	}
	if _, err := db.Exec(`DELETE FROM realtime_events WHERE id=?`, "transcript-history-rebase:"+activated.ActivationSHA256); err != nil {
		t.Fatal(err)
	}
	var missingRebase int
	if err := db.QueryRow(`SELECT COUNT(*) FROM realtime_events WHERE id=?`, "transcript-history-rebase:"+activated.ActivationSHA256).Scan(&missingRebase); err != nil || missingRebase != 0 {
		t.Fatalf("deleted rebase count=%d err=%v", missingRebase, err)
	}
	repaired, err := repo.ReconcileActiveHistoryRealtimeRebases(context.Background(), 1)
	if err != nil || repaired != 1 {
		t.Fatalf("startup rebase repaired=%d err=%v", repaired, err)
	}
	var restoredRebase int
	if err := db.QueryRow(`SELECT COUNT(*) FROM realtime_events WHERE id=?`,
		"transcript-history-rebase:"+activated.ActivationSHA256).Scan(&restoredRebase); err != nil || restoredRebase != 1 {
		t.Fatalf("startup restored rebase=%d err=%v", restoredRebase, err)
	}
	if repaired, err := repo.ReconcileActiveHistoryRealtimeRebases(context.Background(), 1); err != nil || repaired != 0 {
		t.Fatalf("idempotent rebase repaired=%d err=%v", repaired, err)
	}
	if _, err := db.Exec(`DELETE FROM realtime_events WHERE id=?`, "transcript-history-rebase:"+activated.ActivationSHA256); err != nil {
		t.Fatal(err)
	}
	retry, retryCreated, err := repo.ActivateAskUserHistoryCutover(context.Background(), input)
	if err != nil || retryCreated || retry.TargetStreamUID != activated.TargetStreamUID ||
		retry.ActivatedAt != activated.ActivatedAt {
		t.Fatalf("retry=%#v created=%t err=%v", retry, retryCreated, err)
	}
	restoredRebase = 0
	if err := db.QueryRow(`SELECT COUNT(*) FROM realtime_events WHERE id=?`,
		"transcript-history-rebase:"+activated.ActivationSHA256).Scan(&restoredRebase); err != nil || restoredRebase != 1 {
		t.Fatalf("restored rebase=%d err=%v", restoredRebase, err)
	}
	foreign := input
	foreign.OwnerID = "foreign"
	if _, _, err := repo.ActivateAskUserHistoryCutover(context.Background(), foreign); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign activation error=%v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := DeleteRootStreamsTx(context.Background(), tx, stream.OwnerID, stream.ProjectID, stream.RootFrameID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted lineage streams=%d", deleted)
	}
	for _, table := range []string{
		"transcript_frame_authority", "transcript_history_activation_receipts",
		"transcript_history_cutover_runs", "transcript_history_backfill_runs", "transcript_streams",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("deleted lineage table=%s count=%d err=%v", table, count, err)
		}
	}
}

func TestActivateAskUserHistoryCutoverRejectsUnsafeSourceWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Repository, *sql.DB, Stream)
		want   error
	}{
		{
			name: "staged input",
			mutate: func(t *testing.T, _ *Repository, db *sql.DB, stream Stream) {
				if _, err := db.Exec(`UPDATE transcript_streams SET input_revision=consumed_input_revision+1 WHERE stream_uid=?`, stream.UID); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrEventConflict,
		},
		{
			name: "running attempt",
			mutate: func(t *testing.T, _ *Repository, db *sql.DB, stream Stream) {
				now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
				if _, err := db.Exec(`INSERT INTO transcript_runner_attempts(
					stream_uid,attempt,runner_id,claim_token_sha256,claimed_input_revision,resume_source,
					resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,
					claimed_at,expires_at,finished_event_id,finished_at)
					SELECT ?,COALESCE(MAX(attempt),0)+1,'runner-live',?,0,'fresh',0,'running','claimed',1,0,?,?,NULL,NULL
					FROM transcript_runner_attempts WHERE stream_uid=?`, stream.UID, make([]byte, 32), now, now.Add(time.Minute), stream.UID); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrEventConflict,
		},
		{
			name: "pending delivery",
			mutate: func(t *testing.T, _ *Repository, db *sql.DB, stream Stream) {
				var publication int64
				if err := db.QueryRow(`SELECT MIN(publication_seq) FROM transcript_events WHERE stream_uid=?`, stream.UID).Scan(&publication); err != nil {
					t.Fatal(err)
				}
				now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
				if _, err := db.Exec(`INSERT INTO transcript_delivery_routes(stream_uid,destination,current_generation,status,updated_at)
					VALUES(?,'blocked',1,'active',?)`, stream.UID, now); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO transcript_delivery_intents(
					stream_uid,publication_seq,destination,route_generation,status,updated_at)
					VALUES(?,?,'blocked',1,'pending',?)`, stream.UID, publication, now); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrEventConflict,
		},
		{
			name: "unbound artifact commit",
			mutate: func(t *testing.T, _ *Repository, db *sql.DB, stream Stream) {
				var attempt, eventID int64
				if err := db.QueryRow(`SELECT runner_attempt,event_id FROM transcript_events
					WHERE stream_uid=? AND runner_attempt IS NOT NULL ORDER BY event_id LIMIT 1`, stream.UID).Scan(&attempt, &eventID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
					stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,bound_event_id,created_at)
					VALUES(?,?,?,999,'artifact-block','version-block','produced',NULL,?)`,
					stream.UID, attempt, eventID, time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrEventConflict,
		},
		{
			name: "branch drift",
			mutate: func(t *testing.T, _ *Repository, db *sql.DB, stream Stream) {
				if _, err := db.Exec(`UPDATE transcript_branch_state SET generation=generation+1 WHERE stream_uid=?`, stream.UID); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrBranchStateStale,
		},
		{
			name: "missing realtime authority",
			mutate: func(t *testing.T, _ *Repository, db *sql.DB, _ Stream) {
				if _, err := db.Exec(`DROP TABLE realtime_events`); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrSchemaUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, db, stream, input := prepareHistoryActivationTest(t, test.name)
			test.mutate(t, repo, db, stream)
			if _, created, err := repo.ActivateAskUserHistoryCutover(context.Background(), input); created || !errors.Is(err, test.want) {
				t.Fatalf("created=%t error=%v want=%v", created, err, test.want)
			}
			var receiptCount, streamCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_activation_receipts`).Scan(&receiptCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_streams WHERE session_id=?`, stream.SessionID).Scan(&streamCount); err != nil {
				t.Fatal(err)
			}
			if receiptCount != 0 || streamCount != 1 {
				t.Fatalf("receipt count=%d stream count=%d", receiptCount, streamCount)
			}
		})
	}
}

func TestActivateAskUserHistoryCutoverConcurrentRetryCreatesOneAuthority(t *testing.T) {
	repo, db, _, input := prepareHistoryActivationTest(t, "concurrent")
	type outcome struct {
		activation AskUserHistoryActivation
		created    bool
		err        error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			activation, created, err := repo.ActivateAskUserHistoryCutover(context.Background(), input)
			results <- outcome{activation: activation, created: created, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.activation.ActivationSHA256 == "" ||
		first.activation.ActivationSHA256 != second.activation.ActivationSHA256 || first.created == second.created {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	var receipts, authorities int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_activation_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_frame_authority WHERE read_authority='transcript_payload_v1'`).
		Scan(&authorities); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || authorities != 1 {
		t.Fatalf("receipts=%d authorities=%d", receipts, authorities)
	}
}

func prepareHistoryActivationTest(t *testing.T, suffix string) (*Repository, *sql.DB, Stream, ActivateAskUserHistoryCutoverInput) {
	t.Helper()
	repo, db, _ := newTranscriptRepository(t)
	createHistoryRealtimeTestTable(t, db)
	stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "activate-"+strings.ReplaceAll(suffix, " ", "-"), false)
	makeLegacyAskUserAnswered(t, db, stream, "activate-"+strings.ReplaceAll(suffix, " ", "-"))
	audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
	if err != nil {
		t.Fatal(err)
	}
	backfill, _, err := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, audit))
	if err != nil {
		t.Fatal(err)
	}
	cutover, _, err := repo.PrepareAskUserHistoryCutover(context.Background(), prepareAskUserHistoryCutoverInput(stream, backfill))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_streams SET consumed_input_revision=input_revision WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatal(err)
	}
	return repo, db, stream, ActivateAskUserHistoryCutoverInput{
		CutoverID: cutover.CutoverID, OwnerID: stream.OwnerID,
		MaxBranches: 64, MaxEvents: 1000, MaxAttempts: 64, MaxCheckpoints: 1000,
		MaxBranchEvents: 1000, MaxArtifactCommits: 1000, MaxArtifactRefs: 1000, MaxRoutes: 64,
	}
}

func createHistoryRealtimeTestTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE realtime_events (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT NOT NULL UNIQUE,
		user_id TEXT NOT NULL,
		project_id TEXT NOT NULL DEFAULT '',
		root_frame_id TEXT NOT NULL DEFAULT '',
		frame_id TEXT NOT NULL DEFAULT '',
		event_type TEXT NOT NULL,
		event_kind TEXT NOT NULL,
		payload TEXT NOT NULL,
		invalidations TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
}

func mustDecodeHistoryDigest(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
