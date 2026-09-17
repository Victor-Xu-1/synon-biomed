package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestRepositoryArtifactReferencesUseExactVersionsAndStableOrdinals(t *testing.T) {
	repo, db, dsn := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-artifact-versions", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a2")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-b", "version-b1")

	input := AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-1", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"artifacts ready"}`), Destinations: []string{"ws"},
		References: []ArtifactReferenceInput{
			{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationCited},
			{ArtifactID: "artifact-a", VersionID: "version-a2", Relation: ArtifactRelationProduced},
			{ArtifactID: "artifact-b", VersionID: "version-b1", Relation: ArtifactRelationAttached},
		},
	}
	source, refs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), input)
	if err != nil || !created || len(refs) != 3 {
		t.Fatalf("associate refs=%#v created=%t err=%v", refs, created, err)
	}
	for index, ref := range refs {
		if ref.Ordinal != index || ref.RunnerAttempt != claim.Attempt || ref.SourceEventID != source.EventID || ref.Availability != ArtifactAvailable {
			t.Fatalf("ref[%d]=%#v", index, ref)
		}
	}
	againEvent, again, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), input)
	if err != nil || created || againEvent.EventID != source.EventID || len(again) != len(refs) {
		t.Fatalf("idempotent event=%#v refs=%#v created=%t err=%v", againEvent, again, created, err)
	}
	conflict := input
	conflict.References = append([]ArtifactReferenceInput(nil), input.References...)
	conflict.References[0], conflict.References[1] = conflict.References[1], conflict.References[0]
	if _, _, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), conflict); !errors.Is(err, ErrEventConflict) || created {
		t.Fatalf("reordered association created=%t err=%v", created, err)
	}
	duplicate := input
	duplicate.References = []ArtifactReferenceInput{input.References[0], input.References[0]}
	if _, _, _, err := repo.AppendAssistantEventWithArtifacts(context.Background(), duplicate); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("duplicate association error=%v", err)
	}
	secondInput := input
	secondInput.ClientMessageID = "assistant-2"
	secondInput.PayloadJSON = []byte(`{"text":"same version cited later"}`)
	secondInput.References = []ArtifactReferenceInput{
		{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationCited},
	}
	secondEvent, secondRefs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), secondInput)
	if err != nil || !created || secondEvent.EventID == source.EventID || len(secondRefs) != 1 ||
		secondRefs[0].SourceEventID != secondEvent.EventID || secondRefs[0].Ordinal != 0 {
		t.Fatalf("second event=%#v refs=%#v created=%t err=%v", secondEvent, secondRefs, created, err)
	}
	backlinks, err := repo.ListArtifactReferenceBacklinks(context.Background(), "owner-a", "artifact-a", "version-a1", 100)
	if err != nil || len(backlinks) != 2 || backlinks[0].SourceEventID != source.EventID || backlinks[1].SourceEventID != secondEvent.EventID {
		t.Fatalf("backlinks=%#v err=%v", backlinks, err)
	}
	if foreign, err := repo.ListArtifactReferenceBacklinks(context.Background(), "owner-b", "artifact-a", "version-a1", 100); err != nil || len(foreign) != 0 {
		t.Fatalf("foreign backlinks=%#v err=%v", foreign, err)
	}

	if _, err := db.Exec(`DELETE FROM artifact_versions WHERE id='version-a1'`); err != nil {
		t.Fatal(err)
	}
	if affected, err := repo.MarkArtifactVersionUnavailable(context.Background(), "owner-a", "artifact-a", "version-a2", ArtifactDeleted); err != nil || affected != 1 {
		t.Fatalf("mark deleted affected=%d err=%v", affected, err)
	}
	listed, err := repo.ListArtifactReferences(context.Background(), claim.StreamUID, claim.OwnerID, claim.Attempt)
	if err != nil || len(listed) != 4 || listed[0].Availability != ArtifactDeleted || listed[1].Availability != ArtifactDeleted ||
		listed[2].Availability != ArtifactAvailable || listed[3].SourceEventID != secondEvent.EventID {
		t.Fatalf("availability refs=%#v err=%v", listed, err)
	}
	_, replayed, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), input)
	if err != nil || created || replayed[0].Availability != ArtifactDeleted || replayed[1].Availability != ArtifactDeleted {
		t.Fatalf("tombstone replay refs=%#v created=%t err=%v", replayed, created, err)
	}
	wantJSON, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewRepository(reopenedDB).ListArtifactReferences(context.Background(), claim.StreamUID, claim.OwnerID, claim.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(reopened)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("restart refs=%s want=%s", gotJSON, wantJSON)
	}
}

