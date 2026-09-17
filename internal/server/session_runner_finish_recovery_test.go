package server

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	taskruns "synon-go/internal/persistence/taskruns"
	workspace "synon-go/internal/persistence/workspace"
)

func newRunnerFinishRecoveryFixture(t *testing.T, sessionID, runnerID string, attempt int) *Server {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{FileRoot: root, Workspace: store})
	now := time.Now().UTC()
	if err := srv.sessionStore.Save(sessionstore.Session{
		ID: sessionID, LastRole: "user", LastUserMessageAt: now.Add(-2 * time.Minute),
		Runner: &sessionstore.Runner{
			RunnerID: runnerID, Status: "running", Attempt: attempt,
			ClaimedAt: now.Add(-time.Minute), LastHeartbeatAt: now.Add(-30 * time.Second),
			ExpiresAt: now.Add(time.Hour),
		},
	}); err != nil {
		t.Fatal(err)
	}
	return srv
}

func runnerFinishRecoveryInput(t *testing.T, srv *Server, sessionID, runnerID, clientID, status, text string, attempt int) map[string]any {
	t.Helper()
	session, found, err := srv.sessionStore.Get(sessionID)
	if err != nil || !found {
		t.Fatalf("load runner claim: found=%t err=%v", found, err)
	}
	return map[string]any{
		"sessionId": sessionID, "runnerId": runnerID, "runnerAttempt": attempt,
		"claimToken": sessionstore.RunnerClaimToken(sessionID, session.Runner),
		"status":     status, "message": text, "afterEventId": int64(3),
		"runId": "run-recovery", "clientMessageId": clientID,
	}
}

func appendRunnerFinishRecoveryEvent(t *testing.T, srv *Server, input map[string]any) *eventjournal.Entry {
	t.Helper()
	message := eventjournal.Message{
		"type": "runner_finished", "role": "system",
		"runnerId": stringValue(input["runnerId"]), "runnerAttempt": int(numberValue(input["runnerAttempt"])),
		"status": stringValue(input["status"]), "text": stringValue(input["message"]),
		"afterEventId": numberValue(input["afterEventId"]),
	}
	entry, _, err := srv.eventJournal.AppendIdempotent(stringValue(input["sessionId"]), message, eventjournal.Metadata{
		RunID: stringValue(input["runId"]), ClientMessageID: stringValue(input["clientMessageId"]),
	})
	if err != nil || entry == nil {
		t.Fatalf("append persisted finish entry=%#v err=%v", entry, err)
	}
	return entry
}

