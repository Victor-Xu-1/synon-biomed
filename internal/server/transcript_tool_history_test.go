package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestTranscriptWebHistoryProjectsDurableToolLifecycleInOrder(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-tool-history", "frame-tool-history")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-tool-history", MessageUUID: "user-message", ClientMessageID: "user-client", Text: "inspect the target",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-tool-history")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	firstAssistantID := transcriptAssistantMessageID(stream.SessionID, claim.Claim.Attempt, 1)
	finalAssistantID := transcriptAssistantMessageID(stream.SessionID, claim.Claim.Attempt, 2)
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "auto-compact", map[string]any{
		"status": "completed", "toolPhase": "auto_compact", "message": "context compacted",
	})
	appendRunnerPayloadEvent(t, repo, claim.Claim, "before-tool", "content_delta", map[string]any{
		"text": "Before. ", "block_type": "thinking",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-read", "toolName": "Read",
		"toolInput": map[string]any{"path": "report.txt"},
	})
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "tool-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-read", "toolName": "Read",
		"toolInput": map[string]any{"path": "report.txt"}, "toolResult": map[string]any{"content": "evidence"},
	})
	appendRunnerPayloadEvent(t, repo, claim.Claim, "after-tool", "content_delta", map[string]any{
		"text": "After.", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "finish-tool-history", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"done","assistant_segment":{"version":1,"ordinal":2}}`),
	}); err != nil {
		t.Fatal(err)
	}

	messages, found, err := server.loadTranscriptWebHistory(context.Background(), "local", "frame-tool-history")
	if err != nil || !found {
		t.Fatalf("found=%t err=%v", found, err)
	}
	if len(messages) != 4 {
		t.Fatalf("messages=%#v", messages)
	}
	thinkingContent, thinkingOK := messages[1]["content"].(map[string]any)
	if !thinkingOK || messages[1]["type"] != "thinking" || thinkingContent["content"] != "Before. " ||
		thinkingContent["status"] != "done" || messages[1]["status"] != "finish" {
		t.Fatalf("pre-tool assistant segment=%#v", messages[1])
	}
	tool := messages[2]
	content, ok := tool["content"].(map[string]any)
	if !ok || tool["type"] != "tool_call" || content["call_id"] != "call-read" || content["name"] != "Read" ||
		content["status"] != "completed" || content["output"] != `{"content":"evidence"}` {
		t.Fatalf("tool=%#v", tool)
	}
	if args, ok := content["args"].(map[string]any); !ok || args["path"] != "report.txt" {
		t.Fatalf("tool args=%#v", content["args"])
	}
	assertTranscriptTextMessage(t, messages[3], "After.")
	if messages[3]["terminal_status"] != "completed" || messages[3]["status"] != "finish" {
		t.Fatalf("terminal assistant segment=%#v", messages[3])
	}
	if messages[1]["id"] != firstAssistantID || messages[3]["id"] != finalAssistantID ||
		messages[2]["id"] != "transcript-tool:frame:frame-tool-history:1:call-read" {
		t.Fatalf("unstable identities=%#v", messages)
	}
}

func TestTranscriptToolHistoryMessageOmitsInputWhenSettlementDoesNotRepeatIt(t *testing.T) {
	message := transcriptToolHistoryMessage(transcriptToolHistoryAction{
		identity: "transcript-tool:stream:2:call-recovered",
		msgID:    "tool-terminal",
		fact: transcriptToolHistoryFact{
			attempt: 2, callID: "call-recovered", name: "python", status: "completed",
			phase: "completed", revision: 9, result: map[string]any{"stdout": "42"}, resultSet: true,
		},
	}, "frame-recovered", 1)
	content, ok := message["content"].(map[string]any)
	if !ok {
		t.Fatalf("tool message=%#v", message)
	}
	if _, present := content["input"]; present {
		t.Fatalf("unknown input was serialized as authoritative: %#v", content)
	}
	if _, present := content["args"]; present {
		t.Fatalf("unknown args were serialized as authoritative: %#v", content)
	}
}

func TestTranscriptWebHistorySettlesUnmatchedToolAtAskUserBoundary(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-tool-ask-boundary", "frame-tool-ask-boundary")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-tool-ask-boundary", MessageUUID: "user-message", ClientMessageID: "user-client",
		Text: "inspect and ask before continuing",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-tool-ask-boundary")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool-ask-boundary",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "orphan-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-orphan", "toolName": "bash",
		"toolInput": map[string]any{"command": "long-running-command"},
	})
	if _, _, err := store.ParkAskUserWithTranscript(context.Background(), workspace.ParkAskUserInput{
		FrameID: stream.FrameID, ToolID: "ask-after-tool", ToolName: "AskUserQuestion",
		Questions: []any{map[string]any{
			"question": "Continue?", "header": "Choice",
			"options": []any{
				map[string]any{"label": "Continue", "description": "Continue the analysis."},
				map[string]any{"label": "Stop", "description": "Stop the analysis."},
			},
		}},
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "park-after-orphan-tool",
		Phase: transcriptstore.RunnerPhaseWaitingUser, Resumable: true,
		PayloadJSON: []byte(`{"status":"awaiting_user_response"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}

	messages, authoritative, err := server.loadTranscriptWebHistory(context.Background(), "local", stream.SessionID)
	if err != nil || !authoritative {
		t.Fatalf("authoritative=%t err=%v", authoritative, err)
	}
	for _, message := range messages {
		content, _ := message["content"].(map[string]any)
		if message["type"] != "tool_call" || content["call_id"] != "call-orphan" {
			continue
		}
		if content["status"] != "canceled" || message["status"] != "finish" {
			t.Fatalf("orphan tool was not settled at AskUser boundary: %#v", message)
		}
		return
	}
	t.Fatalf("orphan tool message missing: %#v", messages)
}

func TestTranscriptWebHistorySettlesUnmatchedToolAtInterruptedCheckpoint(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-tool-interrupted", "frame-tool-interrupted")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-tool-interrupted", MessageUUID: "user-message", ClientMessageID: "user-client",
		Text: "run a bounded command",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-tool-interrupted")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool-interrupted",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "orphan-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-interrupted", "toolName": "bash",
		"toolInput": map[string]any{"command": "bounded-command"},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "runner-interrupted", map[string]any{
		"status": "interrupted", "reason_code": "terminal_interruption",
	})

	messages, authoritative, err := server.loadTranscriptWebHistory(context.Background(), "local", stream.SessionID)
	if err != nil || !authoritative {
		t.Fatalf("authoritative=%t err=%v", authoritative, err)
	}
	for _, message := range messages {
		content, _ := message["content"].(map[string]any)
		if message["type"] != "tool_call" || content["call_id"] != "call-interrupted" {
			continue
		}
		if content["status"] != "canceled" || message["status"] != "finish" {
			t.Fatalf("interrupted tool was not settled: %#v", message)
		}
		return
	}
	t.Fatalf("interrupted tool message missing: %#v", messages)
}

func TestTranscriptWebHistoryKeepsRecoverableToolOpenUntilDurableCompletion(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-tool-recovered", "frame-tool-recovered")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-tool-recovered", MessageUUID: "user-message", ClientMessageID: "user-client",
		Text: "run and durably recover a bounded command",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-tool-recovered")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool-recovered",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "recovered-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-recovered", "toolName": "repl",
		"toolInput": map[string]any{"code": "print(42)"},
	})
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "recovered-tool-interruption",
		ReasonCode: "tool_lifecycle_persistence_interrupted", ResumeDetail: "recover the exact durable tool batch",
		RecoveryContractRevision: sessionRunnerRecoveryContractRevision, AutoResume: true,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool-recovered-resumed",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	appendRunnerToolCheckpoint(t, repo, reclaimed.Claim, "recovered-tool-completed", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-recovered", "toolName": "repl",
		"toolInput": map[string]any{"code": "print(42)"}, "toolResult": map[string]any{"stdout": "42"},
	})

	messages, authoritative, err := server.loadTranscriptWebHistory(context.Background(), "local", stream.SessionID)
	if err != nil || !authoritative {
		t.Fatalf("authoritative=%t err=%v", authoritative, err)
	}
	for _, message := range messages {
		content, _ := message["content"].(map[string]any)
		if message["type"] != "tool_call" || content["call_id"] != "call-recovered" {
			continue
		}
		if content["status"] != "completed" || message["status"] != "finish" ||
			!strings.Contains(webString(content["output"]), "42") {
			t.Fatalf("recovered tool history=%#v", message)
		}
		return
	}
	t.Fatalf("recovered tool message missing: %#v", messages)
}

func TestTranscriptWebHistoryKeepsRuntimeDrainToolOpenWithoutResumeDetail(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-runtime-drain-tool", "frame-runtime-drain-tool")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-runtime-drain-tool", MessageUUID: "user-message", ClientMessageID: "user-client",
		Text: "run and resume a durable download",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-runtime-drain-tool")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runtime-drain-tool-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	input := map[string]any{"command": "download --continue dataset"}
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "runtime-drain-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-runtime-drain", "toolName": "bash",
		"toolInput": input,
	})
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "runtime-drain-tool-interruption",
		ReasonCode: "runtime_draining", RecoveryContractRevision: sessionRunnerRecoveryContractRevision,
		AutoResume: true,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runtime-drain-tool-resumed",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	appendRunnerToolCheckpoint(t, repo, reclaimed.Claim, "runtime-drain-tool-failed", map[string]any{
		"status": "failed", "toolPhase": "failed", "toolCallId": "call-runtime-drain", "toolName": "bash",
		"toolInput": input, "toolResult": map[string]any{"outcome": "failed"},
	})

	messages, authoritative, err := server.loadTranscriptWebHistory(context.Background(), "local", stream.SessionID)
	if err != nil || !authoritative {
		t.Fatalf("authoritative=%t err=%v", authoritative, err)
	}
	for _, message := range messages {
		content, _ := message["content"].(map[string]any)
		if message["type"] != "tool_call" || content["call_id"] != "call-runtime-drain" {
			continue
		}
		if content["status"] != "error" || message["status"] != "error" {
			t.Fatalf("runtime-drain tool terminal state=%#v", message)
		}
		return
	}
	t.Fatalf("runtime-drain tool message missing: %#v", messages)
}

