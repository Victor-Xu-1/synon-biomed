package outbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"synon-go/internal/outbox"
	transcript "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/server"
)

type recordingRealtimeFanout struct {
	mu       sync.Mutex
	eventIDs []string
}

type blockingRealtimeFanout struct {
	inner   outbox.RealtimeFanout
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingRealtimeFanout) FanoutRealtimeOutbox(
	event workspace.RealtimeEvent,
	projection *workspace.RealtimeFrameProjection,
) error {
	f.once.Do(func() { close(f.entered) })
	<-f.release
	return f.inner.FanoutRealtimeOutbox(event, projection)
}

func (f *recordingRealtimeFanout) FanoutRealtimeOutbox(event workspace.RealtimeEvent, _ *workspace.RealtimeFrameProjection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventIDs = append(f.eventIDs, event.ID)
	return nil
}

func (f *recordingRealtimeFanout) IDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.eventIDs...)
}

func TestRealtimeOutboxRestartRecoveryAndTwoProcessCompetition(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.sqlite")
	primary, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	competitor, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer competitor.Close()

	app := server.New(server.Options{Workspace: primary})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.Close(ctx)
	}()
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	postRealtimeJSON(t, httpServer.URL+"/api/go/projects", map[string]any{
		"id": "project-restart", "name": "committed while dispatcher is stopped",
	})
	if _, found, err := primary.GetProject("project-restart"); err != nil || !found {
		t.Fatalf("committed project found=%v err=%v", found, err)
	}
	if events := getRealtimeEvents(t, httpServer.URL, "project-restart"); len(events) != 0 {
		t.Fatalf("event materialized while dispatcher stopped: %#v", events)
	}

	ctx, cancel := context.WithCancel(context.Background())
	doneA := startRealtimeDispatcher(t, ctx, primary, app, "process-a")
	doneB := startRealtimeDispatcher(t, ctx, competitor, app, "process-b")
	waitRealtime(t, 5*time.Second, func() bool {
		return len(getRealtimeEvents(t, httpServer.URL, "project-restart")) == 1
	})
	waitRealtime(t, 5*time.Second, func() bool {
		pending, countErr := primary.CountUndeliveredRealtimeOutbox(context.Background())
		return countErr == nil && pending == 0
	})
	if pending, err := primary.CountUndeliveredRealtimeOutbox(context.Background()); err != nil || pending != 0 {
		t.Fatalf("pending realtime outbox=%d err=%v", pending, err)
	}

	postRealtimeJSON(t, httpServer.URL+"/api/go/projects", map[string]any{
		"id": "project-competition", "name": "two SQLite processes",
	})
	waitRealtime(t, 5*time.Second, func() bool {
		return len(getRealtimeEvents(t, httpServer.URL, "project-competition")) == 1
	})
	if events := getRealtimeEvents(t, httpServer.URL, "project-competition"); len(events) != 1 {
		t.Fatalf("competition created duplicate durable events: %#v", events)
	}

	outboxID := workspace.DeriveOutboxEventID(workspace.RealtimeOutboxTopic, eventsID(getRealtimeEvents(t, httpServer.URL, "project-competition")[0]))
	envelope, err := primary.GetOutboxEvent(context.Background(), outboxID)
	if err != nil {
		t.Fatal(err)
	}
	deliverer := outbox.RealtimeDeliverer{Store: primary, Fanout: app}
	if err := deliverer.Deliver(context.Background(), envelope); err != nil {
		t.Fatalf("repeat idempotent delivery: %v", err)
	}
	if events := getRealtimeEvents(t, httpServer.URL, "project-competition"); len(events) != 1 {
		t.Fatalf("repeated delivery duplicated durable event: %#v", events)
	}

	cancel()
	waitDispatcherStopped(t, doneA)
	waitDispatcherStopped(t, doneB)
}

