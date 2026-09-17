package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestFrameRunnerUsesTranscriptAuthorityEndToEnd(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-a", "frame-a")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var input struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		if len(input.Messages) == 0 || input.Messages[len(input.Messages)-1].Role != "user" ||
			!strings.Contains(input.Messages[len(input.Messages)-1].Content, "analyze transcript authority") {
			t.Fatalf("provider messages=%#v", input.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"authority completed"}}]}`))
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-a", MessageUUID: "message-a", ClientMessageID: "client-a", Text: "analyze transcript authority",
		RuntimeConfig: map[string]any{"effort": "high", "memory_mode": "on"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-a")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	runtimeConfig, found, err := repo.LatestFrameRuntimeConfig(context.Background(), stream.UID, "local")
	if err != nil || !found || runtimeConfig["effort"] != "high" || runtimeConfig["memory_mode"] != "on" {
		t.Fatalf("runtime config=%#v found=%t err=%v", runtimeConfig, found, err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-a", MessageUUID: "message-a", ClientMessageID: "client-a", Text: "analyze transcript authority",
		RuntimeConfig: map[string]any{"effort": "low", "memory_mode": "on"},
	}); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("conflicting runtime config error=%v", err)
	}
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-a", RunnerID: "runner-a", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 1 << 20,
		DisableSkillDiscovery: true,
	})
	if err != nil || !result.Claimed || result.Status != "completed" || requests.Load() != 1 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	stream, found, err = repo.GetFrameStreamBySession(context.Background(), "local", "frame-a")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	runtimeState, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, "local", 1)
	if err != nil || runtimeState.Status != "completed" || runtimeState.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("runtime=%#v err=%v", runtimeState, err)
	}
	frame, found, err := store.GetFrame("frame-a")
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	stream, err = repo.GetStream(context.Background(), stream.UID, "local")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, event := range projected {
		types = append(types, event.Event.Type)
	}
	if len(types) < 4 || types[0] != "user_message" || types[len(types)-2] != "assistant_message" ||
		types[len(types)-1] != "runner_finished" || countStrings(types, "runner_finished") != 1 ||
		countStrings(types, "assistant_message") != 1 || !onlyStringValues(types[1:len(types)-2], "runner_checkpoint") {
		t.Fatalf("projected event types=%v", types)
	}
	history, authoritative, err := server.loadTranscriptWebHistory(context.Background(), "local", "frame-a")
	if err != nil || !authoritative || len(history) != 2 || webString(history[1]["terminal_status"]) != "completed" ||
		transcriptPayloadText(history[1]["content"].(map[string]any)) != "authority completed" {
		t.Fatalf("history=%#v authoritative=%t err=%v", history, authoritative, err)
	}
	second, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-a", RunnerID: "runner-b", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, ReplayLimit: 100,
	})
	if err != nil || second.Claimed || requests.Load() != 1 {
		t.Fatalf("second=%#v requests=%d err=%v", second, requests.Load(), err)
	}
}

func TestFrameRunnerAutoCompactPersistsTranscriptBoundary(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-auto-compact", "frame-auto-compact")
	var requests atomic.Int64
	var compactRequests atomic.Int64
	compactProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compactRequests.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer compactProvider.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var input struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		compactContext := ""
		for _, message := range input.Messages {
			if message.Role == "system" && strings.Contains(message.Content, "Synon compact handoff context:") {
				compactContext = message.Content
			}
		}
		if !strings.Contains(compactContext, "transcript auto compact request") {
			t.Fatalf("missing transcript compact context: %#v", input.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"transcript compact completed"}}]}`))
	}))
	defer provider.Close()

	server := New(Options{
		Workspace: store, Transcript: repo, FileRoot: t.TempDir(),
		CompactSummarizer: SessionRunnerChatOptions{
			Endpoint: compactProvider.URL + "/v1/chat/completions", Model: "slow-compact-model",
			MaxAttempts: 1, RequestTimeout: time.Minute, CompactSummaryTimeout: 25 * time.Millisecond,
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, err := server.settingsStore.Set(configStoreKey("autoCompactTokenThreshold"), float64(20)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-auto-compact", MessageUUID: "message-auto-compact", ClientMessageID: "client-auto-compact",
		Text: "transcript auto compact request " + strings.Repeat("context ", 80),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-auto-compact", RunnerID: "runner-auto-compact",
		Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
		LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 1 << 20,
		DisableSkillDiscovery: true,
	})
	if err != nil || !result.Claimed || result.Status != "completed" || requests.Load() != 1 || compactRequests.Load() != 0 {
		t.Fatalf("result=%#v requests=%d compactRequests=%d err=%v", result, requests.Load(), compactRequests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-auto-compact")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	replay, err := repo.ListRunnerReplay(context.Background(), transcriptstore.ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: "local", MessageLimit: 100, CheckpointLimit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundBoundary := false
	expectedContextPercent := []byte(fmt.Sprintf(`"contextPercent":%d`, defaultRunnerAutoCompactContextPercent))
	for _, event := range replay {
		if event.Event.Type == "runner_checkpoint" && bytes.Contains(event.ResolvedPayloadJSON, []byte(`"toolPhase":"auto_compact"`)) &&
			bytes.Contains(event.ResolvedPayloadJSON, expectedContextPercent) &&
			bytes.Contains(event.ResolvedPayloadJSON, []byte(`"summaryStatus":"deterministic-context"`)) &&
			bytes.Contains(event.ResolvedPayloadJSON, []byte(`"attempted":false`)) &&
			bytes.Contains(event.ResolvedPayloadJSON, []byte(`"provider":"deterministic"`)) {
			foundBoundary = true
		}
	}
	if !foundBoundary {
		t.Fatalf("missing durable transcript auto-compact checkpoint: %#v", replay)
	}
	archives, err := store.ListCompactionArchives("frame-auto-compact")
	if err != nil || len(archives) != 1 || !strings.Contains(archives[0].Summary, "transcript auto compact request") {
		t.Fatalf("compaction archives=%#v err=%v", archives, err)
	}
	if legacy, err := server.eventJournal.ReadAll("frame-auto-compact"); err != nil || len(legacy) != 0 {
		t.Fatalf("transcript compaction wrote legacy journal entries=%#v err=%v", legacy, err)
	}
}

func TestTranscriptRuntimeConfigUsesLatestExplicitInputAndFailsClosedOnCorruption(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-runtime-config", "frame-runtime-config")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-runtime-config", OwnerID: "local", ExternalID: "frame-runtime-config",
		SessionID: "frame-runtime-config", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-runtime-config", RootFrameID: "frame-runtime-config", FrameID: "frame-runtime-config", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: "local", ClientMessageID: "runtime-config-1",
		FrameEventID: "runtime-config-event-1", MessageUUID: "runtime-config-message-1", Text: "first input",
		RuntimeConfig: map[string]any{"effort": "high", "model": "mimo-v2.5", "verifierMode": "on"},
	}); err != nil || !created {
		t.Fatalf("first input created=%t err=%v", created, err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: "local", ClientMessageID: "runtime-config-2",
		FrameEventID: "runtime-config-event-2", MessageUUID: "runtime-config-message-2", Text: "second input",
		RuntimeConfig: map[string]any{"agentName": "OPERON"},
	}); err != nil || !created {
		t.Fatalf("second input created=%t err=%v", created, err)
	}
	config, found, err := repo.LatestFrameRuntimeConfig(context.Background(), stream.UID, "local")
	if err != nil || !found || config["effort"] != "high" || config["model"] != "mimo-v2.5" ||
		config["verifierMode"] != "on" || config["agentName"] != "OPERON" {
		t.Fatalf("config=%#v found=%t err=%v", config, found, err)
	}
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json='{"runtimeConfig":"invalid"}'
		WHERE stream_uid=? AND client_message_id='runtime-config-1'`, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.LatestFrameRuntimeConfig(context.Background(), stream.UID, "local"); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("corrupt runtime config error=%v", err)
	}
}

func TestFrameRunnerDoesNotUseLegacySessionLeaseAsAuthority(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-a", "frame-a")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"transcript won"}}]}`))
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-a", MessageUUID: "message-a", ClientMessageID: "client-a", Text: "run from transcript",
	}); err != nil {
		t.Fatal(err)
	}
	seedLegacyFrameRunnerProjection(t, server, "frame-a", "legacy-client-a", "legacy pending work")
	legacy, claimed, err := server.sessionStore.ClaimRunner("frame-a", "legacy-runner", time.Hour)
	if err != nil || !claimed || legacy.Runner == nil {
		t.Fatalf("legacy claim=%#v claimed=%t err=%v", legacy.Runner, claimed, err)
	}
	legacy.Title = "untrusted legacy title"
	legacy.WorkDir = "/untrusted/legacy/path"
	legacy.Project = &sessionstore.Project{ID: "foreign-project", Name: "foreign", Path: "/foreign"}
	if err := server.sessionStore.Save(legacy); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-a")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	projection, err := server.loadTranscriptFrameSessionProjection(stream)
	if err != nil || projection.ID != "frame-a" || projection.Title != "frame-a" || projection.Project == nil ||
		projection.Project.ID != "project-a" || projection.WorkDir == "/untrusted/legacy/path" {
		t.Fatalf("canonical projection=%#v err=%v", projection, err)
	}

	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-a", RunnerID: "transcript-runner", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, ReplayLimit: 100,
		OutputLimitBytes: 1 << 20, DisableSkillDiscovery: true,
	})
	if err != nil || !result.Claimed || result.Status != "completed" || result.Attempt != 1 || requests.Load() != 1 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	persisted, found, err := server.sessionStore.Get("frame-a")
	if err != nil || !found || persisted.Runner == nil || persisted.Runner.RunnerID != "legacy-runner" {
		t.Fatalf("legacy projection=%#v found=%t err=%v", persisted.Runner, found, err)
	}
	entries, err := server.eventJournal.ReadAfter("frame-a", 0, 100)
	if err != nil || len(entries) != 1 || webString(entries[0].Message["type"]) != "user_message" {
		t.Fatalf("legacy journal entries=%#v err=%v", entries, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, "local", 1)
	if err != nil || state.RunnerID != "transcript-runner" || state.Status != "completed" {
		t.Fatalf("transcript state=%#v err=%v", state, err)
	}
}