func TestTranscriptWebHistoryNarrowResetPreservesEarlierProgressAndToolOrder(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-narrow-reset", "frame-narrow-reset")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-narrow-reset", MessageUUID: "user-message", ClientMessageID: "user-client",
		Text: "请按步骤检查输入并计算结果",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-narrow-reset")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-narrow-reset", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	attempt := claimed.Claim.Attempt
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "progress", "content_delta", map[string]any{
		"text": "我先检查输入。", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-calculate", "toolName": "Python",
		"toolInput": map[string]any{"code": "print(42)"},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "tool-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-calculate", "toolName": "Python",
		"toolInput": map[string]any{"code": "print(42)"}, "toolResult": map[string]any{"stdout": "42"},
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "rejected", "content_delta", map[string]any{
		"text": "初步结论：结果是 41。", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "narrow-reset", "content_reset", map[string]any{
		"text": "", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, transcriptstore.AssistantReplaceScopeSegment),
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "corrected", "content_delta", map[string]any{
		"text": "根据工具结果，数值是 42。", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, ""),
	})
	finishPayload, err := json.Marshal(map[string]any{
		"status": "completed", "detail": "done",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-narrow-reset", Status: "completed", PayloadJSON: finishPayload,
	}); err != nil {
		t.Fatal(err)
	}

	messages, authoritative, err := server.loadTranscriptWebHistory(context.Background(), "local", "frame-narrow-reset")
	if err != nil || !authoritative || len(messages) != 4 {
		t.Fatalf("messages=%#v authoritative=%t err=%v", messages, authoritative, err)
	}
	assertTranscriptTextMessage(t, messages[1], "我先检查输入。")
	if messages[2]["type"] != "tool_call" {
		t.Fatalf("tool boundary lost chronological position: %#v", messages)
	}
	assertTranscriptTextMessage(t, messages[3], "根据工具结果，数值是 42。")
	for _, message := range messages {
		content, _ := message["content"].(map[string]any)
		if strings.Contains(transcriptPayloadText(content), "结果是 41") {
			t.Fatalf("rejected candidate survived narrow reset: %#v", messages)
		}
	}
	if messages[1]["id"] != transcriptAssistantMessageID(stream.SessionID, attempt, 1) ||
		messages[3]["id"] != transcriptAssistantMessageID(stream.SessionID, attempt, 3) {
		t.Fatalf("segment identities drifted: %#v", messages)
	}
}

func TestTranscriptAssistantSegmentResetTargetUsesImmediatelyPreviousIdentity(t *testing.T) {
	payload := map[string]any{
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, transcriptstore.AssistantReplaceScopeSegment),
	}
	target, narrow, err := transcriptAssistantSegmentResetTarget(payload, "frame-reset-target", 2)
	if err != nil || !narrow || target != transcriptAssistantMessageID("frame-reset-target", 2, 2) {
		t.Fatalf("target=%q narrow=%t err=%v", target, narrow, err)
	}
	payload["assistant_segment"] = transcriptstore.AssistantSegmentPayloadV1(3, transcriptstore.AssistantReplaceScopeAttempt)
	if target, narrow, err := transcriptAssistantSegmentResetTarget(payload, "frame-reset-target", 2); err != nil || narrow || target != "" {
		t.Fatalf("attempt reset target=%q narrow=%t err=%v", target, narrow, err)
	}
	payload["assistant_segment"] = transcriptstore.AssistantSegmentPayloadV1(3, "")
	if target, narrow, err := transcriptAssistantSegmentResetTarget(payload, "frame-reset-target", 2); err != nil || narrow || target != "" {
		t.Fatalf("legacy reset target=%q narrow=%t err=%v", target, narrow, err)
	}
}

func TestTranscriptWebHistorySkipsResetReservationAcrossRecoveredToolBoundary(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-reset-tool-recovery", "frame-reset-tool-recovery")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-reset-tool-recovery", MessageUUID: "reset-tool-user",
		ClientMessageID: "reset-tool-user-client", Text: "run the tool and recover its exact result",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-reset-tool-recovery")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "reset-tool-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "reset-tool-candidate", "content_delta", map[string]any{
		"text":              "the calculation is already complete",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "reset-tool-reset", "content_reset", map[string]any{
		"text":              "",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, transcriptstore.AssistantReplaceScopeSegment),
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "reset-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-reset-tool", "toolName": "Python",
		"toolInput": map[string]any{"code": "print(42)"},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "reset-tool-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-reset-tool", "toolName": "Python",
		"toolInput": map[string]any{"code": "print(42)"}, "toolResult": map[string]any{"stdout": "42"},
	})
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "reset-tool-interrupted",
		ReasonCode: "tool_lifecycle_persistence_interrupted", ResumeDetail: "recover the exact durable tool batch",
		RecoveryContractRevision: sessionRunnerRecoveryContractRevision, AutoResume: true,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "reset-tool-runner-resumed",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	appendRunnerPayloadEvent(t, repo, reclaimed.Claim, "reset-tool-result", "content_delta", map[string]any{
		"text":              "工具结果已恢复，数值是 42。",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, ""),
	})

	messages, authoritative, err := server.loadTranscriptWebHistory(
		context.Background(), "local", "frame-reset-tool-recovery",
	)
	if err != nil || !authoritative || len(messages) != 3 {
		t.Fatalf("messages=%#v authoritative=%t err=%v", messages, authoritative, err)
	}
	if messages[1]["type"] != "tool_call" {
		t.Fatalf("tool boundary missing: %#v", messages)
	}
	assertTranscriptTextMessage(t, messages[2], "工具结果已恢复，数值是 42。")
	if messages[2]["id"] != transcriptAssistantMessageID(stream.SessionID, claimed.Claim.Attempt, 3) {
		t.Fatalf("resumed segment identity=%#v", messages[2])
	}
}

func TestTranscriptWebHistoryRecoversLegacyRuntimeDrainSegmentOrdinalReset(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-runtime-drain-history", "frame-runtime-drain-history")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-runtime-drain-history", MessageUUID: "runtime-drain-user", ClientMessageID: "runtime-drain-user-client",
		Text: "preserve the complete transcript across a runtime drain",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-runtime-drain-history")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-before-runtime-drain",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "legacy-segment-one", "content_delta", map[string]any{
		"text": "Before first tool.", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "legacy-first-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-first", "toolName": "Read",
		"toolInput": map[string]any{"path": "first.txt"},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "legacy-first-tool-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-first", "toolName": "Read",
		"toolInput": map[string]any{"path": "first.txt"}, "toolResult": map[string]any{"content": "first"},
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "legacy-segment-two", "content_delta", map[string]any{
		"text": "Before drain.", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "legacy-final-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-final", "toolName": "Read",
		"toolInput": map[string]any{"path": "final.txt"},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "legacy-final-tool-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-final", "toolName": "Read",
		"toolInput": map[string]any{"path": "final.txt"}, "toolResult": map[string]any{"content": "final"},
	})
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "legacy-runtime-draining", ReasonCode: "runtime_draining", AutoResume: true,
		RecoveryContractRevision: 1,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-after-runtime-drain",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	appendRunnerPayloadEvent(t, repo, reclaimed.Claim, "legacy-reset-segment-one", "content_delta", map[string]any{
		"text": "After drain.", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: reclaimed.Claim, ClientMessageID: "legacy-runtime-drain-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"done","assistant_segment":{"version":1,"ordinal":1}}`),
	}); err != nil {
		t.Fatal(err)
	}

	messages, found, err := server.loadTranscriptWebHistory(context.Background(), "local", "frame-runtime-drain-history")
	if err != nil || !found {
		t.Fatalf("found=%t err=%v", found, err)
	}
	if len(messages) != 6 {
		t.Fatalf("messages=%#v", messages)
	}
	assertTranscriptTextMessage(t, messages[1], "Before first tool.")
	assertTranscriptTextMessage(t, messages[3], "Before drain.")
	assertTranscriptTextMessage(t, messages[5], "After drain.")
	if messages[5]["id"] != transcriptAssistantMessageID(stream.SessionID, claimed.Claim.Attempt, 3) ||
		messages[5]["terminal_status"] != "completed" {
		t.Fatalf("recovered terminal segment=%#v", messages[5])
	}
}

func TestTranscriptWebRuntimeDrainFenceValidatesRecoveryContractRevision(t *testing.T) {
	tests := []struct {
		name     string
		revision any
		present  bool
		want     bool
	}{
		{name: "omitted legacy revision", want: true},
		{name: "current positive revision", revision: float64(1), present: true, want: true},
		{name: "maximum revision", revision: float64(1_000_000), present: true, want: true},
		{name: "zero revision", revision: float64(0), present: true},
		{name: "negative revision", revision: float64(-1), present: true},
		{name: "fractional revision", revision: 1.5, present: true},
		{name: "oversized revision", revision: float64(1_000_001), present: true},
		{name: "string revision", revision: "1", present: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{"status": "interrupted", "reason_code": "runtime_draining"}
			if test.present {
				payload["recovery_contract_revision"] = test.revision
			}
			if got := transcriptWebRuntimeDrainFence(payload); got != test.want {
				t.Fatalf("transcriptWebRuntimeDrainFence(%#v)=%t, want %t", payload, got, test.want)
			}
		})
	}
}

