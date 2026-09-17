package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCardKitClientCreatesSendsStreamsAndFinalizesCard(t *testing.T) {
	var calls []cardKitCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tenant-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body for %s: %v", r.URL.Path, err)
		}
		calls = append(calls, cardKitCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			if r.Method != http.MethodPost {
				t.Fatalf("create method = %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"card_id":"card-1"}}`))
		case "/open-apis/im/v1/messages":
			if r.URL.Query().Get("receive_id_type") != "chat_id" {
				t.Fatalf("receive_id_type = %q", r.URL.Query().Get("receive_id_type"))
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-1"}}`))
		case "/open-apis/cardkit/v1/cards/card-1/elements/streaming_content/content":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-1/settings":
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/open-apis/cardkit/v1/cards/card-1":
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	cardID, err := client.CreateCardEntity(context.Background(), map[string]any{"schema": "2.0"})
	if err != nil {
		t.Fatalf("CreateCardEntity() error = %v", err)
	}
	if cardID != "card-1" {
		t.Fatalf("cardID = %q", cardID)
	}
	messageID, err := client.SendCardAsMessage(context.Background(), "chat-1", cardID, "")
	if err != nil {
		t.Fatalf("SendCardAsMessage() error = %v", err)
	}
	if messageID != "msg-1" {
		t.Fatalf("messageID = %q", messageID)
	}
	if err := client.StreamCardContent(context.Background(), cardID, StreamingElementID, "hello", 42); err != nil {
		t.Fatalf("StreamCardContent() error = %v", err)
	}
	if err := client.SetCardStreamingMode(context.Background(), cardID, false, 43); err != nil {
		t.Fatalf("SetCardStreamingMode() error = %v", err)
	}
	if err := client.UpdateCardKitCard(context.Background(), cardID, map[string]any{"done": true}, 44); err != nil {
		t.Fatalf("UpdateCardKitCard() error = %v", err)
	}

	if got := cardKitCallNames(calls); len(got) != 5 ||
		got[0] != "POST /open-apis/cardkit/v1/cards" ||
		got[1] != "POST /open-apis/im/v1/messages?receive_id_type=chat_id" ||
		got[2] != "PUT /open-apis/cardkit/v1/cards/card-1/elements/streaming_content/content" ||
		got[3] != "PUT /open-apis/cardkit/v1/cards/card-1/settings" ||
		got[4] != "PUT /open-apis/cardkit/v1/cards/card-1" {
		t.Fatalf("calls = %#v", got)
	}
	if calls[0].Body["type"] != "card_json" || calls[0].Body["data"] == "" {
		t.Fatalf("create body = %#v", calls[0].Body)
	}
	if calls[1].Body["receive_id"] != "chat-1" || calls[1].Body["msg_type"] != "interactive" {
		t.Fatalf("send body = %#v", calls[1].Body)
	}
	if calls[2].Body["content"] != "hello" || calls[2].Body["sequence"] != float64(42) {
		t.Fatalf("stream body = %#v", calls[2].Body)
	}
	if calls[3].Body["sequence"] != float64(43) {
		t.Fatalf("settings body = %#v", calls[3].Body)
	}
	if calls[4].Body["sequence"] != float64(44) {
		t.Fatalf("update body = %#v", calls[4].Body)
	}
}

func TestCardKitClientSendsRenderedCardToOpenID(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/open-apis/im/v1/messages" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("receive_id_type"); got != "open_id" {
			t.Fatalf("receive_id_type = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["receive_id"] != "ou_live_user" || body["msg_type"] != "interactive" || body["content"] == "" {
			t.Fatalf("body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-open-id"}}`))
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	messageID, err := client.SendRenderedCardAsMessageTo(
		context.Background(),
		"open_id",
		"ou_live_user",
		map[string]any{"schema": "2.0"},
		"",
	)
	if err != nil {
		t.Fatalf("SendRenderedCardAsMessageTo() error = %v", err)
	}
	if messageID != "msg-open-id" {
		t.Fatalf("messageID = %q", messageID)
	}
}

func TestCardKitClientRepliesWithCard(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-apis/im/v1/messages/parent-message/reply" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["msg_type"] != "interactive" {
			t.Fatalf("reply body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"reply-1"}}`))
	}))
	defer api.Close()

	client := NewCardKitClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	messageID, err := client.SendCardAsMessage(context.Background(), "chat-1", "card-1", "parent-message")
	if err != nil {
		t.Fatalf("SendCardAsMessage(reply) error = %v", err)
	}
	if messageID != "reply-1" {
		t.Fatalf("messageID = %q", messageID)
	}
}

func TestCardKitAPIErrorPredicates(t *testing.T) {
	if !IsCardRateLimitError(&CardKitAPIError{Code: 230020}) {
		t.Fatal("rate limit error not detected")
	}
	tableErr := &CardKitAPIError{Code: 230099, Msg: "Failed; ErrCode: 11310; ErrMsg: card table number over limit;"}
	if ExtractCardSubCode(tableErr.Msg) != 11310 {
		t.Fatalf("subcode = %d", ExtractCardSubCode(tableErr.Msg))
	}
	if !IsCardTableLimitError(tableErr) {
		t.Fatal("table limit error not detected")
	}
	if IsCardTableLimitError(&CardKitAPIError{Code: 230099, Msg: "ErrCode: 11310; ErrMsg: other element limit"}) {
		t.Fatal("other element limit should not be treated as table limit")
	}
}

type cardKitCall struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

func cardKitCallNames(calls []cardKitCall) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		name := call.Method + " " + call.Path
		if call.Query != "" {
			name += "?" + call.Query
		}
		names = append(names, name)
	}
	return names
}
