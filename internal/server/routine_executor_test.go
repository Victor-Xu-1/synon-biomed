package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/routinescheduler"
)

func TestDueRoutineUsesSavedProviderAndPersistsRealAgentRun(t *testing.T) {
	staticRequests := atomic.Int32{}
	staticAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staticRequests.Add(1)
		http.Error(w, "static provider must not be used", http.StatusTeapot)
	}))
	defer staticAPI.Close()

	savedRequests := atomic.Int32{}
	requestSeen := make(chan struct{}, 1)
	savedAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		savedRequests.Add(1)
		select {
		case requestSeen <- struct{}{}:
		default:
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("saved request = %s %s", r.Method, r.URL.Path)
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer routine-secret" {
			t.Errorf("saved authorization = %q", authorization)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Model != "routine-model" || len(body.Messages) == 0 {
			t.Errorf("saved request body = %#v", body)
		} else if latest := body.Messages[len(body.Messages)-1]; latest.Role != "user" || latest.Content != "inspect the latest experiment" {
			t.Errorf("latest routine instruction = %#v", latest)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "routine-request-1")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"routine model response"}}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`))
	}))
	defer savedAPI.Close()

	srv, store, root := newRoutineIntegrationServer(t)
	configureRoutineProvider(t, srv, store, savedAPI.URL)
	createRoutineFixture(t, store, root, "routine-success", "inspect the latest experiment")
	executor, err := srv.NewRoutineExecutor(RoutineExecutorOptions{Chat: SessionRunnerChatOptions{
		Endpoint: staticAPI.URL + "/v1/chat/completions", APIKey: "static-secret", Model: "static-model",
		RequestTimeout: time.Second, MaxAttempts: 1, MaxToolRounds: 1,
		LeaseTTL: 3 * time.Second, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
	}})
	if err != nil {
		t.Fatalf("new routine executor: %v", err)
	}
	scheduler, err := routinescheduler.New(routinescheduler.Options{
		Repository: store, Executor: executor, LockTTL: 4 * time.Second,
		TickTimeout:  2 * time.Second,
		ErrorBackoff: 10 * time.Millisecond, MinimumDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	defer stopRoutineIntegrationScheduler(t, scheduler)
	receiveRoutineSignal(t, requestSeen)
	routineEventually(t, 3*time.Second, func() bool {
		routine, err := store.GetRoutine("routine-success")
		return err == nil && routine.TickCount == 1 && routine.LastOKAt != nil && routine.LastResults == "routine model response"
	})
	if staticRequests.Load() != 0 || savedRequests.Load() != 1 {
		t.Fatalf("provider requests: static=%d saved=%d", staticRequests.Load(), savedRequests.Load())
	}

	events, err := store.ListFrameEvents("frame-routine", 0, 100)
	if err != nil {
		t.Fatalf("list frame events: %v", err)
	}
	for _, expected := range []string{"user_message", "assistant_message", "routine_tick_completed"} {
		if !routineFrameEventExists(events, expected) {
			t.Fatalf("missing %s frame event: %#v", expected, events)
		}
	}
	journal, err := srv.eventJournal.ReadAfter("frame-routine", 0, 100)
	if err != nil {
		t.Fatalf("read session journal: %v", err)
	}
	if !hasJournalEntry(providerAuthorityEntriesToAny(journal), "message", "", "routine model response") || !hasJournalEntry(providerAuthorityEntriesToAny(journal), "runner_finished", "completed", "") {
		t.Fatalf("session journal = %#v", journal)
	}
	audits, err := srv.runtimeStore.List(sessionRunnerModelAuditRuntimeNamespace)
	if err != nil {
		t.Fatalf("list model audits: %v", err)
	}
	audit := findSessionRunnerModelAuditValue(audits, "frame-routine")
	if audit == nil || audit["providerId"] != "routine-provider" || audit["requestId"] != "routine-request-1" || audit["providerAuthority"] != true {
		t.Fatalf("provider audit = %#v", audit)
	}

	// Replaying the same durable tick after a process crash uses the existing
	// runner_finished evidence and must not issue a second model request.
	replayed, err := executor.ExecuteRoutineTick(context.Background(), routinescheduler.Tick{
		RoutineID: "routine-success", RootFrameID: "frame-routine", OwnerUserID: "user-routine",
		Instruction: "inspect the latest experiment", Attempt: 1,
	})
	if err != nil || replayed.Summary != "routine model response" || savedRequests.Load() != 1 {
		t.Fatalf("replayed tick = %#v err=%v savedRequests=%d", replayed, err, savedRequests.Load())
	}
}

func TestRoutineAskUserDefersAndReconcilesTheSameTickAfterResume(t *testing.T) {
	var requests atomic.Int32
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch sequence {
		case 1:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"routine-ask-structure","type":"function","function":{"name":"AskUserQuestion","arguments":"{\"questions\":[{\"question\":\"Which structure should the routine use?\",\"header\":\"Structure\",\"options\":[{\"label\":\"5FQD\",\"description\":\"Use the human CRBN complex.\",\"pros\":\"Matches the requested human-complex objective.\",\"cons\":\"Represents one conformation.\",\"readiness\":\"Structure availability has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Routine analysis using 5FQD if verified.\",\"selection_rationale\":\"Recommended for alignment with the requested species.\",\"recommended\":true},{\"label\":\"4TZ4\",\"description\":\"Use the alternate complex.\",\"pros\":\"Tests an alternate conformation.\",\"cons\":\"Does not match the primary species criterion as closely.\",\"readiness\":\"Structure availability has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Routine analysis using 4TZ4 if verified.\",\"selection_rationale\":\"Choose when conformational comparison is more important.\",\"recommended\":false}]}]}"}}]}}]}`))
		case 2:
			rawMessages, _ := json.Marshal(payload["messages"])
			if !strings.Contains(string(rawMessages), "5FQD") || !strings.Contains(string(rawMessages), "answered") {
				t.Fatalf("routine resume did not receive durable user input: %s", rawMessages)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Routine resumed with 5FQD and completed."}}]}`))
		default:
			t.Fatalf("routine issued duplicate model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	srv, store, root := newRoutineTranscriptIntegrationServer(t)
	configureRoutineProvider(t, srv, store, modelAPI.URL)
	createRoutineFixture(t, store, root, "routine-ask-user", "inspect CRBN and clarify the structure when needed")
	chat := SessionRunnerChatOptions{
		AllowedTools: []string{"AskUserQuestion"}, RequestTimeout: time.Second,
		MaxAttempts: 1, MaxToolRounds: 0, LeaseTTL: 2 * time.Second,
		ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
	}
	executor, err := srv.NewRoutineExecutor(RoutineExecutorOptions{Chat: chat})
	if err != nil {
		t.Fatal(err)
	}
	tick := routinescheduler.Tick{
		RoutineID: "routine-ask-user", RootFrameID: "frame-routine", OwnerUserID: "user-routine",
		Instruction: "inspect CRBN and clarify the structure when needed", Attempt: 1,
	}
	deferred, err := executor.ExecuteRoutineTick(context.Background(), tick)
	if err != nil || !deferred.Deferred || deferred.Summary != "awaiting user response" {
		t.Fatalf("deferred tick = %#v err=%v", deferred, err)
	}
	frame, found, err := store.GetCompatibilityFrame("frame-routine")
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("deferred frame = %#v found=%v err=%v", frame, found, err)
	}
	events, err := store.ListFrameEvents("frame-routine", 0, 100)
	if err != nil || !routineFrameEventExists(events, "routine_tick_waiting") || routineFrameEventExists(events, "routine_tick_failed") {
		t.Fatalf("deferred events = %#v err=%v", events, err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/frames/frame-routine/resolve-input", nil)
	request.Header.Set("X-Synon-User-Id", "user-routine")
	resolved, err := srv.resolveCompatibilityInput(request, frame, compatibilityResolveInputRequest{Responses: []compatibilityInputResponse{{
		ToolID: "routine-ask-structure", Action: "answer",
		Answers: map[string]string{"Which structure should the routine use?": "5FQD"},
	}}})
	if err != nil || resolved.Status != "accepted" {
		t.Fatalf("resolve routine input = %#v err=%v", resolved, err)
	}
	resumeStartedAt := time.Now()
	resumed, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "routine-ask-resume", Chat: chat,
	})
	if err != nil || !resumed.Claimed || resumed.Status != "completed" {
		t.Logf("resume dispatch monotonic elapsed=%s", time.Since(resumeStartedAt))
		terminalJSON, terminalErr := json.Marshal(resumed.TerminalEvent)
		t.Logf("resume terminal=%s marshalErr=%v modelRequests=%d now=%s", terminalJSON, terminalErr, requests.Load(), time.Now().UTC())
		stream, found, streamErr := srv.transcriptStore.GetFrameStreamBySession(context.Background(), "user-routine", "frame-routine")
		if streamErr == nil && found {
			logTranscriptRunnerClaimState(t, srv.transcriptStore, stream.UID, stream.OwnerID, nil)
		} else {
			t.Logf("resume stream found=%t err=%v", found, streamErr)
		}
		t.Fatalf("resume routine frame = %#v err=%v", resumed, err)
	}
	legacySession, found, err := srv.sessionStore.Get("frame-routine")
	if err != nil || !found || legacySession.Runner == nil || legacySession.Runner.Status != "running" {
		t.Fatalf("legacy projection unexpectedly became authority: session=%#v found=%t err=%v", legacySession, found, err)
	}

	reconciled, err := executor.ExecuteRoutineTick(context.Background(), tick)
	debugHistory, _, debugHistoryErr := srv.loadTranscriptWebHistory(context.Background(), "user-routine", "frame-routine")
	if err != nil || reconciled.Deferred || reconciled.Summary != "Routine resumed with 5FQD and completed." {
		t.Fatalf("reconciled tick = %#v err=%v history=%#v historyErr=%v", reconciled, err, debugHistory, debugHistoryErr)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d, want exactly ask + resume", requests.Load())
	}
	events, err = store.ListFrameEvents("frame-routine", 0, 100)
	if err != nil || !routineFrameEventExists(events, "routine_tick_completed") || routineFrameEventExists(events, "routine_tick_failed") {
		t.Fatalf("reconciled events = %#v err=%v", events, err)
	}
	stream, found, err := srv.transcriptStore.GetFrameStreamBySession(context.Background(), "user-routine", "frame-routine")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	runtimeState, found, err := srv.transcriptStore.GetLatestRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || runtimeState.FinishedEventID <= 0 {
		t.Fatalf("runtime=%#v found=%t err=%v", runtimeState, found, err)
	}
	outcome, found, err := store.GetFrameEventByID(routineTickStableID("outcome", tick))
	if err != nil || !found || numberValue(outcome.Payload["assistantEventId"]) <= 0 ||
		numberValue(outcome.Payload["finishEventId"]) != runtimeState.FinishedEventID {
		t.Fatalf("outcome=%#v runtime=%#v found=%t err=%v", outcome, runtimeState, found, err)
	}
	session, found, err := srv.sessionStore.Get("frame-routine")
	if err != nil || !found {
		t.Fatalf("reconciled session found=%v err=%v", found, err)
	}
	if _, exists := session.Orchestration[routineDeferredOrchestrationKey]; exists {
		t.Fatalf("routine deferred marker was not cleared: %#v", session.Orchestration)
	}
}

