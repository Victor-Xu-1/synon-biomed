package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
)

type capturedWeChatMessage struct {
	Path         string
	TargetUserID string
	ContextToken string
	Text         string
}

func TestOutboundProcessorSendsNormalizedServerMessages(t *testing.T) {
	var sent []capturedWeChatMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message struct {
				ToUserID     string        `json:"to_user_id"`
				ContextToken string        `json:"context_token"`
				ItemList     []MessageItem `json:"item_list"`
			} `json:"msg"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		text := ""
		if len(body.Message.ItemList) > 0 && body.Message.ItemList[0].TextItem != nil {
			text = body.Message.ItemList[0].TextItem.Text
		}
		sent = append(sent, capturedWeChatMessage{
			Path:         r.URL.Path,
			TargetUserID: body.Message.ToUserID,
			ContextToken: body.Message.ContextToken,
			Text:         text,
		})
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	processor := NewOutboundProcessor(http.DefaultClient, server.URL, "TOKEN", OutboundProcessorOptions{
		ContextToken: "ctx-1",
		MessageLimit: 4096,
		Timeout:      time.Second,
	})
	messages := []adaptercommon.ServerMessage{
		{"type": "content_delta", "text": "hello from synon"},
		{"type": "tool_use", "toolName": "Bash", "input": map[string]any{"command": "go test ./internal/adapters/wechat"}},
		{"type": "error", "message": "model failed"},
		{"type": "message_complete"},
	}
	for _, message := range messages {
		if err := processor.ProcessServerMessage(context.Background(), "wx-user", message); err != nil {
			t.Fatalf("ProcessServerMessage() error = %v", err)
		}
	}

	if len(sent) != 3 {
		t.Fatalf("sent messages = %#v", sent)
	}
	for _, message := range sent {
		if message.Path != "/ilink/bot/sendmessage" || message.TargetUserID != "wx-user" || message.ContextToken != "ctx-1" {
			t.Fatalf("wechat request = %#v", message)
		}
	}
	for _, want := range []string{"hello from synon", "go test ./internal/adapters/wechat", "model failed"} {
		if !capturedWeChatMessagesContain(sent, want) {
			t.Fatalf("sent messages %#v missing %q", sent, want)
		}
	}
}

func TestOutboundProcessorReportsWeChatDeliveryPolicy(t *testing.T) {
	var sent []capturedWeChatMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message struct {
				ToUserID string        `json:"to_user_id"`
				ItemList []MessageItem `json:"item_list"`
			} `json:"msg"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		text := ""
		if len(body.Message.ItemList) > 0 && body.Message.ItemList[0].TextItem != nil {
			text = body.Message.ItemList[0].TextItem.Text
		}
		sent = append(sent, capturedWeChatMessage{
			Path:         r.URL.Path,
			TargetUserID: body.Message.ToUserID,
			Text:         text,
		})
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	processor := NewOutboundProcessor(http.DefaultClient, server.URL, "TOKEN", OutboundProcessorOptions{
		MessageLimit: 12,
		Timeout:      time.Second,
	})
	report, err := processor.ProcessServerMessageWithReport(context.Background(), "wx-user", adaptercommon.ServerMessage{
		"type":    "content_delta",
		"eventId": "evt-wechat-report",
		"text":    "alpha beta gamma delta",
	})
	if err != nil {
		t.Fatalf("ProcessServerMessageWithReport() error = %v", err)
	}
	if report.Platform != "wechat" || report.MessageKey != "eventId:evt-wechat-report" || report.RenderMode != "wechat-ilink-text" {
		t.Fatalf("report identity = %#v", report)
	}
	if report.Chunks != 1 || report.TextParts != len(sent) || report.PlatformCalls != len(sent) || report.MessageLimit != 12 || report.DeliverySummary != "delivered" {
		t.Fatalf("wechat report = %#v sent=%#v", report, sent)
	}
	if report.Policy.Platform != "wechat" || report.Policy.RenderMode != report.RenderMode || report.Policy.MessageLimit != 12 {
		t.Fatalf("wechat report policy identity = %#v", report.Policy)
	}
	if report.Policy.SupportsMarkdown || report.Policy.SupportsMedia || report.Policy.SupportsCards || report.Policy.MediaStrategy != "text-only" {
		t.Fatalf("wechat report policy capabilities = %#v", report.Policy)
	}
	if safety := strings.Join(report.Policy.Safety, "|"); !strings.Contains(safety, "context-token-forwarding") || !strings.Contains(safety, "bounded-message-splitting") {
		t.Fatalf("wechat report policy safety = %#v", report.Policy.Safety)
	}
}

func TestOutboundProcessorSplitsLongMessages(t *testing.T) {
	var sent []capturedWeChatMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message struct {
				ToUserID string        `json:"to_user_id"`
				ItemList []MessageItem `json:"item_list"`
			} `json:"msg"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		text := ""
		if len(body.Message.ItemList) > 0 && body.Message.ItemList[0].TextItem != nil {
			text = body.Message.ItemList[0].TextItem.Text
		}
		sent = append(sent, capturedWeChatMessage{
			Path:         r.URL.Path,
			TargetUserID: body.Message.ToUserID,
			Text:         text,
		})
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	processor := NewOutboundProcessor(http.DefaultClient, server.URL, "TOKEN", OutboundProcessorOptions{
		MessageLimit: 12,
		Timeout:      time.Second,
	})
	err := processor.ProcessServerMessage(context.Background(), "wx-user", adaptercommon.ServerMessage{
		"type": "content_delta",
		"text": "alpha beta gamma delta",
	})
	if err != nil {
		t.Fatalf("ProcessServerMessage() error = %v", err)
	}

	if len(sent) < 2 {
		t.Fatalf("sent messages = %#v", sent)
	}
	var parts []string
	for _, message := range sent {
		if message.Path != "/ilink/bot/sendmessage" || message.TargetUserID != "wx-user" {
			t.Fatalf("wechat request = %#v", message)
		}
		if len(message.Text) > 12 {
			t.Fatalf("message text %q exceeds limit", message.Text)
		}
		parts = append(parts, message.Text)
	}
	if joined := strings.Join(parts, " "); joined != "alpha beta gamma delta" {
		t.Fatalf("joined split message = %q", joined)
	}
}

func TestOutboundProcessorReturnsWeChatAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":40001,"errmsg":"bad context_token"}`))
	}))
	defer server.Close()

	processor := NewOutboundProcessor(http.DefaultClient, server.URL, "TOKEN", OutboundProcessorOptions{
		Timeout: time.Second,
	})
	err := processor.ProcessServerMessage(context.Background(), "wx-user", adaptercommon.ServerMessage{
		"type": "content_delta",
		"text": "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "bad context_token") {
		t.Fatalf("ProcessServerMessage() error = %v", err)
	}
}

func capturedWeChatMessagesContain(messages []capturedWeChatMessage, want string) bool {
	for _, message := range messages {
		if strings.Contains(message.Text, want) {
			return true
		}
	}
	return false
}
