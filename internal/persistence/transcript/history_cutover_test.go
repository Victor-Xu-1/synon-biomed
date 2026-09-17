package transcript

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPrepareAskUserHistoryCutoverIsReadyIdempotentAndNonServing(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "cutover", false)
	makeLegacyAskUserAnswered(t, db, stream, "cutover")
	audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
	if err != nil {
		t.Fatal(err)
	}
	backfill, _, err := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, audit))
	if err != nil {
		t.Fatal(err)
	}
	seedHistoryCutoverDescendantBranch(t, db, stream, "br_deadbeef", "history-cutover-descendant")
	eventsBefore := countHistoryRows(t, db, "transcript_events")
	membershipsBefore := countHistoryRows(t, db, "transcript_branch_events")
	intentsBefore := countHistoryRows(t, db, "transcript_delivery_intents")
	input := PrepareAskUserHistoryCutoverInput{
		BackfillID: backfill.BackfillID, StreamUID: stream.UID, OwnerID: stream.OwnerID,
		MaxBranches: 64, MaxEvents: 1000, MaxCursorRows: 1000, MaxShadowRows: 1000,
	}
	prepared, created, err := repo.PrepareAskUserHistoryCutover(context.Background(), input)
	if err != nil || !created || !prepared.Ready || prepared.BranchCount != 2 ||
		prepared.EventCount == 0 || prepared.CursorCount == 0 || prepared.VerificationSHA256 == "" ||
		prepared.LineageSHA256 == "" || prepared.CursorSHA256 == "" || prepared.ShadowSHA256 == "" {
		t.Fatalf("prepared=%#v created=%t err=%v", prepared, created, err)
	}
	if got := countHistoryRows(t, db, "transcript_events"); got != eventsBefore {
		t.Fatalf("cutover plan changed live events: %d -> %d", eventsBefore, got)
	}
	if got := countHistoryRows(t, db, "transcript_branch_events"); got != membershipsBefore {
		t.Fatalf("cutover plan changed live memberships: %d -> %d", membershipsBefore, got)
	}
	if got := countHistoryRows(t, db, "transcript_delivery_intents"); got != intentsBefore {
		t.Fatalf("cutover plan created live delivery: %d -> %d", intentsBefore, got)
	}
	retry, retryCreated, err := repo.PrepareAskUserHistoryCutover(context.Background(), input)
	if err != nil || retryCreated || retry.CutoverID != prepared.CutoverID || retry.CreatedAt != prepared.CreatedAt {
		t.Fatalf("retry=%#v created=%t err=%v", retry, retryCreated, err)
	}
	var sourceBranch, stableID string
	var sourceGeneration, sourceThrough, sourceIndex, targetPublication, targetIndex int64
	if err := db.QueryRow(`SELECT source_branch_id,source_generation,source_through_publication_seq,
		source_message_index,stable_message_id,target_publication_seq,target_message_index
		FROM transcript_history_cutover_cursor_map WHERE cutover_id=? ORDER BY source_branch_id,source_message_index LIMIT 1`,
		mustDecodeHistoryHex(t, prepared.CutoverID)).Scan(&sourceBranch, &sourceGeneration, &sourceThrough,
		&sourceIndex, &stableID, &targetPublication, &targetIndex); err != nil {
		t.Fatal(err)
	}
	cursorInput := ResolveHistoryCutoverCursorInput{
		CutoverID: prepared.CutoverID, OwnerID: stream.OwnerID, SourceBranchID: sourceBranch,
		SourceGeneration: sourceGeneration, SourceThroughPublicationSequence: sourceThrough,
		SourceMessageIndex: sourceIndex,
	}
	resolved, err := repo.ResolveHistoryCutoverCursor(context.Background(), cursorInput)
	if err != nil || resolved.StableMessageID != stableID ||
		resolved.TargetPublicationSequence != targetPublication || resolved.TargetMessageIndex != targetIndex {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	foreign := cursorInput
	foreign.OwnerID = "foreign"
	if _, err := repo.ResolveHistoryCutoverCursor(context.Background(), foreign); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign cursor error=%v", err)
	}
	stale := cursorInput
	stale.SourceGeneration++
	if _, err := repo.ResolveHistoryCutoverCursor(context.Background(), stale); !errors.Is(err, ErrBranchStateStale) {
		t.Fatalf("stale cursor error=%v", err)
	}
	seedHistoryCutoverDescendantBranch(t, db, stream, "br_feedface", "history-cutover-successor")
	successor, successorCreated, err := repo.PrepareAskUserHistoryCutover(context.Background(), input)
	if err != nil || !successorCreated || successor.CutoverID == prepared.CutoverID ||
		successor.SupersedesCutoverID != prepared.CutoverID || successor.BranchCount != 3 {
		t.Fatalf("successor=%#v created=%t err=%v", successor, successorCreated, err)
	}
	var oldStatus, newStatus string
	if err := db.QueryRow(`SELECT status FROM transcript_history_cutover_runs WHERE cutover_id=?`,
		mustDecodeHistoryHex(t, prepared.CutoverID)).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM transcript_history_cutover_runs WHERE cutover_id=?`,
		mustDecodeHistoryHex(t, successor.CutoverID)).Scan(&newStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "superseded" || newStatus != "ready" {
		t.Fatalf("cutover statuses old=%q new=%q", oldStatus, newStatus)
	}
	if _, err := repo.ResolveHistoryCutoverCursor(context.Background(), cursorInput); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("superseded cursor error=%v", err)
	}
	if _, err := db.Exec(`UPDATE transcript_history_cutover_events SET payload_json=json_set(payload_json,'$.version',99)
		WHERE cutover_id=? AND fact_kind='ask_user_prompt'`, mustDecodeHistoryHex(t, successor.CutoverID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.PrepareAskUserHistoryCutover(context.Background(), input); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("tampered ready plan error=%v", err)
	}
	for _, table := range []string{
		"transcript_history_cutover_runs", "transcript_history_cutover_branches",
		"transcript_history_cutover_events", "transcript_history_cutover_branch_events",
		"transcript_history_cutover_cursor_map", "transcript_history_cutover_shadow_comparisons",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count == 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func seedHistoryCutoverDescendantBranch(t *testing.T, db *sql.DB, stream Stream, branchID, mutationID string) {
	t.Helper()
	var parentBranch string
	if err := db.QueryRow(`SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`, stream.UID).Scan(&parentBranch); err != nil {
		t.Fatal(err)
	}
	var forkEventID int64
	if err := db.QueryRow(`SELECT event_id FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? ORDER BY ordinal LIMIT 1`, stream.UID, parentBranch).Scan(&forkEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_branches(
		stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
		request_sha256,source_message_id,created_at,updated_at)
		VALUES(?,?,?,?,0,'edit',?,zeroblob(32),?,datetime('now','+1 second'),datetime('now','+1 second'))`,
		stream.UID, branchID, parentBranch, forkEventID, mutationID, "history-source"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		SELECT stream_uid,?,ordinal,event_id FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? ORDER BY ordinal`, branchID, stream.UID, parentBranch); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAskUserHistoryCutoverRejectsTamperAndSourceDrift(t *testing.T) {
	t.Run("staging tamper", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "cutover-tamper", false)
		makeLegacyAskUserAnswered(t, db, stream, "cutover-tamper")
		audit, _, _ := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		backfill, _, _ := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, audit))
		if _, err := db.Exec(`UPDATE transcript_history_backfill_cursor_map SET stable_message_id='tampered'`); err != nil {
			t.Fatal(err)
		}
		_, _, err := repo.PrepareAskUserHistoryCutover(context.Background(), prepareAskUserHistoryCutoverInput(stream, backfill))
		if !errors.Is(err, ErrEventConflict) {
			t.Fatalf("tampered staging error=%v", err)
		}
		assertNoHistoryCutoverRows(t, db)
	})

	t.Run("source drift", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "cutover-drift", false)
		makeLegacyAskUserAnswered(t, db, stream, "cutover-drift")
		audit, _, _ := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		backfill, _, _ := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, audit))
		if _, err := db.Exec(`UPDATE frame_events SET payload=json_set(payload,'$.content[0].content',?) WHERE id=?`,
			`{"status":"answered","answers":{"Which structure?":"7XYZ"}}`, "cutover-drift-result"); err != nil {
			t.Fatal(err)
		}
		_, _, err := repo.PrepareAskUserHistoryCutover(context.Background(), prepareAskUserHistoryCutoverInput(stream, backfill))
		if !errors.Is(err, ErrBranchStateStale) {
			t.Fatalf("source drift error=%v", err)
		}
		assertNoHistoryCutoverRows(t, db)
	})
}

func TestPrepareAskUserHistoryCutoverConcurrentRetryHasOneDurableWinner(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "cutover-race", false)
	makeLegacyAskUserAnswered(t, db, stream, "cutover-race")
	audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
	if err != nil {
		t.Fatal(err)
	}
	backfill, _, err := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, audit))
	if err != nil {
		t.Fatal(err)
	}
	input := prepareAskUserHistoryCutoverInput(stream, backfill)
	const workers = 8
	start := make(chan struct{})
	type result struct {
		cutover AskUserHistoryCutover
		created bool
		err     error
	}
	results := make(chan result, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			cutover, created, err := repo.PrepareAskUserHistoryCutover(context.Background(), input)
			results <- result{cutover: cutover, created: created, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	createdCount := 0
	cutoverID := ""
	for item := range results {
		if item.err != nil {
			t.Fatal(item.err)
		}
		if item.created {
			createdCount++
		}
		if cutoverID == "" {
			cutoverID = item.cutover.CutoverID
		} else if item.cutover.CutoverID != cutoverID {
			t.Fatalf("cutover ids %q and %q", cutoverID, item.cutover.CutoverID)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created winners=%d", createdCount)
	}
	var runs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_cutover_runs`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("runs=%d err=%v", runs, err)
	}
}