func TestTranscriptRoutineTerminalSummaryRequiresOneExactAttempt(t *testing.T) {
	base := map[string]any{
		"id": "assistant-frame-2", "type": "text", "position": "left", "terminal_status": "completed",
		"content": map[string]any{"content": "done"},
	}
	if summary, err := transcriptRoutineTerminalSummary([]map[string]any{base}, "frame", 2, "completed"); err != nil || summary != "done" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	eventBoundary := copyStringAnyMap(base)
	eventBoundary["id"] = "assistant-frame-2-event-17"
	if summary, err := transcriptRoutineTerminalSummary([]map[string]any{eventBoundary}, "frame", 2, "completed"); err != nil || summary != "done" {
		t.Fatalf("event summary=%q err=%v", summary, err)
	}
	segmentBoundary := copyStringAnyMap(base)
	segmentBoundary["id"] = "assistant-frame-2-segment-2"
	if summary, err := transcriptRoutineTerminalSummary([]map[string]any{segmentBoundary}, "frame", 2, "completed"); err != nil || summary != "done" {
		t.Fatalf("segment summary=%q err=%v", summary, err)
	}
	if _, err := transcriptRoutineTerminalSummary([]map[string]any{base, eventBoundary}, "frame", 2, "completed"); err == nil {
		t.Fatal("duplicate terminal messages were accepted")
	}
	invalidSegment := copyStringAnyMap(base)
	invalidSegment["id"] = "assistant-frame-2-segment-invalid"
	if _, err := transcriptRoutineTerminalSummary([]map[string]any{invalidSegment}, "frame", 2, "completed"); err == nil {
		t.Fatal("invalid segment identity was accepted")
	}
	wrongStatus := copyStringAnyMap(base)
	wrongStatus["terminal_status"] = "failed"
	if _, err := transcriptRoutineTerminalSummary([]map[string]any{wrongStatus}, "frame", 2, "completed"); err == nil {
		t.Fatal("wrong terminal status was accepted")
	}
}

