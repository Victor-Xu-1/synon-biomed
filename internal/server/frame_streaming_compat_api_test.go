package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityStreamingBuffersRecoverActiveRootJournal(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "child", ProjectID: "project", ParentFrameID: root.ID,
		AgentName: "RESEARCH", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "terminal", ProjectID: "project", ParentFrameID: root.ID,
		AgentName: "RESEARCH", Status: "completed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "foreign", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, FileRoot: t.TempDir()})
	appendStreamingFixture(t, server.eventJournal, root.ID, "root answer", "root thought")
	appendStreamingFixture(t, server.eventJournal, child.ID, "child answer", "")
	appendStreamingFixture(t, server.eventJournal, terminal.ID, "historical answer", "")
	appendStreamingFixture(t, server.eventJournal, foreign.ID, "foreign answer", "")
	app := server.Handler()

	single := compatJSONRequest(t, app, http.MethodGet, "/api/frames/root/streaming", "local", nil, http.StatusOK)
	if single["frame_id"] != root.ID || single["text"] != "root answer" || single["thinking"] != "root thought" {
		t.Fatalf("single streaming buffer = %#v", single)
	}
	if stdout, ok := single["tool_stdout"].([]any); !ok || len(stdout) != 0 {
		t.Fatalf("single tool stdout = %#v", single["tool_stdout"])
	}

	batch := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/streaming-batch", "local", map[string]any{}, http.StatusOK)
	if batch["root_frame_id"] != root.ID {
		t.Fatalf("batch root = %#v", batch)
	}
	buffers, ok := batch["buffers"].([]any)
	if !ok || len(buffers) != 2 {
		t.Fatalf("batch buffers = %#v", batch["buffers"])
	}
	if buffers[0].(map[string]any)["frame_id"] != root.ID || buffers[1].(map[string]any)["frame_id"] != child.ID {
		t.Fatalf("batch order/scope = %#v", buffers)
	}
	if _, scanned := server.streamingCache[terminal.ID]; scanned {
		t.Fatalf("terminal history was scanned into the live projection cache")
	}

	if _, err := server.eventJournal.Append(root.ID, eventjournal.Message{
		"type": "runner_finished", "role": "system",
	}, eventjournal.Metadata{ClientMessageID: "root-finished"}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := compatRequest(t, http.MethodGet, "/api/frames/root/streaming", "local", nil)
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK || string(bytes.TrimSpace(response.Body.Bytes())) != "null" {
		t.Fatalf("completed stream = %d %q", response.Code, response.Body.String())
	}
}

func TestCompatibilityStreamingProjectionUsesBoundedIncrementalFrameCache(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-cache", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, FileRoot: t.TempDir()})
	for frameIndex := 0; frameIndex < compatibilityStreamingRetainedFrames+1; frameIndex++ {
		frameID := fmt.Sprintf("cache-frame-%02d", frameIndex)
		if _, err := store.CreateFrame(workspace.CreateFrameInput{
			ID: frameID, ProjectID: "project-cache", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := server.eventJournal.Append(frameID, eventjournal.Message{
			"type": "user_message", "role": "user", "text": "question",
		}, eventjournal.Metadata{ClientMessageID: frameID + "-user"}); err != nil {
			t.Fatal(err)
		}
		if _, err := server.frameStreamingBuffer(frameID); err != nil {
			t.Fatal(err)
		}
	}
	if len(server.streamingCache) != compatibilityStreamingRetainedFrames {
		t.Fatalf("retained frame cache=%d", len(server.streamingCache))
	}

	const frameID = "cache-frame-08"
	var wantText string
	for index := 0; index < compatibilityStreamingRetainedEntries+100; index++ {
		chunk := fmt.Sprintf("%03d", index)
		wantText += chunk
		if _, err := server.eventJournal.Append(frameID, eventjournal.Message{
			"type": "content_delta", "role": "assistant", "text": chunk,
		}, eventjournal.Metadata{ClientMessageID: fmt.Sprintf("cache-delta-%03d", index)}); err != nil {
			t.Fatal(err)
		}
	}
	buffer, err := server.frameStreamingBuffer(frameID)
	if err != nil {
		t.Fatal(err)
	}
	if buffer.Content != wantText || len(buffer.Entries) != compatibilityStreamingRetainedEntries {
		t.Fatalf("bounded projection content=%d/%d entries=%d", len(buffer.Content), len(wantText), len(buffer.Entries))
	}
	beforeOffset := server.streamingCache[frameID].cursor.ByteOffset
	if _, err := server.eventJournal.Append(frameID, eventjournal.Message{
		"type": "content_delta", "role": "assistant", "text": "tail",
	}, eventjournal.Metadata{ClientMessageID: "cache-tail"}); err != nil {
		t.Fatal(err)
	}
	buffer, err = server.frameStreamingBuffer(frameID)
	if err != nil {
		t.Fatal(err)
	}
	if buffer.Content != wantText+"tail" || len(buffer.Entries) != compatibilityStreamingRetainedEntries ||
		server.streamingCache[frameID].cursor.ByteOffset <= beforeOffset {
		t.Fatalf("incremental projection content=%d entries=%d cursor=%#v", len(buffer.Content), len(buffer.Entries), server.streamingCache[frameID].cursor)
	}
}

func TestCompatibilityStreamingBatchExposesCanonicalHistoryRevision(t *testing.T) {
	store, repository, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project", "root")
	server := New(Options{Workspace: store, Transcript: repository, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "root", MessageUUID: "message-1", ClientMessageID: "client-1", Text: "question",
	}); err != nil {
		t.Fatal(err)
	}

	batch := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/root/streaming-batch", "local", map[string]any{}, http.StatusOK)
	if batch["history_revision"] != float64(1) {
		t.Fatalf("history revision = %#v", batch)
	}
}

func TestCompatibilityStreamingExposesRealKernelStdout(t *testing.T) {
	manager := newServerKernelTestManager(t)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	workspaceDir := t.TempDir()
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "kernel-http-stream", FrameID: "root", RootFrameID: "root",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Submit(kernelruntime.SubmitRequest{
		FrameID: "root", Language: "python", Environment: "python",
		ExecID: "exec-http-stream", ToolUseID: "tool-http-stream", Origin: "user",
		Code: "from pathlib import Path\nprint('live stdout', flush=True)\nPath('http-stream-started').write_text('ready')\nwhile True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Options{
		Workspace: store, KernelManager: manager, FileRoot: t.TempDir(), StartBackgroundServices: true,
	})
	defer closeTestServer(t, server)
	appendStreamingFixture(t, server.eventJournal, "root", "live answer", "")
	app := server.Handler()
	waitForFile(t, filepath.Join(workspaceDir, "http-stream-started"))
	var stream map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stream = compatJSONRequest(t, app, http.MethodGet, "/api/frames/root/streaming", "local", nil, http.StatusOK)
		stdout, _ := stream["tool_stdout"].([]any)
		if len(stdout) == 1 {
			entry := stdout[0].(map[string]any)
			if entry["tool_use_id"] != "tool-http-stream" || entry["stdout"] != "live stdout\n" {
				t.Fatalf("tool stdout entry = %#v", entry)
			}
			assertCompatibilityExecStreamWatermarks(t, entry, "live stdout\n")
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stdout, _ := stream["tool_stdout"].([]any); len(stdout) != 1 {
		t.Fatalf("live kernel stream = %#v", stream)
	}
	rootAuthoritativeBatch := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/streaming-batch", "local", map[string]any{}, http.StatusOK)
	rootBuffers := rootAuthoritativeBatch["buffers"].([]any)
	if len(rootBuffers) != 1 || len(rootBuffers[0].(map[string]any)["tool_stdout"].([]any)) != 1 {
		t.Fatalf("root-authoritative batch stdout = %#v", rootAuthoritativeBatch)
	}
	batchEntry := rootBuffers[0].(map[string]any)["tool_stdout"].([]any)[0].(map[string]any)
	assertCompatibilityExecStreamWatermarks(t, batchEntry, "live stdout\n")
	legacyRequestedExec := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/streaming-batch", "local", map[string]any{
		"ids": []string{"root"},
	}, http.StatusBadRequest)
	if legacyRequestedExec["detail"] == nil {
		t.Fatalf("legacy requested batch error = %#v", legacyRequestedExec)
	}
	if result := manager.Interrupt("root", "exec-http-stream"); !result.Interrupted {
		t.Fatalf("interrupt = %#v", result)
	}
	select {
	case <-handle.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("kernel execution did not finish")
	}
}

func TestKernelStdoutPublishesDurableRealtimeChunks(t *testing.T) {
	manager := newServerKernelTestManager(t)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-live", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root-live", ProjectID: "project-live", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{
		Workspace: store, KernelManager: manager, FileRoot: t.TempDir(), StartBackgroundServices: true,
	})
	defer closeTestServer(t, server)
	if err := server.sessionStore.Upsert(sessionstore.Session{ID: "root-live", Title: "Live stdout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "kernel-live", FrameID: "root-live", RootFrameID: "root-live",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Submit(kernelruntime.SubmitRequest{
		FrameID: "root-live", Language: "python", Environment: "python", ExecID: "exec-live",
		ToolUseID: "tool-live", ToolName: "python", Origin: "agent", Code: "print('first', flush=True)\nprint('second', flush=True)",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-handle.Done():
		if outcome.Err != nil {
			t.Fatal(outcome.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("kernel stdout publication timed out")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, listErr := store.ListRealtimeEvents(workspace.RealtimeEventFilter{UserID: "local", Limit: 20})
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, event := range events {
			if event.Type != "tool_stdout_chunk" {
				continue
			}
			if event.Payload["tool_use_id"] != "tool-live" || event.Payload["chunk"] == "" {
				t.Fatalf("tool stdout event=%#v", event)
			}
			chunk, ok := event.Payload["chunk"].(string)
			sequence, sequenceOK := event.Payload["chunk_sequence"].(float64)
			start, startOK := event.Payload["chunk_start_byte"].(float64)
			end, endOK := event.Payload["chunk_end_byte"].(float64)
			startedAt, startedAtOK := event.Payload["started_at"].(string)
			background, backgroundOK := event.Payload["background"].(bool)
			if !ok || !sequenceOK || !startOK || !endOK || sequence < 1 || start < 0 || end < start ||
				end-start != float64(len([]byte(chunk))) || !startedAtOK || !backgroundOK || background ||
				event.Payload["status"] != "running" || event.Payload["root_frame_id"] != "root-live" {
				t.Fatalf("tool stdout event watermarks=%#v", event.Payload)
			}
			if _, parseErr := time.Parse(time.RFC3339Nano, startedAt); parseErr != nil {
				t.Fatalf("tool stdout event started_at=%q: %v", startedAt, parseErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("durable tool_stdout_chunk was not published")
}

func assertCompatibilityExecStreamWatermarks(t *testing.T, entry map[string]any, stdout string) {
	t.Helper()
	sequence, sequenceOK := entry["through_chunk_sequence"].(float64)
	start, startOK := entry["stdout_start_byte"].(float64)
	end, endOK := entry["stdout_end_byte"].(float64)
	startedAt, startedAtOK := entry["started_at"].(string)
	if !sequenceOK || !startOK || !endOK || sequence < 1 || start < 0 || end < start ||
		end-start != float64(len([]byte(stdout))) || !startedAtOK || entry["status"] != "running" {
		t.Fatalf("compatibility stdout watermarks=%#v", entry)
	}
	if _, err := time.Parse(time.RFC3339Nano, startedAt); err != nil {
		t.Fatalf("compatibility stdout started_at=%q: %v", startedAt, err)
	}
}

func appendStreamingFixture(t *testing.T, journal *eventjournal.EventJournal, frameID, text, thinking string) {
	t.Helper()
	entries := []eventjournal.Message{{"type": "user_message", "role": "user", "text": "question"}}
	if text != "" {
		entries = append(entries, eventjournal.Message{"type": "content_delta", "role": "assistant", "text": text})
	}
	if thinking != "" {
		entries = append(entries, eventjournal.Message{"type": "thinking_delta", "role": "assistant", "text": thinking})
	}
	for index, message := range entries {
		if _, err := journal.Append(frameID, message, eventjournal.Metadata{ClientMessageID: frameID + "-stream-" + string(rune('0'+index))}); err != nil {
			t.Fatal(err)
		}
	}
}

func newServerKernelTestManager(t *testing.T) *kernelruntime.Manager {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	assetRoot := filepath.Join(repositoryRoot, "assets", "optional")
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: python, AssetRoot: assetRoot,
		ManifestPath:    filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:      filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		ShutdownTimeout: 2 * time.Second,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = manager.CloseAll(ctx)
	})
	return manager
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s was not created", path)
}
