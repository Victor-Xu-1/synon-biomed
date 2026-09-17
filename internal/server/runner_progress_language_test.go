package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestProgressLanguageCoversNativeAndStructuredRequiredToolResponses(t *testing.T) {
	for _, structured := range []bool{false, true} {
		for _, required := range []bool{false, true} {
			t.Run(fmtProgressCase(structured, required), func(t *testing.T) {
				english := "Verifying sample.cif at https://example.org/records for 2 records."
				chinese := "正在核对 sample.cif，来源为 https://example.org/records，共 2 条记录。"
				args := map[string]any{"path": "sample.cif"}
				content := english
				chunks := []string{english}
				if structured {
					args[runnerPublicProgressField] = english
					content = ""
					chunks = nil
				}
				encoded, _ := json.Marshal(args)
				model := &responseLanguageSequenceModel{chunks: [][]string{chunks, {chinese}}, responses: []agentruntime.ModelResponse{
					{Message: agentruntime.Message{Role: "assistant", Content: content, ToolCalls: []agentruntime.ToolCall{{ID: "inspect-1", Name: "inspect", Arguments: encoded}}}},
					{Message: agentruntime.Message{Role: "assistant", Content: chinese}},
				}}
				client := &sessionRunnerResponseLanguageModelClient{delegate: &sessionRunnerResponseContractClient{delegate: model}, language: "zh"}
				request := agentruntime.ModelRequest{Tools: []agentruntime.ToolSchema{{Name: "inspect", Parameters: map[string]any{"type": "object"}}}}
				if required {
					request.ToolChoice = map[string]any{"type": "tool", "name": "inspect"}
				}
				var visible strings.Builder
				response, err := client.CompleteStream(context.Background(), request, func(event agentruntime.ModelStreamEvent) error { visible.WriteString(event.ContentDelta); return nil })
				if err != nil || visible.String() != chinese || response.Message.Content != chinese {
					t.Fatalf("visible=%q response=%q err=%v", visible.String(), response.Message.Content, err)
				}
				if len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].ID != "inspect-1" || string(response.Message.ToolCalls[0].Arguments) != `{"path":"sample.cif"}` {
					t.Fatalf("changed tool batch: %+v", response.Message.ToolCalls)
				}
				if len(model.requests) != 2 || len(model.requests[1].Tools) != 0 || model.requests[1].ToolChoice != nil || !reflect.DeepEqual(model.requests[0].ToolChoice, request.ToolChoice) {
					t.Fatal("translation altered tool authority or repeated the original request")
				}
			})
		}
	}
}

func fmtProgressCase(structured, required bool) string {
	name := "native"
	if structured {
		name = "structured"
	}
	if required {
		name += "/required"
	}
	return name
}

func TestProgressLanguageRetainsLiteralEvidence(t *testing.T) {
	original := "Checking CRBN in 9DUR.cif at https://example.org/a for 2.40 nm using `print(1)` and {{artifact:abc-123}}."
	translated := "正在检查 CRBN，文件为 9DUR.cif，来源 https://example.org/a，数值 2.40 nm，代码 `print(1)`，产物 {{artifact:abc-123}}。"
	if !responseLanguageLiteralsPreserved(original, translated) {
		t.Fatal("valid literal-preserving translation rejected")
	}
	for _, pair := range [][2]string{{"9DUR.cif", "9XYZ.cif"}, {"2.40", "40.2"}, {"https://example.org/a", "https://example.org/b"}, {"print(1)", "print(2)"}, {"abc-123", "abc-456"}, {"CRBN", "OTHER"}} {
		if responseLanguageLiteralsPreserved(original, strings.ReplaceAll(translated, pair[0], pair[1])) {
			t.Fatalf("changed literal accepted: %q", pair[0])
		}
	}
}

func TestProgressLanguageRecognizesShortNarrationButNotEvidenceTokens(t *testing.T) {
	for _, text := range []string{"Saving results", "Checking records", "Reading sample.cif"} {
		if !sessionRunnerClearlyEnglishProgress(text) {
			t.Fatalf("missed English progress: %q", text)
		}
	}
	for _, text := range []string{"CRBN PDB 9DUR.cif https://example.org/a", "正在检查 CRBN 和 sample.cif。", "`print(1)`"} {
		if sessionRunnerClearlyEnglishProgress(text) {
			t.Fatalf("misclassified evidence: %q", text)
		}
	}
}

