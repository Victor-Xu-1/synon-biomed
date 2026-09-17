package server

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestSingleFrameProjectionRecoversLegacyResumeFailureDescription(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-failure-description", UserID: "local", Name: "Failure description",
	}); err != nil {
		t.Fatal(err)
	}
	frameID := "frame-failure-description"
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: "project-failure-description", AgentName: "OPERON",
		Status: "failed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.ResumeCompatibilityFrameConversation(frameID, workspace.ResumeCompatibilityFrameInput{})
	if err != nil || resumed.Event == nil {
		t.Fatalf("resume frame result=%#v err=%v", resumed, err)
	}
	claimed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-failure-description", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim dispatch=%#v ok=%t err=%v", claimed, ok, err)
	}
	detail := "read completion recovery candidate: transcript event conflicts with durable state"
	if _, _, err := store.CompleteCompatibilityFrameResumeDispatch(workspace.CompleteCompatibilityFrameResumeDispatchInput{
		ResumeEventID: claimed.ResumeEvent.ID, ExpectedAttempt: claimed.Attempt,
		ClaimToken: claimed.ClaimToken, Status: "failed", Message: detail,
	}); err != nil {
		t.Fatal(err)
	}
	blank := ""
	if err := store.UpdateFrameRuntimePresentation(frameID, workspace.FrameRuntimePresentationInput{
		StatusDescription: &blank,
	}); err != nil {
		t.Fatal(err)
	}

	app := New(Options{Workspace: store, FileRoot: root}).Handler()
	projected := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	if projected["status"] != "failed" || projected["status_description"] != detail {
		t.Fatalf("single-frame failure projection=%#v", projected)
	}
}

func TestSingleFrameProjectionUsesTranscriptTerminalFailureDetail(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	projectID, frameID := "project-transcript-failure", "frame-transcript-failure"
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: projectID, UserID: "local", Name: "Transcript failure detail",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: projectID, AgentName: "OPERON", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: "local", ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "failure-task",
		FrameEventID: "failure-task-frame", MessageUUID: "failure-task-message", MessageOrigin: "task_intent",
		Text: "Review the scientific package.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "failure-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	detail := "independent scientific reviewer returned malformed tool arguments"
	reasonCode := "model_tool_arguments_invalid"
	if _, _, created, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "failure-finish", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"` + detail + `","reason_code":"` + reasonCode + `"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	failed, generic := "failed", "failed"
	if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateFrameRuntimePresentation(frameID, workspace.FrameRuntimePresentationInput{
		StatusDescription: &generic,
	}); err != nil {
		t.Fatal(err)
	}

	app := New(Options{Workspace: store, Transcript: repository, FileRoot: root}).Handler()
	projected := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	if projected["status"] != "failed" || projected["status_description"] != detail || projected["runtime_failure_reason"] != reasonCode {
		t.Fatalf("single-frame transcript failure projection=%#v", projected)
	}
	if projected["runtime_active"] != false || projected["runtime_attempt"] != float64(claim.Claim.Attempt) ||
		projected["runtime_finished_at"] == nil {
		t.Fatalf("terminal runtime projection=%#v", projected)
	}
}

func TestSingleFrameTerminalAfterCorrectionIsNotProjectedAsRuntimeStalled(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	projectID, frameID := "project-correction-terminal", "frame-correction-terminal"
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: projectID, UserID: "local", Name: "Correction terminal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: projectID, AgentName: "OPERON", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: "local", ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "correction-task",
		FrameEventID: "correction-task-frame", MessageUUID: "correction-task-message", MessageOrigin: "task_intent",
		Text: "Create and validate the requested deliverables.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "correction-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	interrupted, err := repository.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claim.Claim, ClientMessageID: "correction-interruption",
		ReasonCode:   "artifact_reference_correction_required",
		ResumeDetail: "missing required deliverables machine-readable validation record (.json)",
		AutoResume:   true, Destinations: []string{"ws"},
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interruption=%#v err=%v", interrupted, err)
	}
	resumed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "correction-resume-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed {
		t.Fatalf("resume claim=%#v err=%v", resumed, err)
	}
	if _, _, created, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: resumed.Claim, ClientMessageID: "correction-terminal", Status: "failed",
		PayloadJSON:  []byte(`{"status":"failed","detail":"The task failed under the authoritative Frame lifecycle."}`),
		Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	failed, reason := "failed", "artifact_reference_correction_required"
	if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateFrameRuntimePresentation(frameID, workspace.FrameRuntimePresentationInput{StatusDescription: &reason}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, Transcript: repository, FileRoot: root}).Handler()
	projected := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	if projected["status"] != "failed" || projected["runtime_paused"] == true ||
		projected["runtime_failure_reason"] != reason || projected["status_description"] != reason {
		t.Fatalf("terminal correction projection=%#v", projected)
	}
}
