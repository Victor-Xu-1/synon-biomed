package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestStopGenerationCancelsActiveModelRequestAndPersistsCancelledState(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCancelled := make(chan struct{})
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A real chat endpoint reads the request body before streaming. The
		// runner's replay payload can be hundreds of KB; without draining it,
		// the client transport cannot close the connection when the request
		// context is cancelled.
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(requestStarted)
		<-r.Context().Done()
		close(requestCancelled)
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "stop-project", UserID: "local", Name: "Stop Project", Path: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "stop-frame", ProjectID: "stop-project", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent", Name: "Long task",
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	}()
	if err := srv.sessionStore.Upsert(sessionstore.Session{
		ID: "stop-frame", Title: "Long task", WorkDir: root, LastRole: "user", MessageCount: 1,
		Project: &sessionstore.Project{ID: "stop-project", Name: "Stop Project", Path: root, BoundAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.eventJournal.Append("stop-frame", eventjournal.Message{
		"type": "message", "role": "user", "text": "Run until I stop you.",
	}, eventjournal.Metadata{ClientMessageID: "stop-user-1"}); err != nil {
		t.Fatal(err)
	}

	type runnerReturn struct {
		result SessionRunnerCycleResult
		err    error
	}
	runnerDone := make(chan runnerReturn, 1)
	go func() {
		result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
			SessionID: "stop-frame", RunnerID: "stop-runner",
			Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "blocking-model",
			RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 0,
			LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
			DisableMCPDiscovery: true, DisableSkillDiscovery: true,
		})
		runnerDone <- runnerReturn{result: result, err: err}
	}()
	select {
	case <-requestStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("model request did not start")
	}

	client := srv.sessionSockets.Register("stop-frame")
	defer srv.sessionSockets.Unregister("stop-frame", client)
	stopMessage := map[string]any{"type": "stop_generation", "reason": "user stopped the long task", "clientMessageId": "stop-1"}
	if !srv.handleSessionWebSocketMessage("stop-frame", client, stopMessage) {
		t.Fatal("stop_generation unexpectedly closed the websocket")
	}
	if stopMessage["accepted"] != true || stopMessage["runnerId"] != "stop-runner" {
		t.Fatalf("stop acknowledgement = %#v", stopMessage)
	}
	select {
	case <-requestCancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("stop_generation did not cancel the downstream model request")
	}
	var returned runnerReturn
	select {
	case returned = <-runnerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled runner did not settle")
	}
	if returned.err != nil || !returned.result.Claimed || returned.result.Status != "cancelled" {
		t.Fatalf("cancelled runner = %+v err=%v", returned.result, returned.err)
	}
	session, found, err := srv.sessionStore.Get("stop-frame")
	if err != nil || !found || session.Runner == nil || session.Runner.Status != "cancelled" {
		t.Fatalf("cancelled session = %#v found=%v err=%v", session, found, err)
	}
	frame, found, err := store.GetFrame("stop-frame")
	if err != nil || !found || frame.Status != "cancelled" {
		t.Fatalf("cancelled frame = %#v found=%v err=%v", frame, found, err)
	}
	entries, err := srv.eventJournal.ReadAll("stop-frame")
	if err != nil {
		t.Fatal(err)
	}
	var stopPersisted, finishPersisted bool
	for _, entry := range entries {
		if entry.Message["type"] == "stop_generation" && entry.Message["accepted"] == true {
			stopPersisted = true
		}
		if entry.Message["type"] == "runner_finished" && entry.Message["status"] == "cancelled" {
			finishPersisted = true
		}
		if entry.Message["role"] == "assistant" {
			t.Fatalf("cancelled partial response was persisted as an assistant answer: %#v", entry)
		}
	}
	if !stopPersisted || !finishPersisted {
		t.Fatalf("cancel journal evidence missing: %#v", entries)
	}
	if accepted, _ := srv.stopActiveSessionRun("stop-frame", "duplicate stop"); accepted {
		t.Fatal("completed run remained registered as active")
	}
}

