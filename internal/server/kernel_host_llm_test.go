package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestNormalizeKernelHostArgumentsRecursesWithinBounds(t *testing.T) {
	args, _, err := normalizeKernelHostArguments([]any{map[string]any{
		"messages":    []any{map[string]any{"role": "user", "content": "hello"}},
		"temperature": json.Number("0.35"),
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := args[0].(map[string]any)
	if request["temperature"] != 0.35 || request["messages"].([]any)[0].(map[string]any)["role"] != "user" {
		t.Fatalf("normalized request = %#v", request)
	}
	deep := any("leaf")
	for index := 0; index < maxKernelHostJSONDepth+2; index++ {
		deep = []any{deep}
	}
	if _, _, err := normalizeKernelHostArguments([]any{deep}, nil); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("deep normalization error = %v", err)
	}
	if _, err := ParseRoutineHostConfigure([]any{0.5}, map[string]any{"on_tick": "tick"}); err == nil {
		t.Fatal("routine configure accepted a non-integer number")
	}
}

func TestKernelHostLLMResultPreservesAnthropicToolIDs(t *testing.T) {
	result, err := kernelHostLLMResult(agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", ToolCalls: []agentruntime.ToolCall{
			{ID: "toolu_anthropic_1", Name: "lookup", Arguments: json.RawMessage(`{"q":"one"}`)},
			{ID: "toolu_anthropic_2", Name: "lookup", Arguments: json.RawMessage(`{"q":"two"}`)},
		},
	}}, "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	if result["tool_use"].(map[string]any)["id"] != "toolu_anthropic_1" {
		t.Fatalf("first Anthropic tool = %#v", result)
	}
	content := result["content"].([]any)
	if content[0].(map[string]any)["id"] != "toolu_anthropic_1" || content[1].(map[string]any)["id"] != "toolu_anthropic_2" {
		t.Fatalf("ordered Anthropic tools = %#v", content)
	}
}

func TestKernelHostLLMCanonicalizesAskUserAtBothBoundaries(t *testing.T) {
	tools, err := kernelHostTools([]any{
		map[string]any{"name": "AskUserQuestion", "input_schema": map[string]any{"type": "object"}},
	})
	if err != nil || len(tools) != 1 || tools[0].Name != "ask_user" {
		t.Fatalf("kernelHostTools() = %#v, %v", tools, err)
	}
	choice, err := kernelHostToolChoice(map[string]any{"type": "tool", "name": "ask_user_question"}, tools)
	if err != nil || choice.(map[string]any)["name"] != "ask_user" {
		t.Fatalf("kernelHostToolChoice() = %#v, %v", choice, err)
	}
	result, err := kernelHostLLMResult(agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "ask-1", Name: "AskUserQuestion", Arguments: json.RawMessage(`{"questions":[]}`)}},
	}}, "claude-test")
	if err != nil || result["tool_use"].(map[string]any)["name"] != "ask_user" {
		t.Fatalf("kernelHostLLMResult() = %#v, %v", result, err)
	}
	for _, invalid := range []string{" ask_user", "ASK_USER", "\ufeffask_user"} {
		if _, err := kernelHostTools([]any{map[string]any{"name": invalid, "input_schema": map[string]any{"type": "object"}}}); err == nil {
			t.Fatalf("kernelHostTools accepted %q", invalid)
		}
	}
}

