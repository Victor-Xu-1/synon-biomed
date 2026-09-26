package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

type presentationFailureModel struct{ err error }

func (model presentationFailureModel) Complete(context.Context, agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{}, model.err
}

func TestPresentationConversionPreservesProviderFailureIdentity(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("provider endpoint returned 503: unavailable")} {
		client := &sessionRunnerResponseLanguageModelClient{delegate: presentationFailureModel{err: cause}, language: "zh"}
		_, err := client.translateCandidate(context.Background(), agentruntime.ModelRequest{}, agentruntime.ModelResponse{Message: agentruntime.Message{Content: "The verified resource is ready for the next operation."}})
		var mismatch sessionRunnerResponseLanguageMismatch
		if !errors.Is(err, cause) || errors.As(err, &mismatch) {
			t.Errorf("provider failure converted into language failure: cause=%v got=%v", cause, err)
		}
	}
}

func TestPresentationConversionCancellationNeverExecutesOriginalTool(t *testing.T) {
	client := &sessionRunnerResponseLanguageModelClient{delegate: presentationFailureModel{err: context.Canceled}, language: "zh"}
	original := agentruntime.ModelResponse{Message: agentruntime.Message{Content: "Inspecting the verified resource.", ToolCalls: []agentruntime.ToolCall{{ID: "original", Name: "inspect"}}}}
	response, err := client.localizeOrKeepToolCall(context.Background(), agentruntime.ModelRequest{}, original)
	if !errors.Is(err, context.Canceled) || len(response.Message.ToolCalls) != 0 {
		t.Fatalf("canceled conversion released an action: response=%#v err=%v", response, err)
	}
}

func TestPresentationValidationAuditSeparatesCausesWithoutPayloads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply agentruntime.Message
	}{
		{"empty_response", agentruntime.Message{}},
		{"unexpected_tool_calls", agentruntime.Message{Content: "资源已核对。", ToolCalls: []agentruntime.ToolCall{{ID: "injected", Name: "write"}}}},
		{"output_language", agentruntime.Message{Content: "The verified file is ready."}},
		{"protected_literals_changed", agentruntime.Message{Content: "文件已保存，包含 9 条记录。"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var audit map[string]any
			model := &responseLanguageSequenceModel{responses: []agentruntime.ModelResponse{{Message: tc.reply}}}
			client := &sessionRunnerResponseLanguageModelClient{delegate: model, language: "zh", audit: func(record map[string]any) { audit = record }}
			_, err := client.translateCandidate(context.Background(), agentruntime.ModelRequest{}, agentruntime.ModelResponse{Message: agentruntime.Message{Content: "Saved 2 records at https://private.example.test/?token=DO_NOT_LOG."}})
			var mismatch sessionRunnerResponseLanguageMismatch
			if !errors.As(err, &mismatch) || mismatch.validationCode() != tc.name || audit["failure_code"] != tc.name {
				t.Fatalf("cause=%v audit=%#v", err, audit)
			}
			for _, value := range audit {
				if text, ok := value.(string); ok && strings.Contains(text, "DO_NOT_LOG") {
					t.Fatal("audit retained candidate payload")
				}
			}
		})
	}
}

func TestMalformedProgressProducesTypedLocalDiagnostic(t *testing.T) {
	for _, event := range []agentruntime.ModelStreamEvent{
		{Kind: agentruntime.ModelStreamEventPublicProgressDelta, ContentDelta: "unbound"},
		{Kind: agentruntime.ModelStreamEventPublicProgressBoundary, BlockID: "absent"},
		{Kind: agentruntime.ModelStreamEventPublicProgressDelta, BlockID: "too-large", ContentDelta: strings.Repeat("x", 16*1024+1)},
	} {
		blocks := runnerLanguageProgressBlocks{}
		_, err := blocks.accept(event)
		var diagnostic sessionRunnerPresentationViolation
		var correction sessionRunnerBoundedCorrection
		if !errors.As(err, &diagnostic) || errors.As(err, &correction) {
			t.Fatalf("progress diagnostic became a task interruption: %v", err)
		}
	}
}