func TestRoutineSchedulerShutdownCancelsRealModelRequest(t *testing.T) {
	requestSeen := make(chan struct{})
	requestCancelled := make(chan struct{})
	releaseHandler := make(chan struct{})
	var requests atomic.Int32
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(requestSeen)
		select {
		case <-r.Context().Done():
			close(requestCancelled)
		case <-releaseHandler:
		}
	}))
	defer func() {
		close(releaseHandler)
		modelAPI.CloseClientConnections()
		modelAPI.Close()
	}()

	srv, store, root := newRoutineIntegrationServer(t)
	configureRoutineProvider(t, srv, store, modelAPI.URL)
	createRoutineFixture(t, store, root, "routine-cancel", "wait for shutdown")
	executor, err := srv.NewRoutineExecutor(RoutineExecutorOptions{Chat: SessionRunnerChatOptions{
		RequestTimeout: 30 * time.Second, MaxAttempts: 1, MaxToolRounds: 1,
		LeaseTTL: 35 * time.Second, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
	}})
	if err != nil {
		t.Fatalf("new routine executor: %v", err)
	}
	scheduler, err := routinescheduler.New(routinescheduler.Options{
		Repository: store, Executor: executor, LockTTL: 40 * time.Second,
		TickTimeout:  35 * time.Second,
		ErrorBackoff: 10 * time.Millisecond, MinimumDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	serviceContext, cancelService := context.WithCancel(context.Background())
	if err := scheduler.Start(serviceContext); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	receiveRoutineSignal(t, requestSeen)
	cancelService()
	stopContext, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := scheduler.Stop(stopContext); err != nil {
		t.Fatalf("stop scheduler: %v", err)
	}
	receiveRoutineSignal(t, requestCancelled)
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}
	routine, err := store.GetRoutine("routine-cancel")
	if err != nil {
		t.Fatalf("get cancelled routine: %v", err)
	}
	if routine.TickCount != 1 || routine.LockedAt != nil || routine.IdleStreak != 1 || !strings.Contains(routine.LastResults, "context canceled") {
		t.Fatalf("cancelled routine = %#v", routine)
	}
}

