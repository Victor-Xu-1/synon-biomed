package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceFrameStreamingFallsBackToTranscriptProjection(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-stream", "frame-stream")

	srv := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	if _, _, err := srv.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-stream", MessageUUID: "message-1", ClientMessageID: "client-1", Text: "Produce a transcript-backed answer.",
	}); err != nil {
		t.Fatal(err)
	}

	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-stream")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "stream-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}

	// Persist transcript-backed content deltas (the Transcript authority path
	// used by modern runners; the legacy EventJournal stays empty).
	for index, chunk := range []string{"transcript ", "answer"} {
		payload, err := json.Marshal(map[string]any{
			"text": chunk, "delta_index": index,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
			Claim: claim.Claim, ClientMessageID: "stream-delta-" + string(rune('a'+index)),
			Type: "content_delta", Source: "payload", PayloadJSON: payload, Destinations: []string{"ws"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	srv.transcriptWebReadModel = transcriptstore.NewWebReadModelRepository(db, db)
	if err := srv.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := srv.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}

	buffer, err := srv.frameStreamingBuffer("frame-stream")
	if err != nil {
		t.Fatal(err)
	}
	if buffer.Content != "transcript answer" || !buffer.Active {
		t.Fatalf("transcript fallback buffer=%#v", buffer)
	}

	// The legacy EventJournal must remain untouched for the transcript path.
	if entries, err := srv.eventJournal.ReadAll("frame-stream"); err != nil || len(entries) != 0 {
		t.Fatalf("legacy journal entries=%#v err=%v", entries, err)
	}
}

func TestWorkspaceFrameRecoveryViewsUseDurableJournal(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root-frame", ProjectID: "project-1", AgentName: "research",
		Status: "running", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "root-frame", Type: "external_reference",
		Payload: map[string]any{"targetSessionId": "external-session", "kind": "citation"},
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	if err := server.appendClientWebSocketMessage("root-frame", "user", map[string]any{
		"type": "user_message", "text": "Produce an answer.", "clientMessageId": "stream-user",
	}); err != nil {
		t.Fatal(err)
	}
	for index, text := range []string{"partial ", "answer"} {
		if _, err := server.eventJournal.Append("root-frame", eventjournal.Message{
			"type": "content_delta", "role": "assistant", "text": text,
		}, eventjournal.Metadata{ClientMessageID: "delta-" + string(rune('1'+index))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := server.eventJournal.Append("root-frame", eventjournal.Message{
		"type": "session_compact", "role": "system",
		"summary": "Durable compact archive", "compactedThroughEventId": 3,
	}, eventjournal.Metadata{ClientMessageID: "compact-1"}); err != nil {
		t.Fatal(err)
	}
	app := server.Handler()

	stream := getWorkspaceRecoveryJSON(t, app, "/api/go/frames/root-frame/streaming", http.StatusOK)
	buffer := stream["buffer"].(map[string]any)
	if buffer["frameId"] != "root-frame" || buffer["content"] != "partial answer" ||
		buffer["lastEventId"] != float64(4) || buffer["active"] != true {
		t.Fatalf("streaming buffer = %#v", buffer)
	}

	batchBody, _ := json.Marshal(map[string]any{"ids": []any{"root-frame", "missing-frame"}})
	batchResponse := httptest.NewRecorder()
	batchRequest := httptest.NewRequest(http.MethodPost, "/api/go/frames/root-frame/streaming-batch", bytes.NewReader(batchBody))
	batchRequest.Header.Set("Content-Type", "application/json")
	batchRequest.Header.Set("X-Synon-User-Id", "local")
	app.ServeHTTP(batchResponse, batchRequest)
	if batchResponse.Code != http.StatusOK {
		t.Fatalf("streaming batch = %d: %s", batchResponse.Code, batchResponse.Body.String())
	}
	var batch map[string]any
	if err := json.Unmarshal(batchResponse.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	buffers := batch["buffers"].(map[string]any)
	if buffers["root-frame"] == nil || buffers["missing-frame"] != nil {
		t.Fatalf("streaming buffers = %#v", buffers)
	}

	archive := getWorkspaceRecoveryJSON(t, app, "/api/go/frames/root-frame/compaction-archives/0", http.StatusOK)
	item := archive["archive"].(map[string]any)
	if item["summary"] != "Durable compact archive" || item["compaction_index"] != float64(0) || item["message_count"] != float64(3) {
		t.Fatalf("compaction archive = %#v", item)
	}
	archivedMessages := item["messages"].([]any)
	if len(archivedMessages) != 3 || archivedMessages[0].(map[string]any)["type"] != "user_message" {
		t.Fatalf("compaction archive messages = %#v", archivedMessages)
	}
	aggregate := getWorkspaceRecoveryJSON(t, app, "/api/go/frames/root-frame/compaction-messages?through=0", http.StatusOK)
	if messages := aggregate["messages"].([]any); len(messages) != 3 {
		t.Fatalf("compaction aggregate = %#v", aggregate)
	}

	refs := getWorkspaceRecoveryJSON(t, app, "/api/go/frames/root-frame/cross-session-refs", http.StatusOK)
	references := refs["references"].([]any)
	if len(references) != 1 || references[0].(map[string]any)["targetId"] != "external-session" {
		t.Fatalf("cross-session refs = %#v", references)
	}
}

func TestWorkspaceCompactionMessagesUseArchiveIndexesAndFilterBoundaries(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	indexZero := 0
	if _, err := store.CreateCompactionArchive(workspace.CreateCompactionArchiveInput{
		FrameID: "frame", CompactionIndex: &indexZero, MessageCount: 2, Summary: "zero",
		Messages: []map[string]any{
			{"text": "first"},
			{"text": "boundary", "_compact_boundary": true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	indexTwo := 2
	if _, err := store.CreateCompactionArchive(workspace.CreateCompactionArchiveInput{
		FrameID: "frame", CompactionIndex: &indexTwo, MessageCount: 1, Summary: "two",
		Messages: []map[string]any{{"text": "third"}},
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, FileRoot: t.TempDir()}).Handler()

	assertAggregate := func(target string, wantThrough float64, wantTexts ...string) {
		t.Helper()
		response := getWorkspaceRecoveryJSON(t, app, target, http.StatusOK)
		if response["through"] != wantThrough || response["archive_count"] != float64(2) {
			t.Fatalf("GET %s metadata = %#v", target, response)
		}
		messages := response["messages"].([]any)
		if len(messages) != len(wantTexts) {
			t.Fatalf("GET %s messages = %#v", target, messages)
		}
		for index, want := range wantTexts {
			if messages[index].(map[string]any)["text"] != want {
				t.Fatalf("GET %s messages = %#v", target, messages)
			}
		}
	}
	assertAggregate("/api/go/frames/frame/compaction-messages?through=0", 0, "first")
	assertAggregate("/api/go/frames/frame/compaction-messages?through=1", 1, "first")
	assertAggregate("/api/go/frames/frame/compaction-messages?through=2", 2, "first", "third")
	assertAggregate("/api/go/frames/frame/compaction-messages?through=99", 99, "first", "third")
	assertAggregate("/api/go/frames/frame/compaction-messages", 2, "first", "third")
	getWorkspaceRecoveryJSON(t, app, "/api/go/frames/frame/compaction-messages?through=bad", http.StatusBadRequest)
}

func getWorkspaceRecoveryJSON(t *testing.T, app http.Handler, target string, wantStatus int) map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("X-Synon-User-Id", "local")
	app.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("GET %s status = %d: %s", target, response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