func TestRuntimeDrainSegmentNormalizerLeavesNonLegacyIdentityShapesUnchanged(t *testing.T) {
	tests := []struct {
		name        string
		includeTool bool
		reason      string
		replace     string
		wantOrdinal int64
	}{
		{name: "exact legacy fence", includeTool: true, reason: "runtime_draining", wantOrdinal: 3},
		{name: "context pressure starts a fresh segment", includeTool: true, reason: sessionRunnerProviderContextPressureReasonCode, wantOrdinal: 3},
		{name: "tool lifecycle recovery starts a fresh segment", includeTool: true, reason: sessionRunnerToolLifecyclePersistenceReasonCode, wantOrdinal: 3},
		{name: "retired real evidence correction remains unchanged", reason: sessionRunnerRealScientificEvidenceRequiredReasonCode, wantOrdinal: 1},
		{name: "media capability correction starts a fresh segment", reason: sessionRunnerVisualMediaUnsupportedReasonCode, wantOrdinal: 3},
		{name: "no prior tool boundary", reason: "runtime_draining", wantOrdinal: 1},
		{name: "different interruption", includeTool: true, reason: "provider_stream_interrupted", wantOrdinal: 1},
		{name: "explicit replacement scope", includeTool: true, reason: "runtime_draining", replace: transcriptstore.AssistantReplaceScopeAttempt, wantOrdinal: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
			attempt := int64(7)
			emit := func(eventType string, payload map[string]any) transcriptstore.ProjectedEvent {
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				projected, err := normalizer.normalize(transcriptstore.ProjectedEvent{
					Event: transcriptstore.Event{Type: eventType, RunnerAttempt: &attempt}, ResolvedPayloadJSON: raw,
				})
				if err != nil {
					t.Fatal(err)
				}
				return projected
			}
			emit("content_delta", map[string]any{
				"text": "before", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
			})
			if test.includeTool {
				emit("runner_checkpoint", map[string]any{
					"status": "running", "toolPhase": "start", "toolCallId": "call", "toolName": "Read",
				})
			}
			emit("runner_checkpoint", map[string]any{"status": "interrupted", "reason_code": test.reason})
			projected := emit("content_delta", map[string]any{
				"text": "after", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, test.replace),
			})
			payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
			if err != nil {
				t.Fatal(err)
			}
			segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
			if err != nil || !present || segment.Ordinal != test.wantOrdinal {
				t.Fatalf("segment=%#v present=%t err=%v", segment, present, err)
			}
		})
	}
}

func TestTranscriptWebRuntimeDrainSegmentNormalizerRebasesAskUserResumeOrdinals(t *testing.T) {
	normalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
	attempt := int64(4)
	emit := func(eventID int64, eventType string, payload map[string]any) transcriptstore.ProjectedEvent {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := normalizer.normalize(transcriptstore.ProjectedEvent{
			Event: transcriptstore.Event{EventID: eventID, Type: eventType, RunnerAttempt: &attempt}, ResolvedPayloadJSON: raw,
		})
		if err != nil {
			t.Fatal(err)
		}
		return projected
	}
	ordinal := func(projected transcriptstore.ProjectedEvent) int64 {
		payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
		if err != nil {
			t.Fatal(err)
		}
		segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
		if err != nil || !present {
			t.Fatalf("segment=%#v present=%t err=%v", segment, present, err)
		}
		return segment.Ordinal
	}
	if got := ordinal(emit(1, "content_delta", map[string]any{
		"text": "before asking", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})); got != 1 {
		t.Fatalf("before AskUser ordinal=%d", got)
	}
	emit(2, "runner_checkpoint", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "ask-resume", "toolName": "ask_user",
	})
	emit(3, "runner_checkpoint", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "ask-resume", "toolName": "ask_user",
		"toolResult": map[string]any{"status": "answered"},
	})
	if got := ordinal(emit(4, "content_delta", map[string]any{
		"text": "after answer", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})); got != 2 {
		t.Fatalf("first resumed ordinal=%d", got)
	}
	if got := ordinal(emit(5, "assistant_message", map[string]any{
		"text": "final", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})); got != 3 {
		t.Fatalf("terminal resumed ordinal=%d", got)
	}
}

func TestTranscriptWebRuntimeDrainSegmentNormalizerRebasesApprovalResumeOrdinals(t *testing.T) {
	normalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
	attempt := int64(8)
	eventID := int64(0)
	emit := func(eventType string, payload map[string]any) transcriptstore.ProjectedEvent {
		eventID++
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := normalizer.normalize(transcriptstore.ProjectedEvent{
			Event: transcriptstore.Event{
				EventID: eventID, PublicationSeq: eventID, Type: eventType, RunnerAttempt: &attempt,
			},
			ResolvedPayloadJSON: raw,
		})
		if err != nil {
			t.Fatal(err)
		}
		return projected
	}
	ordinal := func(projected transcriptstore.ProjectedEvent) int64 {
		payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
		if err != nil {
			t.Fatal(err)
		}
		segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
		if err != nil || !present {
			t.Fatalf("segment=%#v present=%t err=%v", segment, present, err)
		}
		return segment.Ordinal
	}
	resume := func() {
		emit("runner_checkpoint", map[string]any{
			"status": "running", "stage": "resume_state",
			"detail": "restoring the durable assistant continuation state",
		})
	}

	if got := ordinal(emit("content_delta", map[string]any{
		"text": "before approval", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})); got != 1 {
		t.Fatalf("initial ordinal=%d", got)
	}
	for index, callID := range []string{"approval-one", "approval-two"} {
		emit("runner_checkpoint", map[string]any{
			"status": "waiting", "toolPhase": "waiting", "toolCallId": callID, "toolName": "software_runtime",
		})
		resume()
		if got := ordinal(emit("content_delta", map[string]any{
			"text": "after approval", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		})); got != int64(index+2) {
			t.Fatalf("approval %d ordinal=%d", index, got)
		}
	}
}

func TestTranscriptWebRuntimeDrainSegmentNormalizerKeepsResumeOffsetAfterContentReset(t *testing.T) {
	normalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
	attempt := int64(9)
	eventID := int64(0)
	emit := func(eventType string, payload map[string]any) transcriptstore.ProjectedEvent {
		eventID++
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := normalizer.normalize(transcriptstore.ProjectedEvent{
			Event: transcriptstore.Event{
				EventID: eventID, PublicationSeq: eventID, Type: eventType, RunnerAttempt: &attempt,
			},
			ResolvedPayloadJSON: raw,
		})
		if err != nil {
			t.Fatal(err)
		}
		return projected
	}
	ordinal := func(projected transcriptstore.ProjectedEvent) int64 {
		payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
		if err != nil {
			t.Fatal(err)
		}
		segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
		if err != nil || !present {
			t.Fatalf("segment=%#v present=%t err=%v", segment, present, err)
		}
		return segment.Ordinal
	}
	resumeAfterTool := func(callID string) {
		emit("runner_checkpoint", map[string]any{
			"status": "running", "toolPhase": "start", "toolCallId": callID, "toolName": "python",
		})
		emit("runner_checkpoint", map[string]any{
			"status": "running", "stage": "resume_state",
			"detail": "restoring the durable assistant continuation state", "lifecyclePhase": "recovery",
		})
	}

	if got := ordinal(emit("content_delta", map[string]any{
		"text": "before recovery", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})); got != 1 {
		t.Fatalf("initial ordinal=%d", got)
	}
	resumeAfterTool("first-recovery")
	if got := ordinal(emit("content_delta", map[string]any{
		"text": "provisional", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})); got != 2 {
		t.Fatalf("resumed provisional ordinal=%d", got)
	}
	if got := ordinal(emit("content_reset", map[string]any{
		"text": "", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, transcriptstore.AssistantReplaceScopeSegment),
	})); got != 3 {
		t.Fatalf("resumed reset ordinal=%d", got)
	}
	emit("runner_checkpoint", map[string]any{
		"status": "running", "stage": "resume_state",
		"detail": "restoring the durable assistant continuation state", "lifecyclePhase": "recovery",
	})
	if got := ordinal(emit("content_delta", map[string]any{
		"text": "after reset", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})); got != 3 {
		t.Fatalf("post-reset ordinal=%d", got)
	}
	if got := ordinal(emit("content_delta", map[string]any{
		"text": "next segment", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, ""),
	})); got != 4 {
		t.Fatalf("post-reset next ordinal=%d", got)
	}

	resumeAfterTool("second-recovery")
	if got := ordinal(emit("content_delta", map[string]any{
		"text": "after second recovery", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})); got != 5 {
		t.Fatalf("second recovery ordinal=%d", got)
	}
}

