package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamingCardCardKitLifecycleStreamsAndFinalizes(t *testing.T) {
	var calls []cardKitCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body for %s: %v", r.URL.Path, err)
		}
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-stream"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-stream"}}`))
		case "/open-apis/cardkit/v1/cards/card-stream/elements/streaming_content/content":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-stream/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-stream":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-1", "")

	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	card.AppendText("hello")
	if err := card.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow(first) error = %v", err)
	}
	card.AppendText(" world")
	if err := card.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow(second) error = %v", err)
	}
	if err := card.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}

	if card.Phase() != StreamingCardCompleted {
		t.Fatalf("phase = %s", card.Phase())
	}
	if card.CardID() != "card-stream" || card.MessageID() != "msg-stream" {
		t.Fatalf("card ids = %q %q", card.CardID(), card.MessageID())
	}

	got := cardKitCallNames(calls)
	want := []string{
		"POST /open-apis/cardkit/v1/cards",
		"POST /open-apis/im/v1/messages?receive_id_type=chat_id",
		"PUT /open-apis/cardkit/v1/cards/card-stream/elements/streaming_content/content",
		"PUT /open-apis/cardkit/v1/cards/card-stream/elements/streaming_content/content",
		"PUT /open-apis/cardkit/v1/cards/card-stream/settings",
		"PUT /open-apis/cardkit/v1/cards/card-stream",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %#v", got)
	}

	if calls[2].Body["content"] != "hello" || calls[2].Body["sequence"] != float64(2) {
		t.Fatalf("first stream body = %#v", calls[2].Body)
	}
	if calls[3].Body["content"] != "hello world" || calls[3].Body["sequence"] != float64(3) {
		t.Fatalf("second stream body = %#v", calls[3].Body)
	}
	settings := decodeJSONStringField(t, calls[4].Body["settings"])
	if settings["streaming_mode"] != false || calls[4].Body["sequence"] != float64(4) {
		t.Fatalf("settings body = %#v decoded=%#v", calls[4].Body, settings)
	}
	updateCard := decodeCardUpdate(t, calls[5].Body)
	finalElement := firstCardElement(t, updateCard)
	if finalElement["tag"] != "markdown" || finalElement["element_id"] != StreamingElementID {
		t.Fatalf("final CardKit update changed the streaming element = %#v", finalElement)
	}
	if _, found := finalElement["text"]; found {
		t.Fatalf("final CardKit update must not replace markdown with div/lark_md = %#v", finalElement)
	}
	finalText := cardBodyText(t, updateCard)
	if !strings.Contains(finalText, "hello world") {
		t.Fatalf("final card text = %q", finalText)
	}
	if calls[5].Body["sequence"] != float64(5) {
		t.Fatalf("final update sequence = %#v", calls[5].Body)
	}
}

func TestStreamingCardFinalCardKitUpdateFallsBackToOriginalMessagePatch(t *testing.T) {
	var calls []cardKitCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeJSONBody(t, r)
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-final-patch"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-final-patch"}}`))
		case "/open-apis/cardkit/v1/cards/card-final-patch/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-final-patch":
			_, _ = w.Write([]byte(`{"code":230099,"msg":"final CardKit update rejected"}`))
		case "/open-apis/im/v1/messages/msg-final-patch":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-final-patch", "")
	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	card.AppendText("完整最终答复")
	if err := card.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if card.Phase() != StreamingCardCompleted {
		t.Fatalf("phase = %s", card.Phase())
	}

	patchCalls := filterCalls(calls, http.MethodPatch, "/open-apis/im/v1/messages/msg-final-patch")
	if len(patchCalls) != 1 {
		t.Fatalf("final patch calls = %#v", cardKitCallNames(calls))
	}
	patchedCard := decodeJSONStringField(t, patchCalls[0].Body["content"])
	if text := cardBodyText(t, patchedCard); !strings.Contains(text, "完整最终答复") {
		t.Fatalf("patched final card text = %q", text)
	}
}

func TestStreamingCardFinalizationSendsOneCompleteFallbackCard(t *testing.T) {
	var calls []cardKitCall
	messageCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeJSONBody(t, r)
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-final-fallback"}}`))
		case "/open-apis/im/v1/messages":
			messageCalls++
			if messageCalls == 1 {
				_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-final-fallback"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-complete-fallback"}}`))
		case "/open-apis/cardkit/v1/cards/card-final-fallback/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-final-fallback":
			_, _ = w.Write([]byte(`{"code":230099,"msg":"final CardKit update rejected"}`))
		case "/open-apis/im/v1/messages/msg-final-fallback":
			_, _ = w.Write([]byte(`{"code":230099,"msg":"final patch rejected"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-final-fallback", "")
	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	card.AppendText("完整补发答复")
	if err := card.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if card.Phase() != StreamingCardCompleted {
		t.Fatalf("phase = %s", card.Phase())
	}

	messageRequests := filterCalls(calls, http.MethodPost, "/open-apis/im/v1/messages")
	if len(messageRequests) != 2 {
		t.Fatalf("message calls = %#v", cardKitCallNames(calls))
	}
	fallbackCard := decodeJSONStringField(t, messageRequests[1].Body["content"])
	if text := cardBodyText(t, fallbackCard); !strings.Contains(text, "完整补发答复") {
		t.Fatalf("complete fallback card text = %q", text)
	}
}