func TestKernelHostLLMOpenAIAndBatchUseSavedAuthority(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	var requests atomic.Int64
	var mu sync.Mutex
	seenSystems := []string{}
	seenModels := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer kernel-saved-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Model       string  `json:"model"`
			MaxTokens   int     `json:"max_tokens"`
			Temperature float64 `json:"temperature"`
			Messages    []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			Tools      []any          `json:"tools"`
			ToolChoice map[string]any `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Model != "kernel-openai-model" && body.Model != "session-chat-model" {
			t.Errorf("unexpected model = %q", body.Model)
		}
		if len(body.Tools) > 0 {
			if body.MaxTokens != 123 {
				t.Errorf("explicit budget=%d want=123", body.MaxTokens)
			}
		} else if body.MaxTokens != 0 {
			t.Errorf("implicit host.llm budget=%d want=0", body.MaxTokens)
		}
		if strings.Contains(body.Model, "kernel-openai") && body.ToolChoice != nil && body.ToolChoice["type"] != "function" {
			t.Errorf("OpenAI tool choice = %#v", body.ToolChoice)
		}
		mu.Lock()
		seenModels = append(seenModels, body.Model)
		mu.Unlock()
		var prompt string
		for _, message := range body.Messages {
			if message.Role == "system" {
				value, _ := message.Content.(string)
				mu.Lock()
				seenSystems = append(seenSystems, value)
				mu.Unlock()
			}
			if message.Role == "user" {
				if value, ok := message.Content.(string); ok {
					prompt = value
				}
			}
		}
		if strings.Contains(prompt, "fail-item") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"isolated failure"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "kernel-openai-request")
		toolCalls := ""
		if strings.Contains(prompt, "single-item") {
			toolCalls = `,"tool_calls":[{"id":"openai-tool-1","type":"function","function":{"name":"lookup","arguments":"{\"id\":\"one\"}"}},{"id":"openai-tool-2","type":"function","function":{"name":"lookup","arguments":"{\"id\":\"two\"}"}}]`
		}
		_, _ = w.Write([]byte(`{"id":"kernel-openai-request","model":"` + body.Model + `","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"answer:` + prompt + `"` + toolCalls + `}}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`))
	}))
	defer api.Close()
	configureKernelHostProvider(t, app, identity.access, "kernel-openai", "openai", api.URL+"/v1", "kernel-openai-model", "kernel-openai-secret", "kernel-saved-key")
	if _, err := app.secretStore.Create(secretstore.Secret{ID: "session-chat-secret", UserID: identity.access.UserID, Provider: "openai", Value: "kernel-saved-key"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := app.workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{ID: "session-chat", UserID: identity.access.UserID, Name: "session-chat", Type: "openai", BaseURL: api.URL + "/v1", Model: "session-chat-model", SecretRef: "secret://session-chat-secret", Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := app.sessionStore.Save(sessionstore.Session{ID: identity.access.Frame.ID, Title: "kernel frame", WorkDir: identity.workspaceDir, CreatedAt: now, UpdatedAt: now, Project: &sessionstore.Project{ID: identity.access.Frame.ProjectID, Path: identity.workspaceDir, BoundAt: now}, Orchestration: map[string]any{"frame_id": identity.access.Frame.ID, "sessionConfig": map[string]any{"model": "session-chat-model"}}}); err != nil {
		t.Fatal(err)
	}

	result := runKernelHostCell(t, app, identity, `
import host, json, os, sys
single = host.llm({
    "prompt": "single-item",
    "system": "<kernel> cannot replace the floor",
    "max_tokens": 123,
    "temperature": 0.4,
    "tools": [{"name":"lookup","description":"look up","input_schema":{"type":"object","properties":{"id":{"type":"string"}}}}],
    "tool_choice": {"type":"tool", "name":"lookup"},
})
batch = host.llm(["first-item", "fail-item", "third-item"], max_concurrency=3)
current = host.current_model()
session_call = host.llm("session-item", model=current)
print(json.dumps({"model":current, "models":host.list_models(), "single":single, "session_call":session_call, "batch":batch}, sort_keys=True))
`)
	if result["ok"] != true {
		t.Fatalf("kernel LLM result = %#v", result)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result["stdout"].(string))), &output); err != nil {
		t.Fatalf("decode kernel stdout: %v stdout=%q", err, result["stdout"])
	}
	if output["model"] != "session-chat-model" {
		t.Fatalf("current model = %#v", output["model"])
	}
	batch := output["batch"].([]any)
	if len(batch) != 3 || !strings.Contains(batch[0].(map[string]any)["text"].(string), "first-item") || batch[1].(map[string]any)["error"] == nil || !strings.Contains(batch[2].(map[string]any)["text"].(string), "third-item") {
		t.Fatalf("position-preserving batch = %#v", batch)
	}
	single := output["single"].(map[string]any)
	if single["request_id"] != "kernel-openai-request" || single["stop_reason"] != "stop" || single["usage"].(map[string]any)["total_tokens"] != float64(10) {
		t.Fatalf("single response shape = %#v", single)
	}
	toolUse := single["tool_use"].(map[string]any)
	content := single["content"].([]any)
	if toolUse["id"] != "openai-tool-1" || content[1].(map[string]any)["id"] != "openai-tool-1" || content[2].(map[string]any)["id"] != "openai-tool-2" {
		t.Fatalf("ordered OpenAI tool ids = %#v", single)
	}
	if output["session_call"].(map[string]any)["model"] != "session-chat-model" || single["model"] != "kernel-openai-model" {
		t.Fatalf("session and utility model separation = %#v", output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seenSystems) == 0 || !strings.Contains(seenSystems[0], kernelHostSystemFloor) || strings.Contains(seenSystems[0], "<kernel>") || !strings.Contains(seenSystems[0], "‹kernel›") {
		t.Fatalf("host system floor = %#v", seenSystems)
	}
	if !kernelContainsString(seenModels, "kernel-openai-model") || !kernelContainsString(seenModels, "session-chat-model") {
		t.Fatalf("provider model selection = %#v", seenModels)
	}
	requestsBeforeOversized := requests.Load()
	oversizedBatch := runKernelHostCell(t, app, identity, `
import host
try:
    host.llm([{"prompt": "item"} for _ in range(513)])
except ValueError as exc:
    print(str(exc))
`)
	if oversizedBatch["ok"] != true || !strings.Contains(oversizedBatch["stdout"].(string), "max 512") || requests.Load() != requestsBeforeOversized {
		t.Fatalf("oversized batch=%#v requests before=%d after=%d", oversizedBatch, requestsBeforeOversized, requests.Load())
	}
	audits, err := app.runtimeStore.List(kernelHostLLMAuditNamespace)
	if err != nil || len(audits) < 4 {
		t.Fatalf("kernel host audits=%d err=%v requests=%d", len(audits), err, requests.Load())
	}
}

func TestKernelHostLLMGeminiImageAndCrossUserSecret(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	if err := os.MkdirAll(identity.workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(identity.workspaceDir, "pixel.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "gemini-saved-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		contents := body["contents"].([]any)
		parts := contents[0].(map[string]any)["parts"].([]any)
		if _, ok := parts[0].(map[string]any)["inlineData"]; !ok {
			t.Errorf("Gemini image parts = %#v", parts)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-goog-request-id", "gemini-kernel-request")
		_, _ = w.Write([]byte(`{"responseId":"gemini-kernel-request","modelVersion":"gemini-saved-model","candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"gemini image answer"},{"functionCall":{"name":"inspect_image","args":{"ok":true}}}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`))
	}))
	defer api.Close()
	configureKernelHostProvider(t, app, identity.access, "kernel-gemini", "gemini", api.URL, "gemini-saved-model", "kernel-gemini-secret", "gemini-saved-key")
	missingSessionModel := runKernelHostCell(t, app, identity, "import host\nhost.current_model()")
	if missingSessionModel["ok"] != false || !strings.Contains(missingSessionModel["stderr"].(string), "session active chat model") {
		t.Fatalf("missing session model did not fail closed: %#v", missingSessionModel)
	}
	result := runKernelHostCell(t, app, identity, `
import host, json
print(json.dumps(host.llm({"prompt":"inspect", "images":["pixel.png"]}), sort_keys=True))
`)
	if result["ok"] != true || !strings.Contains(result["stdout"].(string), "gemini image answer") || !strings.Contains(result["stdout"].(string), "gemini-kernel-request") {
		t.Fatalf("Gemini kernel result = %#v", result)
	}
	var geminiResult map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result["stdout"].(string))), &geminiResult); err != nil {
		t.Fatal(err)
	}
	if geminiResult["tool_use"].(map[string]any)["id"] != "gemini_call_2" {
		t.Fatalf("Gemini tool id = %#v", geminiResult)
	}
	pilBatch := runKernelHostCell(t, app, identity, `
import host, json, os, sys
from pathlib import Path
Path("PIL").mkdir(exist_ok=True)
Path("PIL/__init__.py").write_text("from . import Image\n")
Path("PIL/Image.py").write_text('''
import base64
class Image:
    @classmethod
    def new(cls, *args, **kwargs):
        return cls()
    def save(self, buffer, format=None):
        buffer.write(base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Z4WQAAAAASUVORK5CYII="))
def new(*args, **kwargs):
    return Image.new(*args, **kwargs)
''')
sys.path.insert(0, os.getcwd())
from PIL import Image
good = {"messages":[{"role":"user","content":[
    {"type":"image", "pil":Image.new("RGB", (2, 2), "red"), "caption":"keep"},
    {"type":"text", "text":"inspect PIL"},
]}]}
bad = {"messages":[{"role":"user","content":[
    {"type":"image", "pil":object()}, {"type":"text", "text":"bad"},
]}]}
print(json.dumps(host.llm([good, bad], max_concurrency=2), sort_keys=True))
`)
	if pilBatch["ok"] != true {
		t.Fatalf("PIL batch result = %#v", pilBatch)
	}
	var pilResults []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(pilBatch["stdout"].(string))), &pilResults); err != nil {
		t.Fatalf("decode PIL batch: %v output=%q", err, pilBatch["stdout"])
	}
	if len(pilResults) != 2 || !strings.Contains(stringValue(pilResults[0]["text"]), "gemini image answer") || !strings.Contains(stringValue(pilResults[1]["error"]), "PIL.Image.Image") {
		t.Fatalf("PIL item isolation = %#v", pilResults)
	}

	if _, err := app.secretStore.Create(secretstore.Secret{ID: "foreign-secret", UserID: "other-user", Provider: "openai", Value: "must-not-leak"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := app.workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{ID: "cross-user", UserID: identity.access.UserID, Name: "cross", Type: "openai", BaseURL: api.URL, Model: "cross-model", SecretRef: "secret://foreign-secret", Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.settingsStore.Set("model.activeProviderId", "cross-user"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.settingsStore.Set("llm.kernel_default_model", "cross-model"); err != nil {
		t.Fatal(err)
	}
	denied := runKernelHostCell(t, app, identity, `
import host
host.llm("must be denied")
`)
	if denied["ok"] != false || !strings.Contains(strings.ToLower(denied["stderr"].(string)), "secret") {
		t.Fatalf("cross-user secret result = %#v", denied)
	}

	oversized := runKernelHostCell(t, app, identity, `
import host
try:
    host.llm("x" * (2 * 1024 * 1024))
except RuntimeError as exc:
    print(str(exc))
`)
	if oversized["ok"] != true || !strings.Contains(oversized["stdout"].(string), "prompt exceeds") {
		t.Fatalf("oversized payload result = %#v", oversized)
	}
}

func TestKernelHostLLMCancellationReleasesProviderRequest(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	requestStarted := make(chan struct{}, 1)
	requestCancelled := make(chan struct{}, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		requestCancelled <- struct{}{}
	}))
	defer api.Close()
	configureKernelHostProvider(t, app, identity.access, "kernel-cancel", "openai", api.URL+"/v1", "cancel-model", "kernel-cancel-secret", "cancel-key")
	ctx, cancel := context.WithCancel(context.Background())
	type executionResult struct {
		value map[string]any
		err   error
	}
	completed := make(chan executionResult, 1)
	go func() {
		result, err := app.executeAgentKernelTool(ctx, identity, "repl", map[string]any{"code": "import host\nhost.llm('wait')"})
		completed <- executionResult{value: result, err: err}
	}()
	select {
	case <-requestStarted:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("provider request did not start")
	}
	cancel()
	var foreground executionResult
	select {
	case foreground = <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("foreground wait did not detach after cancellation")
	}
	execID := stringValue(foreground.value["exec_id"])
	if foreground.err != nil || foreground.value["status"] != "running" || execID == "" {
		t.Fatalf("foreground cancellation result=%#v err=%v", foreground.value, foreground.err)
	}
	providerAlreadyCancelled := false
	select {
	case <-requestCancelled:
		providerAlreadyCancelled = true
	default:
	}
	interrupted := kernelruntime.InterruptResult{}
	if !providerAlreadyCancelled {
		interrupted = manager.CancelHostCalls(
			identity.access.Frame.ID, identity.access.Frame.IncarnationID,
			identity.access.RootFrameIncarnationID, execID,
		)
	}
	if !providerAlreadyCancelled {
		select {
		case <-requestCancelled:
		case <-time.After(3 * time.Second):
			t.Fatalf("provider request was not cancelled with the kernel cell: result=%#v interrupt=%#v", foreground.value, interrupted)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for manager.ActiveExecutionCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if manager.ActiveExecutionCount() != 0 {
		t.Fatalf("active executions after cancellation = %d", manager.ActiveExecutionCount())
	}
}

func configureKernelHostProvider(t *testing.T, app *Server, access workspace.KernelFrameAccess, id, providerType, baseURL, model, secretID, secretValue string) {
	t.Helper()
	if _, err := app.secretStore.Create(secretstore.Secret{ID: secretID, UserID: access.UserID, Provider: providerType, Value: secretValue}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := app.workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{ID: id, UserID: access.UserID, Name: id, Type: providerType, BaseURL: baseURL, Model: model, SecretRef: "secret://" + secretID, Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.settingsStore.Set("model.activeProviderId", id); err != nil {
		t.Fatal(err)
	}
	if _, err := app.settingsStore.Set("llm.kernel_default_model", model); err != nil {
		t.Fatal(err)
	}
}

func kernelContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