func TestTranscriptWebRuntimeDrainSegmentNormalizerKeepsRebasedIdentityForLegacyTerminal(t *testing.T) {
	normalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
	attempt := int64(11)
	eventID := int64(0)
	emit := func(eventType string, payload map[string]any) transcriptstore.ProjectedEvent {
		eventID++
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := normalizer.normalize(transcriptstore.ProjectedEvent{
			Event:               transcriptstore.Event{EventID: eventID, PublicationSeq: eventID, Type: eventType, RunnerAttempt: &attempt},
			ResolvedPayloadJSON: raw,
		})
		if err != nil {
			t.Fatal(err)
		}
		return projected
	}
	ordinal := func(projected transcriptstore.ProjectedEvent) int64 {
		payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
		if err != nil {
			t.Fatal(err)
		}
		segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
		if err != nil || !present {
			t.Fatalf("segment=%#v present=%t err=%v", segment, present, err)
		}
		return segment.Ordinal
	}
	if got := ordinal(emit("content_delta", map[string]any{"text": "before", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, "")})); got != 1 {
		t.Fatalf("initial ordinal=%d", got)
	}
	emit("runner_checkpoint", map[string]any{"status": "running", "toolPhase": "start", "toolCallId": "legacy-terminal", "toolName": "python"})
	emit("runner_checkpoint", map[string]any{"status": "running", "stage": "resume_state", "detail": "restoring the durable assistant continuation state", "lifecyclePhase": "recovery"})
	if got := ordinal(emit("content_delta", map[string]any{"text": "resumed", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, "")})); got != 2 {
		t.Fatalf("resumed ordinal=%d", got)
	}
	if got := ordinal(emit("content_delta", map[string]any{"text": "next", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, "")})); got != 3 {
		t.Fatalf("next ordinal=%d", got)
	}
	if got := ordinal(emit("runner_finished", map[string]any{"status": "failed", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, "")})); got != 3 {
		t.Fatalf("terminal ordinal=%d", got)
	}
}

func TestTranscriptWebRunnerResumeStateFenceIsStrict(t *testing.T) {
	valid := map[string]any{
		"status": "running", "stage": "resume_state",
		"detail": "restoring the durable assistant continuation state",
	}
	if !transcriptWebRunnerResumeStateFence(valid) {
		t.Fatal("canonical resume-state checkpoint was not recognized")
	}
	withLifecycle := maps.Clone(valid)
	withLifecycle["lifecyclePhase"] = "recovery"
	if !transcriptWebRunnerResumeStateFence(withLifecycle) {
		t.Fatal("canonical recovery lifecycle checkpoint was not recognized")
	}
	for name, mutate := range map[string]func(map[string]any){
		"wrong status": func(payload map[string]any) { payload["status"] = "completed" },
		"wrong stage":  func(payload map[string]any) { payload["stage"] = "planning" },
		"wrong detail": func(payload map[string]any) { payload["detail"] = "resume" },
		"extra field":  func(payload map[string]any) { payload["operation_id"] = "unexpected" },
		"wrong lifecycle": func(payload map[string]any) {
			payload["lifecyclePhase"] = "tool"
		},
	} {
		t.Run(name, func(t *testing.T) {
			payload := maps.Clone(valid)
			mutate(payload)
			if transcriptWebRunnerResumeStateFence(payload) {
				t.Fatalf("invalid resume-state checkpoint accepted: %#v", payload)
			}
		})
	}
}

func TestTranscriptWebRuntimeDrainSegmentNormalizerRebasesCorrectionStartingAfterToolBoundary(t *testing.T) {
	normalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
	attempt := int64(4)
	eventID := int64(0)
	emit := func(eventType string, payload map[string]any) transcriptstore.ProjectedEvent {
		eventID++
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := normalizer.normalize(transcriptstore.ProjectedEvent{
			Event: transcriptstore.Event{
				EventID: eventID, PublicationSeq: eventID, Type: eventType, RunnerAttempt: &attempt,
			},
			ResolvedPayloadJSON: raw,
		})
		if err != nil {
			t.Fatal(err)
		}
		return projected
	}
	ordinal := func(projected transcriptstore.ProjectedEvent) int64 {
		payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
		if err != nil {
			t.Fatal(err)
		}
		segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
		if err != nil || !present {
			t.Fatalf("segment=%#v present=%t err=%v", segment, present, err)
		}
		return segment.Ordinal
	}

	if got := ordinal(emit("content_delta", map[string]any{
		"text": "candidate before correction", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})); got != 2 {
		t.Fatalf("initial ordinal=%d", got)
	}
	emit("runner_checkpoint", map[string]any{
		"status": "interrupted", "reason_code": "artifact_reference_correction_required",
	})
	if got := ordinal(emit("content_delta", map[string]any{
		"text": "first correction", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})); got != 3 {
		t.Fatalf("first correction ordinal=%d", got)
	}
	emit("runner_checkpoint", map[string]any{
		"status": "interrupted", "reason_code": sessionRunnerRealScientificEvidenceRequiredReasonCode,
	})
	for _, phase := range []string{"start", "completed"} {
		status := "running"
		if phase == "completed" {
			status = "completed"
		}
		emit("runner_checkpoint", map[string]any{
			"status": status, "toolPhase": phase, "toolCallId": "call-evidence", "toolName": "edit_file",
		})
	}
	if got := ordinal(emit("content_delta", map[string]any{
		"text": "second correction", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})); got != 4 {
		t.Fatalf("second correction first visible ordinal=%d", got)
	}
	if got := ordinal(emit("content_delta", map[string]any{
		"text": "same segment", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})); got != 4 {
		t.Fatalf("second correction repeated ordinal=%d", got)
	}
	if got := ordinal(emit("content_delta", map[string]any{
		"text": "next segment", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, ""),
	})); got != 5 {
		t.Fatalf("second correction next ordinal=%d", got)
	}
}

func TestTranscriptWebReadModelRebuildsMultipleCorrectionSegmentsStartingAfterTools(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-multiple-corrections", "frame-multiple-corrections")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-multiple-corrections", MessageUUID: "multiple-corrections-user",
		ClientMessageID: "multiple-corrections-user-client", Text: "complete an evidence-backed task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-multiple-corrections")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	branch, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "multiple-corrections-runner-1",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	claim := claimed.Claim
	appendRunnerPayloadEvent(t, repo, claim, "multiple-corrections-segment-1", "content_delta", map[string]any{
		"text": "initial analysis", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerToolCheckpoint(t, repo, claim, "multiple-corrections-tool-1-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-initial", "toolName": "Read",
		"toolInput": map[string]any{"path": "evidence.txt"},
	})
	appendRunnerToolCheckpoint(t, repo, claim, "multiple-corrections-tool-1-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-initial", "toolName": "Read",
		"toolInput": map[string]any{"path": "evidence.txt"}, "toolResult": map[string]any{"ok": true},
	})
	appendRunnerPayloadEvent(t, repo, claim, "multiple-corrections-segment-2", "content_delta", map[string]any{
		"text": "candidate requiring correction", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})

	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server.transcriptWebReadModel = readModel
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	initial := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if initial.StateStatus != "ready" || initial.StateMessageCount != 4 {
		t.Fatalf("initial fence=%#v", initial)
	}

	firstInterrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claim, ClientMessageID: "multiple-corrections-artifact-interrupt",
		ReasonCode: "artifact_reference_correction_required", AutoResume: true,
	})
	if err != nil || !firstInterrupted.Created {
		t.Fatalf("first interruption=%#v err=%v", firstInterrupted, err)
	}
	firstReclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "multiple-corrections-runner-2",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: firstInterrupted.Checkpoint.Sequence,
	})
	if err != nil || !firstReclaimed.Claimed || firstReclaimed.Claim.Attempt != claim.Attempt {
		t.Fatalf("first reclaim=%#v err=%v", firstReclaimed, err)
	}
	claim = firstReclaimed.Claim
	appendRunnerPayloadEvent(t, repo, claim, "multiple-corrections-segment-3-raw-1", "content_delta", map[string]any{
		"text": "artifact correction", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})

	secondInterrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claim, ClientMessageID: "multiple-corrections-evidence-interrupt",
		ReasonCode: sessionRunnerRealScientificEvidenceRequiredReasonCode, AutoResume: true,
	})
	if err != nil || !secondInterrupted.Created {
		t.Fatalf("second interruption=%#v err=%v", secondInterrupted, err)
	}
	secondReclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "multiple-corrections-runner-3",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: secondInterrupted.Checkpoint.Sequence,
	})
	if err != nil || !secondReclaimed.Claimed || secondReclaimed.Claim.Attempt != claim.Attempt {
		t.Fatalf("second reclaim=%#v err=%v", secondReclaimed, err)
	}
	claim = secondReclaimed.Claim
	appendRunnerToolCheckpoint(t, repo, claim, "multiple-corrections-evidence-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-evidence", "toolName": "edit_file",
		"toolInput": map[string]any{"path": "evidence.md"},
	})
	appendRunnerToolCheckpoint(t, repo, claim, "multiple-corrections-evidence-tool-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-evidence", "toolName": "edit_file",
		"toolInput": map[string]any{"path": "evidence.md"}, "toolResult": map[string]any{"ok": true},
	})
	appendRunnerPayloadEvent(t, repo, claim, "multiple-corrections-segment-4-raw-2", "content_delta", map[string]any{
		"text": "evidence correction", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim, ClientMessageID: "multiple-corrections-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"done","assistant_segment":{"version":1,"ordinal":2}}`),
	}); err != nil {
		t.Fatal(err)
	}

	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if ready.StateStatus != "ready" || ready.StateMessageCount != 7 {
		t.Fatalf("rebuilt fence=%#v", ready)
	}
	page := requireTranscriptWebReadModelPage(t, readModel, stream, ready)
	if len(page.Messages) != 7 {
		t.Fatalf("rebuilt page=%#v", page)
	}
	wantSegment3 := transcriptAssistantMessageID(stream.SessionID, claim.Attempt, 3)
	wantSegment4 := transcriptAssistantMessageID(stream.SessionID, claim.Attempt, 4)
	seenSegment3, seenSegment4 := false, false
	for _, message := range page.Messages {
		seenSegment3 = seenSegment3 || message.MessageID == wantSegment3
		seenSegment4 = seenSegment4 || message.MessageID == wantSegment4
	}
	if !seenSegment3 || !seenSegment4 {
		t.Fatalf("correction segments missing segment3=%t segment4=%t page=%#v", seenSegment3, seenSegment4, page)
	}
}

