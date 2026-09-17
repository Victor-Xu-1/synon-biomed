package agentruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"synon-go/internal/tools/webfetch"
)

func TestEngineRunsOpenAICompatibleToolLoopThroughGateway(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("model request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Synon-Provider-Cache") != "1" {
			t.Fatalf("provider cache header = %q", r.Header.Get("X-Synon-Provider-Cache"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		if request["model"] != "runtime-model" {
			t.Fatalf("model = %#v", request["model"])
		}
		metadata, _ := request["metadata"].(map[string]any)
		cache, _ := metadata["synon_provider_cache"].(map[string]any)
		if cache["resumeCacheKeys"] == nil {
			t.Fatalf("provider cache metadata missing: %#v", request["metadata"])
		}
		switch sequence {
		case 1:
			tools, ok := request["tools"].([]any)
			if !ok || !hasOpenAIToolNamed(tools, "echo_upper") {
				t.Fatalf("first model request missing echo_upper tool: %#v", request["tools"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_echo_1",
							"type": "function",
							"function": {
								"name": "echo_upper",
								"arguments": "{\"text\":\"hello runtime\"}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages := request["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "call_echo_1" ||
				!strings.Contains(last["content"].(string), "HELLO RUNTIME") ||
				!strings.Contains(last["content"].(string), "runtime-state.v1") {
				t.Fatalf("second model request missing tool result: %#v", last)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"content": "final: HELLO RUNTIME"
					}
				}]
			}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	events := []Event{}
	engine := Engine{
		Model: OpenAIChatClient{
			Endpoint: modelAPI.URL + "/v1/chat/completions",
			APIKey:   "test-key",
			Model:    "runtime-model",
		},
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			if call.Name != "echo_upper" {
				t.Fatalf("tool name = %q", call.Name)
			}
			var input struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(call.Arguments, &input); err != nil {
				t.Fatalf("decode tool input: %v", err)
			}
			return ToolResult{
				Value:        map[string]any{"ok": true, "text": strings.ToUpper(input.Text)},
				ModelContext: map[string]any{"schema": "runtime-state.v1"},
			}, nil
		}),
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	}

	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "call the runtime tool"}},
		Tools: []ToolSchema{{
			Name:        "echo_upper",
			Description: "Uppercase text.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string"},
				},
				"required": []string{"text"},
			},
		}},
		MaxToolRounds: 2,
		Metadata: map[string]any{"synon_provider_cache": map[string]any{
			"resumeCacheKeys": []string{"tool:echo:call_echo_1"},
		}},
		Headers: map[string]string{"X-Synon-Provider-Cache": "1"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalMessage.Content != "final: HELLO RUNTIME" || requests.Load() != 2 {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}
	if !hasEvent(events, EventToolStarted, "echo_upper") || !hasEvent(events, EventToolCompleted, "echo_upper") || !hasEvent(events, EventFinal, "") {
		t.Fatalf("events = %#v", events)
	}
	for _, event := range events {
		if event.Type == EventToolCompleted && strings.Contains(event.Result, "runtime-state.v1") {
			t.Fatalf("transient model context entered durable evidence: %#v", event)
		}
	}
}

func TestEngineAppliesToolSchemaDefaultsBeforeExecution(t *testing.T) {
	var received ToolCall
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call-python-1", Name: "python", Arguments: json.RawMessage(`{"code":"print(1)"}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "analysis complete"}},
	}}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			received = call
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "run python"}},
		Tools: []ToolSchema{{
			Name: "python",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"code":        map[string]any{"type": "string"},
					"environment": map[string]any{"type": "string", "default": "synon-biomed-python"},
				},
				"required": []string{"code", "environment"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var input map[string]any
	if err := json.Unmarshal(received.Arguments, &input); err != nil {
		t.Fatalf("decode normalized arguments: %v", err)
	}
	if input["environment"] != "synon-biomed-python" || result.FinalMessage.Content != "analysis complete" {
		t.Fatalf("received=%#v result=%#v", received, result)
	}
}

func TestEngineReturnsUnadvertisedToolFailureToModelBeforeCorrection(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call-python", Name: "python", Arguments: json.RawMessage(`{"code":"print(1)"}`),
		}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call-fetch", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test"}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "complete"}},
	}}
	var executed []ToolCall
	var events []Event
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			executed = append(executed, call)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "run a scientific calculation"}},
		Tools:         []ToolSchema{{Name: "web_fetch", Parameters: map[string]any{"type": "object"}}},
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "complete" || len(model.requests) != 3 {
		t.Fatalf("result=%#v requests=%d", result, len(model.requests))
	}
	if len(executed) != 1 || executed[0].Name != "web_fetch" {
		t.Fatalf("executed=%#v", executed)
	}
	settled, started := false, false
	for _, event := range events {
		settled = settled || event.Type == EventToolCompleted && event.ToolCallID == "call-python" && event.RejectedBeforeExecution
		started = started || event.Type == EventToolStarted && event.ToolCallID == "call-python"
	}
	if !settled || started {
		t.Fatalf("unadvertised call lifecycle=%#v", events)
	}
	feedback := model.requests[1].Messages[len(model.requests[1].Messages)-1]
	if feedback.Role != "tool" || feedback.ToolCallID != "call-python" ||
		!strings.Contains(feedback.Content, `"code":"unadvertised_tool"`) {
		t.Fatalf("model-visible feedback=%#v", feedback)
	}
}

func TestEnginePrivatelyRepairsFlattenedMCPCallToCanonicalREPLRoute(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "direct-mcp", Name: "mcp__clinical-trials__get_trial_details",
			Arguments: json.RawMessage(`{"nct_id":"NCT00000000"}`),
		}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "repl-mcp", Name: "repl",
			Arguments: json.RawMessage(`{"code":"result = host.mcp('clinical-trials', 'get_trial_details', nct_id='NCT00000000')"}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "lookup completed"}},
	}}
	var executed []ToolCall
	var events []Event
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			executed = append(executed, call)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "look up the trial"}},
		Tools: []ToolSchema{{Name: "repl", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}},
		}}},
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "lookup completed" || len(executed) != 1 || executed[0].Name != "repl" {
		t.Fatalf("result=%#v executed=%#v", result, executed)
	}
	for _, event := range events {
		if event.ToolCallID == "direct-mcp" {
			t.Fatalf("private MCP route repair leaked a failed tool lifecycle: %#v", events)
		}
	}
	feedback := model.requests[1].Messages[len(model.requests[1].Messages)-1]
	if feedback.Role != "tool" || !strings.Contains(feedback.Content, "mcp_route_requires_repl") ||
		!strings.Contains(feedback.Content, "host.mcp") {
		t.Fatalf("private MCP route feedback=%#v", feedback)
	}
}

func TestEnginePrivatelyRepairsInvalidArgumentsBeforeCorrection(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "invalid", Name: "web_fetch",
			Arguments: json.RawMessage(`{"url":"https://example.test/a","prompt":"unsupported"}`),
		}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "corrected", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test/a"}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "corrected call completed"}},
	}}
	var executions atomic.Int64
	events := make([]Event, 0)
	engine := Engine{
		Model: model,
		Tools: admissionAwareGateway{executions: &executions},
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "fetch the page"}},
		Tools: []ToolSchema{{
			Name: "web_fetch", Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}},
				"required": []string{"url"}, "additionalProperties": false,
			},
		}},
		MaxToolRounds: 2,
	})
	if err != nil || result.FinalMessage.Content != "corrected call completed" || executions.Load() != 1 || len(model.requests) != 3 {
		t.Fatalf("result=%#v err=%v executions=%d requests=%d", result, err, executions.Load(), len(model.requests))
	}
	if messages := model.requests[1].Messages; len(messages) == 0 ||
		messages[len(messages)-1].Role != "tool" ||
		!strings.Contains(messages[len(messages)-1].Content, `"code":"invalid_tool_arguments"`) {
		t.Fatalf("model-visible argument failure was not supplied: %#v", model.requests[1].Messages)
	}
	failed, started := false, false
	for _, event := range events {
		failed = failed || event.Type == EventToolFailed && event.ToolCallID == "invalid"
		started = started || event.Type == EventToolStarted && event.ToolCallID == "invalid"
	}
	if failed || started {
		t.Fatalf("invalid pre-execution lifecycle=%#v", events)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("private invalid-argument attempt leaked into public messages=%#v", result.Messages)
	}
}

func TestEngineExecutesValidCallsBesideModelVisibleRejections(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "invalid", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test/a","prompt":"unsupported"}`)},
			{ID: "valid", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test/b"}`)},
		}}},
		{Message: Message{Role: "assistant", Content: "used the successful subset"}},
	}}
	var executions atomic.Int64
	engine := Engine{Model: model, Tools: admissionAwareGateway{executions: &executions}}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "fetch both"}},
		Tools:         []ToolSchema{{Name: "web_fetch", Parameters: map[string]any{"type": "object"}}},
		MaxToolRounds: 1,
	})
	if err != nil || result.FinalMessage.Content != "used the successful subset" || executions.Load() != 1 {
		t.Fatalf("result=%#v err=%v executions=%d", result, err, executions.Load())
	}
	messages := model.requests[1].Messages
	if len(messages) < 4 || messages[len(messages)-2].ToolCallID != "invalid" ||
		messages[len(messages)-1].ToolCallID != "valid" ||
		!strings.Contains(messages[len(messages)-2].Content, `"executed":false`) ||
		!strings.Contains(messages[len(messages)-1].Content, `"ok":true`) {
		t.Fatalf("mixed tool results=%#v", messages)
	}
}

func TestEnginePersistsSafeDiagnosticAndBoundsInvalidFailureFamily(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "bad-1", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test","prompt":"secret one"}`)}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "bad-2", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test","prompt":"secret two"}`)}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "bad-3", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test","prompt":"secret three"}`)}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "bad-4", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test","prompt":"secret four"}`)}}}},
		{Message: Message{Role: "assistant", Content: "continued with the available evidence"}},
	}}
	var executions atomic.Int64
	engine := Engine{Model: model, Tools: admissionAwareGateway{executions: &executions}}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "fetch"}},
		Tools:    []ToolSchema{{Name: "web_fetch", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil || result.FinalMessage.Content != "continued with the available evidence" || len(model.requests) != 5 {
		t.Fatalf("result=%#v error=%#v requests=%d", result, err, len(model.requests))
	}
	lastMessages := model.requests[4].Messages
	foundFailure := false
	for _, current := range lastMessages {
		if current.Role != "tool" || current.ToolCallID != "bad-4" {
			continue
		}
		var failure map[string]any
		if json.Unmarshal([]byte(current.Content), &failure) != nil {
			t.Fatalf("bounded failure result=%#v", current)
		}
		diagnostic, _ := failure["diagnostic"].(map[string]any)
		encodedDiagnostic, _ := json.Marshal(diagnostic)
		if failure["code"] != "invalid_tool_arguments" || failure["retryable"] != false ||
			!strings.Contains(string(encodedDiagnostic), `"path":"/prompt"`) {
			t.Fatalf("bounded failure result=%#v", current)
		}
		foundFailure = true
	}
	if !foundFailure {
		t.Fatalf("bounded failure result missing from provider context: %#v", lastMessages)
	}
	if executions.Load() != 0 {
		t.Fatalf("invalid calls executed=%d", executions.Load())
	}
}

func TestEngineKeepsRepeatedUnadvertisedCallsModelVisibleWithoutExecution(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "bad-1", Name: "python"}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "bad-2", Name: "shell"}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "bad-3", Name: "repl"}}}},
		{Message: Message{Role: "assistant", Content: "continued without those tools"}},
	}}
	var events []Event
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			t.Fatalf("unadvertised tool executed: %#v", call)
			return ToolResult{}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "run"}},
		Tools:    []ToolSchema{{Name: "software_runtime"}},
	})
	if err != nil || result.FinalMessage.Content != "continued without those tools" || len(model.requests) != 4 {
		t.Fatalf("result=%#v error=%#v requests=%d", result, err, len(model.requests))
	}
	settled := 0
	for _, event := range events {
		if event.Type == EventToolCompleted && event.RejectedBeforeExecution {
			settled++
		}
		if event.Type == EventToolStarted {
			t.Fatalf("rejected call started: %#v", event)
		}
	}
	if settled != 3 {
		t.Fatalf("settled rejection events=%d events=%#v", settled, events)
	}
}

