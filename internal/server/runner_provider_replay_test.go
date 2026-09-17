package server

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestSessionEntriesToProviderMessagesCompactsHistoricalExternalizedResultPreview(t *testing.T) {
	preview := strings.Repeat("科学-evidence-", 5_000)
	descriptor := map[string]any{
		"artifact_id": "large-tool-result-0123456789abcdef0123456789abcdef",
		"version_id":  "ltr-historical-preview", "sha256": strings.Repeat("a", 64),
		"size_bytes": 230_608, "content_type": "application/json", "outcome": "succeeded",
		"content_url": "/api/artifacts/large-tool-result-0123456789abcdef0123456789abcdef/versions/ltr-historical-preview",
		"read_with":   `read_file(version_id="ltr-historical-preview")`,
		"preview":     preview, "truncated": true,
	}
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "continue"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-large", "type": "function", "name": "WebFetch", "arguments": map[string]any{"url": "https://example.test"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-large", "toolName": "WebFetch",
			"toolPhase": "completed", "toolResult": descriptor,
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[3].Role != "tool" {
		t.Fatalf("messages=%#v", messages)
	}
	var replayed map[string]any
	if err := json.Unmarshal([]byte(messages[3].Content), &replayed); err != nil {
		t.Fatal(err)
	}
	compactPreview, _ := replayed["preview"].(string)
	if len([]byte(compactPreview)) == 0 || len([]byte(compactPreview)) > 2_000 ||
		!strings.HasPrefix(preview, compactPreview) ||
		replayed["artifact_id"] != descriptor["artifact_id"] ||
		replayed["version_id"] != descriptor["version_id"] || replayed["sha256"] != descriptor["sha256"] {
		t.Fatalf("replayed descriptor=%#v", replayed)
	}
	if replayed["read_with"] != descriptor["read_with"] {
		t.Fatalf("provider replay lost the exact full-result read contract: %#v", replayed)
	}
	if descriptor["preview"] != preview {
		t.Fatal("durable historical descriptor was mutated during provider projection")
	}
}

func TestSessionEntriesToProviderMessagesNormalizesLegacyReusedLargeResultDescriptor(t *testing.T) {
	descriptor := map[string]any{
		"artifact_id": "large-tool-result-0123456789abcdef0123456789abcdef",
		"version_id":  "ltr-legacy-reused", "sha256": strings.Repeat("b", 64),
		"size_bytes": 407_910, "content_type": "application/json", "outcome": "succeeded",
		"content_url": "/api/artifacts/large-tool-result-0123456789abcdef0123456789abcdef/versions/ltr-legacy-reused",
		"preview":     `{"ok":true,"result":{"statusCode":200}}`, "truncated": true,
		"reused": true,
	}
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "continue"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-large", "type": "function", "name": "web_fetch", "arguments": map[string]any{"url": "https://example.test"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-large", "toolName": "web_fetch",
			"toolPhase": "completed", "toolResult": descriptor,
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[3].Role != "tool" || messages[3].ToolCallID != "call-large" ||
		strings.Contains(messages[3].Content, `"reused"`) {
		t.Fatalf("legacy descriptor replay=%#v", messages)
	}
	var normalized map[string]any
	if json.Unmarshal([]byte(messages[3].Content), &normalized) != nil || len(normalized) != 9 ||
		normalized["artifact_id"] != descriptor["artifact_id"] || normalized["version_id"] != descriptor["version_id"] {
		t.Fatalf("normalized descriptor=%s", messages[3].Content)
	}
}