func TestTranscriptWebHistoryRemovesRejectedCandidateAcrossToolBoundary(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-reset-history", "frame-reset-history")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-reset-history", MessageUUID: "user-message", ClientMessageID: "user-client", Text: "verify sources",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-reset-history")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-reset", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	appendRunnerPayloadEvent(t, repo, claim.Claim, "bad-delta-a", "content_delta", map[string]any{"text": "Unsupported DOI 10.9999/fake-a"})
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-verify", "toolName": "WebFetch",
		"toolInput": map[string]any{"url": "https://doi.org/10.9999/fake"},
	})
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "tool-complete", map[string]any{
		"status": "failed", "toolPhase": "failed", "toolCallId": "call-verify", "toolName": "WebFetch",
		"toolInput":  map[string]any{"url": "https://doi.org/10.9999/fake"},
		"toolResult": map[string]any{"ok": false, "error": "not found"},
	})
	appendRunnerPayloadEvent(t, repo, claim.Claim, "bad-delta-b", "content_delta", map[string]any{"text": "Unsupported DOI 10.9999/fake-b"})
	appendRunnerPayloadEvent(t, repo, claim.Claim, "reset", "content_reset", map[string]any{"text": ""})
	appendRunnerPayloadEvent(t, repo, claim.Claim, "corrected", "content_delta", map[string]any{"text": "The claimed DOI could not be verified."})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "finish-reset-history", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"done"}`),
	}); err != nil {
		t.Fatal(err)
	}

	messages, authoritative, err := server.loadTranscriptWebHistory(context.Background(), "local", "frame-reset-history")
	if err != nil || !authoritative || len(messages) != 3 {
		t.Fatalf("messages=%#v authoritative=%t err=%v", messages, authoritative, err)
	}
	if content := transcriptPayloadText(messages[2]["content"].(map[string]any)); content != "The claimed DOI could not be verified." {
		t.Fatalf("corrected assistant content=%q messages=%#v", content, messages)
	}
	for _, message := range messages {
		if strings.Contains(transcriptPayloadText(message["content"].(map[string]any)), "10.9999/fake") {
			t.Fatalf("rejected candidate survived projection: %#v", messages)
		}
	}
	page := transcriptToolHistoryPage(t, server, "/api/conversations/frame-reset-history/messages?limit=10", "")
	items, _ := page["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("public page=%#v", page)
	}
}

func TestTranscriptToolHistoryProjectsPrestartFailure(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-prestart-history", "frame-prestart-history")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-prestart-history", MessageUUID: "prestart-user",
		ClientMessageID: "prestart-user-client", Text: "run a task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-prestart-history")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-prestart-history",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "prestart-failed", map[string]any{
		"status": "failed", "toolPhase": prestartToolFailurePhase,
		"toolCallId": "call-prestart", "toolName": "generate_plan",
		"toolInput":  map[string]any{"title": "wrong"},
		"toolResult": map[string]any{"ok": false, "executed": false, "code": "invalid_tool_arguments"},
	})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-prestart", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"done"}`),
	}); err != nil {
		t.Fatal(err)
	}
	page := transcriptToolHistoryPage(t, server, "/api/conversations/frame-prestart-history/messages?limit=10", "")
	items, _ := page["items"].([]any)
	var foundFailure bool
	for _, raw := range items {
		message, _ := raw.(map[string]any)
		content, _ := message["content"].(map[string]any)
		if message["type"] == "tool_call" && content["call_id"] == "call-prestart" {
			foundFailure = content["status"] == "error"
		}
	}
	if !foundFailure {
		t.Fatalf("pre-start failure missing from page=%#v", page)
	}
}

func TestTranscriptToolHistoryRepairsLegacyCompletedPrestartFailure(t *testing.T) {
	attempt := int64(1)
	projected := transcriptstore.ProjectedEvent{Event: transcriptstore.Event{
		StreamUID: "frame:legacy-prestart", EventID: 1, Type: "runner_checkpoint",
		Source: transcriptstore.EventSourcePayload, RunnerAttempt: &attempt,
		ClientMessageID: "legacy-prestart-checkpoint",
	}}
	fact, found, err := transcriptToolHistoryFactFromEvent(projected, map[string]any{
		"status": "completed", "toolPhase": prestartToolFailurePhase,
		"toolCallId": "call-prestart", "toolName": "download_public_scientific_file",
		"rejectedBeforeExecution": true,
		"toolResult":              map[string]any{"ok": false, "executed": false, "code": "source_preflight_required"},
	})
	if err != nil || !found || fact.status != "error" {
		t.Fatalf("legacy prestart fact=%#v found=%t err=%v", fact, found, err)
	}
}

func TestTranscriptToolHistoryProjectsCanceledToolSettlement(t *testing.T) {
	for _, spelling := range []string{"cancelled", "canceled"} {
		t.Run(spelling, func(t *testing.T) {
			attempt := int64(1)
			payload := map[string]any{
				"status": spelling, "toolPhase": spelling,
				"toolCallId": "cancelled-call", "toolName": "python",
				"toolResult": map[string]any{"ok": false, "status": spelling},
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			fact, toolEvent, err := transcriptToolHistoryFactFromEvent(transcriptstore.ProjectedEvent{
				Event: transcriptstore.Event{
					Type: "runner_checkpoint", Source: transcriptstore.EventSourcePayload,
					RunnerAttempt: &attempt,
				},
				ResolvedPayloadJSON: encoded,
			}, payload)
			if err != nil || !toolEvent || fact.status != "canceled" {
				t.Fatalf("fact=%#v toolEvent=%t err=%v", fact, toolEvent, err)
			}
		})
	}
}

func TestTranscriptAssistantTimelineTerminalUsesReservedResetIdentity(t *testing.T) {
	const sessionID = "terminal-after-reset"
	state := newTranscriptAssistantTimelineState(sessionID)
	attempt := int64(1)
	first := transcriptAssistantMessageID(sessionID, attempt, 1)
	second := transcriptAssistantMessageID(sessionID, attempt, 2)
	if _, _, created, err := state.content(attempt, 1, 0, first); err != nil || !created {
		t.Fatalf("initial content created=%t err=%v", created, err)
	}
	if _, retired, err := state.resetSegment(attempt, first, second); err != nil || !retired {
		t.Fatalf("reset retired=%t err=%v", retired, err)
	}
	state.correctionBoundary(attempt)
	index, identity, created, err := state.terminal(attempt, 2, 1, "")
	if err != nil || !created || index != 1 || identity != second {
		t.Fatalf("terminal index=%d identity=%q created=%t err=%v", index, identity, created, err)
	}
}

func TestTranscriptToolHistoryProjectsObservedProgressIntoTheSameToolRow(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-progress-history", "frame-progress-history")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-progress-history", MessageUUID: "progress-user",
		ClientMessageID: "progress-user-client", Text: "run a long tool",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-progress-history")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-progress-history",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "progress-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-progress",
		"toolName": "repl", "toolInput": map[string]any{"code": "print(1)"},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "progress-heartbeat", map[string]any{
		"status": "running", "toolPhase": "progress-000001", "toolCallId": "call-progress",
		"toolName": "repl", "toolInput": map[string]any{"code": "print(1)"},
		"toolProgress": true, "progressOrdinal": 1, "elapsedMs": 15000,
		"progress": map[string]any{
			"phase": "downloading_packages", "phasePercent": 40,
			"completedItems": 1, "totalItems": 8, "elapsedMs": 15000, "indeterminate": false,
		},
	})
	progressPage := transcriptToolHistoryPage(t, server, "/api/conversations/frame-progress-history/messages?limit=10", "")
	progressItems, _ := progressPage["items"].([]any)
	runningRows := 0
	for _, raw := range progressItems {
		message, _ := raw.(map[string]any)
		content, _ := message["content"].(map[string]any)
		if message["type"] != "tool_call" || content["call_id"] != "call-progress" {
			continue
		}
		runningRows++
		progress, _ := content["progress"].(map[string]any)
		if content["status"] != "running" || progress["phase"] != "downloading_packages" ||
			progress["phasePercent"] != float64(40) || progress["completedItems"] != float64(1) ||
			progress["totalItems"] != float64(8) {
			t.Fatalf("running content=%#v", content)
		}
	}
	if runningRows != 1 {
		t.Fatalf("runningRows=%d page=%#v", runningRows, progressPage)
	}
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "progress-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-progress",
		"toolName": "repl", "toolInput": map[string]any{"code": "print(1)"},
		"toolResult": map[string]any{"ok": true},
	})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-progress", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"done"}`),
	}); err != nil {
		t.Fatal(err)
	}
	page := transcriptToolHistoryPage(t, server, "/api/conversations/frame-progress-history/messages?limit=10", "")
	items, _ := page["items"].([]any)
	var completed int
	for _, raw := range items {
		message, _ := raw.(map[string]any)
		content, _ := message["content"].(map[string]any)
		if message["type"] == "tool_call" && content["call_id"] == "call-progress" && content["status"] == "completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("completed=%d page=%#v", completed, page)
	}
}

