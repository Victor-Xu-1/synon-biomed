package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

func TestStageAskUserHistoryBackfillIsIdempotentAndNonServing(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "backfill", false)
	makeLegacyAskUserAnswered(t, db, stream, "backfill")
	audit, created, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
	if err != nil || !created || audit.Status != AskUserHistoryEligible || audit.EligibleCount != 1 {
		t.Fatalf("audit=%#v created=%t err=%v", audit, created, err)
	}
	input := stageAskUserHistoryBackfillInput(stream, audit)
	for _, comparison := range audit.Comparisons {
		if comparison.Verdict != AskUserHistoryShadowMatch {
			t.Fatalf("pre-stage shadow comparison=%#v", comparison)
		}
	}
	snapshot, err := loadAskUserHistorySnapshot(context.Background(), db, AuditAskUserHistoryInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, BranchID: audit.BranchID,
		MaxEvents: input.MaxEvents, MaxCandidates: input.MaxCandidates, MaxShadowRows: input.MaxShadowRows,
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidates, err := buildAskUserHistoryBackfillCandidates(snapshot, audit, input.MaxCandidates); err != nil || len(candidates) != 1 {
		t.Fatalf("pre-stage candidates=%#v err=%v", candidates, err)
	}
	eventsBefore := countHistoryRows(t, db, "transcript_events")
	membershipsBefore := countHistoryRows(t, db, "transcript_branch_events")
	intentsBefore := countHistoryRows(t, db, "transcript_delivery_intents")
	staged, stagedCreated, err := repo.StageAskUserHistoryBackfill(context.Background(), input)
	if err != nil || !stagedCreated || staged.BackfillID == "" || staged.StagingSHA256 == "" ||
		len(staged.Candidates) != 1 || staged.Candidates[0].ToolUseID != "ask-backfill" {
		t.Fatalf("staged=%#v created=%t err=%v", staged, stagedCreated, err)
	}
	candidate := staged.Candidates[0]
	prompt, err := DecodeAskUserPromptV1(candidate.PromptJSON)
	if err != nil || prompt.ToolUseID != "ask-backfill" || len(prompt.Questions) != 1 ||
		prompt.Questions[0].Question != "Which structure?" || len(prompt.Questions[0].Options) != 2 {
		t.Fatalf("prompt=%#v err=%v", prompt, err)
	}
	pending, err := DecodeAskUserResultEventV1(candidate.PendingJSON)
	if err != nil || pending.Result.Status != AskUserStatusAwaitingResponse || pending.ModelContinuation != "" ||
		pending.Origin.PendingClientMessageID != candidate.PendingClientMessageID {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	result, err := DecodeAskUserResultEventV1(candidate.ResultJSON)
	if err != nil || result.Result.Status != AskUserStatusAnswered ||
		result.Result.Answers["Which structure?"] != "5FQD" ||
		result.ModelContinuation != `{"status":"answered","answers":{"Which structure?":"5FQD"}}` {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	terminalClientID, err := AskUserResultClientMessageIDV1(result.Origin)
	if err != nil || candidate.TerminalClientMessageID != terminalClientID ||
		candidate.PromptClientMessageID == candidate.PendingClientMessageID ||
		candidate.PendingClientMessageID == candidate.TerminalClientMessageID {
		t.Fatalf("candidate identities=%#v expected terminal=%q err=%v", candidate, terminalClientID, err)
	}
	if events := countHistoryRows(t, db, "transcript_events"); events != eventsBefore {
		t.Fatalf("staging changed live events: %d -> %d", eventsBefore, events)
	}
	if memberships := countHistoryRows(t, db, "transcript_branch_events"); memberships != membershipsBefore {
		t.Fatalf("staging changed live branch membership: %d -> %d", membershipsBefore, memberships)
	}
	if intents := countHistoryRows(t, db, "transcript_delivery_intents"); intents != intentsBefore {
		t.Fatalf("staging created live delivery intents: %d -> %d", intentsBefore, intents)
	}
	retry, retryCreated, err := repo.StageAskUserHistoryBackfill(context.Background(), input)
	if err != nil || retryCreated || retry.BackfillID != staged.BackfillID ||
		retry.StagingSHA256 != staged.StagingSHA256 || retry.CreatedAt != staged.CreatedAt {
		t.Fatalf("retry=%#v created=%t err=%v", retry, retryCreated, err)
	}
	foreign := input
	foreign.OwnerID = "foreign"
	if _, _, err := repo.StageAskUserHistoryBackfill(context.Background(), foreign); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign owner error=%v", err)
	}
	if runs := countHistoryRows(t, db, "transcript_history_backfill_runs"); runs != 1 {
		t.Fatalf("backfill runs=%d", runs)
	}
	if candidates := countHistoryRows(t, db, "transcript_history_backfill_candidates"); candidates != 1 {
		t.Fatalf("backfill candidates=%d", candidates)
	}
	if cursors := countHistoryRows(t, db, "transcript_history_backfill_cursor_map"); cursors != 2 {
		t.Fatalf("backfill cursors=%d", cursors)
	}
	if _, err := db.Exec(`UPDATE transcript_history_backfill_candidates
		SET pending_json=json_set(pending_json,'$.result.status','tampered')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.StageAskUserHistoryBackfill(context.Background(), input); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("tampered staging retry error=%v", err)
	}
}

func TestStageAskUserHistoryBackfillRejectsActivePendingAndStaleSources(t *testing.T) {
	t.Run("active runner", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "active", false)
		makeLegacyAskUserAnsweredPayload(t, db, stream, "active")
		audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		if err != nil || audit.Status != AskUserHistoryEligible {
			t.Fatalf("audit=%#v err=%v", audit, err)
		}
		if _, _, err := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, audit)); !errors.Is(err, ErrHistoryBackfillBlocked) {
			t.Fatalf("active runner error=%v", err)
		}
		assertNoHistoryBackfillRows(t, db)
	})

	t.Run("pending placeholder", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "pending", false)
		makeLegacyAskUserPendingPayload(t, db, stream, "pending")
		finishLegacyAskUserAttempt(t, db, stream)
		audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		if err != nil || audit.Status != AskUserHistoryEligible {
			t.Fatalf("audit=%#v err=%v", audit, err)
		}
		if _, _, err := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, audit)); !errors.Is(err, ErrHistoryBackfillBlocked) {
			t.Fatalf("pending placeholder error=%v", err)
		}
		assertNoHistoryBackfillRows(t, db)
	})

	t.Run("source mutation", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "stale", false)
		makeLegacyAskUserAnswered(t, db, stream, "stale")
		audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		if err != nil || audit.Status != AskUserHistoryEligible {
			t.Fatalf("audit=%#v err=%v", audit, err)
		}
		if _, err := db.Exec(`UPDATE frame_events SET payload=json_set(payload,'$.content[0].content',?) WHERE id=?`,
			`{"status":"answered","answers":{"Which structure?":"Predicted"}}`, "stale-result"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, audit)); !errors.Is(err, ErrBranchStateStale) {
			t.Fatalf("source mutation error=%v", err)
		}
		assertNoHistoryBackfillRows(t, db)
		current, currentCreated, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, audit.BranchID))
		if err != nil || !currentCreated || current.RunID == audit.RunID || current.Status != AskUserHistoryEligible {
			t.Fatalf("current audit=%#v created=%t err=%v", current, currentCreated, err)
		}
		if staged, created, err := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, current)); err != nil || !created || len(staged.Candidates) != 1 {
			t.Fatalf("current staging=%#v created=%t err=%v", staged, created, err)
		}
	})

	t.Run("mixed eligible and poison", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, claim := seedAskUserHistoryClassificationFrame(t, repo, db, "mixed", false)
		makeLegacyAskUserAnsweredPayload(t, db, stream, "mixed")
		appendLegacyAskUserPair(t, repo, db, stream, claim, "mixed-poison", "User cancelled this request")
		finishLegacyAskUserAttempt(t, db, stream)
		audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		if err != nil || audit.Status != AskUserHistoryQuarantined || audit.EligibleCount != 1 || audit.PoisonCount != 1 {
			t.Fatalf("mixed audit=%#v err=%v", audit, err)
		}
		if _, _, err := repo.StageAskUserHistoryBackfill(context.Background(), stageAskUserHistoryBackfillInput(stream, audit)); !errors.Is(err, ErrHistoryBackfillBlocked) {
			t.Fatalf("mixed cohort error=%v", err)
		}
		assertNoHistoryBackfillRows(t, db)
	})

	t.Run("explicit missing artifact", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "artifact-missing", false)
		makeLegacyAskUserAnswered(t, db, stream, "artifact-missing")
		seedAskUserHistoryArtifactRef(t, db, stream, "artifact-missing", "missing")
		audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		if err != nil || audit.Status != AskUserHistoryEligible {
			t.Fatalf("audit=%#v err=%v", audit, err)
		}
		artifactMatch := false
		for _, comparison := range audit.Comparisons {
			if comparison.Dimension == AskUserHistoryShadowArtifacts {
				artifactMatch = comparison.Verdict == AskUserHistoryShadowMatch
			}
		}
		if !artifactMatch {
			t.Fatalf("artifact comparison=%#v", audit.Comparisons)
		}
		if _, _, err := repo.StageAskUserHistoryBackfill(
			context.Background(), stageAskUserHistoryBackfillInput(stream, audit),
		); !errors.Is(err, ErrHistoryBackfillBlocked) {
			t.Fatalf("missing artifact error=%v", err)
		}
		assertNoHistoryBackfillRows(t, db)
	})

	t.Run("available dangling artifact", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "artifact-dangling", false)
		makeLegacyAskUserAnswered(t, db, stream, "artifact-dangling")
		seedAskUserHistoryArtifactRef(t, db, stream, "artifact-dangling", "available")
		audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		if err != nil || audit.Status != AskUserHistoryEligible {
			t.Fatalf("audit=%#v err=%v", audit, err)
		}
		if _, _, err := repo.StageAskUserHistoryBackfill(
			context.Background(), stageAskUserHistoryBackfillInput(stream, audit),
		); !errors.Is(err, ErrHistoryBackfillBlocked) {
			t.Fatalf("dangling artifact error=%v", err)
		}
		assertNoHistoryBackfillRows(t, db)
	})

	t.Run("deleted artifact tombstone", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "artifact-deleted", false)
		makeLegacyAskUserAnswered(t, db, stream, "artifact-deleted")
		seedAskUserHistoryArtifactRef(t, db, stream, "artifact-deleted", "deleted")
		audit, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		if err != nil || audit.Status != AskUserHistoryEligible {
			t.Fatalf("audit=%#v err=%v", audit, err)
		}
		staged, created, err := repo.StageAskUserHistoryBackfill(
			context.Background(), stageAskUserHistoryBackfillInput(stream, audit),
		)
		if err != nil || !created || len(staged.Candidates) != 1 {
			t.Fatalf("deleted tombstone staging=%#v created=%t err=%v", staged, created, err)
		}
	})
}

func seedAskUserHistoryArtifactRef(
	t *testing.T,
	db *sql.DB,
	stream Stream,
	suffix, availability string,
) {
	t.Helper()
	var sourceEventID, attempt int64
	if err := db.QueryRow(`SELECT event_id,runner_attempt FROM transcript_events
		WHERE stream_uid=? AND frame_event_id=?`, stream.UID, suffix+"-tool").Scan(
		&sourceEventID, &attempt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_artifact_refs(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at
	) VALUES(?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)`, stream.UID, attempt, sourceEventID, 1,
		"artifact-"+suffix, "version-"+suffix, "produced", availability); err != nil {
		t.Fatal(err)
	}
}

func stageAskUserHistoryBackfillInput(stream Stream, audit AskUserHistoryAudit) StageAskUserHistoryBackfillInput {
	return StageAskUserHistoryBackfillInput{
		RunID: audit.RunID, StreamUID: stream.UID, OwnerID: stream.OwnerID, BranchID: audit.BranchID,
		MaxEvents: 1000, MaxCandidates: 256, MaxShadowRows: 1000,
	}
}

func makeLegacyAskUserAnswered(t *testing.T, db *sql.DB, stream Stream, suffix string) {
	t.Helper()
	makeLegacyAskUserAnsweredPayload(t, db, stream, suffix)
	finishLegacyAskUserAttempt(t, db, stream)
}

func makeLegacyAskUserAnsweredPayload(t *testing.T, db *sql.DB, stream Stream, suffix string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-" + suffix,
			"content": `{"status":"answered","answers":{"Which structure?":"5FQD"}}`, "is_error": true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frame_events SET payload=? WHERE id=?`, string(payload), suffix+"-result"); err != nil {
		t.Fatal(err)
	}
	bindLegacyAskUserAttempt(t, db, stream, suffix)
}

func makeLegacyAskUserPendingPayload(t *testing.T, db *sql.DB, stream Stream, suffix string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-" + suffix,
			"content": `{"status":"awaiting_user_response"}`, "is_error": true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frame_events SET payload=? WHERE id=?`, string(payload), suffix+"-result"); err != nil {
		t.Fatal(err)
	}
	bindLegacyAskUserAttempt(t, db, stream, suffix)
}

