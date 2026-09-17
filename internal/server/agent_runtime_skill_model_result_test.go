package server

import (
	"strings"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
)

func TestAgentRuntimeSkillModelResultUsesOneReadableContract(t *testing.T) {
	result := map[string]any{
		"prompt": "Default tested execution route:\nRun the prepared asset.",
		"skill": map[string]any{
			"name": "analysis-workflow", "description": "  Analyze   data safely. ",
			"body": "Default tested execution route:\nRun the prepared asset.",
		},
		"data": map[string]any{"preferredExecutionAssets": []string{"scripts/run.py"}},
	}
	projected, ok := agentRuntimeSkillModelResult(result).(string)
	if !ok {
		t.Fatalf("projected result type=%T", agentRuntimeSkillModelResult(result))
	}
	if strings.Count(projected, "Default tested execution route") != 1 ||
		!strings.HasPrefix(projected, `<skill-metadata name="analysis-workflow" description="Analyze data safely." />`) {
		t.Fatalf("projected result=%q", projected)
	}
	if strings.Count(projected, "This capability is now active for the current task") != 1 ||
		!strings.HasSuffix(projected, "do not reload the same capability unless its arguments or the task change.") {
		t.Fatalf("projected result is missing the bounded load receipt=%q", projected)
	}
	if strings.Contains(projected, "preferredExecutionAssets") || strings.Contains(projected, `"body"`) {
		t.Fatalf("projected result retained duplicate JSON=%q", projected)
	}
}

func TestProviderVisibleSkillResultsBecomeReceiptsForSystemContext(t *testing.T) {
	messages := []chatCompletionMessage{
		{Role: "system", Content: "system"},
		{Role: "tool", Content: `<skill-metadata name="analysis-workflow" description="Analyze safely" />` + "\n\nCanonical guidance"},
		{Role: "tool", Content: "ordinary result"},
	}
	compacted := compactProviderVisibleSkillResultBodies(messages)
	if strings.Contains(compacted[1].Content, "Canonical guidance") ||
		!strings.Contains(compacted[1].Content, "supplied once in system context") {
		t.Fatalf("skill result was not compacted to a receipt: %q", compacted[1].Content)
	}
	if compacted[2].Content != "ordinary result" || messages[1].Content == compacted[1].Content {
		t.Fatalf("compaction mutated unrelated or source messages: source=%q compacted=%#v", messages[1].Content, compacted)
	}
	if visible := providerVisibleSkillResultNames(compacted); len(visible) != 1 {
		t.Fatalf("receipt lost current Skill identity=%#v", visible)
	}
}

func TestProviderVisibleSkillNamesRestoreContinuationAuthority(t *testing.T) {
	messages := []chatCompletionMessage{
		{Role: "tool", Content: `<skill-metadata name="single-cell-rna-analysis" description="analysis" />` + "\n\ncontract"},
		{Role: "tool", Content: `<skill-metadata name="document-workbench" />` + "\n\ncontract"},
	}
	if got := strings.Join(providerVisibleSkillNames(messages), ","); got != "document-workbench,single-cell-rna-analysis" {
		t.Fatalf("visible Skill names = %q", got)
	}
}

func TestSessionEntriesToProviderMessagesReplacesStaleReadableSkillBodyWithIdentityReceipt(t *testing.T) {
	const stalePath = "/workspace/.synon/runtime/skills/analysis-workflow/old/scripts/run.py"
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "load it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-skill", "type": "function", "name": "skill", "arguments": map[string]any{"skill": "analysis-workflow"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{"type": "runner_checkpoint", "toolCallId": "call-skill",
			"toolName": "skill", "toolPhase": "completed",
			"toolResult": `<skill-metadata name="analysis-workflow" />` + "\n\nRun " + stalePath}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	got := messages[len(messages)-1].Content
	if !strings.HasPrefix(got, `<skill-metadata replay="legacy" name="analysis-workflow" />`) ||
		strings.Contains(got, stalePath) || strings.Contains(got, "Run ") {
		t.Fatalf("stale readable Skill result was replayed as authority: %q", got)
	}
	if visible := providerVisibleSkillResultNames(messages); len(visible) != 0 {
		t.Fatalf("legacy readable Skill body incorrectly suppressed current contract: %#v", visible)
	}
}

func TestSessionEntriesToProviderMessagesCompactsLegacySkillPayload(t *testing.T) {
	prompt := "Default tested execution route:\nRun the prepared asset."
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "load it"}},
		{EventID: 2, Message: eventjournal.Message{"type": "runner_checkpoint", "modelToolCalls": []any{
			map[string]any{"id": "call-skill", "type": "function", "name": "skill", "arguments": map[string]any{"skill": "analysis-workflow"}},
		}}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolCallId": "call-skill", "toolName": "skill", "toolPhase": "completed",
			"toolResult": map[string]any{
				"prompt": prompt,
				"skill":  map[string]any{"name": "analysis-workflow", "description": "Analyze safely", "body": prompt},
			},
		}},
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	got := messages[len(messages)-1].Content
	if strings.Contains(got, "Default tested execution route") ||
		!strings.HasPrefix(got, `<skill-metadata replay="legacy" name="analysis-workflow"`) || strings.Contains(got, `"body"`) {
		t.Fatalf("legacy Skill payload was not compacted: %q", got)
	}
	if visible := providerVisibleSkillResultNames(messages); len(visible) != 0 {
		t.Fatalf("legacy Skill body incorrectly suppressed current contract: %#v", visible)
	}
}
