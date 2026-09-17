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
	"testing"
	"time"

	"synon-go/internal/outbox"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/server"
)

func TestArtifactOutboxSurvivesStoppedDispatcherAndPublishesV11Payload(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := server.New(server.Options{Workspace: store})
	defer app.Close(context.Background())
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	requestDomainJSON(t, httpServer.URL+"/api/go/projects", http.MethodPost,
		map[string]any{"id": "project-a", "name": "Project"}, "project-create", http.StatusOK)
	requestDomainJSON(t, httpServer.URL+"/api/go/artifacts/rejected-key/versions", http.MethodPost,
		map[string]any{"projectId": "project-a", "name": "bad.txt", "kind": "text/plain", "content": "bad"},
		strings.Repeat("x", 257), http.StatusBadRequest)
	versionRequest := map[string]any{"projectId": "project-a", "name": "report.txt", "kind": "text/plain", "content": "v1"}
	firstResult := requestDomainJSONResult(t, httpServer.URL+"/api/go/artifacts/artifact-a/versions", http.MethodPost,
		versionRequest, "artifact-version-one", http.StatusOK)
	replayedResult := requestDomainJSONResult(t, httpServer.URL+"/api/go/artifacts/artifact-a/versions", http.MethodPost,
		versionRequest, "artifact-version-one", http.StatusOK)
	firstVersion := firstResult["version"].(map[string]any)
	replayedVersion := replayedResult["version"].(map[string]any)
	if firstVersion["id"] != replayedVersion["id"] {
		t.Fatalf("HTTP retry returned a different version: first=%#v replay=%#v", firstVersion, replayedVersion)
	}
	requestDomainJSON(t, httpServer.URL+"/api/go/artifacts/artifact-a/versions", http.MethodPost,
		map[string]any{"projectId": "project-a", "name": "report.txt", "kind": "text/plain", "content": "changed"},
		"artifact-version-one", http.StatusConflict)
	if events := getRealtimeEvents(t, httpServer.URL, "project-a"); len(events) != 0 {
		t.Fatalf("events materialized while dispatcher stopped: %#v", events)
	}
	if pending, err := store.CountUndeliveredRealtimeOutbox(context.Background()); err != nil || pending != 3 {
		t.Fatalf("pending outbox=%d err=%v", pending, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startRealtimeDispatcher(t, ctx, store, app, "artifact-restart-worker")
	waitRealtime(t, 5*time.Second, func() bool { return len(getRealtimeEvents(t, httpServer.URL, "project-a")) == 3 })
	events := getRealtimeEvents(t, httpServer.URL, "project-a")
	var created map[string]any
	for _, event := range events {
		if event["type"] == "artifact_created" {
			created = event
		}
	}
	if created == nil {
		t.Fatalf("artifact_created missing from %#v", events)
	}
	payload, _ := created["payload"].(map[string]any)
	artifact, _ := payload["artifact"].(map[string]any)
	if artifact["id"] != "artifact-a" || artifact["filename"] != "report.txt" || artifact["version_id"] == "" {
		t.Fatalf("v1.1 artifact payload=%#v", payload)
	}
	if invalidations, _ := created["invalidations"].([]any); len(invalidations) == 0 {
		t.Fatalf("artifact event has no 60-key invalidations: %#v", created)
	}

	outboxID := workspace.DeriveOutboxEventID(workspace.RealtimeOutboxTopic, eventsID(created))
	envelope, err := store.GetOutboxEvent(context.Background(), outboxID)
	if err != nil {
		t.Fatal(err)
	}
	if err := (outbox.RealtimeDeliverer{Store: store, Fanout: app}).Deliver(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if events := getRealtimeEvents(t, httpServer.URL, "project-a"); len(events) != 3 {
		t.Fatalf("repeat delivery duplicated event: %#v", events)
	}
	cancel()
	waitDispatcherStopped(t, done)
}

func requestDomainJSON(t *testing.T, target, method string, input any, idempotencyKey string, wantStatus int) {
	requestDomainJSONResult(t, target, method, input, idempotencyKey, wantStatus)
}

func requestDomainJSONResult(t *testing.T, target, method string, input any, idempotencyKey string, wantStatus int) map[string]any {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, target, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "local")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s status=%d want=%d body=%s", method, target, response.StatusCode, wantStatus, raw)
	}
	result := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode %s %s response: %v", method, target, err)
	}
	return result
}
