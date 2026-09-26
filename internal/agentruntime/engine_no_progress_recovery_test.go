package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNoProgressRecoveryInstructionRequiresSuccessfulIdempotentReceipts(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"executed reuse", `{"ok":true,"reused":true}`, true},
		{"compact non-executing reuse", `{"ok":true,"executed":false,"reused":true}`, true},
		{"explicit unchanged effect", `{"ok":true,"executed":false,"effect":{"schema":"synon.tool_effect.v1","state":"unchanged"}}`, true},
		{"decision required", `{"ok":true,"executed":false,"decision_required":true}`, false},
		{"preflight required", `{"ok":true,"executed":false,"status":"implementation_selection_required"}`, false},
		{"failed receipt", `{"ok":false,"executed":false,"code":"edit_conflict"}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := noProgressReceiptsAreSuccessful([]Message{{Role: "tool", ToolCallID: "call-1", Content: test.content}})
			if got != test.want {
				t.Fatalf("successful idempotent receipt=%v want %v", got, test.want)
			}
		})
	}
	if noProgressReceiptsAreSuccessful([]Message{{Role: "assistant", Content: "no tool receipt"}}) {
		t.Fatal("a round without tool receipts was accepted as successful idempotent work")
	}
}

func TestNoProgressRecoveryUsesFullExecutionIdentity(t *testing.T) {
	longPrefix := strings.Repeat("shared-", 60)
	longArgs := func(suffix string) json.RawMessage {
		value, err := json.Marshal(map[string]any{"path": longPrefix + suffix})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, test := range []struct {
		name          string
		first, second json.RawMessage
		want          int
	}{
		{"different after display limit", longArgs("first"), longArgs("second"), 2},
		{"same semantic call with new label", json.RawMessage(`{"path":"source.txt","human_description":"first label"}`), json.RawMessage(`{"path":"source.txt","human_description":"second label"}`), 1},
		{"equivalent JSON order", json.RawMessage(`{"path":"source.txt","offset":1}`), json.RawMessage(`{ "offset": 1, "path": "source.txt" }`), 1},
		{"material offset change", json.RawMessage(`{"path":"source.txt","offset":1}`), json.RawMessage(`{"path":"source.txt","offset":2}`), 2},
		{"array order is material", json.RawMessage(`{"paths":["a","b"]}`), json.RawMessage(`{"paths":["b","a"]}`), 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := ToolCall{ID: "first", Name: "read_file", Arguments: test.first}
			second := ToolCall{ID: "second", Name: "read_file", Arguments: test.second}
			calls := appendNoProgressRecoveryCalls([]ToolCall{first}, []ToolCall{second})
			if len(calls) != test.want {
				t.Fatalf("retained=%d want%d", len(calls), test.want)
			}
			if calls[0].ID != first.ID || string(calls[0].Arguments) != string(test.first) {
				t.Fatal("recovery rewrote the original executed call")
			}
		})
	}
	if got := compactNoProgressArguments(longArgs("tail-sentinel")); len(got) > 243 || strings.Contains(got, "tail-sentinel") {
		t.Fatalf("display preview lost its bounded contract: %d bytes", len(got))
	}
}

type longIdentityNoProgressModel struct{ requests int }

func (m *longIdentityNoProgressModel) Complete(_ context.Context, _ ModelRequest) (ModelResponse, error) {
	m.requests++
	args, _ := json.Marshal(map[string]any{"url": "https://example.org/" + strings.Repeat("same/", 60) + fmt.Sprint(m.requests)})
	return ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
		ID: fmt.Sprintf("long-read-%d", m.requests), Name: "web_fetch", Arguments: args,
	}}}}, nil
}

func TestEngineRetainsDistinctLongNoProgressIdentities(t *testing.T) {
	model := &longIdentityNoProgressModel{}
	gateway := &reusedReadGateway{}
	_, err := (Engine{Model: model, Tools: gateway}).Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "Use the existing source receipts"}},
		Tools:    []ToolSchema{{Name: "web_fetch"}}, MaxConsecutiveIdenticalToolRounds: 3,
	})
	var noProgress *ToolRoundNoProgressError
	if !errors.As(err, &noProgress) {
		t.Fatalf("error=%v", err)
	}
	if len(noProgress.Calls) != 3 || model.requests != 3 || gateway.calls != 3 {
		t.Fatalf("recovery calls=%d requests=%d gateway=%d", len(noProgress.Calls), model.requests, gateway.calls)
	}
	seen := map[string]bool{}
	for _, call := range noProgress.Calls {
		seen[ExecutionCallFingerprint(call.Name, call.Arguments)] = true
	}
	if len(seen) != 3 {
		t.Fatalf("materially distinct identities collapsed: %d", len(seen))
	}
}
