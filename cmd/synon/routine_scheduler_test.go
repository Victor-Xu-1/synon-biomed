package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/config"
	runtimekv "synon-go/internal/persistence/runtimekv"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/server"
)

func TestRoutineRunnerOptionsNeverUseDeterministicBuiltin(t *testing.T) {
	options := routineSessionRunnerChatOptions(config.Config{Runner: config.RunnerConfig{
		Enabled: true, Provider: "go_builtin", ChatEndpoint: server.BuiltinSessionRunnerChatEndpoint,
		ChatModel: server.BuiltinSessionRunnerChatModel, ChatAPIKey: "must-not-be-used",
	}})
	if options.Endpoint != "" || options.Model != "" || options.APIKey != "" || !options.RequireSavedModel {
		t.Fatalf("builtin leaked into routine options: %#v", options)
	}
	if options.RequestTimeout <= 0 || options.LeaseTTL <= 0 || options.RunnerID == "" {
		t.Fatalf("routine defaults were not normalized: %#v", options)
	}
}

func TestRoutineSchedulerFactoryUsesFullExecutionBudgetAndRejectsOverflow(t *testing.T) {
	chat := server.SessionRunnerChatOptions{RequestTimeout: 100 * time.Millisecond, MaxToolRounds: 2, MaxToolCallsPerRound: 3, MaxAttempts: 3}
	tickTimeout, lockTTL, err := routineSchedulerDurations(chat, time.Second)
	if err != nil {
		t.Fatalf("scheduler durations: %v", err)
	}
	minimumModelBudget := 900 * time.Millisecond
	if tickTimeout < minimumModelBudget || lockTTL <= tickTimeout {
		t.Fatalf("tickTimeout=%v lockTTL=%v minimumModelBudget=%v", tickTimeout, lockTTL, minimumModelBudget)
	}
	if strconv.IntSize == 64 {
		overflowSeconds := int(math.MaxInt64/int64(time.Second) + 1)
		_, err := newRoutineScheduler(config.Config{Runner: config.RunnerConfig{ChatTimeoutSeconds: overflowSeconds}}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "maximum representable duration") {
			t.Fatalf("overflow config error = %v", err)
		}
	}
}

func TestRoutineMutationMiddlewareWakesAfterMutation(t *testing.T) {
	var wakes atomic.Int32
	var handled atomic.Bool
	handler := withRoutineSchedulerWake(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handled.Store(true)
		w.WriteHeader(http.StatusCreated)
	}), func() {
		if !handled.Load() {
			t.Error("wake ran before mutation handler completed")
		}
		wakes.Add(1)
	})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/go/routines", nil),
		httptest.NewRequest(http.MethodPut, "/api/go/routines/routine-1", nil),
		httptest.NewRequest(http.MethodPatch, "/api/go/routines/routine-1", nil),
		httptest.NewRequest(http.MethodDelete, "/api/go/routines/routine-1", nil),
	} {
		handled.Store(false)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	if wakes.Load() != 4 {
		t.Fatalf("routine mutation wakes = %d, want 4", wakes.Load())
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/go/routines", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/projects", nil))
	if wakes.Load() != 4 {
		t.Fatalf("non-mutation requests changed wakes to %d", wakes.Load())
	}
}

func TestRoutineSchedulerFactoryAuditsMissingProviderFailure(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	defer store.Close()
	app := newSynonTestServer(t, server.Options{FileRoot: root, Workspace: store})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.Close(ctx)
	}()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-audit", UserID: "user-audit", Name: "Audit", Path: root}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-audit", ProjectID: "project-audit", AgentName: "planner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatalf("create frame: %v", err)
	}
	if _, err := store.CreateRoutine(workspace.CreateRoutineInput{
		ID: "routine-audit", RootFrameID: "frame-audit", OwnerUserID: "user-audit",
		OnTick: "must require a saved provider", EveryMinutes: 5, Enabled: true,
		NextDue: time.Now().UTC().Add(-time.Second),
	}); err != nil {
		t.Fatalf("create routine: %v", err)
	}
	scheduler, err := newRoutineScheduler(config.Config{HomeDir: root, Runner: config.RunnerConfig{Provider: "go_builtin"}}, app, store)
	if err != nil {
		t.Fatalf("new routine scheduler: %v", err)
	}
	serviceContext, cancelService := context.WithCancel(context.Background())
	if err := startRoutineScheduler(serviceContext, scheduler); err != nil {
		t.Fatalf("start routine scheduler: %v", err)
	}
	defer func() {
		cancelService()
		stopRoutineScheduler(app, scheduler)
	}()

	auditStore := runtimekv.New(filepath.Join(root, "runtime-state.sqlite"))
	// The factory path loads the signed agent catalog before the missing saved
	// provider is reported. Under the repository-wide parallel test gate that
	// real initialization can legitimately exceed three seconds, so wait for
	// the durable routine and audit conditions instead of racing CPU load.
	if cmdRoutineEventually(t, 10*time.Second, func() bool {
		routine, routineErr := store.GetRoutine("routine-audit")
		audits, auditErr := auditStore.List("routine-scheduler-audit")
		return routineErr == nil && routine.TickCount == 1 && routine.IdleStreak == 1 &&
			strings.Contains(routine.LastResults, "No active model configuration is available") &&
			auditErr == nil && len(audits) > 0
	}) {
		return
	}
	routine, routineErr := store.GetRoutine("routine-audit")
	audits, auditErr := auditStore.List("routine-scheduler-audit")
	t.Fatalf("routine=%#v routine_err=%v audits=%#v audit_err=%v", routine, routineErr, audits, auditErr)
}

func cmdRoutineEventually(t *testing.T, timeout time.Duration, predicate func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
