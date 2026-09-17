package feishu

import (
	"context"
	"testing"
)

func TestStreamRouterRoutesOfficialDispatcherMessageEvent(t *testing.T) {
	var routed []InboundEvent
	router := NewStreamRouter(StreamRouterOptions{
		OnInboundEvent: func(_ context.Context, event InboundEvent) error {
			routed = append(routed, event)
			return nil
		},
	})
	dispatcher := NewStreamEventDispatcher("", "", router)

	raw := []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-stream-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_stream"}},
			"message": {
				"message_id": "om_stream_1",
				"chat_id": "oc_stream_1",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"ship Feishu WSClient bridge\"}"
			}
		}
	}`)
	if _, err := dispatcher.Do(context.Background(), raw); err != nil {
		t.Fatalf("dispatcher.Do message error = %v", err)
	}
	if len(routed) != 1 {
		t.Fatalf("routed events = %#v", routed)
	}
	event := routed[0]
	if event.EventID != "evt-stream-1" || event.MessageID != "om_stream_1" || event.ChatID != "oc_stream_1" {
		t.Fatalf("message metadata = %#v", event)
	}
	if event.SenderOpenID != "ou_stream" || event.Text != "ship Feishu WSClient bridge" || event.TaskTitle != "Feishu: ship Feishu WSClient bridge" {
		t.Fatalf("message event = %#v", event)
	}
}
