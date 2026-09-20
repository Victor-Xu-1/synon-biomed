package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestPlanModeProtocolRecoveryPersistsUntilRealPlanApproval(t *testing.T) {
	ctx := context.Background()
	store, repo, _ := newTranscriptWebFixture(t)
	const frame = "plan-continuity"
	seedTranscriptWebFrame(t, store, "local", "plan-continuity-project", frame)
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(ctx) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{FrameID: frame, MessageUUID: "plan-message", ClientMessageID: "plan-input", Text: "Prepare an evidence plan and wait for approval."}); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(map[string]any{
		"human_description": "Preparing the plan for approval", "task_summary": "Prepare a reviewed evidence report.",
		"phases":          []any{map[string]any{"name": "Review", "delegations": []any{map[string]any{"name": "Evidence", "steps": []any{map[string]any{"title": "Inspect sources", "description": "Check admitted sources before execution."}}}}}},
		"desired_outputs": []any{"reviewed report"}, "feasibility": map[string]any{"confidence": "high", "rationale": "Source inspection is available."},
	})
	var allowPlan atomic.Bool
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		message := map[string]any{"role": "assistant", "content": "Here is the prose plan candidate."}
		if strings.Contains(string(mustJSON(t, request.Messages)), "Write a brief expert orientation before the task begins.") {
			message["content"] = "The plan will be prepared for approval."
		} else {
			requests.Add(1)
			if allowPlan.Load() {
				message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "real-plan", "type": "function", "function": map[string]any{"name": generatePlanToolName, "arguments": string(planArguments)}}}}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	defer provider.Close()
	options := SessionRunnerChatOptions{SessionID: frame, RunnerID: "plan-continuity-runner", Endpoint: provider.URL + "/v1/chat/completions", APIKey: "local-test", Model: "local-test",
		AllowedTools: []string{generatePlanToolName}, LeaseTTL: time.Minute, ReplayLimit: 20, MaxAttempts: 1, MaxToolRounds: 2,
		DisableSkillDiscovery: true, DisableMCPDiscovery: true, RuntimeSessionConfig: map[string]any{"planMode": true}}
	for unit := 1; unit <= 5; unit++ {
		allowPlan.Store(unit == 5)
		result, err := server.RunSessionRunnerChatOnce(ctx, options)
		if err != nil || !result.Claimed {
			t.Fatalf("unit %d: result=%#v error=%v", unit, result, err)
		}
		if unit == 5 {
			if result.Status != "awaiting_approval" {
				t.Fatalf("real plan did not reach user approval: %#v", result)
			}
			metadata, found, err := store.GetFrameRuntimeMetadata(frame)
			if err != nil || !found || stringValue(metadata.ContextData["_plan_artifact_id"]) == "" || boolValue(metadata.ContextData["_plan_approved"], false) {
				t.Fatalf("missing unapproved durable plan: %#v error=%v", metadata, err)
			}
			break
		}
		if result.Status != "interrupted" || result.InterruptionReasonCode != sessionRunnerPlanApprovalRequiredReasonCode || !result.InterruptionAutoResume {
			t.Fatalf("unit %d lost pending plan obligation: %#v", unit, result)
		}
		stream, found, err := repo.GetFrameStreamBySession(ctx, "local", frame)
		if err != nil || !found {
			t.Fatal(err)
		}
		checkpoint, found, err := repo.LatestResumableCheckpoint(ctx, stream.UID, stream.OwnerID)
		if err != nil || !found {
			t.Fatal(err)
		}
		candidate, found, err := repo.GetAutoResumeCandidate(ctx, stream.UID, stream.OwnerID)
		if err != nil || !found || candidate.ReasonCode != sessionRunnerPlanApprovalRequiredReasonCode {
			t.Fatalf("automatic dispatcher lost pending plan: found=%t candidate=%#v error=%v", found, candidate, err)
		}
		options.TranscriptResumeSource, options.TranscriptCheckpoint = transcriptstore.ResumeSourceCheckpoint, checkpoint.Sequence
		options.RunnerID = fmt.Sprintf("plan-continuity-%d", unit)
	}
	t.Logf("four bounded protocol recovery units followed by native durable plan approval; local HTTP requests=%d", requests.Load())
}