func TestTranscriptWebHistoryPreservesFailedCandidateBeforeTerminalReset(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-failed-history", "frame-failed-history")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-failed-history", MessageUUID: "user-message", ClientMessageID: "user-client", Text: "run analysis",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-failed-history")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-failed", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	appendRunnerPayloadEvent(t, repo, claim.Claim, "analysis", "content_delta", map[string]any{
		"text": "Intermediate analysis. Final diagnostic conclusion.",
	})
	appendRunnerPayloadEvent(t, repo, claim.Claim, "discard-empty", "content_reset", map[string]any{"text": ""})
	appendRunnerPayloadEvent(t, repo, claim.Claim, "discard-error", "content_reset", map[string]any{
		"text": "Completion verification failed; no result was accepted.",
	})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "finish-failed-history", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"verification failed"}`),
	}); err != nil {
		t.Fatal(err)
	}

	messages, authoritative, err := server.loadTranscriptWebHistory(context.Background(), "local", "frame-failed-history")
	if err != nil || !authoritative || len(messages) != 2 {
		t.Fatalf("messages=%#v authoritative=%t err=%v", messages, authoritative, err)
	}
	assertTranscriptTextMessage(t, messages[1], "Intermediate analysis. Final diagnostic conclusion.")
	if messages[1]["terminal_status"] != "failed" || messages[1]["status"] != "error" {
		t.Fatalf("failed assistant message=%#v", messages[1])
	}
	frame, found, err := store.GetCompatibilityFrame("frame-failed-history")
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	readCursor, active, err := server.compatibilityFrameReadCursorCoordinates(context.Background(), frame)
	if err != nil || !active || len(readCursor.coordinates) != len(messages) {
		t.Fatalf("readCursor=%#v active=%t messages=%#v err=%v", readCursor, active, messages, err)
	}
	if readCursor.coordinates[1].id != webString(messages[1]["id"]) ||
		readCursor.coordinates[1].messageID != webString(messages[1]["msg_id"]) {
		t.Fatalf("assistant coordinate=%#v message=%#v", readCursor.coordinates[1], messages[1])
	}
}

func TestPreserveTranscriptTerminalFailureCandidatesIsClosedToLegacyMarkers(t *testing.T) {
	attempt := int64(1)
	event := func(eventType, payload string) transcriptstore.ProjectedEvent {
		return transcriptstore.ProjectedEvent{
			Event:               transcriptstore.Event{Type: eventType, RunnerAttempt: &attempt},
			ResolvedPayloadJSON: []byte(payload),
		}
	}
	for _, test := range []struct {
		name      string
		status    string
		resetText string
		wantReset bool
	}{
		{name: "failed legacy marker", status: "failed", resetText: "Completion failed before a result was accepted."},
		{name: "cancelled empty discard", status: "cancelled", resetText: ""},
		{name: "failed explicit replacement", status: "failed", resetText: "A bounded replacement explanation.", wantReset: true},
		{name: "completed empty reset", status: "completed", resetText: "", wantReset: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetPayload, err := json.Marshal(map[string]any{"text": test.resetText})
			if err != nil {
				t.Fatal(err)
			}
			terminalPayload, err := json.Marshal(map[string]any{"status": test.status, "detail": "done"})
			if err != nil {
				t.Fatal(err)
			}
			projected, err := preserveTranscriptTerminalFailureCandidates([]transcriptstore.ProjectedEvent{
				event("content_delta", `{"text":"candidate"}`),
				{Event: transcriptstore.Event{Type: "content_reset", RunnerAttempt: &attempt}, ResolvedPayloadJSON: resetPayload},
				{Event: transcriptstore.Event{Type: "runner_finished", RunnerAttempt: &attempt}, ResolvedPayloadJSON: terminalPayload},
			})
			if err != nil {
				t.Fatal(err)
			}
			gotReset := len(projected) == 3
			if gotReset != test.wantReset {
				t.Fatalf("projected=%#v gotReset=%t wantReset=%t", projected, gotReset, test.wantReset)
			}
		})
	}
}

func TestTranscriptToolHistoryRejectsNormalizedOrMistypedIdentity(t *testing.T) {
	attempt := int64(1)
	projected := transcriptstore.ProjectedEvent{Event: transcriptstore.Event{
		StreamUID: "frame:identity", EventID: 1, Type: "runner_checkpoint", Source: transcriptstore.EventSourcePayload,
		RunnerAttempt: &attempt, ClientMessageID: "checkpoint",
	}}
	for _, value := range []any{" call-1", "call-1 ", "call-1\n", float64(1)} {
		payload := map[string]any{
			"status": "running", "toolPhase": "start", "toolCallId": value, "toolName": "Read",
			"toolInput": map[string]any{"path": "report.txt"},
		}
		if _, found, err := transcriptToolHistoryFactFromEvent(projected, payload); !found || err == nil {
			t.Fatalf("identity=%#v found=%t err=%v", value, found, err)
		}
	}
	if _, found, err := transcriptToolHistoryFactFromEvent(projected, map[string]any{
		"status": "running", "toolPhase": nil, "toolCallId": "call-1", "toolName": "Read",
	}); !found || err == nil {
		t.Fatalf("explicit null phase found=%t err=%v", found, err)
	}
}

func TestTranscriptToolHistoryAcceptsOutcomeUnknownRecoverySettlement(t *testing.T) {
	attempt := int64(2)
	projected := transcriptstore.ProjectedEvent{Event: transcriptstore.Event{
		StreamUID: "frame:recovery", EventID: 48, Type: "runner_checkpoint", Source: transcriptstore.EventSourcePayload,
		RunnerAttempt: &attempt, ClientMessageID: "recovery-checkpoint",
	}}
	payload := map[string]any{
		"status": "failed", "toolPhase": "outcome_unknown",
		"toolCallId": "call-recovered", "toolName": "WebResearch",
		"toolInput": map[string]any{"query": "recovery"},
		"toolResult": map[string]any{
			"ok": false, "error": map[string]any{
				"code": "tool_outcome_unknown", "message": "Tool execution outcome is unavailable after runner recovery.",
			},
		},
	}
	fact, found, err := transcriptToolHistoryFactFromEvent(projected, payload)
	if err != nil || !found {
		t.Fatalf("outcome_unknown fact found=%t err=%v", found, err)
	}
	if fact.status != "error" || fact.callID != "call-recovered" || fact.name != "WebResearch" {
		t.Fatalf("outcome_unknown fact=%#v", fact)
	}
	// The terminal recovery settlement must merge after a running start fact.
	startProjected := projected
	startPayload := map[string]any{
		"status": "running", "toolPhase": "start",
		"toolCallId": "call-recovered", "toolName": "WebResearch",
		"toolInput": map[string]any{"query": "recovery"},
	}
	startFact, found, err := transcriptToolHistoryFactFromEvent(startProjected, startPayload)
	if err != nil || !found || startFact.status != "running" {
		t.Fatalf("start fact found=%t err=%v fact=%#v", found, err, startFact)
	}
	merged, err := mergeTranscriptToolHistoryFact(startFact, fact)
	if err != nil || merged.status != "error" {
		t.Fatalf("merge outcome_unknown err=%v merged=%#v", err, merged)
	}
}

func TestTranscriptWebToolRecoveryNormalizerKeepsImmutableSourceInput(t *testing.T) {
	attempt := int64(2)
	projected := func(eventID int64, payload map[string]any) transcriptstore.ProjectedEvent {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return transcriptstore.ProjectedEvent{
			Event: transcriptstore.Event{
				StreamUID: "frame:recovered-tool", EventID: eventID, PublicationSeq: eventID,
				ClientMessageID: "checkpoint-" + string(rune('0'+eventID)), Type: "runner_checkpoint",
				Source: transcriptstore.EventSourcePayload, RunnerAttempt: &attempt,
			},
			ResolvedPayloadJSON: raw,
		}
	}
	originalInput := map[string]any{"code": "print(1)", "background": false}
	legacyRecoveredInput := map[string]any{"code": "print(1)"}
	events := []transcriptstore.ProjectedEvent{
		projected(1, map[string]any{
			"status": "running", "toolPhase": "start", "toolCallId": "call-1", "toolName": "python",
			"toolInput": originalInput, "message": "tool python started", "resumeCacheKey": chatToolCheckpointPhase("start", "call-1"),
		}),
		projected(2, map[string]any{
			"status": "running", "toolPhase": "start", "toolCallId": "call-1", "toolName": "python",
			"toolInput": legacyRecoveredInput, "message": "tool python resumed", "resumeCacheKey": chatToolCheckpointPhase("start", "call-1"),
		}),
	}
	normalized, err := normalizeTranscriptWebProjectionEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	var recovered map[string]any
	if err := json.Unmarshal(normalized[1].ResolvedPayloadJSON, &recovered); err != nil {
		t.Fatal(err)
	}
	if input, ok := recovered["toolInput"].(map[string]any); !ok || input["background"] != false {
		t.Fatalf("normalized recovery input=%#v", recovered["toolInput"])
	}
	var immutable map[string]any
	if err := json.Unmarshal(events[1].ResolvedPayloadJSON, &immutable); err != nil {
		t.Fatal(err)
	}
	if input, ok := immutable["toolInput"].(map[string]any); !ok {
		t.Fatalf("immutable source input=%#v", immutable["toolInput"])
	} else if _, changed := input["background"]; changed {
		t.Fatal("projection normalization changed the immutable source event")
	}
	state := newTranscriptToolHistoryState()
	for index, event := range normalized {
		payload, err := transcriptPayloadObject(event.ResolvedPayloadJSON)
		if err != nil {
			t.Fatal(err)
		}
		if _, found, err := state.consume(event, payload, index); err != nil || !found {
			t.Fatalf("event %d found=%t err=%v", index, found, err)
		}
	}
}

func TestTranscriptWebProjectionNormalizerOmitsAllLegacySyntheticProgress(t *testing.T) {
	attempt := int64(23)
	projected := func(eventID int64, eventType string, payload map[string]any) transcriptstore.ProjectedEvent {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return transcriptstore.ProjectedEvent{Event: transcriptstore.Event{
			StreamUID: "frame:late-progress", EventID: eventID, PublicationSeq: eventID,
			ClientMessageID: fmt.Sprintf("event-%d", eventID), Type: eventType,
			Source: transcriptstore.EventSourcePayload, RunnerAttempt: &attempt,
		}, ResolvedPayloadJSON: raw}
	}
	events := []transcriptstore.ProjectedEvent{
		projected(1, "content_delta", map[string]any{
			"text": "正在分析任务要求并确定下一步…", "synthetic_progress": true,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		}),
		projected(2, "content_reset", map[string]any{
			"text": "", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, transcriptstore.AssistantReplaceScopeSegment),
		}),
		projected(3, "content_delta", map[string]any{
			"text": "真实内容", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
		}),
		projected(4, "content_delta", map[string]any{
			"text": "正在分析任务要求并确定下一步…", "synthetic_progress": true,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		}),
		projected(5, "content_delta", map[string]any{
			"text": "正在准备下一段…", "synthetic_progress": true,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, ""),
		}),
		projected(6, "content_reset", map[string]any{
			"text": "", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(4, transcriptstore.AssistantReplaceScopeSegment),
		}),
		projected(7, "content_delta", map[string]any{
			"text": "后一段真实内容", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(4, ""),
		}),
		projected(8, "content_delta", map[string]any{
			"text": "正在执行：搜索资料…", "public_progress": true,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(5, ""),
		}),
	}
	normalized, err := normalizeTranscriptWebProjectionEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 2 || normalized[0].Event.EventID != 3 || normalized[len(normalized)-1].Event.EventID != 7 {
		t.Fatalf("normalized events=%#v", normalized)
	}
	oldIdentity := transcriptAssistantMessageID("late-progress", attempt, 1)
	currentIdentity := transcriptAssistantMessageID("late-progress", attempt, 2)
	if !transcriptWebIncrementalSyntheticProgressIsStale(
		true, oldIdentity, currentIdentity, false, map[string]bool{oldIdentity: true},
	) {
		t.Fatal("incremental projection accepted a superseded synthetic progress identity")
	}
}

func TestMergeTranscriptToolHistoryReplacesWaitingResultAtSettlement(t *testing.T) {
	current := transcriptToolHistoryFact{
		attempt: 1, callID: "call-1", name: "python", status: "running",
		result: map[string]any{"status": "awaiting_approval"}, resultSet: true,
	}
	terminal := transcriptToolHistoryFact{
		attempt: 1, callID: "call-1", name: "python", status: "error",
		result: map[string]any{"status": "failed", "code": "execution_failed"}, resultSet: true,
	}
	merged, err := mergeTranscriptToolHistoryFact(current, terminal)
	if err != nil || merged.status != "error" {
		t.Fatalf("merged=%#v err=%v", merged, err)
	}
	result, ok := merged.result.(map[string]any)
	if !ok || result["status"] != "failed" {
		t.Fatalf("terminal result=%#v", merged.result)
	}
	if _, err := mergeTranscriptToolHistoryFact(merged, transcriptToolHistoryFact{
		attempt: 1, callID: "call-1", name: "python", status: "error",
		result: map[string]any{"status": "different"}, resultSet: true,
	}); err == nil {
		t.Fatal("a second divergent terminal result was accepted")
	}
}

func TestTranscriptToolHistorySettlesEveryActiveLifecycleState(t *testing.T) {
	for _, status := range []string{"running", "waiting", "blocked"} {
		state := newTranscriptToolHistoryState()
		identity := "tool:" + status
		state.byIdentity[identity] = transcriptToolHistoryRecord{
			identity: identity,
			fact: transcriptToolHistoryFact{
				attempt: 1, callID: "call-" + status, name: "python", status: status,
			},
		}
		actions := state.settleAttempt(1, "completed")
		if len(actions) != 1 || actions[0].fact.status != "interrupted" ||
			actions[0].fact.settlement != "task_completed_without_tool_receipt" {
			t.Fatalf("status=%q actions=%#v", status, actions)
		}
	}
}

func TestTranscriptToolHistoryReconcilesWaitingToolReceiptAcrossRunnerAttempt(t *testing.T) {
	state := newTranscriptToolHistoryState()
	state.byIdentity["tool:waiting"] = transcriptToolHistoryRecord{
		identity: "tool:waiting",
		fact: transcriptToolHistoryFact{
			attempt: 1, callID: "call-waiting", name: "python", status: "waiting",
			input: map[string]any{"code": "print(1)"}, inputSet: true,
		},
	}
	identity, record, found, err := state.findCompatibleRunningTool(transcriptToolHistoryFact{
		attempt: 2, callID: "call-waiting", name: "python", status: "completed",
		input: map[string]any{"code": "print(1)"}, inputSet: true,
	})
	if err != nil || !found || identity != "tool:waiting" || record.fact.attempt != 1 {
		t.Fatalf("identity=%q record=%#v found=%t err=%v", identity, record, found, err)
	}
}

func TestTranscriptToolHistoryIdentityStaysStableWithinBrowserProtocolLimit(t *testing.T) {
	identity := transcriptToolHistoryIdentity(strings.Repeat("stream", 80), 12, strings.Repeat("call", 80))
	if len(identity) > 256 || identity != transcriptToolHistoryIdentity(strings.Repeat("stream", 80), 12, strings.Repeat("call", 80)) {
		t.Fatalf("identity=%q bytes=%d", identity, len(identity))
	}
	if identity == transcriptToolHistoryIdentity(strings.Repeat("stream", 80), 13, strings.Repeat("call", 80)) {
		t.Fatal("runner attempts produced the same bounded tool identity")
	}
}

func TestTranscriptToolHistoryPublicListSingleCursorAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebFrame(t, store, "local", "project-tool-public", "frame-tool-public")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-tool-public", MessageUUID: "user-message", ClientMessageID: "user-client", Text: "inspect",
	}); err != nil {
		t.Fatal(err)
	}
	stream, _, _ := repo.GetFrameStreamBySession(context.Background(), "local", "frame-tool-public")
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	appendRunnerPayloadEvent(t, repo, claim.Claim, "before", "content_delta", map[string]any{"text": "Before"})
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-public", "toolName": "Read",
		"toolInput": map[string]any{"path": "report.txt"},
	})
	runningPage := transcriptToolHistoryPage(t, server, "/api/conversations/frame-tool-public/messages?limit=10", "")
	runningItems, _ := runningPage["items"].([]any)
	runningNewestCursor := webString(runningPage["newest_cursor"])
	if len(runningItems) != 3 || runningNewestCursor == "" {
		t.Fatalf("running page=%#v", runningPage)
	}
	runningTool := runningItems[2].(map[string]any)
	stableID := webString(runningTool["id"])
	if runningTool["content"].(map[string]any)["status"] != "running" {
		t.Fatalf("running tool=%#v", runningTool)
	}
	detailRecords := make([]any, 105)
	for index := range detailRecords {
		detailRecords[index] = map[string]any{
			"id": fmt.Sprintf("record-%03d", index+1), "title": "Shared title",
			"measurement": map[string]any{"value": index + 1, "unit": "nM"},
		}
	}
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-public", "toolName": "Read",
		"toolInput":  map[string]any{"path": "report.txt"},
		"toolResult": map[string]any{"content": "done", "records": detailRecords},
	})
	completedPage := transcriptToolHistoryPage(t, server, "/api/conversations/frame-tool-public/messages?limit=10", "")
	completedItems, _ := completedPage["items"].([]any)
	if len(completedItems) != 3 || webString(completedItems[2].(map[string]any)["id"]) != stableID ||
		completedItems[2].(map[string]any)["content"].(map[string]any)["status"] != "completed" ||
		completedPage["newest_cursor"] == runningNewestCursor {
		t.Fatalf("completion changed coordinates running=%#v completed=%#v", runningPage, completedPage)
	}
	stale := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-tool-public/messages?before="+url.QueryEscape(runningNewestCursor)+"&limit=2", nil, "")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale cursor status=%d body=%s", stale.Code, stale.Body.String())
	}
	anchoredPage := transcriptToolHistoryPage(t, server,
		"/api/conversations/frame-tool-public/messages?anchor_message_id="+url.QueryEscape(stableID)+"&limit=3", "")
	anchoredItems, _ := anchoredPage["items"].([]any)
	if len(anchoredItems) != 3 || webString(anchoredItems[2].(map[string]any)["id"]) != stableID {
		t.Fatalf("anchored page=%#v", anchoredPage)
	}
	appendRunnerPayloadEvent(t, repo, claim.Claim, "after", "content_delta", map[string]any{"text": "After"})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"done"}`),
	}); err != nil {
		t.Fatal(err)
	}
	finalPage := transcriptToolHistoryPage(t, server, "/api/conversations/frame-tool-public/messages?limit=2", "")
	finalItems, _ := finalPage["items"].([]any)
	finalOldestCursor := webString(finalPage["oldest_cursor"])
	if len(finalItems) != 2 || finalOldestCursor == "" ||
		finalPage["newest_cursor"] == finalOldestCursor ||
		finalPage["has_more_before"] != true {
		t.Fatalf("final page=%#v", finalPage)
	}
	finalTool := finalItems[0].(map[string]any)
	if webString(finalTool["id"]) != stableID || finalTool["content"].(map[string]any)["status"] != "completed" {
		t.Fatalf("final tool=%#v stable=%q", finalTool, stableID)
	}
	previousPage := transcriptToolHistoryPage(t, server,
		"/api/conversations/frame-tool-public/messages?before="+url.QueryEscape(finalOldestCursor)+"&limit=2", "")
	previousItems, _ := previousPage["items"].([]any)
	if len(previousItems) != 2 || webString(previousItems[0].(map[string]any)["id"]) != "user-message" ||
		webString(previousItems[1].(map[string]any)["id"]) != "assistant-frame-tool-public-1" {
		t.Fatalf("previous page=%#v", previousPage)
	}
	single := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-tool-public/messages/"+url.PathEscape(stableID), nil, "")
	singleObject := p3DecodeObject(t, single)
	if single.Code != http.StatusOK || webString(singleObject["id"]) != stableID {
		t.Fatalf("single status=%d body=%s", single.Code, single.Body.String())
	}
	detail := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-tool-public/messages/"+url.PathEscape(stableID)+
			"/tool-detail?section=output&path="+url.QueryEscape("/records")+"&limit=100", nil, "")
	if detail.Code != http.StatusOK {
		t.Fatalf("tool detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	detailPage := p3DecodeObject(t, detail)
	detailItems, _ := detailPage["items"].([]any)
	detailCursor := webString(detailPage["next_cursor"])
	if detailPage["kind"] != "array" || detailPage["total"] != float64(105) || len(detailItems) != 100 || detailCursor == "" {
		t.Fatalf("tool detail page=%#v", detailPage)
	}
	detailNext := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-tool-public/messages/"+url.PathEscape(stableID)+
			"/tool-detail?cursor="+url.QueryEscape(detailCursor), nil, "")
	detailNextPage := p3DecodeObject(t, detailNext)
	detailNextItems, _ := detailNextPage["items"].([]any)
	if detailNext.Code != http.StatusOK || len(detailNextItems) != 5 || detailNextPage["from"] != float64(100) {
		t.Fatalf("tool detail next status=%d page=%#v", detailNext.Code, detailNextPage)
	}
	staleDetail := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-tool-public/messages/"+url.PathEscape(stableID)+
			"/tool-detail?section=output&revision=999", nil, "")
	if staleDetail.Code != http.StatusConflict {
		t.Fatalf("stale tool detail status=%d body=%s", staleDetail.Code, staleDetail.Body.String())
	}
	foreign := p3JSONRequest(t, server, http.MethodGet, "/api/conversations/frame-tool-public/messages?limit=10", nil, "foreign")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	foreignDetail := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-tool-public/messages/"+url.PathEscape(stableID)+"/tool-detail", nil, "foreign")
	if foreignDetail.Code != http.StatusNotFound {
		t.Fatalf("foreign detail status=%d body=%s", foreignDetail.Code, foreignDetail.Body.String())
	}
	beforeReopen := transcriptToolHistoryMessageIDs(t, transcriptToolHistoryPage(t, server,
		"/api/conversations/frame-tool-public/messages?limit=10", ""))
	closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := server.Close(closeContext); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, err := workspace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })
	reopenedRepo, err := reopenedStore.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reopenedServer := New(Options{Workspace: reopenedStore, Transcript: reopenedRepo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = reopenedServer.Close(ctx)
	})
	afterReopen := transcriptToolHistoryMessageIDs(t, transcriptToolHistoryPage(t, reopenedServer,
		"/api/conversations/frame-tool-public/messages?limit=10", ""))
	if !reflect.DeepEqual(beforeReopen, afterReopen) {
		t.Fatalf("before=%#v after=%#v", beforeReopen, afterReopen)
	}
}

