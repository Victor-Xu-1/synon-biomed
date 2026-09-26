package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

type conditionPagingModel struct {
	id          string
	pages       int
	lastOffset  int
	startOffset int
	callPrefix  string
}

func (model *conditionPagingModel) Complete(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	offset := max(1, model.startOffset)
	for index := len(request.Messages) - 1; index >= 0; index-- {
		message := request.Messages[index]
		if message.Role != "tool" {
			continue
		}
		var page map[string]any
		if err := json.Unmarshal([]byte(message.Content), &page); err != nil {
			return agentruntime.ModelResponse{}, err
		}
		if page["evidence_kind"] != "runtime_condition" {
			return agentruntime.ModelResponse{}, fmt.Errorf("unexpected condition page: %v", page)
		}
		offset = int(numberValue(page["next_offset"]))
		if offset == 0 {
			return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "Diagnostic coverage complete."}}, nil
		}
		if offset <= model.lastOffset {
			return agentruntime.ModelResponse{}, fmt.Errorf("non-advancing condition read")
		}
		break
	}
	if model.pages >= 100 {
		return agentruntime.ModelResponse{}, fmt.Errorf("test reader exceeded expected page count")
	}
	model.pages++
	model.lastOffset = offset
	arguments, _ := json.Marshal(map[string]any{"recovery_condition_id": model.id, "json_pointer": "/text/detail", "offset": offset, "limit": 200, "human_description": "Reading the next diagnostic window"})
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "Inspecting the next diagnostic window.", ToolCalls: []agentruntime.ToolCall{{ID: fmt.Sprintf("%scondition-page-%d", model.callPrefix, model.pages), Name: "read_file", Arguments: arguments}}}}, nil
}

