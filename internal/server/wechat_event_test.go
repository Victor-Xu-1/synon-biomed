package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adapterwechat "synon-go/internal/adapters/wechat"
)

func TestWeChatEventCreatesDurableTaskAndDeduplicatesMessage(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "wechat", "wx-user-1")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	payload := []byte(`{
		"message_id": 12345,
		"seq": 7,
		"from_user_id": "wx-user-1",
		"create_time_ms": 1710000000000,
		"context_token": "ctx-1",
		"item_list": [
			{"type": 1, "msg_id": "txt-1", "text_item": {"text": "review WeChat package"}},
			{"type": 2, "msg_id": "img-1", "image_item": {"media": {"full_url": "https://cdn.example.com/image", "encrypt_query_param": "enc=1"}, "aeskey": "001122"}}
		]
	}`)

	first := postWeChatEvent(t, httpServer.URL, payload)
	if first["ok"] != true || first["platform"] != "wechat" || first["deduplicated"] != false {
		t.Fatalf("first wechat response = %#v", first)
	}
	if first["messageId"] != float64(12345) || first["chatId"] != "wx-user-1" || first["contextToken"] != "ctx-1" {
		t.Fatalf("wechat metadata = %#v", first)
	}
	task := first["task"].(map[string]any)
	if task["status"] != "open" || !strings.Contains(task["title"].(string), "review WeChat package") {
		t.Fatalf("created task = %#v", task)
	}
	downloads := first["downloads"].([]any)
	if len(downloads) != 1 {
		t.Fatalf("downloads = %#v", downloads)
	}

	second := postWeChatEvent(t, httpServer.URL, payload)
	if second["ok"] != true || second["deduplicated"] != true {
		t.Fatalf("duplicate wechat response = %#v", second)
	}
	if _, ok := second["task"]; ok {
		t.Fatalf("duplicate should not create task: %#v", second)
	}

	listed := postToolInput(t, httpServer.URL, "task_list", map[string]any{})
	tasks := listed["result"].(map[string]any)["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("tasks after duplicate wechat event = %#v", tasks)
	}
}

func TestWeChatEventRejectsInvalidJSON(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/adapters/wechat/event", "application/json", strings.NewReader(`{"message_id":`))
	if err != nil {
		t.Fatalf("POST invalid wechat event error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid wechat status = %d", resp.StatusCode)
	}
}

func TestWeChatPollerCreatesDurableTaskThroughServerHandler(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "wechat", "wx-poll-user")
	message := adapterwechat.Message{
		MessageID:    91001,
		Seq:          3,
		FromUserID:   "wx-poll-user",
		CreateTimeMS: 1710000000000,
		ContextToken: "ctx-poll",
		ItemList: []adapterwechat.MessageItem{
			{Type: 1, TextItem: &adapterwechat.TextItem{Text: "polling task from wechat"}},
		},
	}

	first, err := srv.HandleWeChatInbound(context.Background(), message)
	if err != nil {
		t.Fatalf("HandleWeChatInbound() error = %v", err)
	}
	if !first.OK || first.Platform != "wechat" || first.Deduplicated || first.Task == nil {
		t.Fatalf("first result = %+v", first)
	}
	if !strings.Contains(first.Task.Title, "polling task from wechat") {
		t.Fatalf("task title = %q", first.Task.Title)
	}

	second, err := srv.HandleWeChatInbound(context.Background(), message)
	if err != nil {
		t.Fatalf("duplicate HandleWeChatInbound() error = %v", err)
	}
	if !second.Deduplicated || second.Task != nil {
		t.Fatalf("duplicate result = %+v", second)
	}
	tasks, err := srv.taskStore.List()
	if err != nil {
		t.Fatalf("taskStore.List() error = %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestWeChatWebhookWaitsRepairsAndRejectsFingerprintConflict(t *testing.T) {
	workspace, transcript, _ := newTranscriptWebFixture(t)
	root := t.TempDir()
	srv := New(Options{Workspace: workspace, Transcript: transcript, FileRoot: root, IMOutbound: &recordingSessionOutboundSink{}})
	allowPairingForTest(t, srv, "wechat", "wx-repair")
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv.imProjectionHook = func() { once.Do(func() { close(entered); <-release }) }
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	payload := []byte(`{
		"message_id":81234,"seq":1,"from_user_id":"wx-repair","context_token":"ctx-repair",
		"item_list":[{"type":1,"msg_id":"txt-repair","text_item":{"text":"repair wechat admission"}}]}`)
	statuses := make(chan int, 2)
	post := func() {
		status, _ := postWebhookStatus(httpServer.URL+"/api/adapters/wechat/event", payload)
		statuses <- status
	}
	go post()
	<-entered
	indexPath := filepath.Join(root, "sessions", "index.json")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	go post()
	select {
	case status := <-statuses:
		t.Fatalf("follower returned status %d before leader", status)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if status := <-statuses; status != http.StatusBadRequest {
			t.Fatalf("failed admission status=%d", status)
		}
	}
	srv.imProjectionHook = nil
	if err := os.WriteFile(indexPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := postWeChatEvent(t, httpServer.URL, payload); result["deduplicated"] != false {
		t.Fatalf("repair result=%#v", result)
	}
	conflict := bytes.Replace(payload, []byte("ctx-repair"), []byte("ctx-conflict"), 1)
	if status, err := postWebhookStatus(httpServer.URL+"/api/adapters/wechat/event", conflict); err != nil || status != http.StatusBadRequest {
		t.Fatalf("conflict status=%d err=%v", status, err)
	}
}

func postWeChatEvent(t *testing.T, baseURL string, body []byte) map[string]any {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/adapters/wechat/event", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST wechat event error = %v", err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode wechat event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wechat event status = %d body = %#v", resp.StatusCode, decoded)
	}
	return decoded
}
