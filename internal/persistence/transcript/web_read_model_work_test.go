package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTranscriptWebProjectionFenceIsOwnerFirstAndReturnsStateMetadata(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	fixture := seedTranscriptWebWorkFrame(t, repo, db, "owner-fence", "session-fence", "stream-fence", 1)
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: fixture.streamUID, OwnerID: fixture.ownerID, ClientMessageID: "user-fence",
		PayloadJSON: []byte(`{"text":"private"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append user created=%t err=%v", created, err)
	}
	seedTranscriptWebWorkState(t, db, fixture, "ready", 7)
	readModel := NewWebReadModelRepository(db, db)
	if _, err := db.Exec(`UPDATE transcript_web_projection_state SET projector_version=?
		WHERE stream_uid=? AND branch_id=?`, TranscriptWebProjectorVersion-1, fixture.streamUID, fixture.branchID); err != nil {
		t.Fatal(err)
	}
	older, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), fixture.ownerID, fixture.streamUID, fixture.branchID,
	)
	if err != nil || !older.StateFound || older.StateProjectorVersion != TranscriptWebProjectorVersion-1 {
		t.Fatalf("older projector fence=%#v err=%v", older, err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_web_projection_dirty(
		stream_uid,branch_id,source_revision,first_affected_ordinal,reason_mask
	) VALUES(?,?,?,?,?)`, fixture.streamUID, fixture.branchID, 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	work, found, err := readModel.GetTranscriptWebProjectionWork(
		context.Background(), fixture.ownerID, fixture.streamUID, fixture.branchID,
	)
	if err != nil || !found || work.Status != TranscriptWebProjectionWorkStale ||
		work.Reason != "projection_projector_version_stale" || work.DirtyFirstAffectedOrdinal != 1 {
		t.Fatalf("stale projector with dirty source work=%#v found=%t err=%v", work, found, err)
	}
	if _, err := db.Exec(`DELETE FROM transcript_web_projection_dirty WHERE stream_uid=? AND branch_id=?`,
		fixture.streamUID, fixture.branchID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_web_projection_state SET projector_version=?
		WHERE stream_uid=? AND branch_id=?`, TranscriptWebProjectorVersion, fixture.streamUID, fixture.branchID); err != nil {
		t.Fatal(err)
	}

	foreign, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), "owner-foreign", fixture.streamUID, fixture.branchID,
	)
	if !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign fence=%#v err=%v", foreign, err)
	}
	if !reflect.DeepEqual(foreign, TranscriptWebProjectionFence{}) {
		t.Fatalf("foreign owner received projection metadata: %#v", foreign)
	}
	inactive, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), fixture.ownerID, fixture.streamUID, "br_deadbeef",
	)
	if !errors.Is(err, ErrBranchStateStale) || !reflect.DeepEqual(inactive, TranscriptWebProjectionFence{}) {
		t.Fatalf("inactive branch fence=%#v err=%v", inactive, err)
	}

	for _, status := range []string{"ready", "building", "quarantined"} {
		lastError := ""
		if status == "quarantined" {
			lastError = "test_projection_failure"
		}
		if _, err := db.Exec(`UPDATE transcript_web_projection_state
			SET status=?,last_error_code=? WHERE stream_uid=? AND branch_id=?`,
			status, lastError, fixture.streamUID, fixture.branchID); err != nil {
			t.Fatal(err)
		}
		fence, err := readModel.GetTranscriptWebProjectionFence(
			context.Background(), fixture.ownerID, fixture.streamUID, fixture.branchID,
		)
		if err != nil {
			t.Fatalf("status %q fence error=%v", status, err)
		}
		if fence.OwnerID != fixture.ownerID || fence.SessionID != fixture.sessionID ||
			fence.StreamUID != fixture.streamUID || fence.BranchID != fixture.branchID ||
			fence.BranchGeneration != 1 || fence.ThroughPublicationSequence != 1 ||
			fence.SourceRevision != 0 || !fence.StateFound || fence.StateStatus != status ||
			fence.StateBranchGeneration != 1 || fence.StateThroughPublicationSequence != 1 ||
			fence.StateSourceRevision != 0 || fence.VisibleMessageCount != 7 {
			t.Fatalf("status %q fence=%#v", status, fence)
		}
	}
	if _, err := db.Exec(`DELETE FROM transcript_web_projection_state WHERE stream_uid=? AND branch_id=?`,
		fixture.streamUID, fixture.branchID); err != nil {
		t.Fatal(err)
	}
	missing, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), fixture.ownerID, fixture.streamUID, fixture.branchID,
	)
	if err != nil || missing.StateFound || missing.StateStatus != "" || missing.VisibleMessageCount != 0 ||
		missing.BranchGeneration != 1 || missing.ThroughPublicationSequence != 1 || missing.SourceRevision != 0 {
		t.Fatalf("missing projection fence=%#v err=%v", missing, err)
	}
}

func TestListTranscriptWebProjectionOwnersIsolatedDeduplicatedAndLimited(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	readModel := NewWebReadModelRepository(db, db)

	zCurrent := seedTranscriptWebWorkFrame(t, repo, db, "owner-z", "session-z1", "stream-z-active", 1)
	seedTranscriptWebWorkFrame(t, repo, db, "owner-z", "session-z1", "stream-z-inactive", 2)
	zSecond := seedTranscriptWebWorkFrame(t, repo, db, "owner-z", "session-z2", "stream-z-second", 1)
	aCurrent := seedTranscriptWebWorkFrame(t, repo, db, "owner-a", "session-a", "stream-a", 1)
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-standalone", OwnerID: "owner-standalone", ExternalID: "standalone",
		Kind: StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	assertTranscriptWebOwnerQueryPlan(t, db)

	owners, err := readModel.ListTranscriptWebProjectionOwners(context.Background(), 100)
	if err != nil || !reflect.DeepEqual(owners, []string{"owner-a", "owner-z"}) {
		t.Fatalf("owners=%v err=%v", owners, err)
	}
	again, err := readModel.ListTranscriptWebProjectionOwners(context.Background(), 100)
	if err != nil || !reflect.DeepEqual(again, owners) {
		t.Fatalf("deterministic owners=%v first=%v err=%v", again, owners, err)
	}
	batch, err := readModel.ListTranscriptWebProjectionOwners(context.Background(), 1)
	if err != nil || !reflect.DeepEqual(batch, []string{"owner-a"}) {
		t.Fatalf("owner batch=%v err=%v", batch, err)
	}
	largeLimit, err := readModel.ListTranscriptWebProjectionOwners(context.Background(), 1_000_000)
	if err != nil || !reflect.DeepEqual(largeLimit, owners) {
		t.Fatalf("large owner limit=%v err=%v", largeLimit, err)
	}
	seedTranscriptWebWorkState(t, db, zCurrent, "ready", 0)
	seedTranscriptWebWorkState(t, db, zSecond, "ready", 0)
	seedTranscriptWebWorkState(t, db, aCurrent, "ready", 0)
	owners, err = readModel.ListTranscriptWebProjectionOwners(context.Background(), 100)
	if err != nil || len(owners) != 0 {
		t.Fatalf("ready owners should not consume the work queue: owners=%v err=%v", owners, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: zSecond.streamUID, OwnerID: zSecond.ownerID, ClientMessageID: "user-z-pending",
		PayloadJSON: []byte(`{"text":"pending"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append pending owner event created=%t err=%v", created, err)
	}
	owners, err = readModel.ListTranscriptWebProjectionOwners(context.Background(), 100)
	if err != nil || !reflect.DeepEqual(owners, []string{"owner-z"}) {
		t.Fatalf("pending owner queue=%v err=%v", owners, err)
	}
	if _, err := readModel.ListTranscriptWebProjectionOwners(context.Background(), 0); err == nil {
		t.Fatal("non-positive owner limit was accepted")
	}
}

func TestListTranscriptWebProjectionWorkUsesCanonicalAuthorityAndDeterministicBatches(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	readModel := NewWebReadModelRepository(db, db)

	ready := seedTranscriptWebWorkFrame(t, repo, db, "owner-work", "session-10-ready", "stream-ready", 1)
	seedTranscriptWebWorkState(t, db, ready, "ready", 0)

	missing := seedTranscriptWebWorkFrame(t, repo, db, "owner-work", "session-20-missing", "stream-missing", 1)

	artifactDirty := seedTranscriptWebWorkFrame(t, repo, db, "owner-work", "session-30-artifact", "stream-artifact", 1)
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: artifactDirty.streamUID, OwnerID: artifactDirty.ownerID, ClientMessageID: "user-artifact",
		PayloadJSON: []byte(`{"text":"artifact work"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("artifact user created=%t err=%v", created, err)
	}
	claimResult, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: artifactDirty.streamUID, OwnerID: artifactDirty.ownerID,
		RunnerID: "runner-artifact", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimResult.Claimed {
		t.Fatalf("artifact claim=%#v err=%v", claimResult, err)
	}
	seedTranscriptWebWorkState(t, db, artifactDirty, "ready", 0)
	seedArtifactVersion(
		t, db, artifactDirty.ownerID, artifactDirty.projectID, artifactDirty.rootFrameID,
		artifactDirty.frameID, "artifact-work", "version-work",
	)
	if _, refs, created, err := repo.AppendAssistantEventWithArtifacts(
		context.Background(), AppendAssistantEventWithArtifactsInput{
			Claim: claimResult.Claim, ClientMessageID: "assistant-artifact", Source: EventSourcePayload,
			PayloadJSON: []byte(`{"text":"artifact ready"}`), Destinations: []string{"ws"},
			References: []ArtifactReferenceInput{{
				ArtifactID: "artifact-work", VersionID: "version-work", Relation: ArtifactRelationProduced,
			}},
		},
	); err != nil || !created || len(refs) != 1 {
		t.Fatalf("artifact append refs=%#v created=%t err=%v", refs, created, err)
	}

	dirty := seedTranscriptWebWorkFrame(t, repo, db, "owner-work", "session-31-dirty", "stream-dirty", 1)
	dirtyEvent, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: dirty.streamUID, OwnerID: dirty.ownerID, ClientMessageID: "user-dirty",
		PayloadJSON: []byte(`{"text":"before"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("dirty user created=%t err=%v", created, err)
	}
	seedTranscriptWebWorkState(t, db, dirty, "ready", 0)
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json=? WHERE stream_uid=? AND event_id=?`,
		[]byte(`{"text":"after"}`), dirty.streamUID, dirtyEvent.EventID); err != nil {
		t.Fatal(err)
	}

	stale := seedTranscriptWebWorkFrame(t, repo, db, "owner-work", "session-40-stale", "stream-stale", 1)
	seedTranscriptWebWorkState(t, db, stale, "ready", 0)
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: stale.streamUID, OwnerID: stale.ownerID, ClientMessageID: "user-stale",
		PayloadJSON: []byte(`{"text":"append"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("stale user created=%t err=%v", created, err)
	}

	building := seedTranscriptWebWorkFrame(t, repo, db, "owner-work", "session-50-building", "stream-building", 1)
	seedTranscriptWebWorkState(t, db, building, "building", 0)
	quarantined := seedTranscriptWebWorkFrame(t, repo, db, "owner-work", "session-51-quarantine", "stream-quarantine", 1)
	seedTranscriptWebWorkState(t, db, quarantined, "quarantined", 0)

	active := seedTranscriptWebWorkFrame(t, repo, db, "owner-work", "session-60-inactive", "stream-active", 1)
	seedTranscriptWebWorkState(t, db, active, "ready", 0)
	inactive := seedTranscriptWebWorkFrame(t, repo, db, "owner-work", "session-60-inactive", "stream-inactive", 2)
	foreign := seedTranscriptWebWorkFrame(t, repo, db, "owner-foreign", "session-foreign", "stream-foreign", 1)
	assertTranscriptWebWorkQueryPlan(t, db, "owner-work")

	work, err := readModel.ListTranscriptWebProjectionWork(context.Background(), "owner-work", 100)
	if err != nil {
		t.Fatal(err)
	}
	wantStreams := []string{
		missing.streamUID, artifactDirty.streamUID, dirty.streamUID,
		stale.streamUID, building.streamUID, quarantined.streamUID,
	}
	wantStatuses := []TranscriptWebProjectionWorkStatus{
		TranscriptWebProjectionWorkMissing,
		TranscriptWebProjectionWorkDirty,
		TranscriptWebProjectionWorkDirty,
		TranscriptWebProjectionWorkStale,
		TranscriptWebProjectionWorkNotReady,
		TranscriptWebProjectionWorkNotReady,
	}
	if got := transcriptWebWorkStreams(work); !reflect.DeepEqual(got, wantStreams) {
		t.Fatalf("work streams=%v want=%v work=%#v", got, wantStreams, work)
	}
	if got := transcriptWebWorkStatuses(work); !reflect.DeepEqual(got, wantStatuses) {
		t.Fatalf("work statuses=%v want=%v", got, wantStatuses)
	}
	for _, item := range work {
		if item.OwnerID != "owner-work" || item.SessionID == "" || item.StreamUID == ready.streamUID ||
			item.StreamUID == inactive.streamUID || item.StreamUID == foreign.streamUID ||
			item.BranchGeneration <= 0 || item.ThroughOrdinal < 0 ||
			item.ThroughPublicationSequence < 0 || item.SourceRevision < 0 || item.Reason == "" {
			t.Fatalf("invalid or unauthorized work item=%#v", item)
		}
		if item.Status == TranscriptWebProjectionWorkDirty &&
			(item.DirtyFirstAffectedOrdinal <= 0 || item.DirtyReasonMask <= 0) {
			t.Fatalf("dirty coordinates missing: %#v", item)
		}
	}
	if work[1].DirtyFirstAffectedOrdinal != 2 || work[1].DirtyReasonMask&1 == 0 ||
		work[2].DirtyFirstAffectedOrdinal != 1 || work[2].DirtyReasonMask&2 == 0 {
		t.Fatalf("dirty sources artifact=%#v update=%#v", work[1], work[2])
	}
	if work[3].ThroughOrdinal != 1 || work[3].ThroughPublicationSequence != 1 ||
		work[3].Status != TranscriptWebProjectionWorkStale {
		t.Fatalf("append work=%#v", work[3])
	}
	if work[4].ProjectionStatus != "building" || work[5].ProjectionStatus != "quarantined" ||
		work[5].Reason != "test_projection_failure" {
		t.Fatalf("not-ready work building=%#v quarantined=%#v", work[4], work[5])
	}

	again, err := readModel.ListTranscriptWebProjectionWork(context.Background(), "owner-work", 100)
	if err != nil || !reflect.DeepEqual(again, work) {
		t.Fatalf("deterministic work=%#v first=%#v err=%v", again, work, err)
	}
	batch, err := readModel.ListTranscriptWebProjectionWork(context.Background(), "owner-work", 2)
	if err != nil || !reflect.DeepEqual(batch, work[:2]) {
		t.Fatalf("limited batch=%#v want=%#v err=%v", batch, work[:2], err)
	}
	unboundedCallerLimit, err := readModel.ListTranscriptWebProjectionWork(
		context.Background(), "owner-work", 1_000_000,
	)
	if err != nil || !reflect.DeepEqual(unboundedCallerLimit, work) {
		t.Fatalf("large caller limit work=%#v err=%v", unboundedCallerLimit, err)
	}
	if _, err := readModel.ListTranscriptWebProjectionWork(context.Background(), "owner-work", 0); err == nil {
		t.Fatal("non-positive work limit was accepted")
	}
}

func TestListTranscriptWebProjectionWorkRequeuesOlderQuarantinedProjectorVersion(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	fixture := seedTranscriptWebWorkFrame(t, repo, db, "owner-projector-upgrade", "session-projector-upgrade", "stream-projector-upgrade", 1)
	seedTranscriptWebWorkState(t, db, fixture, "quarantined", 0)
	if _, err := db.Exec(`UPDATE transcript_web_projection_state
		SET projector_version=? WHERE stream_uid=? AND branch_id=?`,
		TranscriptWebProjectorVersion-1, fixture.streamUID, fixture.branchID); err != nil {
		t.Fatal(err)
	}

	readModel := NewWebReadModelRepository(db, db)
	work, err := readModel.ListTranscriptWebProjectionWork(context.Background(), fixture.ownerID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || work[0].StreamUID != fixture.streamUID ||
		work[0].Status != TranscriptWebProjectionWorkStale || work[0].Reason != "projection_projector_version_stale" {
		t.Fatalf("projector upgrade work=%#v", work)
	}
}

func TestListTranscriptWebProjectionWorkRequeuesMatchingQuarantinedProjection(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	fixture := seedTranscriptWebWorkFrame(t, repo, db, "owner-quarantined-current", "session-quarantined-current", "stream-quarantined-current", 1)
	seedTranscriptWebWorkState(t, db, fixture, "quarantined", 0)

	readModel := NewWebReadModelRepository(db, db)
	owners, err := readModel.ListTranscriptWebProjectionOwners(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0] != fixture.ownerID {
		t.Fatalf("quarantined owner not requeued: %#v", owners)
	}
	work, err := readModel.ListTranscriptWebProjectionWork(context.Background(), fixture.ownerID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || work[0].StreamUID != fixture.streamUID ||
		work[0].Status != TranscriptWebProjectionWorkNotReady || work[0].Reason != "test_projection_failure" {
		t.Fatalf("matching quarantined work=%#v", work)
	}
}

type transcriptWebWorkFrameFixture struct {
	ownerID, sessionID, streamUID, branchID string
	projectID, rootFrameID, frameID         string
}

func seedTranscriptWebWorkFrame(
	t *testing.T, repo *Repository, db *sql.DB, ownerID, sessionID, streamUID string, epoch int64,
) transcriptWebWorkFrameFixture {
	t.Helper()
	fixture := transcriptWebWorkFrameFixture{
		ownerID: ownerID, sessionID: sessionID, streamUID: streamUID,
		projectID: "project-" + streamUID, rootFrameID: "root-" + streamUID, frameID: "frame-" + streamUID,
	}
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES(?,?)`, fixture.projectID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES(?,?,?)`,
		fixture.frameID, fixture.projectID, fixture.rootFrameID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: streamUID, OwnerID: ownerID, ExternalID: fixture.frameID, SessionID: sessionID,
		Kind: StreamKindFrameRef, ProjectID: fixture.projectID, RootFrameID: fixture.rootFrameID,
		FrameID: fixture.frameID, Epoch: epoch,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`, streamUID).
		Scan(&fixture.branchID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func seedTranscriptWebWorkState(
	t *testing.T, db *sql.DB, fixture transcriptWebWorkFrameFixture, status string, visibleCount int,
) {
	t.Helper()
	var generation, throughPublication, sourceRevision int64
	if err := db.QueryRow(`SELECT state.generation,head.through_publication_seq,head.source_revision
		FROM transcript_branch_state state
		JOIN transcript_branch_heads head
			ON head.stream_uid=state.stream_uid AND head.branch_id=state.active_branch_id
		WHERE state.stream_uid=?`, fixture.streamUID).
		Scan(&generation, &throughPublication, &sourceRevision); err != nil {
		t.Fatal(err)
	}
	projectorState := []byte(`{"cursor":0}`)
	projectorSHA := sha256.Sum256(projectorState)
	sourceSHA := sha256.Sum256([]byte(fixture.streamUID + "\x00" + fixture.branchID))
	lastError := ""
	if status == "quarantined" {
		lastError = "test_projection_failure"
	}
	if _, err := db.Exec(`INSERT INTO transcript_web_projection_state(
		stream_uid,branch_id,branch_generation,projector_version,projection_revision,
		through_publication_seq,source_revision,message_count,visible_message_count,
		message_artifact_reference_count,projector_state_json,projector_state_sha256,
		source_chain_sha256,status,last_error_code,updated_at)
		VALUES(?,?,?,?,1,?,?,?, ?,0,?,?, ?,?,?,?)`,
		fixture.streamUID, fixture.branchID, generation, TranscriptWebProjectorVersion, throughPublication, sourceRevision,
		visibleCount, visibleCount, string(projectorState), hex.EncodeToString(projectorSHA[:]),
		hex.EncodeToString(sourceSHA[:]), status, lastError, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
}

func transcriptWebWorkStreams(work []TranscriptWebProjectionWork) []string {
	result := make([]string, len(work))
	for index := range work {
		result[index] = work[index].StreamUID
	}
	return result
}

func transcriptWebWorkStatuses(work []TranscriptWebProjectionWork) []TranscriptWebProjectionWorkStatus {
	result := make([]TranscriptWebProjectionWorkStatus, len(work))
	for index := range work {
		result[index] = work[index].Status
	}
	return result
}

func assertTranscriptWebWorkQueryPlan(t *testing.T, db *sql.DB, ownerID string) {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+listTranscriptWebProjectionWorkSQL,
		TranscriptWebProjectorVersion, ownerID, TranscriptWebProjectorVersion, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ownerIndex := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "SEARCH authority") && strings.Contains(detail, "owner_id=?") {
			ownerIndex = true
		}
		if strings.Contains(detail, "SCAN authority") || strings.Contains(detail, "transcript_events") ||
			strings.Contains(detail, "transcript_web_messages") {
			t.Fatalf("work query plan leaves indexed metadata path: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !ownerIndex {
		t.Fatal("work query plan did not start from the owner authority index")
	}
}

func assertTranscriptWebOwnerQueryPlan(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+listTranscriptWebProjectionOwnersSQL, TranscriptWebProjectorVersion, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	authorityIndex, streamIndex := false, false
	details := make([]string, 0, 16)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
		if strings.Contains(detail, "authority USING INDEX") {
			authorityIndex = true
		}
		if strings.Contains(detail, "SEARCH stream") && strings.Contains(detail, "USING") {
			streamIndex = true
		}
		if strings.Contains(detail, "transcript_events") || strings.Contains(detail, "transcript_web_messages") {
			t.Fatalf("owner query plan leaves authority metadata: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !authorityIndex || !streamIndex {
		t.Fatalf("owner query plan lacks indexed authority joins: authority=%t stream=%t plan=%v", authorityIndex, streamIndex, details)
	}
}
