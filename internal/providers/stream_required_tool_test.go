package providers

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
)

type fragmentedStreamTestTool struct{ calls int }

func (tool *fragmentedStreamTestTool) Execute(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	if call.Name != "lookup" || string(call.Arguments) != "{}" {
		return agentruntime.ToolResult{}, fmt.Errorf("unexpected call: %s", call.Name)
	}
	tool.calls++
	return agentruntime.ToolResult{Value: map[string]any{"ok": true, "value": "observed"}}, nil
}

func TestEngineRequiredToolStreamManyFragments(t *testing.T) {
	const fragments = 70000
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writer := bufio.NewWriter(w)
		defer writer.Flush()
		if requests.Add(1) == 1 {
			for n := 0; n < fragments; n++ {
				if _, err := fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"字\"}}]}\n\n"); err != nil {
					return
				}
			}
			fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"lookup-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
		} else {
			fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "fragmented-stream", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL + "/v1/chat/completions"},
		Model:    "local-test", Request: RequestProfile{Timeout: 10 * time.Second, MaxAttempts: 1, MaxResponseBytes: 8 << 20},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tool := &fragmentedStreamTestTool{}
	var visible strings.Builder
	engine := agentruntime.Engine{Model: client, Tools: tool, OnModelDelta: func(event agentruntime.ModelStreamEvent) error { visible.WriteString(event.ContentDelta); return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := engine.Run(ctx, agentruntime.RunRequest{
		Messages:          []agentruntime.Message{{Role: "user", Content: "Inspect the source through the required tool."}},
		Tools:             []agentruntime.ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		InitialToolChoice: "required", MaxToolRounds: 2,
	})
	if err != nil || result.FinalMessage.Content != "done" || requests.Load() != 2 || tool.calls != 1 || visible.String() != strings.Repeat("字", fragments)+"done" {
		t.Fatalf("real SSE -> required-tool -> next model round failed: err=%v requests=%d calls=%d visible=%d final=%q", err, requests.Load(), tool.calls, visible.Len(), result.FinalMessage.Content)
	}
}
