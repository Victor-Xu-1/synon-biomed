package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPublicProgressEnvelopeDecoderHandlesArbitraryChunkBoundariesWithoutLeakingControlBytes(t *testing.T) {
	raw := "candidate-before " + publicProgressTestEnvelope("progress-1", "Checking source one. ") +
		publicProgressTestEnvelope("progress-2", "Checking source two. ") + "candidate-after"
	events := make([]ModelStreamEvent, 0)
	decoder := newPublicProgressEnvelopeDecoder(func(event ModelStreamEvent) error {
		events = append(events, event)
		return nil
	})
	for index := range raw {
		if err := decoder.write(raw[index : index+1]); err != nil {
			t.Fatalf("byte %d: %v", index, err)
		}
	}
	if err := decoder.finish(); err != nil {
		t.Fatal(err)
	}
	if decoder.envelopeCount() != 2 || decoder.candidateContent() != "candidate-before candidate-after" ||
		decoder.visibleContent() != "candidate-before Checking source one. Checking source two. candidate-after" {
		t.Fatalf("count=%d candidate=%q visible=%q", decoder.envelopeCount(), decoder.candidateContent(), decoder.visibleContent())
	}
	var candidate strings.Builder
	public := make([]string, 0, 2)
	boundaries := make([]string, 0, 2)
	for _, event := range events {
		switch event.Kind {
		case ModelStreamEventContentDelta:
			candidate.WriteString(event.ContentDelta)
		case ModelStreamEventPublicProgressDelta:
			public = append(public, event.BlockID+":"+event.ContentDelta)
		case ModelStreamEventPublicProgressBoundary:
			boundaries = append(boundaries, event.BlockID)
		}
	}
	if candidate.String() != decoder.candidateContent() || strings.Join(public, "|") !=
		"progress-1:Checking source one. |progress-2:Checking source two. " ||
		strings.Join(boundaries, "|") != "progress-1|progress-2" {
		t.Fatalf("candidate=%q public=%#v boundaries=%#v", candidate.String(), public, boundaries)
	}
	for _, event := range events {
		if strings.Contains(event.ContentDelta, "PublicProgress") {
			t.Fatalf("control marker leaked in %#v", event)
		}
	}
}

func TestPublicProgressEnvelopeDecoderRejectsMalformedUnsafeAndDuplicateBlocks(t *testing.T) {
	oversized := strings.Repeat("x", maxPublicProgressTextBytes+1)
	for name, raw := range map[string]string{
		"duplicate field": PublicProgressEnvelopeBegin + `{"version":1,"id":"a","id":"b","text":"ok"}` + PublicProgressEnvelopeEnd,
		"unknown field":   PublicProgressEnvelopeBegin + `{"version":1,"id":"a","text":"ok","extra":true}` + PublicProgressEnvelopeEnd,
		"wrong version":   PublicProgressEnvelopeBegin + `{"version":2,"id":"a","text":"ok"}` + PublicProgressEnvelopeEnd,
		"unsafe id":       publicProgressTestEnvelope("../escape", "ok"),
		"empty text":      publicProgressTestEnvelope("empty", "  "),
		"control text":    publicProgressTestEnvelope("control", "unsafe\x1btext"),
		"oversized text":  publicProgressTestEnvelope("large", oversized),
		"unmatched end":   "candidate" + PublicProgressEnvelopeEnd,
		"partial marker":  "candidate<|PublicProgress",
		"duplicate id": publicProgressTestEnvelope("same", "first") +
			publicProgressTestEnvelope("same", "first"),
	} {
		t.Run(name, func(t *testing.T) {
			decoder := newPublicProgressEnvelopeDecoder(nil)
			writeErr := decoder.write(raw)
			if writeErr == nil {
				writeErr = decoder.finish()
			}
			if writeErr == nil {
				t.Fatalf("malformed public progress was accepted: %q", raw)
			}
		})
	}
}