func TestEngineStopsAfterSuccessfulTerminalToolResult(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "submit-1", Name: "submit_output", Arguments: json.RawMessage(`{"answer":"verified"}`)},
			{ID: "after-submit", Name: "read_file", Arguments: json.RawMessage(`{"path":"must-not-run"}`)},
		}}},
		{Message: Message{Role: "assistant", Content: "must not request another model round"}},
	}}
	var executed []string
	var events []Event
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			executed = append(executed, call.Name)
			return ToolResult{
				Value:    map[string]any{"ok": true, "status": "submitted"},
				Terminal: call.Name == "submit_output",
			}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages:             []Message{{Role: "user", Content: "submit once and stop"}},
		Tools:                []ToolSchema{{Name: "submit_output"}, {Name: "read_file"}},
		MaxToolCallsPerRound: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 1 || !reflect.DeepEqual(executed, []string{"submit_output"}) {
		t.Fatalf("model requests=%d executed=%#v", len(model.requests), executed)
	}
	if result.FinalMessage.ToolCalls[0].Name != "submit_output" || len(result.Messages) != 3 {
		t.Fatalf("terminal result=%#v", result)
	}
	if !hasEvent(events, EventToolCompleted, "submit_output") || !hasEvent(events, EventFinal, "") {
		t.Fatalf("terminal lifecycle=%#v", events)
	}
}

type preflightAwareGateway struct {
	executions *atomic.Int64
}

type secondRoundToolChoiceGateway struct {
	executions *atomic.Int64
}

func (g secondRoundToolChoiceGateway) RequiredToolChoice(messages []Message, _ []ToolSchema) any {
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == "clarify" {
			return nil
		}
	}
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == "inspect" {
			return map[string]any{"type": "tool", "name": "ask_user"}
		}
	}
	return nil
}

func (g secondRoundToolChoiceGateway) Execute(_ context.Context, _ ToolCall) (ToolResult, error) {
	g.executions.Add(1)
	return ToolResult{Value: map[string]any{"ok": true}}, nil
}

func TestEngineAppliesToolChoicePolicyAfterToolResult(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "inspect", Name: "read_file", Arguments: json.RawMessage(`{}`),
		}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "clarify", Name: "ask_user", Arguments: json.RawMessage(`{}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "completed"}},
	}}
	var executions atomic.Int64
	engine := Engine{Model: model, Tools: secondRoundToolChoiceGateway{executions: &executions}}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "run"}},
		Tools:         []ToolSchema{{Name: "read_file"}, {Name: "ask_user"}},
		MaxToolRounds: 3,
	})
	if err != nil || result.FinalMessage.Content != "completed" || executions.Load() != 2 {
		t.Fatalf("result=%#v err=%v executions=%d", result, err, executions.Load())
	}
	if got := model.requests[1].ToolChoice; !reflect.DeepEqual(got, map[string]any{"type": "tool", "name": "ask_user"}) {
		t.Fatalf("second-round tool choice=%#v", got)
	}
	if len(model.requests[1].Tools) != 1 || model.requests[1].Tools[0].Name != "ask_user" {
		t.Fatalf("second-round tools=%#v", model.requests[1].Tools)
	}
}

type noMoreToolsGateway struct {
	executed []string
}

func (gateway *noMoreToolsGateway) RequiredToolChoice(messages []Message, _ []ToolSchema) any {
	for _, message := range messages {
		if message.Role == "tool" {
			return "none"
		}
	}
	return nil
}

func (gateway *noMoreToolsGateway) Execute(_ context.Context, call ToolCall) (ToolResult, error) {
	gateway.executed = append(gateway.executed, call.Name)
	return ToolResult{Value: map[string]any{"ok": true}}, nil
}

func TestEngineEnforcesDynamicToolChoiceNoneBeforeTerminalValidation(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "edit", Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"evidence.csv"}`),
		}}}},
		// Simulate an OpenAI-compatible provider that ignores tool_choice=none.
		// This response must remain private and its call must never execute.
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "ignored-save", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["evidence.csv"]}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "terminal candidate"}},
	}}
	gateway := &noMoreToolsGateway{}
	engine := Engine{Model: model, Tools: gateway}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "repair once and finish"}},
		Tools:    []ToolSchema{{Name: "edit_file"}, {Name: "save_artifacts"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gateway.executed, []string{"edit_file"}) {
		t.Fatalf("executed tools=%#v, ignored tool_choice=none call escaped the engine", gateway.executed)
	}
	if result.FinalMessage.Content != "terminal candidate" || len(model.requests) != 3 {
		t.Fatalf("result=%#v requests=%d", result, len(model.requests))
	}
	for index := 1; index < len(model.requests); index++ {
		if model.requests[index].ToolChoice != "none" || len(model.requests[index].Tools) != 0 {
			t.Fatalf("request %d choice=%#v tools=%#v", index, model.requests[index].ToolChoice, model.requests[index].Tools)
		}
	}
}

func (g preflightAwareGateway) ToolCallPreflightDiagnostic(call ToolCall) string {
	var input map[string]any
	if json.Unmarshal(call.Arguments, &input) == nil && input["blocked"] == true {
		return `{"code":"runtime_preflight_required","message":"The selected route is unavailable.","recovery":"Choose an available route."}`
	}
	return ""
}

func (g preflightAwareGateway) Execute(_ context.Context, _ ToolCall) (ToolResult, error) {
	g.executions.Add(1)
	return ToolResult{Value: map[string]any{"ok": true}}, nil
}

func TestEnginePrivatelyRepairsPreflightRejectionBeforeAlternative(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "blocked", Name: "repl", Arguments: json.RawMessage(`{"blocked":true}`),
		}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "allowed", Name: "repl", Arguments: json.RawMessage(`{"blocked":false}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "alternative completed"}},
	}}
	var executions atomic.Int64
	var events []Event
	engine := Engine{
		Model: model, Tools: preflightAwareGateway{executions: &executions},
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "run"}},
		Tools:         []ToolSchema{{Name: "repl", Parameters: map[string]any{"type": "object"}}},
		MaxToolRounds: 2,
	})
	if err != nil || result.FinalMessage.Content != "alternative completed" || executions.Load() != 1 {
		t.Fatalf("result=%#v err=%v executions=%d", result, err, executions.Load())
	}
	feedback := model.requests[1].Messages[len(model.requests[1].Messages)-1]
	if feedback.Role != "tool" || feedback.ToolCallID != "blocked" ||
		!strings.Contains(feedback.Content, `"code":"runtime_preflight_required"`) {
		t.Fatalf("preflight feedback=%#v", feedback)
	}
	blockedFailed, blockedStarted := false, false
	for _, event := range events {
		blockedFailed = blockedFailed || event.Type == EventToolFailed && event.ToolCallID == "blocked"
		blockedStarted = blockedStarted || event.Type == EventToolStarted && event.ToolCallID == "blocked"
	}
	if blockedFailed || blockedStarted {
		t.Fatalf("preflight lifecycle=%#v", events)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("private preflight attempt leaked into public messages=%#v", result.Messages)
	}
}

func TestEnginePrivatePreflightBudgetIsConsecutiveAndResetsAfterProgress(t *testing.T) {
	blocked := func(id string) ModelResponse {
		return ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: id, Name: "repl", Arguments: json.RawMessage(`{"blocked":true}`),
		}}}}
	}
	allowed := func(id string) ModelResponse {
		return ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: id, Name: "repl", Arguments: json.RawMessage(`{"blocked":false}`),
		}}}}
	}
	model := &capturingRequestModelClient{responses: []ModelResponse{
		blocked("first-1"), blocked("first-2"), blocked("first-3"), allowed("progress-1"),
		blocked("second-1"), blocked("second-2"), blocked("second-3"), allowed("progress-2"),
		{Message: Message{Role: "assistant", Content: "completed"}},
	}}
	var executions atomic.Int64
	var events []Event
	engine := Engine{
		Model: model, Tools: preflightAwareGateway{executions: &executions},
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "run"}},
		Tools:         []ToolSchema{{Name: "repl", Parameters: map[string]any{"type": "object"}}},
		MaxToolRounds: 8,
	})
	if err != nil || result.FinalMessage.Content != "completed" || executions.Load() != 2 {
		t.Fatalf("result=%#v err=%v executions=%d", result, err, executions.Load())
	}
	for _, event := range events {
		if event.Type == EventToolFailed {
			t.Fatalf("bounded private correction leaked a public failure: %#v", events)
		}
	}
	if len(result.Messages) != 6 {
		t.Fatalf("private correction attempts leaked into canonical messages=%#v", result.Messages)
	}
}

func TestEnginePublicPreflightRejectionHasCompletedLifecycle(t *testing.T) {
	blocked := func(id string) ModelResponse {
		return ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: id, Name: "repl", Arguments: json.RawMessage(`{"blocked":true}`),
		}}}}
	}
	model := &capturingRequestModelClient{responses: []ModelResponse{
		blocked("private-1"), blocked("private-2"), blocked("private-3"), blocked("public-1"),
		{Message: Message{Role: "assistant", Content: "switched route"}},
	}}
	var executions atomic.Int64
	var events []Event
	engine := Engine{
		Model: model, Tools: preflightAwareGateway{executions: &executions},
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "run"}},
		Tools:    []ToolSchema{{Name: "repl", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil || result.FinalMessage.Content != "switched route" || executions.Load() != 0 {
		t.Fatalf("result=%#v err=%v executions=%d", result, err, executions.Load())
	}
	if !hasRejectedBeforeExecutionEvent(events, "repl") || hasEvent(events, EventToolFailed, "repl") {
		t.Fatalf("public preflight lifecycle=%#v", events)
	}
	if len(model.requests) != 5 || len(model.requests[4].Tools) != 1 || model.requests[4].Tools[0].Name != "repl" {
		t.Fatalf("multiplexed REPL surface was removed after one scoped preflight family: requests=%#v", model.requests)
	}
}

func TestEngineStopsIdenticalRejectedToolRoundsEvenWithCommentary(t *testing.T) {
	blocked := func(id string) ModelResponse {
		return ModelResponse{Message: Message{Role: "assistant", Content: "I corrected it.", ToolCalls: []ToolCall{{
			ID: id, Name: "repl", Arguments: json.RawMessage(`{"blocked":true}`),
		}}}}
	}
	model := &capturingRequestModelClient{responses: []ModelResponse{
		blocked("private-1"), blocked("private-2"), blocked("private-3"),
		blocked("public-1"), blocked("public-2"), blocked("public-3"),
	}}
	var executions atomic.Int64
	engine := Engine{Model: model, Tools: preflightAwareGateway{executions: &executions}}
	_, err := engine.Run(context.Background(), RunRequest{
		Messages:                          []Message{{Role: "user", Content: "run"}},
		Tools:                             []ToolSchema{{Name: "repl", Parameters: map[string]any{"type": "object"}}},
		MaxConsecutiveIdenticalToolRounds: 3,
	})
	var noProgress *ToolRoundNoProgressError
	if !errors.As(err, &noProgress) || noProgress.Limit != 3 || executions.Load() != 0 {
		t.Fatalf("error=%v executions=%d", err, executions.Load())
	}
}

func hasRuntimeToolSchema(schemas []ToolSchema, name string) bool {
	for _, schema := range schemas {
		if schema.Name == name {
			return true
		}
	}
	return false
}

func TestEngineNamespacesProviderToolCallIDsReusedAcrossRounds(t *testing.T) {
	client := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call_0", Name: "noop", Arguments: json.RawMessage(`{"round":1}`),
		}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call_0", Name: "noop", Arguments: json.RawMessage(`{"round":2}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "finished"}},
	}}
	var executed []string
	engine := Engine{
		Model: client,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			executed = append(executed, call.ID)
			return ToolResult{Value: map[string]any{"ok": true, "call": call.ID}}, nil
		}),
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "run two rounds"}}, MaxToolRounds: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "finished" || len(executed) != 2 ||
		executed[0] != "call_0" || executed[1] != "call_0-synon-2" {
		t.Fatalf("result=%#v executed=%#v", result, executed)
	}
	if len(result.Messages) != 6 || result.Messages[1].ToolCalls[0].ID != "call_0" ||
		result.Messages[3].ToolCalls[0].ID != "call_0-synon-2" ||
		result.Messages[2].ToolCallID != "call_0" || result.Messages[4].ToolCallID != "call_0-synon-2" {
		t.Fatalf("messages=%#v", result.Messages)
	}
	if len(client.requests) != 3 || len(client.requests[2].Messages) < 5 ||
		client.requests[2].Messages[3].ToolCalls[0].ID != "call_0-synon-2" ||
		client.requests[2].Messages[4].ToolCallID != "call_0-synon-2" {
		t.Fatalf("provider requests=%#v", client.requests)
	}
}

