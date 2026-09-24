package server

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/persistence/runtimekv"
)

func TestWebContextUsageMissingRecordIsExplicit(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "context-usage", "local")
	created := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Context usage", "assistant": map[string]any{"id": "synonbiomed:OPERON"},
		"extra": map[string]any{"project_id": project.ID},
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create HTTP %d", created.Code)
	}
	id := webString(p3DecodeObject(t, created)["id"])
	response := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+id+"/context-usage", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("context usage HTTP %d: %s", response.Code, response.Body.String())
	}
	if status := p3DecodeObject(t, response)["status"]; status != "unavailable" {
		t.Fatalf("missing telemetry status = %v", status)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("context usage must not be cached")
	}
	foreign := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+id+"/context-usage", nil, "foreign-user")
	if foreign.Code != http.StatusNotFound && foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign context access HTTP %d", foreign.Code)
	}
	recorder := newSessionContextUsageRecorder(app, id, 1, SessionRunnerChatOptions{})
	if recorder.begin("model", agentruntime.ModelRequest{}) == nil {
		t.Fatal("record context usage")
	}
	ready := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+id+"/context-usage", nil, "")
	payload := p3DecodeObject(t, ready)
	snapshot, _ := payload["snapshot"].(map[string]any)
	if ready.Code != http.StatusOK || payload["status"] != "available" || snapshot["usedTokens"] != float64(0) {
		t.Fatalf("zero context usage HTTP=%d payload=%+v", ready.Code, payload)
	}
	post := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+id+"/context-usage", nil, "")
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("unexpected mutation allowed: HTTP %d", post.Code)
	}
	if _, err := app.runtimeStore.DeleteScope(context.Background(), runtimekv.Scope{FrameID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.runtimeStore.Set(contextUsageNamespace(id), "latest", map[string]any{"sessionId": id, "usedTokens": -1}); err != nil {
		t.Fatal(err)
	}
	invalid := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+id+"/context-usage", nil, "")
	if invalid.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid durable record served as usage: HTTP %d", invalid.Code)
	}
	deleted := p3JSONRequest(t, app, http.MethodDelete, "/api/conversations/"+id, nil, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete HTTP %d: %s", deleted.Code, deleted.Body.String())
	}
	if recorder.begin("late-model", agentruntime.ModelRequest{}) != nil {
		t.Fatal("late model call recreated a deleted task usage record")
	}
	if _, found, err := app.runtimeStore.Get(contextUsageNamespace(id), "latest"); err != nil || found {
		t.Fatalf("deleted task retains context usage: found=%v err=%v", found, err)
	}
}

type contextUsageBlockingSchema struct {
	started chan struct{}
	release chan struct{}
}

func (schema contextUsageBlockingSchema) MarshalJSON() ([]byte, error) {
	close(schema.started)
	<-schema.release
	return []byte(`{"type":"string"}`), nil
}

func TestWebContextUsageConcurrentDeleteFencesPendingWrite(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "context-delete-race", "local")
	created := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Context delete", "assistant": map[string]any{"id": "synonbiomed:OPERON"},
		"extra": map[string]any{"project_id": project.ID},
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create HTTP %d", created.Code)
	}
	id := webString(p3DecodeObject(t, created)["id"])
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() {
		unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("context writer did not release test storage")
		}
	}()
	recorder := newSessionContextUsageRecorder(app, id, 1, SessionRunnerChatOptions{})
	go func() {
		defer close(done)
		recorder.begin("model", agentruntime.ModelRequest{Tools: []agentruntime.ToolSchema{{
			Name: "test", Parameters: map[string]any{"properties": contextUsageBlockingSchema{started, release}},
		}}})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach estimation")
	}
	deleted := p3JSONRequest(t, app, http.MethodDelete, "/api/conversations/"+id, nil, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete HTTP %d", deleted.Code)
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pending context write did not settle")
	}
	if _, found, err := app.runtimeStore.Get(contextUsageNamespace(id), "latest"); err != nil || found {
		t.Fatalf("concurrent delete left an orphan context record: found=%v err=%v", found, err)
	}
}