func TestSessionEntriesToProviderMessagesKeepsUnknownDescriptorExtensionsFailClosed(t *testing.T) {
	base := map[string]any{
		"artifact_id": "large-tool-result-0123456789abcdef0123456789abcdef",
		"version_id":  "ltr-legacy-invalid", "sha256": strings.Repeat("c", 64),
		"size_bytes": 1024, "content_type": "application/json", "outcome": "succeeded",
		"content_url": "/api/artifacts/large-tool-result-0123456789abcdef0123456789abcdef/versions/ltr-legacy-invalid",
		"preview":     `{}`, "truncated": true,
	}
	for name, mutate := range map[string]func(map[string]any){
		"false reused marker": func(value map[string]any) { value["reused"] = false },
		"additional unknown field": func(value map[string]any) {
			value["reused"] = true
			value["unexpected"] = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			descriptor := make(map[string]any, len(base)+2)
			for key, value := range base {
				descriptor[key] = value
			}
			mutate(descriptor)
			entries := []eventjournal.Entry{
				{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "continue"}},
				{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
					map[string]any{"id": "call-large", "type": "function", "name": "web_fetch", "arguments": map[string]any{"url": "https://example.test"}},
				}}},
				{EventID: 3, Message: eventjournal.Message{
					"type": "runner_checkpoint", "toolCallId": "call-large", "toolName": "web_fetch",
					"toolPhase": "completed", "toolResult": descriptor,
				}},
			}
			if messages, err := sessionEntriesToProviderMessages("system", entries); err == nil {
				t.Fatalf("messages=%#v err=nil", messages)
			}
		})
	}
}

func TestSessionEntriesToProviderMessagesPreservesNativeToolProtocol(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "run it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-a", "type": "function", "name": "python", "arguments": map[string]any{"code": "print(42)", "environment": "python"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-a",
			"toolPhase": "completed", "toolResult": map[string]any{"ok": true, "stdout": "42\n"}}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[2].Role != "assistant" || len(messages[2].ToolCalls) != 1 ||
		messages[2].ToolCalls[0].ID != "call-a" || messages[2].ToolCalls[0].Function.Name != "python" ||
		messages[3].Role != "tool" || messages[3].ToolCallID != "call-a" ||
		!strings.Contains(messages[3].Content, `"stdout":"42\n"`) {
		t.Fatalf("messages=%#v", messages)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "Recovered tool transcript") {
			t.Fatalf("tool protocol was downgraded to system prose: %#v", messages)
		}
	}
}

func TestSessionEntriesToProviderMessagesAddsStructuredFailureRepairContext(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "dock it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-dock", "type": "function", "name": softwareRuntimeToolName, "arguments": map[string]any{
				"capability": "molecular-docking", "provider": "local-conda", "language": "python", "executable": "python",
			}},
		}}},
		{EventID: 3, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-dock", "toolName": softwareRuntimeToolName,
			"toolPhase": "failed", "toolResult": map[string]any{
				"ok": true, "code": "invalid_arguments", "retryable": true,
				"recovery": "prepare_the_source_structure_as_valid_pdbqt_in_the_governed_scientific_environment_then_retry_with_new_artifact_versions",
			}}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range messages {
		if message.Role == "system" && strings.Contains(message.Content, "Synon task self-repair controller") &&
			strings.Contains(message.Content, "new_artifact_versions") {
			found = true
		}
	}
	if !found {
		t.Fatalf("structured failure repair context missing: %#v", messages)
	}
}

func TestRecoveredArtifactFailureContextNamesUnsupportedAndAvailableReferences(t *testing.T) {
	entries := []eventjournal.Entry{{EventID: 1, Message: eventjournal.Message{
		"type": "runner_checkpoint", "toolPhase": "completed", "toolName": "save_artifacts",
		"toolResult": map[string]any{
			"ok": false, "code": "artifact_save_requires_correction", "retryable": false,
			"recovery": "correct_or_omit_the_failed_files_then_continue",
			"errors": []any{map[string]any{
				"code":                            "unsupported_evidence_references",
				"unsupported_evidence_references": []any{"accession:source:ITEM12"},
				"available_evidence_references":   []any{"accession:source:ITEM112", "accession:source:ITEM25"},
			}},
		},
	}}}
	context := recoveredToolFailureContext(entries, 0)
	if !strings.Contains(context, "unsupported=accession:source:ITEM12") ||
		!strings.Contains(context, "unique_exact_replacement_candidates=accession:source:ITEM12=>accession:source:ITEM112") ||
		!strings.Contains(context, "exact_available=accession:source:ITEM112,accession:source:ITEM25") {
		t.Fatalf("context=%q", context)
	}
}