func TestNamespaceReusedToolCallIDsPreservesSameRoundDuplicates(t *testing.T) {
	messages := []Message{{ToolCalls: []ToolCall{{ID: "call_0"}}}}
	calls := []ToolCall{{ID: "call_0", Name: "noop"}, {ID: "call_0", Name: "noop"}}
	normalized := namespaceReusedToolCallIDs(messages, calls, 2)
	if len(normalized) != 2 || normalized[0].ID != "call_0" || normalized[1].ID != "call_0" {
		t.Fatalf("normalized=%#v", normalized)
	}
}

func TestEngineStopsIdenticalToolCallRoundWithoutProgress(t *testing.T) {
	engine := Engine{
		Model: identicalToolCallModelClient{},
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"ok": true, "call": call.Name}}, nil
		}),
	}
	_, err := engine.Run(context.Background(), RunRequest{
		Messages:                          []Message{{Role: "user", Content: "search and report"}},
		Tools:                             []ToolSchema{{Name: "TodoWrite", Description: "Write a todo."}},
		MaxConsecutiveIdenticalToolRounds: 3,
	})
	var noProgress *ToolRoundNoProgressError
	if !errors.As(err, &noProgress) || noProgress.Limit != 3 {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestEnginePreservesSinglePassNarrationAndToolInterleaving(t *testing.T) {
	model := &singlePassNarrationModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", Content: "先核对资料范围与可比性。", ToolCalls: []ToolCall{{ID: "plan-1", Name: "plan", Arguments: json.RawMessage(`{}`)}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "search-1", Name: "search", Arguments: json.RawMessage(`{}`)}}}},
		{Message: Message{Role: "assistant", Content: "现有证据改变了分析路径，需要补充定量验证。", ToolCalls: []ToolCall{{ID: "analyze-1", Name: "analyze", Arguments: json.RawMessage(`{}`)}}}},
		{Message: Message{Role: "assistant", Content: "最终结论"}},
	}}
	var progress []string
	var executed []string
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			executed = append(executed, call.Name)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
		OnEventError: func(event Event) error {
			if event.Type == EventModelResponse && len(event.ToolCalls) > 0 {
				progress = append(progress, event.Message)
			}
			return nil
		},
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "完成多阶段科研分析"}},
		Tools:    []ToolSchema{{Name: "plan"}, {Name: "search"}, {Name: "analyze"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "最终结论" ||
		!reflect.DeepEqual(executed, []string{"plan", "search", "analyze"}) ||
		!reflect.DeepEqual(progress, []string{"先核对资料范围与可比性。", "", "现有证据改变了分析路径，需要补充定量验证。"}) {
		t.Fatalf("result=%#v executed=%#v progress=%#v", result, executed, progress)
	}
	if len(model.requests) != 4 {
		t.Fatalf("model requests=%d; auxiliary narration calls must not be added", len(model.requests))
	}
	for _, request := range model.requests {
		if request.ToolChoice == "none" && len(request.Tools) == 0 {
			t.Fatalf("unexpected auxiliary presentation request: %#v", request)
		}
	}
}

type singlePassNarrationModelClient struct {
	responses []ModelResponse
	requests  []ModelRequest
}

func (client *singlePassNarrationModelClient) Complete(_ context.Context, request ModelRequest) (ModelResponse, error) {
	client.requests = append(client.requests, request)
	if len(client.responses) == 0 {
		return ModelResponse{}, errors.New("no response")
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	return response, nil
}

func TestEngineInitialToolChoiceAppliesOnlyBeforeFirstToolRound(t *testing.T) {
	model := &capturingToolChoiceModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "download-1", Name: "download", Arguments: json.RawMessage(`{}`)}}}},
		{Message: Message{Role: "assistant", Content: "continued with automatic tool selection"}},
	}}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}
	choice := map[string]any{"type": "tool", "name": "download"}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "recover the dataset"}},
		Tools:    []ToolSchema{{Name: "download"}}, InitialToolChoice: choice,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.choices) != 2 || !reflect.DeepEqual(model.choices[0], choice) || model.choices[1] != nil ||
		result.FinalMessage.Content != "continued with automatic tool selection" {
		t.Fatalf("choices=%#v result=%#v", model.choices, result)
	}
}

func TestEngineRepairsIgnoredRequiredToolChoiceWithoutPublishingProse(t *testing.T) {
	model := &repairingStreamingModelClient{
		responses: []ModelResponse{
			{Message: Message{Role: "assistant", Content: "任务已完成，但未调用工具"}},
			{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "runtime-1", Name: "software_runtime", Arguments: json.RawMessage(`{"capability":"molecular-docking"}`),
			}}}},
			{Message: Message{Role: "assistant", Content: "真实工具执行后完成"}},
		},
		chunks: [][]string{{"任务已完成", "，但未调用工具"}, nil, []string{"真实工具执行后完成"}},
	}
	published := make([]string, 0)
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			if call.Name != "software_runtime" {
				t.Fatalf("tool=%#v", call)
			}
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
		OnModelDelta: func(event ModelStreamEvent) error {
			published = append(published, event.ContentDelta)
			return nil
		},
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "repair the durable scientific failure"}},
		Tools:    []ToolSchema{{Name: "software_runtime"}}, InitialToolChoice: "required",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "真实工具执行后完成" || !reflect.DeepEqual(published, []string{"真实工具执行后完成"}) {
		t.Fatalf("result=%#v published=%#v", result, published)
	}
	if len(model.requests) != 3 || model.requests[0].ToolChoice != "required" || model.requests[1].ToolChoice != "required" ||
		model.requests[2].ToolChoice != nil {
		t.Fatalf("requests=%#v", model.requests)
	}
	secondMessages := model.requests[1].Messages
	if len(secondMessages) != 2 || secondMessages[1].Role != "user" ||
		!strings.Contains(secondMessages[1].Content, "Return no prose") {
		t.Fatalf("repair request messages=%#v", secondMessages)
	}
	for _, message := range result.Messages {
		if strings.Contains(message.Content, "未调用工具") || strings.Contains(message.Content, "Tool protocol repair") {
			t.Fatalf("private protocol repair leaked into canonical result: %#v", result.Messages)
		}
	}
}

func TestEngineRepairsWrongNamedInitialToolWithoutExecutingIt(t *testing.T) {
	model := &repairingStreamingModelClient{
		responses: []ModelResponse{
			{Message: Message{Role: "assistant", Content: "checking the old report", ToolCalls: []ToolCall{{
				ID: "python-1", Name: "python", Arguments: json.RawMessage(`{"code":"print('old')"}`),
			}}}},
			{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "runtime-1", Name: "software_runtime", Arguments: json.RawMessage(`{"capability":"molecular-docking"}`),
			}}}},
			{Message: Message{Role: "assistant", Content: "completed from the unified runtime receipt"}},
		},
		chunks: [][]string{{"checking the old report"}, nil, {"completed from the unified runtime receipt"}},
	}
	executed := make([]string, 0)
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			executed = append(executed, call.Name)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}
	choice := map[string]any{"type": "tool", "name": "software_runtime"}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "repair the missing capability witness"}},
		Tools:    []ToolSchema{{Name: "python"}, {Name: "software_runtime"}}, InitialToolChoice: choice,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(executed, []string{"software_runtime"}) ||
		result.FinalMessage.Content != "completed from the unified runtime receipt" {
		t.Fatalf("executed=%#v result=%#v", executed, result)
	}
	if len(model.requests) != 3 || !reflect.DeepEqual(model.requests[0].ToolChoice, choice) ||
		!reflect.DeepEqual(model.requests[1].ToolChoice, choice) || model.requests[2].ToolChoice != nil {
		t.Fatalf("requests=%#v", model.requests)
	}
	if len(model.requests[0].Tools) != 1 || model.requests[0].Tools[0].Name != "software_runtime" ||
		len(model.requests[1].Tools) != 1 || model.requests[1].Tools[0].Name != "software_runtime" ||
		len(model.requests[2].Tools) != 2 {
		t.Fatalf("constrained request tools=%#v", model.requests)
	}
}

func TestEngineRejectsUnavailableNamedInitialToolBeforeCallingProvider(t *testing.T) {
	model := &capturingToolChoiceModelClient{}
	engine := Engine{Model: model, Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
		t.Fatal("tool gateway must not run for an unavailable constrained tool")
		return ToolResult{}, nil
	})}
	_, err := engine.Run(context.Background(), RunRequest{
		Messages:          []Message{{Role: "user", Content: "repair with the unified runtime"}},
		Tools:             []ToolSchema{{Name: "python"}},
		InitialToolChoice: map[string]any{"type": "tool", "name": "software_runtime"},
	})
	var violation *InitialToolChoiceViolationError
	if !errors.As(err, &violation) || violation.RequiredTool != "software_runtime" || len(model.choices) != 0 {
		t.Fatalf("err=%v choices=%#v", err, model.choices)
	}
}