func transcriptToolHistoryPage(t *testing.T, server *Server, path, userID string) map[string]any {
	t.Helper()
	response := p3JSONRequest(t, server, http.MethodGet, path, nil, userID)
	if response.Code != http.StatusOK {
		t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
	}
	return p3DecodeObject(t, response)
}

func transcriptToolHistoryMessageIDs(t *testing.T, page map[string]any) []string {
	t.Helper()
	items, ok := page["items"].([]any)
	if !ok {
		t.Fatalf("page=%#v", page)
	}
	identities := make([]string, 0, len(items)*2)
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item=%#v", item)
		}
		identities = append(identities, webString(message["id"]), webString(message["msg_id"]))
	}
	return identities
}

func TestTranscriptWebHistorySettlesRunningToolsAndRejectsConflicts(t *testing.T) {
	tests := []struct {
		name       string
		terminal   string
		toolStatus string
		preText    bool
	}{
		{name: "failed-after-text", terminal: "failed", toolStatus: "interrupted", preText: true},
		{name: "cancelled-after-text", terminal: "cancelled", toolStatus: "canceled", preText: true},
		// A task receipt proves only the task terminal state. An open tool without
		// its own result receipt must never be painted as a successful operation.
		{name: "completed-tool-only", terminal: "completed", toolStatus: "interrupted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, repo, _ := newTranscriptWebFixture(t)
			frameID := "frame-tool-" + test.terminal
			seedTranscriptWebFrame(t, store, "local", "project-tool-"+test.terminal, frameID)
			server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
			if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
				FrameID: frameID, MessageUUID: "user-message", ClientMessageID: "user-client", Text: "run tool",
			}); err != nil {
				t.Fatal(err)
			}
			stream, _, _ := repo.GetFrameStreamBySession(context.Background(), "local", frameID)
			claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool", TTL: time.Minute,
				ResumeSource: transcriptstore.ResumeSourceFresh,
			})
			if err != nil || !claim.Claimed {
				t.Fatalf("claim=%#v err=%v", claim, err)
			}
			if test.preText {
				appendRunnerPayloadEvent(t, repo, claim.Claim, "before", "content_delta", map[string]any{"text": "Before tool"})
			}
			appendRunnerToolCheckpoint(t, repo, claim.Claim, "tool-start", map[string]any{
				"status": "running", "toolPhase": "start", "toolCallId": "call-active", "toolName": "WebSearch",
				"toolInput": map[string]any{"query": "NEK7"},
			})
			if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
				Claim: claim.Claim, ClientMessageID: "finish", Status: test.terminal,
				PayloadJSON: []byte(`{"status":"` + test.terminal + `","detail":"terminal"}`),
			}); err != nil {
				t.Fatal(err)
			}
			messages, _, err := server.loadTranscriptWebHistory(context.Background(), "local", frameID)
			if err != nil {
				t.Fatal(err)
			}
			tool := transcriptToolMessageByCallID(t, messages, "call-active")
			if tool["content"].(map[string]any)["status"] != test.toolStatus {
				t.Fatalf("tool=%#v", tool)
			}
			terminal := messages[len(messages)-1]
			terminalContent, _ := terminal["content"].(map[string]any)
			wantTerminalDetail := "terminal"
			if test.terminal == "failed" {
				// Failed terminal receipts are published through the public
				// boundary; raw diagnostics remain durable but are not exposed in
				// the conversation projection.
				wantTerminalDetail = publicSessionRunnerFailureMessage("terminal", "en")
			}
			if terminal["type"] != "text" || terminal["terminal_status"] != test.terminal ||
				terminalContent["content"] != wantTerminalDetail {
				t.Fatalf("terminal=%#v messages=%#v", terminal, messages)
			}
			if test.preText && webString(messages[len(messages)-2]["id"]) != webString(tool["id"]) {
				t.Fatalf("tool was not immediately before terminal messages=%#v", messages)
			}
		})
	}
}