func TestSessionRunnerFinishRecoversPersistedEventThroughFrameAdvance(t *testing.T) {
	const sessionID = "finish-recovery-frame"
	srv := newRunnerFinishRecoveryFixture(t, sessionID, "runner-a", 2)
	if _, err := srv.workspaceStore.CreateProject(workspace.CreateProjectInput{ID: "finish-project", UserID: "local", Name: "Finish"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.workspaceStore.CreateFrame(workspace.CreateFrameInput{
		ID: sessionID, ProjectID: "finish-project", AgentName: "OPERON", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	input := runnerFinishRecoveryInput(t, srv, sessionID, "runner-a", "finish-recovery-event", "completed", "recovered", 2)
	existing := appendRunnerFinishRecoveryEvent(t, srv, input)
	restarted := New(Options{FileRoot: srv.fileRoot, Workspace: srv.workspaceStore})

	result, err := restarted.finishSessionRunner(input)
	if err != nil {
		t.Fatalf("finishSessionRunner() error = %v", err)
	}
	if event := result["event"].(*eventjournal.Entry); event.EventID != existing.EventID {
		t.Fatalf("recovered event=%#v existing=%#v", event, existing)
	}
	session, found, err := restarted.sessionStore.Get(sessionID)
	if err != nil || !found || session.Runner == nil || session.Runner.Status != "completed" || session.Runner.LastCheckpointEventID != existing.EventID {
		t.Fatalf("recovered session=%#v found=%t err=%v", session, found, err)
	}
	frame, found, err := restarted.workspaceStore.GetFrame(sessionID)
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("advanced frame=%#v found=%t err=%v", frame, found, err)
	}
	entries, err := restarted.eventJournal.ReadAll(sessionID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("finish journal entries=%#v err=%v", entries, err)
	}
}

func TestSessionRunnerFinishRecoversAfterSessionFinalizeBeforeFrameAdvance(t *testing.T) {
	const sessionID = "finish-recovery-finalized"
	srv := newRunnerFinishRecoveryFixture(t, sessionID, "runner-a", 2)
	if _, err := srv.workspaceStore.CreateProject(workspace.CreateProjectInput{ID: "finish-finalized-project", UserID: "local", Name: "Finish"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.workspaceStore.CreateFrame(workspace.CreateFrameInput{
		ID: sessionID, ProjectID: "finish-finalized-project", AgentName: "OPERON", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	input := runnerFinishRecoveryInput(t, srv, sessionID, "runner-a", "finish-finalized-event", "completed", "recovered", 2)
	entry := appendRunnerFinishRecoveryEvent(t, srv, input)
	finishedAt, err := time.Parse(time.RFC3339Nano, entry.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, applied, err := srv.sessionStore.FinalizeRunner(sessionstore.FinalizeRunnerInput{
		SessionID: sessionID, RunnerID: "runner-a", Attempt: 2, Status: "completed",
		Checkpoint: "recovered", CheckpointAt: finishedAt, EventID: entry.EventID, FinishedAt: finishedAt,
	}); err != nil || !applied {
		t.Fatalf("seed finalized session applied=%t err=%v", applied, err)
	}
	if _, err := srv.finishSessionRunner(input); err != nil {
		t.Fatal(err)
	}
	frame, found, err := srv.workspaceStore.GetFrame(sessionID)
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("recovered frame=%#v found=%t err=%v", frame, found, err)
	}
}

func TestSessionRunnerFinishRejectsReclaimedSameRunnerAttemptBeforeAppend(t *testing.T) {
	srv := newRunnerFinishRecoveryFixture(t, "finish-stale", "runner-a", 2)
	input := runnerFinishRecoveryInput(t, srv, "finish-stale", "runner-a", "stale-finish", "completed", "stale", 1)
	if _, err := srv.finishSessionRunner(input); !errors.Is(err, sessionstore.ErrRunnerClaimStale) {
		t.Fatalf("stale finish error = %v", err)
	}
	entries, err := srv.eventJournal.ReadAll("finish-stale")
	if err != nil || len(entries) != 0 {
		t.Fatalf("stale finish persisted entries=%#v err=%v", entries, err)
	}
	session, _, _ := srv.sessionStore.Get("finish-stale")
	if session.Runner == nil || session.Runner.Attempt != 2 || session.Runner.Status != "running" {
		t.Fatalf("stale finish changed active runner: %#v", session.Runner)
	}
}

func TestSessionRunnerFinishRejectsPersistedPayloadConflict(t *testing.T) {
	srv := newRunnerFinishRecoveryFixture(t, "finish-conflict", "runner-a", 1)
	input := runnerFinishRecoveryInput(t, srv, "finish-conflict", "runner-a", "finish-conflict-id", "completed", "canonical", 1)
	appendRunnerFinishRecoveryEvent(t, srv, input)
	conflict := runnerFinishRecoveryInput(t, srv, "finish-conflict", "runner-a", "finish-conflict-id", "failed", "different", 1)
	if _, err := srv.finishSessionRunner(conflict); !errors.Is(err, eventjournal.ErrIdempotencyConflict) {
		t.Fatalf("payload conflict error = %v", err)
	}
	session, _, _ := srv.sessionStore.Get("finish-conflict")
	if session.Runner == nil || session.Runner.Status != "running" {
		t.Fatalf("payload conflict finalized session: %#v", session.Runner)
	}
}

func TestSessionRunnerFinishConcurrentRetryConvergesOneEvent(t *testing.T) {
	srv := newRunnerFinishRecoveryFixture(t, "finish-concurrent", "runner-a", 1)
	input := runnerFinishRecoveryInput(t, srv, "finish-concurrent", "runner-a", "finish-concurrent-id", "completed", "done", 1)
	start := make(chan struct{})
	errs := make(chan error, 32)
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := srv.finishSessionRunner(input)
			errs <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent finish error = %v", err)
		}
	}
	entries, err := srv.eventJournal.ReadAll("finish-concurrent")
	if err != nil || len(entries) != 1 {
		t.Fatalf("concurrent finish entries=%#v err=%v", entries, err)
	}
	session, _, _ := srv.sessionStore.Get("finish-concurrent")
	if session.Runner == nil || session.Runner.Status != "completed" || session.Runner.LastCheckpointEventID != entries[0].EventID {
		t.Fatalf("concurrent finish session=%#v", session)
	}
}

func TestSessionRunnerFinishRequiresExplicitAttemptAndClaimToken(t *testing.T) {
	srv := newRunnerFinishRecoveryFixture(t, "finish-internal", "runner-a", 3)
	input := runnerFinishRecoveryInput(t, srv, "finish-internal", "runner-a", "", "completed", "internal", 3)
	input["clientMessageId"] = "explicit-internal-finish"
	if _, err := srv.finishSessionRunner(input); err != nil {
		t.Fatalf("explicit internal finish error = %v", err)
	}

	blocked := newRunnerFinishRecoveryFixture(t, "finish-public", "runner-a", 1)
	missing := runnerFinishRecoveryInput(t, blocked, "finish-public", "runner-a", "arbitrary-public-id", "completed", "blocked", 1)
	delete(missing, "runnerAttempt")
	if _, err := blocked.finishSessionRunner(missing); err == nil || err.Error() != "session_runner_finish.runnerAttempt must be a positive integer" {
		t.Fatalf("missing public attempt error = %v", err)
	}
	entries, err := blocked.eventJournal.ReadAll("finish-public")
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing attempt persisted entries=%#v err=%v", entries, err)
	}
}

func TestSessionRunnerTerminalCheckpointDoesNotEndActiveAttempt(t *testing.T) {
	srv := newRunnerFinishRecoveryFixture(t, "finish-checkpoint-phase", "runner-a", 1)
	claimedSession, found, err := srv.sessionStore.Get("finish-checkpoint-phase")
	if err != nil || !found {
		t.Fatalf("load runner claim: found=%t err=%v", found, err)
	}
	claim := sessionstore.RunnerClaimFromSession(claimedSession)
	checkpoint, err := srv.checkpointSessionRunner(map[string]any{
		"sessionId": "finish-checkpoint-phase", "runnerId": "runner-a", "status": "completed",
		"runnerAttempt": claim.Attempt, "claimToken": claim.ClaimToken,
		"message": "tool phase completed", "clientMessageId": "finish-checkpoint-phase-event",
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := checkpoint["event"].(*eventjournal.Entry)
	if stringValue(entry.Message["status"]) != "completed" {
		t.Fatalf("checkpoint event status=%#v", entry.Message)
	}
	session, found, err := srv.sessionStore.Get("finish-checkpoint-phase")
	if err != nil || !found || session.Runner == nil || session.Runner.Status != "running" || session.Runner.LastCheckpointEventID != entry.EventID {
		t.Fatalf("checkpoint runner session=%#v found=%t err=%v", session, found, err)
	}
	if _, renewed, err := srv.sessionStore.HeartbeatRunner(claim, time.Minute); err != nil || !renewed {
		t.Fatalf("checkpoint runner heartbeat renewed=%t err=%v", renewed, err)
	}
}

func TestSessionRunnerFinishConvergesLegacyTerminalCheckpointState(t *testing.T) {
	srv := newRunnerFinishRecoveryFixture(t, "finish-legacy-checkpoint", "runner-a", 1)
	checkpoint, err := srv.eventJournal.Append("finish-legacy-checkpoint", eventjournal.Message{
		"type": "runner_checkpoint", "role": "system", "runnerId": "runner-a", "runnerAttempt": 1,
		"status": "failed", "text": "legacy tool failure",
	}, eventjournal.Metadata{ClientMessageID: "legacy-terminal-checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, _ := srv.sessionStore.Get("finish-legacy-checkpoint")
	session.Runner.Status = "failed"
	session.Runner.LastCheckpoint = "legacy tool failure"
	session.Runner.LastCheckpointEventID = checkpoint.EventID
	session.Runner.LastCheckpointAt = time.Now().UTC()
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	input := runnerFinishRecoveryInput(t, srv, "finish-legacy-checkpoint", "runner-a", "legacy-checkpoint-finish", "completed", "recovered", 1)
	session.Runner.LastCheckpoint = "tampered checkpoint"
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.finishSessionRunner(input); !errors.Is(err, sessionstore.ErrRunnerFinalizeConflict) {
		t.Fatalf("tampered checkpoint error = %v", err)
	}
	session.Runner.LastCheckpoint = "legacy tool failure"
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.finishSessionRunner(input); err != nil {
		t.Fatalf("finish legacy terminal checkpoint: %v", err)
	}
}

func TestSessionRunnerFinishConvergesTaskRunMirrorExactlyOnce(t *testing.T) {
	srv := newRunnerFinishRecoveryFixture(t, "unused", "runner-a", 1)
	started, err := srv.executeTaskRunTool(map[string]any{
		"action": "start", "objective": "verify finish recovery",
		"success_criteria": []any{"runner finish is mirrored exactly once"},
		"task_graph": map[string]any{"steps": []any{map[string]any{
			"id": "agent", "title": "Agent", "description": "finish",
			"executor": map[string]any{"kind": "agent", "agent_type": "general-purpose"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, ok := started.(taskruns.Record)
	if !ok || run.SessionID == "" {
		t.Fatalf("started TaskRun = %#v", started)
	}
	claimed, claimedOK, err := srv.sessionStore.ClaimRunner(run.SessionID, "runner-task", time.Minute)
	if err != nil || !claimedOK || claimed.Runner == nil {
		t.Fatalf("claim TaskRun session=%#v claimed=%t err=%v", claimed, claimedOK, err)
	}
	input := runnerFinishRecoveryInput(t, srv, run.SessionID, "runner-task", "taskrun-finish-recovery", "completed", "task done", claimed.Runner.Attempt)
	input["runId"] = run.RunID
	entry := appendRunnerFinishRecoveryEvent(t, srv, input)
	if err := srv.mirrorTaskRunRunnerEvent(run.SessionID, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.finishSessionRunner(input); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.finishSessionRunner(input); err != nil {
		t.Fatal(err)
	}
	updated, found, err := srv.taskRunStore.Get(run.RunID)
	if err != nil || !found {
		t.Fatalf("get TaskRun found=%t err=%v", found, err)
	}
	count := 0
	for _, trace := range updated.ExecutionTrace {
		if trace.Event == "runner_finished" && numberValue(trace.Metadata["eventId"]) == entry.EventID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("runner finish trace count=%d trace=%#v", count, updated.ExecutionTrace)
	}
}

func TestSessionRunnerFinishRetriesAfterTaskRunMirrorStorageFailure(t *testing.T) {
	srv := newRunnerFinishRecoveryFixture(t, "unused-storage-failure", "runner-a", 1)
	started, err := srv.executeTaskRunTool(map[string]any{
		"action": "start", "objective": "recover mirror storage failure",
		"success_criteria": []any{"finish converges after storage recovery"},
		"task_graph": map[string]any{"steps": []any{map[string]any{
			"id": "agent", "title": "Agent", "description": "finish",
			"executor": map[string]any{"kind": "agent", "agent_type": "general-purpose"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := started.(taskruns.Record)
	claimed, claimedOK, err := srv.sessionStore.ClaimRunner(run.SessionID, "runner-task", time.Minute)
	if err != nil || !claimedOK || claimed.Runner == nil {
		t.Fatalf("claim=%#v claimed=%t err=%v", claimed, claimedOK, err)
	}
	input := runnerFinishRecoveryInput(t, srv, run.SessionID, "runner-task", "taskrun-storage-recovery", "completed", "task done", claimed.Runner.Attempt)
	input["runId"] = run.RunID
	runPath := filepath.Join(srv.fileRoot, "task_runs", run.RunID+".json")
	original, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.finishSessionRunner(input); err == nil {
		t.Fatal("finish unexpectedly succeeded while TaskRun storage was corrupt")
	}
	session, _, _ := srv.sessionStore.Get(run.SessionID)
	entries, readErr := srv.eventJournal.ReadAll(run.SessionID)
	finishCount := 0
	for _, entry := range entries {
		if stringValue(entry.Message["type"]) == "runner_finished" {
			finishCount++
		}
	}
	if readErr != nil || session.Runner == nil || session.Runner.Status != "running" || finishCount != 1 {
		t.Fatalf("partial finish session=%#v entries=%#v err=%v", session, entries, readErr)
	}
	if err := os.WriteFile(runPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.finishSessionRunner(input); err != nil {
		t.Fatalf("finish retry after storage recovery: %v", err)
	}
	updated, found, err := srv.taskRunStore.Get(run.RunID)
	if err != nil || !found || len(updated.ExecutionTrace) == 0 {
		t.Fatalf("recovered TaskRun=%#v found=%t err=%v", updated, found, err)
	}
}