func TestEngineFailsBoundedlyWhenProviderNeverHonorsRequiredToolChoice(t *testing.T) {
	model := &capturingToolChoiceModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", Content: "prose one"}},
		{Message: Message{Role: "assistant", Content: "prose two"}},
		{Message: Message{Role: "assistant", Content: "prose three"}},
	}}
	engine := Engine{Model: model, Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
		t.Fatal("tool gateway must not run without a model tool call")
		return ToolResult{}, nil
	})}
	_, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "repair with a tool"}},
		Tools:    []ToolSchema{{Name: "repair"}}, InitialToolChoice: "required",
	})
	var violation *InitialToolChoiceViolationError
	if !errors.As(err, &violation) || violation.Attempts != 3 || len(model.choices) != 3 {
		t.Fatalf("err=%v choices=%#v", err, model.choices)
	}
}

type deterministicControlRecoveryGateway struct {
	executed []ToolCall
}

func (gateway *deterministicControlRecoveryGateway) Execute(_ context.Context, call ToolCall) (ToolResult, error) {
	gateway.executed = append(gateway.executed, call)
	return ToolResult{Value: map[string]any{"ok": true}}, nil
}

func (gateway *deterministicControlRecoveryGateway) RecoverRequiredToolCall(
	requiredTool string,
	_ []Message,
	_ []ToolSchema,
) (ToolCall, bool) {
	if requiredTool != "update_step_status" {
		return ToolCall{}, false
	}
	return ToolCall{
		ID: "runtime-control", Name: requiredTool,
		Arguments: json.RawMessage(`{"step":"research-1","status":"completed"}`),
	}, true
}

func TestEngineRecoversDeterministicNamedControlAfterProviderIgnoresToolChoice(t *testing.T) {
	model := &capturingToolChoiceModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", Content: "prose one"}},
		{Message: Message{Role: "assistant", Content: "prose two"}},
		{Message: Message{Role: "assistant", Content: "prose three"}},
		{Message: Message{Role: "assistant", Content: "finished after runtime control reconciliation"}},
	}}
	gateway := &deterministicControlRecoveryGateway{}
	choice := map[string]any{"type": "tool", "name": "update_step_status"}
	result, err := (Engine{Model: model, Tools: gateway}).Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "continue the durable research plan"}},
		Tools:    []ToolSchema{{Name: "update_step_status"}}, InitialToolChoice: choice,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "finished after runtime control reconciliation" || len(gateway.executed) != 1 ||
		gateway.executed[0].Name != "update_step_status" || !gateway.executed[0].RuntimeRecovered {
		t.Fatalf("result=%#v executed=%#v", result, gateway.executed)
	}
	if len(model.choices) != 4 || !reflect.DeepEqual(model.choices[0], choice) ||
		!reflect.DeepEqual(model.choices[1], choice) || !reflect.DeepEqual(model.choices[2], choice) ||
		model.choices[3] != nil {
		t.Fatalf("choices=%#v", model.choices)
	}
	for _, message := range result.Messages {
		if strings.HasPrefix(message.Content, "prose ") || strings.Contains(message.Content, "Tool protocol repair") {
			t.Fatalf("discarded provider protocol output leaked into result: %#v", result.Messages)
		}
	}
}

type repairingStreamingModelClient struct {
	responses []ModelResponse
	chunks    [][]string
	requests  []ModelRequest
}

func (client *repairingStreamingModelClient) Complete(context.Context, ModelRequest) (ModelResponse, error) {
	return ModelResponse{}, errors.New("streaming completion required")
}

func (client *repairingStreamingModelClient) CompleteStream(
	_ context.Context,
	request ModelRequest,
	onEvent func(ModelStreamEvent) error,
) (ModelResponse, error) {
	client.requests = append(client.requests, request)
	if len(client.responses) == 0 || len(client.chunks) == 0 {
		return ModelResponse{}, errors.New("no response")
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	chunks := client.chunks[0]
	client.chunks = client.chunks[1:]
	for _, chunk := range chunks {
		if err := onEvent(ModelStreamEvent{ContentDelta: chunk}); err != nil {
			return ModelResponse{}, err
		}
	}
	return response, nil
}

type capturingToolChoiceModelClient struct {
	responses []ModelResponse
	choices   []any
}

func (client *capturingToolChoiceModelClient) Complete(_ context.Context, request ModelRequest) (ModelResponse, error) {
	client.choices = append(client.choices, request.ToolChoice)
	if len(client.responses) == 0 {
		return ModelResponse{}, errors.New("no response")
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	return response, nil
}

type identicalToolCallModelClient struct{}

func (identicalToolCallModelClient) Complete(context.Context, ModelRequest) (ModelResponse, error) {
	return ModelResponse{Message: Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID: "call_loop", Name: "TodoWrite", Arguments: json.RawMessage(`{"content":"search PubMed"}`),
		}},
	}}, nil
}

func TestEngineCheckedToolStartFailurePreventsGatewayExecution(t *testing.T) {
	checkpointErr := errors.New("persist tool start checkpoint")
	var gatewayCalls atomic.Int64
	engine := Engine{
		Model: &staticModelClient{responses: []ModelResponse{{Message: Message{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID: "call_mutation", Name: "mutate", Arguments: json.RawMessage(`{"value":"unsafe"}`),
			}},
		}}}},
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			gatewayCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
		OnEventError: func(event Event) error {
			if event.Type == EventToolStarted {
				return checkpointErr
			}
			return nil
		},
	}

	_, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "mutate durable state"}},
		Tools:    []ToolSchema{{Name: "mutate", Parameters: map[string]any{"type": "object"}}},
	})
	if !errors.Is(err, checkpointErr) {
		t.Fatalf("Engine.Run() error = %v", err)
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gateway calls after failed durable start checkpoint = %d", gatewayCalls.Load())
	}
}

func TestEngineCheckedToolTerminalFailurePreventsNextModelRequest(t *testing.T) {
	tests := []struct {
		name      string
		eventType EventType
		execute   FuncToolGateway
	}{
		{
			name:      "completed",
			eventType: EventToolCompleted,
			execute: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Value: map[string]any{"ok": true}}, nil
			}),
		},
		{
			name:      "failed",
			eventType: EventToolFailed,
			execute: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{}, errors.New("gateway failure")
			}),
		},
		{
			name:      "paused",
			eventType: EventToolPaused,
			execute: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{}, &PauseError{Status: "awaiting_approval", Message: "approval required"}
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkpointErr := fmt.Errorf("persist %s tool checkpoint", test.name)
			model := &staticModelClient{responses: []ModelResponse{
				{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
					ID: "call_terminal", Name: "mutate", Arguments: json.RawMessage(`{"value":"unsafe"}`),
				}}}},
				{Message: Message{Role: "assistant", Content: "must not be requested"}},
			}}
			engine := Engine{
				Model: model,
				Tools: test.execute,
				OnEventError: func(event Event) error {
					if event.Type == test.eventType {
						return checkpointErr
					}
					return nil
				},
			}

			_, err := engine.Run(context.Background(), RunRequest{
				Messages: []Message{{Role: "user", Content: "mutate durable state"}},
				Tools:    []ToolSchema{{Name: "mutate", Parameters: map[string]any{"type": "object"}}},
			})
			if !errors.Is(err, checkpointErr) {
				t.Fatalf("Engine.Run() error = %v", err)
			}
			if calls := model.calls.Load(); calls != 1 {
				t.Fatalf("model requests after failed durable terminal checkpoint = %d", calls)
			}
		})
	}
}