func TestRealtimeOutboxProjectDeleteRetiresFrameSourceAndUnblocksLaterWork(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	project, err := store.CreateProjectRealtime(ctx, workspace.CreateProjectInput{
		ID: "project-deleted-source", UserID: "local", Name: "Deleted source",
	}, "realtime-project-created")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: "frame-deleted-source", ProjectID: project.ID, AgentName: "OPERON",
		Status: "pending", ConversationType: "task",
	}, "local", "realtime-frame-created", "frame-event-created")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteProjectRealtime(ctx, project.ID, "local", "realtime-project-deleted"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProjectRealtime(ctx, workspace.CreateProjectInput{
		ID: "project-after-delete", UserID: "local", Name: "Later work",
	}, "realtime-project-after-delete"); err != nil {
		t.Fatal(err)
	}

	fanout := &recordingRealtimeFanout{}
	deliverer := outbox.RealtimeDeliverer{Store: store, Fanout: fanout}
	if _, err := store.GetOutboxEvent(ctx,
		workspace.DeriveOutboxEventID(workspace.RealtimeOutboxTopic, "realtime-frame-created")); err == nil {
		t.Fatal("project delete retained obsolete frame outbox transport")
	}
	if _, found, err := store.GetRealtimeEventByID("realtime-frame-created"); err != nil || found {
		t.Fatalf("project delete retained obsolete frame realtime event found=%v err=%v", found, err)
	}

	dispatchCtx, cancel := context.WithCancel(context.Background())
	errorsSeen := make(chan error, 8)
	dispatcher, err := outbox.NewDispatcher(store, deliverer, outbox.Options{
		WorkerID: "delete-recovery", Topics: []string{workspace.RealtimeOutboxTopic}, BatchSize: 1,
		Lease: time.Second, DeliveryLimit: 500 * time.Millisecond, PollInterval: 5 * time.Millisecond,
		ErrorBackoff: 5 * time.Millisecond, RetryBase: 5 * time.Millisecond, RetryMax: 50 * time.Millisecond,
		OnError: func(err error) { errorsSeen <- err },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(dispatchCtx) }()
	waitRealtime(t, 5*time.Second, func() bool {
		pending, err := store.CountUndeliveredRealtimeOutbox(context.Background())
		return err == nil && pending == 0
	})
	cancel()
	waitDispatcherStopped(t, done)
	close(errorsSeen)
	for deliveryErr := range errorsSeen {
		t.Fatalf("dispatcher reported obsolete source failure: %v", deliveryErr)
	}

	for _, eventID := range []string{"realtime-project-created", "realtime-project-deleted", "realtime-project-after-delete"} {
		if _, found, err := store.GetRealtimeEventByID(eventID); err != nil || !found {
			t.Fatalf("durable event %s found=%v err=%v", eventID, found, err)
		}
	}
	if _, found, err := store.GetRealtimeEventByID("realtime-frame-created"); err != nil || found {
		t.Fatalf("obsolete frame event found=%v err=%v", found, err)
	}
	for _, eventID := range fanout.IDs() {
		if eventID == "realtime-frame-created" {
			t.Fatalf("obsolete frame event reached fanout for frame %#v", frame)
		}
	}
}

func TestRealtimeOutboxMissingSourceWithoutDeleteIntentFailsClosed(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProjectRealtime(ctx, workspace.CreateProjectInput{
		ID: "project-corrupt-source", UserID: "local", Name: "Corrupt source",
	}, "realtime-corrupt-project")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: "frame-corrupt-source", ProjectID: project.ID, AgentName: "OPERON",
		Status: "pending", ConversationType: "task",
	}, "local", "realtime-corrupt-frame", "frame-event-corrupt")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate corruption/legacy deletion without the durable realtime delete
	// mutation. Mere source absence must never be accepted as retirement.
	if err := store.DeleteFrame(frame.ID); err != nil {
		t.Fatal(err)
	}
	event, err := store.GetOutboxEvent(ctx,
		workspace.DeriveOutboxEventID(workspace.RealtimeOutboxTopic, "realtime-corrupt-frame"))
	if err != nil {
		t.Fatal(err)
	}
	fanout := &recordingRealtimeFanout{}
	err = (outbox.RealtimeDeliverer{Store: store, Fanout: fanout}).Deliver(ctx, event)
	if err == nil || !strings.Contains(err.Error(), "source frame event") {
		t.Fatalf("missing source without delete intent err=%v", err)
	}
	if _, found, err := store.GetRealtimeEventByID("realtime-corrupt-frame"); err != nil || found {
		t.Fatalf("corrupt source materialized found=%v err=%v", found, err)
	}
	if ids := fanout.IDs(); len(ids) != 0 {
		t.Fatalf("corrupt source reached fanout: %#v", ids)
	}
}

