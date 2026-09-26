package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
)

func (client *sessionRunnerResponseLanguageModelClient) translateBoundPresentation(ctx context.Context, request agentruntime.ModelRequest, original agentruntime.ModelResponse, template responseLanguageTemplate) (agentruntime.ModelResponse, error) {
	encoded, err := json.Marshal(template.text)
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	request.Messages = []agentruntime.Message{
		{Role: "system", Content: simplifiedChineseTranslationInstruction + " 所有 ⟪literal-…⟫ 都是宿主管理的不可变数据槽；每个标记必须逐字保留一次，不得展开、遗漏或复制。"},
		{Role: "user", Content: "请仅翻译下面 JSON 字符串中的叙述，保留数据槽标记：\n" + string(encoded)},
	}
	translated, err := client.delegate.Complete(ctx, request)
	if ctx.Err() != nil {
		return agentruntime.ModelResponse{}, ctx.Err()
	}
	if err != nil {
		return agentruntime.ModelResponse{}, fmt.Errorf("bound response presentation conversion: %w", err)
	}
	content, intact := template.restore(translated.Message.Content)
	han, _, _ := sessionRunnerLanguageProfile(content)
	code := ""
	switch {
	case len(translated.Message.ToolCalls) > 0:
		code = "unexpected_tool_calls"
	case strings.TrimSpace(translated.Message.Content) == "":
		code = "empty_response"
	case sessionRunnerClearlyEnglishProgress(translated.Message.Content) && !intact:
		code = "output_language"
	case !intact || !responseLanguageLiteralsPreserved(original.Message.Content, content):
		code = "protected_literals_changed"
	case han == 0 || sessionRunnerClearlyEnglishProgress(content):
		code = "output_language"
	}
	if code != "" {
		client.auditConversion(map[string]any{"decision": "language_bound_conversion_failed", "failure_code": code, "literal_count": len(template.literals)}, original.Message.Content, content)
		return agentruntime.ModelResponse{}, sessionRunnerResponseLanguageMismatch{ValidationCode: code}
	}
	client.auditConversion(map[string]any{"decision": "language_bound_conversion_repaired", "literal_count": len(template.literals)}, original.Message.Content, content)
	original.Message.Content = content
	original.Usage = addModelUsage(original.Usage, translated.Usage)
	return original, nil
}

func (client *sessionRunnerResponseLanguageModelClient) auditConversion(record map[string]any, input, output string) {
	if client.audit == nil {
		return
	}
	addSessionRunnerNarrationCounters(record, "input_", input)
	addSessionRunnerNarrationCounters(record, "output_", output)
	client.audit(record)
}