func TestStreamingCardFailedFinalizationRemainsRetryable(t *testing.T) {
	messageCalls := 0
	patchCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = decodeJSONBody(t, r)
		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-final-retry"}}`))
		case "/open-apis/im/v1/messages":
			messageCalls++
			if messageCalls == 1 {
				_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-final-retry"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":230099,"msg":"fallback send rejected"}`))
		case "/open-apis/cardkit/v1/cards/card-final-retry/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-final-retry":
			_, _ = w.Write([]byte(`{"code":230099,"msg":"final CardKit update rejected"}`))
		case "/open-apis/im/v1/messages/msg-final-retry":
			patchCalls++
			if patchCalls == 1 {
				_, _ = w.Write([]byte(`{"code":230099,"msg":"first final patch rejected"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-final-retry", "")
	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	card.AppendText("可重试最终答复")
	if err := card.Finalize(context.Background()); err == nil {
		t.Fatal("first Finalize() unexpectedly succeeded")
	}
	if card.Phase() == StreamingCardCompleted {
		t.Fatal("failed finalization was falsely marked completed")
	}
	if err := card.Finalize(context.Background()); err != nil {
		t.Fatalf("second Finalize() error = %v", err)
	}
	if card.Phase() != StreamingCardCompleted {
		t.Fatalf("phase after retry = %s", card.Phase())
	}
}

func TestStreamingCardDeduplicatesReplayedAndOverlappingText(t *testing.T) {
	var finalCard map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeJSONBody(t, r)
		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-dedupe"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-dedupe"}}`))
		case "/open-apis/cardkit/v1/cards/card-dedupe/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-dedupe":
			finalCard = decodeCardUpdate(t, body)
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-dedupe", "")
	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	card.AppendText("hello")
	card.AppendText("hello")
	card.AppendText("hello world")
	card.AppendText(" world")
	if err := card.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if finalCard == nil {
		t.Fatal("missing final card")
	}
	if text := cardBodyText(t, finalCard); text != "hello world" {
		t.Fatalf("deduplicated final text = %q", text)
	}
}

func TestStreamingCardReasoningPreviewShowsLatestTail(t *testing.T) {
	got := truncateReasoningPreview("先检查输入，再运行工具，最后核对结果", 9)
	if !strings.HasPrefix(got, "...") || !strings.HasSuffix(got, "最后核对结果") {
		t.Fatalf("reasoning preview = %q", got)
	}
	if strings.Contains(got, "先检查输入") {
		t.Fatalf("reasoning preview kept stale prefix = %q", got)
	}
}

func TestStreamingCardToolReplayDoesNotRegressCompletedStatus(t *testing.T) {
	card := NewStreamingCard(nil, "chat-tool-replay", "")
	card.StartTool("tool-1", "web_fetch")
	card.CompleteTool("tool-1", "web_fetch")
	card.StartTool("tool-1", "web_fetch")

	text := card.renderedText()
	if !strings.Contains(text, "✅ web_fetch") || strings.Contains(text, "⚙️ web_fetch") {
		t.Fatalf("replayed tool status = %q", text)
	}
}

func TestStreamingCardAppendDoesNotBlockBehindNetworkFlush(t *testing.T) {
	streamStarted := make(chan struct{})
	releaseStream := make(chan struct{})
	var blockOnce sync.Once
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = decodeJSONBody(t, r)
		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-responsive"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-responsive"}}`))
		case "/open-apis/cardkit/v1/cards/card-responsive/elements/streaming_content/content":
			blockOnce.Do(func() {
				close(streamStarted)
				<-releaseStream
			})
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-responsive/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-responsive":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-responsive", "")
	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	card.AppendText("first")
	flushDone := make(chan error, 1)
	go func() { flushDone <- card.FlushNow(context.Background()) }()
	<-streamStarted

	appendDone := make(chan struct{})
	go func() {
		card.AppendText(" second")
		close(appendDone)
	}()
	select {
	case <-appendDone:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("AppendText blocked behind a slow Feishu network request")
	}
	close(releaseStream)
	if err := <-flushDone; err != nil {
		t.Fatalf("FlushNow() error = %v", err)
	}
	if err := card.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
}