func TestCompleteModelRoundPublishesTypedProgressBeforeProviderReturnsAndKeepsFinalCandidatePrivate(t *testing.T) {
	progress := "正在核对真实证据。"
	envelope := publicProgressTestEnvelope("progress-live", progress)
	model := &blockingPublicProgressModel{
		raw: envelope + "最终候选。",
		response: ModelResponse{Message: Message{
			Role: "assistant", Content: envelope + "最终候选。",
		}},
		emitted: make(chan struct{}), release: make(chan struct{}),
	}
	observed := make(chan ModelStreamEvent, 4)
	engine := Engine{Model: model, OnModelDelta: func(event ModelStreamEvent) error {
		observed <- event
		return nil
	}}
	type outcome struct {
		response ModelResponse
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, _, err := engine.completeModelRound(context.Background(), ModelRequest{}, true)
		done <- outcome{response: response, err: err}
	}()
	select {
	case <-model.emitted:
	case <-time.After(time.Second):
		t.Fatal("provider did not emit the public envelope")
	}
	select {
	case event := <-observed:
		if event.Kind != ModelStreamEventPublicProgressDelta || event.BlockID != "progress-live" || event.ContentDelta != progress {
			t.Fatalf("first event=%#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("public progress was not observable before provider completion")
	}
	close(model.release)
	result := <-done
	if result.err != nil || result.response.Message.Content != "最终候选。" {
		t.Fatalf("response=%#v err=%v", result.response, result.err)
	}
	for len(observed) > 0 {
		if event := <-observed; event.Kind == ModelStreamEventContentDelta {
			t.Fatalf("final candidate escaped the required-tool buffer: %#v", event)
		}
	}
}

func TestCompleteModelRoundNormalizesNonStreamingPublicProgressWithoutLeakingEnvelopeMarkers(t *testing.T) {
	envelope := publicProgressTestEnvelope("progress-static", "Checking evidence. ")
	model := staticPublicProgressModel{response: ModelResponse{Message: Message{
		Role: "assistant", Content: envelope,
		ToolCalls: []ToolCall{{ID: "call-1", Name: "search", Arguments: json.RawMessage(`{"query":"evidence"}`)}},
	}}}
	var events []ModelStreamEvent
	engine := Engine{Model: model, OnModelDelta: func(event ModelStreamEvent) error {
		events = append(events, event)
		return nil
	}}
	response, _, err := engine.completeModelRound(context.Background(), ModelRequest{}, false)
	if err != nil || response.Message.Content != "Checking evidence. " || len(events) != 2 ||
		events[0].Kind != ModelStreamEventPublicProgressDelta || events[1].Kind != ModelStreamEventPublicProgressBoundary {
		t.Fatalf("response=%#v events=%#v err=%v", response, events, err)
	}
}

type staticPublicProgressModel struct{ response ModelResponse }

func (m staticPublicProgressModel) Complete(context.Context, ModelRequest) (ModelResponse, error) {
	return m.response, nil
}

type blockingPublicProgressModel struct {
	raw      string
	response ModelResponse
	emitted  chan struct{}
	release  chan struct{}
}

func (m *blockingPublicProgressModel) Complete(context.Context, ModelRequest) (ModelResponse, error) {
	return ModelResponse{}, errors.New("non-streaming path is not expected")
}

func (m *blockingPublicProgressModel) CompleteStream(
	_ context.Context,
	_ ModelRequest,
	emit func(ModelStreamEvent) error,
) (ModelResponse, error) {
	if err := emit(ModelStreamEvent{Kind: ModelStreamEventContentDelta, ContentDelta: m.raw}); err != nil {
		return ModelResponse{}, err
	}
	close(m.emitted)
	<-m.release
	return m.response, nil
}

func publicProgressTestEnvelope(id, text string) string {
	payload, err := json.Marshal(map[string]any{"version": 1, "id": id, "text": text})
	if err != nil {
		panic(err)
	}
	return PublicProgressEnvelopeBegin + string(payload) + PublicProgressEnvelopeEnd
}
