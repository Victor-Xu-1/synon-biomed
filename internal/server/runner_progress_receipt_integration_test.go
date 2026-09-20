package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRunnerRejectedToolRequestsCannotPublishSuccess(t *testing.T) {
	for name, claim := range map[string]string{
		"download":  "The file was successfully downloaded. I will inspect it next.",
		"retrieval": "Successfully obtained the complete source; I will inspect it next.",
		"localized": "已成功获取完整数据，下一步继续处理。",
	} {
		t.Run(name, func(t *testing.T) { testRunnerRejectedToolRequestsCannotPublishSuccess(t, claim) })
	}
}

func testRunnerRejectedToolRequestsCannotPublishSuccess(t *testing.T, falseClaim string) {
	t.Helper()
	ctx := context.Background()
	store, repo, _ := newTranscriptWebFixture(t)
	const frameID = "native-recovery-progress"
	seedTranscriptWebFrame(t, store, "local", "native-recovery-project", frameID)
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	// This server deliberately has no confined kernel authority. The model's
	// edit proposal must remain non-executing; unavailable authority cannot be
	// bypassed to make its accompanying success claim appear legitimate.
	t.Cleanup(func() { _ = server.Close(ctx) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{FrameID: frameID, MessageUUID: "progress-message", ClientMessageID: "progress-input", Text: "Prepare one valid local input file."}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		message := map[string]any{"role": "assistant", "content": "I will inspect the requested input format."}
		if !strings.Contains(string(mustJSON(t, request.Messages)), "Write a brief expert orientation before the task begins.") {
			sequence := requests.Add(1)
			if sequence > 12 {
				http.Error(w, "unexpected unbounded generation", http.StatusConflict)
				return
			}
			arguments, _ := json.Marshal(map[string]any{"file_path": "input.cif", "old_string": "", "new_string": fmt.Sprintf("data_invalid_%d\n_entry.id empty\n", sequence), "human_description": "Preparing the input file", "public_progress": falseClaim})
			message = map[string]any{"role": "assistant", "content": falseClaim, "tool_calls": []any{map[string]any{"id": fmt.Sprintf("invalid-%d", sequence), "type": "function", "function": map[string]any{"name": "edit_file", "arguments": string(arguments)}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	defer provider.Close()
	result, err := server.RunSessionRunnerChatOnce(ctx, SessionRunnerChatOptions{SessionID: frameID, RunnerID: "native-recovery-runner", Endpoint: provider.URL + "/v1/chat/completions", APIKey: "local-test", Model: "local-test",
		AllowedTools: []string{"edit_file"}, MaxAttempts: 1, MaxToolRounds: 3, MaxConsecutiveIdenticalToolRounds: 2, LeaseTTL: time.Minute, ReplayLimit: 30, DisableSkillDiscovery: true, DisableMCPDiscovery: true})
	if err != nil || result.Status != "interrupted" || !result.InterruptionAutoResume {
		t.Fatalf("semantic failure did not preserve task continuity: %#v error=%v", result, err)
	}
	stream, found, err := repo.GetFrameStreamBySession(ctx, "local", frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	events, err := repo.ListProjectedCoordinateEvents(ctx, transcriptstore.ListProjectedEventsInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	failedReceipts := 0
	for _, event := range events {
		switch event.Event.Type {
		case "assistant_message", "content_delta":
			if strings.Contains(string(event.ResolvedPayloadJSON), falseClaim) {
				t.Fatal("unexecuted operation's success claim reached public durable text")
			}
		case "runner_checkpoint":
			var value map[string]any
			if err := json.Unmarshal(event.ResolvedPayloadJSON, &value); err != nil {
				t.Fatal(err)
			}
			if result := mapValue(value["toolResult"]); result["ok"] == false && result["executed"] == false && stringValue(result["code"]) != "" {
				failedReceipts++
			}
		}
	}
	if failedReceipts == 0 {
		t.Fatalf("missing non-execution receipt: reason=%s requests=%d", result.InterruptionReasonCode, requests.Load())
	}
	t.Logf("retained failed receipts=%d local HTTP requests=%d; false success withheld; task remains resumable", failedReceipts, requests.Load())
}