func TestProgressLanguageTranslationCannotChangeOrExecuteTool(t *testing.T) {
	english := "Inspecting sample.cif before the next step."
	chinese := "正在核对 sample.cif，再决定下一步。"
	for _, invalid := range []agentruntime.Message{
		{Content: "Checking sample.cif"},
		{Content: "正在核对 wrong.cif，再决定下一步。"},
		{Content: chinese, ToolCalls: []agentruntime.ToolCall{{ID: "injected", Name: "inspect"}}},
	} {
		model := &responseLanguageSequenceModel{chunks: [][]string{{english}, nil}, responses: []agentruntime.ModelResponse{
			{Message: agentruntime.Message{Content: english, ToolCalls: []agentruntime.ToolCall{{ID: "original", Name: "inspect", Arguments: json.RawMessage(`{"path":"sample.cif"}`)}}}},
			{Message: invalid},
		}}
		client := &sessionRunnerResponseLanguageModelClient{delegate: model, language: "zh"}
		var visible strings.Builder
		response, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{ToolChoice: "required"}, func(event agentruntime.ModelStreamEvent) error { visible.WriteString(event.ContentDelta); return nil })
		if err != nil || visible.Len() != 0 || len(model.requests) != 2 || len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].ID != "original" || string(response.Message.ToolCalls[0].Arguments) != `{"path":"sample.cif"}` {
			t.Fatalf("visible=%q calls=%d err=%v", visible.String(), len(model.requests), err)
		}
	}
}

func TestProgressLanguageExecutesOriginalRequiredActionExactlyOnce(t *testing.T) {
	english := "Checking sample.cif before comparing the records."
	chinese := "先检查 sample.cif，再比较记录。"
	model := &responseLanguageSequenceModel{chunks: [][]string{nil, {chinese}, {"检查完成。"}}, responses: []agentruntime.ModelResponse{
		{Message: agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "action", Name: "inspect", Arguments: json.RawMessage(`{"path":"sample.cif","public_progress":"` + english + `"}`)}}}},
		{Message: agentruntime.Message{Role: "assistant", Content: chinese}},
		{Message: agentruntime.Message{Role: "assistant", Content: "检查完成。"}},
	}}
	client := &sessionRunnerResponseLanguageModelClient{delegate: &sessionRunnerResponseContractClient{delegate: model}, language: "zh"}
	calls := 0
	engine := agentruntime.Engine{Model: client, Tools: agentruntime.FuncToolGateway(func(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		calls++
		if call.ID != "action" || string(call.Arguments) != `{"path":"sample.cif"}` {
			t.Fatalf("changed action: %+v", call)
		}
		return agentruntime.ToolResult{Value: map[string]any{"ok": true}}, nil
	})}
	result, err := engine.Run(context.Background(), agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "检查记录"}}, Tools: []agentruntime.ToolSchema{{Name: "inspect", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "additionalProperties": false}}}, InitialToolChoice: map[string]any{"type": "tool", "name": "inspect"}, MaxToolRounds: 2})
	if err != nil || calls != 1 || len(model.requests) != 3 || result.FinalMessage.Content != "检查完成。" {
		t.Fatalf("tool calls=%d model calls=%d final=%q err=%v", calls, len(model.requests), result.FinalMessage.Content, err)
	}
}

func TestProgressLanguageRejectsUntranslatedShortReply(t *testing.T) {
	model := &responseLanguageSequenceModel{responses: []agentruntime.ModelResponse{
		{Message: agentruntime.Message{Content: "Saving results"}},
	}}
	client := &sessionRunnerResponseLanguageModelClient{delegate: model, language: "zh"}
	original := agentruntime.ModelResponse{Message: agentruntime.Message{Content: "Saving results", ToolCalls: []agentruntime.ToolCall{{ID: "original", Name: "inspect"}}}}
	response, err := client.localizeOrKeepToolCall(context.Background(), agentruntime.ModelRequest{}, original)
	if err != nil || response.Message.Content != "" || len(response.Message.ToolCalls) != 1 {
		t.Fatalf("untranslated response escaped: %+v err=%v", response, err)
	}
}
