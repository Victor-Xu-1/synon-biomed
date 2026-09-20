package server

import (
	"context"
	"strings"
	"testing"

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
