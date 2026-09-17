package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFeishuEventURLVerificationReturnsChallenge(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	body := []byte(`{"type":"url_verification","challenge":"challenge-token-123"}`)
	resp, err := http.Post(httpServer.URL+"/api/adapters/feishu/event", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST feishu challenge error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("challenge status = %d", resp.StatusCode)
	}
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode challenge response: %v", err)
	}
	if decoded["challenge"] != "challenge-token-123" {
		t.Fatalf("challenge response = %#v", decoded)
	}
}

func TestFeishuEventCreatesDurableTaskAndDeduplicatesMessage(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_victor")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	payload := []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-feishu-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_victor"}},
			"message": {
				"message_id": "om_1",
				"chat_id": "oc_1",
				"chat_type": "p2p",
				"message_type": "post",
				"content": "{\"zh_cn\":{\"content\":[[{\"tag\":\"text\",\"text\":\"review Feishu package \"},{\"tag\":\"img\",\"image_key\":\"img_key_1\"}],[{\"tag\":\"file\",\"file_key\":\"file_key_1\",\"file_name\":\"notes.pdf\"}]]}}"
			}
		}
	}`)

	first := postFeishuEvent(t, httpServer.URL, payload)
	if first["ok"] != true || first["platform"] != "feishu" || first["deduplicated"] != false {
		t.Fatalf("first feishu response = %#v", first)
	}
	if first["messageId"] != "om_1" || first["chatId"] != "oc_1" || first["eventId"] != "evt-feishu-1" {
		t.Fatalf("feishu metadata = %#v", first)
	}
	task := first["task"].(map[string]any)
	if task["status"] != "open" || !strings.Contains(task["title"].(string), "review Feishu package") {
		t.Fatalf("created task = %#v", task)
	}
	downloads := first["downloads"].([]any)
	if len(downloads) != 2 {
		t.Fatalf("downloads = %#v", downloads)
	}

	second := postFeishuEvent(t, httpServer.URL, payload)
	if second["ok"] != true || second["deduplicated"] != true {
		t.Fatalf("duplicate feishu response = %#v", second)
	}
	if _, ok := second["task"]; ok {
		t.Fatalf("duplicate should not create task: %#v", second)
	}

	listed := postToolInput(t, httpServer.URL, "task_list", map[string]any{})
	tasks := listed["result"].(map[string]any)["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("tasks after duplicate feishu event = %#v", tasks)
	}
	stored := tasks[0].(map[string]any)
	if stored["id"] != task["id"] || stored["title"] != task["title"] {
		t.Fatalf("stored task = %#v, webhook task = %#v", stored, task)
	}
}

func TestFeishuEventTreatsSlashCommandAsCanonicalTask(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	allowPairingForTest(t, srv, "feishu", "ou_victor")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	payload := []byte(`{
		"header": {"event_id": "evt-feishu-command", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_victor"}},
			"message": {
				"message_id": "om_cmd",
				"chat_id": "oc_cmd",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"/goal status\"}"
			}
		}
	}`)

	body := postFeishuEvent(t, httpServer.URL, payload)
	if body["ok"] != true || body["deduplicated"] != false {
		t.Fatalf("command response = %#v", body)
	}
	if _, ok := body["route"]; ok {
		t.Fatalf("slash command must not enter a detached routing path: %#v", body)
	}
	task, ok := body["task"].(map[string]any)
	title, _ := task["title"].(string)
	if !ok || task["status"] != "open" || !strings.Contains(title, "/goal status") {
		t.Fatalf("slash command should create a canonical task: %#v", body)
	}
}

func TestFeishuWebhookWaitsRepairsAndRejectsFingerprintConflict(t *testing.T) {
	workspace, transcript, _ := newTranscriptWebFixture(t)
	root := t.TempDir()
	srv := New(Options{Workspace: workspace, Transcript: transcript, FileRoot: root, IMOutbound: &recordingSessionOutboundSink{}})
	allowPairingForTest(t, srv, "feishu", "ou_repair")
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv.imProjectionHook = func() { once.Do(func() { close(entered); <-release }) }
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	payload := []byte(`{
		"header":{"event_id":"evt-feishu-repair","event_type":"im.message.receive_v1"},
		"event":{"sender":{"sender_id":{"open_id":"ou_repair"}},"message":{
			"message_id":"om_repair","chat_id":"oc_repair","chat_type":"p2p",
			"message_type":"text","content":"{\"text\":\"repair feishu admission\"}"}}}`)
	statuses := make(chan int, 2)
	post := func() {
		status, _ := postWebhookStatus(httpServer.URL+"/api/adapters/feishu/event", payload)
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
	if result := postFeishuEvent(t, httpServer.URL, payload); result["deduplicated"] != false {
		t.Fatalf("repair result=%#v", result)
	}
	conflict := bytes.Replace(payload, []byte("repair feishu admission"), []byte("conflicting feishu payload"), 1)
	if status, err := postWebhookStatus(httpServer.URL+"/api/adapters/feishu/event", conflict); err != nil || status != http.StatusBadRequest {
		t.Fatalf("conflict status=%d err=%v", status, err)
	}
}

func postFeishuEvent(t *testing.T, baseURL string, body []byte) map[string]any {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/adapters/feishu/event", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST feishu event error = %v", err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode feishu event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feishu event status = %d body = %#v", resp.StatusCode, decoded)
	}
	return decoded
}