func TestRepositoryArtifactReferenceRevisionTracksStructureNotAvailability(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-artifact-revision", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")

	if revision, err := repo.ArtifactReferenceRevision(context.Background(), claim.StreamUID, claim.OwnerID); err != nil || revision != 0 {
		t.Fatalf("initial revision=%d err=%v", revision, err)
	}
	assertArtifactReferenceHead(t, db, claim.StreamUID, 0)
	if _, _, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-revision", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"artifact ready"}`),
		References: []ArtifactReferenceInput{{
			ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationProduced,
		}},
	}); err != nil || !created {
		t.Fatalf("append created=%t err=%v", created, err)
	}
	if revision, err := repo.ArtifactReferenceRevision(context.Background(), claim.StreamUID, claim.OwnerID); err != nil || revision != 1 {
		t.Fatalf("bound revision=%d err=%v", revision, err)
	}
	assertArtifactReferenceHead(t, db, claim.StreamUID, 1)
	if affected, err := repo.MarkArtifactVersionUnavailable(
		context.Background(), claim.OwnerID, "artifact-a", "version-a1", ArtifactDeleted,
	); err != nil || affected != 1 {
		t.Fatalf("mark unavailable affected=%d err=%v", affected, err)
	}
	if revision, err := repo.ArtifactReferenceRevision(context.Background(), claim.StreamUID, claim.OwnerID); err != nil || revision != 1 {
		t.Fatalf("availability revision=%d err=%v", revision, err)
	}
	assertArtifactReferenceHead(t, db, claim.StreamUID, 1)
	if _, err := db.Exec(`UPDATE transcript_artifact_refs SET ordinal=ordinal+1 WHERE stream_uid=?`, claim.StreamUID); err == nil {
		t.Fatal("artifact reference structural mutation was accepted")
	}
	if _, err := repo.ArtifactReferenceRevision(context.Background(), claim.StreamUID, "owner-b"); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign owner error=%v", err)
	}
}

func assertArtifactReferenceHead(t *testing.T, db *sql.DB, streamUID string, revision int64) {
	t.Helper()
	var gotRevision int64
	if err := db.QueryRow(`SELECT revision FROM transcript_artifact_reference_heads WHERE stream_uid=?`,
		streamUID).Scan(&gotRevision); err != nil {
		t.Fatal(err)
	}
	if gotRevision != revision {
		t.Fatalf("artifact reference head=%d, want=%d", gotRevision, revision)
	}
}

