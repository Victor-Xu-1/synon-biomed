package server

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestArtifactTemplateValidationRejectsWholeFileScriptTemplate(t *testing.T) {
	for name, content := range map[string]string{
		"php": "<?php echo $content; ?>",
		"erb": "<%= report_body %>",
	} {
		t.Run(name, func(t *testing.T) {
			want := []string{"unresolved_template_marker:report.md"}
			if got := runnerCrossArtifactTemplateFailures("report.md", content); !reflect.DeepEqual(got, want) {
				t.Fatalf("template failures=%#v, want %#v", got, want)
			}
		})
	}
}

func TestAgentSaveArtifactsRejectsWholeFileScriptTemplateBeforeCanonicalWrite(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "<?php echo $content; ?>")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-script-template", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md"}, "language": "text",
		"human_description": "Saving a rendered report",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-script-template", input), fixture.identity, "save-script-template", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) {
		t.Fatalf("whole-file script template err=%v result=%#v", err, result)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 {
		t.Fatalf("whole-file script template failures=%#v", failures)
	}
	assertAgentSaveArtifactFailure(t, failures[0], "out/report.md", "unresolved_template_marker")
	assertAgentSaveArtifactsNoCanonicalWrites(t, fixture)
}

func TestUnresolvedTemplateSaveRequiresEditBeforeRepublish(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "prepare a rendered report"}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-template", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["report.md"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-template", Content: `{
		"ok":false,"code":"artifact_save_requires_correction",
		"errors":[{"code":"unresolved_template_marker","path":"report.md"}]
	}`}
	tools := []agentruntime.ToolSchema{{Name: "read_file"}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{saveCall, saveResult}, tools,
	).(map[string]any)
	if choice["name"] != "edit_file" {
		t.Fatalf("unresolved template correction choice=%#v, want edit_file", choice)
	}
}

func TestArtifactTemplateValidationAllowsRenderedReportWithCodeExample(t *testing.T) {
	content := "# Integration example\n\nThe rendered result is shown above.\n\n```php\n<?php echo $content; ?>\n```\n"
	if got := runnerCrossArtifactTemplateFailures("report.md", content); len(got) != 0 {
		t.Fatalf("embedded code example was mistaken for an unrendered whole-file template: %#v", got)
	}
}
