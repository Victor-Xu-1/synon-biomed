package server

import (
	"context"
	"encoding/json"
	"errors"
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

func TestWebContextUsageLegacyRecordIsUnavailableUntilNextRequest(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "context-legacy", "local")
	created := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Legacy context usage", "assistant": map[string]any{"id": "synonbiomed:OPERON"},
		"extra": map[string]any{"project_id": project.ID},
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create HTTP %d", created.Code)
	}
	id := webString(p3DecodeObject(t, created)["id"])
	legacy := runnerContextUsage{
		SessionID: id, RequestID: "old-request", Attempt: 1, Model: "old-model",
		ObservedAt: time.Now().UTC(), State: "complete", Source: "provider",
		UsedTokens: 31, LimitTokens: 100, LimitSource: "configured", OutputTokens: 4,
		InputEstimates: []runnerContextUsageRow{
			{Key: "systemPrompt", Tokens: 10}, {Key: "messages", Tokens: 10}, {Key: "toolDefinitions", Tokens: 7},
		},
	}
	if _, err := app.runtimeStore.Set(contextUsageNamespace(id), "latest", legacy); err != nil {
		t.Fatal(err)
	}
	before := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+id+"/context-usage", nil, "")
	if before.Code != http.StatusOK || p3DecodeObject(t, before)["status"] != "unavailable" {
		t.Fatalf("legacy three-category record fabricated five buckets: HTTP %d body=%s", before.Code, before.Body.String())
	}
	recorder := newSessionContextUsageRecorder(app, id, 1, SessionRunnerChatOptions{})
	if recorder.begin("new-model", agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "fresh"}}}) == nil {
		t.Fatal("new request could not replace legacy projection")
	}
	after := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+id+"/context-usage", nil, "")
	snapshot, _ := p3DecodeObject(t, after)["snapshot"].(map[string]any)
	rows, _ := snapshot["inputEstimates"].([]any)
	if after.Code != http.StatusOK || len(rows) != 5 || snapshot["model"] != "new-model" {
		t.Fatalf("fresh request failed to replace legacy projection: HTTP %d body=%s", after.Code, after.Body.String())
	}
	legacy.InputEstimates[2].Key = "incorrect"
	if _, err := app.runtimeStore.Set(contextUsageNamespace(id), "latest", legacy); err != nil {
		t.Fatal(err)
	}
	malformed := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+id+"/context-usage", nil, "")
	if malformed.Code != http.StatusServiceUnavailable {
		t.Fatalf("malformed legacy projection accepted: HTTP %d", malformed.Code)
	}
}

func TestWebContextUsageRejectsIncompleteCategoryRows(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "context-invalid-rows", "local")
	created := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Invalid context usage", "assistant": map[string]any{"id": "synonbiomed:OPERON"},
		"extra": map[string]any{"project_id": project.ID},
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create HTTP %d", created.Code)
	}
	id := webString(p3DecodeObject(t, created)["id"])
	defects := []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing-tokens", func(row map[string]any) { delete(row, "tokens") }},
		{"null-tokens", func(row map[string]any) { row["tokens"] = nil }},
		{"missing-key", func(row map[string]any) { delete(row, "key") }},
		{"null-key", func(row map[string]any) { row["key"] = nil }},
	}
	for _, legacy := range []bool{false, true} {
		for _, defect := range defects {
			version := "current"
			if legacy {
				version = "legacy"
			}
			t.Run(version+"/"+defect.name, func(t *testing.T) {
				rows := []runnerContextUsageRow{{Key: "systemPrompt", Tokens: 10}, {Key: "tools", Tokens: 5}, {Key: "messages", Tokens: 5}, {Key: "mcp", Tokens: 0}, {Key: "skills", Tokens: 0}}
				if legacy {
					rows = []runnerContextUsageRow{{Key: "systemPrompt", Tokens: 10}, {Key: "messages", Tokens: 5}, {Key: "toolDefinitions", Tokens: 5}}
				}
				valid := runnerContextUsage{
					SessionID: id, RequestID: "request", Attempt: 1, Model: "model", ObservedAt: time.Now().UTC(),
					State: "complete", Source: "provider", UsedTokens: 20, OutputTokens: 0,
					LimitTokens: 100, LimitSource: "configured", InputEstimates: rows,
				}
				raw, err := json.Marshal(valid)
				if err != nil {
					t.Fatal(err)
				}
				var candidate map[string]any
				if err := json.Unmarshal(raw, &candidate); err != nil {
					t.Fatal(err)
				}
				defect.change(candidate["inputEstimates"].([]any)[0].(map[string]any))
				if _, err := decodeRunnerContextUsage(runtimekv.Entry{Value: candidate}); err == nil || errors.Is(err, errLegacyRunnerContextUsage) {
					t.Fatalf("incomplete row was accepted: %v", err)
				}
				if _, err := app.runtimeStore.Set(contextUsageNamespace(id), "latest", candidate); err != nil {
					t.Fatal(err)
				}
				response := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+id+"/context-usage", nil, "")
				if response.Code != http.StatusServiceUnavailable {
					t.Fatalf("incomplete row became available/unavailable: HTTP %d body=%s", response.Code, response.Body.String())
				}
			})
		}
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