func TestDrainLinearizesBeforeTerminalSettlement(t *testing.T) {
	server := New(Options{})
	runCtx, activeRun, finishRun, err := server.registerActiveSessionRun(
		context.Background(), "session-drain-settlement", "runner-drain-settlement",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer finishRun()

	// Holding settlement models a runner that has already entered its final
	// commit boundary. Drain must wait for that boundary instead of changing
	// its cause concurrently and turning infrastructure shutdown into a
	// terminal business result.
	activeRun.settlement.Lock()
	drainDone := make(chan error, 1)
	go func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		drainDone <- server.Drain(drainCtx)
	}()
	deadline := time.Now().Add(time.Second)
	for !server.isDraining() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !server.isDraining() {
		activeRun.settlement.Unlock()
		t.Fatal("server did not enter draining state")
	}
	select {
	case <-runCtx.Done():
		activeRun.settlement.Unlock()
		t.Fatalf("drain crossed settlement boundary: %v", context.Cause(runCtx))
	case <-time.After(25 * time.Millisecond):
	}

	activeRun.settled = true
	activeRun.settlement.Unlock()
	finishRun()
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if errors.Is(context.Cause(runCtx), ErrRuntimeDraining) {
		t.Fatalf("settled run was changed to runtime drain: %v", context.Cause(runCtx))
	}
}

func TestTryDrainIdleIsAtomicWithRunnerAdmission(t *testing.T) {
	idle := New(Options{})
	if !idle.TryDrainIdle() {
		t.Fatal("idle runtime did not accept verified reload drain")
	}
	if active, draining := idle.sessionRunActivity(); active != 0 || !draining {
		t.Fatalf("idle activity active=%d draining=%t", active, draining)
	}
	if _, _, _, err := idle.registerActiveSessionRun(context.Background(), "late-session", "late-runner"); !errors.Is(err, ErrRuntimeDraining) {
		t.Fatalf("late admission error=%v", err)
	}

	busy := New(Options{})
	_, _, finish, err := busy.registerActiveSessionRun(context.Background(), "active-session", "active-runner")
	if err != nil {
		t.Fatal(err)
	}
	if busy.TryDrainIdle() {
		t.Fatal("busy runtime accepted reload drain")
	}
	if active, draining := busy.sessionRunActivity(); active != 1 || draining {
		t.Fatalf("busy activity active=%d draining=%t", active, draining)
	}
	finish()
	if !busy.TryDrainIdle() {
		t.Fatal("runtime did not accept reload drain after task settlement")
	}
}

func TestTryDrainIdleRefusesDurableRunnerHandoffWithoutInMemoryOwner(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-drain-handoff", "project-drain-handoff", "frame-drain-handoff")
	if _, _, err := (&Server{workspaceStore: store, transcriptStore: repo}).submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-drain-handoff", MessageUUID: "message-drain-handoff",
		ClientMessageID: "client-drain-handoff", Text: "continue the long task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "owner-drain-handoff", "frame-drain-handoff")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-drain-handoff",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	server := New(Options{Workspace: store, Transcript: repo})
	if server.TryDrainIdle() {
		t.Fatal("deployment drain crossed a durable running attempt between execution segments")
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-drain-handoff", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if !server.TryDrainIdle() {
		t.Fatal("deployment drain stayed blocked after durable runner settlement")
	}
}