func TestPrepareAskUserHistoryCutoverProjectsMixedNativeAndLegacyHistory(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "cutover-mixed", true)
	questions := []any{map[string]any{
		"question": "Which dataset?", "header": "Dataset",
		"options": []any{
			map[string]any{"label": "Curated", "description": "Use the curated dataset."},
			map[string]any{"label": "Raw", "description": "Use the raw dataset."},
		},
		"multiSelect": false,
	}}
	toolPayload := map[string]any{
		"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": "ask-cutover-mixed-legacy", "name": "ask_user",
			"input": map[string]any{"questions": questions},
		}},
	}
	resultPayload := map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-cutover-mixed-legacy",
			"content": `{"status":"answered","answers":{"Which dataset?":"Curated"}}`, "is_error": true,
		}},
	}
	appendHistoryFrameReferences(t, repo, db, stream, []historyFrameReferenceFixture{
		{id: "cutover-mixed-legacy-tool", eventType: "assistant_message", payload: toolPayload},
		{id: "cutover-mixed-legacy-result", eventType: "user_message", payload: resultPayload},
	})
	bindLegacyAskUserAttempt(t, db, stream, "cutover-mixed-legacy")
	finishLegacyAskUserAttempt(t, db, stream)
	audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
	if err != nil || audit.Status != AskUserHistoryEligible || audit.NativeCount != 1 || audit.EligibleCount != 1 {
		t.Fatalf("mixed audit=%#v err=%v", audit, err)
	}
	backfill, _, err := repo.StageAskUserHistoryBackfill(
		context.Background(), stageAskUserHistoryBackfillInput(stream, audit),
	)
	if err != nil {
		t.Fatal(err)
	}
	cutover, created, err := repo.PrepareAskUserHistoryCutover(
		context.Background(), prepareAskUserHistoryCutoverInput(stream, backfill),
	)
	if err != nil || !created || !cutover.Ready {
		t.Fatalf("mixed cutover=%#v created=%t err=%v", cutover, created, err)
	}
	var prompts, results int
	if err := db.QueryRow(`SELECT
		SUM(event_type='ask_user_prompt'),SUM(event_type='ask_user_result')
		FROM transcript_history_cutover_events WHERE cutover_id=?`,
		mustDecodeHistoryHex(t, cutover.CutoverID)).Scan(&prompts, &results); err != nil {
		t.Fatal(err)
	}
	if prompts != 2 || results != 3 {
		t.Fatalf("mixed target prompts=%d results=%d", prompts, results)
	}
}

