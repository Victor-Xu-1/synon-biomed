package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"
)

func TestDeleteRootStreamsRemovesRestrictedArtifactsAndBranchLineageOnlyInScope(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-delete-root", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	_, source, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-source", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"tool":"artifact_register"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, claim.StreamUID, claim.Attempt, source.EventID, 0,
		"artifact-a", "version-a1", "produced", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_artifact_refs(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, claim.StreamUID, claim.Attempt, source.EventID, 0,
		"artifact-a", "version-a1", "produced", "available", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects(id,user_id) VALUES('project-b','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame-b','project-b','root-b');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-unrelated", OwnerID: "owner-a", ExternalID: "frame-b", SessionID: "session-b",
		Kind: StreamKindFrameRef, ProjectID: "project-b", RootFrameID: "root-b", FrameID: "frame-b", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "stream-unrelated", OwnerID: "owner-a", ClientMessageID: "unrelated-user",
		PayloadJSON: []byte(`{"text":"unrelated"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("unrelated created=%t err=%v", created, err)
	}
	targetRunID := seedHistoryClassificationLedger(t, db, claim.StreamUID, byte(1))
	unrelatedRunID := seedHistoryClassificationLedger(t, db, "stream-unrelated", byte(2))

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := DeleteRootStreamsTx(context.Background(), tx, "owner-a", "project-a", "root-a")
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if deleted != 1 {
		_ = tx.Rollback()
		t.Fatalf("deleted streams=%d", deleted)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{
		"transcript_artifact_refs", "transcript_artifact_commits", "transcript_branch_events",
		"transcript_branch_state", "transcript_branches", "transcript_events", "transcript_runner_attempts",
		"transcript_delivery_routes", "transcript_delivery_intents", "transcript_streams",
		"transcript_history_classification_runs",
	} {
		var scoped int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE stream_uid=?", claim.StreamUID).Scan(&scoped); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if scoped != 0 {
			t.Fatalf("scoped rows in %s=%d", table, scoped)
		}
		var unrelated int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE stream_uid='stream-unrelated'").Scan(&unrelated); err != nil {
			t.Fatalf("count unrelated %s: %v", table, err)
		}
		if table == "transcript_artifact_refs" || table == "transcript_artifact_commits" || table == "transcript_runner_attempts" {
			continue
		}
		if unrelated == 0 {
			t.Fatalf("unrelated lineage missing from %s", table)
		}
	}
	for _, table := range []string{"transcript_history_classification_candidates", "transcript_history_shadow_comparisons"} {
		var scoped int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE run_id=?", targetRunID).Scan(&scoped); err != nil || scoped != 0 {
			t.Fatalf("scoped history rows in %s=%d err=%v", table, scoped, err)
		}
		var unrelated int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE run_id=?", unrelatedRunID).Scan(&unrelated); err != nil || unrelated == 0 {
			t.Fatalf("unrelated history rows in %s=%d err=%v", table, unrelated, err)
		}
	}
}

func seedHistoryClassificationLedger(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
	QueryRow(string, ...any) *sql.Row
}, streamUID string, marker byte) []byte {
	t.Helper()
	var branchID string
	var generation int64
	if err := db.QueryRow(`SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`, streamUID).
		Scan(&branchID, &generation); err != nil {
		t.Fatal(err)
	}
	runID := bytes.Repeat([]byte{marker}, sha256.Size)
	sourceDigest := bytes.Repeat([]byte{marker + 10}, sha256.Size)
	evidence := []byte(`{"version":1}`)
	evidenceDigest := sha256.Sum256(evidence)
	if _, err := db.Exec(`INSERT INTO transcript_history_classification_runs(
		run_id,stream_uid,branch_id,branch_generation,contract_version,through_ordinal,
		through_publication_seq,source_sha256,scan_status,scan_reason,candidate_count,
		native_count,eligible_count,poison_count,conflict_count,classified_at
	) VALUES(?,?,?,?,1,1,1,?,'complete','none',1,0,1,0,0,?)`,
		runID, streamUID, branchID, generation, sourceDigest, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_history_classification_candidates(
		run_id,candidate_id,first_ordinal,tool_use_id,runner_attempt,disposition,reason_code,
		state_json,evidence_json,evidence_sha256
	) VALUES(?,?,1,'ask',1,'eligible','structured_legacy',?,?,?)`, runID,
		bytes.Repeat([]byte{marker + 20}, sha256.Size), []byte(`{"version":1,"status":"awaiting_user_response"}`),
		evidence, evidenceDigest[:]); err != nil {
		t.Fatal(err)
	}
	legacyDigest := bytes.Repeat([]byte{marker + 30}, sha256.Size)
	for _, dimension := range askUserHistoryShadowDimensions {
		if _, err := db.Exec(`INSERT INTO transcript_history_shadow_comparisons(
			run_id,dimension,verdict,legacy_sha256,candidate_sha256,reason_code,evidence_json,compared_at
		) VALUES(?,?,'blocked',?,NULL,'canonical_unavailable',?,?)`, runID, string(dimension),
			legacyDigest, evidence, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	return runID
}