func TestPersistedRoutineOutcomeSummaryHasUTF8ByteLimit(t *testing.T) {
	srv, store, root := newRoutineIntegrationServer(t)
	createRoutineFixture(t, store, root, "routine-bounded", "bounded")
	executor := &routineExecutor{server: srv}
	tick := routinescheduler.Tick{RoutineID: "routine-bounded", RootFrameID: "frame-routine", OwnerUserID: "user-routine", Instruction: "bounded", Attempt: 1}
	large := strings.Repeat("界", 30000)
	if err := executor.persistRoutineOutcome(tick, "runner-bounded", SessionRunnerCycleResult{SessionID: "frame-routine", AssistantEventID: 1, FinishEventID: 2}, large, true); err != nil {
		t.Fatalf("persist bounded outcome: %v", err)
	}
	event, found, err := store.GetFrameEventByID(routineTickStableID("outcome", tick))
	if err != nil || !found {
		t.Fatalf("get bounded outcome: found=%v err=%v", found, err)
	}
	summary, ok := event.Payload["summary"].(string)
	if !ok || len(summary) > maxRoutineOutcomeSummary || !utf8.ValidString(summary) || len(summary) < maxRoutineOutcomeSummary-4 {
		t.Fatalf("bounded summary bytes=%d valid=%v typeOK=%v", len(summary), utf8.ValidString(summary), ok)
	}
}

