package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/memoryconfig"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestMemoryToolErrorsAreBoundedAtTheRunnerBoundary(t *testing.T) {
	internal := errors.New("read memory rows: sql: database is closed at /private/workspace.sqlite")
	if got := boundedMemoryScopeError("read_memory", internal); got != "unavailable" {
		t.Fatalf("scope error=%q", got)
	}
	if got := boundedMemoryOperationError("read_memory", internal); got != "read_memory failed" {
		t.Fatalf("operation error=%q", got)
	}
	input := errors.New("Missing 'query' argument")
	if got := boundedMemoryOperationError("search_memory", input); got != input.Error() {
		t.Fatalf("input error=%q", got)
	}
}

func TestTranscriptRunnerDiscoversMemoryToolsForAuthenticatedWebOwner(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	const (
		ownerID   = "authenticated-web-owner"
		projectID = "project-memory-web-owner"
		frameID   = "frame-memory-web-owner"
	)
	seedTranscriptWebFrame(t, store, ownerID, projectID, frameID)
	if err := store.SetMemoryEnabled(context.Background(), ownerID, true); err != nil {
		t.Fatal(err)
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		tools, ok := body["tools"].([]any)
		if !ok {
			t.Fatalf("provider tools=%#v", body["tools"])
		}
		for _, name := range []string{"read_memory", "write_memory", "search_memory"} {
			if !hasChatToolNamed(tools, name) {
				t.Fatalf("authenticated web owner omitted %s: %#v", name, body["tools"])
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Memory tools are available."}}]}`))
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	server.memoryConfig = memoryconfig.Default()
	server.memoryConfig.PIClassifierEnabled = false
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frameID, MessageUUID: "message-memory-web-owner", ClientMessageID: "client-memory-web-owner",
		Text: "Recall the project memory.", RuntimeConfig: map[string]any{"memory_mode": "on"},
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, server, ownerID, frameID)

	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: frameID, RunnerID: "memory-web-owner-runner", Endpoint: provider.URL + "/v1/chat/completions",
		Model: "local-openai-compatible", LeaseTTL: time.Minute,
		ReplayLimit: 100, OutputLimitBytes: 1 << 20, DisableSkillDiscovery: true,
	})
	if err != nil || !result.Claimed || result.Status != "completed" {
		t.Fatalf("runner result=%#v err=%v", result, err)
	}
}