func TestRealtimeOutboxDeleteRetirementIsBoundToFrameIncarnation(t *testing.T) {
	tests := []struct {
		name       string
		newOwner   string
		newProject string
	}{
		{name: "same owner and project", newOwner: "owner-a", newProject: "project-a"},
		{name: "same owner different project", newOwner: "owner-a", newProject: "project-b"},
		{name: "foreign owner", newOwner: "owner-b", newProject: "project-b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			if _, err := store.CreateProjectRealtime(ctx, workspace.CreateProjectInput{
				ID: "project-a", UserID: "owner-a", Name: "Generation A",
			}, "project-a-created"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
				ID: "reused-frame", ProjectID: "project-a", AgentName: "OPERON",
				Status: "pending", ConversationType: "agent",
			}, "owner-a", "generation-a-created", "generation-a-source"); err != nil {
				t.Fatal(err)
			}
			oldEvent, err := store.GetOutboxEvent(ctx,
				workspace.DeriveOutboxEventID(workspace.RealtimeOutboxTopic, "generation-a-created"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteFrame("reused-frame"); err != nil {
				t.Fatal(err)
			}
			if test.newProject != "project-a" {
				if _, err := store.CreateProjectRealtime(ctx, workspace.CreateProjectInput{
					ID: test.newProject, UserID: test.newOwner, Name: "Generation B",
				}, "project-b-created"); err != nil {
					t.Fatal(err)
				}
			}
			current, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
				ID: "reused-frame", ProjectID: test.newProject, AgentName: "OPERON",
				Status: "pending", ConversationType: "agent",
			}, test.newOwner, "generation-b-created", "generation-b-source")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteFrameRealtime(ctx, current, test.newOwner, "generation-b-deleted"); err != nil {
				t.Fatal(err)
			}
			fanout := &recordingRealtimeFanout{}
			err = (outbox.RealtimeDeliverer{Store: store, Fanout: fanout}).Deliver(ctx, oldEvent)
			if err == nil || !strings.Contains(err.Error(), "source frame event") {
				t.Fatalf("prior incarnation was falsely retired: %v", err)
			}
			if _, found, err := store.GetRealtimeEventByID("generation-a-created"); err != nil || found {
				t.Fatalf("prior incarnation materialized found=%t err=%v", found, err)
			}
			if ids := fanout.IDs(); len(ids) != 0 {
				t.Fatalf("prior incarnation reached fanout: %#v", ids)
			}
		})
	}
}

func TestRealtimeOutboxRejectsReusedSourceIDFromNewFrameIncarnation(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.CreateProjectRealtime(ctx, workspace.CreateProjectInput{
		ID: "project-reused-source", UserID: "owner", Name: "Reused source",
	}, "project-reused-source-created"); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: "frame-reused-source", ProjectID: "project-reused-source", AgentName: "OPERON",
		Status: "pending", ConversationType: "agent",
	}, "owner", "generation-a-realtime", "reused-frame-event")
	if err != nil {
		t.Fatal(err)
	}
	oldEvent, err := store.GetOutboxEvent(ctx,
		workspace.DeriveOutboxEventID(workspace.RealtimeOutboxTopic, "generation-a-realtime"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFrame(first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: first.ID, ProjectID: first.ProjectID, AgentName: "OPERON",
		Status: "pending", ConversationType: "agent",
	}, "owner", "generation-b-realtime", "reused-frame-event")
	if err != nil {
		t.Fatal(err)
	}
	if first.IncarnationID == second.IncarnationID {
		t.Fatalf("frame incarnation reused: %q", first.IncarnationID)
	}
	fanout := &recordingRealtimeFanout{}
	err = (outbox.RealtimeDeliverer{Store: store, Fanout: fanout}).Deliver(ctx, oldEvent)
	if err == nil || !strings.Contains(err.Error(), "source frame event") {
		t.Fatalf("old envelope adopted new source generation: %v", err)
	}
	if _, found, err := store.GetRealtimeEventByID("generation-a-realtime"); err != nil || found {
		t.Fatalf("old envelope materialized found=%t err=%v", found, err)
	}
	if ids := fanout.IDs(); len(ids) != 0 {
		t.Fatalf("old envelope reached fanout: %#v", ids)
	}
}

func TestRealtimeOutboxSkipsExplicitFrameDeleteSource(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProjectRealtime(ctx, workspace.CreateProjectInput{
		ID: "project-frame-delete", UserID: "local", Name: "Frame delete",
	}, "realtime-frame-delete-project")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: "frame-explicit-delete", ProjectID: project.ID, AgentName: "OPERON",
		Status: "pending", ConversationType: "task",
	}, "local", "realtime-frame-before-delete", "frame-event-before-delete")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFrameRealtime(ctx, frame, "local", "realtime-frame-deleted"); err != nil {
		t.Fatal(err)
	}
	event, err := store.GetOutboxEvent(ctx,
		workspace.DeriveOutboxEventID(workspace.RealtimeOutboxTopic, "realtime-frame-before-delete"))
	if err != nil {
		t.Fatal(err)
	}
	fanout := &recordingRealtimeFanout{}
	if err := (outbox.RealtimeDeliverer{Store: store, Fanout: fanout}).Deliver(ctx, event); err != nil {
		t.Fatalf("explicit frame delete should retire source: %v", err)
	}
	if _, found, err := store.GetRealtimeEventByID("realtime-frame-before-delete"); err != nil || found {
		t.Fatalf("retired frame event materialized found=%v err=%v", found, err)
	}
	if ids := fanout.IDs(); len(ids) != 0 {
		t.Fatalf("retired frame event reached fanout: %#v", ids)
	}
}