func TestFrameRunnerPollsCanonicalTranscriptInsteadOfLegacyQueue(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-a", "frame-a")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"polled transcript"}}]}`))
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-a", MessageUUID: "message-a", ClientMessageID: "client-a", Text: "poll canonical work",
	}); err != nil {
		t.Fatal(err)
	}
	seedLegacyFrameRunnerProjection(t, server, "frame-a", "legacy-client-a", "legacy pending work")
	if _, claimed, err := server.sessionStore.ClaimRunner("frame-a", "legacy-runner", time.Hour); err != nil || !claimed {
		t.Fatalf("legacy claim claimed=%t err=%v", claimed, err)
	}

	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		RunnerID: "transcript-runner", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, ReplayLimit: 100,
		OutputLimitBytes: 1 << 20, DisableSkillDiscovery: true,
	})
	if err != nil || !result.Claimed || result.SessionID != "frame-a" || result.Status != "completed" || requests.Load() != 1 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
}

func seedLegacyFrameRunnerProjection(t *testing.T, server *Server, frameID, clientID, text string) {
	t.Helper()
	if err := server.appendClientWebSocketMessage(frameID, "user", map[string]any{
		"type": "user_message", "clientMessageId": clientID, "messageUuid": clientID, "text": text,
		"content": []any{map[string]any{"type": "text", "text": text}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFrameRunnerReplayDoesNotCompactCheckpointVolume(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-replay", "frame-replay")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-replay", MessageUUID: "message-first", ClientMessageID: "client-first",
		Text: "original research task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-replay")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	first, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "runner-first", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	for index := 0; index < 240; index++ {
		if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: first.Claim, ClientMessageID: fmt.Sprintf("checkpoint-volume-%03d", index),
			Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true,
			PayloadJSON: []byte(fmt.Sprintf(`{"status":"running","index":%d}`, index)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "first-failed", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"interrupted"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-replay", MessageUUID: "message-latest", ClientMessageID: "client-latest",
		Text: "continue with the latest clinical and patent evidence",
	}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].Role != "user" ||
			request.Messages[len(request.Messages)-1].Content != "continue with the latest clinical and patent evidence" {
			t.Fatalf("provider messages=%#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"continued from canonical replay"}}]}`))
	}))
	defer provider.Close()
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-replay", RunnerID: "runner-retry", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, ReplayLimit: 20,
		OutputLimitBytes: 1 << 20, DisableSkillDiscovery: true,
	})
	if err != nil || !result.Claimed || result.Status != "completed" || requests.Load() != 1 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
}