func TestUniqueEvidenceReferenceReplacementCandidatesRejectsAmbiguity(t *testing.T) {
	got := uniqueEvidenceReferenceReplacementCandidates(
		[]string{"accession:source:ITEM12"},
		[]string{"accession:source:ITEM112", "accession:source:ITEM122"},
	)
	if len(got) != 0 {
		t.Fatalf("ambiguous candidates=%#v", got)
	}
	got = uniqueEvidenceReferenceReplacementCandidates(
		[]string{"accession:source:ITEM12"},
		[]string{"accession:other:ITEM112", "accession:source:ITEM112"},
	)
	if !slices.Equal(got, []string{"accession:source:ITEM12=>accession:source:ITEM112"}) {
		t.Fatalf("unique candidates=%#v", got)
	}
}

func TestSessionEntriesToProviderMessagesSettlesPrestartFailure(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "run it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-invalid", "type": "function", "name": "generate_plan", "arguments": map[string]any{"title": "wrong"}, "rejectedBeforeExecution": true},
		}}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-invalid", "toolName": "generate_plan",
			"toolPhase": prestartToolFailurePhase, "status": "failed",
			"toolResult": map[string]any{
				"ok": false, "executed": false, "code": "invalid_tool_arguments", "retryable": true,
				"recovery": "Choose another advertised tool or correct the schema.",
			},
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 || messages[3].Role != "assistant" ||
		len(messages[3].ToolCalls) != 1 || messages[3].ToolCalls[0].ID != "call-invalid" ||
		messages[4].Role != "tool" || messages[4].ToolCallID != "call-invalid" ||
		!strings.Contains(messages[4].Content, "invalid_tool_arguments") {
		t.Fatalf("messages=%#v", messages)
	}
	if messages[1].Role != "system" || !strings.Contains(messages[1].Content, "Synon task self-repair controller") {
		t.Fatalf("pre-start repair context missing: %#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesDeduplicatesIdenticalTerminalRecovery(t *testing.T) {
	result := map[string]any{
		"ok": false, "executed": false, "status": "code_preflight_required",
		"recovery": "install the missing import in a verified successor environment",
	}
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "run it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-preflight", "type": "function", "name": "python", "arguments": map[string]any{"code": "import pooch"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-preflight", "toolName": "python",
			"toolPhase": prestartToolFailurePhase, "status": "failed", "toolResult": result,
		}},
		{EventID: 4, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-preflight", "toolName": "python",
			"toolPhase": "failed", "status": "failed", "toolResult": result,
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	toolMessages := 0
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == "call-preflight" {
			toolMessages++
		}
	}
	if toolMessages != 1 {
		t.Fatalf("identical terminal recovery produced %d provider tool messages: %#v", toolMessages, messages)
	}
}

func TestSessionEntriesToProviderMessagesRejectsConflictingDuplicateTerminalRecovery(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-conflict", "type": "function", "name": "python", "arguments": map[string]any{"code": "print(1)"}},
		}}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-conflict",
			"toolPhase": "failed", "toolResult": map[string]any{"ok": false, "code": "first"}}},
		{EventID: 3, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-conflict",
			"toolPhase": "failed", "toolResult": map[string]any{"ok": false, "code": "different"}}},
	}
	if messages, err := sessionEntriesToProviderMessages("system", entries); err == nil {
		t.Fatalf("conflicting terminal recovery was accepted: %#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesKeepsPreflightAuthorityOverInvalidLaterExecution(t *testing.T) {
	preflight := map[string]any{
		"ok": false, "executed": false,
		"code":    "scientific_download_contract_preflight_required",
		"message": "the source contract was rejected before execution",
	}
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-download", "type": "function", "name": "download_public_scientific_file", "arguments": map[string]any{
				"url": "https://example.org/download", "filename": "guide.pdf",
			}},
		}}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-download", "toolName": "download_public_scientific_file",
			"toolPhase": "completed", "toolResult": preflight,
		}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-download", "toolName": "download_public_scientific_file",
			"toolPhase": "failed", "toolResult": map[string]any{"ok": false, "error": "invalid later execution"},
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	toolMessages := 0
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == "call-download" {
			toolMessages++
			if !strings.Contains(message.Content, "scientific_download_contract_preflight_required") ||
				strings.Contains(message.Content, "invalid later execution") {
				t.Fatalf("provider result lost preflight authority: %#v", message)
			}
		}
	}
	if toolMessages != 1 {
		t.Fatalf("preflight recovery produced %d provider tool messages: %#v", toolMessages, messages)
	}
}

