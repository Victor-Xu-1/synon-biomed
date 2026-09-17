package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	adaptercommon "synon-go/internal/adapters/common"
	eventjournal "synon-go/internal/persistence/journal"
	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
)

func TestAdapterInboundEventsCreateDurableLiveSessionsAndJournalEntries(t *testing.T) {
	root := t.TempDir()
	outbound := &recordingSessionOutboundSink{}
	srv := New(Options{FileRoot: root, IMOutbound: outbound})
	allowPairingForTest(t, srv, "feishu", "ou_live_1")
	allowPairingForTest(t, srv, "wechat", "wx-live-user-1")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	cases := []struct {
		name         string
		platform     string
		chatID       string
		messageID    string
		textFragment string
		post         func() map[string]any
	}{
		{
			name:         "feishu",
			platform:     "feishu",
			chatID:       "oc_live_1",
			messageID:    "om_live_1",
			textFragment: "live Feishu session",
			post: func() map[string]any {
				return postFeishuEvent(t, httpServer.URL, []byte(`{
					"schema": "2.0",
					"header": {"event_id": "evt-feishu-live-1", "event_type": "im.message.receive_v1"},
					"event": {
						"sender": {"sender_id": {"open_id": "ou_live_1"}},
						"message": {
							"message_id": "om_live_1",
							"chat_id": "oc_live_1",
							"chat_type": "p2p",
							"message_type": "text",
							"content": "{\"text\":\"live Feishu session\"}"
						}
					}
				}`))
			},
		},
		{
			name:         "wechat",
			platform:     "wechat",
			chatID:       "wx-live-user-1",
			messageID:    "92001",
			textFragment: "live WeChat session",
			post: func() map[string]any {
				return postWeChatEvent(t, httpServer.URL, []byte(`{
					"message_id": 92001,
					"seq": 9,
					"from_user_id": "wx-live-user-1",
					"create_time_ms": 1710000000000,
					"context_token": "ctx-live-1",
					"item_list": [
						{"type": 1, "msg_id": "txt-live-1", "text_item": {"text": "live WeChat session"}}
					]
				}`))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.post()
			if body["ok"] != true || body["deduplicated"] != false {
				t.Fatalf("inbound response = %#v", body)
			}
			if _, ok := body["task"].(map[string]any); !ok {
				t.Fatalf("inbound response missing task: %#v", body)
			}
		})
	}

	sessions, err := sessionstore.NewStore(root).List()
	if err != nil {
		t.Fatalf("session List() error = %v", err)
	}
	if len(sessions) != len(cases) {
		t.Fatalf("sessions = %+v", sessions)
	}

	journal := eventjournal.NewEventJournal(root)
	for _, tc := range cases {
		sessionID := fmt.Sprintf("im:%s:%s", tc.platform, tc.chatID)
		stored := findSessionForTest(sessions, sessionID)
		if stored == nil {
			t.Fatalf("missing session %s in %+v", sessionID, sessions)
		}
		if stored.LastRole != "user" || stored.MessageCount != 1 {
			t.Fatalf("session %s = %+v", sessionID, *stored)
		}
		if !strings.Contains(stored.Title, tc.platform) || !strings.Contains(stored.Title, tc.chatID) {
			t.Fatalf("session %s title = %q", sessionID, stored.Title)
		}

		entries, err := journal.ReadAfter(sessionID, 0, 10)
		if err != nil {
			t.Fatalf("ReadAfter(%s) error = %v", sessionID, err)
		}
		if len(entries) != 1 {
			t.Fatalf("journal entries for %s = %+v", sessionID, entries)
		}
		message := entries[0].Message
		if message["type"] != "im_message" || message["role"] != "user" || message["platform"] != tc.platform || message["chatId"] != tc.chatID || message["messageId"] != tc.messageID || message["ownerUserId"] != secretstore.DefaultUserID {
			t.Fatalf("journal message for %s = %#v", sessionID, message)
		}
		if !strings.Contains(fmt.Sprint(message["text"]), tc.textFragment) {
			t.Fatalf("journal text for %s = %#v", sessionID, message)
		}
		if fmt.Sprint(message["taskId"]) == "" {
			t.Fatalf("journal message missing taskId for %s: %#v", sessionID, message)
		}
	}

	bindings := outbound.bindingSnapshot()
	if len(bindings) != len(cases) {
		t.Fatalf("outbound bindings = %+v", bindings)
	}
	for _, binding := range bindings {
		if binding.SessionID != imLiveSessionID(binding.Platform, binding.ChatID) || binding.OwnerUserID != secretstore.DefaultUserID || binding.TargetID == "" {
			t.Fatalf("outbound binding = %+v", binding)
		}
	}
	message := eventjournal.Message{"type": "message", "role": "assistant", "text": "bound delivery"}
	if !srv.publishSessionMessage("im:wechat:wx-live-user-1", 91001, message) {
		t.Fatal("publishSessionMessage() did not reach the bound IM sink")
	}
	events := outbound.eventSnapshot()
	if len(events) != 1 || events[0].SessionID != "im:wechat:wx-live-user-1" || events[0].EventID != 91001 || events[0].Message["text"] != "bound delivery" {
		t.Fatalf("outbound events = %+v", events)
	}
}

type recordingSessionOutboundSink struct {
	mu       sync.Mutex
	bindings []adaptercommon.IMSessionBinding
	events   []adaptercommon.SessionOutboundEvent
	unbound  []string
}

func (s *recordingSessionOutboundSink) Bind(binding adaptercommon.IMSessionBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = append(s.bindings, binding)
	return nil
}

func (s *recordingSessionOutboundSink) Enqueue(event adaptercommon.SessionOutboundEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return true
}

func (s *recordingSessionOutboundSink) Unbind(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unbound = append(s.unbound, sessionID)
}

func (s *recordingSessionOutboundSink) bindingSnapshot() []adaptercommon.IMSessionBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]adaptercommon.IMSessionBinding(nil), s.bindings...)
}

func (s *recordingSessionOutboundSink) eventSnapshot() []adaptercommon.SessionOutboundEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]adaptercommon.SessionOutboundEvent(nil), s.events...)
}

func (s *recordingSessionOutboundSink) unboundSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.unbound...)
}

func findSessionForTest(sessions []sessionstore.Session, id string) *sessionstore.Session {
	for index := range sessions {
		if sessions[index].ID == id {
			return &sessions[index]
		}
	}
	return nil
}
