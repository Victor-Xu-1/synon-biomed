package workspace

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestCancelFrameWithTranscriptCommitsOneTerminalAuthority(t *testing.T) {
	store, repo, stream, claim := newTranscriptCancellationFixture(t)
	if _, err := store.SetFrameRuntimeMetadata("frame-a", FrameRuntimeMetadata{ContextData: map[string]any{
		"_pending_input_requests": []any{map[string]any{"requestId": "stale-approval"}},
		"preserved":               "yes",
	}}); err != nil {
		t.Fatal(err)
	}
	frame, event, err := store.CancelFrameWithTranscript(context.Background(), "frame-a")
	if err != nil || event == nil || frame.Status != "cancelled" || event.Type != "frame_cancelled" {
		t.Fatalf("frame=%#v event=%#v err=%v", frame, event, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Attempt)
	if err != nil || state.Status != "cancelled" || state.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	var frameEvents, terminals, receipts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame-a'`).Scan(&frameEvents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, stream.UID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, stream.UID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if frameEvents != 1 || terminals != 1 || receipts != 1 {
		t.Fatalf("counts frame=%d terminal=%d receipt=%d", frameEvents, terminals, receipts)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-a")
	if err != nil || !found || metadata.ContextData["_pending_input_requests"] != nil ||
		metadata.ContextData["preserved"] != "yes" {
		t.Fatalf("cancelled metadata=%#v found=%t err=%v", metadata.ContextData, found, err)
	}
	frame, event, err = store.CancelFrameWithTranscript(context.Background(), "frame-a")
	if err != nil || event != nil || frame.Status != "cancelled" {
		t.Fatalf("idempotent frame=%#v event=%#v err=%v", frame, event, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, stream.UID).Scan(&terminals); err != nil || terminals != 1 {
		t.Fatalf("idempotent terminals=%d err=%v", terminals, err)
	}
}

func TestCancelFrameWithTranscriptRollsBackFrameWhenTerminalReceiptFails(t *testing.T) {
	store, _, stream, claim := newTranscriptCancellationFixture(t)
	if _, err := store.db.Exec(`DROP TABLE transcript_runner_receipts`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CancelFrameWithTranscript(context.Background(), "frame-a"); err == nil {
		t.Fatal("cancellation succeeded without terminal receipt storage")
	}
	frame, found, err := store.GetFrame("frame-a")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	var frameEvents, terminals int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame-a'`).Scan(&frameEvents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, stream.UID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`, stream.UID, claim.Attempt).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if frameEvents != 0 || terminals != 0 || status != "running" {
		t.Fatalf("rollback frameEvents=%d terminals=%d runner=%q", frameEvents, terminals, status)
	}
}

func TestCancelFrameWithTranscriptCreatesTerminalAttemptBeforeAdmission(t *testing.T) {
	store, repo, stream, claim := newTranscriptCancellationFixture(t)
	if _, err := store.db.Exec(`DELETE FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`, stream.UID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	frame, event, err := store.CancelFrameWithTranscript(context.Background(), "frame-a")
	if err != nil || event == nil || frame.Status != "cancelled" {
		t.Fatalf("frame=%#v event=%#v err=%v", frame, event, err)
	}
	state, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || state.Attempt != 1 || state.Status != "cancelled" || state.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("state=%#v found=%t err=%v", state, found, err)
	}
	var terminals, receipts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, stream.UID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, stream.UID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if terminals != 1 || receipts != 1 {
		t.Fatalf("terminals=%d receipts=%d", terminals, receipts)
	}
}

func TestCancelCompatibilityFrameTreeWithTranscriptIsAtomicAcrossChildren(t *testing.T) {
	store, repo, rootStream, _ := newTranscriptCancellationFixture(t)
	childStream, _ := addTranscriptCancellationFrame(t, store, repo, "frame-child", "frame-a")
	for _, frameID := range []string{"frame-a", "frame-child"} {
		if _, err := store.SetFrameRuntimeMetadata(frameID, FrameRuntimeMetadata{ContextData: map[string]any{
			"_pending_input_requests": []any{map[string]any{"requestId": "pending-" + frameID}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.CancelCompatibilityFrameTreeWithTranscript(context.Background(), "frame-a", "stop tree")
	if err != nil || len(result.CancelledFrameIDs) != 2 || len(result.Events) != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, stream := range []transcriptstore.Stream{rootStream, childStream} {
		state, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID)
		if err != nil || !found || state.Status != "cancelled" {
			t.Fatalf("stream=%s state=%#v found=%t err=%v", stream.UID, state, found, err)
		}
	}
	var cancelledFrames, receipts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frames WHERE root_frame_id='frame-a' AND status='cancelled'`).Scan(&cancelledFrames); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE status='cancelled'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if cancelledFrames != 2 || receipts != 2 {
		t.Fatalf("cancelled frames=%d receipts=%d", cancelledFrames, receipts)
	}
	for _, frameID := range []string{"frame-a", "frame-child"} {
		metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
		if err != nil || !found || metadata.ContextData["_pending_input_requests"] != nil {
			t.Fatalf("frame=%s cancelled metadata=%#v found=%t err=%v", frameID, metadata.ContextData, found, err)
		}
	}
}

func TestCancelCompatibilityFrameTreeWithTranscriptRollsBackWholeTree(t *testing.T) {
	store, repo, _, _ := newTranscriptCancellationFixture(t)
	_, _ = addTranscriptCancellationFrame(t, store, repo, "frame-child", "frame-a")
	if _, err := store.db.Exec(`DROP TABLE transcript_runner_receipts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelCompatibilityFrameTreeWithTranscript(context.Background(), "frame-a", "stop tree"); err == nil {
		t.Fatal("tree cancellation succeeded without terminal receipt storage")
	}
	var processing, cancelledEvents, runningAttempts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frames WHERE root_frame_id='frame-a' AND status='processing'`).Scan(&processing); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE event_type='frame_cancelled'`).Scan(&cancelledEvents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE status='running'`).Scan(&runningAttempts); err != nil {
		t.Fatal(err)
	}
	if processing != 2 || cancelledEvents != 0 || runningAttempts != 2 {
		t.Fatalf("processing=%d cancelEvents=%d running=%d", processing, cancelledEvents, runningAttempts)
	}
}

func newTranscriptCancellationFixture(t *testing.T) (*Store, *transcriptstore.Repository, transcriptstore.Stream, transcriptstore.RunnerClaim) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-a", ProjectID: "project-a", AgentName: "agent", Status: "processing", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-a", OwnerID: "owner-a", ExternalID: "frame-a", SessionID: "frame-a",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-a", RootFrameID: "frame-a", FrameID: "frame-a", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-a", FrameEventID: "frame-user-a",
		MessageUUID: "message-a", Text: "cancel this run", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	return store, repo, stream, claimed.Claim
}

func addTranscriptCancellationFrame(
	t *testing.T,
	store *Store,
	repo *transcriptstore.Repository,
	frameID, parentFrameID string,
) (transcriptstore.Stream, transcriptstore.RunnerClaim) {
	t.Helper()
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: frameID, ProjectID: "project-a", ParentFrameID: parentFrameID,
		AgentName: "agent", Status: "processing", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: "owner-a", ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-a", RootFrameID: "frame-a", FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-" + frameID,
		FrameEventID: "frame-user-" + frameID, MessageUUID: "message-" + frameID,
		Text: "cancel " + frameID, Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append %s created=%t err=%v", frameID, created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-" + frameID,
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim %s=%#v err=%v", frameID, claimed, err)
	}
	return stream, claimed.Claim
}
