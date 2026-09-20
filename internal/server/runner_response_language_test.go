package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
)

type responseLanguageFixtureModel struct {
	chunks   []string
	events   []agentruntime.ModelStreamEvent
	response agentruntime.ModelResponse
}

func TestResponseLanguageConversionAuditsBothSidesWithoutSemanticVerdict(t *testing.T) {
	original := "Show the requested repetition literally " + strings.Repeat("echo ", 30)
	translated := "按请求原样重复 " + strings.Repeat("回声 ", 30)
	model := &responseLanguageSequenceModel{responses: []agentruntime.ModelResponse{
		{Message: agentruntime.Message{Content: original}},
		{Message: agentruntime.Message{Content: translated}},
	}}
	var record map[string]any
	client := &sessionRunnerResponseLanguageModelClient{delegate: model, language: "zh", audit: func(value map[string]any) { record = value }}
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{})
	if err != nil || len(model.requests) != 2 || response.Message.Content != translated {
		t.Fatalf("diagnostics changed intentional repetition: calls=%d err=%v", len(model.requests), err)
	}
	if record["input_longest_equal_unit_run"] != 30 || record["output_longest_equal_unit_run"] != 30 ||
		record["input_whitespace_units"] != 35 || record["output_whitespace_units"] != 31 {
		t.Fatalf("conversion sides are not independently observable: %v", record)
	}
}

type responseLanguageSequenceModel struct {
	chunks    [][]string
	responses []agentruntime.ModelResponse
	requests  []agentruntime.ModelRequest
}

func (model *responseLanguageSequenceModel) Complete(
	_ context.Context,
	request agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	model.requests = append(model.requests, request)
	index := len(model.requests) - 1
	if index >= len(model.responses) {
		index = len(model.responses) - 1
	}
	return model.responses[index], nil
}

func (model *responseLanguageSequenceModel) CompleteStream(
	_ context.Context,
	request agentruntime.ModelRequest,
	emit func(agentruntime.ModelStreamEvent) error,
) (agentruntime.ModelResponse, error) {
	model.requests = append(model.requests, request)
	index := len(model.requests) - 1
	if index >= len(model.responses) {
		index = len(model.responses) - 1
	}
	chunkIndex := index
	if chunkIndex >= len(model.chunks) {
		chunkIndex = len(model.chunks) - 1
	}
	for _, chunk := range model.chunks[chunkIndex] {
		if emit != nil {
			if err := emit(agentruntime.ModelStreamEvent{ContentDelta: chunk}); err != nil {
				return agentruntime.ModelResponse{}, err
			}
		}
	}
	return model.responses[index], nil
}

func (model responseLanguageFixtureModel) Complete(
	context.Context,
	agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	return model.response, nil
}

func (model responseLanguageFixtureModel) CompleteStream(
	_ context.Context,
	_ agentruntime.ModelRequest,
	emit func(agentruntime.ModelStreamEvent) error,
) (agentruntime.ModelResponse, error) {
	if len(model.events) > 0 {
		for _, event := range model.events {
			if emit != nil {
				if err := emit(event); err != nil {
					return agentruntime.ModelResponse{}, err
				}
			}
		}
		return model.response, nil
	}
	for _, chunk := range model.chunks {
		if emit != nil {
			if err := emit(agentruntime.ModelStreamEvent{ContentDelta: chunk}); err != nil {
				return agentruntime.ModelResponse{}, err
			}
		}
	}
	return model.response, nil
}

