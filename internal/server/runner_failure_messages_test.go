package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestPublicSessionRunnerFailureMessageQuotaDoesNotExposeProviderPayload(t *testing.T) {
	raw := `provider endpoint returned 429: {"error":{"code":"429","message":"quota exhausted","type":"limitation"}}`
	got := publicSessionRunnerFailureMessage(raw, "zh")
	if strings.Contains(got, "{") || strings.Contains(got, "quota exhausted") || strings.Contains(got, "429") {
		t.Fatalf("public failure message leaked provider payload: %q", got)
	}
	if !strings.Contains(got, "额度") || !strings.Contains(got, "失败记录") {
		t.Fatalf("public failure message lost actionable Chinese guidance: %q", got)
	}
}

func TestSanitizeTranscriptWebTerminalMessagesCleansCachedFailure(t *testing.T) {
	messages := []map[string]any{{
		"terminal_status": "failed",
		"content": map[string]any{
			"content": "automatic recovery budget exhausted: resume dispatch claim changed before runner execution",
		},
	}}
	(&Server{}).sanitizeTranscriptWebTerminalMessages(context.Background(), transcriptstore.Stream{}, messages)
	content := messages[0]["content"].(map[string]any)
	if detail := webString(content["content"]); strings.Contains(detail, "resume dispatch") ||
		strings.Contains(detail, "automatic recovery") {
		t.Fatalf("cached terminal failure leaked internal detail: %q", detail)
	}
}

func TestPresentationFailureReasonSurvivesHistorySanitization(t *testing.T) {
	for _, reason := range []string{sessionRunnerResponseLanguageMismatchReasonCode} {
		messages := []map[string]any{{"terminal_status": "failed", "terminal_reason_code": reason, "content": map[string]any{"content": "回复原始诊断 /private/path?token=secret"}}}
		server := &Server{}
		server.sanitizeTranscriptWebTerminalMessages(context.Background(), transcriptstore.Stream{}, messages)
		first := webString(mapValue(messages[0]["content"])["content"])
		server.sanitizeTranscriptWebTerminalMessages(context.Background(), transcriptstore.Stream{}, messages)
		second := webString(mapValue(messages[0]["content"])["content"])
		if first != second || !strings.Contains(first, "呈现") || strings.Contains(first, "secret") {
			t.Fatalf("presentation reason lost/leaked: %q -> %q", first, second)
		}
	}
}

func TestSanitizeTranscriptWebPublicMessagesCleansHistoricalInternalNarration(t *testing.T) {
	messages := []map[string]any{
		{
			"type": "text", "content": map[string]any{"content": "公开证据已核对。\n方向：本任务遵循工具协议修正，执行单个 `web_fetch` 工具调用，以符合有界执行单元要求。"},
		},
		{
			"type": "text", "content": map[string]any{"content": "结论依据包括药代和固态稳定性数据。"},
		},
	}
	(&Server{}).sanitizeTranscriptWebPublicMessages(messages)
	first := webString(messages[0]["content"].(map[string]any)["content"])
	if strings.Contains(first, "工具协议") || strings.Contains(first, "web_fetch") || strings.Contains(first, "有界执行单元") ||
		!strings.Contains(first, "公开证据已核对") {
		t.Fatalf("historical internal narration was not filtered: %q", first)
	}
	second := webString(messages[1]["content"].(map[string]any)["content"])
	if second != "结论依据包括药代和固态稳定性数据。" {
		t.Fatalf("ordinary scientific text changed: %q", second)
	}
}

func TestHistoricalPublicTextPreservesOnlyAuthorizedFailureCodes(t *testing.T) {
	raw := "完成结果如下：\n" +
		"1. 首次失败代码 `artifact_save_requires_correction`，具体验证码 `invalid_smiles_records`。\n" +
		"2. 内部状态 `runner_private_checkpoint` 不得公开。\n" +
		"3. 修复后文件已保存并重新读取。"
	got := sessionRunnerSanitizeHistoricalPublicTextWithCodes(raw, map[string]struct{}{
		"artifact_save_requires_correction": {},
		"invalid_smiles_records":            {},
	})
	if !strings.Contains(got, "artifact_save_requires_correction") ||
		!strings.Contains(got, "invalid_smiles_records") ||
		!strings.Contains(got, "修复后文件已保存") {
		t.Fatalf("authorized failure report was removed: %q", got)
	}
	if strings.Contains(got, "runner_private_checkpoint") || strings.Contains(got, "内部状态") {
		t.Fatalf("untrusted runtime identifier escaped: %q", got)
	}
	strict := sessionRunnerSanitizeHistoricalPublicText(raw)
	if strings.Contains(strict, "artifact_save_requires_correction") ||
		strings.Contains(strict, "runner_private_checkpoint") {
		t.Fatalf("unscoped sanitizer preserved internal identifiers: %q", strict)
	}
}

