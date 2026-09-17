package transcript

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestFinishRunnerProjectsAuthoritativeFailureDescription(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	stream := seedTerminalPresentationFrame(t, repo, db, "failure")
	finishedAt := time.Date(2026, 8, 11, 3, 4, 5, 0, time.UTC)
	repo.now = func() time.Time { return finishedAt }
	claim := claimTerminalPresentationRunner(t, repo, stream)

	finish := FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish-failure", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"transcript event conflicts with durable state"}`),
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), finish); err != nil || !created {
		t.Fatalf("finish failed runner created=%t err=%v", created, err)
	}
	assertTerminalPresentation(t, db, stream.FrameID, "failed", "transcript event conflicts with durable state")
	assertTerminalCompletedAt(t, db, stream.FrameID, &finishedAt)

	repo.now = func() time.Time { return finishedAt.Add(time.Hour) }
	if _, _, created, err := repo.FinishRunner(context.Background(), finish); err != nil || created {
		t.Fatalf("idempotent finish created=%t err=%v", created, err)
	}
	assertTerminalPresentation(t, db, stream.FrameID, "failed", "transcript event conflicts with durable state")
	assertTerminalCompletedAt(t, db, stream.FrameID, &finishedAt)
}

func TestFinishRunnerClearsStaleFailureDescriptionAfterCompletion(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	stream := seedTerminalPresentationFrame(t, repo, db, "completion")
	finishedAt := time.Date(2026, 8, 11, 6, 7, 8, 0, time.UTC)
	repo.now = func() time.Time { return finishedAt }
	if _, err := db.Exec(`INSERT INTO frame_runtime_metadata(frame_id,context_data,status_description)
		VALUES(?, '{}', 'stale failure')`, stream.FrameID); err != nil {
		t.Fatal(err)
	}
	claim := claimTerminalPresentationRunner(t, repo, stream)
	if _, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish-completion", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"task completed"}`),
	}); err != nil || !created {
		t.Fatalf("finish completed runner created=%t err=%v", created, err)
	}
	assertTerminalPresentation(t, db, stream.FrameID, "completed", "")
	assertTerminalCompletedAt(t, db, stream.FrameID, &finishedAt)

	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "input-completion-next",
		FrameEventID: "frame-input-completion-next", MessageUUID: "message-completion-next",
		MessageOrigin: "task_intent", Text: "Continue with a new durable task.",
	}); err != nil || !created {
		t.Fatalf("append next task created=%t err=%v", created, err)
	}
	assertTerminalPresentation(t, db, stream.FrameID, "processing", "")
	assertTerminalCompletedAt(t, db, stream.FrameID, nil)
}

func assertTerminalCompletedAt(t *testing.T, db *sql.DB, frameID string, want *time.Time) {
	t.Helper()
	var completedAt sql.NullTime
	if err := db.QueryRow(`SELECT completed_at FROM frame_runtime_metadata WHERE frame_id=?`, frameID).Scan(&completedAt); err != nil {
		t.Fatal(err)
	}
	if want == nil {
		if completedAt.Valid {
			t.Fatalf("terminal completed_at=%s want NULL", completedAt.Time.UTC())
		}
		return
	}
	if !completedAt.Valid || !completedAt.Time.UTC().Equal(want.UTC()) {
		t.Fatalf("terminal completed_at=%v want=%s", completedAt, want.UTC())
	}
}

func seedTerminalPresentationFrame(t *testing.T, repo *Repository, db *sql.DB, suffix string) Stream {
	t.Helper()
	projectID := "project-terminal-presentation-" + suffix
	frameID := "frame-terminal-presentation-" + suffix
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES(?,?)`, projectID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,incarnation_id,project_id,root_frame_id,status,updated_at)
		VALUES(?,?,?,?,?,?)`, frameID, "incarnation-"+suffix, projectID, frameID, "processing", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-terminal-presentation-" + suffix, OwnerID: "owner", ExternalID: frameID,
		SessionID: frameID, Kind: StreamKindFrameRef, ProjectID: projectID,
		RootFrameID: frameID, FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "input-" + suffix,
		FrameEventID: "frame-input-" + suffix, MessageUUID: "message-" + suffix,
		MessageOrigin: "task_intent", Text: "Run the durable task.",
	}); err != nil || !created {
		t.Fatalf("append frame input created=%t err=%v", created, err)
	}
	return stream
}

func claimTerminalPresentationRunner(t *testing.T, repo *Repository, stream Stream) RunnerClaim {
	t.Helper()
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-" + stream.FrameID,
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim runner=%#v err=%v", claimed, err)
	}
	return claimed.Claim
}

func assertTerminalPresentation(t *testing.T, db *sql.DB, frameID, wantStatus, wantDescription string) {
	t.Helper()
	var status, description string
	if err := db.QueryRow(`SELECT frames.status,COALESCE(metadata.status_description,'')
		FROM frames LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id=frames.id
		WHERE frames.id=?`, frameID).Scan(&status, &description); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || description != wantDescription {
		t.Fatalf("terminal presentation status=%q description=%q want status=%q description=%q",
			status, description, wantStatus, wantDescription)
	}
}