func TestResponseLanguageGatePreservesToolCallBoundaryAfterChineseProgress(t *testing.T) {
	client := &sessionRunnerResponseLanguageModelClient{
		delegate: responseLanguageFixtureModel{
			events: []agentruntime.ModelStreamEvent{
				{Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: "我先核对两项原始证据。"},
				{Kind: agentruntime.ModelStreamEventToolCallBoundary},
			},
			response: agentruntime.ModelResponse{Message: agentruntime.Message{
				Content:   "我先核对两项原始证据。",
				ToolCalls: []agentruntime.ToolCall{{ID: "call-1", Name: "web_search"}},
			}},
		},
		language: "zh",
	}
	events := make([]agentruntime.ModelStreamEvent, 0, 2)
	_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, func(event agentruntime.ModelStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil || len(events) != 2 ||
		events[0].Kind != agentruntime.ModelStreamEventContentDelta ||
		events[0].ContentDelta != "我先核对两项原始证据。" ||
		events[1].Kind != agentruntime.ModelStreamEventToolCallBoundary {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestResponseLanguageGateRejectsEnglishBeforeDurableEmission(t *testing.T) {
	content := "All four figures and their layout manifests exist. Let me validate the layouts now."
	client := &sessionRunnerResponseLanguageModelClient{
		delegate: responseLanguageFixtureModel{
			chunks:   []string{"All four figures and ", "their layout manifests exist. ", "Let me validate the layouts now."},
			response: agentruntime.ModelResponse{Message: agentruntime.Message{Content: content}},
		},
		language: "zh",
	}
	var emitted strings.Builder
	_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, func(event agentruntime.ModelStreamEvent) error {
		emitted.WriteString(event.ContentDelta)
		return nil
	})
	var mismatch sessionRunnerResponseLanguageMismatch
	if !errors.As(err, &mismatch) || emitted.Len() != 0 {
		t.Fatalf("err=%v emitted=%q", err, emitted.String())
	}
}

func TestResponseLanguageGateTranslatesEnglishOpeningBeforeDurableEmission(t *testing.T) {
	english := "All requested result files are ready. I will summarize them now."
	chinese := "全部结果文件已经生成，我现在用简体中文汇总。"
	model := &responseLanguageSequenceModel{
		chunks: [][]string{{english}, {"全部结果文件已经生成，", "我现在用简体中文汇总。"}},
		responses: []agentruntime.ModelResponse{
			{Message: agentruntime.Message{Content: english}},
			{Message: agentruntime.Message{Content: chinese}},
		},
	}
	client := &sessionRunnerResponseLanguageModelClient{delegate: model, language: "zh"}
	var emitted strings.Builder
	response, err := client.CompleteStream(
		context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "继续"}}},
		func(event agentruntime.ModelStreamEvent) error {
			emitted.WriteString(event.ContentDelta)
			return nil
		},
	)
	if err != nil || response.Message.Content != chinese || emitted.String() != chinese || len(model.requests) != 2 {
		t.Fatalf("response=%#v err=%v emitted=%q requests=%d", response, err, emitted.String(), len(model.requests))
	}
	if len(model.requests[1].Messages) != 2 || len(model.requests[1].Tools) != 0 ||
		model.requests[1].Messages[0].Role != "system" ||
		model.requests[1].Messages[0].Content != simplifiedChineseTranslationInstruction ||
		model.requests[1].Messages[1].Role != "user" ||
		!strings.Contains(model.requests[1].Messages[1].Content, english) ||
		model.requests[1].Temperature == nil || *model.requests[1].Temperature != 0 {
		t.Fatalf("translation request=%#v", model.requests[1])
	}
}

func TestResponseLanguageGateTranslatesNonStreamingEnglishOpening(t *testing.T) {
	english := "All requested result files are ready. I will summarize them now."
	chinese := "全部结果文件已经生成，我现在用简体中文汇总。"
	model := &responseLanguageSequenceModel{responses: []agentruntime.ModelResponse{
		{Message: agentruntime.Message{Content: english}},
		{Message: agentruntime.Message{Content: chinese}},
	}}
	client := &sessionRunnerResponseLanguageModelClient{delegate: model, language: "zh"}
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{})
	if err != nil || response.Message.Content != chinese || len(model.requests) != 2 {
		t.Fatalf("response=%#v err=%v requests=%d", response, err, len(model.requests))
	}
}

func TestResponseLanguageGateReleasesChineseIntoOriginalStream(t *testing.T) {
	chunks := []string{"我先检查输入，", "再调用本地 RDKit 计算 SMILES，", "最后核对结果。"}
	content := strings.Join(chunks, "")
	client := &sessionRunnerResponseLanguageModelClient{
		delegate: responseLanguageFixtureModel{
			chunks:   chunks,
			response: agentruntime.ModelResponse{Message: agentruntime.Message{Content: content}},
		},
		language: "zh",
	}
	var emitted strings.Builder
	response, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, func(event agentruntime.ModelStreamEvent) error {
		emitted.WriteString(event.ContentDelta)
		return nil
	})
	if err != nil || emitted.String() != content || response.Message.Content != content {
		t.Fatalf("response=%#v err=%v emitted=%q", response, err, emitted.String())
	}
}

func TestResponseLanguageGateReleasesOnFirstHanWithoutArtificialTokenDelay(t *testing.T) {
	chunks := []string{"（", "我", "先检查输入。"}
	content := strings.Join(chunks, "")
	client := &sessionRunnerResponseLanguageModelClient{
		delegate: responseLanguageFixtureModel{
			chunks: chunks, response: agentruntime.ModelResponse{Message: agentruntime.Message{Content: content}},
		},
		language: "zh",
	}
	emitted := make([]string, 0, 2)
	response, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, func(event agentruntime.ModelStreamEvent) error {
		emitted = append(emitted, event.ContentDelta)
		return nil
	})
	if err != nil || response.Message.Content != content || len(emitted) != 2 ||
		emitted[0] != "（我" || emitted[1] != "先检查输入。" {
		t.Fatalf("response=%#v err=%v emitted=%#v", response, err, emitted)
	}
}