func TestRepositoryBindsOnlyCommittedRunnerArtifactsToAssistantAndTerminal(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-artifact-commits", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-b", "version-b1")
	_, source, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-a-start", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"tool":"artifact_register"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, claim.StreamUID, claim.Attempt, source.EventID, 0,
		"artifact-a", "version-a1", "produced", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	commits, err := repo.ListArtifactCommitReferences(context.Background(), claim.StreamUID, claim.OwnerID, claim.Attempt)
	if err != nil || len(commits) != 1 || commits[0].ArtifactID != "artifact-a" ||
		commits[0].VersionID != "version-a1" || commits[0].Relation != ArtifactRelationProduced {
		t.Fatalf("commit references=%#v err=%v", commits, err)
	}
	assistant, refs, created, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "assistant-committed", Type: "assistant_message",
		Source: EventSourcePayload, PayloadJSON: []byte(`{"text":"artifact ready"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created || len(refs) != 1 || refs[0].VersionID != "version-a1" || refs[0].SourceEventID != assistant.EventID {
		t.Fatalf("assistant=%#v refs=%#v created=%t err=%v", assistant, refs, created, err)
	}
	var bound int64
	if err := db.QueryRow(`SELECT bound_event_id FROM transcript_artifact_commits WHERE version_id='version-a1'`).Scan(&bound); err != nil || bound != assistant.EventID {
		t.Fatalf("assistant bound=%d err=%v", bound, err)
	}
	_, secondSource, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-b-start", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"tool":"artifact_register"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, claim.StreamUID, claim.Attempt, secondSource.EventID, 0,
		"artifact-b", "version-b1", "produced", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	terminal, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "runner-finished", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("terminal=%#v created=%t err=%v", terminal, created, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertProjectedArtifactRefs(t, projected, assistant.EventID, []string{"version-a1"})
	assertProjectedArtifactRefs(t, projected, terminal.EventID, []string{"version-a1", "version-b1"})
	if _, _, _, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "assistant-after-terminal", Type: "assistant_message",
		Source: EventSourcePayload, PayloadJSON: []byte(`{"text":"late"}`), Destinations: []string{"ws"},
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late assistant error=%v", err)
	}
}

func TestRepositoryCurrentArtifactCommitSnapshotCarriesHeadsAcrossReclaims(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	firstClaim := seedArtifactProjectionClaim(t, repo, db, "stream-artifact-recovery-heads", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-b", "version-b1")
	firstCheckpoint, firstSource, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: firstClaim, ClientMessageID: "attempt-1-artifacts", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"tool":"save_artifacts"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for ordinal, value := range []ArtifactReferenceInput{
		{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationProduced},
		{ArtifactID: "artifact-b", VersionID: "version-b1", Relation: ArtifactRelationConsumed},
	} {
		if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
			stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
		) VALUES(?,?,?,?,?,?,?,?)`, firstClaim.StreamUID, firstClaim.Attempt, firstSource.EventID,
			ordinal, value.ArtifactID, value.VersionID, string(value.Relation), now); err != nil {
			t.Fatal(err)
		}
	}

	now = now.Add(2 * time.Minute)
	second, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: firstClaim.StreamUID, OwnerID: firstClaim.OwnerID, RunnerID: firstClaim.RunnerID,
		TTL: time.Minute, ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: firstCheckpoint.Sequence,
	})
	if err != nil || !second.Claimed || second.Claim.Attempt != firstClaim.Attempt {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a2")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-c", "version-c1")
	_, secondSource, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: second.Claim, ClientMessageID: "attempt-2-artifacts", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"tool":"save_artifacts"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for ordinal, value := range []ArtifactReferenceInput{
		{ArtifactID: "artifact-a", VersionID: "version-a2", Relation: ArtifactRelationProduced},
		{ArtifactID: "artifact-c", VersionID: "version-c1", Relation: ArtifactRelationProduced},
	} {
		if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
			stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
		) VALUES(?,?,?,?,?,?,?,?)`, second.Claim.StreamUID, second.Claim.Attempt, secondSource.EventID,
			ordinal, value.ArtifactID, value.VersionID, string(value.Relation), now); err != nil {
			t.Fatal(err)
		}
	}

	first, err := repo.CurrentArtifactCommitSnapshot(
		context.Background(), firstClaim.StreamUID, firstClaim.OwnerID, firstClaim.Attempt,
	)
	if err != nil || len(first.References) != 3 {
		t.Fatalf("first snapshot=%#v err=%v", first, err)
	}
	firstVersions := []string{first.References[0].VersionID, first.References[1].VersionID, first.References[2].VersionID}
	if !slices.Equal(firstVersions, []string{"version-b1", "version-a2", "version-c1"}) {
		t.Fatalf("stable-attempt versions=%v", firstVersions)
	}
	current, err := repo.CurrentArtifactCommitSnapshot(
		context.Background(), second.Claim.StreamUID, second.Claim.OwnerID, second.Claim.Attempt,
	)
	if err != nil || current.BranchID == "" || current.BranchGeneration <= 0 || len(current.References) != 3 {
		t.Fatalf("current snapshot=%#v err=%v", current, err)
	}
	got := []string{current.References[0].VersionID, current.References[1].VersionID, current.References[2].VersionID}
	if !slices.Equal(got, []string{"version-b1", "version-a2", "version-c1"}) {
		t.Fatalf("current versions=%v", got)
	}
	if _, err := db.Exec(`INSERT INTO transcript_branches(
		stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,
		client_mutation_id,request_sha256,source_message_id,created_at,updated_at
	) VALUES(?,?,?,?,?,'edit',?,zeroblob(32),?,?,?)`,
		second.Claim.StreamUID, "br_deadbeef", current.BranchID, firstSource.EventID, 2,
		"artifact-head-branch-fixture", "save-user-message", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		SELECT stream_uid,'br_deadbeef',ordinal,event_id FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? AND event_id<=? ORDER BY ordinal`,
		second.Claim.StreamUID, current.BranchID, firstSource.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_branch_state
		SET active_branch_id='br_deadbeef',generation=generation+1,updated_at=? WHERE stream_uid=?`,
		now, second.Claim.StreamUID); err != nil {
		t.Fatal(err)
	}
	branched, err := repo.CurrentArtifactCommitSnapshot(
		context.Background(), second.Claim.StreamUID, second.Claim.OwnerID, second.Claim.Attempt,
	)
	if err != nil || branched.BranchID != "br_deadbeef" || branched.BranchGeneration != current.BranchGeneration+1 {
		t.Fatalf("branched snapshot=%#v err=%v", branched, err)
	}
	branchedVersions := make([]string, 0, len(branched.References))
	for _, ref := range branched.References {
		branchedVersions = append(branchedVersions, ref.VersionID)
	}
	if !slices.Equal(branchedVersions, []string{"version-a1", "version-b1"}) {
		t.Fatalf("branched versions=%v", branchedVersions)
	}
	if _, err := repo.CurrentArtifactCommitSnapshot(
		context.Background(), second.Claim.StreamUID, "owner-b", second.Claim.Attempt,
	); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign owner error=%v", err)
	}
}

func TestAppendAssistantEventWithCommittedArtifactsPublishesOnlyLatestArtifactHead(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-artifact-latest-publication", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a2")

	_, source, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "artifact-refinement", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"tool":"save_artifacts"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for ordinal, versionID := range []string{"version-a1", "version-a2"} {
		if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
			stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
		) VALUES(?,?,?,?,?,?,?,?)`, claim.StreamUID, claim.Attempt, source.EventID, ordinal,
			"artifact-a", versionID, string(ArtifactRelationProduced), now); err != nil {
			t.Fatal(err)
		}
	}

	event, refs, created, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "assistant-latest-artifact", Type: "assistant_message",
		Source: EventSourcePayload, PayloadJSON: []byte(`{"text":"final"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("event=%#v created=%t err=%v", event, created, err)
	}
	if len(refs) != 1 || refs[0].ArtifactID != "artifact-a" || refs[0].VersionID != "version-a2" {
		t.Fatalf("latest refs=%#v", refs)
	}
	var bound int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND bound_event_id=?`,
		claim.StreamUID, claim.Attempt, event.EventID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != 2 {
		t.Fatalf("bound commits=%d want=2", bound)
	}
}

