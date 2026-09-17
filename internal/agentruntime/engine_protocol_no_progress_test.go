package agentruntime

import (
	"encoding/json"
	"testing"
)

func TestToolCallRoundSignatureUsesRejectionFamilyForSemanticNoProgress(t *testing.T) {
	first := []ToolCall{{Name: "ask_user", Arguments: json.RawMessage(`{"question":"provide another public URL"}`)}}
	second := []ToolCall{{Name: "ask_user", Arguments: json.RawMessage(`{"question":"choose a replacement public source"}`)}}
	rejected := map[int]toolCallRejection{0: {Code: "agent_owned_decision", Preflight: true}}
	if got, want := toolCallRoundSignature(first, rejected), toolCallRoundSignature(second, rejected); got != want {
		t.Fatalf("same rejection family changed with wording:\nfirst=%q\nsecond=%q", got, want)
	}
	if got, want := toolCallRoundSignature(first, nil), toolCallRoundSignature(second, nil); got == want {
		t.Fatalf("executable calls with different arguments collapsed: %q", got)
	}
	other := map[int]toolCallRejection{0: {Code: "invalid_tool_arguments", Preflight: true}}
	if toolCallRoundSignature(first, rejected) == toolCallRoundSignature(first, other) {
		t.Fatal("different rejection families collapsed into one signature")
	}
}

func TestToolCallRoundSignatureIgnoresPresentationOnlyDescription(t *testing.T) {
	first := []ToolCall{{Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["report.md"],"human_description":"保存报告"}`)}}
	second := []ToolCall{{Name: "save_artifacts", Arguments: json.RawMessage(`{"human_description":"最终保存完整报告","files":["report.md"]}`)}}
	if got, want := toolCallRoundSignature(first, nil), toolCallRoundSignature(second, nil); got != want {
		t.Fatalf("presentation-only wording changed semantic signature:\nfirst=%q\nsecond=%q", got, want)
	}
	different := []ToolCall{{Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["table.csv"],"human_description":"保存表格"}`)}}
	if toolCallRoundSignature(first, nil) == toolCallRoundSignature(different, nil) {
		t.Fatal("different execution arguments collapsed into one signature")
	}
}

func TestToolCallRejectionBudgetSeparatesIndependentMultiplexedOperations(t *testing.T) {
	attempts := map[string]int{}
	openAlexCall := []ToolCall{{Name: "repl"}}
	openAlexRejection := toolCallRejection{
		Code:       "mcp_schema_preflight_required",
		Diagnostic: `{"message":"The literal host.mcp call for literature/openalex_search_works does not match its live input schema."}`,
		Retryable:  true,
	}
	for attempt := 0; attempt < maxModelVisibleRejectionFamilyAttempts; attempt++ {
		rejections := map[int]toolCallRejection{0: openAlexRejection}
		applyToolCallRejectionFamilyBudget(openAlexCall, rejections, attempts)
		if !rejections[0].Retryable {
			t.Fatalf("OpenAlex family exhausted early at attempt %d", attempt+1)
		}
	}
	exhausted := map[int]toolCallRejection{0: openAlexRejection}
	applyToolCallRejectionFamilyBudget(openAlexCall, exhausted, attempts)
	if exhausted[0].Retryable {
		t.Fatal("identical OpenAlex rejection family was not bounded")
	}

	pubMed := map[int]toolCallRejection{0: {
		Code:       "mcp_schema_preflight_required",
		Diagnostic: `{"message":"The literal host.mcp call for pubmed/search_articles does not match its live input schema."}`,
		Retryable:  true,
	}}
	applyToolCallRejectionFamilyBudget([]ToolCall{{Name: "repl"}}, pubMed, attempts)
	if !pubMed[0].Retryable {
		t.Fatal("an unrelated PubMed correction inherited the exhausted OpenAlex budget")
	}
}