func TestCorrectionPagedReadsAdvanceEngineWithoutClearingObligation(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	cause := newRunnerTextCorrection(sessionRunnerCompletionReviewRecoveryReasonCode, strings.Repeat("diagnostic context 界 \n", 500))
	run := &sessionRunnerChatRun{SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	run.restoreCorrection(&recoveredRunnerCorrection{ReasonCode: cause.ReasonCode, Detail: cause.Detail, Condition: cause.Condition})
	ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)
	gateway := serverAgentRuntimeToolGateway{server: fixture.server, kernel: fixture.identity, sessionID: run.SessionID, taskRun: run, toolSchemas: []agentruntime.ToolSchema{agentWorkspaceReadFileToolSchema()}, hasToolSnapshot: true, suppressHooks: true, fileReadLimitBytes: 2048}
	model := &conditionPagingModel{id: cause.Condition.ContentID()}
	options := SessionRunnerChatOptions{SessionID: run.SessionID, RunnerID: fixture.claim.RunnerID}
	reads := 0
	engine := agentruntime.Engine{Model: model, Tools: gateway, OnEventError: func(event agentruntime.Event) error {
		if event.Type == agentruntime.EventModelResponse {
			return fixture.server.checkpointChatModelToolCalls(options, run, event.ToolCalls)
		}
		if event.Type == agentruntime.EventToolCompleted {
			reads++
			var value map[string]any
			if err := json.Unmarshal([]byte(event.Result), &value); err != nil {
				return err
			}
			if runnerToolCompletionHasMaterialProgress(event.ToolName, value) {
				return fmt.Errorf("diagnostic read reset task progress")
			}
		}
		return fixture.server.checkpointSessionRunnerToolEvent(ctx, options, run, event)
	}}
	result, err := engine.Run(ctx, agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Inspect the complete stored runtime condition."}}, Tools: gateway.toolSchemas, MaxConsecutiveIdenticalToolRounds: 2})
	if err != nil || reads <= 2 || result.FinalMessage.Content != "Diagnostic coverage complete." {
		t.Fatalf("page progress prematurely interrupted: pages=%d error=%v", reads, err)
	}
	t.Logf("successive_novel_pages=%d no_progress_round_budget=2", reads)
}

func TestCorrectionReadCursorSurvivesExecutionUnitAndNarrowReplay(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx := context.Background()
	cause := newRunnerTextCorrection(sessionRunnerCompletionReviewRecoveryReasonCode, strings.Repeat("retained diagnostic text 界\n", 500))
	if _, err := fixture.repo.InterruptRunner(ctx, transcriptstore.InterruptRunnerInput{Claim: fixture.claim, ClientMessageID: "seed-complete-condition", ReasonCode: cause.ReasonCode, ResumeDetail: cause.Detail, Cause: &cause, AutoResume: true}); err != nil {
		t.Fatal(err)
	}
	resume := func(runnerID string) *sessionRunnerChatRun {
		checkpoint, found, err := fixture.repo.LatestResumableCheckpoint(ctx, fixture.stream.UID, fixture.stream.OwnerID)
		if err != nil || !found {
			t.Fatalf("checkpoint: %v", err)
		}
		claimed, err := fixture.repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, RunnerID: runnerID, TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence})
		if err != nil || !claimed.Claimed {
			t.Fatalf("resume: %v", err)
		}
		run := &sessionRunnerChatRun{SessionID: fixture.stream.SessionID, Attempt: int(claimed.Claim.Attempt), Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: claimed.Claim}}
		entries, err := fixture.server.loadTranscriptRunnerReplay(ctx, run.Transcript, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		correction, found := latestRunnerCorrection(entries)
		if !found || correction.Condition == nil {
			t.Fatal("complete condition absent after resume")
		}
		run.restoreCorrection(&correction)
		return run
	}
	runPages := func(run *sessionRunnerChatRun, model *conditionPagingModel, rounds int) (agentruntime.RunResult, error) {
		readCtx := context.WithValue(ctx, transcriptRunnerChatRunContextKey{}, run)
		gateway := serverAgentRuntimeToolGateway{server: fixture.server, kernel: fixture.identity, sessionID: run.SessionID, taskRun: run, toolSchemas: []agentruntime.ToolSchema{agentWorkspaceReadFileToolSchema()}, hasToolSnapshot: true, suppressHooks: true, fileReadLimitBytes: 2048}
		options := SessionRunnerChatOptions{SessionID: run.SessionID, RunnerID: run.Transcript.Claim.RunnerID}
		engine := agentruntime.Engine{Model: model, Tools: gateway, OnEventError: func(event agentruntime.Event) error {
			if event.Type == agentruntime.EventModelResponse {
				return fixture.server.checkpointChatModelToolCalls(options, run, event.ToolCalls)
			}
			return fixture.server.checkpointSessionRunnerToolEvent(readCtx, options, run, event)
		}}
		return engine.Run(readCtx, agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Inspect the complete condition."}}, Tools: gateway.toolSchemas, MaxToolRounds: rounds, MaxConsecutiveIdenticalToolRounds: 2})
	}
	first := resume("condition-first-reader")
	firstModel := &conditionPagingModel{id: cause.Condition.ContentID()}
	_, runErr := runPages(first, firstModel, 3)
	var boundary *agentruntime.ToolRoundLimitError
	if !errors.As(runErr, &boundary) {
		t.Fatalf("expected bounded execution-unit handoff: proposals=%d error=%v", firstModel.pages, runErr)
	}
	cycle := &SessionRunnerCycleResult{SessionID: first.SessionID, Attempt: first.Attempt}
	if handled, err := fixture.server.handleSessionRunnerChatInterruption(ctx, SessionRunnerChatOptions{SessionID: first.SessionID, RunnerID: first.Transcript.Claim.RunnerID}, cycle, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, first.Transcript, first, nil, &runErr, nil); err != nil || !handled || !cycle.InterruptionAutoResume {
		t.Fatalf("durable boundary: handled=%t error=%v", handled, err)
	}
	second := resume("condition-second-reader")
	entries, err := fixture.server.loadTranscriptRunnerReplay(ctx, second.Transcript, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	recovered, found := latestRunnerCorrection(entries)
	if !found || recovered.ReadCoverage == nil || recovered.ReadCoverage.NextOffset <= 1 || recovered.ReadCoverage.ContiguousThrough != recovered.ReadCoverage.EndLine {
		t.Fatalf("narrow replay lost delivered cursor: %#v", recovered.ReadCoverage)
	}
	lastReceiptPinned := false
	for _, entry := range entries {
		if entry.EventID == recovered.ReadCoverage.EventID && entry.Message["toolName"] == "read_file" {
			lastReceiptPinned = true
		}
	}
	if !lastReceiptPinned {
		t.Fatal("cursor survived but its latest real read receipt was evicted")
	}
	contractText := recoveredRunnerCorrectionContext(entries)
	parts := strings.SplitN(contractText, "\n", 2)
	if len(parts) != 2 {
		t.Fatal("recovery contract missing")
	}
	var contract map[string]any
	if err := json.Unmarshal([]byte(parts[1]), &contract); err != nil {
		t.Fatal(err)
	}
	readWith := mapValue(mapValue(contract["condition"])["read_with"])
	start := int(numberValue(readWith["offset"]))
	if start != recovered.ReadCoverage.NextOffset || readWith["json_pointer"] != "/text/detail" {
		t.Fatalf("wrong continuation selector: %#v", readWith)
	}
	secondModel := &conditionPagingModel{id: cause.Condition.ContentID(), startOffset: start, callPrefix: "resumed-"}
	result, err := runPages(second, secondModel, 0)
	if err != nil || result.FinalMessage.Content != "Diagnostic coverage complete." {
		t.Fatalf("resumed reader failed: %v", err)
	}
	finalEntries, err := fixture.server.loadTranscriptRunnerReplay(ctx, second.Transcript, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	final, found := latestRunnerCorrection(finalEntries)
	if !found || final.ReadCoverage == nil || final.ReadCoverage.ContiguousThrough != final.ReadCoverage.TotalLines {
		t.Fatalf("coverage incomplete: %#v", final.ReadCoverage)
	}
	if count := runnerRepeatedCorrectionInterruptionCount(finalEntries, cause); count != 1 {
		t.Fatalf("reading changed correction count: %d", count)
	}
	t.Logf("first_unit_proposals=%d resumed_offset=%d second_unit_pages=%d retained_prefix_lines=%d", firstModel.pages, start, secondModel.pages, final.ReadCoverage.TotalLines)
}