func TestTranscriptWebHistoryRejectsConflictingToolFactsAndHidesVerification(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-tool-conflict", "frame-tool-conflict")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-tool-conflict", MessageUUID: "user-message", ClientMessageID: "user-client", Text: "inspect",
	}); err != nil {
		t.Fatal(err)
	}
	stream, _, _ := repo.GetFrameStreamBySession(context.Background(), "local", "frame-tool-conflict")
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "verification", map[string]any{
		"status": "running", "toolPhase": "verification_tool", "toolCallId": "review-call", "toolName": "Read",
	})
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "review-policy", map[string]any{
		"status": "completed", "toolPhase": "review_policy", "toolCallId": "review-policy-1",
		"resolvedReviewPolicy": map[string]any{"authority": "server"},
	})
	verificationMessages, _, err := server.loadTranscriptWebHistory(context.Background(), "local", "frame-tool-conflict")
	if err != nil || len(verificationMessages) != 1 {
		t.Fatalf("verification messages=%#v err=%v", verificationMessages, err)
	}
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-conflict", "toolName": "Read",
		"toolInput": map[string]any{"path": "first.txt"},
	})
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "tool-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-conflict", "toolName": "Read",
		"toolInput": map[string]any{"path": "different.txt"}, "toolResult": map[string]any{"ok": true},
	})
	if _, _, err := server.loadTranscriptWebHistory(context.Background(), "local", "frame-tool-conflict"); err == nil {
		t.Fatal("conflicting durable tool facts were accepted")
	}
}

func appendRunnerPayloadEvent(
	t *testing.T, repo *transcriptstore.Repository, claim transcriptstore.RunnerClaim, clientID, eventType string, payload map[string]any,
) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: claim, ClientMessageID: clientID, Type: eventType, Source: transcriptstore.EventSourcePayload, PayloadJSON: raw,
	}); err != nil {
		t.Fatal(err)
	}
}

func appendRunnerToolCheckpoint(
	t *testing.T, repo *transcriptstore.Repository, claim transcriptstore.RunnerClaim, clientID string, payload map[string]any,
) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: clientID, Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: raw,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertTranscriptTextMessage(t *testing.T, message map[string]any, expected string) {
	t.Helper()
	content, ok := message["content"].(map[string]any)
	if !ok || message["type"] != "text" || content["content"] != expected {
		t.Fatalf("message=%#v", message)
	}
}

func transcriptToolMessageByCallID(t *testing.T, messages []map[string]any, callID string) map[string]any {
	t.Helper()
	for _, message := range messages {
		content, ok := message["content"].(map[string]any)
		if ok && message["type"] == "tool_call" && content["call_id"] == callID {
			return message
		}
	}
	t.Fatalf("tool %q missing from %#v", callID, messages)
	return nil
}
