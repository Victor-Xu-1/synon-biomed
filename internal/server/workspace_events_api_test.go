package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceEventSSEReplaysDurableFrameEvents(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: project.ID, AgentName: "planner", Status: "running", ConversationType: "task"})
	if err != nil {
		t.Fatalf("create frame: %v", err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: frame.ID, Type: "artifact_created", Payload: map[string]any{"artifactId": "artifact-1"}}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	server := httptest.NewServer(New(Options{Workspace: store}).Handler())
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/go/events/stream?frame_id=frame-1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("X-Synon-User-Id", "local")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open sse: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sse status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("sse content type = %q", got)
	}
	reader := bufio.NewReader(response.Body)
	deadline := time.After(2 * time.Second)
	lines := make([]string, 0, 4)
	for len(lines) < 3 {
		lineCh := make(chan string, 1)
		errCh := make(chan error, 1)
		go func() {
			line, err := reader.ReadString('\n')
			if err != nil {
				errCh <- err
				return
			}
			lineCh <- line
		}()
		select {
		case line := <-lineCh:
			lines = append(lines, line)
		case err := <-errCh:
			t.Fatalf("read sse line: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for sse replay")
		}
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "event: artifact_created") || !strings.Contains(joined, "artifact-1") {
		t.Fatalf("sse payload = %q", joined)
	}
}

func TestWorkspaceEventsRequireFrameOwnership(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: project.ID, AgentName: "planner", Status: "running", ConversationType: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: frame.ID, Type: "user_message", Payload: map[string]any{"text": "private"}}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	unauthenticated := httptest.NewRecorder()
	app.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/go/events?frame_id=frame-1", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	foreign := httptest.NewRecorder()
	foreignRequest := httptest.NewRequest(http.MethodGet, "/api/go/events?frame_id=frame-1", nil)
	foreignRequest.Header.Set("X-Synon-User-Id", "user-2")
	app.ServeHTTP(foreign, foreignRequest)
	if foreign.Code != http.StatusNotFound || strings.Contains(foreign.Body.String(), "private") {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	owner := httptest.NewRecorder()
	ownerRequest := httptest.NewRequest(http.MethodGet, "/api/go/events?frame_id=frame-1", nil)
	ownerRequest.Header.Set("X-Synon-User-Id", "user-1")
	app.ServeHTTP(owner, ownerRequest)
	if owner.Code != http.StatusOK || !strings.Contains(owner.Body.String(), "private") {
		t.Fatalf("owner status=%d body=%s", owner.Code, owner.Body.String())
	}
}

func TestWorkspaceEventHubDisconnectsLaggingSubscriber(t *testing.T) {
	hub := newWorkspaceEventHub()
	channel, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	disconnected := 0
	for sequence := int64(1); sequence <= workspaceEventSubscriberBuffer+1; sequence++ {
		disconnected += hub.Publish(workspace.FrameEvent{ID: "event", FrameID: "frame", Sequence: sequence, Type: "text_chunk"})
	}
	if disconnected != 1 {
		t.Fatalf("disconnected=%d", disconnected)
	}
	count := 0
	for range channel {
		count++
	}
	if count != workspaceEventSubscriberBuffer {
		t.Fatalf("buffered events=%d", count)
	}
}