func TestEngineReturnsErrorWhenConfiguredToolRoundLimitIsExceeded(t *testing.T) {
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"tool_calls": [{
						"id": "call_loop",
						"type": "function",
						"function": {"name": "noop", "arguments": "{}"}
					}]
				}
			}]
		}`))
	}))
	defer modelAPI.Close()

	engine := Engine{
		Model: OpenAIChatClient{Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "runtime-model"},
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}

	_, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "loop"}},
		Tools:         []ToolSchema{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
		MaxToolRounds: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "exceeded 1 tool call rounds") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestEngineSharesToolRoundBudgetAcrossRuns(t *testing.T) {
	client := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Name: "noop", Arguments: json.RawMessage(`{}`)}}}},
		{Message: Message{Role: "assistant", Content: "first final"}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-2", Name: "noop", Arguments: json.RawMessage(`{}`)}}}},
	}}
	var gatewayCalls atomic.Int64
	engine := Engine{
		Model: client,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			gatewayCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}
	budget := NewToolRoundBudget(1)
	request := RunRequest{
		Messages:        []Message{{Role: "user", Content: "run"}},
		Tools:           []ToolSchema{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
		ToolRoundBudget: budget,
	}
	if _, err := engine.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Run(context.Background(), request)
	var limitErr *ToolRoundLimitError
	if !errors.As(err, &limitErr) || limitErr.Limit != 1 {
		t.Fatalf("second Run() error = %#v", err)
	}
	if gatewayCalls.Load() != 1 || client.calls.Load() != 3 {
		t.Fatalf("gatewayCalls=%d modelCalls=%d", gatewayCalls.Load(), client.calls.Load())
	}
	if remaining, limited := budget.Remaining(); !limited || remaining != 0 {
		t.Fatalf("remaining=%d limited=%t", remaining, limited)
	}
}

func TestEngineRejectsOversizedToolCallBatchBeforeGatewaySideEffects(t *testing.T) {
	toolCalls := make([]ToolCall, 0, 4)
	for index := 0; index < 4; index++ {
		toolCalls = append(toolCalls, ToolCall{
			ID: fmt.Sprintf("call-%d", index), Name: "noop", Arguments: json.RawMessage(`{}`),
		})
	}
	client := &staticModelClient{responses: []ModelResponse{{Message: Message{Role: "assistant", ToolCalls: toolCalls}}}}
	var gatewayCalls atomic.Int64
	engine := Engine{
		Model: client,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			gatewayCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}
	_, err := engine.Run(context.Background(), RunRequest{
		Messages:             []Message{{Role: "user", Content: "run"}},
		Tools:                []ToolSchema{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
		MaxToolCallsPerRound: 3,
	})
	var limitErr *ToolCallBatchLimitError
	if !errors.As(err, &limitErr) || limitErr.Limit != 3 || limitErr.Received != 4 {
		t.Fatalf("Run() error = %#v", err)
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gatewayCalls=%d, want zero", gatewayCalls.Load())
	}
}

func TestEngineExecutesUnboundedToolCallBatchInModelOrder(t *testing.T) {
	toolCalls := make([]ToolCall, 0, 4)
	for index := 0; index < 4; index++ {
		toolCalls = append(toolCalls, ToolCall{
			ID: fmt.Sprintf("call-%d", index), Name: "noop", Arguments: json.RawMessage(`{"index":0}`),
		})
	}
	client := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: toolCalls}},
		{Message: Message{Role: "assistant", Content: "all tools completed"}},
	}}
	var executed []string
	engine := Engine{
		Model: client,
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			executed = append(executed, call.ID)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "run all four"}},
		Tools:    []ToolSchema{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalMessage.Content != "all tools completed" {
		t.Fatalf("final content = %q", result.FinalMessage.Content)
	}
	want := []string{"call-0", "call-1", "call-2", "call-3"}
	if !reflect.DeepEqual(executed, want) {
		t.Fatalf("executed = %#v, want %#v", executed, want)
	}
}

func TestEngineRejectsInvalidToolCallBatchBeforeEventsOrGatewaySideEffects(t *testing.T) {
	tests := []struct {
		name  string
		calls []ToolCall
		code  string
	}{
		{name: "duplicate id", calls: []ToolCall{{ID: "same", Name: "noop", Arguments: json.RawMessage(`{}`)}, {ID: "same", Name: "noop", Arguments: json.RawMessage(`{}`)}}, code: "duplicate_id"},
		{name: "blank id", calls: []ToolCall{{ID: " ", Name: "noop", Arguments: json.RawMessage(`{}`)}}, code: "invalid_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &staticModelClient{responses: []ModelResponse{{Message: Message{Role: "assistant", ToolCalls: test.calls}}}}
			var gatewayCalls atomic.Int64
			var events atomic.Int64
			engine := Engine{
				Model: client,
				Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
					gatewayCalls.Add(1)
					return ToolResult{}, nil
				}),
				OnEvent: func(Event) { events.Add(1) },
			}
			_, err := engine.Run(context.Background(), RunRequest{
				Messages: []Message{{Role: "user", Content: "invalid batch"}},
				Tools:    []ToolSchema{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
			})
			var validationErr *ToolCallBatchValidationError
			if !errors.As(err, &validationErr) || validationErr.Code != test.code {
				t.Fatalf("Run() error = %#v", err)
			}
			if gatewayCalls.Load() != 0 || events.Load() != 1 {
				t.Fatalf("gatewayCalls=%d events=%d", gatewayCalls.Load(), events.Load())
			}
		})
	}
}

func TestEngineToolRoundBudgetIsConsumedByPauseAndAllowsLaterFinal(t *testing.T) {
	client := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "ask-1", Name: "AskUserQuestion", Arguments: json.RawMessage(`{"questions":[]}`)}}}},
		{Message: Message{Role: "assistant", Content: "final without another tool"}},
	}}
	engine := Engine{
		Model: client,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{}, &PauseError{Status: "awaiting_user_response", Message: "waiting"}
		}),
	}
	budget := NewToolRoundBudget(1)
	request := RunRequest{
		Messages:        []Message{{Role: "user", Content: "ask"}},
		Tools:           []ToolSchema{{Name: "AskUserQuestion", Parameters: map[string]any{"type": "object"}}},
		ToolRoundBudget: budget,
	}
	if _, err := engine.Run(context.Background(), request); err == nil {
		t.Fatal("paused Run() unexpectedly succeeded")
	}
	result, err := engine.Run(context.Background(), request)
	if err != nil || result.FinalMessage.Content != "final without another tool" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if remaining, limited := budget.Remaining(); !limited || remaining != 0 {
		t.Fatalf("remaining=%d limited=%t", remaining, limited)
	}
}

func TestEngineHasNoToolRoundLimitByDefault(t *testing.T) {
	responses := make([]ModelResponse, 0, 7)
	for index := 0; index < 6; index++ {
		responses = append(responses, ModelResponse{Message: Message{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID:        fmt.Sprintf("call_%d", index),
				Name:      "noop",
				Arguments: json.RawMessage(`{}`),
			}},
		}})
	}
	responses = append(responses, ModelResponse{Message: Message{Role: "assistant", Content: "completed after six tool rounds"}})

	var gatewayCalls atomic.Int64
	client := &staticModelClient{responses: responses}
	engine := Engine{
		Model: client,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			gatewayCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "complete a long task"}},
		Tools:    []ToolSchema{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalMessage.Content != "completed after six tool rounds" || gatewayCalls.Load() != 6 || client.calls.Load() != 7 {
		t.Fatalf("result=%#v gatewayCalls=%d modelCalls=%d", result, gatewayCalls.Load(), client.calls.Load())
	}
}

func TestEngineStopsAtDurableUserInputPause(t *testing.T) {
	client := &staticModelClient{responses: []ModelResponse{{Message: Message{
		Role: "assistant", ToolCalls: []ToolCall{{ID: "ask-1", Name: "AskUserQuestion", Arguments: json.RawMessage(`{"questions":[]}`)}},
	}}}}
	events := make([]Event, 0)
	engine := Engine{
		Model: client,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{}, &PauseError{Status: "awaiting_user_response", Message: "waiting for answer", Data: map[string]any{"tool_id": "ask-1"}}
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "clarify first"}},
		Tools:    []ToolSchema{{Name: "AskUserQuestion", Parameters: map[string]any{"type": "object"}}},
	})
	var pause *PauseError
	if !errors.As(err, &pause) || pause.Status != "awaiting_user_response" {
		t.Fatalf("pause error = %#v, err=%v", pause, err)
	}
	if len(result.Messages) != 3 || !hasEvent(events, EventToolPaused, "AskUserQuestion") || client.calls.Load() != 1 {
		t.Fatalf("result=%#v events=%#v calls=%d", result, events, client.calls.Load())
	}
}

func TestEngineReturnsInvalidToolCallResultWithoutCallingGateway(t *testing.T) {
	var gatewayCalls atomic.Int64
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch sequence {
		case 1:
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "bad_args_1",
							"type": "function",
							"function": {"name": "echo_upper", "arguments": "{not-json"}
						}]
					}
				}]
			}`))
		case 2:
			messages := request["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "bad_args_1" || !strings.Contains(last["content"].(string), "valid JSON") {
				t.Fatalf("invalid tool result message = %#v", last)
			}
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {"role": "assistant", "content": "saw invalid tool call"}
				}]
			}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	events := []Event{}
	engine := Engine{
		Model: OpenAIChatClient{Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "runtime-model"},
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			gatewayCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	}

	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "call a tool badly"}},
		Tools:         []ToolSchema{{Name: "echo_upper", Parameters: map[string]any{"type": "object"}}},
		MaxToolRounds: 2,
	})
	if err != nil || result.FinalMessage.Content != "saw invalid tool call" ||
		gatewayCalls.Load() != 0 || requests.Load() != 2 || !hasEvent(events, EventToolFailed, "echo_upper") {
		t.Fatalf("result=%#v error=%#v gatewayCalls=%d requests=%d events=%#v", result, err, gatewayCalls.Load(), requests.Load(), events)
	}
}

func TestEngineReturnsProviderArgumentDiagnosticToModel(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "provider-invalid", Name: "lookup", Arguments: json.RawMessage(`{}`),
			ProviderProtocolDiagnostic: "provider emitted invalid JSON object arguments",
		}}}},
		{Message: Message{Role: "assistant", Content: "continued after provider argument repair"}},
	}}
	var gatewayCalls atomic.Int64
	var events []Event
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			gatewayCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "run"}},
		Tools:    []ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil || result.FinalMessage.Content != "continued after provider argument repair" || gatewayCalls.Load() != 0 {
		t.Fatalf("result=%#v err=%v gatewayCalls=%d", result, err, gatewayCalls.Load())
	}
	feedback := model.requests[1].Messages[len(model.requests[1].Messages)-1]
	if feedback.Role != "tool" || feedback.ToolCallID != "provider-invalid" ||
		!strings.Contains(feedback.Content, `"code":"invalid_tool_arguments"`) {
		t.Fatalf("provider feedback=%#v", feedback)
	}
	if hasEvent(events, EventToolFailed, "lookup") || hasEvent(events, EventToolStarted, "lookup") {
		t.Fatalf("events=%#v", events)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("provider argument repair leaked into public messages=%#v", result.Messages)
	}
}

func TestEngineRejectsEmptyToolNameBeforeGateway(t *testing.T) {
	var gatewayCalls atomic.Int64
	engine := Engine{
		Model: &staticModelClient{responses: []ModelResponse{{
			Message: Message{
				Role:      "assistant",
				ToolCalls: []ToolCall{{ID: "missing_name", Arguments: json.RawMessage(`{"text":"hello"}`)}},
			},
		}, {
			Message: Message{Role: "assistant", Content: "handled missing name"},
		}}},
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			gatewayCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}

	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "bad tool"}},
		MaxToolRounds: 2,
	})
	if err != nil || result.FinalMessage.Content != "handled missing name" || gatewayCalls.Load() != 0 {
		t.Fatalf("result=%#v error=%#v gatewayCalls=%d", result, err, gatewayCalls.Load())
	}
}

func TestEngineRequiresToolArgumentsObject(t *testing.T) {
	var gatewayCalls atomic.Int64
	engine := Engine{
		Model: &staticModelClient{responses: []ModelResponse{{
			Message: Message{
				Role:      "assistant",
				ToolCalls: []ToolCall{{ID: "array_args", Name: "echo_upper", Arguments: json.RawMessage(`["not","object"]`)}},
			},
		}, {
			Message: Message{Role: "assistant", Content: "handled array args"},
		}}},
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			gatewayCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
	}

	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "array args"}},
		MaxToolRounds: 2,
	})
	if err != nil || result.FinalMessage.Content != "handled array args" || gatewayCalls.Load() != 0 {
		t.Fatalf("result=%#v error=%#v gatewayCalls=%d", result, err, gatewayCalls.Load())
	}
}

func TestEngineResumesPausedMultiToolBatchAtExactOrdinal(t *testing.T) {
	var firstCalls, pausedCalls, lastCalls atomic.Int64
	engine := Engine{Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		switch call.ID {
		case "call-first":
			firstCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true, "ordinal": 0}}, nil
		case "call-paused":
			attempt := pausedCalls.Add(1)
			if attempt == 1 {
				return ToolResult{}, &PauseError{Status: "awaiting_approval", Message: "approval required"}
			}
			return ToolResult{Value: map[string]any{"ok": true, "ordinal": 1}}, nil
		case "call-last":
			lastCalls.Add(1)
			return ToolResult{Value: map[string]any{"ok": true, "ordinal": 2}}, nil
		default:
			t.Fatalf("unexpected call %#v", call)
			return ToolResult{}, nil
		}
	})}
	calls := []ToolCall{
		{ID: "call-first", Name: "first", Arguments: json.RawMessage(`{}`)},
		{ID: "call-paused", Name: "paused", Arguments: json.RawMessage(`{}`)},
		{ID: "call-last", Name: "last", Arguments: json.RawMessage(`{}`)},
	}

	initial, err := engine.ExecuteToolBatch(context.Background(), calls, 0, MediaPolicy{}, 0)
	var pause *PauseError
	if !errors.As(err, &pause) || initial.NextOrdinal != 1 || len(initial.Messages) != 2 {
		t.Fatalf("initial=%#v err=%v", initial, err)
	}
	resumed, err := engine.ExecuteToolBatch(
		context.Background(), calls, initial.NextOrdinal, MediaPolicy{}, initial.MediaBytesUsed,
	)
	if err != nil || resumed.NextOrdinal != len(calls) || len(resumed.Messages) != 2 {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
	if firstCalls.Load() != 1 || pausedCalls.Load() != 2 || lastCalls.Load() != 1 {
		t.Fatalf("gateway counts first=%d paused=%d last=%d", firstCalls.Load(), pausedCalls.Load(), lastCalls.Load())
	}
}

