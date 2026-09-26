package server

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolgateway"
	"testing"
	"time"
)

func revisionPlanInput(steps ...string) map[string]any {
	items := make([]any, 0, len(steps))
	for _, title := range steps {
		items = append(items, map[string]any{"title": title, "description": "Investigate " + title})
	}
	return map[string]any{
		"task_summary":    "Investigate an evolving question",
		"phases":          []any{map[string]any{"name": "Research", "delegations": []any{map[string]any{"name": "Investigation", "steps": items}}}},
		"desired_outputs": []any{"report"},
		"feasibility":     map[string]any{"confidence": "high", "rationale": "Sources are accessible"},
	}
}

func revisePlanForTest(t *testing.T, f *agentSaveArtifactsFixture, call string, input map[string]any) map[string]any {
	t.Helper()
	ctx, run := appendLargeToolResultSource(t, f, call, generatePlanToolName)
	run.AutonomousPlanning = true
	result, err := f.server.executeAgentGeneratePlan(ctx, f.stream.SessionID, call, input)
	if err != nil {
		t.Fatalf("autonomous plan %s failed: %v", call, err)
	}
	return mapValue(result)
}

func TestAutonomousPlanRevisionPreservesProgressAndImmutableVersions(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	first := revisePlanForTest(t, f, "plan-initial", revisionPlanInput("Initial finding", "Comparison"))
	initialID := stringValue(mapValue(anySliceValue(first["steps"])[0])["id"])
	if _, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "progress", map[string]any{
		"step": initialID, "status": "completed", "notes": "Source version abc, section 2; comparison remains open.",
	}); err != nil {
		t.Fatal(err)
	}
	second := revisePlanForTest(t, f, "plan-follow-up", revisionPlanInput("New lead", "Initial finding", "Comparison"))
	if second["artifact_id"] != first["artifact_id"] || second["version_id"] == first["version_id"] {
		t.Fatal("revision must use a new immutable version of the same plan")
	}
	steps := anySliceValue(second["steps"])
	retained := mapValue(steps[1])
	if retained["id"] != initialID || retained["status"] != "completed" || !strings.Contains(stringValue(retained["notes"]), "section 2") || mapValue(steps[0])["status"] != "pending" {
		t.Fatalf("new work inherited old completion or retained research was lost: %#v", steps)
	}
	_, _, reader, found, err := f.store.OpenArtifactVersionContent(stringValue(first["version_id"]))
	if err != nil || !found {
		t.Fatalf("old plan version unavailable: %v", err)
	}
	old, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || strings.Contains(string(old), "New lead") {
		t.Fatal("old plan was overwritten")
	}
	third := revisePlanForTest(t, f, "plan-identical", revisionPlanInput("New lead", "Initial finding", "Comparison"))
	if third["version_id"] != second["version_id"] || third["idempotent"] != true {
		t.Fatal("identical plan created another version")
	}
	if remaining, err := f.server.incompleteGeneratedPlanCondition(f.stream.SessionID); err != nil || remaining == nil || len(remaining.condition.Steps) != 2 {
		t.Fatalf("autonomous revision lost its remaining work: %#v %v", remaining, err)
	}
}

func TestAutonomousPlanRevisionDoesNotCarryCompletionToChangedWork(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	first := revisePlanForTest(t, f, "plan-first", revisionPlanInput("Finding"))
	oldID := stringValue(mapValue(anySliceValue(first["steps"])[0])["id"])
	if _, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "status", map[string]any{
		"step": oldID, "status": "completed", "notes": "Original study details",
		"observations": []any{"The retrieved cohort separates two response groups."},
		"source_refs":  []any{"source-call-17:results-section"},
		"follow_ups":   []any{"Compare the two response groups in an independent cohort."},
	}); err != nil {
		t.Fatal(err)
	}
	replacement := revisionPlanInput("Finding")
	phase := mapValue(anySliceValue(replacement["phases"])[0])
	del := mapValue(anySliceValue(phase["delegations"])[0])
	mapValue(anySliceValue(del["steps"])[0])["description"] = "Investigate a different comparison"
	second := revisePlanForTest(t, f, "plan-changed", replacement)
	current := mapValue(anySliceValue(second["steps"])[0])
	if current["id"] == oldID || current["status"] != "pending" {
		t.Fatalf("changed work inherited completion: %#v", current)
	}
	retired := anySliceValue(second["retired_research"])
	if len(retired) != 1 {
		t.Fatalf("retired discovery state was omitted from revision receipt: %#v", second)
	}
	archived := mapValue(retired[0])
	if archived["id"] != oldID ||
		!strings.Contains(stringValueSlice(archived["observations"])[0], "two response groups") ||
		stringValueSlice(archived["source_refs"])[0] != "source-call-17:results-section" ||
		!strings.Contains(stringValueSlice(archived["follow_ups"])[0], "independent cohort") {
		t.Fatalf("retired discovery state lost its navigation data: %#v", archived)
	}
	metadata, _, err := f.store.GetFrameRuntimeMetadata(f.stream.SessionID)
	if err != nil || stringValue(mapValue(mapValue(metadata.ContextData["_step_statuses"])[oldID])["notes"]) != "Original study details" {
		t.Fatal("retired research notes were erased")
	}
}

