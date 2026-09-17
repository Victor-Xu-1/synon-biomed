package workspace

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestCompleteCompatibilityFrameResumeDispatchPersistsFailureDescription(t *testing.T) {
	store := newResumeDescriptionStore(t)
	frameID := "frame-resume-description-failed"
	seedClaimableResumeDescriptionFrame(t, store, frameID)
	claimed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-failure", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim resume dispatch=%#v ok=%t err=%v", claimed, ok, err)
	}
	detail := "read completion recovery candidate: transcript event conflicts with durable state"
	input := CompleteCompatibilityFrameResumeDispatchInput{
		ResumeEventID: claimed.ResumeEvent.ID, ExpectedAttempt: claimed.Attempt,
		ClaimToken: claimed.ClaimToken, Status: "failed", Message: detail,
	}
	if _, repeated, err := store.CompleteCompatibilityFrameResumeDispatch(input); err != nil || repeated {
		t.Fatalf("complete failed dispatch repeated=%t err=%v", repeated, err)
	}
	assertResumeDescription(t, store, frameID, "failed", detail)

	if _, err := store.db.Exec(`UPDATE frame_runtime_metadata SET status_description=NULL WHERE frame_id=?`, frameID); err != nil {
		t.Fatal(err)
	}
	if _, repeated, err := store.CompleteCompatibilityFrameResumeDispatch(input); err != nil || !repeated {
		t.Fatalf("replay failed dispatch repeated=%t err=%v", repeated, err)
	}
	assertResumeDescription(t, store, frameID, "failed", detail)
}

func TestCompleteCompatibilityFrameResumeDispatchClearsStaleFailureDescription(t *testing.T) {
	store := newResumeDescriptionStore(t)
	frameID := "frame-resume-description-completed"
	seedClaimableResumeDescriptionFrame(t, store, frameID)
	if _, err := store.db.Exec(`INSERT INTO frame_runtime_metadata(frame_id,context_data,status_description)
		VALUES(?, '{}', 'stale failure')`, frameID); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-completion", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim resume dispatch=%#v ok=%t err=%v", claimed, ok, err)
	}
	if _, repeated, err := store.CompleteCompatibilityFrameResumeDispatch(CompleteCompatibilityFrameResumeDispatchInput{
		ResumeEventID: claimed.ResumeEvent.ID, ExpectedAttempt: claimed.Attempt,
		ClaimToken: claimed.ClaimToken, Status: "completed", Message: "completed",
	}); err != nil || repeated {
		t.Fatalf("complete dispatch repeated=%t err=%v", repeated, err)
	}
	assertResumeDescription(t, store, frameID, "completed", "")
}

func TestResumeCompatibilityFrameConversationClearsStaleFailureDescriptionOnActivation(t *testing.T) {
	store := newResumeDescriptionStore(t)
	frameID := "frame-resume-description-activation"
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: frameID, ProjectID: "project-resume-description", AgentName: "OPERON",
		Status: "failed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO frame_runtime_metadata(frame_id,context_data,status_description,completed_at)
		VALUES(?, '{}', 'previous attempt failed', ?)`, frameID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	result, err := store.ResumeCompatibilityFrameConversation(frameID, ResumeCompatibilityFrameInput{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResumedFrameID != frameID {
		t.Fatalf("resumed frame=%q want %q", result.ResumedFrameID, frameID)
	}
	assertResumeDescription(t, store, frameID, "processing", "")
	assertCompatibilityFrameCompletionCleared(t, store, frameID, true)
}

func newResumeDescriptionStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-resume-description", UserID: "owner-resume-description", Name: "Resume description",
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func seedClaimableResumeDescriptionFrame(t *testing.T, store *Store, frameID string) {
	t.Helper()
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: frameID, ProjectID: "project-resume-description", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	payload, err := json.Marshal(map[string]any{
		"previousStatus": "failed", "rootFrameId": frameID, "agentName": "OPERON",
		"dispatch": map[string]any{
			"status": "registered", "attempt": 0,
			"registeredAt": now.Format(time.RFC3339Nano),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?,?,?,?,?,?)`, "resume-"+frameID, frameID, 1, "frame_resumed", string(payload), now); err != nil {
		t.Fatal(err)
	}
}

func assertResumeDescription(t *testing.T, store *Store, frameID, wantStatus, wantDescription string) {
	t.Helper()
	frame, found, err := store.GetCompatibilityFrame(frameID)
	if err != nil || !found {
		t.Fatalf("load frame found=%t err=%v", found, err)
	}
	if frame.Status != wantStatus || frame.StatusDescription != wantDescription {
		t.Fatalf("frame status=%q description=%v want status=%q description=%q",
			frame.Status, frame.StatusDescription, wantStatus, wantDescription)
	}
}