func TestSessionEntriesToProviderMessagesKeepsAgentOwnedDecisionPreflightAuthority(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-question", "type": "function", "name": "ask_user", "arguments": map[string]any{"question": "approve output?"}},
		}}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-question", "toolName": "ask_user",
			"toolPhase": "completed", "toolResult": map[string]any{
				"ok": false, "executed": false, "code": "agent_owned_decision", "retryable": true,
			},
		}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-question", "toolName": "ask_user",
			"toolPhase": "completed", "toolResult": map[string]any{
				"ok": false, "executed": false, "code": "agent_owned_decision", "retryable": false,
			},
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	toolMessages := 0
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == "call-question" {
			toolMessages++
			if !strings.Contains(message.Content, `"retryable":true`) {
				t.Fatalf("first preflight settlement was not preserved: %#v", message)
			}
		}
	}
	if toolMessages != 1 {
		t.Fatalf("agent-owned preflight produced %d provider results: %#v", toolMessages, messages)
	}
}

func TestSessionEntriesToProviderMessagesIgnoresLegacyBackgroundLifecycleDuplicate(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "run it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-background", "type": "function", "name": "python", "arguments": map[string]any{"background": true, "code": "work()"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-background", "toolName": "python",
			"toolPhase": "completed", "status": "completed", "toolInput": map[string]any{"background": true, "code": "work()"},
			"toolResult": map[string]any{"ok": true, "status": "background", "exec_id": "exec-1"},
		}},
		{EventID: 4, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-background", "toolName": "python",
			"toolPhase": "cancelled", "status": "cancelled",
			"toolResult": map[string]any{"ok": false, "status": "cancelled", "exec_id": "exec-1"},
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	toolMessages := 0
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == "call-background" {
			toolMessages++
		}
	}
	if toolMessages != 1 {
		t.Fatalf("background lifecycle produced %d provider results: %#v", toolMessages, messages)
	}
}

