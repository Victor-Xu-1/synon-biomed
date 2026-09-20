package server

import (
	"context"
	"encoding/json"
	"reflect"
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

func TestSessionRunnerExplicitToolContractRequiresPostWriteReadOfNamedFile(t *testing.T) {
	task := "创建 molecules.smi；最终保存成功后必须重新读取 molecules.smi，之后才能宣布完成。"
	messages := []agentruntime.Message{
		toolRoundWithInput("edit-invalid", "edit_file", `{"file_path":"molecules.smi"}`), toolResult("edit-invalid"),
		toolRoundWithInput("read-early", "read_file", `{"file_path":"molecules.smi"}`), toolResult("read-early"),
		toolRoundWithInput("edit-fixed", "edit_file", `{"file_path":"molecules.smi"}`), toolResult("edit-fixed"),
		toolRoundWithInput("save-fixed", "save_artifacts", `{"files":["molecules.smi"]}`), toolResult("save-fixed"),
	}
	gaps := sessionRunnerExplicitToolContractGaps(task, messages)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "molecules.smi") || !strings.Contains(gaps[0], "read_file") {
		t.Fatalf("post-write read gap=%#v", gaps)
	}
	messages = append(messages,
		toolRoundWithInput("read-other", "read_file", `{"file_path":"other.smi"}`), toolResult("read-other"),
	)
	if gaps := sessionRunnerExplicitToolContractGaps(task, messages); len(gaps) != 1 {
		t.Fatalf("unrelated read discharged named requirement: %#v", gaps)
	}
	messages = append(messages,
		toolRoundWithInput("read-final", "read_file", `{"file_path":"molecules.smi"}`), toolResult("read-final"),
	)
	if gaps := sessionRunnerExplicitToolContractGaps(task, messages); len(gaps) != 0 {
		t.Fatalf("post-write read receipt did not satisfy contract: %#v", gaps)
	}
}

func TestSessionRunnerExplicitToolContractRecognizesEnglishReadBackWithoutInferringOrdinaryReads(t *testing.T) {
	for _, task := range []string{
		"Create result.csv, save it, then read result.csv back before completion.",
		"Generate result.csv and re-read result.csv after saving it.",
	} {
		contract := buildSessionRunnerExplicitToolContract(task)
		if !reflect.DeepEqual(contract.PostWriteReads, []string{"result.csv"}) {
			t.Fatalf("task %q post-write reads=%#v", task, contract.PostWriteReads)
		}
	}
	for _, task := range []string{
		"Read input.csv and summarize it.",
		"不要重新读取 result.csv；只需说明已知限制。",
	} {
		if targets := sessionRunnerExplicitPostWriteReadTargets(task); len(targets) != 0 {
			t.Fatalf("task %q invented post-write read targets=%#v", task, targets)
		}
	}
	if target := sessionRunnerExplicitFileTarget("result.csv.backup"); target != "" {
		t.Fatalf("unsupported extension suffix became a target: %q", target)
	}
}

func TestSessionRunnerExplicitToolContractRequiresFirstFailureCodeInFinalAnswer(t *testing.T) {
	tasks := []string{
		"最终只需简要报告首次失败代码、修复动作和最终产物。",
		"In the final answer, report the first failure code and the repair.",
	}
	messages := []agentruntime.Message{{
		Role: "tool", ToolCallID: "save-invalid",
		Content: `{"ok":false,"code":"artifact_save_requires_correction","errors":[{"code":"invalid_scientific_artifact","validation_code":"invalid_smiles_records"}]}`,
	}}
	for _, task := range tasks {
		contract := buildSessionRunnerExplicitToolContract(task)
		if !contract.ReportFirstFailureCode {
			t.Fatalf("task %q did not require the first failure code", task)
		}
		gaps := contract.finalGaps(messages, "The file was repaired and saved.")
		if len(gaps) != 1 || !strings.Contains(gaps[0], "invalid_smiles_records") {
			t.Fatalf("task %q final gaps=%#v", task, gaps)
		}
		if gaps := contract.finalGaps(messages, "首次失败代码：invalid_smiles_records；文件已修复并保存。"); len(gaps) != 0 {
			t.Fatalf("task %q rejected reported code: %#v", task, gaps)
		}
	}
	for _, task := range []string{
		"Summarize the repair without internal details.",
		"最终不要报告首次失败代码，只说明产物。",
	} {
		if buildSessionRunnerExplicitToolContract(task).ReportFirstFailureCode {
			t.Fatalf("task %q invented a failure-code requirement", task)
		}
	}
}

func TestSessionRunnerExplicitToolContractDoesNotInventMissingFailureCode(t *testing.T) {
	contract := buildSessionRunnerExplicitToolContract("最终报告首次失败代码。")
	gaps := contract.finalGaps(nil, "任务完成。")
	if len(gaps) != 1 || !strings.Contains(gaps[0], "no failed tool receipt") {
		t.Fatalf("missing failure receipt gaps=%#v", gaps)
	}
}

