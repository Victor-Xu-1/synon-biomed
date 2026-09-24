package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	"synon-go/internal/persistence/runtimekv"
	workspace "synon-go/internal/persistence/workspace"
)

func readContextUsageTest(t *testing.T, store *runtimekv.Store, sessionID string) runnerContextUsage {
	t.Helper()
	entry, found, err := store.Get(contextUsageNamespace(sessionID), "latest")
	if err != nil || !found {
		t.Fatalf("context record found=%v err=%v", found, err)
	}
	snapshot, err := decodeRunnerContextUsage(entry)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestContextUsageEstimatesActualRequestWithoutFixedShares(t *testing.T) {
	request := agentruntime.ModelRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: "abcd"},
			{Role: "user", Content: "分子模拟", ToolCalls: []agentruntime.ToolCall{{ID: "call", Name: "read", Arguments: json.RawMessage("{}")}}},
			{Role: "tool", Parts: []agentruntime.ContentPart{{Type: agentruntime.ContentPartText, Text: "result"}, {Type: agentruntime.ContentPartImage}}},
			{Role: "system", Content: "selected methodology", ContextUsageSource: agentruntime.ContextUsageSkills},
			{Role: "system", Content: "admitted connector list", ContextUsageSource: agentruntime.ContextUsageMCP},
			{Role: "system", Content: "prior work summary", ContextUsageSource: agentruntime.ContextUsageMessages},
		},
		Tools: []agentruntime.ToolSchema{
			{Name: "read", Parameters: map[string]any{"type": "object"}},
			{Name: "connector", Parameters: map[string]any{"type": "object"}, Capabilities: []string{"mcp"}},
		},
	}
	rows, media, err := estimateRunnerRequestUsage(request)
	if err != nil || !media || len(rows) != 5 {
		t.Fatalf("rows=%+v media=%v err=%v", rows, media, err)
	}
	for index, key := range contextUsageCategoryKeys {
		if rows[index].Key != key || rows[index].Tokens <= 0 {
			t.Fatalf("missing category %s in %+v", key, rows)
		}
	}
	if rows[0].Tokens != 7 || rows[2].Tokens <= 19 {
		t.Fatalf("actual request estimates = %+v", rows)
	}
	request.Tools[0].OutputSchema = map[string]any{"notSent": strings.Repeat("x", 1000)}
	request.Tools[0].Capabilities = []string{strings.Repeat("not-sent", 100)}
	unchanged, _, _ := estimateRunnerRequestUsage(request)
	if unchanged[1].Tokens != rows[1].Tokens {
		t.Fatal("runtime-only metadata inflated the provider tool estimate")
	}
	request.Tools[0].Parameters["invalid"] = func() {}
	if _, _, err := estimateRunnerRequestUsage(request); err == nil {
		t.Fatal("unencodable schema accepted")
	}
}

type contextUsageRoundModel struct {
	requests []agentruntime.ModelRequest
}

func (model *contextUsageRoundModel) Complete(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	model.requests = append(model.requests, request)
	if len(model.requests) == 1 {
		return agentruntime.ModelResponse{Message: agentruntime.Message{
			Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "one", Name: "inspect", Arguments: json.RawMessage(`{}`)}},
		}}, nil
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "done"}}, nil
}