func TestStreamingCardFallsBackToPatchWhenCardKitCreateFails(t *testing.T) {
	var calls []cardKitCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body for %s: %v", r.URL.Path, err)
		}
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":99991672,"msg":"permission denied"}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-fallback"}}`))
		case "/open-apis/im/v1/messages/msg-fallback":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-1", "")

	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	if card.CardID() != "" || card.MessageID() != "msg-fallback" || card.CardKitStreamActive() {
		t.Fatalf("fallback state cardID=%q messageID=%q active=%v", card.CardID(), card.MessageID(), card.CardKitStreamActive())
	}
	card.AppendText("fallback text")
	if err := card.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow() error = %v", err)
	}
	if err := card.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}

	got := cardKitCallNames(calls)
	want := []string{
		"POST /open-apis/cardkit/v1/cards",
		"POST /open-apis/im/v1/messages?receive_id_type=chat_id",
		"PATCH /open-apis/im/v1/messages/msg-fallback",
		"PATCH /open-apis/im/v1/messages/msg-fallback",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %#v", got)
	}
	patchCard := decodeJSONStringField(t, calls[2].Body["content"])
	if text := cardBodyText(t, patchCard); !strings.Contains(text, "fallback text") {
		t.Fatalf("patch card text = %q", text)
	}
}

func TestStreamingCardRateLimitSkipsFrameAndRetriesNextFlush(t *testing.T) {
	var calls []cardKitCall
	streamCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body for %s: %v", r.URL.Path, err)
		}
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-rate"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-rate"}}`))
		case "/open-apis/cardkit/v1/cards/card-rate/elements/streaming_content/content":
			streamCalls++
			if streamCalls == 1 {
				_, _ = w.Write([]byte(`{"code":230020,"msg":"qps limit"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-1", "")
	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	card.AppendText("rate limited")
	if err := card.FlushNow(context.Background()); err != nil {
		t.Fatalf("first FlushNow() error = %v", err)
	}
	if !card.CardKitStreamActive() {
		t.Fatal("rate limit should not disable CardKit streaming")
	}
	if err := card.FlushNow(context.Background()); err != nil {
		t.Fatalf("retry FlushNow() error = %v", err)
	}

	contentCalls := filterCalls(calls, "PUT", "/open-apis/cardkit/v1/cards/card-rate/elements/streaming_content/content")
	if len(contentCalls) != 2 {
		t.Fatalf("stream calls = %d", len(contentCalls))
	}
	if contentCalls[1].Body["content"] != "rate limited" {
		t.Fatalf("retry stream body = %#v", contentCalls[1].Body)
	}
}

func TestStreamingCardTableLimitDisablesIntermediateStreamButFinalizesCardKit(t *testing.T) {
	var calls []cardKitCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body for %s: %v", r.URL.Path, err)
		}
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-table"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-table"}}`))
		case "/open-apis/cardkit/v1/cards/card-table/elements/streaming_content/content":
			_, _ = w.Write([]byte(`{"code":230099,"msg":"Failed; ErrCode: 11310; ErrMsg: card table number over limit;"}`))
		case "/open-apis/cardkit/v1/cards/card-table/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-table":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-1", "")
	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	card.AppendText("| a | b |\n|---|---|\n| 1 | 2 |")
	if err := card.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow() error = %v", err)
	}
	if card.CardKitStreamActive() {
		t.Fatal("table limit should disable intermediate CardKit streaming")
	}
	if card.CardID() != "card-table" {
		t.Fatalf("card id after table limit = %q", card.CardID())
	}
	if err := card.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}

	if len(filterCalls(calls, "PATCH", "/open-apis/im/v1/messages/msg-table")) != 0 {
		t.Fatalf("table limit should not fall back to patch: %#v", cardKitCallNames(calls))
	}
	if len(filterCalls(calls, "PUT", "/open-apis/cardkit/v1/cards/card-table/settings")) != 1 ||
		len(filterCalls(calls, "PUT", "/open-apis/cardkit/v1/cards/card-table")) != 1 {
		t.Fatalf("finalize calls = %#v", cardKitCallNames(calls))
	}
}

