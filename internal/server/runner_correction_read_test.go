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

func TestCorrectionCompleteConditionPersistsReplaysAndReadsThroughGateway(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx := context.Background()
	failure := sessionRunnerCompletionReviewCorrection{Summary: "Review findings", Issues: []sessionRunnerReviewIssue{{
		MessageIndex: 3, Severity: "major", Claim: strings.Repeat("claim fragment ", 85000) + "final claim tail",
		EvidenceRefs: []string{"receipt-original"}, EvidenceQuote: strings.Repeat("quoted 界证据 \n\t\"literal\" ", 200) + "exact quoted tail",
	}}}
	cause := failure.runnerCorrection()
	raw, err := transcriptstore.EncodeRunnerCorrectionCondition(*cause.Condition)
	if err != nil || len(raw) <= transcriptstore.MaxEventPayloadBytes {
		t.Fatalf("fixture bytes=%d error=%v", len(raw), err)
	}
	authority := &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}
	run := &sessionRunnerChatRun{SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), Transcript: authority}
	options := SessionRunnerChatOptions{SessionID: run.SessionID, RunnerID: fixture.claim.RunnerID}
	var chatErr error = failure
	result := &SessionRunnerCycleResult{SessionID: run.SessionID, Attempt: run.Attempt}
	if handled, err := fixture.server.handleSessionRunnerChatInterruption(ctx, options, result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, authority, run, nil, &chatErr, nil); err != nil || !handled || !result.InterruptionAutoResume {
		t.Fatalf("large real handler interruption: handled=%t result=%#v error=%v", handled, result, err)
	}
	checkpoint, found, err := fixture.repo.LatestResumableCheckpoint(ctx, fixture.stream.UID, fixture.stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("checkpoint: %v", err)
	}
	resumed, err := fixture.repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, RunnerID: "condition-resumed", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence})
	if err != nil || !resumed.Claimed {
		t.Fatalf("resume: %v", err)
	}
	authority = &transcriptRunnerAuthority{Stream: fixture.stream, Claim: resumed.Claim}
	for index := 0; index < 1020; index++ {
		appendRunnerToolCheckpoint(t, fixture.repo, resumed.Claim, fmt.Sprintf("condition-audit-%d", index), map[string]any{"status": "running", "audit": index})
	}
	entries, err := fixture.server.loadTranscriptRunnerReplay(ctx, authority, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	encodedReplay, err := json.Marshal(entries)
	if err != nil || len(encodedReplay) > 50000 || strings.Contains(string(encodedReplay), "final claim tail") {
		t.Fatal("full condition leaked into serializable history")
	}
	entries, err = fixture.server.runnerEntriesForCurrentLogicalInput(ctx, entries, &sessionRunnerChatRun{SessionID: run.SessionID, Attempt: int(resumed.Claim.Attempt), Transcript: authority})
	if err != nil {
		t.Fatal(err)
	}
	recovered, found := latestRunnerCorrection(entries)
	if !found || recovered.Condition == nil || recovered.Condition.ContentID() != cause.Condition.ContentID() {
		t.Fatal("bounded replay lost exact complete condition")
	}
	if count := runnerRepeatedCorrectionInterruptionCount(entries, cause); count != 1 {
		t.Fatalf("count=%d", count)
	}
	fresh := &sessionRunnerChatRun{SessionID: run.SessionID, Attempt: int(resumed.Claim.Attempt), Transcript: authority}
	fresh.restoreCorrection(&recovered)
	if !strings.Contains(fresh.CorrectionDetail, "final claim tail") {
		t.Fatal("repair consumer still uses display prefix")
	}
	contract := applyRecoveredRunnerCorrectionToTaskContract(buildSessionRunnerTaskContract("original task", "task", 1), recovered)
	if contract.CorrectionConditionID != cause.Condition.ContentID() || contract.CorrectionFingerprint != cause.Condition.Fingerprint() {
		t.Fatal("task contract lost exact condition binding")
	}
	contextMessage := recoveredRunnerCorrectionContext(entries)
	if len(contextMessage) > 10000 || !strings.Contains(contextMessage, "recovery_condition_id") || strings.Contains(contextMessage, "final claim tail") {
		t.Fatal("context is unbounded or lacks complete evidence navigation")
	}
	readCtx := context.WithValue(ctx, transcriptRunnerChatRunContextKey{}, fresh)
	gateway := serverAgentRuntimeToolGateway{server: fixture.server, kernel: fixture.identity, sessionID: run.SessionID, taskRun: fresh, toolSchemas: []agentruntime.ToolSchema{agentWorkspaceReadFileToolSchema()}, hasToolSnapshot: true, suppressHooks: true, fileReadLimitBytes: 2048}
	var quoted strings.Builder
	offset, pages := 1, 0
	for {
		arguments, _ := json.Marshal(map[string]any{"recovery_condition_id": cause.Condition.ContentID(), "json_pointer": "/review/issues/0/evidence_quote", "offset": offset, "limit": 40, "human_description": "Reading complete correction evidence"})
		response, err := gateway.Execute(readCtx, agentruntime.ToolCall{ID: fmt.Sprintf("condition-read-%d", pages), Name: "read_file", Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		value, ok := response.Value.(map[string]any)
		if !ok || value["ok"] == false || value["complete_condition"] != false || value["evidence_kind"] != "runtime_condition" {
			t.Fatalf("read result=%#v", value)
		}
		encoded, _ := json.Marshal(map[string]any{"ok": true, "result": value})
		if len(encoded) > 2048 || numberValue(value["truncated_lines"]) != 0 {
			t.Fatalf("page budget/coverage invalid: bytes=%d", len(encoded))
		}
		for _, line := range strings.Split(strings.TrimSuffix(stringValue(value["content"]), "\n"), "\n") {
			_, text, found := strings.Cut(line, "\t")
			if !found {
				t.Fatal("missing line coordinate")
			}
			quoted.WriteString(text)
		}
		if runnerToolCompletionHasMaterialProgress("read_file", value) {
			t.Fatal("diagnostic reading reset repair progress")
		}
		if signals := fixture.server.trustedScientificReviewSignalsForCompletedTool("read_file", map[string]any{"recovery_condition_id": cause.Condition.ContentID()}, value, nil); len(signals) > 0 {
			t.Fatalf("diagnostic became scientific evidence: %v", signals)
		}
		pages++
		next := int(numberValue(value["next_offset"]))
		if next == 0 {
			break
		}
		if next <= offset || pages > 100 {
			t.Fatal("non-progressing read cursor")
		}
		offset = next
	}
	var exactQuote string
	if err := json.Unmarshal([]byte(quoted.String()), &exactQuote); err != nil {
		t.Fatalf("quoted JSON could not be reconstructed: %v", err)
	}
	if exactQuote != failure.Issues[0].EvidenceQuote || pages < 2 {
		t.Fatalf("paginated quote lost bytes: pages=%d bytes=%d", pages, len(exactQuote))
	}
	tail, err := fixture.server.executeAgentWorkspaceFileTool(readCtx, fixture.identity, "read_file", map[string]any{"recovery_condition_id": cause.Condition.ContentID(), "json_pointer": "/review/issues/0/claim", "offset": (len(failure.Issues[0].Claim) - 1) / 128, "limit": 2})
	var tailText strings.Builder
	for _, line := range strings.Split(stringValue(mapValue(tail)["content"]), "\n") {
		_, text, _ := strings.Cut(line, "\t")
		tailText.WriteString(text)
	}
	if err != nil || !strings.Contains(tailText.String(), "final claim tail") {
		t.Fatalf("long claim tail unreachable: %v", err)
	}
	for _, pointer := range []string{"/review/issues/0/evidence_refs/0", "/review/issues/0/claim"} {
		input := map[string]any{"recovery_condition_id": cause.Condition.ContentID(), "json_pointer": pointer, "offset": 1, "limit": 1}
		if _, err := fixture.server.executeAgentWorkspaceFileTool(readCtx, fixture.identity, "read_file", input); err != nil {
			t.Fatalf("typed field unavailable: %s %v", pointer, err)
		}
	}
	canceled, cancel := context.WithCancel(readCtx)
	cancel()
	if _, err := fixture.server.executeAgentWorkspaceFileTool(canceled, fixture.identity, "read_file", map[string]any{"recovery_condition_id": cause.Condition.ContentID()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	fresh.restoreCorrection(nil)
	if _, err := fixture.server.executeAgentWorkspaceFileTool(readCtx, fixture.identity, "read_file", map[string]any{"recovery_condition_id": cause.Condition.ContentID()}); err == nil {
		t.Fatal("retired condition stayed readable")
	}
	t.Logf("condition_bytes=%d quote_pages=%d exact_quote_bytes=%d", len(raw), pages, quoted.Len())
}

func TestCorrectionReadRejectsForeignStaleAndAmbiguousAuthority(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	cause := (&sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: []string{"exact.dat"}}).runnerCorrection()
	run := &sessionRunnerChatRun{SessionID: fixture.stream.SessionID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	run.restoreCorrection(&recoveredRunnerCorrection{ReasonCode: cause.ReasonCode, Detail: cause.Detail, Condition: cause.Condition})
	ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)
	input := map[string]any{"recovery_condition_id": cause.Condition.ContentID()}
	for _, key := range []string{"file_path", "version_id", "pages"} {
		conflicting := copyMapAny(input)
		conflicting[key] = "another-source"
		if validateAgentWorkspaceReadFileInput(normalizeAgentWorkspaceReadFileArguments(conflicting)) == nil {
			t.Fatalf("ambiguous source %s admitted", key)
		}
	}
	foreign := *fixture.identity
	foreign.access.UserID = "another-owner"
	if _, err := fixture.server.executeAgentWorkspaceFileTool(ctx, &foreign, "read_file", input); err == nil {
		t.Fatal("foreign owner read condition")
	}
	wrong := copyMapAny(input)
	wrong["recovery_condition_id"] = strings.Repeat("a", 64)
	if _, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", wrong); err == nil {
		t.Fatal("unknown condition read")
	}
	if _, _, _, err := fixture.repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, ClientMessageID: "condition-new-input", FrameEventID: "condition-new-input-event", MessageUUID: "condition-new-input-message", Text: "A new task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", input); !errors.Is(err, transcriptstore.ErrClaimStale) {
		t.Fatalf("new input inherited prior condition: %v", err)
	}
	if _, err := fixture.repo.CancelRunner(context.Background(), transcriptstore.CancelRunnerInput{StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, ExpectedAttempt: fixture.claim.Attempt, ClientMessageID: "cancel-condition-reader", ReasonCode: "user_cancelled"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", input); !errors.Is(err, transcriptstore.ErrClaimStale) {
		t.Fatalf("stale reader claim: %v", err)
	}
}

func TestCorrectionBranchForkRetiresConditionAndStaleRead(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	cause := (&sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: []string{"original.dat"}}).runnerCorrection()
	payload, err := transcriptstore.RunnerInterruptionCausePayload(cause)
	if err != nil {
		t.Fatal(err)
	}
	appendRunnerToolCheckpoint(t, fixture.repo, fixture.claim, "condition-before-fork", map[string]any{"status": "interrupted", "reason_code": cause.ReasonCode, "resume_detail": cause.Detail, transcriptstore.RunnerInterruptionCauseField: payload})
	run := &sessionRunnerChatRun{SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	run.restoreCorrection(&recoveredRunnerCorrection{ReasonCode: cause.ReasonCode, Detail: cause.Detail, Condition: cause.Condition})
	branch, err := fixture.repo.GetBranchState(context.Background(), fixture.stream.UID, fixture.stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repo.ForkFrameUserMessageBranch(context.Background(), transcriptstore.ForkFrameUserMessageBranchInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, SourceBranchID: branch.ActiveBranchID, ExpectedActiveBranchID: branch.ActiveBranchID, ExpectedGeneration: branch.Generation,
		ClientMutationID: "condition-fork", SourceClientMessageID: "save-user", SourceMessageIndex: 0, ReplacementText: "A corrected independent request", Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)
	if _, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", map[string]any{"recovery_condition_id": cause.Condition.ContentID()}); err == nil {
		t.Fatal("old branch condition remained readable")
	}
	entries, err := fixture.server.loadTranscriptRunnerReplay(context.Background(), run.Transcript, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := latestRunnerCorrection(entries); found {
		t.Fatal("new branch inherited old correction")
	}
}

func TestCorrectionReadIsNotARepairActionOrArtifactInspection(t *testing.T) {
	cause := (sessionRunnerCompletionReviewCorrection{Summary: "repair required", Issues: []sessionRunnerReviewIssue{{Claim: "repair the result", Severity: "major"}}}).runnerCorrection()
	run := &sessionRunnerChatRun{}
	run.restoreCorrection(&recoveredRunnerCorrection{ReasonCode: cause.ReasonCode, Detail: cause.Detail, Condition: cause.Condition})
	arguments, _ := json.Marshal(map[string]any{"recovery_condition_id": cause.Condition.ContentID()})
	messages := []agentruntime.Message{
		{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "diagnostic-read", Name: "read_file", Arguments: arguments}}},
		{Role: "tool", ToolCallID: "diagnostic-read", Content: `{"ok":true,"idempotent":true,"evidence_kind":"runtime_condition","content":"a bounded diagnostic page"}`},
	}
	tools := []agentruntime.ToolSchema{{Name: "read_file", Capabilities: []string{"artifact-read"}}}
	if !sessionRunnerCorrectionStillRequiresAction(run, messages) || sessionRunnerCorrectionReadyForRevalidation(run, messages) {
		t.Error("reading a diagnostic discharged the repair condition")
	}
	if runnerSuccessfulToolNamesSinceCorrection(messages)["read_file"] {
		t.Error("diagnostic read was promoted to completed repair capability")
	}
	if runnerCorrectionCapabilityAttemptedSinceBoundary(messages, tools, "artifact-read") {
		t.Error("diagnostic read replaced actual artifact inspection")
	}
	// Classification follows the typed call, never arbitrary result body labels.
	messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"file_path":"actual-result.csv"}`)
	if sessionRunnerCorrectionStillRequiresAction(run, messages) || !runnerCorrectionCapabilityAttemptedSinceBoundary(messages, tools, "artifact-read") {
		t.Error("ordinary artifact inspection behavior changed")
	}
}