func TestContextUsageProvenanceSurvivesToolRoundAndReplayRebuild(t *testing.T) {
	messages := []chatCompletionMessage{{Role: "system", Content: "base policy"}, {Role: "user", Content: "inspect records"}}
	messages = appendRuntimeSkillCandidateContextMessage(messages, "available methodology")
	messages = appendRuntimeAgentPolicyContextMessageWithSource(messages, "skill execution order", agentruntime.ContextUsageSkills)
	messages = appendRuntimeSkillContextMessage(messages, "loaded method")
	messages = appendRuntimeTerminalPolicyContextMessageWithSource(messages, "skill decision rule", agentruntime.ContextUsageSkills)
	messages = appendRuntimeMCPContextMessage(messages, "admitted connector")
	model := &contextUsageRoundModel{}
	engine := agentruntime.Engine{Model: model, Tools: agentruntime.FuncToolGateway(func(_ context.Context, _ agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		return agentruntime.ToolResult{Value: map[string]any{"ok": true}}, nil
	})}
	_, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages: agentRuntimeMessagesFromChat(messages),
		Tools: []agentruntime.ToolSchema{
			{Name: "inspect", Parameters: map[string]any{"type": "object"}},
			{Name: "connector", Parameters: map[string]any{"type": "object"}, Capabilities: []string{"mcp"}},
		},
		MaxToolRounds: 2,
	})
	if err != nil || len(model.requests) != 2 {
		t.Fatalf("model rounds=%d err=%v", len(model.requests), err)
	}
	first, _, err := estimateRunnerRequestUsage(model.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := estimateRunnerRequestUsage(model.requests[1])
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Tokens <= 0 || first[1].Tokens <= 0 || first[2].Tokens <= 0 || first[3].Tokens <= 0 || first[4].Tokens <= 0 ||
		second[2].Tokens <= first[2].Tokens || second[0].Tokens != first[0].Tokens || second[1].Tokens != first[1].Tokens ||
		second[3].Tokens != first[3].Tokens || second[4].Tokens != first[4].Tokens {
		t.Fatalf("tool round changed fixed provenance: first=%+v second=%+v", first, second)
	}
	for _, message := range model.requests[1].Messages {
		if message.Role == "tool" && message.ContextUsageSource != "" {
			t.Fatalf("tool result inherited system provenance: %+v", message)
		}
	}
	if encoded, err := json.Marshal(messages); err != nil || bytes.Contains(encoded, []byte("contextUsageSource")) || bytes.Contains(encoded, []byte(`"skills"`)) {
		t.Fatalf("prompt provenance leaked into durable/wire JSON: err=%v value=%s", err, encoded)
	}
	// A restarted run rebuilds these transient labels from trusted prompt
	// constructors; it does not need to store prompt text in usage telemetry.
	replayed, err := sessionEntriesToProviderMessages("base policy", []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "initial task"}},
		{EventID: 2, Message: eventjournal.Message{"type": "session_compact", "summary": "prior work"}},
		{EventID: 3, Message: eventjournal.Message{"type": "message", "role": "user", "text": "continue"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) < 2 || replayed[1].ContextUsageSource != agentruntime.ContextUsageMessages {
		t.Fatalf("compacted conversation replay lost attribution: %+v", replayed)
	}
	replayed = appendRuntimeSkillContextMessage(replayed, "restored selected method")
	replayed = appendRuntimeMCPContextMessage(replayed, "restored admitted connector")
	rebuilt, _, err := estimateRunnerRequestUsage(agentruntime.ModelRequest{Messages: agentRuntimeMessagesFromChat(replayed)})
	if err != nil || rebuilt[2].Tokens <= 0 || rebuilt[3].Tokens <= 0 || rebuilt[4].Tokens <= 0 {
		t.Fatalf("replay rebuild attribution=%+v err=%v", rebuilt, err)
	}
}

