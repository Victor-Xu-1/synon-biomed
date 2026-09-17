package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	workspace "synon-go/internal/persistence/workspace"
)

func TestArtifactFolderNoteAndRoutineMutationsPublishBaselineEvents(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	serverApp := New(Options{Workspace: store})
	startServerRealtimeOutbox(t, store, serverApp)
	app := serverApp.Handler()

	httpServer := httptest.NewServer(app)
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	websocketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/events/ws?userId=local&after_sequence=0"
	liveConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if connected := readCompatWebSocketTest(t, ctx, liveConnection); connected["type"] != "connected" {
		t.Fatalf("live websocket handshake=%#v", connected)
	}

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects", map[string]any{"id": "project-1", "name": "Domain events"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/projects/project-1", map[string]any{"name": "Updated domain events"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/project-1/frames", map[string]any{
		"id": "frame-1", "agentName": "planner", "status": "running", "conversationType": "task",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/artifacts/artifact-1/versions", map[string]any{
		"projectId": "project-1", "name": "draft.md", "kind": "markdown", "content": "v1", "createdBy": "planner",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/artifacts/artifact-1", map[string]any{"name": "report.md"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/artifacts/artifact-1/priority", map[string]any{"priority": "user_starred"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/project-1/folders", map[string]any{"id": "folder-1", "name": "Evidence"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/folders/folder-1", map[string]any{"name": "Primary evidence"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/artifacts/artifact-1/folder", map[string]any{"folderId": "folder-1"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/project-1/notes", map[string]any{
		"id": "note-1", "userId": "local", "targetType": "artifact", "targetFrameId": "frame-1",
		"targetArtifactId": "artifact-1", "content": "Review",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/notes/note-1", map[string]any{"userId": "local", "content": "Reviewed"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/notes/note-1?user_id=local", nil, http.StatusOK)
	now := time.Now().UTC().Truncate(time.Second)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/routines", map[string]any{
		"id": "routine-1", "rootFrameId": "frame-1", "ownerUserId": "local", "label": "Daily",
		"onTick": "summarize", "everyMinutes": 60, "enabled": true, "nextDue": now,
	}, http.StatusOK)
	claim := claimRoutineForTest(t, app, now.Add(time.Second), "domain-events-claim")
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/routines/routine-1/complete", map[string]any{
		"at": now.Add(time.Minute), "successful": true, "result": "done",
		"claimToken": claim.ClaimToken, "claimGeneration": claim.ClaimGeneration, "lockedAt": claim.LockedAt,
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/artifacts/artifact-1", nil, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/folders/folder-1", nil, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/frames/frame-1", map[string]any{"status": "completed", "name": "Finished"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/frames/frame-1", nil, http.StatusOK)
	waitServerRealtimeOutbox(t, store)

	listed := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?project_id=project-1&limit=100", "local", nil, http.StatusOK)
	events := listed["events"].([]any)
	expectedEventCount := len(events)
	targetEventCounts := map[string]int{
		"artifact_created": 1, "artifact_deleted": 1, "artifact_priority_update": 1,
		"artifact_renamed": 1, "artifact_moved": 1, "folder_created": 1,
		"folder_updated": 1, "folder_deleted": 1, "note_update": 3,
		"lineage_ready": 1, "routine_update": 3,
	}
	liveEvents, err := readDomainWebSocketEvents(ctx, liveConnection, targetEventCounts, expectedEventCount)
	if err != nil {
		t.Fatalf("live WebSocket received %v, durable events %v: %v",
			domainEventTypes(liveEvents), domainRawEventTypes(events), err)
	}
	if err := liveConnection.Close(websocket.StatusNormalClosure, "reconnect for replay"); err != nil {
		t.Fatal(err)
	}
	replayConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer replayConnection.Close(websocket.StatusNormalClosure, "test complete")
	if connected := readCompatWebSocketTest(t, ctx, replayConnection); connected["type"] != "connected" {
		t.Fatalf("replay websocket handshake=%#v", connected)
	}
	replayedEvents, err := readDomainWebSocketEvents(ctx, replayConnection, targetEventCounts, expectedEventCount)
	if err != nil {
		t.Fatalf("replay WebSocket received %v, durable events %v: %v",
			domainEventTypes(replayedEvents), domainRawEventTypes(events), err)
	}
	if !reflect.DeepEqual(liveEvents, replayedEvents) {
		t.Fatalf("live and replayed domain events differ:\nlive=%#v\nreplayed=%#v", liveEvents, replayedEvents)
	}

	wantCounts := map[string]int{
		"frame_update": 4, "artifact_created": 1, "lineage_ready": 1,
		"artifact_renamed": 1, "artifact_priority_update": 1, "artifact_moved": 1,
		"artifact_deleted": 1, "folder_created": 1, "folder_updated": 1,
		"folder_deleted": 1, "note_update": 3, "routine_update": 3,
	}
	gotCounts := map[string]int{}
	deletedFrameEvents := 0
	for _, raw := range events {
		event := raw.(map[string]any)
		gotCounts[event["type"].(string)]++
		if event["userId"] != "local" || event["projectId"] != "project-1" {
			t.Fatalf("event escaped owner/project scope: %#v", event)
		}
		if len(event["invalidations"].([]any)) == 0 {
			t.Fatalf("event has no query invalidations: %#v", event)
		}
		switch event["type"] {
		case "artifact_created", "artifact_deleted", "artifact_priority_update", "artifact_renamed", "artifact_moved":
			assertDomainInvalidation(t, event, "artifacts", []any{"artifacts", "project-1"}, "debounced", "exact")
			assertDomainInvalidation(t, event, "artifact", []any{"artifact", "artifact-1"}, "immediate", "exact")
			assertDomainInvalidation(t, event, "artifactVersions", []any{"artifact-versions", "artifact-1"}, "immediate", "exact")
		case "folder_created", "folder_updated", "folder_deleted":
			assertDomainInvalidation(t, event, "folders", []any{"folders", "project-1"}, "immediate", "exact")
		case "note_update":
			assertDomainInvalidation(t, event, "notes", []any{"notes", "project-1"}, "debounced", "exact")
		case "lineage_ready":
			payload := event["payload"].(map[string]any)
			versionIDs := payload["version_ids"].([]any)
			if len(versionIDs) != 1 {
				t.Fatalf("lineage_ready version ids=%#v", versionIDs)
			}
			assertDomainInvalidation(t, event, "artifactLineage",
				[]any{"artifact-lineage", versionIDs[0]}, "immediate", "exact")
		case "routine_update":
			assertDomainInvalidation(t, event, "routines", []any{"routines", "project-1"}, "immediate", "exact")
		}
		payload := event["payload"].(map[string]any)
		if event["type"] == "frame_update" && payload["action"] == "deleted" {
			deletedFrameEvents++
			if payload["frame_id"] != "frame-1" || payload["project_id"] != "project-1" {
				t.Fatalf("frame delete payload=%#v", payload)
			}
		}
		switch event["type"] {
		case "artifact_created":
			artifact, _ := payload["artifact"].(map[string]any)
			if artifact["id"] != "artifact-1" || artifact["filename"] != "draft.md" || artifact["version_id"] == "" {
				t.Fatalf("artifact_created payload is not v1.1 compatible: %#v", payload)
			}
		case "artifact_renamed":
			if payload["new_filename"] != "report.md" {
				t.Fatalf("artifact_renamed payload=%#v", payload)
			}
		case "artifact_moved":
			if payload["new_folder_id"] != "folder-1" {
				t.Fatalf("artifact_moved payload=%#v", payload)
			}
		case "folder_created", "folder_updated":
			folder, _ := payload["folder"].(map[string]any)
			if folder["id"] != "folder-1" || folder["name"] == "" {
				t.Fatalf("folder payload=%#v", payload)
			}
		}
	}
	if deletedFrameEvents != 1 {
		t.Fatalf("durable frame delete events=%d", deletedFrameEvents)
	}
	for eventType, want := range wantCounts {
		if gotCounts[eventType] != want {
			t.Fatalf("event count %s=%d want=%d all=%#v", eventType, gotCounts[eventType], want, gotCounts)
		}
	}
}