func TestEngineRuntimeCancellationLeavesToolOpenWithoutFailureEvent(t *testing.T) {
	drainCause := errors.New("runtime draining")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(drainCause)
	events := []Event{}
	engine := Engine{
		Tools: FuncToolGateway(func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			return ToolResult{}, context.Cause(ctx)
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	execution, err := engine.ExecuteToolBatch(ctx, []ToolCall{{
		ID: "call-draining", Name: "read_source", Arguments: json.RawMessage(`{}`),
	}}, 0, MediaPolicy{}, 0)
	if !errors.Is(err, drainCause) || execution.NextOrdinal != 0 || len(execution.Messages) != 0 {
		t.Fatalf("execution=%#v err=%v", execution, err)
	}
	if !hasEvent(events, EventToolStarted, "read_source") ||
		hasEvent(events, EventToolFailed, "read_source") ||
		hasEvent(events, EventToolCompleted, "read_source") {
		t.Fatalf("events=%#v", events)
	}
}

func TestEngineRecoveredToolEmitsTerminalWithoutSecondStart(t *testing.T) {
	events := []Event{}
	engine := Engine{
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	execution, err := engine.ExecuteToolBatch(context.Background(), []ToolCall{{
		ID: "call-recovered", Name: "read_source", Arguments: json.RawMessage(`{}`), Resumed: true,
	}}, 0, MediaPolicy{}, 0)
	if err != nil || execution.NextOrdinal != 1 || len(execution.Messages) != 1 {
		t.Fatalf("execution=%#v err=%v", execution, err)
	}
	if hasEvent(events, EventToolStarted, "read_source") ||
		!hasEvent(events, EventToolCompleted, "read_source") ||
		hasEvent(events, EventToolFailed, "read_source") {
		t.Fatalf("events=%#v", events)
	}
}

func TestEngineTerminalEventUsesExecutedArguments(t *testing.T) {
	events := []Event{}
	engine := Engine{
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{
				Value:             map[string]any{"ok": true},
				ExecutedArguments: json.RawMessage(`{"query":"normalized follow-up","research_session":{"id":"session-1","mode":"continue"}}`),
			}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	_, err := engine.ExecuteToolBatch(context.Background(), []ToolCall{{
		ID: "normalized-source", Name: "web_research", Arguments: json.RawMessage(`{"query":"model draft"}`),
	}}, 0, MediaPolicy{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != EventToolStarted || events[0].Arguments != `{"query":"model draft"}` {
		t.Fatalf("tool start lost the requested input: %#v", events)
	}
	if events[1].Type != EventToolCompleted || events[1].Arguments != `{"query":"model draft"}` ||
		!strings.Contains(events[1].ExecutedArguments, "normalized follow-up") ||
		!strings.Contains(events[1].ExecutedArguments, "session-1") || strings.Contains(events[1].ExecutedArguments, "model draft") {
		t.Fatalf("terminal event did not preserve requested and executed input identities: %#v", events[1])
	}
}

func TestToolModelContextStaysInToolRoleWithoutChangingEvidenceEvents(t *testing.T) {
	events := []Event{}
	engine := Engine{
		Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			return ToolResult{
				Value:        map[string]any{"ok": true, "source": call.ID},
				ModelContext: map[string]any{"schema": "runtime-state.v1", "source_call": call.ID},
			}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	execution, err := engine.ExecuteToolBatch(context.Background(), []ToolCall{
		{ID: "source-a", Name: "read_source", Arguments: json.RawMessage(`{}`)},
		{ID: "source-b", Name: "read_source", Arguments: json.RawMessage(`{}`)},
	}, 0, MediaPolicy{}, 0)
	if err != nil || len(execution.Messages) != 2 {
		t.Fatalf("execution=%#v err=%v", execution, err)
	}
	for index := 0; index < 2; index++ {
		message := execution.Messages[index]
		if message.Role != "tool" || !strings.Contains(message.Content, "runtime-state.v1") ||
			!strings.Contains(message.Content, callIDForOrdinal(index)) {
			t.Fatalf("source result %d did not retain tool-role model context: %#v", index, message)
		}
	}
	for _, event := range events {
		if event.Type == EventToolCompleted && strings.Contains(event.Result, "runtime-state.v1") {
			t.Fatalf("continuation state contaminated durable evidence event: %#v", event)
		}
	}
}

func callIDForOrdinal(index int) string {
	if index == 0 {
		return "source-a"
	}
	return "source-b"
}

func TestRuntimeMessageFromOpenAIDefaultsAssistantRole(t *testing.T) {
	message := runtimeMessageFromOpenAI(openAIMessage{Content: "hello"})
	if message.Role != "assistant" || message.Content != "hello" {
		t.Fatalf("runtime message = %#v", message)
	}
}

func TestEngineEmitsStreamingModelDeltasBeforeFinalResponse(t *testing.T) {
	client := &streamingStaticModelClient{
		deltas:   []ModelStreamEvent{{ContentDelta: "hel"}, {ContentDelta: "lo"}},
		response: ModelResponse{Message: Message{Role: "assistant", Content: "hello"}},
	}
	events := make([]Event, 0)
	engine := Engine{
		Model: client,
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "stream"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "hello" || client.completeCalls.Load() != 0 || client.streamCalls.Load() != 1 {
		t.Fatalf("result=%#v completeCalls=%d streamCalls=%d", result, client.completeCalls.Load(), client.streamCalls.Load())
	}
	deltas := make([]string, 0)
	for _, event := range events {
		if event.Type == EventModelDelta {
			deltas = append(deltas, event.Message)
		}
	}
	if len(deltas) != 2 || deltas[0] != "hel" || deltas[1] != "lo" {
		t.Fatalf("events = %#v", events)
	}
}

func TestEngineEmitsFailedEventForSemanticToolFailureAndContinues(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "search-1", Name: "WebSearch", Arguments: json.RawMessage(`{"query":"test"}`)}}}},
		{Message: Message{Role: "assistant", Content: "Source unavailable."}},
	}}
	events := []Event{}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"ok": false, "error": "search unavailable"}}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "Find evidence"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "Source unavailable." {
		t.Fatalf("final = %#v", result.FinalMessage)
	}
	foundFailed, foundCompleted := false, false
	for _, event := range events {
		if event.ToolCallID != "search-1" {
			continue
		}
		foundFailed = foundFailed || event.Type == EventToolFailed && event.Message == "tool result reported failure"
		foundCompleted = foundCompleted || event.Type == EventToolCompleted
	}
	if !foundFailed || foundCompleted {
		t.Fatalf("semantic failure events = %#v", events)
	}
}

func TestEngineEmitsCompletedLifecycleForNonExecutingPreflight(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "preflight-1", Name: "python", Arguments: json.RawMessage(`{"code":"print("}`)}}}},
		{Message: Message{Role: "assistant", Content: "Corrected next step."}},
	}}
	events := []Event{}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{
				"ok": false, "executed": false, "status": "python_syntax_preflight_required",
				"message": "Python syntax validation failed before execution.",
			}}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "Run code"}}})
	if err != nil || result.FinalMessage.Content != "Corrected next step." {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !hasRejectedBeforeExecutionEvent(events, "python") || hasEvent(events, EventToolFailed, "python") {
		t.Fatalf("non-executing preflight lifecycle events=%#v", events)
	}
}

func TestEngineFailedEventIncludesOnlySafeTypedFailureCode(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "runtime-1", Name: "software_runtime", Arguments: json.RawMessage(`{"capability":"molecular-docking"}`)}}}},
		{Message: Message{Role: "assistant", Content: "Recovered."}},
	}}
	events := []Event{}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{
				"ok": false, "code": "software_runtime_binary_abi_incompatible",
				"details": map[string]any{"diagnostic": "private path must stay in the result"},
			}}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	if _, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "Run"}}}); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.ToolCallID != "runtime-1" || event.Type != EventToolFailed {
			continue
		}
		if event.Message != "tool result reported failure: software_runtime_binary_abi_incompatible" ||
			strings.Contains(event.Message, "private path") {
			t.Fatalf("typed failure lifecycle event = %#v", event)
		}
		return
	}
	t.Fatalf("typed failure lifecycle event missing: %#v", events)
}