func TestContextUsagePersistsLatestRequestAndFencesLateCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	store := runtimekv.New(path)
	recorder := &sessionContextUsageRecorder{store: store, sessionID: "one", attempt: 1, limit: 1000, limitSource: "configured"}
	first := recorder.begin("model-one", agentruntime.ModelRequest{})
	if first == nil || first.UsedTokens != 0 {
		t.Fatal("zero usage must be a valid record")
	}
	second := recorder.begin("model-two", agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "latest"}}})
	recorder.finish(second, agentruntime.ModelResponse{Usage: agentruntime.ModelUsage{
		InputTokens: 12, OutputTokens: 3, CacheReadTokens: 5, TotalTokens: 20,
	}}, nil)
	recorder.finish(first, agentruntime.ModelResponse{Usage: agentruntime.ModelUsage{InputTokens: 900, OutputTokens: 1}}, nil)
	got := readContextUsageTest(t, store, "one")
	if got.Model != "model-two" || got.UsedTokens != 20 || got.OutputTokens != 3 || got.Source != "provider" {
		t.Fatalf("latest usage overwritten or cache miscounted: %+v", got)
	}
	newer := *recorder
	newer.attempt = 2
	newer.begin("new-attempt", agentruntime.ModelRequest{})
	recorder.begin("stale-attempt", agentruntime.ModelRequest{})
	if got := readContextUsageTest(t, store, "one"); got.Model != "new-attempt" {
		t.Fatalf("old attempt replaced current: %+v", got)
	}
	// Request order comes from the SQLite write, not the wall clock. A
	// corrected clock must not leave yesterday's request on screen.
	clockSkew := readContextUsageTest(t, store, "one")
	clockSkew.ObservedAt = clockSkew.ObservedAt.Add(24 * time.Hour)
	if _, err := store.Set(contextUsageNamespace("one"), "latest", clockSkew); err != nil {
		t.Fatal(err)
	}
	newer.begin("after-clock-correction", agentruntime.ModelRequest{})
	if got := readContextUsageTest(t, store, "one"); got.Model != "after-clock-correction" {
		t.Fatalf("wall-clock skew blocked current request: %+v", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := runtimekv.New(path)
	t.Cleanup(func() { _ = reopened.Close() })
	if got := readContextUsageTest(t, reopened, "one"); got.Model != "after-clock-correction" || got.UsedTokens != 0 {
		t.Fatalf("restart lost last request: %+v", got)
	}
	newer.store = reopened
	pending := newer.begin("cancelled", agentruntime.ModelRequest{})
	newer.finish(pending, agentruntime.ModelResponse{}, context.Canceled)
	if got := readContextUsageTest(t, reopened, "one"); got.State != "failed" || got.Source != "estimated" {
		t.Fatalf("failed provider call claimed measured usage: %+v", got)
	}
	if _, err := reopened.DeleteScope(context.Background(), runtimekv.Scope{FrameID: "one"}); err != nil {
		t.Fatal(err)
	}
	newer.finish(pending, agentruntime.ModelResponse{}, nil)
	if _, found, err := reopened.Get(contextUsageNamespace("one"), "latest"); err != nil || found {
		t.Fatalf("late result recreated deleted record: found=%v err=%v", found, err)
	}
}

func TestContextUsageMissingProviderCountersRemainEstimated(t *testing.T) {
	store := runtimekv.New(filepath.Join(t.TempDir(), "runtime.sqlite"))
	t.Cleanup(func() { _ = store.Close() })
	recorder := &sessionContextUsageRecorder{store: store, sessionID: "estimate", limit: 100, limitSource: "runner_default"}
	current := recorder.begin("model", agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "hello"}}})
	input := current.UsedTokens
	recorder.finish(current, agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "hello"}}, nil)
	got := readContextUsageTest(t, store, "estimate")
	if got.Source != "estimated" || got.UsedTokens <= input || got.OutputTokens <= 0 {
		t.Fatalf("missing provider usage falsely measured: %+v", got)
	}
}

func TestContextUsagePresentationCallDoesNotReplaceMainRequest(t *testing.T) {
	store := runtimekv.New(filepath.Join(t.TempDir(), "runtime.sqlite"))
	t.Cleanup(func() { _ = store.Close() })
	client := &sessionRunnerDynamicModelClient{
		initial:      sessionRunnerResolvedModelClient{client: &preResolvedDynamicModelClient{}, model: "model"},
		initialReady: true,
		contextUsage: &sessionContextUsageRecorder{store: store, sessionID: "agent-session", limit: 100, limitSource: "configured"},
	}
	request := agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "main request"}}}
	if _, err := client.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	main := readContextUsageTest(t, store, "agent-session")
	client.initialReady = true // Reuse the lightweight fixture for the second call.
	// The language wrapper invokes the same delegate for a short translation.
	// That audit belongs to the provider, while the composer remains scoped to
	// the actual agent request.
	if _, err := client.Complete(withAuxiliaryContextUsage(context.Background()), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "presentation translation"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := readContextUsageTest(t, store, "agent-session"); got.RequestID != main.RequestID || got.UsedTokens != main.UsedTokens {
		t.Fatalf("auxiliary call replaced main request: before=%+v after=%+v", main, got)
	}
}

