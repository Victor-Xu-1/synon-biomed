package workspace

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestExpireCompatibilityFrameResumeDispatchesClaimedBeforeRecoversImmediately(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-restart", UserID: "owner", Name: "Restart"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-restart", ProjectID: "project-restart", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	payload := `{"previousStatus":"processing","dispatch":{"status":"registered","attempt":0,"registeredAt":"` + now.Format(time.RFC3339Nano) + `"}}`
	if _, err := store.db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('resume-restart','frame-restart',1,'frame_resumed',?,?)`, payload, now); err != nil {
		t.Fatal(err)
	}
	first, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("old-runtime", 5*time.Minute)
	if err != nil || !claimed || first.Attempt != 1 {
		t.Fatalf("first=%#v claimed=%t err=%v", first, claimed, err)
	}
	startup := now.Add(time.Second)
	now = startup.Add(time.Second)
	fenced, err := store.ExpireCompatibilityFrameResumeDispatchesClaimedBefore(startup)
	if err != nil || fenced != 1 {
		t.Fatalf("fenced=%d err=%v", fenced, err)
	}
	second, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("new-runtime", 5*time.Minute)
	if err != nil || !claimed || !second.Recovered || second.Attempt != 2 || second.ClaimToken == first.ClaimToken {
		t.Fatalf("second=%#v claimed=%t err=%v", second, claimed, err)
	}
	if fenced, err := store.ExpireCompatibilityFrameResumeDispatchesClaimedBefore(startup); err != nil || fenced != 0 {
		t.Fatalf("idempotent fenced=%d err=%v", fenced, err)
	}
}

func TestWakeCompatibilityFrameResumeDispatchClearsNotBefore(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-wake", UserID: "owner-wake", Name: "Wake project",
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-wake", ProjectID: "project-wake", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatalf("create frame: %v", err)
	}
	now := time.Now().UTC()
	notBefore := now.Add(time.Hour)
	payload := map[string]any{
		"previousStatus": "processing", "rootFrameId": "frame-wake", "agentName": "OPERON",
		"reason": "approval_wake_test", "dispatch": map[string]any{
			"status": "registered", "attempt": 0,
			"registeredAt": now.Format(time.RFC3339Nano),
			"notBefore":    notBefore.Format(time.RFC3339Nano),
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('resume-wake','frame-wake',1,'frame_resumed',?,?)`, string(raw), now); err != nil {
		t.Fatalf("insert registered dispatch: %v", err)
	}

	event, changed, err := store.WakeCompatibilityFrameResumeDispatch("resume-wake")
	if err != nil || !changed || event.Type != "frame_resume_dispatch_woken" || event.FrameID != "frame-wake" {
		t.Fatalf("wake event=%#v changed=%t err=%v", event, changed, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatch("resume-wake")
	if err != nil || !found {
		t.Fatalf("reload dispatch found=%t err=%v", found, err)
	}
	if dispatch.Status != "registered" || !dispatch.NotBefore.IsZero() {
		t.Fatalf("woken dispatch status=%q notBefore=%v", dispatch.Status, dispatch.NotBefore)
	}

	if _, changed, err := store.WakeCompatibilityFrameResumeDispatch("resume-wake"); err != nil || changed {
		t.Fatalf("second wake changed=%t err=%v", changed, err)
	}

	if _, err := store.db.Exec(`UPDATE frame_events SET payload=? WHERE id='resume-wake'`,
		`{"dispatch":{"status":"claimed","attempt":1,"claimToken":"token","leaseExpiresAt":"2026-01-01T00:00:00Z"}}`); err != nil {
		t.Fatalf("rewrite claimed dispatch: %v", err)
	}
	if _, changed, err := store.WakeCompatibilityFrameResumeDispatch("resume-wake"); err != nil || changed {
		t.Fatalf("claimed dispatch wake changed=%t err=%v", changed, err)
	}
}

func TestModelSelectionWaitIsNotClaimedUntilModelSwitch(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-model-wait", UserID: "owner-model-wait", Name: "Model wait",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-model-wait", ProjectID: "project-model-wait", AgentName: "OPERON",
		Status: FrameStatusProcessing, ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateAutoResumeDispatch(
		"frame-model-wait", "frame-model-wait", "project-model-wait", "OPERON", "initial_task",
	)
	if err != nil || created.Event == nil {
		t.Fatalf("create dispatch=%#v err=%v", created, err)
	}
	claimed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("model-wait-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim dispatch=%#v ok=%t err=%v", claimed, ok, err)
	}
	if _, _, err := store.RequeueCompatibilityFrameResumeDispatch(RequeueCompatibilityFrameResumeDispatchInput{
		ResumeEventID: claimed.ResumeEvent.ID, ExpectedAttempt: claimed.Attempt,
		ClaimToken: claimed.ClaimToken, ReasonCode: "model_provider_unavailable",
		RunnerAttempt: 1, CheckpointEventID: 7,
		WaitingFor: CompatibilityFrameResumeDispatchWaitModelSelection,
	}); err != nil {
		t.Fatal(err)
	}
	parked, found, err := store.GetCompatibilityFrameResumeDispatch(claimed.ResumeEvent.ID)
	if err != nil || !found || parked.Status != frameResumeDispatchRegistered ||
		parked.WaitingFor != CompatibilityFrameResumeDispatchWaitModelSelection {
		t.Fatalf("parked dispatch=%#v found=%t err=%v", parked, found, err)
	}
	if next, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("model-wait-worker", time.Minute); err != nil || ok {
		t.Fatalf("waiting dispatch claimed=%#v ok=%t err=%v", next, ok, err)
	}

	wake, state, err := store.SignalCompatibilityFrameResumeDispatchModelSwitch(
		"frame-model-wait", 1, "model_provider_unavailable",
	)
	if err != nil || state != CompatibilityFrameResumeDispatchWakeWoken || wake.Type != "frame_resume_dispatch_woken" {
		t.Fatalf("model switch wake=%#v state=%q err=%v", wake, state, err)
	}
	resumed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("model-wait-worker", time.Minute)
	if err != nil || !ok || resumed.Attempt != claimed.Attempt+1 || resumed.WaitingFor != "" {
		t.Fatalf("resumed dispatch=%#v ok=%t err=%v", resumed, ok, err)
	}
}