func TestFrameRunnerTranscriptAuthorityConvergesFailedAndCancelled(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		store, repo, _ := newTranscriptWebFixture(t)
		seedTranscriptWebFrame(t, store, "local", "project-failed", "frame-failed")
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
		}))
		defer provider.Close()
		server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = server.Close(ctx)
		})
		if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
			FrameID: "frame-failed", MessageUUID: "message-failed", ClientMessageID: "client-failed", Text: "fail safely",
		}); err != nil {
			t.Fatal(err)
		}
		result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
			SessionID: "frame-failed", RunnerID: "runner-failed", Endpoint: provider.URL + "/v1/chat/completions",
			Model: "test-model", LeaseTTL: time.Minute, MaxAttempts: 1, DisableSkillDiscovery: true,
		})
		if err != nil || result.Status != "failed" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		assertFrameTranscriptTerminal(t, store, repo, "frame-failed", "failed", "error")
	})

	t.Run("cancelled", func(t *testing.T) {
		store, repo, _ := newTranscriptWebFixture(t)
		seedTranscriptWebFrame(t, store, "local", "project-cancelled", "frame-cancelled")
		entered := make(chan struct{})
		releaseProvider := make(chan struct{})
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			select {
			case <-r.Context().Done():
			case <-releaseProvider:
			}
		}))
		defer provider.Close()
		server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = server.Close(ctx)
		})
		if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
			FrameID: "frame-cancelled", MessageUUID: "message-cancelled", ClientMessageID: "client-cancelled", Text: "cancel safely",
		}); err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			result SessionRunnerCycleResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
				SessionID: "frame-cancelled", RunnerID: "runner-cancelled", Endpoint: provider.URL + "/v1/chat/completions",
				Model: "test-model", LeaseTTL: time.Minute, MaxAttempts: 1,
				DisableSkillDiscovery: true,
			})
			done <- outcome{result: result, err: err}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("provider was not called")
		}
		if stopped, runnerID := server.stopActiveSessionRun("frame-cancelled", "test cancellation"); !stopped || runnerID != "runner-cancelled" {
			t.Fatalf("stop stopped=%t runner=%q", stopped, runnerID)
		}
		close(releaseProvider)
		select {
		case completed := <-done:
			if completed.err != nil || completed.result.Status != "cancelled" {
				t.Fatalf("result=%#v err=%v", completed.result, completed.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled runner did not terminate")
		}
		assertFrameTranscriptTerminal(t, store, repo, "frame-cancelled", "cancelled", "finish")
	})
}