func TestContextUsageDynamicClientUsesRealProviderProtocol(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			calls := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, readErr := io.ReadAll(r.Body)
				if readErr != nil || bytes.Contains(raw, []byte("contextUsageSource")) || bytes.Contains(raw, []byte(`"skills"`)) {
					t.Errorf("request leaked usage provenance: err=%v body=%s", readErr, raw)
				}
				var input struct {
					Messages []agentruntime.Message `json:"messages"`
				}
				if err := json.Unmarshal(raw, &input); err != nil || len(input.Messages) != 1 {
					t.Errorf("provider input=%+v err=%v", input, err)
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"id\":\"one\",\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":3,\"total_tokens\":20}}\n\ndata: [DONE]\n\n"))
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answer"}}],"usage":{"prompt_tokens":17,"completion_tokens":3,"total_tokens":20}}`))
				}
			}))
			defer provider.Close()
			enabled := true
			srv, _, project, frame := newDynamicModelTestRuntime(t, "observed-model", []workspace.ModelProviderInput{{
				ID: "observed", UserID: "dynamic-user", Name: "Observed", Type: "openai-compatible",
				BaseURL: provider.URL + "/v1", Model: "observed-model", Enabled: &enabled,
			}})
			client := newDynamicModelTestClient(srv, project, frame)
			client.contextUsage = newSessionContextUsageRecorder(srv, frame, 1, SessionRunnerChatOptions{RuntimeSessionConfig: map[string]any{"contextWindow": 500}})
			request := agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "observe this request", ContextUsageSource: agentruntime.ContextUsageSkills}}}
			var err error
			if stream {
				_, err = client.CompleteStream(context.Background(), request, nil)
			} else {
				_, err = client.Complete(context.Background(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := readContextUsageTest(t, srv.runtimeStore, frame)
			if got.Source != "provider" || got.UsedTokens != 20 || got.Model != "observed-model" || got.LimitTokens != 500 || got.State != "complete" || calls != 1 {
				t.Fatalf("provider-to-durable usage=%+v calls=%d", got, calls)
			}
			api := p3JSONRequest(t, srv, http.MethodGet, "/api/conversations/"+frame+"/context-usage", nil, "dynamic-user")
			if api.Code != http.StatusOK || p3DecodeObject(t, api)["status"] != "available" {
				t.Fatalf("provider-to-API projection HTTP=%d body=%s", api.Code, api.Body.String())
			}
			// Another request replaces the snapshot; it must not accumulate 40.
			if stream {
				_, err = client.CompleteStream(context.Background(), request, nil)
			} else {
				_, err = client.Complete(context.Background(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := readContextUsageTest(t, srv.runtimeStore, frame); got.UsedTokens != 20 {
				t.Fatalf("context use accumulated requests: %+v", got)
			}
		})
	}
}

func TestContextUsageInvalidRecordRejected(t *testing.T) {
	if _, err := decodeRunnerContextUsage(runtimekv.Entry{Value: map[string]any{"usedTokens": -1}}); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	legacy := runnerContextUsage{
		SessionID: "one", RequestID: "old", Attempt: 1, Model: "model", ObservedAt: time.Now().UTC(),
		State: "complete", Source: "provider", UsedTokens: 20, OutputTokens: 2,
		LimitTokens: 100, LimitSource: "configured", InputEstimates: []runnerContextUsageRow{
			{Key: "systemPrompt", Tokens: 3}, {Key: "messages", Tokens: 8}, {Key: "toolDefinitions", Tokens: 7},
		},
	}
	if _, err := decodeRunnerContextUsage(runtimekv.Entry{Value: legacy}); !errors.Is(err, errLegacyRunnerContextUsage) {
		t.Fatalf("valid old snapshot must be marked legacy: %v", err)
	}
	legacy.State = "request"
	if _, err := decodeRunnerContextUsage(runtimekv.Entry{Value: legacy}); errors.Is(err, errLegacyRunnerContextUsage) || err == nil {
		t.Fatalf("provider-sourced pending request treated as legacy: %v", err)
	}
	legacy.State, legacy.Source, legacy.OutputTokens = "failed", "estimated", 1
	if _, err := decodeRunnerContextUsage(runtimekv.Entry{Value: legacy}); errors.Is(err, errLegacyRunnerContextUsage) || err == nil {
		t.Fatalf("failed request with output treated as legacy: %v", err)
	}
	legacy.State, legacy.OutputTokens = "complete", 21
	if _, err := decodeRunnerContextUsage(runtimekv.Entry{Value: legacy}); errors.Is(err, errLegacyRunnerContextUsage) || err == nil {
		t.Fatalf("output exceeded total but was treated as legacy: %v", err)
	}
	legacy.OutputTokens = 0
	missingMedia := map[string]any{}
	raw, err := json.Marshal(legacy)
	if err != nil || json.Unmarshal(raw, &missingMedia) != nil {
		t.Fatal("encode complete legacy fixture", err)
	}
	delete(missingMedia, "hasMedia")
	if _, err := decodeRunnerContextUsage(runtimekv.Entry{Value: missingMedia}); errors.Is(err, errLegacyRunnerContextUsage) || err == nil {
		t.Fatalf("incomplete old projection treated as legacy: %v", err)
	}
	legacy.Attempt = -1
	if _, err := decodeRunnerContextUsage(runtimekv.Entry{Value: legacy}); errors.Is(err, errLegacyRunnerContextUsage) || err == nil {
		t.Fatalf("negative attempt treated as valid legacy projection: %v", err)
	}
}