func assertDomainInvalidation(t *testing.T, event map[string]any, query string, key []any, policy, match string) {
	t.Helper()
	for _, raw := range event["invalidations"].([]any) {
		invalidation := raw.(map[string]any)
		if invalidation["query"] != query {
			continue
		}
		if !reflect.DeepEqual(invalidation["key"], key) ||
			invalidation["policy"] != policy || invalidation["match"] != match {
			t.Fatalf("event %s invalidation %s=%#v", event["type"], query, invalidation)
		}
		return
	}
	t.Fatalf("event %s missing invalidation %s: %#v", event["type"], query, event["invalidations"])
}

func readDomainWebSocketEvents(ctx context.Context, connection *websocket.Conn, want map[string]int, maxMessages int) ([]map[string]any, error) {
	events := make([]map[string]any, 0, maxMessages)
	counts := make(map[string]int, len(want))
	for inspected := 0; inspected < maxMessages; inspected++ {
		readContext, cancel := context.WithTimeout(ctx, time.Second)
		_, raw, err := connection.Read(readContext)
		cancel()
		if err != nil {
			return events, err
		}
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			return events, err
		}
		eventType := stringValue(event["type"])
		if _, tracked := want[eventType]; !tracked {
			continue
		}
		events = append(events, event)
		counts[eventType]++
		complete := true
		for target, count := range want {
			if counts[target] != count {
				complete = false
				break
			}
		}
		if complete {
			return events, nil
		}
	}
	return events, fmt.Errorf("target event counts=%v, want=%v", counts, want)
}

func domainEventTypes(events []map[string]any) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, stringValue(event["type"]))
	}
	return types
}

func domainRawEventTypes(events []any) []string {
	types := make([]string, 0, len(events))
	for _, raw := range events {
		event, _ := raw.(map[string]any)
		types = append(types, stringValue(event["type"]))
	}
	return types
}