func TestStreamingCardRendersReasoningToolsAndAnswerThenFinalizesCleanAnswer(t *testing.T) {
	var calls []cardKitCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body for %s: %v", r.URL.Path, err)
		}
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-process"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-process"}}`))
		case "/open-apis/cardkit/v1/cards/card-process/elements/streaming_content/content":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-process/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-process":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-1", "")
	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}

	card.AppendReasoning("Need to inspect the file before answering.")
	card.StartTool("tool-read-1", "Read")
	card.CompleteTool("tool-read-1", "Read")
	card.AppendText("## 答复\n\n这是最终答复正文。")
	if err := card.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow() error = %v", err)
	}

	contentCalls := filterCalls(calls, "PUT", "/open-apis/cardkit/v1/cards/card-process/elements/streaming_content/content")
	if len(contentCalls) == 0 {
		t.Fatal("missing streaming content call")
	}
	midFrame := contentCalls[len(contentCalls)-1].Body["content"].(string)
	toolIndex := strings.Index(midFrame, "Read")
	reasonIndex := strings.Index(midFrame, "思考中")
	answerIndex := strings.Index(midFrame, "最终答复正文")
	if toolIndex < 0 || reasonIndex < 0 || answerIndex < 0 {
		t.Fatalf("mid frame missing sections = %q", midFrame)
	}
	if !(toolIndex < reasonIndex && reasonIndex < answerIndex) {
		t.Fatalf("section order invalid = %q", midFrame)
	}
	if !strings.Contains(midFrame, "✅") || strings.Contains(midFrame, "⚙️") {
		t.Fatalf("completed tool marker invalid = %q", midFrame)
	}

	if err := card.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	updateCalls := filterCalls(calls, "PUT", "/open-apis/cardkit/v1/cards/card-process")
	if len(updateCalls) != 1 {
		t.Fatalf("update calls = %#v", cardKitCallNames(calls))
	}
	finalText := cardBodyText(t, decodeCardUpdate(t, updateCalls[0].Body))
	if !strings.Contains(finalText, "最终答复正文") {
		t.Fatalf("final text missing answer = %q", finalText)
	}
	for _, leaked := range []string{"思考中", "Need to inspect", "Read", "🛠️", "💭"} {
		if strings.Contains(finalText, leaked) {
			t.Fatalf("final text leaked %q: %q", leaked, finalText)
		}
	}
}

func TestStreamingCardAbortRendersErrorCard(t *testing.T) {
	var calls []cardKitCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body for %s: %v", r.URL.Path, err)
		}
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-abort"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-abort"}}`))
		case "/open-apis/cardkit/v1/cards/card-abort/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-abort":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	card := NewStreamingCard(client, "chat-1", "")
	if err := card.EnsureCreated(context.Background()); err != nil {
		t.Fatalf("EnsureCreated() error = %v", err)
	}
	card.AppendText("partial answer")

	if err := card.Abort(context.Background(), "tool failed"); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if card.Phase() != StreamingCardAborted {
		t.Fatalf("phase = %s", card.Phase())
	}
	if len(filterCalls(calls, "PUT", "/open-apis/cardkit/v1/cards/card-abort/settings")) != 1 {
		t.Fatalf("missing streaming-mode close call: %#v", cardKitCallNames(calls))
	}
	updateCalls := filterCalls(calls, "PUT", "/open-apis/cardkit/v1/cards/card-abort")
	if len(updateCalls) != 1 {
		t.Fatalf("missing error card update: %#v", cardKitCallNames(calls))
	}
	errorCard := decodeCardUpdate(t, updateCalls[0].Body)
	header := errorCard["header"].(map[string]any)
	if header["template"] != "red" {
		t.Fatalf("error card header = %#v", header)
	}
	errorText := cardBodyText(t, errorCard)
	if !strings.Contains(errorText, "tool failed") || !strings.Contains(errorText, "partial answer") {
		t.Fatalf("error card text = %q", errorText)
	}
}

func filterCalls(calls []cardKitCall, method string, path string) []cardKitCall {
	filtered := make([]cardKitCall, 0)
	for _, call := range calls {
		if call.Method == method && call.Path == path {
			filtered = append(filtered, call)
		}
	}
	return filtered
}

func decodeJSONStringField(t *testing.T, value any) map[string]any {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		t.Fatalf("value is not a JSON string: %#v", value)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("decode JSON string %q: %v", text, err)
	}
	return decoded
}

func decodeCardUpdate(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	card, ok := body["card"].(map[string]any)
	if !ok {
		t.Fatalf("update body missing card: %#v", body)
	}
	return decodeJSONStringField(t, card["data"])
}

func cardBodyText(t *testing.T, card map[string]any) string {
	t.Helper()
	body, ok := card["body"].(map[string]any)
	if !ok {
		t.Fatalf("card missing body: %#v", card)
	}
	elements, ok := body["elements"].([]any)
	if !ok || len(elements) == 0 {
		t.Fatalf("card missing elements: %#v", card)
	}
	first, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatalf("first element invalid: %#v", elements[0])
	}
	if content, ok := first["content"].(string); ok {
		return content
	}
	if text, ok := first["text"].(map[string]any); ok {
		if content, ok := text["content"].(string); ok {
			return content
		}
	}
	t.Fatalf("element has no text content: %#v", first)
	return ""
}