func TestPrepareAskUserHistoryCutoverRejectsEveryV29TimestampDrift(t *testing.T) {
	for _, table := range []string{
		"transcript_history_backfill_runs",
		"transcript_history_backfill_candidates",
		"transcript_history_backfill_cursor_map",
	} {
		t.Run(table, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "cutover-time-"+table, false)
			makeLegacyAskUserAnswered(t, db, stream, "cutover-time-"+table)
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
			var createdAt time.Time
			if err := db.QueryRow(`SELECT created_at FROM ` + table + ` LIMIT 1`).Scan(&createdAt); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE `+table+` SET created_at=?`, createdAt.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			_, _, err = repo.PrepareAskUserHistoryCutover(
				context.Background(), prepareAskUserHistoryCutoverInput(stream, backfill),
			)
			if !errors.Is(err, ErrEventConflict) {
				t.Fatalf("timestamp drift error=%v", err)
			}
			assertNoHistoryCutoverRows(t, db)
		})
	}
}

func TestPrepareAskUserHistoryCutoverMaterializesTerminalReceiptAndArtifactAuthority(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	createHistoryRealtimeTestTable(t, db)
	stream, claim := seedAskUserHistoryClassificationFrame(t, repo, db, "cutover-authority", false)
	makeLegacyAskUserAnsweredPayload(t, db, stream, "cutover-authority")
	seedArtifactVersion(t, db, stream.OwnerID, stream.ProjectID, stream.RootFrameID, stream.FrameID,
		"artifact-cutover", "version-cutover")
	assistant, refs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "cutover-authority-assistant", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"artifact ready"}`),
		References: []ArtifactReferenceInput{{
			ArtifactID: "artifact-cutover", VersionID: "version-cutover", Relation: ArtifactRelationProduced,
		}},
	})
	if err != nil || !created || len(refs) != 1 {
		t.Fatalf("assistant=%#v refs=%#v created=%t err=%v", assistant, refs, created, err)
	}
	secondAssistant, secondRefs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "cutover-authority-assistant-2", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"artifact cited again"}`),
		References: []ArtifactReferenceInput{{
			ArtifactID: "artifact-cutover", VersionID: "version-cutover", Relation: ArtifactRelationProduced,
		}},
	})
	if err != nil || !created || secondAssistant.EventID == assistant.EventID || len(secondRefs) != 1 ||
		secondRefs[0].SourceEventID != secondAssistant.EventID {
		t.Fatalf("second assistant=%#v refs=%#v created=%t err=%v", secondAssistant, secondRefs, created, err)
	}
	terminal, receipt, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "cutover-authority-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	})
	if err != nil || !created || receipt.EventID != terminal.EventID {
		t.Fatalf("terminal=%#v receipt=%#v created=%t err=%v", terminal, receipt, created, err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='completed' WHERE id=?`, stream.FrameID); err != nil {
		t.Fatal(err)
	}
	audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
	if err != nil || audit.Status != AskUserHistoryEligible {
		t.Fatalf("authority audit=%#v err=%v", audit, err)
	}
	backfill, _, err := repo.StageAskUserHistoryBackfill(
		context.Background(), stageAskUserHistoryBackfillInput(stream, audit),
	)
	if err != nil {
		t.Fatal(err)
	}
	cutover, created, err := repo.PrepareAskUserHistoryCutover(
		context.Background(), prepareAskUserHistoryCutoverInput(stream, backfill),
	)
	if err != nil || !created || !cutover.Ready {
		t.Fatalf("authority cutover=%#v created=%t err=%v", cutover, created, err)
	}
	cutoverID := mustDecodeHistoryHex(t, cutover.CutoverID)
	var receiptStatus, receiptEventType string
	if err := db.QueryRow(`SELECT receipt.status,event.event_type
		FROM transcript_history_cutover_receipts receipt
		JOIN transcript_history_cutover_events event
		  ON event.cutover_id=receipt.cutover_id AND event.target_event_key=receipt.target_event_key
		WHERE receipt.cutover_id=?`, cutoverID).Scan(&receiptStatus, &receiptEventType); err != nil {
		t.Fatal(err)
	}
	if receiptStatus != "completed" || receiptEventType != "runner_finished" {
		t.Fatalf("cutover receipt status=%q event=%q", receiptStatus, receiptEventType)
	}
	var artifactID, versionID, artifactEventType string
	if err := db.QueryRow(`SELECT ref.artifact_id,ref.version_id,event.event_type
		FROM transcript_history_cutover_artifact_refs ref
		JOIN transcript_history_cutover_events event
		  ON event.cutover_id=ref.cutover_id AND event.target_event_key=ref.target_event_key
		WHERE ref.cutover_id=?`, cutoverID).Scan(&artifactID, &versionID, &artifactEventType); err != nil {
		t.Fatal(err)
	}
	if artifactID != "artifact-cutover" || versionID != "version-cutover" || artifactEventType != "assistant_message" {
		t.Fatalf("cutover artifact=%q/%q event=%q", artifactID, versionID, artifactEventType)
	}
	var artifactRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_cutover_artifact_refs WHERE cutover_id=?`, cutoverID).
		Scan(&artifactRows); err != nil || artifactRows != 3 {
		rows, queryErr := db.Query(`SELECT event.event_type,event.client_message_id
			FROM transcript_history_cutover_artifact_refs ref
			JOIN transcript_history_cutover_events event
			  ON event.cutover_id=ref.cutover_id AND event.target_event_key=ref.target_event_key
			WHERE ref.cutover_id=? ORDER BY event.target_publication_seq`, cutoverID)
		var targets []string
		if queryErr == nil {
			defer rows.Close()
			for rows.Next() {
				var eventType, clientID string
				if scanErr := rows.Scan(&eventType, &clientID); scanErr != nil {
					t.Fatal(scanErr)
				}
				targets = append(targets, eventType+":"+clientID)
			}
		}
		t.Fatalf("cutover artifact rows=%d err=%v targets=%v queryErr=%v", artifactRows, err, targets, queryErr)
	}
	for _, dimension := range []string{"terminal_facts", "artifact_refs"} {
		var legacyDigest, targetDigest []byte
		if err := db.QueryRow(`SELECT legacy_sha256,target_sha256
			FROM transcript_history_cutover_shadow_comparisons WHERE cutover_id=? AND dimension=?`,
			cutoverID, dimension).Scan(&legacyDigest, &targetDigest); err != nil {
			t.Fatal(err)
		}
		if len(legacyDigest) != 32 || !bytes.Equal(legacyDigest, targetDigest) {
			t.Fatalf("%s shadow legacy=%x target=%x", dimension, legacyDigest, targetDigest)
		}
	}
	if _, err := db.Exec(`UPDATE transcript_streams SET consumed_input_revision=input_revision WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_delivery_intents SET status='delivered',delivered_at=updated_at
		WHERE stream_uid=? AND status IN ('pending','inflight','failed')`, stream.UID); err != nil {
		t.Fatal(err)
	}
	activation, created, err := repo.ActivateAskUserHistoryCutover(context.Background(), ActivateAskUserHistoryCutoverInput{
		CutoverID: cutover.CutoverID, OwnerID: stream.OwnerID,
		MaxBranches: 64, MaxEvents: 1000, MaxAttempts: 64, MaxCheckpoints: 1000,
		MaxBranchEvents: 1000, MaxArtifactCommits: 1000, MaxArtifactRefs: 1000, MaxRoutes: 64,
	})
	if err != nil || !created || activation.ReceiptCount != 1 || activation.ArtifactRefCount != artifactRows ||
		activation.AttemptCount != 1 || activation.EventCount != cutover.EventCount {
		t.Fatalf("activation=%#v created=%t err=%v", activation, created, err)
	}
	var targetReceiptStatus string
	var targetArtifactRows int
	if err := db.QueryRow(`SELECT status FROM transcript_runner_receipts
		WHERE stream_uid=? AND attempt=?`, activation.TargetStreamUID, claim.Attempt).Scan(&targetReceiptStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_artifact_refs WHERE stream_uid=?`, activation.TargetStreamUID).
		Scan(&targetArtifactRows); err != nil {
		t.Fatal(err)
	}
	if targetReceiptStatus != "completed" || targetArtifactRows != artifactRows {
		t.Fatalf("target receipt=%q artifact rows=%d want=%d", targetReceiptStatus, targetArtifactRows, artifactRows)
	}
	if recovered, err := repo.RecoverTerminalDeliveryIntents(context.Background(), stream.OwnerID); err != nil || recovered != 0 {
		t.Fatalf("historical terminal recovery=%d err=%v", recovered, err)
	}
	var historicalIntents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid=?`, activation.TargetStreamUID).
		Scan(&historicalIntents); err != nil || historicalIntents != 0 {
		t.Fatalf("historical intents=%d err=%v", historicalIntents, err)
	}
}

func prepareAskUserHistoryCutoverInput(stream Stream, backfill AskUserHistoryBackfill) PrepareAskUserHistoryCutoverInput {
	return PrepareAskUserHistoryCutoverInput{
		BackfillID: backfill.BackfillID, StreamUID: stream.UID, OwnerID: stream.OwnerID,
		MaxBranches: 64, MaxEvents: 1000, MaxCursorRows: 1000, MaxShadowRows: 1000,
	}
}

func assertNoHistoryCutoverRows(t *testing.T, db interface {
	QueryRow(query string, args ...any) *sql.Row
}) {
	t.Helper()
	for _, table := range []string{
		"transcript_history_cutover_runs", "transcript_history_cutover_branches",
		"transcript_history_cutover_events", "transcript_history_cutover_receipts",
		"transcript_history_cutover_artifact_refs", "transcript_history_cutover_branch_events",
		"transcript_history_cutover_cursor_map", "transcript_history_cutover_shadow_comparisons",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}
