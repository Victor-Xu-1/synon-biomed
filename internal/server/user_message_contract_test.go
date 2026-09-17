package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBriefCompatibilityBoundaryUsesCanonicalUserMessageContract(t *testing.T) {
	httpServer := httptest.NewServer(New(Options{}).Handler())
	t.Cleanup(httpServer.Close)

	post := func(input map[string]any) (int, map[string]any) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"input": input})
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		resp, err := http.Post(httpServer.URL+"/api/tools/Brief/execute", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST Brief: %v", err)
		}
		defer resp.Body.Close()
		var decoded map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode Brief status=%d: %v", resp.StatusCode, err)
		}
		return resp.StatusCode, decoded
	}

	status, success := post(map[string]any{"message": "done"})
	if status != http.StatusOK || success["ok"] != true {
		t.Fatalf("omitted status response status=%d body=%#v", status, success)
	}
	result, ok := success["result"].(map[string]any)
	if !ok || result["message"] != "done" || result["sentAt"] == "" {
		t.Fatalf("omitted status result = %#v", success["result"])
	}

	status, rejected := post(map[string]any{"message": "done", "status": "completed"})
	rawRejected, _ := json.Marshal(rejected)
	if status != http.StatusBadRequest || !strings.Contains(string(rawRejected), "SendUserMessage.status must be one of: normal, proactive") {
		t.Fatalf("unadvertised status response status=%d body=%s", status, rawRejected)
	}
}