func TestSessionRunnerFailureCodeReportUsesLatestNoToolCandidate(t *testing.T) {
	messages := []agentruntime.Message{
		{
			Role: "assistant", Content: "首次失败代码 invalid_smiles_records，准备修复。",
			ToolCalls: []agentruntime.ToolCall{{ID: "repair", Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"molecules.smi"}`)}},
		},
		{Role: "tool", ToolCallID: "repair", Content: `{"ok":true}`},
		{Role: "assistant", Content: "文件已修复并保存。"},
	}
	candidate := sessionRunnerLatestFinalCandidateContent(messages, "invalid_smiles_records leaked through cumulative narration")
	if candidate != "文件已修复并保存。" {
		t.Fatalf("latest final candidate=%q", candidate)
	}
	contract := buildSessionRunnerExplicitToolContract("最终报告首次失败代码。")
	failed := []agentruntime.Message{{
		Role: "tool", ToolCallID: "save-invalid",
		Content: `{"ok":false,"errors":[{"validation_code":"invalid_smiles_records"}]}`,
	}}
	if gaps := contract.finalGaps(failed, candidate); len(gaps) != 1 {
		t.Fatalf("progress narration satisfied final-answer contract: %#v", gaps)
	}
}

func TestSessionRunnerSMILESRepairCompletionRequiresEveryExplicitReceipt(t *testing.T) {
	task := "创建 molecules.smi，保存失败后修复并再次保存。只有最终保存成功且能重新读取 molecules.smi 后才能宣布完成；最终报告首次失败代码、修复动作和最终产物。"
	messages := []agentruntime.Message{
		toolRoundWithInput("edit-invalid", "edit_file", `{"file_path":"molecules.smi"}`), toolResult("edit-invalid"),
		toolRoundWithInput("save-invalid", "save_artifacts", `{"files":["molecules.smi"]}`),
		{Role: "tool", ToolCallID: "save-invalid", Content: `{"ok":false,"code":"artifact_save_requires_correction","errors":[{"validation_code":"invalid_smiles_records"}]}`},
		toolRoundWithInput("edit-fixed", "edit_file", `{"file_path":"molecules.smi"}`), toolResult("edit-fixed"),
		toolRoundWithInput("save-fixed", "save_artifacts", `{"files":["molecules.smi"]}`), toolResult("save-fixed"),
	}
	contract := buildSessionRunnerExplicitToolContract(task)
	if gaps := contract.gaps(messages); len(gaps) != 1 || !strings.Contains(gaps[0], "read_file") {
		t.Fatalf("missing post-save read gaps=%#v", gaps)
	}
	if gaps := contract.finalGaps(messages, "文件已修复并保存。"); len(gaps) != 1 || !strings.Contains(gaps[0], "invalid_smiles_records") {
		t.Fatalf("missing failure-code gaps=%#v", gaps)
	}
	messages = append(messages,
		toolRoundWithInput("read-final", "read_file", `{"file_path":"molecules.smi"}`), toolResult("read-final"),
	)
	if gaps := contract.gaps(messages); len(gaps) != 0 {
		t.Fatalf("complete receipt sequence rejected: %#v", gaps)
	}
	if gaps := contract.finalGaps(messages, "首次失败代码 invalid_smiles_records；文件已修复并保存。"); len(gaps) != 0 {
		t.Fatalf("complete final report rejected: %#v", gaps)
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

func TestSessionRunnerDurableExplicitToolContractPreservesPostWriteReadOrder(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	appendReceipt := func(id, name string, input map[string]any) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"lifecyclePhase": "tool", "toolName": name, "toolPhase": "completed",
			"toolCallId": id, "toolInput": input, "toolResult": map[string]any{"ok": true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: "explicit-file-receipt-" + id,
			Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendReceipt("edit", "edit_file", map[string]any{"file_path": "molecules.smi"})
	appendReceipt("read-early", "read_file", map[string]any{"file_path": "molecules.smi"})
	appendReceipt("save", "save_artifacts", map[string]any{"files": []any{"molecules.smi"}})
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	messages, err := fixture.server.sessionRunnerDurableExplicitToolContractMessages(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	task := "保存 molecules.smi 后重新读取 molecules.smi，再宣布完成。"
	if gaps := sessionRunnerExplicitToolContractGaps(task, messages); len(gaps) != 1 {
		t.Fatalf("early durable read satisfied post-save obligation: %#v", gaps)
	}
	appendReceipt("read-final", "read_file", map[string]any{"file_path": "molecules.smi"})
	messages, err = fixture.server.sessionRunnerDurableExplicitToolContractMessages(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if gaps := sessionRunnerExplicitToolContractGaps(task, messages); len(gaps) != 0 {
		t.Fatalf("durable post-save read did not satisfy obligation: %#v", gaps)
	}
}

func TestSessionRunnerDurableExplicitToolContractPreservesFirstFailedReceipt(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	appendReceipt := func(id, phase string, result map[string]any) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"lifecyclePhase": "tool", "toolName": "save_artifacts", "toolPhase": phase,
			"toolCallId": id, "toolInput": map[string]any{"files": []any{"molecules.smi"}},
			"toolResult": result,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: "explicit-failure-receipt-" + id,
			Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendReceipt("save-invalid", "failed", map[string]any{
		"ok": false, "code": "artifact_save_requires_correction",
		"errors": []any{map[string]any{
			"code": "invalid_scientific_artifact", "validation_code": "invalid_smiles_records",
		}},
	})
	appendReceipt("save-fixed", "completed", map[string]any{"ok": true})
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	messages, err := fixture.server.sessionRunnerDurableExplicitToolContractMessages(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	contract := buildSessionRunnerExplicitToolContract("最终报告首次失败代码。")
	if gaps := contract.finalGaps(messages, "文件已修复并保存。"); len(gaps) != 1 || !strings.Contains(gaps[0], "invalid_smiles_records") {
		t.Fatalf("durable failed receipt was not enforced: %#v", gaps)
	}
	if gaps := contract.finalGaps(messages, "首次失败代码 invalid_smiles_records；文件已修复并保存。"); len(gaps) != 0 {
		t.Fatalf("reported durable failure code was rejected: %#v", gaps)
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

func toolRoundWithInput(id, name, input string) agentruntime.Message {
	return agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: id, Name: name, Arguments: json.RawMessage(input),
	}}}
}