func TestRepositoryArtifactReferencesRestrictSourceDeletionAndRollbackStorageFailures(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-artifact-retention", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	event, refs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-retained", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"retained"}`), Destinations: []string{"ws"},
		References: []ArtifactReferenceInput{{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationProduced}},
	})
	if err != nil || !created || len(refs) != 1 {
		t.Fatalf("append event=%#v refs=%#v created=%t err=%v", event, refs, created, err)
	}
	if _, err := db.Exec(`DELETE FROM transcript_events WHERE stream_uid=? AND event_id=?`, claim.StreamUID, event.EventID); err == nil {
		t.Fatal("source event deletion unexpectedly bypassed restrictive artifact retention")
	}
	if _, err := db.Exec(`ALTER TABLE artifact_versions RENAME TO artifact_versions_unavailable`); err != nil {
		t.Fatal(err)
	}
	_, _, created, err = repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-storage-fault", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"must roll back"}`), Destinations: []string{"ws"},
		References: []ArtifactReferenceInput{{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationCited}},
	})
	if !errors.Is(err, ErrSchemaUnavailable) || created {
		t.Fatalf("storage fault created=%t err=%v", created, err)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND client_message_id='assistant-storage-fault'`, claim.StreamUID).Scan(&events); err != nil || events != 0 {
		t.Fatalf("rolled-back events=%d err=%v", events, err)
	}
}

func TestRepositoryArtifactReferencesFailClosedAcrossAuthorityBoundaries(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-artifact-authority", "owner-a")
	tests := []struct {
		name       string
		owner      string
		project    string
		root       string
		frame      string
		artifactID string
		versionID  string
		relation   ArtifactRelation
		want       error
	}{
		{name: "foreign owner", owner: "owner-b", project: "project-b", root: "root-b", frame: "frame-b", artifactID: "foreign", versionID: "foreign-v1", relation: ArtifactRelationCited, want: ErrArtifactMismatch},
		{name: "foreign project", owner: "owner-a", project: "project-b", root: "root-b", frame: "frame-b", artifactID: "project", versionID: "project-v1", relation: ArtifactRelationConsumed, want: ErrArtifactMismatch},
		{name: "foreign root", owner: "owner-a", project: "project-a", root: "root-b", frame: "frame-b", artifactID: "root", versionID: "root-v1", relation: ArtifactRelationCited, want: ErrArtifactMismatch},
		{name: "other frame cannot produce", owner: "owner-a", project: "project-a", root: "root-a", frame: "frame-child", artifactID: "child", versionID: "child-v1", relation: ArtifactRelationProduced, want: ErrArtifactMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seedArtifactVersion(t, db, test.owner, test.project, test.root, test.frame, test.artifactID, test.versionID)
			_, _, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
				Claim: claim, ClientMessageID: "assistant-" + test.artifactID, Source: EventSourcePayload,
				PayloadJSON: []byte(`{"text":"artifact"}`), Destinations: []string{"ws"},
				References: []ArtifactReferenceInput{{ArtifactID: test.artifactID, VersionID: test.versionID, Relation: test.relation}},
			})
			if !errors.Is(err, test.want) || created {
				t.Fatalf("created=%t err=%v", created, err)
			}
		})
	}
	if _, _, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-missing", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"missing"}`), Destinations: []string{"ws"},
		References: []ArtifactReferenceInput{{ArtifactID: "missing", VersionID: "missing-v1", Relation: ArtifactRelationCited}},
	}); !errors.Is(err, ErrArtifactMissing) || created {
		t.Fatalf("missing version created=%t err=%v", created, err)
	}
	if refs, err := repo.ListArtifactReferences(context.Background(), claim.StreamUID, "owner-b", claim.Attempt); !errors.Is(err, ErrOwnerMismatch) || refs != nil {
		t.Fatalf("foreign list refs=%#v err=%v", refs, err)
	}
}

