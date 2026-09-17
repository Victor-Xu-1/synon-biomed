package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/server"
)

func TestRoutineCreateKeepsSchedulerWakeWithRealtimeOutboxWiring(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := newSynonTestServer(t, server.Options{Workspace: store})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.Close(ctx)
	}()
	dispatcher, err := newRealtimeOutboxDispatcher(store, app)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("stop dispatcher: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("dispatcher did not stop")
		}
	}()

	var wakes atomic.Int32
	httpServer := httptest.NewServer(withRoutineSchedulerWake(app.Handler(), func() { wakes.Add(1) }))
	defer httpServer.Close()
	postCommandJSON(t, httpServer.URL+"/api/go/projects", map[string]any{"id": "project-routine", "name": "Routine"})
	postCommandJSON(t, httpServer.URL+"/api/go/projects/project-routine/frames", map[string]any{
		"id": "frame-routine", "agentName": "planner", "status": "running", "conversationType": "task",
	})
	postCommandJSON(t, httpServer.URL+"/api/go/routines", map[string]any{
		"id": "routine-a", "rootFrameId": "frame-routine", "ownerUserId": "local",
		"onTick": "continue", "everyMinutes": 5, "enabled": true,
		"nextDue": time.Now().UTC().Add(time.Hour),
	})
	if wakes.Load() != 1 {
		t.Fatalf("routine scheduler wakes=%d want=1", wakes.Load())
	}
	cmdEventually(t, 3*time.Second, func() bool {
		events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
			UserID: "local", ProjectID: "project-routine", Type: "routine_update", Limit: 10,
		})
		return err == nil && len(events) == 1 && events[0].Payload["action"] == "created"
	})
}

func TestKernelSettlementOutboxDispatcherStartsAndStopsWithoutWork(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dispatcher, err := newKernelSettlementOutboxDispatcher(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stop kernel settlement dispatcher: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("kernel settlement dispatcher did not stop")
	}
}

func TestTaskOperationDispatcherStartsAndStopsWithoutWork(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := server.New(server.Options{Workspace: store, FileRoot: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunTaskOperationDispatcher(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("task operation dispatcher did not stop")
	}
}

func postCommandJSON(t *testing.T, target string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "local")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s status=%d body=%s", target, response.StatusCode, body)
	}
}

func cmdEventually(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied")
}