func TestTranscriptPublicMessagesPreserveExplicitDurableFailureCode(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "failure-owner", "failure-project", "failure-frame")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:failure-frame", OwnerID: "failure-owner", ExternalID: "failure-frame",
		SessionID: "failure-frame", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "failure-project", RootFrameID: "failure-frame", FrameID: "failure-frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ClientMessageID: "failure-task", FrameEventID: "failure-task-event",
		MessageUUID: "failure-task-message", MessageOrigin: "task_intent",
		Text: "最终报告首次失败代码、修复动作和最终产物。",
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	intent, found, err := repo.EnsureActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("active intent=%#v found=%t err=%v", intent, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "failure-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	payload, err := json.Marshal(map[string]any{
		"lifecyclePhase": "tool", "toolName": "save_artifacts", "toolPhase": "failed",
		"toolCallId": "save-invalid", "toolInput": map[string]any{"files": []any{"molecules.smi"}},
		"toolResult": map[string]any{
			"ok": false, "code": "artifact_save_requires_correction",
			"errors": []any{map[string]any{"validation_code": "invalid_smiles_records"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "failure-receipt",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
	}); err != nil {
		t.Fatal(err)
	}
	messages := []map[string]any{{
		"type": "text", "content": map[string]any{"content": "1. 首次失败代码 `artifact_save_requires_correction`，具体验证码 `invalid_smiles_records`。\n" +
			"2. `runner_private_checkpoint` 不得公开。\n3. 修复后文件已保存。"},
	}}
	server := &Server{transcriptStore: repo}
	server.sanitizeTranscriptWebPublicMessagesForTask(context.Background(), stream, messages)
	got := webString(mapValue(messages[0]["content"])["content"])
	if !strings.Contains(got, "artifact_save_requires_correction") ||
		!strings.Contains(got, "invalid_smiles_records") || !strings.Contains(got, "修复后文件已保存") {
		t.Fatalf("explicit durable failure codes were removed: %q", got)
	}
	if strings.Contains(got, "runner_private_checkpoint") {
		t.Fatalf("unbound runtime identifier escaped: %q", got)
	}
}

func TestPublicSessionRunnerFailureMessageQuotaUsesEnglishForEnglishTask(t *testing.T) {
	raw := `provider endpoint returned 429: {"error":{"message":"rate limit reached"}}`
	got := publicSessionRunnerFailureMessage(raw, "en")
	if strings.Contains(got, "{") || strings.Contains(strings.ToLower(got), "rate limit reached") {
		t.Fatalf("public failure message leaked provider payload: %q", got)
	}
	if !strings.Contains(got, "rate limit") || !strings.Contains(got, "failure record") {
		t.Fatalf("public failure message lost actionable English guidance: %q", got)
	}
}

func TestPublicSessionRunnerFailureMessageMapsProviderFailureKinds(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "authentication", raw: `provider endpoint returned 401: {"error":"invalid api key"}`, want: "认证"},
		{name: "model", raw: `provider endpoint returned 404: {"error":"model not found"}`, want: "模型"},
		{name: "timeout", raw: `provider endpoint returned 504: {"error":"deadline exceeded"}`, want: "超时"},
		{name: "connection", raw: `provider endpoint returned 503: {"error":"connection refused"}`, want: "连接"},
		{name: "generic", raw: `provider endpoint returned 500: {"error":"upstream failed"}`, want: "不可用"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := publicSessionRunnerFailureMessage(tc.raw, "zh")
			if strings.Contains(got, "{") {
				t.Fatalf("public failure message leaked provider payload: %q", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("public failure message %q does not contain %q", got, tc.want)
			}
		})
	}
}

func TestPublicSessionRunnerFailureMessageDoesNotExposeInternalExecutionFailure(t *testing.T) {
	raw := "tool execution failed: missing required input"
	got := publicSessionRunnerFailureMessage(raw, "zh")
	if strings.Contains(got, "tool") || strings.Contains(got, "missing required input") {
		t.Fatalf("internal execution failure leaked: %q", got)
	}
	if !strings.Contains(got, "已有结果") || !strings.Contains(got, "恢复") {
		t.Fatalf("public recovery guidance=%q", got)
	}
}

func TestPublicSessionRunnerFailureMessageSanitizesNetworkEndpointAndPreservesTask(t *testing.T) {
	raw := `Post "https://secret-model.example.test/v1/chat/completions": dial tcp: connection refused`
	got := publicSessionRunnerFailureMessage(raw, "zh")
	if strings.Contains(got, "secret-model.example.test") || strings.Contains(got, "dial tcp") {
		t.Fatalf("public failure message leaked provider endpoint: %q", got)
	}
	if !strings.Contains(got, "原任务") || !strings.Contains(got, "检查点") {
		t.Fatalf("public failure message lost same-task recovery guidance: %q", got)
	}
}