func TestRepositoryFrameReferenceStreamValidatesWorkspaceAuthority(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-a','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame-a','project-a','root-a')`); err != nil {
		t.Fatal(err)
	}
	base := CreateStreamInput{
		UID: "stream-a", OwnerID: "owner-a", ExternalID: "frame-a", SessionID: "session-a", Kind: StreamKindFrameRef,
		ProjectID: "project-a", RootFrameID: "root-a", FrameID: "frame-a", Epoch: 1,
	}
	tests := []struct {
		name   string
		mutate func(*CreateStreamInput)
		want   error
	}{
		{name: "foreign owner", mutate: func(input *CreateStreamInput) { input.OwnerID = "owner-b" }, want: ErrOwnerMismatch},
		{name: "foreign project", mutate: func(input *CreateStreamInput) { input.ProjectID = "project-b" }, want: ErrEventConflict},
		{name: "wrong root", mutate: func(input *CreateStreamInput) { input.RootFrameID = "root-b" }, want: ErrEventConflict},
		{name: "missing frame", mutate: func(input *CreateStreamInput) { input.FrameID = "frame-missing" }, want: ErrEventConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			if _, err := repo.CreateStream(context.Background(), input); !errors.Is(err, test.want) {
				t.Fatalf("CreateStream error=%v, want %v", err, test.want)
			}
		})
	}
	var streams int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_streams`).Scan(&streams); err != nil || streams != 0 {
		t.Fatalf("streams=%d err=%v", streams, err)
	}
	if _, err := repo.CreateStream(context.Background(), base); err != nil {
		t.Fatalf("valid frame stream: %v", err)
	}
	changedMapping := base
	changedMapping.SessionID = "session-b"
	if _, err := repo.CreateStream(context.Background(), changedMapping); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("changed session mapping error=%v", err)
	}
}

func TestRepositoryArtifactReferencesRejectReclaimedAttempt(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-artifact-reclaimed", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")

	now = now.Add(2 * time.Minute)
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "artifact-new-task",
		PayloadJSON: []byte(`{"text":"new task"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("new task input created=%t err=%v", created, err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, RunnerID: claim.RunnerID, TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claim.Attempt+1 {
		t.Fatalf("reclaim=%#v err=%v", reclaimed, err)
	}
	if _, refs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-stale", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"stale"}`), Destinations: []string{"ws"},
		References: []ArtifactReferenceInput{{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationProduced}},
	}); !errors.Is(err, ErrClaimStale) || created || refs != nil {
		t.Fatalf("stale refs=%#v created=%t err=%v", refs, created, err)
	}
	if refs, err := repo.ListArtifactReferences(context.Background(), claim.StreamUID, claim.OwnerID, claim.Attempt); err != nil || len(refs) != 0 {
		t.Fatalf("stale persisted refs=%#v err=%v", refs, err)
	}
}

