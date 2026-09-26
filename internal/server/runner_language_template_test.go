package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestResponseLanguageTemplateProtectsExactLiterals(t *testing.T) {
	original := "Saved CRBN in sample.cif at https://example.org/a?q=2. Recorded 2.40 nm and `print(1)` with {{artifact:abc-123}}."
	template := newResponseLanguageTemplate(original)
	if strings.Contains(template.text, "sample.cif") || strings.Contains(template.text, "print(1)") || len(template.literals) < 5 {
		t.Fatalf("unprotected template: %q", template.text)
	}
	got, ok := template.restore(template.text)
	if !ok || got != original {
		t.Fatalf("lossy literal transport: %q ok=%t", got, ok)
	}
	for _, text := range []string{
		strings.Replace(template.text, template.marker(0), "", 1),
		template.text + template.marker(0),
		template.text + template.marker(999),
	} {
		if _, ok := template.restore(text); ok {
			t.Fatal("omitted/duplicated/invented slot accepted")
		}
	}
}

func TestResponseLanguageCannotInventOrDuplicateEvidence(t *testing.T) {
	for _, translated := range []string{"已保存 2 条记录，另有 9 条。", "已保存 2 条记录，另有 2 条。", "已保存 2 条记录，文件是 invented.cif。"} {
		if responseLanguageLiteralsPreserved("Saved 2 records.", translated) {
			t.Fatalf("invented evidence accepted: %q", translated)
		}
	}
}

type finalPresentationRepairModel struct{ requests int }

func (model *finalPresentationRepairModel) Complete(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	model.requests++
	content := "Changed 9 records in wrong.cif."
	if model.requests == 1 {
		content = "Saved 2 records in sample.cif."
	} else if model.requests == 2 {
		content = "已保存 9 条记录，文件为 wrong.cif。"
	} else {
		if len(request.Tools) != 0 || request.ToolChoice != nil {
			panic("presentation repair acquired tools")
		}
		var masked string
		text := request.Messages[len(request.Messages)-1].Content
		if json.Unmarshal([]byte(text[strings.IndexByte(text, '\n')+1:]), &masked) != nil {
			panic("missing template data")
		}
		content = strings.NewReplacer("Saved ", "已保存 ", " records in ", " 条记录，文件为 ").Replace(masked)
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: content}, Usage: agentruntime.ModelUsage{TotalTokens: 7}}, nil
}

func TestResponseLanguageFinalRepairDoesNotRestartScientificExecution(t *testing.T) {
	model := &finalPresentationRepairModel{}
	client := &sessionRunnerResponseLanguageModelClient{delegate: model, language: "zh"}
	engine := agentruntime.Engine{Model: client, Tools: agentruntime.FuncToolGateway(func(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		t.Fatal("presentation repair dispatched a tool")
		return agentruntime.ToolResult{}, nil
	})}
	result, err := engine.Run(context.Background(), agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "汇总已有结果"}}})
	if err != nil || model.requests != 3 || result.FinalMessage.Content != "已保存 2 条记录，文件为 sample.cif." {
		t.Fatalf("requests=%d final=%q err=%v", model.requests, result.FinalMessage.Content, err)
	}
	model.requests = 0
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{})
	if err != nil || response.Usage.TotalTokens != 21 {
		t.Fatalf("discarded translation cost lost: %#v err=%v", response.Usage, err)
	}
}
