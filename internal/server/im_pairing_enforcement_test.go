package server

import (
	"fmt"
	"testing"
)

func TestIMInboundRequiresDurablePairingAcrossPlatforms(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	httpServer := httptestServer(t, srv)

	cases := []struct {
		platform string
		userID   string
		post     func(string) map[string]any
	}{
		{
			platform: "feishu",
			userID:   "ou_pairing",
			post: func(id string) map[string]any {
				return postFeishuEvent(t, httpServer.URL, []byte(fmt.Sprintf(`{
					"schema": "2.0",
					"header": {"event_id": "evt-pair-%s", "event_type": "im.message.receive_v1"},
					"event": {
						"sender": {"sender_id": {"open_id": "ou_pairing"}},
						"message": {
							"message_id": "om_pair_%s",
							"chat_id": "oc_pair",
							"chat_type": "p2p",
							"message_type": "text",
							"content": "{\"text\":\"pairing Feishu %s\"}"
						}
					}
				}`, id, id, id)))
			},
		},
		{
			platform: "wechat",
			userID:   "wx-pairing",
			post: func(id string) map[string]any {
				return postWeChatEvent(t, httpServer.URL, []byte(fmt.Sprintf(`{
					"message_id": %d,
					"seq": %d,
					"from_user_id": "wx-pairing",
					"create_time_ms": 1710000000000,
					"item_list": [{"type": 1, "msg_id": "txt-%s", "text_item": {"text": "pairing WeChat %s"}}]
				}`, numericSuffix(id, 30000), numericSuffix(id, 40000), id, id)))
			},
		},
	}

	for index, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			blocked := tc.post(fmt.Sprintf("%d-blocked", index))
			if blocked["blocked"] != true || blocked["paired"] != false {
				t.Fatalf("unpaired %s should be blocked: %#v", tc.platform, blocked)
			}
			if _, ok := blocked["task"]; ok {
				t.Fatalf("unpaired %s should not create task: %#v", tc.platform, blocked)
			}

			postToolInput(t, httpServer.URL, "pairing_allow", map[string]any{
				"platform":    tc.platform,
				"userId":      tc.userID,
				"displayName": "Pairing User",
			})
			allowed := tc.post(fmt.Sprintf("%d-allowed", index))
			if allowed["paired"] != true || allowed["blocked"] == true {
				t.Fatalf("paired %s should pass: %#v", tc.platform, allowed)
			}
			if _, ok := allowed["task"]; !ok {
				t.Fatalf("paired %s should create task: %#v", tc.platform, allowed)
			}

			postToolInput(t, httpServer.URL, "pairing_revoke", map[string]any{
				"platform": tc.platform,
				"userId":   tc.userID,
			})
			revoked := tc.post(fmt.Sprintf("%d-revoked", index))
			if revoked["blocked"] != true || revoked["paired"] != false {
				t.Fatalf("revoked %s should be blocked again: %#v", tc.platform, revoked)
			}
		})
	}
}

func TestIMInboundPairingFailsClosedWithoutStore(t *testing.T) {
	status, err := (&Server{}).checkInboundPairing("wechat", "10086")
	if err != nil {
		t.Fatal(err)
	}
	if status.Paired || !status.Blocked || status.BlockedReason != imPairingUnavailableReason {
		t.Fatalf("missing pairing store status = %#v", status)
	}
}

func TestAuthenticatedIMPairingWithoutPersistentOwnerFailsClosed(t *testing.T) {
	srv := New(Options{
		FileRoot:      t.TempDir(),
		SynonLinkAuth: SynonLinkAuthOptions{Username: "operator", Password: "secret", UserID: "operator"},
	})
	if _, err := srv.pairingStore.Allow("wechat", "101", "legacy"); err != nil {
		t.Fatal(err)
	}
	status, err := srv.checkInboundPairing("wechat", "101")
	if err != nil {
		t.Fatal(err)
	}
	if status.Paired || !status.Blocked || status.OwnerUserID != "" || status.BlockedReason != imPairingUnavailableReason {
		t.Fatalf("status=%#v", status)
	}
}

func numericSuffix(value string, base int) int {
	total := base
	for _, r := range value {
		total += int(r)
	}
	return total
}

func allowPairingForTest(t *testing.T, srv *Server, platform string, userID string) {
	t.Helper()
	if srv.pairingStore == nil {
		t.Fatalf("pairing store is not configured")
	}
	if _, err := srv.pairingStore.AllowForOwner(platform, userID, "Test User", "local"); err != nil {
		t.Fatalf("allow pairing %s/%s: %v", platform, userID, err)
	}
}
