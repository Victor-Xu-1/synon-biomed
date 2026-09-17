package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestSessionRunnerExplicitToolContractRequiresNamedCompletedActions(t *testing.T) {
	task := "使用 search_skills 并实际加载至少两个 Skill；使用 web_search，并通过 MCP 的真实方法查询至少两个不同数据域，至少包含 PubMed；通过 manage_packages 安装依赖。"
	if gaps := sessionRunnerExplicitToolContractGaps(task, nil); len(gaps) != 5 {
		t.Fatalf("missing explicit tool gaps=%#v", gaps)
	}
	messages := []agentruntime.Message{
		toolRound("search", "search_skills"), toolResult("search"),
		toolRound("skill-a", "skill"), toolResult("skill-a"),
		toolRound("skill-b", "skill"), toolResult("skill-b"),
		toolRound("web", "web_search"), toolResult("web"),
		toolRound("packages", "manage_packages"), toolResult("packages"),
		toolRound("pubmed", "mcp__pubmed__search_articles"), toolResult("pubmed"),
		toolRound("trials", "mcp__clinicaltrials__search_studies"), toolResult("trials"),
	}
	if gaps := sessionRunnerExplicitToolContractGaps(task, messages); len(gaps) != 0 {
		t.Fatalf("completed explicit tool contract gaps=%#v", gaps)
	}
}

func TestSessionRunnerExplicitToolContractRequiresSuccessfulResults(t *testing.T) {
	task := "使用 search_skills 查找能力，然后使用 Skill 加载它。"
	messages := []agentruntime.Message{
		toolRound("search", "search_skills"),
		{Role: "tool", ToolCallID: "search", Content: `{"ok":false,"error":"catalog unavailable"}`},
		toolRound("skill", "skill"), toolResult("skill"),
	}
	gaps := sessionRunnerExplicitToolContractGaps(task, messages)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "search_skills") || !strings.Contains(gaps[0], "successful") {
		t.Fatalf("failed tool result satisfied explicit contract: %#v", gaps)
	}
}

func TestSessionRunnerExplicitToolReceiptScopeCompletesFromExactReceipts(t *testing.T) {
	task := "请只执行运行时能力验收：使用 search_skills 查找 structure-based-molecule-generation，然后使用 skill 加载它；不要运行分子生成、不要创建环境、不要下载文件。最后仅报告两个工具是否成功，以及加载到的 Skill 名称。"
	messages := []agentruntime.Message{
		toolRound("search", "search_skills"), toolResult("search"),
		toolRound("skill", "skill"), toolResult("skill"),
	}
	contract := buildSessionRunnerExplicitToolContract(task)
	if !contract.ReceiptOnly {
		t.Fatal("explicit tool-only task did not produce a receipt-only completion contract")
	}
	if !contract.receiptOnlySatisfied(messages) {
		t.Fatal("exact successful receipts did not satisfy the receipt-only contract")
	}
}

func TestSessionRunnerExplicitToolReceiptScopeRejectsUnexpectedWork(t *testing.T) {
	task := "请只执行运行时能力验收：使用 search_skills 查找 structure-based-molecule-generation，然后使用 skill 加载它。最后仅报告两个工具是否成功，以及加载到的 Skill 名称。"
	messages := []agentruntime.Message{
		toolRound("search", "search_skills"), toolResult("search"),
		toolRound("skill", "skill"), toolResult("skill"),
		toolRound("python", "python"), toolResult("python"),
	}
	contract := buildSessionRunnerExplicitToolContract(task)
	if contract.receiptOnlySatisfied(messages) {
		t.Fatal("an unrequested execution was hidden by the receipt-only contract")
	}
}

func TestSessionRunnerScientificDeliverableDoesNotBecomeToolReceiptOnly(t *testing.T) {
	tasks := []string{
		"使用 search_skills 查找分子生成能力并加载 Skill，然后基于口袋生成 30 个候选并完成对接排序。",
		"Only use search_skills and load a Skill before generating and docking 30 molecules; deliver the structures and report.",
	}
	for _, task := range tasks {
		contract := buildSessionRunnerExplicitToolContract(task)
		if contract.ReceiptOnly {
			t.Fatalf("scientific deliverable became receipt-only: %q", task)
		}
	}
}

func TestSessionRunnerExplicitToolContractLeavesDataDomainSemanticsToEvidenceReview(t *testing.T) {
	task := "通过真实 MCP 方法查询至少两个不同公开数据域（由可用性决定，例如 PubChem、PubMed、ChEBI）。"
	messages := []agentruntime.Message{
		toolRound("pubchem", "mcp__chemistry__pubchem_get_compound"), toolResult("pubchem"),
		toolRound("chebi", "mcp__chemistry__chebi_get_entity"), toolResult("chebi"),
	}
	if gaps := sessionRunnerExplicitToolContractGaps(task, messages); len(gaps) != 0 {
		t.Fatalf("structural checker reinterpreted semantic data-domain coverage: %#v", gaps)
	}
}