func TestFrameRunnerPublishesValidatedFinalCandidateBeforeTerminal(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-stream", "frame-stream")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "stream-model" || !request.Stream {
			http.Error(w, "stream request required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, payload := range []string{
			`{"id":"transcript-stream","choices":[{"delta":{"role":"assistant","content":"streamed "}}]}`,
			`{"id":"transcript-stream","choices":[{"delta":{"content":"authority"}}]}`,
		} {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, err := server.settingsStore.Set("model.activeProviderId", "provider-stream"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "stream-secret", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "provider-stream", UserID: "local", Name: "Stream", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "stream-model", SecretRef: "secret://stream-secret", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-stream", MessageUUID: "message-stream", ClientMessageID: "client-stream", Text: "stream through authority",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-stream", RunnerID: "runner-stream", Endpoint: "http://127.0.0.1:1/v1/chat/completions",
		Model: "wrong-model", LeaseTTL: time.Minute, MaxAttempts: 1,
		DisableSkillDiscovery: true,
	})
	if err != nil || result.Status != "completed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-stream")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	stream, err = repo.GetStream(context.Background(), stream.UID, "local")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(projected))
	var deltaText string
	var assistantText string
	terminals := 0
	for _, event := range projected {
		types = append(types, event.Event.Type)
		if event.Event.Type == "content_delta" {
			deltaText += transcriptPayloadText(mustTranscriptPayloadObject(t, event.ResolvedPayloadJSON))
		}
		if event.Event.Type == "assistant_message" {
			assistantText = transcriptPayloadText(mustTranscriptPayloadObject(t, event.ResolvedPayloadJSON))
		}
		if event.Event.Type == "runner_finished" {
			terminals++
		}
	}
	assistantIndex := lastStringIndex(types, "assistant_message")
	terminalIndex := lastStringIndex(types, "runner_finished")
	if len(types) < 4 || types[0] != "user_message" || assistantIndex <= 0 || terminalIndex != len(types)-1 ||
		assistantIndex != terminalIndex-1 || countStrings(types, "assistant_message") != 1 || terminals != 1 ||
		countStrings(types, "content_delta") != 0 || deltaText != "" || assistantText != "streamed authority" ||
		!onlyStringValuesExcept(types[1:assistantIndex], "runner_checkpoint", "content_delta") {
		t.Fatalf("types=%v deltas=%q assistant=%q terminals=%d", types, deltaText, assistantText, terminals)
	}
}