func TestTranscriptRunnerExecutesClaimedMemoryWriteThroughOpenAIProtocol(t *testing.T) {
	store, repo, database := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-memory-runner", "frame-memory-runner")
	if err := store.SetMemoryEnabled(context.Background(), "local", true); err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int64
	var openingRequests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		// A fresh tool-only response is preceded by one optional, tool-free
		// orientation request. It is a presentation concern, not an action
		// round, so keep it out of the durable memory-action sequence.
		if choice, ok := body["tool_choice"].(string); ok && choice == "none" {
			openingRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Review the requested scope before recording the durable memory."}}]}`))
			return
		}
		sequence := requests.Add(1)
		switch sequence {
		case 1:
			tools, ok := body["tools"].([]any)
			if !ok || !hasChatToolNamed(tools, "write_memory") {
				t.Fatalf("write_memory schema missing from provider request: %#v", body["tools"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_memory_1","type":"function","function":{"name":"write_memory","arguments":"{\"append\":[{\"text\":\"NEK7 assay licenses are reviewed quarterly\",\"evidence\":\"observed\"}]}"}}]}}]}`))
		case 2:
			messages, ok := body["messages"].([]any)
			if !ok || !hasToolResultMessage(messages, "call_memory_1", "Memory updated: 1 appended") {
				t.Fatalf("provider did not receive the claimed memory result: %#v", body["messages"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_memory_2","type":"function","function":{"name":"search_memory","arguments":"{\"query\":\"NEK7 assay licenses\"}"}}]}}]}`))
		case 3:
			messages, ok := body["messages"].([]any)
			if !ok || !hasToolResultMessage(messages, "call_memory_2", "NEK7 assay licenses are reviewed quarterly") {
				t.Fatalf("provider did not receive durable memory search result: %#v", body["messages"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Stored and verified the quarterly NEK7 memory."}}]}`))
		case 4:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_memory_3","type":"function","function":{"name":"search_memory","arguments":"{\"query\":\"NEK7 assay licenses\"}"}}]}}]}`))
		case 5:
			messages, ok := body["messages"].([]any)
			if !ok || !hasToolResultMessage(messages, "call_memory_3", "NEK7 assay licenses are reviewed quarterly") {
				t.Fatalf("provider did not receive replayed durable memory search result: %#v", body["messages"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Recovered the durable quarterly NEK7 memory."}}]}`))
		default:
			t.Fatalf("unexpected provider request %d", sequence)
		}
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	server.memoryConfig = memoryconfig.Default()
	server.memoryConfig.PIClassifierEnabled = false
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-memory-runner", MessageUUID: "message-memory-runner", ClientMessageID: "client-memory-runner",
		Text: "Remember our quarterly NEK7 assay license review.", RuntimeConfig: map[string]any{"memory_mode": "on"},
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, server, "local", "frame-memory-runner")

	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-memory-runner", RunnerID: "memory-runner", Endpoint: provider.URL + "/v1/chat/completions",
		Model: "local-openai-compatible", AllowedTools: []string{"write_memory", "search_memory"}, LeaseTTL: time.Minute,
		ReplayLimit: 100, OutputLimitBytes: 1 << 20, DisableSkillDiscovery: true,
	})
	if err != nil || !result.Claimed || result.Status != "completed" || requests.Load() != 3 || openingRequests.Load() > 1 {
		t.Fatalf("runner result=%#v action_requests=%d opening_requests=%d err=%v", result, requests.Load(), openingRequests.Load(), err)
	}

	memories, err := store.ListMemoriesForUser(context.Background(), "local", "", "", false)
	if err != nil || len(memories) != 1 || memories[0].Body != "NEK7 assay licenses are reviewed quarterly" || memories[0].Origin != "agent_tool" {
		t.Fatalf("memories=%#v err=%v", memories, err)
	}
	var receipts int
	if err := database.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='memory_mutation_applied'`, "frame:frame-memory-runner").Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipt count=%d err=%v", receipts, err)
	}
	stream, err := repo.GetStream(context.Background(), "frame:frame-memory-runner", "local")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ThroughPublicationSequence: stream.NextPublication - 1, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	var completedTool, finalAssistant bool
	for _, event := range projected {
		payload := string(event.Event.PayloadJSON)
		completedTool = completedTool || event.Event.Type == "runner_checkpoint" && strings.Contains(payload, `"toolName":"write_memory"`) && strings.Contains(payload, `"toolPhase":"completed"`)
		finalAssistant = finalAssistant || event.Event.Type == "assistant_message" && strings.Contains(payload, "Stored and verified the quarterly NEK7 memory.")
	}
	if !completedTool || !finalAssistant {
		t.Fatalf("projected memory tool history completed=%t assistant=%t events=%#v", completedTool, finalAssistant, projected)
	}
	second, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-memory-runner", RunnerID: "memory-runner-replay", Endpoint: provider.URL + "/v1/chat/completions",
		Model: "local-openai-compatible", AllowedTools: []string{"write_memory"}, LeaseTTL: time.Minute,
	})
	if err != nil || second.Claimed || requests.Load() != 3 {
		t.Fatalf("terminal replay=%#v requests=%d err=%v", second, requests.Load(), err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-memory-runner", MessageUUID: "message-memory-runner-2", ClientMessageID: "client-memory-runner-2",
		Text: "What do you remember about NEK7 assay licenses?", RuntimeConfig: map[string]any{"memory_mode": "on"},
	}); err != nil {
		t.Fatal(err)
	}
	recalled, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-memory-runner", RunnerID: "memory-runner-recall", Endpoint: provider.URL + "/v1/chat/completions",
		Model: "local-openai-compatible", AllowedTools: []string{"search_memory"}, LeaseTTL: time.Minute,
		ReplayLimit: 100, OutputLimitBytes: 1 << 20, DisableSkillDiscovery: true,
	})
	if err != nil || !recalled.Claimed || recalled.Status != "completed" || requests.Load() != 5 {
		t.Fatalf("recall result=%#v requests=%d err=%v", recalled, requests.Load(), err)
	}
	if rows, err := store.ListMemoriesForUser(context.Background(), "local", "", "", false); err != nil || len(rows) != 1 {
		t.Fatalf("memory replay rows=%#v err=%v", rows, err)
	}
}