func TestRealtimeOutboxUsesImmutableProjectionAcrossConcurrentDelete(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-delete-race", UserID: "local", Name: "Delete race",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: "frame-delete-race", ProjectID: project.ID, AgentName: "OPERON",
		Status: "pending", ConversationType: "agent",
	}, "local", "realtime-delete-race", "frame-event-delete-race")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{
		FrameID: frame.ID, ContextData: map[string]any{"web_extra": map[string]any{}},
	}); err != nil {
		t.Fatal(err)
	}
	app := server.New(server.Options{Workspace: store})
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	event, err := store.GetOutboxEvent(ctx,
		workspace.DeriveOutboxEventID(workspace.RealtimeOutboxTopic, "realtime-delete-race"))
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingRealtimeFanout{
		inner: app, entered: make(chan struct{}), release: make(chan struct{}),
	}
	delivered := make(chan error, 1)
	go func() {
		delivered <- (outbox.RealtimeDeliverer{Store: store, Fanout: blocking}).Deliver(ctx, event)
	}()
	<-blocking.entered
	if _, err := store.DeleteProjectRealtime(ctx, project.ID, "local", "realtime-delete-race-project"); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	if err := <-delivered; err != nil {
		t.Fatalf("snapshot fanout after committed delete: %v", err)
	}
	if _, found, err := store.GetRealtimeEventByID("realtime-delete-race"); err != nil || found {
		t.Fatalf("retired predecessor transport found=%v err=%v", found, err)
	}
}

func TestRealtimeOutboxFreezesTranscriptAuthorityAcrossConcurrentDelete(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-transcript-delete-race", UserID: "local", Name: "Transcript delete race",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: "frame-transcript-delete-race", ProjectID: project.ID, AgentName: "OPERON",
		Status: "pending", ConversationType: "agent",
	}, "local", "realtime-transcript-delete-race", "frame-event-transcript-delete-race")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{
		FrameID: frame.ID, ContextData: map[string]any{"web_extra": map[string]any{}},
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(ctx, transcript.CreateStreamInput{
		UID: "stream-transcript-delete-race", OwnerID: "local",
		ExternalID: "frame:" + frame.ID, SessionID: frame.ID, Kind: transcript.StreamKindFrameRef,
		ProjectID: project.ID, RootFrameID: frame.RootFrameID, FrameID: frame.ID, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	app := server.New(server.Options{Workspace: store, Transcript: repo})
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	event, err := store.GetOutboxEvent(ctx,
		workspace.DeriveOutboxEventID(workspace.RealtimeOutboxTopic, "realtime-transcript-delete-race"))
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingRealtimeFanout{
		inner: app, entered: make(chan struct{}), release: make(chan struct{}),
	}
	delivered := make(chan error, 1)
	go func() {
		delivered <- (outbox.RealtimeDeliverer{Store: store, Fanout: blocking}).Deliver(ctx, event)
	}()
	<-blocking.entered
	if _, err := store.DeleteProjectRealtime(ctx, project.ID, "local", "delete-transcript-race-project"); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	if err := <-delivered; err != nil {
		t.Fatalf("transcript snapshot fanout after delete: %v", err)
	}
	legacyProjectionID := "web-runtime:frame-event-transcript-delete-race"
	if _, found, err := store.GetRealtimeEventByID(legacyProjectionID); err != nil || found {
		t.Fatalf("transcript authority switched to legacy projection found=%t err=%v", found, err)
	}
}

func startRealtimeDispatcher(t *testing.T, ctx context.Context, store *workspace.Store, app *server.Server, workerID string) <-chan error {
	t.Helper()
	dispatcher, err := outbox.NewDispatcher(store, outbox.RealtimeDeliverer{Store: store, Fanout: app}, outbox.Options{
		WorkerID: workerID, Topics: []string{workspace.RealtimeOutboxTopic}, BatchSize: 2,
		Lease: time.Second, DeliveryLimit: 500 * time.Millisecond, PollInterval: 5 * time.Millisecond,
		ErrorBackoff: 5 * time.Millisecond, RetryBase: 5 * time.Millisecond, RetryMax: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	return done
}

func postRealtimeJSON(t *testing.T, target string, input any) {
	t.Helper()
	raw, err := json.Marshal(input)
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
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s status=%d body=%s", target, response.StatusCode, raw)
	}
}

func getRealtimeEvents(t *testing.T, baseURL, projectID string) []map[string]any {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/events?project_id="+projectID+"&limit=100", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Synon-User-Id", "local")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		Events []map[string]any `json:"events"`
	}
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("GET events status=%d body=%s", response.StatusCode, raw)
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result.Events
}

func eventsID(event map[string]any) string {
	if value, ok := event["id"].(string); ok {
		return value
	}
	return ""
}

func waitRealtime(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("realtime condition was not satisfied")
}

func waitDispatcherStopped(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dispatcher stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop")
	}
}