func TestSessionEntriesToProviderMessagesDefersConversationUntilToolBatchSettles(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "run both"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-a", "type": "function", "name": "ask_user", "arguments": map[string]any{"questions": []any{}}},
			map[string]any{"id": "call-b", "type": "function", "name": "python", "arguments": map[string]any{"code": "print(42)"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{"type": "message", "role": "assistant", "text": "legacy AskUser projection"}},
		{EventID: 4, Message: eventjournal.Message{"type": "message", "role": "user", "text": "structured answer projection"}},
		{EventID: 5, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-a",
			"toolPhase": "completed", "toolResult": map[string]any{"status": "answered"}}},
		{EventID: 6, Message: eventjournal.Message{"type": "message", "role": "user", "text": "model continuation"}},
		{EventID: 7, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-b",
			"toolPhase": "completed", "toolResult": map[string]any{"ok": true, "stdout": "42\n"}}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 8 || messages[2].Role != "assistant" || len(messages[2].ToolCalls) != 2 ||
		messages[3].Role != "tool" || messages[3].ToolCallID != "call-a" ||
		messages[4].Role != "tool" || messages[4].ToolCallID != "call-b" ||
		messages[5].Content != "legacy AskUser projection" || messages[6].Content != "structured answer projection" ||
		messages[7].Content != "model continuation" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesSettlesOutcomeUnknownRecovery(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "run it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-recovered", "type": "function", "name": "web_research", "arguments": map[string]any{"query": "test"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-recovered",
			"toolPhase": "outcome_unknown", "toolResult": map[string]any{
				"ok": false, "error": map[string]any{
					"code": "tool_outcome_unknown", "message": "Tool execution outcome is unavailable after runner recovery.",
				},
			}}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[2].Role != "assistant" || len(messages[2].ToolCalls) != 1 ||
		messages[2].ToolCalls[0].ID != "call-recovered" ||
		messages[3].Role != "tool" || messages[3].ToolCallID != "call-recovered" ||
		!strings.Contains(messages[3].Content, "tool_outcome_unknown") {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesSkipsCrossTurnOutcomeUnknownSettlement(t *testing.T) {
	// A stale running tool item from a completed earlier turn is settled by
	// runner recovery as outcome_unknown. Its tool call was declared in an
	// earlier replay window, so the settlement must not be injected into the
	// current provider conversation nor fail the replay.
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "new follow-up"}},
		{EventID: 2, SourceEventType: transcriptstore.TerminalToolRecoveryEventType,
			Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-stale",
				"toolPhase": "outcome_unknown", "toolResult": map[string]any{
					"ok": false, "error": map[string]any{
						"code": "tool_outcome_unknown", "message": "Tool execution outcome is unavailable after runner recovery.",
					},
				}}},
		{EventID: 3, Message: eventjournal.Message{"type": "message", "role": "assistant", "text": "continuing the new turn"}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[1].Role != "user" || messages[2].Role != "assistant" ||
		messages[2].Content != "continuing the new turn" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesSkipsCrossTurnLateCompletedSettlement(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "new follow-up"}},
		{EventID: 2, SourceEventType: transcriptstore.TerminalToolRecoveryEventType,
			Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-late-completed",
				"toolPhase": "completed", "toolResult": map[string]any{"ok": true}}},
		{EventID: 3, Message: eventjournal.Message{"type": "message", "role": "assistant", "text": "continuing the new turn"}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[1].Role != "user" || messages[2].Role != "assistant" ||
		messages[2].Content != "continuing the new turn" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesSkipsLegacyRematerializedLargeResultWithoutRoot(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "continue the current task"}},
		{EventID: 2, SourceEventType: "runner_checkpoint", Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-compacted-large", "toolName": "search_skills",
			"toolPhase": "completed", "lifecyclePhase": "recovery",
			"message":        "recovered durable externalized tool result",
			"resumeCacheKey": "chat-tool-completed-call-compacted-large",
			"toolResult":     map[string]any{"ok": true},
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Role != "user" || messages[1].Content != "continue the current task" {
		t.Fatalf("legacy rematerialized receipt leaked into provider replay: %#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesKeepsLateCompletedSettlementWithMatchingCall(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "run it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-late-completed", "type": "function", "name": "python", "arguments": map[string]any{"code": "print(1)"}},
		}}},
		{EventID: 3, SourceEventType: transcriptstore.TerminalToolRecoveryEventType,
			Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-late-completed",
				"toolPhase": "completed", "toolResult": map[string]any{"ok": true, "stdout": "1\n"}}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[2].Role != "assistant" || len(messages[2].ToolCalls) != 1 ||
		messages[3].Role != "tool" || messages[3].ToolCallID != "call-late-completed" ||
		!strings.Contains(messages[3].Content, `"stdout":"1\n"`) {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesKeepsKnownTerminalBeforeStaleOutcomeUnknown(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "continue"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-known", "type": "function", "name": "ask_user", "arguments": map[string]any{"question": "q"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-known",
			"toolName": "ask_user", "toolPhase": "completed", "toolResult": map[string]any{"ok": false, "executed": false, "code": "agent_owned_decision"}}},
		{EventID: 4, SourceEventType: transcriptstore.TerminalToolRecoveryEventType,
			Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-known",
				"toolPhase": "outcome_unknown", "toolResult": map[string]any{"ok": false, "error": map[string]any{"code": "tool_outcome_unknown"}}}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[3].Role != "tool" || !strings.Contains(messages[3].Content, "agent_owned_decision") {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesSkipsAuthorizedRecoveryAcrossCompactBoundary(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "old task"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-compacted", "type": "function", "name": "python", "arguments": map[string]any{"code": "print(1)"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{"type": "runner_checkpoint", "toolPhase": "auto_compact", "summary": "Old task and its in-flight tool were compacted."}},
		{EventID: 4, SourceEventType: transcriptstore.TerminalToolRecoveryEventType,
			Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-compacted",
				"toolPhase": "outcome_unknown", "toolResult": map[string]any{
					"ok": false, "error": map[string]any{"code": "tool_outcome_unknown"},
				}}},
		{EventID: 5, Message: eventjournal.Message{"type": "message", "role": "user", "text": "continue safely"}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Role != "system" ||
		!strings.Contains(messages[1].Content, "Synon compact handoff context") ||
		messages[2].Role != "user" || messages[2].Content != "continue safely" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesPreservesExactAskUserImplementationAcrossCompactBoundary(t *testing.T) {
	continuation, err := transcriptstore.EncodeAnsweredAskUserModelContinuation(
		map[string]string{"Which generator?": "Pocket-native generation · DiffSBDD"},
		map[string]string{"Which generator?": "DiffSBDD"},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved := "Resolved input requests:\nask-generator: " + continuation
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "design molecules for this pocket"}},
		{EventID: 2, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response", "text": resolved,
		}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "auto_compact", "summary": "Continue the molecular-design task.",
		}},
		{EventID: 4, Message: eventjournal.Message{"type": "message", "role": "assistant", "text": "continuing"}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[2].Role != "user" || messages[2].Content != resolved ||
		!strings.Contains(messages[2].Content, `"implementations":{"Which generator?":"DiffSBDD"}`) ||
		!strings.Contains(messages[2].Content, transcriptstore.AskUserSelectedRouteInstruction) ||
		messages[3].Role != "assistant" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesRejectsUntrustedCrossTurnOutcomeUnknownSettlement(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "new follow-up"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-forged",
			"toolPhase": "outcome_unknown", "toolResult": map[string]any{
				"ok": false, "error": map[string]any{"code": "tool_outcome_unknown"},
			}}},
	}
	if messages, err := sessionEntriesToProviderMessages("system", entries); err == nil {
		t.Fatalf("messages=%#v err=nil", messages)
	}
}