func TestRoutineExecutionBudgetCoversAllRoundsRetriesAndStrictLease(t *testing.T) {
	unbounded, err := RoutineExecutionBudget(SessionRunnerChatOptions{RequestTimeout: 100 * time.Millisecond, MaxToolRounds: 0, MaxAttempts: 2})
	if err != nil || unbounded != 0 {
		t.Fatalf("unbounded execution budget=%v error=%v", unbounded, err)
	}
	if lease, err := RoutineLockTTL(time.Second, unbounded); err != nil || lease != time.Second {
		t.Fatalf("unbounded renewable lease=%v error=%v", lease, err)
	}
	if unboundedBatch, err := RoutineExecutionBudget(SessionRunnerChatOptions{RequestTimeout: 100 * time.Millisecond, MaxToolRounds: 2, MaxToolCallsPerRound: 0, MaxAttempts: 2}); err != nil || unboundedBatch != 0 {
		t.Fatalf("unbounded batch execution budget=%v error=%v", unboundedBatch, err)
	}
	options := SessionRunnerChatOptions{RequestTimeout: 100 * time.Millisecond, MaxToolRounds: 2, MaxToolCallsPerRound: 3, MaxAttempts: 3}
	budget, err := RoutineExecutionBudget(options)
	if err != nil {
		t.Fatalf("execution budget: %v", err)
	}
	reviewerRuns := options.MaxToolRounds + 1
	wantModelCalls := (options.MaxToolRounds +
		maxSessionRunnerArtifactReferenceRepairs + 1 +
		reviewerRuns*(sessionReviewerMaxToolRounds+1)) * options.MaxAttempts
	want := time.Duration(wantModelCalls)*100*time.Millisecond +
		time.Duration((options.MaxToolRounds+reviewerRuns*sessionReviewerMaxToolRounds)*options.MaxToolCallsPerRound)*routineToolRoundAllowance + routineFinalizeAllowance
	if budget != want {
		t.Fatalf("execution budget = %v, want %v", budget, want)
	}
	lockTTL, err := RoutineLockTTL(time.Second, budget)
	if err != nil {
		t.Fatalf("lock ttl: %v", err)
	}
	if lockTTL <= budget || lockTTL != budget+routineLeaseSafetyMargin {
		t.Fatalf("lock ttl = %v, budget = %v", lockTTL, budget)
	}
	if _, err := RoutineExecutionBudget(SessionRunnerChatOptions{RequestTimeout: time.Duration(math.MaxInt64), MaxToolRounds: 2, MaxToolCallsPerRound: 2, MaxAttempts: 2}); err == nil {
		t.Fatal("overflowing routine budget was accepted")
	}
	if _, err := RoutineLockTTL(0, time.Duration(math.MaxInt64)); err == nil {
		t.Fatal("overflowing routine lock ttl was accepted")
	}
}

