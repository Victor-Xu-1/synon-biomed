package transcript

import (
	"context"
	"testing"
	"time"
)

func TestFramePendingInputStateUsesPrimaryRuntimeAuthority(t *testing.T) {
	repository, database, _ := newTranscriptRepository(t)
	if _, err := database.Exec(`
		INSERT INTO projects(id,user_id) VALUES('project-a','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES('frame-pending-input','project-a','frame-pending-input','processing');`); err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:pending-input", OwnerID: "owner-a", ExternalID: "frame-pending-input",
		SessionID: "frame-pending-input", Kind: StreamKindFrameRef, ProjectID: "project-a",
		RootFrameID: "frame-pending-input", FrameID: "frame-pending-input", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE transcript_streams SET input_revision=4,consumed_input_revision=3 WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "pending-input-runner", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed || claim.Claim.ClaimedInputRevision != 4 {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if _, err := database.Exec(`UPDATE transcript_streams SET input_revision=5 WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatal(err)
	}
	state, found, err := repository.GetFramePendingInputState(context.Background(), stream.OwnerID, stream.SessionID)
	if err != nil || !found || state.InputRevision != 5 || state.ConsumedInputRevision != 3 ||
		!state.HasRunnerAttempt || state.LatestClaimedInputRevision != 4 {
		t.Fatalf("state=%#v found=%t err=%v", state, found, err)
	}
}
