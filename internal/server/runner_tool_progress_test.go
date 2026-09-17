package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolprogress"
)

func TestSessionRunnerToolProgressCheckpointPersistsRunningState(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	run := &sessionRunnerChatRun{
		SessionID:                      "frame-save",
		RequiredScientificCapabilities: []string{"pocket-conditioned-molecule-generation"},
		Transcript: &transcriptRunnerAuthority{
			Stream: fixture.stream,
			Claim:  fixture.claim,
		},
	}
	phasePercent := float64(42)
	bytesPerSecond := float64(224320)
	completed, total := int64(1), int64(8)
	if err := fixture.server.checkpointSessionRunnerToolEvent(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-save",
	}, run, agentruntime.Event{
		Type:            agentruntime.EventToolProgress,
		ToolName:        "manage_environments",
		ToolCallID:      "long-python-call",
		Arguments:       `{"mode":"create","human_description":"Prepare environment"}`,
		Elapsed:         32 * time.Second,
		ProgressOrdinal: 7,
		Progress: &toolprogress.Update{
			Phase: "downloading_packages", PhasePercent: &phasePercent, BytesPerSecond: &bytesPerSecond,
			CompletedItems: &completed, TotalItems: &total,
		},
	}); err != nil {
		t.Fatalf("persist tool progress: %v", err)
	}

	rows, err := fixture.db.Query(`
		SELECT event_type, payload_json
		FROM transcript_events
		WHERE stream_uid = ?
		ORDER BY event_id`, fixture.stream.UID)
	if err != nil {
		t.Fatalf("query transcript progress: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var eventType string
		var raw []byte
		if err := rows.Scan(&eventType, &raw); err != nil {
			t.Fatalf("scan transcript event: %v", err)
		}
		if eventType != "runner_checkpoint" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode checkpoint: %v", err)
		}
		if payload["toolProgress"] != true {
			continue
		}
		found = true
		if payload["visibility"] == "provisional" {
			t.Fatalf("authoritative environment setup progress was hidden: %#v", payload)
		}
		if payload["status"] != "running" || payload["toolPhase"] != "progress-000007" ||
			payload["toolName"] != "manage_environments" || payload["progressOrdinal"] != float64(7) ||
			payload["elapsedMs"] != float64(32000) {
			t.Fatalf("unexpected progress checkpoint payload: %#v", payload)
		}
		progress, _ := payload["progress"].(map[string]any)
		if progress["phase"] != "downloading_packages" || progress["phasePercent"] != float64(42) ||
			progress["bytesPerSecond"] != float64(224320) ||
			progress["completedItems"] != float64(1) || progress["totalItems"] != float64(8) ||
			progress["elapsedMs"] != float64(32000) {
			t.Fatalf("unexpected structured progress payload: %#v", progress)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate transcript progress: %v", err)
	}
	if !found {
		t.Fatal("durable running tool progress checkpoint was not found")
	}
}

func TestSessionRunnerToolProgressDoesNotInterruptDurableToolBatch(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-progress", "frame-progress")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-progress", MessageUUID: "message-progress",
		ClientMessageID: "client-progress", Text: "run one long local operation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-progress")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "progress-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript:                     &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
		RequiredScientificCapabilities: []string{"sequence-analysis"},
	}
	call := agentruntime.ToolCall{
		ID: "progress-python-call", Name: "python",
		Arguments: json.RawMessage(`{"code":"long_running_computation()","environment":"science"}`),
	}
	options := SessionRunnerChatOptions{SessionID: stream.SessionID, RunnerID: claimed.Claim.RunnerID}
	if err := server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	assertVisibleTool := func(want bool) {
		t.Helper()
		messages, found, err := server.loadTranscriptWebHistory(context.Background(), stream.OwnerID, stream.SessionID)
		if err != nil || !found {
			t.Fatalf("history found=%t err=%v", found, err)
		}
		visible := false
		for _, message := range messages {
			content, _ := message["content"].(map[string]any)
			if message["type"] == "tool_call" && content["call_id"] == call.ID {
				visible = true
				if content["status"] != "running" {
					t.Fatalf("live tool status=%#v", content)
				}
			}
		}
		if visible != want {
			t.Fatalf("visible tool=%t want=%t", visible, want)
		}
	}
	assertVisibleTool(false)
	if err := server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolStarted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments),
	}); err != nil {
		t.Fatal(err)
	}
	var hiddenStarts int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM transcript_events
		WHERE stream_uid = ? AND json_extract(payload_json, '$.toolCallId') = ?
		AND json_extract(payload_json, '$.toolPhase') = 'start'
		AND json_extract(payload_json, '$.visibility') = 'provisional'`,
		stream.UID, call.ID).Scan(&hiddenStarts); err != nil {
		t.Fatal(err)
	}
	if hiddenStarts != 0 {
		t.Fatal("scientific completion requirements hid the admitted live tool")
	}
	assertVisibleTool(true)
	if err := server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolProgress, ToolName: call.Name, ToolCallID: call.ID,
		Arguments: string(call.Arguments), Elapsed: 10 * time.Second, ProgressOrdinal: 1,
	}); err != nil {
		t.Fatalf("durable tool progress interrupted the live batch: %v", err)
	}
	batch, found, err := store.GetToolCallBatch(context.Background(), stream.OwnerID, run.ToolBatchIDs[call.ID])
	if err != nil || !found || batch.State != workspace.ToolCallBatchStateRunning {
		t.Fatalf("batch=%#v found=%t err=%v", batch, found, err)
	}
	items, err := store.ListToolCallBatchItems(context.Background(), stream.OwnerID, batch.BatchID)
	if err != nil || len(items) != 1 || items[0].State != workspace.ToolCallBatchItemStateRunning {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestSessionRunnerToolProgressPersistenceErrorIsBestEffort(t *testing.T) {
	persistenceErr := context.DeadlineExceeded
	if err := sessionRunnerToolProgressPersistenceBestEffort(agentruntime.Event{
		Type: agentruntime.EventToolProgress, ToolCallID: "progress-call",
	}, persistenceErr); err != nil {
		t.Fatalf("progress persistence error interrupted execution: %v", err)
	}
	if err := sessionRunnerToolProgressPersistenceBestEffort(agentruntime.Event{
		Type: agentruntime.EventToolStarted, ToolCallID: "start-call",
	}, persistenceErr); err != persistenceErr {
		t.Fatalf("authoritative start persistence error was hidden: %v", err)
	}
	if err := sessionRunnerToolProgressPersistenceBestEffort(agentruntime.Event{
		Type: agentruntime.EventToolCompleted, ToolCallID: "terminal-call",
	}, persistenceErr); err != persistenceErr {
		t.Fatalf("authoritative terminal persistence error was hidden: %v", err)
	}
}