func TestRoutineSessionTerminalMatchesOnlyTheExactFencedClaim(t *testing.T) {
	claimedAt := time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC)
	newSession := func(status string) sessionstore.Session {
		return sessionstore.Session{ID: "routine-session", Runner: &sessionstore.Runner{
			RunnerID: "routine-runner", Status: status, Attempt: 2,
			ClaimedAt: claimedAt, LastHeartbeatAt: claimedAt, ExpiresAt: claimedAt.Add(time.Minute),
		}}
	}
	exact := newSession("completed")
	claim := sessionstore.RunnerClaimFromSession(exact)
	for _, status := range []string{"completed", "failed", "cancelled"} {
		if session := newSession(status); !routineSessionTerminalForClaim(session, claim) {
			t.Fatalf("exact %s claim was not recognized as terminal", status)
		}
	}

	tests := map[string]func(*sessionstore.Session){
		"running": func(session *sessionstore.Session) { session.Runner.Status = "running" },
		"session": func(session *sessionstore.Session) { session.ID = "other-session" },
		"runner":  func(session *sessionstore.Session) { session.Runner.RunnerID = "other-runner" },
		"attempt": func(session *sessionstore.Session) { session.Runner.Attempt++ },
		"token":   func(session *sessionstore.Session) { session.Runner.ClaimedAt = claimedAt.Add(time.Second) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			session := newSession("completed")
			mutate(&session)
			if routineSessionTerminalForClaim(session, claim) {
				t.Fatal("non-matching claim was accepted")
			}
		})
	}
	if routineSessionTerminalForClaim(sessionstore.Session{ID: exact.ID}, claim) {
		t.Fatal("missing runner was accepted")
	}
}

