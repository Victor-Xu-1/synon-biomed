package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	workspace "synon-go/internal/persistence/workspace"
)

func TestComputeMutationsPublishOwnerScopedLiveAndReplayEvents(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SetBYOCEnabled("modal", "owner-a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-a", UserID: "owner-a", Name: "Compute realtime",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root-a", ProjectID: "project-a", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}

	serverApp := New(Options{Workspace: store})
	startServerRealtimeOutbox(t, store, serverApp)
	httpServer := httptest.NewServer(serverApp.Handler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	websocketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/events/ws?userId=owner-a&after_sequence=0"
	liveConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	liveConnection.SetReadLimit(1 << 20)
	if connected := readCompatWebSocketTest(t, ctx, liveConnection); connected["type"] != "connected" {
		t.Fatalf("live websocket handshake=%#v", connected)
	}

	rootID, frameID := "root-a", "root-a"
	job, err := store.CreateComputeJob("owner-a", workspace.ComputeJob{
		JobID: "job-realtime", ProjectID: "project-a", Environment: "python",
		TierType: "gpu", Provider: "byoc:modal", RootFrameID: &rootID, FrameID: &frameID,
		ProviderFamily: "byoc", ProviderLabel: "Modal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindComputeJobExternal("owner-a", job.JobID, "sandbox-a", "https://example.invalid/sandbox-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendComputeJobLog("owner-a", job.JobID, "stdout", strings.Repeat("a", 70000)+"done"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionComputeJob("owner-a", job.JobID, workspace.ComputeJobDone, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManagedEndpoint(workspace.ManagedEndpoint{
		Name: "endpoint-a", RegisteredBy: "owner-a", State: "live", Location: "local",
		URL: "http://127.0.0.1:9444/v1", Port: 9444, StopScript: "true",
	}); err != nil {
		t.Fatal(err)
	}
	endpoint, claimed, err := store.ClaimManagedEndpointStop("endpoint-a", "owner-a")
	if err != nil || !claimed {
		t.Fatalf("claim endpoint=%#v claimed=%v err=%v", endpoint, claimed, err)
	}
	if state, err := store.FinishManagedEndpointStop(endpoint.ClaimHandle, "endpoint stopped", nil); err != nil || state != "stopped" {
		t.Fatalf("finish state=%q err=%v", state, err)
	}
	if deleted, err := store.DeleteManagedEndpoint("endpoint-a", "owner-a"); err != nil || !deleted {
		t.Fatalf("delete endpoint=%v err=%v", deleted, err)
	}
	waitServerRealtimeOutbox(t, store)

	want := map[string]int{
		"compute_job_update":          3,
		"compute_job_log_chunk":       1,
		"managed_endpoint_transcript": 1,
		"managed_endpoint_update":     3,
		"managed_endpoint_removed":    1,
	}
	liveEvents, err := readDomainWebSocketEvents(ctx, liveConnection, want, 9)
	if err != nil {
		t.Fatalf("live compute events=%v err=%v", domainEventTypes(liveEvents), err)
	}
	if err := liveConnection.Close(websocket.StatusNormalClosure, "reconnect for replay"); err != nil {
		t.Fatal(err)
	}
	replayConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayConnection.SetReadLimit(1 << 20)
	defer replayConnection.Close(websocket.StatusNormalClosure, "test complete")
	if connected := readCompatWebSocketTest(t, ctx, replayConnection); connected["type"] != "connected" {
		t.Fatalf("replay websocket handshake=%#v", connected)
	}
	replayed, err := readDomainWebSocketEvents(ctx, replayConnection, want, 9)
	if err != nil {
		t.Fatalf("replayed compute events=%v err=%v", domainEventTypes(replayed), err)
	}
	if !reflect.DeepEqual(liveEvents, replayed) {
		t.Fatalf("live and replayed compute events differ:\nlive=%#v\nreplayed=%#v", liveEvents, replayed)
	}

	listed := runtimeCompatJSON(t, serverApp.Handler(), "GET", "/api/events?limit=100", "owner-a", nil, 200)
	durable := listed["events"].([]any)
	if len(durable) != 9 {
		t.Fatalf("durable compute event count=%d want=9", len(durable))
	}
	for _, raw := range durable {
		event := raw.(map[string]any)
		if event["userId"] != "owner-a" {
			t.Fatalf("event escaped owner scope: %#v", event)
		}
		payload := event["payload"].(map[string]any)
		switch event["type"] {
		case "compute_job_update":
			if event["projectId"] != "project-a" || event["rootFrameId"] != "root-a" ||
				event["frameId"] != "root-a" || payload["job_id"] != "job-realtime" {
				t.Fatalf("compute update scope/payload=%#v", event)
			}
			assertDomainInvalidation(t, event, "computeJobs",
				[]any{"compute-jobs", "project-a"}, "immediate", "exact")
		case "compute_job_log_chunk":
			chunk, _ := payload["chunk"].(string)
			if payload["stream"] != "out" || len([]byte(chunk)) > 32<<10 ||
				!strings.HasSuffix(chunk, "done") || len(event["invalidations"].([]any)) != 0 {
				t.Fatalf("bounded log event=%#v", event)
			}
		case "managed_endpoint_update", "managed_endpoint_removed":
			if payload["name"] != "endpoint-a" {
				t.Fatalf("managed event payload=%#v", payload)
			}
			assertDomainInvalidation(t, event, "managedEndpoints",
				[]any{"operon", "compute", "managed-endpoints"}, "immediate", "exact")
			assertDomainInvalidation(t, event, "computeProviders",
				[]any{"operon", "compute", "providers"}, "immediate", "exact")
		case "managed_endpoint_transcript":
			if payload["phase"] != "stop" || payload["text"] != "endpoint stopped" ||
				payload["done"] != true || len(event["invalidations"].([]any)) != 0 {
				t.Fatalf("managed transcript payload=%#v", event)
			}
		}
	}
	foreign := runtimeCompatJSON(t, serverApp.Handler(), "GET", "/api/events?limit=100", "owner-b", nil, 200)
	if events := foreign["events"].([]any); len(events) != 0 {
		t.Fatalf("foreign owner received compute events=%#v", events)
	}
}