func TestEngineExternalizesOversizedToolResultWithoutChangingOutcomeOrParts(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "large-1", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"https://example.test"}`)}}}},
		{Message: Message{Role: "assistant", Content: "The complete result is available from the artifact."}},
	}}
	events := []Event{}
	authority := &recordingLargeToolResultAuthority{}
	value := map[string]any{"ok": true, "result": strings.Repeat("evidence-科学-", 256)}
	expectedRaw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
			return ToolResult{Value: value, Parts: []ContentPart{{Type: ContentPartText, Text: "visual sidecar retained"}}}, nil
		}),
		MaxToolResultBytes: 1024,
		LargeToolResults:   authority,
		OnEvent:            func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "Fetch evidence"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(authority.inputs) != 1 || !bytes.Equal(authority.inputs[0].RawJSON, expectedRaw) ||
		authority.inputs[0].Outcome != ToolResultSucceeded || authority.inputs[0].ToolCall.ID != "large-1" ||
		authority.inputs[0].ToolCall.Name != "WebFetch" || authority.inputs[0].MaxInlineBytes != 1024 {
		t.Fatalf("authority inputs = %#v, raw=%q", authority.inputs, authority.inputs[0].RawJSON)
	}
	toolMessage := Message{}
	for _, message := range result.Messages {
		if message.Role == "tool" && message.ToolCallID == "large-1" {
			toolMessage = message
			break
		}
	}
	var descriptor LargeToolResultDescriptor
	if err := json.Unmarshal([]byte(toolMessage.Content), &descriptor); err != nil {
		t.Fatalf("descriptor JSON = %q: %v", toolMessage.Content, err)
	}
	if descriptor.ArtifactID != "artifact-large-1" || descriptor.VersionID != "version-large-1" ||
		descriptor.SizeBytes != int64(len(expectedRaw)) || descriptor.Outcome != ToolResultSucceeded ||
		descriptor.ContentURL != "/api/artifacts/artifact-large-1/versions/version-large-1" ||
		!descriptor.Truncated || descriptor.Preview == "" || !utf8.ValidString(descriptor.Preview) {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	if strings.Contains(toolMessage.Content, strings.Repeat("evidence", 32)) {
		t.Fatalf("descriptor retained oversized inline content: %s", toolMessage.Content)
	}
	if len(model.requests) != 2 || len(model.requests[1].Messages) < 4 {
		t.Fatalf("model requests = %#v", model.requests)
	}
	nextMessages := model.requests[1].Messages
	if nextMessages[len(nextMessages)-2].Content != toolMessage.Content ||
		len(nextMessages[len(nextMessages)-1].Parts) != 2 || nextMessages[len(nextMessages)-1].Parts[1].Text != "visual sidecar retained" {
		t.Fatalf("next provider messages = %#v", nextMessages)
	}
	failed, completed := false, false
	for _, event := range events {
		if event.ToolCallID != "large-1" {
			continue
		}
		failed = failed || event.Type == EventToolFailed
		completed = completed || event.Type == EventToolCompleted
	}
	if failed || !completed {
		t.Fatalf("externalized result events = %#v", events)
	}
}

func TestEngineMaterializedToolResultBindsCanonicalJSONDigestAndArtifactReference(t *testing.T) {
	authority := &recordingLargeToolResultAuthority{}
	value := map[string]any{"ok": true, "result": strings.Repeat("large-evidence-", 256)}
	engine := Engine{MaxToolResultBytes: 512, LargeToolResults: authority}
	materialized, err := engine.MaterializeToolResult(context.Background(), ToolCall{
		ID: "materialized-1", Name: "WebFetch", Arguments: json.RawMessage(`{}`),
	}, value, ToolResultSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(materialized.JSON) || materialized.ResultRef != "artifact-version:version-materialized-1" ||
		materialized.Outcome != ToolResultSucceeded {
		t.Fatalf("materialized=%#v", materialized)
	}
	digest := sha256.Sum256(materialized.JSON)
	if materialized.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("materialized digest=%q want=%q", materialized.SHA256, hex.EncodeToString(digest[:]))
	}
	var descriptor LargeToolResultDescriptor
	if err := json.Unmarshal(materialized.JSON, &descriptor); err != nil ||
		descriptor.VersionID != "version-materialized-1" || descriptor.SHA256 == materialized.SHA256 {
		t.Fatalf("descriptor=%#v err=%v", descriptor, err)
	}
}

func TestEngineReturnsTypedInfrastructureErrorWhenLargeResultAuthorityFails(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID: "large-failure", Name: "WebFetch", Arguments: json.RawMessage(`{}`),
			}},
		}},
	}}
	authorityErr := errors.New("artifact store secret=/private/runtime/blob.db")
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"ok": true, "result": strings.Repeat("x", 2048)}}, nil
		}),
		MaxToolResultBytes: 512,
		LargeToolResults: FuncLargeToolResultAuthority(func(context.Context, LargeToolResultInput) (LargeToolResultDescriptor, error) {
			return LargeToolResultDescriptor{}, authorityErr
		}),
	}
	result, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "fetch"}}})
	var infrastructureErr *LargeToolResultInfrastructureError
	if !errors.As(err, &infrastructureErr) || !errors.Is(err, authorityErr) || infrastructureErr.ToolCallID != "large-failure" {
		t.Fatalf("Run() error = %#v", err)
	}
	if infrastructureErr.ReasonCode() != "large_tool_result_authority_failed" {
		t.Fatalf("infrastructure reason code = %q", infrastructureErr.ReasonCode())
	}
	if err.Error() != "large tool result infrastructure failure" || strings.Contains(err.Error(), "secret=") ||
		strings.Contains(err.Error(), "large-failure") || strings.Contains(err.Error(), "WebFetch") {
		t.Fatalf("infrastructure error leaked internal detail: %q", err.Error())
	}
	for _, message := range result.Messages {
		if strings.Contains(message.Content, "tool result exceeded") || strings.Contains(message.Content, `"ok":false`) {
			t.Fatalf("authority failure was converted to a tool business result: %#v", result.Messages)
		}
	}
}

func TestEngineExternalizedSemanticFailureKeepsFailedOutcome(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "large-failed", Name: "WebFetch", Arguments: json.RawMessage(`{}`)}}}},
		{Message: Message{Role: "assistant", Content: "reported the failure"}},
	}}
	authority := &recordingLargeToolResultAuthority{}
	events := []Event{}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"ok": false, "error": strings.Repeat("upstream failed ", 256)}}, nil
		}),
		MaxToolResultBytes: 1024,
		LargeToolResults:   authority,
		OnEvent:            func(event Event) { events = append(events, event) },
	}
	result, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "fetch"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(authority.inputs) != 1 || authority.inputs[0].Outcome != ToolResultFailed {
		t.Fatalf("authority inputs = %#v", authority.inputs)
	}
	var descriptor LargeToolResultDescriptor
	for _, message := range result.Messages {
		if message.Role == "tool" && message.ToolCallID == "large-failed" {
			if err := json.Unmarshal([]byte(message.Content), &descriptor); err != nil {
				t.Fatal(err)
			}
		}
	}
	if descriptor.Outcome != ToolResultFailed || !hasEvent(events, EventToolFailed, "WebFetch") || hasEvent(events, EventToolCompleted, "WebFetch") {
		t.Fatalf("descriptor=%#v events=%#v", descriptor, events)
	}
}

func TestEngineContinuesAfterTypedUnavailableWebFetchResult(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "missing-source", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test/missing"}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "continued with another evidence source"}},
	}}
	events := []Event{}
	authority := &recordingLargeToolResultAuthority{}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: webfetch.Result{
				StatusCode: http.StatusNotFound, Body: strings.Repeat(`{"error":"missing"}`, 256), URL: "https://example.test/missing",
				SourceUnavailable: true, Error: "HTTP source returned 404 Not Found.",
			}}, nil
		}),
		MaxToolResultBytes: 512,
		LargeToolResults:   authority,
		OnEvent:            func(event Event) { events = append(events, event) },
	}

	result, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "verify the source"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "continued with another evidence source" {
		t.Fatalf("final = %#v", result.FinalMessage)
	}
	if len(authority.inputs) != 1 || authority.inputs[0].Outcome != ToolResultUnavailable {
		t.Fatalf("large unavailable authority inputs = %#v", authority.inputs)
	}
	var descriptor LargeToolResultDescriptor
	for _, message := range result.Messages {
		if message.Role == "tool" && message.ToolCallID == "missing-source" {
			if err := json.Unmarshal([]byte(message.Content), &descriptor); err != nil {
				t.Fatal(err)
			}
		}
	}
	if descriptor.Outcome != ToolResultUnavailable || descriptor.VersionID == "" {
		t.Fatalf("unavailable descriptor = %#v", descriptor)
	}
	if !hasEvent(events, EventToolCompleted, "web_fetch") || hasEvent(events, EventToolFailed, "web_fetch") {
		t.Fatalf("typed unavailable lifecycle events = %#v", events)
	}
	if descriptor.Outcome != ToolResultUnavailable || !descriptor.Truncated {
		t.Fatalf("typed unavailable descriptor missing from model context: %#v", result.Messages)
	}
}

func TestEngineContinuesAfterRecoverableWebSearchUnavailable(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "search-outage", Name: "WebSearch", Arguments: json.RawMessage(`{"query":"public evidence"}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "continued with a source-specific tool"}},
	}}
	events := []Event{}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"failure": map[string]any{
				"kind": "search_unavailable", "message": "search providers timed out", "recoverable": true,
			}}}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}

	result, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "find evidence"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "continued with a source-specific tool" {
		t.Fatalf("final = %#v", result.FinalMessage)
	}
	if !hasEvent(events, EventToolCompleted, "WebSearch") || hasEvent(events, EventToolFailed, "WebSearch") {
		t.Fatalf("recoverable search lifecycle events = %#v", events)
	}
}

func TestEngineDoesNotExternalizeInlineToolResult(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "small-1", Name: "Tool", Arguments: json.RawMessage(`{}`)}}}},
		{Message: Message{Role: "assistant", Content: "done"}},
	}}
	authority := &recordingLargeToolResultAuthority{}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"ok": true, "value": "small"}}, nil
		}),
		MaxToolResultBytes: 1024,
		LargeToolResults:   authority,
	}
	if _, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "small"}}}); err != nil {
		t.Fatal(err)
	}
	if len(authority.inputs) != 0 {
		t.Fatalf("inline result created large-result writes: %#v", authority.inputs)
	}
}

func TestEngineMarshalsOversizedToolResultExactlyOnce(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "marshal-once", Name: "Tool", Arguments: json.RawMessage(`{}`)}}}},
		{Message: Message{Role: "assistant", Content: "done"}},
	}}
	var marshalCalls atomic.Int64
	authority := &recordingLargeToolResultAuthority{}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: countingLargeJSONValue{calls: &marshalCalls}}, nil
		}),
		MaxToolResultBytes: 1024,
		LargeToolResults:   authority,
	}
	if _, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "once"}}}); err != nil {
		t.Fatal(err)
	}
	if marshalCalls.Load() != 1 || len(authority.inputs) != 1 || !json.Valid(authority.inputs[0].RawJSON) {
		t.Fatalf("marshal calls=%d authority inputs=%#v", marshalCalls.Load(), authority.inputs)
	}
}

func TestEngineCarriesVisualToolResultIntoTheNextModelRequest(t *testing.T) {
	model := &capturingMediaModelClient{}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{
				Value: map[string]any{"ok": true, "message": "image attached"},
				Parts: []ContentPart{{
					Type: ContentPartImage,
					Media: &MediaContent{
						MIMEType: "image/png", Filename: "figure.png",
						Source: MediaSource{Type: MediaSourceData, Data: tinyPNG},
					},
				}},
			}, nil
		}),
	}
	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "inspect the figure"}},
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "visual inspection complete" || len(model.requests) != 2 {
		t.Fatalf("result=%#v requests=%d", result.FinalMessage, len(model.requests))
	}
	messages := model.requests[1].Messages
	if len(messages) < 4 {
		t.Fatalf("second request messages=%#v", messages)
	}
	visual := messages[len(messages)-1]
	if visual.Role != "user" || len(visual.Parts) != 2 || visual.Parts[1].Type != ContentPartImage ||
		visual.Parts[1].Media == nil || string(visual.Parts[1].Media.Source.Data) != string(tinyPNG) {
		t.Fatalf("visual follow-up=%#v", visual)
	}
}

func TestEngineEnforcesMediaBudgetAcrossToolRounds(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "read-image-1", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"first.png"}`),
		}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "read-image-2", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"second.png"}`),
		}}}},
	}}
	engine := Engine{
		Model: model,
		Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Value: map[string]any{"ok": true}, Parts: []ContentPart{{
				Type: ContentPartImage,
				Media: &MediaContent{
					MIMEType: "image/png", Filename: "figure.png",
					Source: MediaSource{Type: MediaSourceData, Data: tinyPNG},
				},
			}}}, nil
		}),
	}
	_, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "inspect both figures"}},
		MaxToolRounds: 3,
		MediaPolicy: MediaPolicy{
			MaxPartBytes: int64(len(tinyPNG)), MaxTotalBytes: int64(len(tinyPNG) + 1),
		},
	})
	if err == nil || err.Error() != fmt.Sprintf("media content exceeds total limit of %d bytes", len(tinyPNG)+1) {
		t.Fatalf("Run() error = %v", err)
	}
	if calls := model.calls.Load(); calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
}

func TestOpenAIContentEncodesImageAndDocumentPartsWithoutLeakingPaths(t *testing.T) {
	content := openAIContentFromRuntime(Message{Role: "user", Parts: []ContentPart{
		{Type: ContentPartText, Text: "inspect"},
		{Type: ContentPartImage, Media: &MediaContent{
			MIMEType: "image/png", Filename: "figure.png", Source: MediaSource{Type: MediaSourceData, Data: tinyPNG},
		}},
		{Type: ContentPartDocument, Media: &MediaContent{
			MIMEType: "application/pdf", Filename: "paper.pdf", Source: MediaSource{Type: MediaSourceData, Data: []byte("%PDF-1.4\n%%EOF")},
		}},
	}})
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, required := range []string{`"type":"image_url"`, `data:image/png;base64,`, `"type":"file"`, `"filename":"paper.pdf"`, `data:application/pdf;base64,`} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("encoded content missing %q: %s", required, encoded)
		}
	}
	if strings.Contains(encoded, `"path"`) {
		t.Fatalf("encoded content leaked a local path: %s", encoded)
	}
}

type capturingMediaModelClient struct {
	requests []ModelRequest
}

