package wechat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetUpdatesPostsRealWechatRequestAndReturnsBuffer(t *testing.T) {
	var gotAuth string
	var gotClientVersion string
	var gotBuffer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/getupdates" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		gotAuth = r.Header.Get("Authorization")
		gotClientVersion = r.Header.Get("iLink-App-ClientVersion")
		var body struct {
			GetUpdatesBuf string `json:"get_updates_buf"`
			BaseInfo      struct {
				ChannelVersion string `json:"channel_version"`
			} `json:"base_info"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		gotBuffer = body.GetUpdatesBuf
		if body.BaseInfo.ChannelVersion != "2.1.7" {
			t.Fatalf("channel_version = %q", body.BaseInfo.ChannelVersion)
		}
		_, _ = w.Write([]byte(`{
			"ret": 0,
			"get_updates_buf": "next-buffer",
			"longpolling_timeout_ms": 35000,
			"msgs": [{
				"message_id": 901,
				"seq": 5,
				"from_user_id": "wx-user",
				"create_time_ms": 1710000000000,
				"context_token": "ctx-1",
				"item_list": [{"type": 1, "msg_id": "txt-1", "text_item": {"text": "hello wechat"}}]
			}]
		}`))
	}))
	defer server.Close()

	resp, err := GetUpdates(context.Background(), http.DefaultClient, server.URL, "TOKEN", GetUpdatesOptions{
		GetUpdatesBuf: "old-buffer",
		Timeout:       time.Second,
	})
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if gotAuth != "Bearer TOKEN" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotClientVersion != "131335" {
		t.Fatalf("iLink-App-ClientVersion = %q", gotClientVersion)
	}
	if gotBuffer != "old-buffer" {
		t.Fatalf("get_updates_buf = %q", gotBuffer)
	}
	if resp.GetUpdatesBuf != "next-buffer" || len(resp.Messages) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	if text := ExtractText(resp.Messages[0].ItemList); text != "hello wechat" {
		t.Fatalf("message text = %q", text)
	}
}

func TestSendTextPostsWechatSendMessageRequest(t *testing.T) {
	var gotBody struct {
		Message struct {
			ToUserID     string        `json:"to_user_id"`
			MessageType  int           `json:"message_type"`
			MessageState int           `json:"message_state"`
			ContextToken string        `json:"context_token"`
			ItemList     []MessageItem `json:"item_list"`
		} `json:"msg"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("AuthorizationType") != "ilink_bot_token" {
			t.Fatalf("AuthorizationType = %q", r.Header.Get("AuthorizationType"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	err := SendText(context.Background(), http.DefaultClient, server.URL, "TOKEN", SendTextOptions{
		To:           "wx-user",
		Text:         "hello from synon",
		ContextToken: "ctx-1",
		Timeout:      time.Second,
	})
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
	if gotBody.Message.ToUserID != "wx-user" || gotBody.Message.MessageType != 2 || gotBody.Message.MessageState != 2 {
		t.Fatalf("message metadata = %+v", gotBody.Message)
	}
	if gotBody.Message.ContextToken != "ctx-1" {
		t.Fatalf("context token = %q", gotBody.Message.ContextToken)
	}
	if len(gotBody.Message.ItemList) != 1 || gotBody.Message.ItemList[0].Type != 1 || gotBody.Message.ItemList[0].TextItem.Text != "hello from synon" {
		t.Fatalf("item list = %+v", gotBody.Message.ItemList)
	}
}

func TestSendTextRejectsWechatRetError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret":40001,"errmsg":"bad context_token"}`))
	}))
	defer server.Close()

	err := SendText(context.Background(), http.DefaultClient, server.URL, "TOKEN", SendTextOptions{
		To:      "wx-user",
		Text:    "hello",
		Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("SendText should reject non-zero WeChat ret")
	}
}

func TestPollerRunOnceDeliversMessagesAndKeepsBuffer(t *testing.T) {
	requestBuffers := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			GetUpdatesBuf string `json:"get_updates_buf"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		requestBuffers = append(requestBuffers, body.GetUpdatesBuf)
		nextBuffer := "buf-1"
		messageID := int64(1001)
		if len(requestBuffers) == 2 {
			nextBuffer = "buf-2"
			messageID = 1002
		}
		_, _ = w.Write([]byte(`{
			"ret": 0,
			"get_updates_buf": "` + nextBuffer + `",
			"msgs": [{
				"message_id": ` + jsonNumber(messageID) + `,
				"seq": 1,
				"from_user_id": "wx-user",
				"create_time_ms": 1710000000000,
				"item_list": [{"type": 1, "text_item": {"text": "wechat poll"}}]
			}]
		}`))
	}))
	defer server.Close()

	delivered := make([]Message, 0, 2)
	poller := NewPoller(http.DefaultClient, server.URL, "TOKEN", PollerOptions{
		GetUpdatesBuf: "initial",
		Interval:      time.Millisecond,
	}, func(_ context.Context, message Message) error {
		delivered = append(delivered, message)
		return nil
	})

	first, err := poller.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	second, err := poller.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}

	if len(requestBuffers) != 2 || requestBuffers[0] != "initial" || requestBuffers[1] != "buf-1" {
		t.Fatalf("request buffers = %#v", requestBuffers)
	}
	if first.GetUpdatesBuf != "buf-1" || second.GetUpdatesBuf != "buf-2" || poller.GetUpdatesBuf() != "buf-2" {
		t.Fatalf("buffers first=%q second=%q poller=%q", first.GetUpdatesBuf, second.GetUpdatesBuf, poller.GetUpdatesBuf())
	}
	if len(delivered) != 2 || delivered[0].MessageID != 1001 || delivered[1].MessageID != 1002 {
		t.Fatalf("delivered = %+v", delivered)
	}
}

func TestPollerRunStopsWhenHandlerCancelsContext(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{
			"ret": 0,
			"get_updates_buf": "next",
			"msgs": [{
				"message_id": 2001,
				"from_user_id": "wx-user",
				"create_time_ms": 1710000000000,
				"item_list": [{"type": 1, "text_item": {"text": "stop"}}]
			}]
		}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	delivered := 0
	poller := NewPoller(http.DefaultClient, server.URL, "TOKEN", PollerOptions{
		Interval: time.Millisecond,
	}, func(_ context.Context, _ Message) error {
		delivered++
		cancel()
		return nil
	})

	if err := poller.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if delivered != 1 || requests != 1 {
		t.Fatalf("delivered=%d requests=%d", delivered, requests)
	}
}

func jsonNumber(value int64) string {
	return fmt.Sprintf("%d", value)
}