func TestRoutineMultiRoundToolRunRenewsLeaseAndIsNotCancelledEarly(t *testing.T) {
	var requests atomic.Int32
	firstRequest := make(chan struct{})
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round := requests.Add(1)
		if round == 1 {
			close(firstRequest)
		}
		if r.Header.Get("Authorization") != "Bearer routine-secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		time.Sleep(180 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		if round <= 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"id": fmt.Sprintf("routine-tool-%d", round), "type": "function",
						"function": map[string]any{"name": "settings_get", "arguments": `{"key":"routine-heartbeat"}`},
					}},
				}}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "multi-round completed"}}},
			"usage":   map[string]any{"total_tokens": 9},
		})
	}))
	defer modelAPI.Close()

	srv, store, root := newRoutineIntegrationServer(t)
	configureRoutineProvider(t, srv, store, modelAPI.URL)
	createRoutineFixture(t, store, root, "routine-multi-round", "perform two tool rounds")
	chat := SessionRunnerChatOptions{
		AllowedTools: []string{"settings_get"}, RequestTimeout: 2 * time.Second,
		MaxAttempts: 1, MaxToolRounds: 2, MaxToolCallsPerRound: 1, LeaseTTL: 100 * time.Millisecond,
		ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
	}
	executor, err := srv.NewRoutineExecutor(RoutineExecutorOptions{Chat: chat})
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	tickBudget, err := RoutineExecutionBudget(chat)
	if err != nil {
		t.Fatalf("tick budget: %v", err)
	}
	lockTTL, err := RoutineLockTTL(0, tickBudget)
	if err != nil {
		t.Fatalf("lock ttl: %v", err)
	}
	schedulerErrors := make(chan error, 1)
	scheduler, err := routinescheduler.New(routinescheduler.Options{
		Repository: store, Executor: executor, TickTimeout: tickBudget, LockTTL: lockTTL,
		ErrorBackoff: 5 * time.Millisecond, MinimumDelay: time.Millisecond,
		OnError: func(err error) {
			select {
			case schedulerErrors <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	defer stopRoutineIntegrationScheduler(t, scheduler)
	receiveRoutineSignal(t, firstRequest)
	initial, found, err := srv.sessionStore.Get("frame-routine")
	if err != nil || !found || initial.Runner == nil {
		t.Fatalf("load initial routine runner: found=%t session=%#v err=%v", found, initial, err)
	}
	initialExpiry := initial.Runner.ExpiresAt
	renewed := false
	renewalDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(renewalDeadline) {
		current, currentFound, currentErr := srv.sessionStore.Get("frame-routine")
		if currentErr != nil {
			t.Fatalf("load renewed routine runner: %v", currentErr)
		}
		if currentFound && current.Runner != nil && current.Runner.RunnerID == initial.Runner.RunnerID &&
			current.Runner.Attempt == initial.Runner.Attempt && current.Runner.ExpiresAt.After(initialExpiry) {
			renewed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !renewed {
		t.Fatal("routine runner lease was not renewed during the first model request")
	}
	owned, stolen, err := srv.sessionStore.ClaimRunner("frame-routine", "lease-stealer", time.Second)
	if err != nil {
		t.Fatalf("attempt lease steal: %v", err)
	}
	if stolen || owned.Runner == nil || !strings.HasPrefix(owned.Runner.RunnerID, defaultRoutineRunnerPrefix) {
		t.Fatalf("lease steal result: stolen=%v session=%#v", stolen, owned)
	}
	var observed workspace.Routine
	var observedErr error
	completed := false
	completionTimeout := time.Duration(chat.MaxToolRounds+1)*time.Duration(chat.MaxAttempts)*chat.RequestTimeout + 5*time.Second
	if completionTimeout > 15*time.Second {
		completionTimeout = 15 * time.Second
	}
	deadline := time.Now().Add(completionTimeout)
	for time.Now().Before(deadline) {
		select {
		case schedulerErr := <-schedulerErrors:
			current, currentFound, currentErr := srv.sessionStore.Get("frame-routine")
			t.Fatalf("routine scheduler failed before completion: %v requests=%d current_found=%t current=%#v current_runner=%#v current_err=%v initial_claim=%#v",
				schedulerErr, requests.Load(), currentFound, current, current.Runner, currentErr, initial.Runner)
		default:
		}
		observed, observedErr = store.GetRoutine("routine-multi-round")
		if observedErr == nil && observed.TickCount == 1 && observed.LastOKAt != nil && observed.LastResults == "multi-round completed" {
			completed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !completed {
		session, found, sessionErr := srv.sessionStore.Get("frame-routine")
		t.Fatalf("routine condition was not satisfied: requests=%d routine=%#v err=%v session_found=%t session=%#v session_err=%v",
			requests.Load(), observed, observedErr, found, session, sessionErr)
	}
	if requests.Load() != 3 {
		t.Fatalf("model requests = %d, want 3", requests.Load())
	}
}

func newRoutineIntegrationServer(t *testing.T) (*Server, *workspace.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{FileRoot: root, Workspace: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	return srv, store, root
}

func newRoutineTranscriptIntegrationServer(t *testing.T) (*Server, *workspace.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatalf("open Transcript repository: %v", err)
	}
	srv := New(Options{FileRoot: root, Workspace: store, Transcript: repository})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	return srv, store, root
}

func configureRoutineProvider(t *testing.T, srv *Server, store *workspace.Store, modelURL string) {
	t.Helper()
	if _, err := srv.settingsStore.Set("model.activeProviderId", "routine-provider"); err != nil {
		t.Fatalf("set active provider: %v", err)
	}
	if _, err := srv.secretStore.Create(secretstore.Secret{ID: "routine-provider-key", UserID: "user-routine", Provider: "openai", Value: "routine-secret"}); err != nil {
		t.Fatalf("create provider secret: %v", err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "routine-provider", UserID: "user-routine", Name: "Routine Provider", Type: "openai",
		BaseURL: modelURL + "/v1", Model: "routine-model", SecretRef: "secret://routine-provider-key", Enabled: &enabled,
	}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
}

func createRoutineFixture(t *testing.T, store *workspace.Store, root, routineID, instruction string) {
	t.Helper()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-routine", UserID: "user-routine", Name: "Routine Project", Path: root}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-routine", ProjectID: "project-routine", AgentName: "planner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatalf("create root frame: %v", err)
	}
	if _, err := store.CreateRoutine(workspace.CreateRoutineInput{
		ID: routineID, RootFrameID: "frame-routine", OwnerUserID: "user-routine",
		OnTick: instruction, EveryMinutes: 5, Enabled: true, NextDue: time.Now().UTC().Add(-time.Second),
	}); err != nil {
		t.Fatalf("create routine: %v", err)
	}
}

func stopRoutineIntegrationScheduler(t *testing.T, scheduler *routinescheduler.Scheduler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := scheduler.Stop(ctx); err != nil {
		t.Fatalf("stop scheduler: %v", err)
	}
}

func closeRoutineServer(t *testing.T, srv *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Fatalf("close server: %v", err)
	}
}

func receiveRoutineSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for routine signal")
	}
}

func routineEventually(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("routine condition was not satisfied")
}

func routineFrameEventExists(events []workspace.FrameEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
