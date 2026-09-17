package server

import (
	"context"
	"testing"

	adapterfeishu "synon-go/internal/adapters/feishu"
)

func TestFeishuStreamMessageCallbackCreatesDurableTask(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	allowPairingForTest(t, srv, "feishu", "ou_stream")
	router := adapterfeishu.NewStreamRouter(adapterfeishu.StreamRouterOptions{
		OnInboundEvent: func(ctx context.Context, inbound adapterfeishu.InboundEvent) error {
			_, err := srv.HandleFeishuInbound(ctx, inbound)
			return err
		},
	})
	dispatcher := adapterfeishu.NewStreamEventDispatcher("", "", router)

	raw := []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-feishu-stream-task", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_stream"}},
			"message": {
				"message_id": "om_stream_task",
				"chat_id": "oc_stream_task",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"ship Go Feishu live stream\"}"
			}
		}
	}`)
	if _, err := dispatcher.Do(context.Background(), raw); err != nil {
		t.Fatalf("dispatcher.Do error = %v", err)
	}

	tasks, err := srv.taskStore.List()
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %#v", tasks)
	}
	if tasks[0].Status != "open" || tasks[0].Title != "Feishu: ship Go Feishu live stream" {
		t.Fatalf("stream-created task = %#v", tasks[0])
	}
}
