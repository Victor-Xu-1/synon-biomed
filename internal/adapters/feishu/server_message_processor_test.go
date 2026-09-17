package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
)

func TestServerMessageProcessorStreamsFinalizesAndDispatchesOutboundImage(t *testing.T) {
	tmp := t.TempDir()
	imagePath := filepath.Join(tmp, "plot.png")
	if err := os.WriteFile(imagePath, []byte("PNGDATA"), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	var mu sync.Mutex
	var calls []cardKitCall
	var uploadedImagePayload string
	var sentImageKey string

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			body := decodeJSONBody(t, r)
			calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-processor"}}`))
		case "/open-apis/im/v1/messages":
			body := decodeJSONBody(t, r)
			calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
			if body["msg_type"] == "image" {
				var content map[string]string
				if err := json.Unmarshal([]byte(body["content"].(string)), &content); err != nil {
					t.Fatalf("decode image content: %v", err)
				}
				sentImageKey = content["image_key"]
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-processor"}}`))
		case "/open-apis/cardkit/v1/cards/card-processor/settings":
			body := decodeJSONBody(t, r)
			calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-processor":
			body := decodeJSONBody(t, r)
			calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/im/v1/images":
			uploadedImagePayload = requireMultipartFilePayload(t, r, "image", "PNGDATA")
			_, _ = w.Write([]byte(`{"code":0,"data":{"image_key":"img-processor"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	cardClient := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	mediaClient := NewMediaClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	processor := NewServerMessageProcessor(cardClient, NewOutboundMediaDispatcher(mediaClient))

	ctx := context.Background()
	if err := processor.Handle(ctx, "oc_chat_1", adaptercommon.ServerMessage{"type": "content_start", "blockType": "text"}); err != nil {
		t.Fatalf("content_start error = %v", err)
	}
	if err := processor.Handle(ctx, "oc_chat_1", adaptercommon.ServerMessage{"type": "content_delta", "text": "hello ![plot](" + imagePath + ")"}); err != nil {
		t.Fatalf("content_delta error = %v", err)
	}
	if err := processor.Handle(ctx, "oc_chat_1", adaptercommon.ServerMessage{"type": "message_complete"}); err != nil {
		t.Fatalf("message_complete error = %v", err)
	}
	if err := processor.WaitForMedia(context.Background()); err != nil {
		t.Fatalf("WaitForMedia() error = %v", err)
	}

	if uploadedImagePayload != "PNGDATA" || sentImageKey != "img-processor" {
		t.Fatalf("uploadedImagePayload=%q sentImageKey=%q", uploadedImagePayload, sentImageKey)
	}
	if processor.HasActiveCard("oc_chat_1") {
		t.Fatal("processor kept active card after message_complete")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 4 {
		t.Fatalf("card calls = %#v", calls)
	}
	finalCall, ok := findCall(calls, http.MethodPut, "/open-apis/cardkit/v1/cards/card-processor")
	if !ok {
		t.Fatalf("missing final card update in calls = %#v", calls)
	}
	finalCard := decodeCardUpdate(t, finalCall.Body)
	if finalText := cardBodyText(t, finalCard); !strings.Contains(finalText, "hello") {
		t.Fatalf("final card text = %q", finalText)
	}
}

func TestServerMessageProcessorReportsFeishuDeliveryPolicy(t *testing.T) {
	tmp := t.TempDir()
	imagePath := filepath.Join(tmp, "plot.png")
	if err := os.WriteFile(imagePath, []byte("PNGDATA"), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	var sentImageKey string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_ = decodeJSONBody(t, r)
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-report"}}`))
		case "/open-apis/im/v1/messages":
			body := decodeJSONBody(t, r)
			if body["msg_type"] == "image" {
				var content map[string]string
				if err := json.Unmarshal([]byte(body["content"].(string)), &content); err != nil {
					t.Fatalf("decode image content: %v", err)
				}
				sentImageKey = content["image_key"]
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-report"}}`))
		case "/open-apis/im/v1/images":
			_ = requireMultipartFilePayload(t, r, "image", "PNGDATA")
			_, _ = w.Write([]byte(`{"code":0,"data":{"image_key":"img-report"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	cardClient := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	mediaClient := NewMediaClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	processor := NewServerMessageProcessor(cardClient, NewOutboundMediaDispatcher(mediaClient))

	report, err := processor.HandleWithReport(context.Background(), "oc_chat_report", adaptercommon.ServerMessage{
		"type":    "content_delta",
		"eventId": "evt-feishu-report",
		"text":    "hello ![plot](" + imagePath + ")",
	})
	if err != nil {
		t.Fatalf("HandleWithReport() error = %v", err)
	}
	if err := processor.WaitForMedia(context.Background()); err != nil {
		t.Fatalf("WaitForMedia() error = %v", err)
	}
	if sentImageKey != "img-report" {
		t.Fatalf("sent image key = %q", sentImageKey)
	}
	if report.Platform != "feishu" || report.MessageKey != "eventId:evt-feishu-report" || report.RenderMode != "feishu-cardkit-stream" {
		t.Fatalf("report identity = %#v", report)
	}
	if report.Chunks != 1 || report.TextParts != 1 || report.CardUpdates != 1 || report.MediaUploads != 1 || report.PlatformCalls != 2 || report.DeliverySummary != "delivered" {
		t.Fatalf("feishu report = %#v", report)
	}
	if report.Policy.Platform != "feishu" || report.Policy.RenderMode != report.RenderMode {
		t.Fatalf("feishu report policy identity = %#v", report.Policy)
	}
	if !report.Policy.SupportsMarkdown || !report.Policy.SupportsMedia || !report.Policy.SupportsCards || report.Policy.MediaStrategy != "cardkit-stream-plus-media-dispatcher" {
		t.Fatalf("feishu report policy capabilities = %#v", report.Policy)
	}
	if safety := strings.Join(report.Policy.Safety, "|"); !strings.Contains(safety, "cardkit-table-sanitization") || !strings.Contains(safety, "async-media-error-collection") {
		t.Fatalf("feishu report policy safety = %#v", report.Policy.Safety)
	}
}

func findCall(calls []cardKitCall, method string, path string) (cardKitCall, bool) {
	for _, call := range calls {
		if call.Method == method && call.Path == path {
			return call, true
		}
	}
	return cardKitCall{}, false
}

func TestServerMessageProcessorTracksReasoningToolStepsAndAbortsOnError(t *testing.T) {
	var mu sync.Mutex
	var calls []cardKitCall

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body := decodeJSONBody(t, r)
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-error"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-error"}}`))
		case "/open-apis/cardkit/v1/cards/card-error/elements/streaming_content/content":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-error/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-error":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	cardClient := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	processor := NewServerMessageProcessor(cardClient, nil)
	ctx := context.Background()
	for _, msg := range []adaptercommon.ServerMessage{
		{"type": "content_start", "blockType": "text"},
		{"type": "content_delta", "text": "partial answer"},
		{"type": "thinking", "text": "checking source"},
		{"type": "content_start", "blockType": "tool_use", "toolUseId": "tool-1", "toolName": "web_fetch"},
		{"type": "tool_use_complete", "toolUseId": "tool-1", "toolName": "web_fetch"},
		{"type": "error", "message": "upstream failed"},
	} {
		if err := processor.Handle(ctx, "oc_chat_2", msg); err != nil {
			t.Fatalf("Handle(%v) error = %v", msg, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 4 {
		t.Fatalf("calls = %#v", calls)
	}
	finalCall := calls[len(calls)-1]
	errorCard := decodeCardUpdate(t, finalCall.Body)
	text := cardBodyText(t, errorCard)
	if !strings.Contains(text, "upstream failed") || !strings.Contains(text, "partial answer") {
		t.Fatalf("error card text = %q", text)
	}
	if processor.HasActiveCard("oc_chat_2") {
		t.Fatal("processor kept active card after error")
	}
}

func TestServerMessageProcessorStartsOneCardFromReasoningAndKeepsSingleLifecycle(t *testing.T) {
	var mu sync.Mutex
	var calls []cardKitCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeJSONBody(t, r)
		mu.Lock()
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
		mu.Unlock()

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-single-lifecycle"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-single-lifecycle"}}`))
		case "/open-apis/cardkit/v1/cards/card-single-lifecycle/elements/streaming_content/content":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-single-lifecycle/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-single-lifecycle":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	processor := NewServerMessageProcessor(client, nil)
	ctx := context.Background()

	if err := processor.Handle(ctx, "oc_single_lifecycle", adaptercommon.ServerMessage{
		"type": "thinking", "text": "正在规划完整答复",
	}); err != nil {
		t.Fatalf("thinking error = %v", err)
	}
	if !processor.HasActiveCard("oc_single_lifecycle") {
		t.Fatal("reasoning did not start the streaming card")
	}
	for _, msg := range []adaptercommon.ServerMessage{
		{"type": "content_start", "blockType": "tool_use", "toolUseId": "tool-1", "toolName": "web_fetch"},
		{"type": "tool_result", "toolUseId": "tool-1", "toolName": "web_fetch", "is_error": true},
		{"type": "content_delta", "text": "这是"},
		{"type": "content_delta", "text": "完整答复"},
	} {
		if err := processor.Handle(ctx, "oc_single_lifecycle", msg); err != nil {
			t.Fatalf("Handle(%v) error = %v", msg, err)
		}
	}
	time.Sleep(CardKitThrottle + 100*time.Millisecond)
	if err := processor.Handle(ctx, "oc_single_lifecycle", adaptercommon.ServerMessage{"type": "message_complete"}); err != nil {
		t.Fatalf("message_complete error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	messageCalls := filterCalls(calls, http.MethodPost, "/open-apis/im/v1/messages")
	if len(messageCalls) != 1 {
		t.Fatalf("single lifecycle created %d messages: %#v", len(messageCalls), cardKitCallNames(calls))
	}
	streamCalls := filterCalls(calls, http.MethodPut, "/open-apis/cardkit/v1/cards/card-single-lifecycle/elements/streaming_content/content")
	if len(streamCalls) == 0 {
		t.Fatal("missing process-state streaming frame")
	}
	processText, _ := streamCalls[len(streamCalls)-1].Body["content"].(string)
	if !strings.Contains(processText, "正在规划完整答复") || !strings.Contains(processText, "❌ web_fetch") {
		t.Fatalf("process frame = %q", processText)
	}
	finalCalls := filterCalls(calls, http.MethodPut, "/open-apis/cardkit/v1/cards/card-single-lifecycle")
	if len(finalCalls) != 1 {
		t.Fatalf("final update calls = %#v", cardKitCallNames(calls))
	}
	finalText := cardBodyText(t, decodeCardUpdate(t, finalCalls[0].Body))
	if finalText != "这是完整答复" {
		t.Fatalf("final text = %q", finalText)
	}
}

func decodeJSONBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode json body for %s: %v body=%s", r.URL.Path, err, raw)
	}
	return body
}

func waitUntil(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