func bindLegacyAskUserAttempt(t *testing.T, db *sql.DB, stream Stream, suffix string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE transcript_events SET runner_attempt=(
		SELECT MAX(attempt) FROM transcript_runner_attempts WHERE stream_uid=?
	) WHERE stream_uid=? AND frame_event_id IN (?,?)`, stream.UID, stream.UID, suffix+"-tool", suffix+"-result"); err != nil {
		t.Fatal(err)
	}
}

func finishLegacyAskUserAttempt(t *testing.T, db *sql.DB, stream Stream) {
	t.Helper()
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET
		status='reclaimed',phase='terminal',finished_event_id=(
			SELECT MAX(event_id) FROM transcript_events WHERE stream_uid=?
		),finished_at=CURRENT_TIMESTAMP WHERE stream_uid=?`, stream.UID, stream.UID); err != nil {
		t.Fatal(err)
	}
}

func appendLegacyAskUserPair(
	t *testing.T,
	repo *Repository,
	db *sql.DB,
	stream Stream,
	claim RunnerClaim,
	suffix, content string,
) {
	t.Helper()
	toolID := "ask-" + suffix
	toolEventID := suffix + "-tool"
	resultEventID := suffix + "-result"
	questions := []any{map[string]any{
		"question": "Continue?", "header": "Decision",
		"options": []any{
			map[string]any{"label": "Continue", "description": "Continue the current plan."},
			map[string]any{"label": "Stop", "description": "Stop the current plan."},
		},
		"multiSelect": false,
	}}
	toolPayload, _ := json.Marshal(map[string]any{
		"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": toolID, "name": "ask_user", "input": map[string]any{"questions": questions},
		}},
	})
	resultPayload, _ := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": toolID, "content": `{"status":"awaiting_user_response"}`, "is_error": true,
		}},
	})
	var nextSequence int
	if err := db.QueryRow(`SELECT COALESCE(MAX(sequence),0)+1 FROM frame_events WHERE frame_id=?`, stream.FrameID).Scan(&nextSequence); err != nil {
		t.Fatal(err)
	}
	for offset, event := range []struct {
		id, eventType string
		payload       []byte
	}{
		{id: toolEventID, eventType: "assistant_message", payload: toolPayload},
		{id: resultEventID, eventType: "user_message", payload: resultPayload},
	} {
		if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
			VALUES(?,?,?,?,?,CURRENT_TIMESTAMP)`, event.id, stream.FrameID, nextSequence+offset, event.eventType, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, err := tx.AppendFrameAskUserReferences(context.Background(), AppendFrameAskUserReferencesInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, FrameID: stream.FrameID, ToolUseID: toolID,
			ToolUseFrameEventID: toolEventID, ToolResultFrameEventID: resultEventID,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	finalResultPayload, _ := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": toolID, "content": content, "is_error": true,
		}},
	})
	if _, err := db.Exec(`UPDATE frame_events SET payload=? WHERE id=?`, finalResultPayload, resultEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_events SET runner_attempt=?
		WHERE stream_uid=? AND frame_event_id IN (?,?)`, claim.Attempt, stream.UID, toolEventID, resultEventID); err != nil {
		t.Fatal(err)
	}
}

func countHistoryRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	allowed := map[string]bool{
		"transcript_events": true, "transcript_branch_events": true, "transcript_delivery_intents": true,
		"transcript_history_backfill_runs": true, "transcript_history_backfill_candidates": true,
		"transcript_history_backfill_cursor_map": true,
	}
	if !allowed[table] {
		t.Fatalf("unsupported history table %q", table)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertNoHistoryBackfillRows(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{
		"transcript_history_backfill_runs", "transcript_history_backfill_candidates",
		"transcript_history_backfill_cursor_map",
	} {
		if count := countHistoryRows(t, db, table); count != 0 {
			t.Fatalf("%s rows=%d", table, count)
		}
	}
}