func TestSessionRunnerDurableExplicitToolContractUsesGovernedReceiptsWithoutPromotingEvidence(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	for index, receipt := range []struct {
		name   string
		input  string
		result string
	}{
		{name: "search_skills", input: `{"query":"formulation"}`, result: `{"matches":["formulation-development"]}`},
		{name: "manage_packages", input: `{"action":"install","packages":["pyDOE3"]}`, result: `{"ok":true}`},
	} {
		payload, err := json.Marshal(map[string]any{
			"lifecyclePhase": "tool", "toolName": receipt.name, "toolPhase": "completed",
			"toolCallId": receipt.name + "-completed", "toolInput": json.RawMessage(receipt.input),
			"toolResult": json.RawMessage(receipt.result),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: "explicit-tool-receipt-" + string(rune('a'+index)),
			Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	messages, err := fixture.server.sessionRunnerDurableExplicitToolContractMessages(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if gaps := sessionRunnerExplicitToolContractGaps(
		"使用 search_skills，并通过 manage_packages 安装依赖。", messages,
	); len(gaps) != 0 {
		t.Fatalf("durable explicit tool receipts were ignored: %#v", gaps)
	}
	evidence, err := fixture.server.sessionRunnerDurableEvidenceMessages(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 0 {
		t.Fatalf("ordinary explicit tool receipts became scientific evidence: %#v", evidence)
	}
}

func TestSessionRunnerDurableExplicitToolContractDoesNotCrossTaskIntent(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	oldPayload, err := json.Marshal(map[string]any{
		"lifecyclePhase": "tool", "toolName": "manage_packages", "toolPhase": "completed",
		"toolCallId": "old-packages", "toolInput": map[string]any{"mode": "install"},
		"toolResult": map[string]any{"ok": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "old-task-packages", Phase: transcriptstore.RunnerPhaseExecuting,
		PayloadJSON: oldPayload,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: fixture.claim, ClientMessageID: "old-task-finished", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := fixture.repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, ClientMessageID: "new-task-user",
		FrameEventID: "new-task-event", MessageUUID: "new-task-message",
		Text: "Use search_skills and manage_packages for a new task.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append new task created=%t err=%v", created, err)
	}
	claimed, err := fixture.repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, RunnerID: "runner-new-task",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim new task=%#v err=%v", claimed, err)
	}
	newPayload, err := json.Marshal(map[string]any{
		"lifecyclePhase": "tool", "toolName": "search_skills", "toolPhase": "completed",
		"toolCallId": "new-search", "toolInput": map[string]any{"query": "new task"},
		"toolResult": map[string]any{"matches": []string{"formulation-development"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "new-task-search", Phase: transcriptstore.RunnerPhaseExecuting,
		PayloadJSON: newPayload,
	}); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: claimed.Claim}}
	messages, err := fixture.server.sessionRunnerDurableExplicitToolContractMessages(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	gaps := sessionRunnerExplicitToolContractGaps("Use search_skills and manage_packages for a new task.", messages)
	if len(gaps) != 1 || gaps[0] != "the explicitly requested manage_packages action has no successful tool result" {
		t.Fatalf("prior task receipt crossed the active task boundary: %#v", gaps)
	}
}

func TestSessionRunnerDurableExplicitToolContractSurvivesManualContinuation(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	payload, err := json.Marshal(map[string]any{
		"lifecyclePhase": "tool", "toolName": "manage_packages", "toolPhase": "completed",
		"toolCallId": "packages-before-continuation",
		"toolInput":  map[string]any{"mode": "install", "packages": []any{"example-doe"}},
		"toolResult": map[string]any{"ok": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "packages-before-continuation",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := fixture.repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
		ClientMessageID: "manual-continuation", FrameEventID: "manual-continuation-event",
		MessageUUID: "manual-continuation-message", MessageOrigin: "input_response",
		Text: "Continue the same task with the existing evidence.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append continuation created=%t err=%v", created, err)
	}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	messages, err := fixture.server.sessionRunnerDurableExplicitToolContractMessages(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if gaps := sessionRunnerExplicitToolContractGaps("Use manage_packages in this task.", messages); len(gaps) != 0 {
		t.Fatalf("manual continuation lost durable tool receipts: %#v", gaps)
	}
}

func toolRound(id, name string) agentruntime.Message {
	return agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: id, Name: name, Arguments: json.RawMessage(`{}`),
	}}}
}

func toolResult(id string) agentruntime.Message {
	return agentruntime.Message{Role: "tool", ToolCallID: id, Content: `{"ok":true}`}
}