func countStrings(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func lastStringIndex(values []string, target string) int {
	for index := len(values) - 1; index >= 0; index-- {
		if values[index] == target {
			return index
		}
	}
	return -1
}

func onlyStringValues(values []string, allowed string) bool {
	return onlyStringValuesExcept(values, allowed)
}

func onlyStringValuesExcept(values []string, allowed ...string) bool {
	for _, value := range values {
		matched := false
		for _, candidate := range allowed {
			if value == candidate {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func TestFrameRunnerKeepsLegacyArtifactAliasOutOfTheModelSnapshot(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-artifact", "frame-artifact")
	fileRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(fileRoot, "report.txt"), []byte("authoritative report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	var advertisedLegacy atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		for _, tool := range request.Tools {
			if strings.EqualFold(strings.TrimSpace(tool.Function.Name), "artifact_register") {
				advertisedLegacy.Store(true)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if requestNumber == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"artifact-call","type":"function","function":{"name":"artifact_register","arguments":"{\"path\":\"report.txt\",\"kind\":\"report\"}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"report is ready"}}]}`))
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: fileRoot})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-artifact", MessageUUID: "message-artifact", ClientMessageID: "client-artifact",
		Text: "create and register the report",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-artifact", RunnerID: "runner-artifact", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, ReplayLimit: 100,
		OutputLimitBytes: 1 << 20, DisableSkillDiscovery: true,
		AllowedTools: []string{"artifact_register"},
	})
	if err != nil || result.Status != "completed" || requests.Load() != 2 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-artifact")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	stream, err = repo.GetStream(context.Background(), stream.UID, "local")
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	var assistantRefs, terminalRefs []transcriptstore.ArtifactReference
	for _, event := range events {
		switch event.Event.Type {
		case "assistant_message":
			assistantRefs = event.ArtifactReferences
		case "runner_finished":
			terminalRefs = event.ArtifactReferences
		}
	}
	artifacts, artifactErr := store.ListArtifacts("project-artifact", 20, 0)
	if advertisedLegacy.Load() || len(assistantRefs) != 0 || len(terminalRefs) != 0 || artifactErr != nil || len(artifacts) != 0 {
		t.Fatalf("advertisedLegacy=%t assistant refs=%#v terminal refs=%#v artifacts=%#v err=%v events=%#v",
			advertisedLegacy.Load(), assistantRefs, terminalRefs, artifacts, artifactErr, events)
	}
}

func TestTranscriptArtifactRegistrationVersionsOneLogicalPath(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-artifact-versions", "frame-artifact-versions")
	fileRoot := t.TempDir()
	path := filepath.Join(fileRoot, "report.md")
	if err := os.WriteFile(path, []byte("version one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: fileRoot})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-artifact-versions", MessageUUID: "message-artifact-versions",
		ClientMessageID: "client-artifact-versions", Text: "create and revise the report",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-artifact-versions")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-artifact-versions",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	register := func(callID string) map[string]any {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"toolCallId": callID, "toolName": "artifact_register", "toolPhase": "start",
			"toolInput": map[string]any{"path": "report.md", "kind": "report"},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, source, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: claimed.Claim, ClientMessageID: "artifact-source-" + callID,
			Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := server.registerTranscriptArtifact(context.Background(), transcriptArtifactRun{
			Authority: authority, SourceEventID: source.EventID,
		}, map[string]any{"path": "report.md", "kind": "report"})
		if err != nil {
			t.Fatal(err)
		}
		return result["artifact"].(map[string]any)
	}
	first := register("first")
	if err := os.WriteFile(path, []byte("version two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := register("second")
	third := register("third-identical")
	if first["artifactId"] != second["artifactId"] || first["versionId"] == second["versionId"] {
		t.Fatalf("artifact revisions did not retain logical identity: first=%#v second=%#v", first, second)
	}
	if third["artifactId"] != second["artifactId"] || third["versionId"] != second["versionId"] {
		t.Fatalf("identical registration created a redundant version: second=%#v third=%#v", second, third)
	}
	artifact, history, found, err := store.ListArtifactVersionHistory(stringValue(first["artifactId"]))
	if err != nil || !found || len(history) != 2 || artifact.CurrentVersionNumber != 2 {
		t.Fatalf("artifact=%#v history=%#v found=%t err=%v", artifact, history, found, err)
	}
	if history[0].VersionID != first["versionId"] || history[0].VersionNumber != 1 ||
		history[1].VersionID != second["versionId"] || history[1].VersionNumber != 2 {
		t.Fatalf("version history=%#v first=%#v second=%#v", history, first, second)
	}
	var commits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND artifact_id=?`,
		stream.UID, claimed.Claim.Attempt, first["artifactId"],
	).Scan(&commits); err != nil || commits != 3 {
		t.Fatalf("artifact transcript commits=%d err=%v", commits, err)
	}
	if other := transcriptArtifactIDFor("local", "project-artifact-versions", "frame:other", "report.md"); other == first["artifactId"] {
		t.Fatalf("different transcript streams shared artifact identity %q", other)
	}
	jsonPath := filepath.Join(fileRoot, "results.json")
	if err := os.WriteFile(jsonPath, []byte(`{"value":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonPayload, err := json.Marshal(map[string]any{
		"toolCallId": "json", "toolName": "artifact_register", "toolPhase": "start",
		"toolInput": map[string]any{"path": "results.json", "kind": "data"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, jsonSource, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "artifact-source-json",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: jsonPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonArtifact, err := server.registerTranscriptArtifact(context.Background(), transcriptArtifactRun{
		Authority: authority, SourceEventID: jsonSource.EventID,
	}, map[string]any{"path": "results.json", "kind": "data"})
	if err != nil || stringValue(mapValue(jsonArtifact["artifact"])["fileName"]) != "results.json" {
		t.Fatalf("valid JSON artifact registration result=%#v error=%v", jsonArtifact, err)
	}
}

func assertFrameTranscriptTerminal(
	t *testing.T,
	store interface {
		GetFrame(string) (workspace.Frame, bool, error)
	},
	repo *transcriptstore.Repository,
	frameID, status, streamType string,
) {
	t.Helper()
	frame, found, err := store.GetFrame(frameID)
	if err != nil || !found || frame.Status != status {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", frameID)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	runtimeState, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, "local", 1)
	if err != nil || runtimeState.Status != status || runtimeState.FinishedEventID <= 0 {
		t.Fatalf("runtime=%#v err=%v", runtimeState, err)
	}
	snapshot, err := repo.GetProjectionSnapshot(context.Background(), stream.UID, "local")
	if err != nil {
		t.Fatal(err)
	}
	foundTerminal := false
	afterPublication := int64(0)
	for afterPublication < snapshot.ThroughPublicationSequence {
		events, listErr := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
			StreamUID: stream.UID, OwnerID: "local",
			BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
			AfterPublicationSequence:   afterPublication,
			ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: 20,
		})
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(events) == 0 {
			break
		}
		for _, candidate := range events {
			if candidate.Event.EventID != runtimeState.FinishedEventID {
				continue
			}
			if candidate.Event.Type != "runner_finished" {
				t.Fatalf("finish receipt mapped to event=%#v", candidate.Event)
			}
			foundTerminal = true
			break
		}
		next := events[len(events)-1].Event.PublicationSeq
		if next <= afterPublication {
			t.Fatalf("projected event cursor did not advance: previous=%d next=%d", afterPublication, next)
		}
		afterPublication = next
		if foundTerminal {
			break
		}
	}
	if !foundTerminal {
		t.Fatalf("terminal event %d is absent from active branch snapshot %#v", runtimeState.FinishedEventID, snapshot)
	}
	projection, err := repo.GetTerminalProjection(
		context.Background(), "local", stream.UID, runtimeState.FinishedEventID,
	)
	if err != nil || projection.EventID != runtimeState.FinishedEventID || projection.Attempt != runtimeState.Attempt ||
		projection.TerminalStatus != status || projection.StreamType != streamType {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
}