func TestLegacyBlockedCompatibilityFrameResumeDispatchCannotBeRevived(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-unblock", UserID: "owner-unblock", Name: "Unblock project",
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-unblock", ProjectID: "project-unblock", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatalf("create frame: %v", err)
	}
	now := time.Now().UTC()
	payload := map[string]any{
		"previousStatus": "processing", "rootFrameId": "frame-unblock", "agentName": "OPERON",
		"dispatch": map[string]any{
			"status": "blocked", "attempt": 3, "claimToken": "stale-token",
			"registeredAt": now.Format(time.RFC3339Nano),
			"blockedAt":    now.Format(time.RFC3339Nano),
			"errorCode":    "runner_tool_round_no_progress_exhausted",
			"message":      "manual resolution is required before resume can continue",
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('resume-unblock','frame-unblock',1,'frame_resumed',?,?)`, string(raw), now); err != nil {
		t.Fatalf("insert blocked dispatch: %v", err)
	}

	event, changed, err := store.WakeCompatibilityFrameResumeDispatch("resume-unblock")
	if err != nil || changed || event.Type != "" {
		t.Fatalf("wake event=%#v changed=%t err=%v", event, changed, err)
	}
	failed, resumed, err := store.FailCompatibilityFrameResumeDispatch("resume-unblock", 3, "stale-token", "legacy_blocked_runtime")
	if err != nil || resumed || failed.Type != "frame_resume_dispatch_failed" {
		t.Fatalf("failed event=%#v resumed=%t err=%v", failed, resumed, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatch("resume-unblock")
	if err != nil || !found {
		t.Fatalf("reload dispatch found=%t err=%v", found, err)
	}
	if dispatch.Status != "failed" || dispatch.Error != "legacy_blocked_runtime" {
		t.Fatalf("failed dispatch=%#v", dispatch)
	}
	frame, found, err := store.GetFrame("frame-unblock")
	if err != nil || !found || frame.Status != FrameStatusFailed {
		t.Fatalf("failed frame=%#v found=%t err=%v", frame, found, err)
	}
	if _, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-unblock", time.Second); err != nil || claimed {
		t.Fatalf("terminal dispatch claimed=%t err=%v", claimed, err)
	}
}

func TestResumeDispatchLookupPrefersOlderActiveStateOverNewerTerminalHistory(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-active-history", UserID: "owner-active-history", Name: "Active history",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-active-history", ProjectID: "project-active-history", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	blocked := `{"previousStatus":"processing","dispatch":{"status":"blocked","attempt":3,"errorCode":"artifact_reference_correction_required"}}`
	completed := `{"previousStatus":"processing","dispatch":{"status":"completed","attempt":1}}`
	if _, err := store.db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES
		('resume-older-blocked','frame-active-history',1,'frame_resumed',?,?),
		('resume-newer-completed','frame-active-history',2,'frame_resumed',?,?)`, blocked, now, completed, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("frame-active-history")
	if err != nil || !found || dispatch.ResumeEvent.ID != "resume-older-blocked" || dispatch.Status != "blocked" {
		t.Fatalf("active dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	blockedDispatches, err := store.ListLegacyBlockedCompatibilityFrameResumeDispatches(10)
	if err != nil || len(blockedDispatches) != 1 || blockedDispatches[0].ResumeEvent.ID != "resume-older-blocked" {
		t.Fatalf("blocked dispatches=%#v err=%v", blockedDispatches, err)
	}
}

func TestCreateAutoResumeDispatchDoesNotReuseTerminalHistory(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-terminal-history", UserID: "owner-terminal-history", Name: "Terminal history project",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-terminal-history", ProjectID: "project-terminal-history", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	terminal := `{"previousStatus":"interrupted","rootFrameId":"frame-terminal-history","leafFramesReady":1,"agentName":"OPERON","dispatch":{"status":"completed","attempt":1,"registeredAt":"` + now.Add(-time.Minute).Format(time.RFC3339Nano) + `","terminalAt":"` + now.Format(time.RFC3339Nano) + `"}}`
	if _, err := store.db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('resume-terminal-history','frame-terminal-history',1,'frame_resumed',?,?)`, terminal, now); err != nil {
		t.Fatal(err)
	}

	resumed, err := store.CreateAutoResumeDispatch(
		"frame-terminal-history", "frame-terminal-history", "project-terminal-history", "OPERON", "explicit_user_continue",
	)
	if err != nil || resumed.Event == nil || resumed.Event.ID == "resume-terminal-history" {
		t.Fatalf("new resume=%#v err=%v", resumed, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatch(resumed.Event.ID)
	if err != nil || !found || dispatch.Status != "registered" {
		t.Fatalf("new dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
}

func TestWakeCompatibilityFrameResumeDispatchMissingEventFails(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.WakeCompatibilityFrameResumeDispatch("missing-resume"); err == nil {
		t.Fatal("missing resume dispatch wake succeeded")
	}
}

func TestClaimNextCompatibilityFrameResumeDispatchSkipsSupersededActiveDispatch(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-resume-supersession", UserID: "owner-resume-supersession", Name: "Resume supersession",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-resume-supersession", ProjectID: "project-resume-supersession", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	older := `{"previousStatus":"awaiting_user_response","dispatch":{"status":"registered","attempt":3,"registeredAt":"` + now.Format(time.RFC3339Nano) + `"}}`
	newer := `{"previousStatus":"awaiting_plan_approval","dispatch":{"status":"registered","attempt":0,"registeredAt":"` + now.Add(time.Second).Format(time.RFC3339Nano) + `"}}`
	if _, err := store.db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES
		('resume-superseded','frame-resume-supersession',1,'frame_resumed',?,?),
		('resume-current','frame-resume-supersession',2,'frame_resumed',?,?)`,
		older, now, newer, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	claimed, found, err := store.ClaimNextCompatibilityFrameResumeDispatch("supersession-worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim current dispatch=%#v found=%t err=%v", claimed, found, err)
	}
	if claimed.ResumeEvent.ID != "resume-current" {
		t.Fatalf("claimed superseded dispatch %q", claimed.ResumeEvent.ID)
	}
	olderDispatch, found, err := store.GetCompatibilityFrameResumeDispatch("resume-superseded")
	if err != nil || !found || olderDispatch.Status != frameResumeDispatchRegistered || olderDispatch.Attempt != 3 {
		t.Fatalf("older dispatch mutated=%#v found=%t err=%v", olderDispatch, found, err)
	}
}

func TestModelSwitchSignalSurvivesClaimedToTerminalRace(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-model-switch-race", UserID: "owner-model-switch-race", Name: "Model switch race",
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-model-switch-race", ProjectID: "project-model-switch-race", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatalf("create frame: %v", err)
	}
	resume, err := store.CreateAutoResumeDispatch(
		"frame-model-switch-race", "frame-model-switch-race", "project-model-switch-race", "OPERON", "manual_resume",
	)
	if err != nil || resume.Event == nil {
		t.Fatalf("create dispatch event=%#v err=%v", resume.Event, err)
	}
	claimed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("model-switch-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim dispatch=%#v ok=%t err=%v", claimed, ok, err)
	}

	signalEvent, state, err := store.SignalCompatibilityFrameResumeDispatchModelSwitch(
		claimed.FrameID, 7, "model_provider_unavailable",
	)
	if err != nil || state != CompatibilityFrameResumeDispatchWakeArmed || signalEvent.Type != "frame_resume_dispatch_model_switch_armed" {
		t.Fatalf("signal event=%#v state=%q err=%v", signalEvent, state, err)
	}
	interrupted, resumed, err := store.FailCompatibilityFrameResumeDispatch(
		claimed.ResumeEvent.ID, claimed.Attempt, claimed.ClaimToken, "model_provider_unavailable",
	)
	if err != nil || !resumed || interrupted.Type != "frame_resume_dispatch_interrupted" {
		t.Fatalf("settle event=%#v resumed=%t err=%v", interrupted, resumed, err)
	}
	persisted, found, err := store.GetCompatibilityFrameResumeDispatch(claimed.ResumeEvent.ID)
	if err != nil || !found || persisted.Status != "registered" || persisted.Error != "" || !persisted.NotBefore.IsZero() {
		t.Fatalf("persisted dispatch=%#v found=%t err=%v", persisted, found, err)
	}

	second, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("model-switch-worker", time.Minute)
	if err != nil || !ok || second.Attempt != claimed.Attempt+1 {
		t.Fatalf("second claim=%#v ok=%t err=%v", second, ok, err)
	}
	_, resumed, err = store.FailCompatibilityFrameResumeDispatch(
		second.ResumeEvent.ID, second.Attempt, second.ClaimToken, "model_provider_unavailable",
	)
	if err != nil || resumed {
		t.Fatalf("unsignaled provider failure resumed=%t err=%v", resumed, err)
	}
	persisted, found, err = store.GetCompatibilityFrameResumeDispatch(second.ResumeEvent.ID)
	if err != nil || !found || persisted.Status != "failed" || persisted.Error != "model_provider_unavailable" {
		t.Fatalf("unsignaled dispatch=%#v found=%t err=%v", persisted, found, err)
	}
}