func TestRepositoryArtifactReferencesHaveOneConcurrentCreator(t *testing.T) {
	repo, db, dsn := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-artifact-concurrent", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	input := AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-1", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"artifact"}`), Destinations: []string{"ws"},
		References: []ArtifactReferenceInput{{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationProduced}},
	}

	const workers = 16
	start := make(chan struct{})
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			workerDB, err := sql.Open("sqlite", dsn)
			if err != nil {
				errs <- err
				return
			}
			defer workerDB.Close()
			<-start
			_, refs, created, err := NewRepository(workerDB).AppendAssistantEventWithArtifacts(context.Background(), input)
			if err != nil {
				errs <- fmt.Errorf("worker %d: %w", index, err)
				return
			}
			if len(refs) != 1 || refs[0].VersionID != "version-a1" {
				errs <- fmt.Errorf("worker %d returned refs %#v", index, refs)
				return
			}
			results <- created
		}(index)
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	creators := 0
	for created := range results {
		if created {
			creators++
		}
	}
	if creators != 1 {
		t.Fatalf("concurrent creators=%d, want 1", creators)
	}
}

func TestRepositoryRunnerToolArtifactSourceAcceptsCompletionReviewPhase(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-review-source", "owner-a")
	cases := []struct {
		name  string
		phase string
		want  bool
	}{
		{name: "main run start", phase: "start", want: true},
		{name: "completion review read", phase: "verification_tool", want: true},
		{name: "terminal phase rejected", phase: "completed", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"toolCallId":"call-review","toolName":"read_file","toolPhase":%q}`, tc.phase)
			_, source, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
				Claim: claim, ClientMessageID: "review-" + tc.phase, Phase: RunnerPhaseExecuting, Resumable: true,
				PayloadJSON: []byte(payload), Destinations: []string{"ws"},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.ValidateRunnerToolArtifactSource(
				context.Background(), claim, source.EventID, "call-review", "read_file",
			)
			if tc.want && err != nil {
				t.Fatalf("ValidateRunnerToolArtifactSource(phase=%q) error = %v", tc.phase, err)
			}
			if !tc.want && err == nil {
				t.Fatalf("ValidateRunnerToolArtifactSource(phase=%q) must fail closed", tc.phase)
			}
		})
	}
}

func seedArtifactVersion(
	t *testing.T,
	db *sql.DB,
	ownerID, projectID, rootFrameID, frameID, artifactID, versionID string,
) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO projects(id,user_id) VALUES(?,?)`, projectID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO frames(id,project_id,root_frame_id) VALUES(?,?,?)`, frameID, projectID, rootFrameID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO artifacts(id,project_id) VALUES(?,?)`, artifactID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_versions(id,artifact_id) VALUES(?,?)`, versionID, artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO artifact_runtime_metadata(artifact_id,root_frame_id,frame_id) VALUES(?,?,?)`, artifactID, rootFrameID, frameID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_version_provenance(version_id,frame_id) VALUES(?,?)`, versionID, frameID); err != nil {
		t.Fatal(err)
	}
}
