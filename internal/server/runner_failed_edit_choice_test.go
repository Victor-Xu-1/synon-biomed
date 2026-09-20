package server

import (
	"encoding/json"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestCorrectionFailedNativeEditAllowsAlternateToolStrategy(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "Repair and save the output"}
	tools := []agentruntime.ToolSchema{{Name: "edit_file"}, {Name: "read_file"}, {Name: "web_fetch"}, {Name: "bash"}, {Name: "save_artifacts"}}
	for _, code := range []string{"invalid_file_structure", "file_content_type_mismatch", "edit_conflict"} {
		t.Run(code, func(t *testing.T) {
			messages := []agentruntime.Message{
				{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker},
				runnerEvidenceDepthCall("save-bad", "save_artifacts", map[string]any{"files": []string{"input.pdb"}}),
				{Role: "tool", ToolCallID: "save-bad", Content: `{"ok":false,"code":"artifact_save_requires_correction","errors":[{"path":"input.pdb","code":"unresolved_template_marker"}]}`},
			}
			before, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
			if before["name"] != "edit_file" {
				t.Fatalf("fixture did not require edit: %#v", before)
			}
			messages = append(messages,
				agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{correctionEditCall("edit-bad", "input.pdb", "{% include https://example.test/input.pdb %}")}},
				agentruntime.Message{Role: "tool", ToolCallID: "edit-bad", Content: `{"ok":false,"executed":false,"code":"` + code + `"}`},
			)
			gateway := serverAgentRuntimeToolGateway{taskRun: run}
			if choice := gateway.RequiredToolChoice(messages, tools); choice != "required" {
				t.Fatalf("native rejection still forces the failed edit strategy: %#v", choice)
			}
			messages = append(messages,
				runnerEvidenceDepthCall("read-input", "read_file", map[string]any{"file_path": "input.pdb"}),
				agentruntime.Message{Role: "tool", ToolCallID: "read-input", Content: `{"ok":true,"content":"existing file"}`},
				agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{correctionEditCall("edit-other", "other.txt", "unrelated")}},
				agentruntime.Message{Role: "tool", ToolCallID: "edit-other", Content: `{"ok":true,"changed":true}`},
			)
			if choice := gateway.RequiredToolChoice(messages, tools); choice != "required" {
				t.Fatalf("inspection or unrelated mutation re-pinned failed edit: %#v", choice)
			}
			if !sessionRunnerImmediateArtifactRepairRequired(run, messages) {
				t.Fatal("read incorrectly discharged repair")
			}
			messages = append(messages,
				agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{correctionEditCall("edit-fixed", "input.pdb", "valid bytes")}},
				agentruntime.Message{Role: "tool", ToolCallID: "edit-fixed", Content: `{"ok":true,"changed":true}`},
			)
			choice, _ := gateway.RequiredToolChoice(messages, tools).(map[string]any)
			if choice["name"] != "save_artifacts" {
				t.Fatalf("real mutation did not advance to publication: %#v", choice)
			}
		})
	}
}

func TestCorrectionFailedEditChoiceKeepsScopeAndOtherRequiredTools(t *testing.T) {
	tools := []agentruntime.ToolSchema{{Name: "edit_file"}, {Name: "skill"}, {Name: "save_artifacts"}}
	editChoice := map[string]any{"type": "tool", "name": "edit_file"}
	messages := []agentruntime.Message{
		{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{correctionEditCall("failed", "input.pdb", "invalid")}},
		{Role: "tool", ToolCallID: "failed", Content: `{"ok":false,"executed":false,"code":"invalid_file_structure"}`},
	}
	if runnerCorrectionFailedEditChoice(editChoice, messages, tools) != "required" {
		t.Fatal("failed edit did not release named selection")
	}
	for _, choice := range []any{nil, "none", "required", map[string]any{"type": "tool", "name": "skill"}, map[string]any{"type": "tool", "name": "save_artifacts"}} {
		before, _ := json.Marshal(choice)
		after, _ := json.Marshal(runnerCorrectionFailedEditChoice(choice, messages, tools))
		if string(before) != string(after) {
			t.Fatalf("unrelated requirement changed: %s => %s", before, after)
		}
	}
	messages = append(messages, agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker})
	if got := mapValue(runnerCorrectionFailedEditChoice(editChoice, messages, tools)); got["name"] != "edit_file" {
		t.Fatalf("previous repair scope leaked into the new correction: %#v", got)
	}
}