func TestCorrectionCancellationCannotScheduleAnotherUnit(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var chatErr error = sessionRunnerPlanApprovalRequired{}
	result := &SessionRunnerCycleResult{}
	handled, err := fixture.server.handleSessionRunnerChatInterruption(ctx, SessionRunnerChatOptions{}, result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, fixture.run.Transcript, fixture.run, nil, &chatErr, nil)
	if err != nil || handled || !errors.Is(chatErr, context.Canceled) || result.InterruptionAutoResume {
		t.Fatalf("cancellation scheduled another correction: result=%#v handled=%t error=%v chatError=%v", result, handled, err, chatErr)
	}
	if _, found, err := fixture.repo.LatestResumableCheckpoint(context.Background(), fixture.stream.UID, fixture.stream.OwnerID); err != nil || found {
		t.Fatalf("cancellation created a resumable checkpoint: found=%t error=%v", found, err)
	}
	model := &planModeCompletionModel{}
	session := sessionstore.Session{ID: fixture.run.SessionID, Orchestration: map[string]any{"sessionConfig": map[string]any{"planMode": true}}}
	_, err = fixture.server.runSessionAgentWithPlanMode(ctx, session, agentruntime.Engine{Model: model}, agentruntime.RunRequest{}, fixture.run, nil)
	if !errors.Is(err, context.Canceled) || model.calls != 0 {
		t.Fatalf("canceled plan started a model call: calls=%d error=%v", model.calls, err)
	}
}

func TestPlanModeDenialHistoryScopesToLogicalInput(t *testing.T) {
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{"type": "runner_checkpoint", "toolPhase": planModeDenialToolPhase, "planModeDenial": 300}},
		{SourceEventType: "user_input_response", Message: eventjournal.Message{"type": "message", "role": "user", "text": "Retain the original method."}},
	}
	if sessionRunnerPlanModeDenialCount(entries) != 300 {
		t.Fatal("same-task clarification erased denial history")
	}
	entries = append(entries, eventjournal.Entry{SourceEventType: "user_message", Message: eventjournal.Message{"type": "message", "role": "user", "text": "Prepare a different plan."}})
	if sessionRunnerPlanModeDenialCount(entries) != 0 {
		t.Fatal("new logical task inherited historical plan denial count")
	}
}

func TestPlanModeRestoreDoesNotHideRecoveryForDifferentTaskInput(t *testing.T) {
	for _, change := range []string{"id", "revision", "content", "missing-artifact", "same-input"} {
		t.Run(change, func(t *testing.T) {
			fixture := newCorrectionRouteFixture(t)
			fixture.run.TaskIntentID, fixture.run.TaskIntentRevision, fixture.run.TaskIntent = "current-intent", 2, "Current task"
			metadata, _, err := fixture.store.GetFrameRuntimeMetadata(fixture.run.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			metadata.ContextData["_plan_artifact_id"] = "approved-plan"
			metadata.ContextData["_plan_approved"] = true
			metadata.ContextData["_plan_task_intent_id"] = fixture.run.TaskIntentID
			metadata.ContextData["_plan_task_intent_revision"] = fixture.run.TaskIntentRevision
			metadata.ContextData["_plan_task_intent_sha256"] = generatedPlanTaskIntentSHA(fixture.run.TaskIntent)
			switch change {
			case "id":
				metadata.ContextData["_plan_task_intent_id"] = "previous-intent"
			case "revision":
				metadata.ContextData["_plan_task_intent_revision"] = 1
			case "content":
				metadata.ContextData["_plan_task_intent_sha256"] = generatedPlanTaskIntentSHA("Previous task")
			case "missing-artifact":
				delete(metadata.ContextData, "_plan_artifact_id")
			}
			if _, err := fixture.store.SetFrameRuntimeMetadata(fixture.run.SessionID, metadata); err != nil {
				t.Fatal(err)
			}
			session := sessionstore.Session{ID: fixture.run.SessionID, Orchestration: map[string]any{"sessionConfig": map[string]any{"planMode": true}}}
			_, approved, err := fixture.server.restoreSessionRunnerPlanControl(session, fixture.run)
			if err != nil || approved != (change == "same-input") {
				t.Fatalf("stale approval hid the plan recovery capability: approved=%t error=%v", approved, err)
			}
			pending, err := fixture.server.sessionRunnerPlanModePending(session, fixture.run)
			if err != nil || pending == approved {
				t.Fatalf("plan admission and completion disagree: approved=%t pending=%t error=%v", approved, pending, err)
			}
		})
	}
}