func (client *capturingMediaModelClient) Complete(_ context.Context, request ModelRequest) (ModelResponse, error) {
	client.requests = append(client.requests, request)
	if len(client.requests) == 1 {
		return ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "read-image", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"figure.png"}`),
		}}}}, nil
	}
	return ModelResponse{Message: Message{Role: "assistant", Content: "visual inspection complete"}}, nil
}

type streamingStaticModelClient struct {
	deltas        []ModelStreamEvent
	response      ModelResponse
	completeCalls atomic.Int64
	streamCalls   atomic.Int64
}

func (c *streamingStaticModelClient) Complete(context.Context, ModelRequest) (ModelResponse, error) {
	c.completeCalls.Add(1)
	return ModelResponse{}, errors.New("non-streaming Complete called")
}

func (c *streamingStaticModelClient) CompleteStream(_ context.Context, _ ModelRequest, emit func(ModelStreamEvent) error) (ModelResponse, error) {
	c.streamCalls.Add(1)
	for _, delta := range c.deltas {
		if err := emit(delta); err != nil {
			return ModelResponse{}, err
		}
	}
	return c.response, nil
}

type staticModelClient struct {
	responses []ModelResponse
	calls     atomic.Int64
}

func (c *staticModelClient) Complete(context.Context, ModelRequest) (ModelResponse, error) {
	index := int(c.calls.Add(1)) - 1
	if index < 0 || index >= len(c.responses) {
		return ModelResponse{}, nil
	}
	return c.responses[index], nil
}

type capturingRequestModelClient struct {
	responses []ModelResponse
	requests  []ModelRequest
}

type expandingToolGateway struct {
	loaded   bool
	executed []string
}

func (gateway *expandingToolGateway) Execute(_ context.Context, call ToolCall) (ToolResult, error) {
	gateway.executed = append(gateway.executed, call.Name)
	if call.Name == "skill" {
		gateway.loaded = true
	}
	return ToolResult{Value: map[string]any{"ok": true}}, nil
}

func (gateway *expandingToolGateway) AdditionalModelToolSchemas(_ []ToolSchema) []ToolSchema {
	if !gateway.loaded {
		return nil
	}
	return []ToolSchema{{Name: "deep_search", Parameters: map[string]any{"type": "object"}}}
}

func TestEngineExpandsExactToolSchemasAfterSuccessfulDiscoveryCall(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "load-skill", Name: "skill", Arguments: json.RawMessage(`{"skill":"literature-review"}`),
		}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "deep-search", Name: "deep_search", Arguments: json.RawMessage(`{}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "done"}},
	}}
	gateway := &expandingToolGateway{}
	result, err := (Engine{Model: model, Tools: gateway}).Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "research"}}, Tools: []ToolSchema{{Name: "skill"}},
		MaxToolRounds: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage.Content != "done" || len(model.requests) != 3 {
		t.Fatalf("result=%#v requests=%d", result, len(model.requests))
	}
	if !toolSchemaNamedFolded(model.requests[1].Tools, "deep_search") {
		t.Fatalf("second request tools=%#v", model.requests[1].Tools)
	}
	if strings.Join(gateway.executed, ",") != "skill,deep_search" {
		t.Fatalf("executed=%v", gateway.executed)
	}
}

func (c *capturingRequestModelClient) Complete(_ context.Context, request ModelRequest) (ModelResponse, error) {
	c.requests = append(c.requests, request)
	index := len(c.requests) - 1
	if index < 0 || index >= len(c.responses) {
		return ModelResponse{}, nil
	}
	return c.responses[index], nil
}

type recordingLargeToolResultAuthority struct {
	inputs []LargeToolResultInput
}

func (a *recordingLargeToolResultAuthority) Externalize(_ context.Context, input LargeToolResultInput) (LargeToolResultDescriptor, error) {
	input.RawJSON = append([]byte(nil), input.RawJSON...)
	a.inputs = append(a.inputs, input)
	digest := sha256.Sum256(input.RawJSON)
	preview := input.RawJSON
	if len(preview) > 96 {
		preview = preview[:96]
		for len(preview) > 0 && !utf8.Valid(preview) {
			preview = preview[:len(preview)-1]
		}
	}
	return LargeToolResultDescriptor{
		ArtifactID:  "artifact-" + input.ToolCall.ID,
		VersionID:   "version-" + input.ToolCall.ID,
		SHA256:      hex.EncodeToString(digest[:]),
		SizeBytes:   int64(len(input.RawJSON)),
		ContentType: "application/json",
		Outcome:     input.Outcome,
		ContentURL:  "/api/artifacts/artifact-" + input.ToolCall.ID + "/versions/version-" + input.ToolCall.ID,
		Preview:     string(preview),
		Truncated:   len(preview) < len(input.RawJSON),
	}, nil
}

type countingLargeJSONValue struct {
	calls *atomic.Int64
}

func (value countingLargeJSONValue) MarshalJSON() ([]byte, error) {
	value.calls.Add(1)
	return []byte(`{"ok":true,"result":"` + strings.Repeat("marshal-once-", 256) + `"}`), nil
}

func hasOpenAIToolNamed(tools []any, name string) bool {
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		function, ok := tool["function"].(map[string]any)
		if ok && function["name"] == name {
			return true
		}
	}
	return false
}

func hasEvent(events []Event, typ EventType, toolName string) bool {
	for _, event := range events {
		if event.Type != typ {
			continue
		}
		if toolName == "" || event.ToolName == toolName {
			return true
		}
	}
	return false
}

func hasRejectedBeforeExecutionEvent(events []Event, toolName string) bool {
	for _, event := range events {
		if event.Type == EventToolCompleted && event.ToolName == toolName && event.RejectedBeforeExecution {
			return true
		}
	}
	return false
}

func TestOpenAIChatClientRejectsOversizedResponse(t *testing.T) {
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("x", maxOpenAIChatResponseBytes+1)))
	}))
	defer modelAPI.Close()

	client := OpenAIChatClient{Endpoint: modelAPI.URL, Model: "smoke-model", MaxAttempts: 1}
	_, err := client.Complete(t.Context(), ModelRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("Complete() error = %v", err)
	}
}

func TestOpenAIChatClientPreservesResponseMetadataAndUsage(t *testing.T) {
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"metadata-request",
			"model":"metadata-model",
			"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":13,"completion_tokens":5,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":8}}
		}`))
	}))
	defer modelAPI.Close()

	client := OpenAIChatClient{Endpoint: modelAPI.URL, Model: "configured-model", MaxAttempts: 1}
	response, err := client.Complete(t.Context(), ModelRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Model != "metadata-model" || response.RequestID != "metadata-request" || response.StopReason != "stop" ||
		response.Usage.InputTokens != 13 || response.Usage.OutputTokens != 5 ||
		response.Usage.CacheReadTokens != 8 || response.Usage.TotalTokens != 18 {
		t.Fatalf("response metadata = %#v", response)
	}
}

func TestToolExposureKeepsEveryDirectSchemaWithoutHiddenOverflow(t *testing.T) {
	tools := make([]ToolSchema, 0, 96)
	for index := 0; index < 96; index++ {
		tools = append(tools, ToolSchema{Name: fmt.Sprintf("mcp__fixture__tool_%02d", index)})
	}
	active := appendUniqueToolSchemas(nil, tools...)
	if len(active) != len(tools) || !toolSchemaNamedFolded(active, "mcp__fixture__tool_95") {
		t.Fatalf("direct schemas were truncated: got=%d want=%d", len(active), len(tools))
	}
}

func TestNormalizeModelStreamEventRequiresOneExplicitSemanticKind(t *testing.T) {
	legacy, err := normalizeModelStreamEvent(ModelStreamEvent{ContentDelta: "visible"})
	if err != nil || legacy.Kind != ModelStreamEventContentDelta {
		t.Fatalf("legacy content event=%#v err=%v", legacy, err)
	}
	boundary, err := normalizeModelStreamEvent(ModelStreamEvent{Kind: ModelStreamEventToolCallBoundary})
	if err != nil || boundary.Kind != ModelStreamEventToolCallBoundary {
		t.Fatalf("tool boundary=%#v err=%v", boundary, err)
	}
	public, err := normalizeModelStreamEvent(ModelStreamEvent{
		Kind: ModelStreamEventPublicProgressDelta, BlockID: "progress-1", ContentDelta: "visible progress",
	})
	if err != nil || public.Kind != ModelStreamEventPublicProgressDelta {
		t.Fatalf("public progress=%#v err=%v", public, err)
	}
	publicBoundary, err := normalizeModelStreamEvent(ModelStreamEvent{
		Kind: ModelStreamEventPublicProgressBoundary, BlockID: "progress-1",
	})
	if err != nil || publicBoundary.Kind != ModelStreamEventPublicProgressBoundary {
		t.Fatalf("public progress boundary=%#v err=%v", publicBoundary, err)
	}
	for _, event := range []ModelStreamEvent{
		{},
		{Kind: ModelStreamEventContentDelta},
		{Kind: ModelStreamEventContentDelta, BlockID: "unexpected", ContentDelta: "candidate"},
		{Kind: ModelStreamEventPublicProgressDelta, BlockID: "progress-1"},
		{Kind: ModelStreamEventPublicProgressDelta, BlockID: "../unsafe", ContentDelta: "progress"},
		{Kind: ModelStreamEventPublicProgressBoundary, BlockID: "progress-1", ContentDelta: "leak"},
		{Kind: ModelStreamEventToolCallBoundary, ContentDelta: "leak"},
		{Kind: ModelStreamEventPrivateReasoning, ReasoningActive: false},
		{Kind: "unknown"},
	} {
		if _, err := normalizeModelStreamEvent(event); err == nil {
			t.Fatalf("invalid stream event was accepted: %#v", event)
		}
	}
}

func TestCompleteModelRoundBoundsAndCoalescesBufferedSemanticEvents(t *testing.T) {
	coalescedModel := &streamingStaticModelClient{
		deltas: []ModelStreamEvent{
			{Kind: ModelStreamEventPrivateReasoning, ReasoningActive: true},
			{Kind: ModelStreamEventPrivateReasoning, ReasoningActive: true},
			{Kind: ModelStreamEventContentDelta, ContentDelta: "visible"},
			{Kind: ModelStreamEventToolCallBoundary},
			{Kind: ModelStreamEventToolCallBoundary},
		},
		response: ModelResponse{Message: Message{ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}}}},
	}
	_, buffered, err := (Engine{Model: coalescedModel}).completeModelRound(context.Background(), ModelRequest{}, true)
	if err != nil || len(buffered) != 3 || buffered[0].Kind != ModelStreamEventPrivateReasoning ||
		buffered[1].ContentDelta != "visible" || buffered[2].Kind != ModelStreamEventToolCallBoundary {
		t.Fatalf("buffered=%#v err=%v", buffered, err)
	}

	flood := make([]ModelStreamEvent, maxBufferedModelStreamEvents+1)
	for index := range flood {
		flood[index] = ModelStreamEvent{Kind: ModelStreamEventContentDelta, ContentDelta: "x"}
	}
	_, _, err = (Engine{Model: &streamingStaticModelClient{deltas: flood}}).completeModelRound(
		context.Background(), ModelRequest{}, true,
	)
	if err == nil || !strings.Contains(err.Error(), "event limit") {
		t.Fatalf("event flood error=%v", err)
	}
}