func TestTryDrainIdleRefusesKernelExecutionAfterRunnerCycleYields(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	assetRoot := filepath.Join(repositoryRoot, "assets", "optional")
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python:          python,
		AssetRoot:       assetRoot,
		ManifestPath:    filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:      filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		ShutdownTimeout: 2 * time.Second,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = manager.CloseAll(ctx)
	})
	workspaceDir := t.TempDir()
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "reload-gate-kernel", FrameID: "reload-gate-frame", RootFrameID: "reload-gate-frame",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Submit(kernelruntime.SubmitRequest{
		KernelID: "reload-gate-kernel", FrameID: "reload-gate-frame", Language: "python", Environment: "python",
		ExecID: "reload-gate-exec", ToolUseID: "reload-gate-tool", Origin: "agent",
		Code: "while True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-handle.Started():
	case <-time.After(3 * time.Second):
		t.Fatal("kernel execution did not start")
	}

	server := New(Options{KernelManager: manager})
	if server.TryDrainIdle() {
		t.Fatal("runtime accepted reload drain while a yielded kernel execution was active")
	}
	if active, draining := server.sessionRunActivity(); active != 0 || draining {
		t.Fatalf("runner activity active=%d draining=%t", active, draining)
	}
	if active := server.activeKernelExecutionCount(); active != 1 {
		t.Fatalf("active kernel executions=%d", active)
	}
	healthResponse := httptest.NewRecorder()
	server.handleHealth(healthResponse, httptest.NewRequest(http.MethodGet, "/health", nil))
	var health map[string]any
	if err := json.Unmarshal(healthResponse.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health["active_session_runs"] != float64(1) ||
		health["active_runner_sessions"] != float64(0) ||
		health["active_kernel_executions"] != float64(1) {
		t.Fatalf("reload health gate=%#v", health)
	}

	result := manager.Interrupt("reload-gate-frame", "reload-gate-exec")
	if !result.Interrupted {
		t.Fatalf("kernel interrupt result=%#v", result)
	}
	select {
	case <-handle.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("kernel execution did not settle")
	}
	if !server.TryDrainIdle() {
		t.Fatal("runtime did not accept reload drain after kernel settlement")
	}
}

func TestDrainDeadlineIsHonoredWhileSettlementIsBusy(t *testing.T) {
	server := New(Options{})
	_, activeRun, finishRun, err := server.registerActiveSessionRun(
		context.Background(), "session-drain-deadline", "runner-drain-deadline",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer finishRun()
	activeRun.settlement.Lock()
	drainCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	drainDone := make(chan error, 1)
	go func() { drainDone <- server.Drain(drainCtx) }()
	select {
	case err := <-drainDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			activeRun.settlement.Unlock()
			t.Fatalf("drain error=%v", err)
		}
	case <-time.After(time.Second):
		activeRun.settlement.Unlock()
		t.Fatal("drain ignored its deadline while settlement was busy")
	}
	activeRun.settlement.Unlock()
}

func TestDrainWinsLegacyTerminalBoundaryWithoutAssistantOrFinish(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseResponse
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"must not commit during drain"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	}()
	if err := srv.sessionStore.Upsert(sessionstore.Session{
		ID: "legacy-drain-boundary", Title: "Drain boundary", WorkDir: root,
		LastRole: "user", MessageCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.eventJournal.Append("legacy-drain-boundary", eventjournal.Message{
		"type": "message", "role": "user", "text": "complete only if drain loses",
	}, eventjournal.Metadata{ClientMessageID: "legacy-drain-user"}); err != nil {
		t.Fatal(err)
	}

	type runnerReturn struct {
		result SessionRunnerCycleResult
		err    error
	}
	runnerDone := make(chan runnerReturn, 1)
	go func() {
		result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
			SessionID: "legacy-drain-boundary", RunnerID: "legacy-drain-runner",
			Endpoint: modelAPI.URL, Model: "test-model", MaxAttempts: 1,
			DisableSkillDiscovery: true,
			LeaseTTL:              time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
		})
		runnerDone <- runnerReturn{result: result, err: err}
	}()
	select {
	case <-requestStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("model request did not start")
	}

	srv.sessionRunsMu.Lock()
	activeRun := srv.sessionRuns["legacy-drain-boundary"]
	srv.sessionRunsMu.Unlock()
	if activeRun == nil {
		t.Fatal("legacy runner was not registered")
	}
	activeRun.settlement.Lock()
	drainDone := make(chan error, 1)
	go func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		drainDone <- srv.Drain(drainCtx)
	}()
	deadline := time.Now().Add(time.Second)
	for !srv.isDraining() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !srv.isDraining() {
		activeRun.settlement.Unlock()
		t.Fatal("server did not enter draining state")
	}
	close(releaseResponse)
	activeRun.settlement.Unlock()

	var returned runnerReturn
	select {
	case returned = <-runnerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("legacy runner did not settle after drain")
	}
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if returned.err != nil || returned.result.Status != "interrupted" || returned.result.FinishEventID != 0 {
		t.Fatalf("legacy drain result=%#v err=%v", returned.result, returned.err)
	}
	entries, err := srv.eventJournal.ReadAll("legacy-drain-boundary")
	if err != nil {
		t.Fatal(err)
	}
	interruptions := 0
	for _, entry := range entries {
		if entry.Message["role"] == "assistant" || entry.Message["type"] == "runner_finished" {
			t.Fatalf("terminal content crossed drain boundary: %#v", entries)
		}
		if entry.Message["type"] == "runner_checkpoint" &&
			(entry.Message["reasonCode"] == "runtime_draining" || entry.Message["reason_code"] == "runtime_draining") {
			interruptions++
		}
	}
	if interruptions != 1 {
		t.Fatalf("interruptions=%d entries=%#v", interruptions, entries)
	}
}
