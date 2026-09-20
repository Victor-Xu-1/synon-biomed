package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRequiredToolStreamFragmentationDoesNotLimitValidResponse(t *testing.T) {
	const fragments = 70000
	deltas := make([]ModelStreamEvent, fragments)
	for index := range deltas {
		deltas[index] = ModelStreamEvent{Kind: ModelStreamEventContentDelta, ContentDelta: "字"}
	}
	want := strings.Repeat("字", fragments)
	model := &streamingStaticModelClient{deltas: deltas, response: ModelResponse{Message: Message{Content: want, ToolCalls: []ToolCall{{ID: "tool-1", Name: "lookup"}}}}}
	_, events, err := (Engine{Model: model}).completeModelRound(context.Background(), ModelRequest{}, true)
	if err != nil {
		t.Fatalf("valid response failed because of provider fragmentation: %v", err)
	}
	var text strings.Builder
	for _, event := range events {
		if !utf8.ValidString(event.ContentDelta) {
			t.Fatal("coalescing split a Unicode character")
		}
		text.WriteString(event.ContentDelta)
	}
	if text.String() != want || len(events) > 8 {
		t.Fatalf("unbounded or incomplete buffered response: events=%d bytes=%d", len(events), text.Len())
	}
}

func TestRequiredToolStreamStillEnforcesBytesAndCancellation(t *testing.T) {
	model := &streamingStaticModelClient{deltas: []ModelStreamEvent{{ContentDelta: strings.Repeat("x", maxOpenAIChatResponseBytes+1)}}}
	if _, events, err := (Engine{Model: model}).completeModelRound(context.Background(), ModelRequest{}, true); err == nil || len(events) != 0 {
		t.Fatal("oversized response escaped its byte budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	model.deltas = []ModelStreamEvent{{ContentDelta: "valid"}}
	if _, events, err := (Engine{Model: model}).completeModelRound(ctx, ModelRequest{}, true); !errors.Is(err, context.Canceled) || len(events) != 0 {
		t.Fatalf("cancellation lost: %v", err)
	}
}
