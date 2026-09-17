package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"synon-go/internal/memoryconfig"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRunnerTaskMemorySnapshotSurvivesLeaseRecoveryWithoutRecallDrift(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-a", "frame-a")
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "memory-original", UserID: "local", SubjectProjectID: "project-a",
		Body: "EGFR original accepted evidence", Origin: "user", Evidence: "stated",
	}); err != nil {
		t.Fatal(err)
	}
	config := memoryconfig.Default()
	config.Enabled = true
	server := &Server{workspaceStore: store, transcriptStore: repo, memoryConfig: config}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-a", MessageUUID: "message-a", ClientMessageID: "client-a",
		Text: "analyze EGFR with remembered evidence", RuntimeConfig: map[string]any{"memory_mode": "on"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-a")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	intent, found, err := repo.EnsureActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-a",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	session := sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}}
	messages := []chatCompletionMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "analyze EGFR original accepted evidence"}}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}}
	firstWorkspace, err := server.runnerTaskMemoryContext(
		context.Background(), session, messages, run, intent.ID, intent.Revision,
	)
	if err != nil || !strings.Contains(firstWorkspace, "memory-original") {
		t.Fatalf("first workspace memory=%q err=%v", firstWorkspace, err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), stream.UID, claimed.Claim.Attempt); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := repo.LatestResumableCheckpoint(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "memory-later", UserID: "local", SubjectProjectID: "project-a",
		Body: "EGFR later memory must not enter the running task", Origin: "user", Evidence: "stated",
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-b", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed || resumed.Claim.Attempt != claimed.Claim.Attempt {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
	run.Transcript.Claim = resumed.Claim
	resumedWorkspace, err := server.runnerTaskMemoryContext(
		context.Background(), session, messages, run, intent.ID, intent.Revision,
	)
	if err != nil || resumedWorkspace != firstWorkspace || strings.Contains(resumedWorkspace, "memory-later") {
		t.Fatalf("resumed workspace memory=%q first=%q err=%v", resumedWorkspace, firstWorkspace, err)
	}
}

func TestRunnerTaskMemoryCheckpointResumeFallsBackToLatestWhenAttemptHasNoSnapshot(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-a", "frame-a")
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "memory-original", UserID: "local", SubjectProjectID: "project-a",
		Body: "EGFR original accepted evidence", Origin: "user", Evidence: "stated",
	}); err != nil {
		t.Fatal(err)
	}
	config := memoryconfig.Default()
	config.Enabled = true
	server := &Server{workspaceStore: store, transcriptStore: repo, memoryConfig: config}
	ctx := context.Background()
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-a", MessageUUID: "message-a", ClientMessageID: "client-a",
		Text: "analyze EGFR with remembered evidence", RuntimeConfig: map[string]any{"memory_mode": "on"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(ctx, "local", "frame-a")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	intent, found, err := repo.EnsureActiveFrameTaskIntent(ctx, stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	claimed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-a",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	session := sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}}
	messages := []chatCompletionMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "analyze EGFR original accepted evidence"}}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}}
	firstWorkspace, err := server.runnerTaskMemoryContext(
		ctx, session, messages, run, intent.ID, intent.Revision,
	)
	if err != nil || !strings.Contains(firstWorkspace, "memory-original") {
		t.Fatalf("first workspace memory=%q err=%v", firstWorkspace, err)
	}
	if _, err := server.finishTranscriptRunner(ctx, run.Transcript, "completed", "test completed"); err != nil {
		t.Fatal(err)
	}
	// A second attempt for the same task dies before the model stage, leaving
	// only a resumable checkpoint and no snapshot for its attempt. Such state
	// can arise when an AskUser response admits a new input revision and the
	// resumed attempt fails during provider replay before sealing memory.
	var nextEventID, nextPublication, checkpointSequence int64
	if err := db.QueryRow(`SELECT next_event_id,next_publication_seq,next_checkpoint_sequence
		FROM transcript_streams WHERE stream_uid=?`, stream.UID).Scan(&nextEventID, &nextPublication, &checkpointSequence); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// The attempt and its terminal event reference each other through foreign
	// keys, so the attempt must start as a running row, the event is appended,
	// and only then is the attempt settled as failed.
	if _, err := db.Exec(`INSERT INTO transcript_runner_attempts(
		stream_uid,attempt,runner_id,claim_token_sha256,claimed_input_revision,
		resume_source,resume_checkpoint_sequence,status,phase,phase_sequence,
		last_checkpoint_sequence,claimed_at,expires_at
	) VALUES(?,2,?,?,1,'fresh',0,'running','planning',1,0,?,?)`,
		stream.UID, "runner-b", make([]byte, 32), now.Add(-time.Hour), now.Add(-time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_events(
		stream_uid,event_id,publication_seq,client_message_id,event_type,source,
		runner_attempt,payload_json,created_at
	) VALUES(?,?,?,'manual-attempt-2','runner_finished','payload',2,'{}',?)`,
		stream.UID, nextEventID, nextPublication, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_streams SET next_event_id=?,next_publication_seq=? WHERE stream_uid=?`,
		nextEventID+1, nextPublication+1, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts
		SET status='failed',phase='terminal',phase_sequence=phase_sequence+1,
			last_checkpoint_sequence=?,finished_event_id=?,finished_at=?
		WHERE stream_uid=? AND attempt=2`,
		checkpointSequence, nextEventID, now.Add(-time.Minute), stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_runner_checkpoints(
		stream_uid,checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at
	) VALUES(?,?,2,?,'executing',1,?)`, stream.UID, checkpointSequence, nextEventID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_streams SET next_checkpoint_sequence=? WHERE stream_uid=?`,
		checkpointSequence+1, stream.UID); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := repo.LatestResumableCheckpoint(ctx, stream.UID, stream.OwnerID)
	if err != nil || !found || checkpoint.Attempt != 2 {
		t.Fatalf("checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}
	// The resume control resets the completed frame and records the
	// authorized dispatch before the runner claims the checkpoint.
	status := "processing"
	if _, err := store.UpdateFrame("frame-a", workspace.UpdateFrameInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	resumeEvent, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "frame-a", Type: "frame_resumed",
		Payload: map[string]any{"dispatch": map[string]any{"status": "claimed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "memory-later", UserID: "local", SubjectProjectID: "project-a",
		Body: "EGFR later memory must not enter the running task", Origin: "user", Evidence: "stated",
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "frame-resume:" + resumeEvent.ID,
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed || resumed.Claim.Attempt != 3 {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
	run.Transcript.Claim = resumed.Claim
	resumedWorkspace, err := server.runnerTaskMemoryContext(
		ctx, session, messages, run, intent.ID, intent.Revision,
	)
	if err != nil || resumedWorkspace != firstWorkspace || strings.Contains(resumedWorkspace, "memory-later") {
		t.Fatalf("resumed workspace memory=%q first=%q err=%v", resumedWorkspace, firstWorkspace, err)
	}
	snapshot, found, err := repo.GetRunnerTaskMemorySnapshot(
		ctx, stream.UID, stream.OwnerID, resumed.Claim.Attempt,
	)
	if err != nil || !found || snapshot.TaskIntentID != intent.ID || snapshot.TaskIntentRevision != intent.Revision {
		t.Fatalf("resumed attempt snapshot=%#v found=%t err=%v", snapshot, found, err)
	}
}
