package feishu

import (
	"strings"
	"testing"
)

func TestExtractInboundPayloadFromPostTextAndDownloads(t *testing.T) {
	content := `{"zh_cn":{"content":[[{"tag":"text","text":"look: "}],[{"tag":"img","image_key":"img_post_1"}],[{"tag":"text","text":" and "}],[{"tag":"file","file_key":"file_post_1","file_name":"note.txt"}]]}}`
	payload := ExtractInboundPayload(content, "post")
	if payload.Text != "look: \n\n and " {
		t.Fatalf("text = %q", payload.Text)
	}
	if len(payload.PendingDownloads) != 2 {
		t.Fatalf("downloads = %#v", payload.PendingDownloads)
	}
	if payload.PendingDownloads[0].Kind != "image" || payload.PendingDownloads[0].FileKey != "img_post_1" {
		t.Fatalf("image download = %#v", payload.PendingDownloads[0])
	}
	if payload.PendingDownloads[1].Kind != "file" || payload.PendingDownloads[1].FileKey != "file_post_1" || payload.PendingDownloads[1].FileName != "note.txt" {
		t.Fatalf("file download = %#v", payload.PendingDownloads[1])
	}
}

func TestPostContentMentionsBot(t *testing.T) {
	content := `{"content":[[{"tag":"at","id":{"open_id":"ou_bot_123"},"name":"Synon"},{"tag":"text","text":" 帮我看看这个图"}]]}`
	if !PostContentMentionsBot(content, "ou_bot_123") {
		t.Fatal("post content should mention bot")
	}
	if PostContentMentionsBot(content, "ou_other") {
		t.Fatal("post content should not mention other bot")
	}
}

func TestParseWebhookEventPreservesSlashCommandAndStripsMentions(t *testing.T) {
	event, err := ParseWebhookEvent(strings.NewReader(`{
		"header": {"event_id": "evt_route", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_user"}},
			"message": {
				"message_id": "om_route",
				"chat_id": "oc_route",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"@_user_1 /goal status\"}"
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseWebhookEvent() error = %v", err)
	}
	if event.Inbound == nil {
		t.Fatal("missing inbound event")
	}
	if event.Inbound.Text != "/goal status" {
		t.Fatalf("text = %q", event.Inbound.Text)
	}
	if event.Inbound.DedupID != "feishu:message:om_route" {
		t.Fatalf("dedup id = %q", event.Inbound.DedupID)
	}
}