func TestResponseLanguageGateAllowsToolOnlyResponse(t *testing.T) {
	client := &sessionRunnerResponseLanguageModelClient{
		delegate: responseLanguageFixtureModel{response: agentruntime.ModelResponse{
			Message: agentruntime.Message{ToolCalls: []agentruntime.ToolCall{{ID: "call-1", Name: "Read"}}},
		}},
		language: "zh",
	}
	response, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, nil)
	if err != nil || len(response.Message.ToolCalls) != 1 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestResponseLanguageClassifierAllowsChineseNarrativeWithEnglishEvidence(t *testing.T) {
	text := "根据搜索结果，最相关的三个来源如下：\n" +
		"1. GitHub - openai/codex: Lightweight coding agent — https://github.com/openai/codex\n" +
		"2. OpenAI Codex Documentation — https://developers.openai.com/codex/\n" +
		"3. Codex CLI release notes — `codex-rs/core/src/lib.rs`"
	if sessionRunnerClearlyEnglishNarrative(text, true) {
		t.Fatalf("Chinese narrative with preserved evidence was rejected: %s", text)
	}
	if !sessionRunnerClearlyEnglishNarrative("答：This response remains entirely in English and does not satisfy the requested language.", true) {
		t.Fatal("a token Chinese prefix masked an English response")
	}
}

func TestResponseLanguageGateDefersToRequiredToolProtocol(t *testing.T) {
	content := "I need to inspect the current evidence before choosing the next action."
	for _, choice := range []any{
		"required",
		map[string]any{"type": "tool", "name": "software_runtime"},
		map[string]any{"type": "function", "function": map[string]any{"name": "software_runtime"}},
	} {
		client := &sessionRunnerResponseLanguageModelClient{
			delegate: responseLanguageFixtureModel{
				chunks:   []string{content},
				response: agentruntime.ModelResponse{Message: agentruntime.Message{Content: content}},
			},
			language: "zh",
		}
		var emitted strings.Builder
		response, err := client.CompleteStream(
			context.Background(),
			agentruntime.ModelRequest{ToolChoice: choice},
			func(event agentruntime.ModelStreamEvent) error {
				emitted.WriteString(event.ContentDelta)
				return nil
			},
		)
		if err != nil || response.Message.Content != content || emitted.String() != "" {
			t.Fatalf("choice=%#v response=%#v err=%v emitted=%q", choice, response, err, emitted.String())
		}
	}
}

func TestResponseLanguageGateStillChecksUnconstrainedResponse(t *testing.T) {
	content := "I need to inspect the current evidence before choosing the next action."
	client := &sessionRunnerResponseLanguageModelClient{
		delegate: responseLanguageFixtureModel{
			response: agentruntime.ModelResponse{Message: agentruntime.Message{Content: content}},
		},
		language: "zh",
	}
	_, err := client.Complete(context.Background(), agentruntime.ModelRequest{ToolChoice: "auto"})
	var mismatch sessionRunnerResponseLanguageMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("err=%v, want response language mismatch", err)
	}
}

func TestResponseLanguageMismatchUsesExistingBoundedCorrectionRecovery(t *testing.T) {
	if !runnerInterruptionMayContinueSameTask(sessionRunnerResponseLanguageMismatchReasonCode) ||
		!runnerInterruptionAutoResume(sessionRunnerResponseLanguageMismatchReasonCode) ||
		!runnerInterruptionNeedsRecoveryBackoff(sessionRunnerResponseLanguageMismatchReasonCode) {
		t.Fatal("presentation correction must retain the task through the existing backoff dispatcher")
	}
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type":          "runner_checkpoint",
		"reason_code":   sessionRunnerResponseLanguageMismatchReasonCode,
		"resume_detail": "restart the response in Simplified Chinese",
	}}}
	correction, found := latestRunnerCorrection(entries)
	if !found || correction.ReasonCode != sessionRunnerResponseLanguageMismatchReasonCode {
		t.Fatalf("language correction=%#v found=%t", correction, found)
	}
	context := recoveredRunnerCorrectionContext(entries)
	if !strings.Contains(context, "Simplified Chinese") ||
		!strings.Contains(context, `"required_transition":"replace_response_presentation"`) {
		t.Fatalf("language correction context=%q", context)
	}
	contract := applyRecoveredRunnerCorrectionToTaskContract(sessionRunnerTaskContract{
		Version: 1, AcceptanceChecks: []string{"complete the task"},
	}, correction)
	if contract.CorrectionReason != sessionRunnerResponseLanguageMismatchReasonCode ||
		!strings.Contains(strings.Join(contract.AcceptanceChecks, "\n"), "restart the response in Simplified Chinese") {
		t.Fatalf("language correction task contract=%#v", contract)
	}
}