func TestGeneratedPlanControlStateIsDataNotACompletionScore(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	first := revisePlanForTest(t, f, "plan-state", revisionPlanInput("Inspect primary record", "Investigate resulting lead"))
	steps := anySliceValue(first["steps"])
	firstID := stringValue(mapValue(steps[0])["id"])
	result, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "record-discovery", map[string]any{
		"step": firstID, "status": "completed",
		"notes":        "Compared the result and methods sections.",
		"observations": []any{"A subgroup difference changes the next comparison."},
		"source_refs":  []any{"source-call-22:methods", "source-call-22:results"},
		"follow_ups":   []any{"Test whether the subgroup difference persists in another study."},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt := mapValue(result)
	if receipt["status"] != "completed" || receipt["idempotent"] != false {
		t.Fatalf("legacy update receipt changed: %#v", receipt)
	}
	state := mapValue(receipt["research_transition"])
	if state["schema"] != "synon.research_transition.v1" || state["completion_score"] != nil || state["success"] != nil {
		t.Fatalf("research transition became a synthetic quality score: %#v", state)
	}
	next := anySliceValue(state["next_investigations"])
	if len(next) != 1 || !strings.Contains(stringValue(mapValue(next[0])["description"]), "Investigate") {
		t.Fatalf("next investigation did not retain its substantive description: %#v", next)
	}
	open := stringValueSlice(state["open_follow_ups"])
	if len(open) != 1 || !strings.Contains(open[0], "another study") {
		t.Fatalf("discovery did not reach follow-up navigation: %#v", state)
	}
	context := f.server.generatedPlanDesiredOutputsContext(f.stream.SessionID)
	for _, expected := range []string{"synon.research_navigation.v1", "subgroup difference", "another study", "Investigate resulting lead"} {
		if !strings.Contains(context, expected) {
			t.Fatalf("resume state omitted %q: %s", expected, context)
		}
	}
	for _, forbidden := range []string{"continue from", "must investigate", "minimum", "completion_score"} {
		if strings.Contains(strings.ToLower(context), forbidden) {
			t.Fatalf("state projection contains a behavioral gate %q: %s", forbidden, context)
		}
	}
	messages := attachRuntimeResearchNavigationState([]chatCompletionMessage{
		{Role: "system", Content: "runtime contract"},
		{Role: "assistant", ToolCalls: []chatCompletionToolCall{{ID: "source-call-21", Function: chatCompletionToolCallFunction{Name: "web_fetch", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "source-call-21", Content: `{"ok":true,"content":"source evidence"}`},
	}, context)
	if len(messages) != 3 || messages[2].Role != "tool" || !strings.Contains(messages[2].Content, "source evidence") ||
		!strings.Contains(messages[2].Content, generatedPlanResearchNavigationSchema) {
		t.Fatalf("research state was not attached to the existing tool result: %#v", messages)
	}
	handoff := f.server.generatedPlanResearchHandoffContext(f.stream.SessionID, agentruntime.ToolCall{ID: "source-call-22", Name: "web_fetch"}, nil)
	for _, expected := range []string{"synon.research_handoff.v1", "source-call-22", "web_fetch", "another study"} {
		if !strings.Contains(handoff, expected) {
			t.Fatalf("source-to-investigation handoff omitted %q: %s", expected, handoff)
		}
	}
}

func TestResearchNavigationSurvivesCompactBoundaryWithoutToolMessage(t *testing.T) {
	state := `{"schema":"synon.research_navigation.v1","navigation_state_only":true,"current_investigations":[{"id":"module-2","status":"in_progress"}]}`
	messages := []chatCompletionMessage{
		{Role: "system", Content: "Synon compact handoff context"},
		{Role: "user", Content: "continue"},
	}
	attached := attachRuntimeResearchNavigationState(messages, state)
	if len(attached) != 3 || attached[1].Role != "system" || !strings.Contains(attached[1].Content, "module-2") {
		t.Fatalf("compact recovery lost durable plan navigation: %#v", attached)
	}
	again := attachRuntimeResearchNavigationState(attached, state)
	joined := ""
	for _, message := range again {
		joined += message.Content
	}
	if strings.Count(joined, generatedPlanResearchNavigationSchema) != 1 {
		t.Fatalf("plan navigation was duplicated during recovery: %#v", again)
	}
}

func TestResearchNavigationReinjectsLatestStateAfterStaleCompactSummary(t *testing.T) {
	stale := `{"schema":"synon.research_navigation.v1","navigation_state_only":true,"current_investigations":[{"id":"module-old","status":"in_progress"}]}`
	latest := `{"schema":"synon.research_navigation.v1","navigation_state_only":true,"current_investigations":[{"id":"module-latest","status":"in_progress"}]}`
	messages := []chatCompletionMessage{
		{Role: "system", Content: "runtime contract"},
		{Role: "system", Content: "Compact handoff\nDurable plan navigation state:\n" + stale},
		{Role: "user", Content: "continue"},
	}
	attached := attachRuntimeResearchNavigationState(messages, latest)
	if len(attached) != 4 || attached[2].Role != "system" ||
		!strings.Contains(attached[2].Content, "<research_navigation_state>") ||
		!strings.Contains(attached[2].Content, "module-latest") {
		t.Fatalf("latest navigation did not supersede compact state: %#v", attached)
	}

	newest := strings.ReplaceAll(latest, "module-latest", "module-newest")
	replaced := attachRuntimeResearchNavigationState(attached, newest)
	if len(replaced) != len(attached) || !strings.Contains(replaced[2].Content, "module-newest") ||
		strings.Contains(replaced[2].Content, "module-latest") {
		t.Fatalf("stable navigation fragment was duplicated or left stale: %#v", replaced)
	}
}

func TestSourceResultCarriesSeparateResearchNavigationContinuation(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	plan := revisePlanForTest(t, f, "plan-source-handoff", revisionPlanInput("Inspect source", "Follow evidence"))
	firstID := stringValue(mapValue(anySliceValue(plan["steps"])[0])["id"])
	if _, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "start-source-step", map[string]any{
		"step": firstID, "status": "in_progress",
	}); err != nil {
		t.Fatal(err)
	}
	call := agentruntime.ToolCall{ID: "source-call", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test/source"}`)}
	response := map[string]any{"ok": true, "url": "https://example.test/source", "content": "source evidence"}
	execution := &serverAgentRuntimeGatewayExecution{
		gateway: serverAgentRuntimeToolGateway{server: f.server, sessionID: f.stream.SessionID},
		call:    call, response: response,
	}
	invocation := toolgateway.NewInvocation(context.Background(), call.ID, call.Name, call.Arguments, execution)
	invocation.CanonicalName = call.Name
	serverAgentRuntimeGatewayMaterialize(invocation)
	if !execution.resultReady || execution.result.ModelContext == nil {
		t.Fatalf("source handoff was not materialized: %#v", execution.result)
	}
	encoded := string(mustJSON(t, execution.result.Value))
	if strings.Contains(encoded, "research_handoff") || !strings.Contains(encoded, "source evidence") {
		t.Fatalf("research navigation contaminated evidence bytes: %s", encoded)
	}
	modelContext := string(mustJSON(t, execution.result.ModelContext))
	if !strings.Contains(modelContext, "synon.research_handoff.v1") || !strings.Contains(modelContext, "Inspect source") ||
		strings.Contains(modelContext, "Follow evidence") {
		t.Fatalf("source handoff did not carry the current investigation: %s", modelContext)
	}
}

func TestResearchNavigationKeepsActiveWorkThenSelectsOnePendingItem(t *testing.T) {
	items := []any{
		map[string]any{"id": "done", "phase_id": "phase-1", "track_id": "track-a", "status": "completed"},
		map[string]any{"id": "next-a", "phase_id": "phase-1", "track_id": "track-a", "status": "pending"},
		map[string]any{"id": "active-b", "phase_id": "phase-1", "track_id": "track-b", "status": "in_progress"},
		map[string]any{"id": "later", "phase_id": "phase-2", "track_id": "track-c", "status": "pending"},
	}
	actionable := generatedPlanActionableInvestigations(items)
	if len(actionable) != 1 || stringValue(actionable[0]["id"]) != "active-b" {
		t.Fatalf("pending work displaced an active investigation: %#v", actionable)
	}
	mapValue(items[2])["status"] = "completed"
	actionable = generatedPlanActionableInvestigations(items)
	if len(actionable) != 1 || stringValue(actionable[0]["id"]) != "next-a" {
		t.Fatalf("root navigation exposed multiple pending branches: %#v", actionable)
	}
}

func TestAutonomousPlanRevisionCannotModifyReviewedPlan(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	first := revisePlanForTest(t, f, "plan-first", revisionPlanInput("Finding"))
	meta, _, _ := f.store.GetFrameRuntimeMetadata(f.stream.SessionID)
	meta.ContextData["_plan_approved"] = true
	if _, err := f.store.SetFrameRuntimeMetadata(f.stream.SessionID, meta); err != nil {
		t.Fatal(err)
	}
	ctx, run := appendLargeToolResultSource(t, f, "forbidden-revision", generatePlanToolName)
	run.AutonomousPlanning = true
	if _, err := f.server.executeAgentGeneratePlan(ctx, f.stream.SessionID, "forbidden-revision", revisionPlanInput("Changed")); err == nil {
		t.Fatal("reviewed plan changed through autonomous route")
	}
	meta, _, _ = f.store.GetFrameRuntimeMetadata(f.stream.SessionID)
	if meta.ContextData["_plan_version_id"] != first["version_id"] {
		t.Fatal("denied revision mutated plan")
	}
}

func TestAutonomousPlanContentDoesNotTurnRedundantApprovalIntoUserConsent(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	input := revisionPlanInput("Research", "Compare")
	input["approve"] = true
	result := revisePlanForTest(t, f, "autonomous-content", input)
	meta, _, err := f.store.GetFrameRuntimeMetadata(f.stream.SessionID)
	if err != nil || result["status"] != "working_plan_ready" || meta.ContextData["_plan_approved"] != nil || !autonomousGeneratedPlan(meta.ContextData) {
		t.Fatal("redundant provider flag became user approval")
	}
}

func TestReviewedPlanContentStillRequiresRealUserApproval(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	ctx, run := appendLargeToolResultSource(t, f, "reviewed-content", generatePlanToolName)
	run.AutonomousPlanning = false
	input := revisionPlanInput("Research")
	input["approve"] = true
	if _, err := f.server.executeAgentGeneratePlan(ctx, f.stream.SessionID, "reviewed-content", input); err == nil {
		t.Fatal("model-supplied approval bypassed user review")
	}
	meta, _, _ := f.store.GetFrameRuntimeMetadata(f.stream.SessionID)
	if meta.ContextData["_plan_artifact_id"] != nil {
		t.Fatal("denied mixed review call mutated plan")
	}
}

func TestAutonomousPlanRevisionRestartRetainsControlAndNotes(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	first := revisePlanForTest(t, f, "plan-initial", revisionPlanInput("Source details"))
	id := stringValue(mapValue(anySliceValue(first["steps"])[0])["id"])
	if _, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "progress", map[string]any{"step": id, "status": "in_progress", "notes": "Stored source excerpt and comparison"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.Open(f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	repo, err := reopened.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(Options{Workspace: reopened, Transcript: repo, FileRoot: t.TempDir()})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = restarted.Close(ctx)
	}()
	ctx, run := appendLargeToolResultSource(t, f, "plan-after-restart", generatePlanToolName)
	tracking, approved, err := restarted.restoreSessionRunnerPlanControl(sessionstore.Session{ID: f.stream.SessionID}, run)
	if err != nil || !tracking || approved || !run.AutonomousPlanning || !run.planProgressAvailable.Load() {
		t.Fatalf("autonomous work became approval mode on restart: %t %t %t %v", tracking, approved, run.AutonomousPlanning, err)
	}
	if state := restarted.generatedPlanDesiredOutputsContext(f.stream.SessionID); !strings.Contains(state, "Stored source excerpt") || !strings.Contains(state, "in_progress") {
		t.Fatal("resume received a checklist without stored findings")
	}
	if _, err := restarted.executeAgentGeneratePlan(ctx, f.stream.SessionID, "plan-after-restart", revisionPlanInput("Source details", "Follow-up")); err != nil {
		t.Fatal(err)
	}
}

func TestAutonomousPlanRevisionRejectsSupersededSourceAndLease(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	revisePlanForTest(t, f, "plan-initial", revisionPlanInput("Finding"))
	oldCtx, oldRun := appendLargeToolResultSource(t, f, "late-old-call", generatePlanToolName)
	oldRun.AutonomousPlanning = true
	latest := revisePlanForTest(t, f, "newer-call", revisionPlanInput("Finding", "Current follow-up"))
	if _, err := f.server.executeAgentGeneratePlan(oldCtx, f.stream.SessionID, "late-old-call", revisionPlanInput("Stale replacement")); err == nil {
		t.Fatal("late old revision rewound the plan")
	}
	ctx, run := appendLargeToolResultSource(t, f, "expired-call", generatePlanToolName)
	run.AutonomousPlanning = true
	if _, err := f.repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{Claim: f.claim, ClientMessageID: "interrupt-plan", ReasonCode: "test_recovery", ResumeDetail: "preserve working plan", Resumable: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.executeAgentGeneratePlan(ctx, f.stream.SessionID, "expired-call", revisionPlanInput("Finding", "Current follow-up")); err == nil {
		t.Fatal("expired lease was allowed to write through idempotent route")
	}
	currentStep := stringValue(mapValue(anySliceValue(latest["steps"])[0])["id"])
	if _, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "expired-status", map[string]any{"step": currentStep, "status": "completed", "notes": "must not commit"}); err == nil {
		t.Fatal("expired lease was allowed to update plan progress")
	}
	meta, _, err := f.store.GetFrameRuntimeMetadata(f.stream.SessionID)
	if err != nil || meta.ContextData["_plan_version_id"] != latest["version_id"] {
		t.Fatal("rejected operation changed current plan")
	}
}

func TestAutonomousPlanRevisionInvalidatesDependentPhase(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	input := revisionPlanInput("Measure")
	input["phases"] = append(anySliceValue(input["phases"]), map[string]any{"name": "Synthesis", "delegations": []any{map[string]any{"name": "Compare", "steps": []any{map[string]any{"title": "Comparison", "description": "Compare measured findings"}}}}})
	first := revisePlanForTest(t, f, "plan-initial", input)
	prior := stringValue(mapValue(anySliceValue(first["steps"])[1])["id"])
	if _, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "comparison-done", map[string]any{"step": prior, "status": "completed", "notes": "Prior comparison remains archived"}); err != nil {
		t.Fatal(err)
	}
	mapValue(anySliceValue(input["phases"])[0])["name"] = "Changed measurements"
	second := revisePlanForTest(t, f, "plan-updated", input)
	current := mapValue(anySliceValue(second["steps"])[1])
	if current["id"] == prior || current["status"] != "pending" {
		t.Fatalf("dependent comparison inherited old completion: %#v", current)
	}
}

func TestAutonomousPlanRevisionConcurrentProgressDoesNotRewind(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	first := revisePlanForTest(t, f, "plan-initial", revisionPlanInput("Finding"))
	id := stringValue(mapValue(anySliceValue(first["steps"])[0])["id"])
	ctxA, runA := appendLargeToolResultSource(t, f, "revision-a", generatePlanToolName)
	ctxB, runB := appendLargeToolResultSource(t, f, "revision-b", generatePlanToolName)
	runA.AutonomousPlanning, runB.AutonomousPlanning = true, true
	var wg sync.WaitGroup
	errors := make(chan error, 3)
	wg.Add(3)
	go func() {
		defer wg.Done()
		_, err := f.server.executeAgentGeneratePlan(ctxA, f.stream.SessionID, "revision-a", revisionPlanInput("Finding", "Earlier lead"))
		if err != nil && !strings.Contains(err.Error(), "superseded") {
			errors <- err
		}
	}()
	go func() {
		defer wg.Done()
		_, err := f.server.executeAgentGeneratePlan(ctxB, f.stream.SessionID, "revision-b", revisionPlanInput("Finding", "Latest lead"))
		if err != nil {
			errors <- err
		}
	}()
	go func() {
		defer wg.Done()
		_, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "progress", map[string]any{"step": id, "status": "completed", "notes": "Concurrent source note"})
		if err != nil {
			errors <- err
		}
	}()
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	meta, _, err := f.store.GetFrameRuntimeMetadata(f.stream.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContextData["_plan_tool_call_id"] != "revision-b" || stringValue(mapValue(mapValue(meta.ContextData["_step_statuses"])[id])["notes"]) != "Concurrent source note" {
		t.Fatalf("concurrent state lost: %#v", meta.ContextData)
	}
}