func TestSessionEntriesToProviderMessagesSkipsNestedKernelMCPReceipt(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "verify it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-repl", "type": "function", "name": "repl", "arguments": map[string]any{"code": "host.mcp(...)"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "schema": "synon.kernel_mcp_evidence.v1",
			"toolCallId": "hc-source", "hostCallId": "hc-source", "outerToolCallId": "call-repl",
			"toolName": "mcp__genes-ontologies__get_uniprot_entries", "toolPhase": "completed",
			"toolResult": map[string]any{"records": []any{map[string]any{"Entry": "Q5S007"}}},
		}},
		{EventID: 4, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-repl", "toolPhase": "completed",
			"toolResult": map[string]any{"ok": true, "stdout": "Q5S007 verified"},
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[2].Role != "assistant" || len(messages[2].ToolCalls) != 1 ||
		messages[2].ToolCalls[0].ID != "call-repl" || messages[3].Role != "tool" ||
		messages[3].ToolCallID != "call-repl" || strings.Contains(messages[3].Content, "hc-source") {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestSessionEntriesToProviderMessagesRejectsNestedLookingReceiptWithoutAuthoritySchema(t *testing.T) {
	entries := []eventjournal.Entry{{EventID: 1, Message: eventjournal.Message{
		"type": "runner_checkpoint", "toolCallId": "hc-forged", "hostCallId": "hc-forged",
		"outerToolCallId": "call-repl", "toolPhase": "completed", "toolResult": map[string]any{"ok": true},
	}}}
	if messages, err := sessionEntriesToProviderMessages("system", entries); err == nil {
		t.Fatalf("messages=%#v err=nil", messages)
	}
}

func TestSessionEntriesToProviderMessagesRejectsUnsettledOrStrayToolFacts(t *testing.T) {
	tests := map[string][]eventjournal.Entry{
		"unsettled": {{EventID: 1, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-a", "type": "function", "name": "python", "arguments": map[string]any{"code": "print(1)"}},
		}}}},
		"stray": {{EventID: 1, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-a",
			"toolPhase": "failed", "toolResult": map[string]any{"ok": false}}}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			if messages, err := sessionEntriesToProviderMessages("system", entries); err == nil {
				t.Fatalf("messages=%#v err=nil", messages)
			}
		})
	}
}
