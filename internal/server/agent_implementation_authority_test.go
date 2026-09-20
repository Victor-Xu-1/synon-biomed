package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"synon-go/internal/agentruntime"
	"testing"
)

func TestImplementationAuthorityDoesNotInterpretReportProse(t *testing.T) {
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"engine-a"}}
	gateway := serverAgentRuntimeToolGateway{taskRun: run, allowedTools: []string{"edit_file"}}
	for _, prose := range []string{"当前方案的风险见附件。", "Current implementation risks are documented in the appendix.", "## Selected implementation\nengine-b\n"} {
		args, _ := json.Marshal(map[string]any{"file_path": "report.md", "old_string": "", "new_string": prose, "human_description": "Update report"})
		if diagnostic := gateway.toolCallPreflightDiagnostic(context.Background(), agentruntime.ToolCall{ID: "report-edit", Name: "edit_file", Arguments: args}); strings.Contains(diagnostic, "selected_implementation_declaration_mismatch") {
			t.Fatalf("prose interpreted as an authority update: %s", diagnostic)
		}
		if got := run.selectedImplementationsSnapshot(); !reflect.DeepEqual(got, []string{"engine-a"}) {
			t.Fatalf("prose changed selection: %v", got)
		}
	}
	// Removing prose inference must not release the actual execution boundary.
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	if result, blocked := managedEnvironmentImplementationDecision(ctx, "engine-b", true); !blocked || result["status"] != "selected_implementation_mismatch" {
		t.Fatalf("unselected execution allowed: %v", result)
	}
	if result, blocked := managedEnvironmentImplementationDecision(ctx, "engine-a", true); blocked || result != nil {
		t.Fatalf("selected execution blocked: %v", result)
	}
}

func TestSaveArtifactsPreservesProseWithoutChangingImplementationAuthority(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "report.md", "当前方案的风险见附件。\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "report-write", 1, write)
	input := map[string]any{"files": []any{"report.md"}, "language": "text", "human_description": "Save report"}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}, SelectedImplementations: []string{"engine-a"}}
	ctx := withTranscriptRunnerChatRun(fixture.toolContext(t, "save-report", input), run)
	result, err := fixture.server.executeAgentSaveArtifacts(ctx, fixture.identity, "save-report", input)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 {
		t.Fatalf("ordinary report save failed: %v %#v", err, result)
	}
	if got := run.selectedImplementationsSnapshot(); !reflect.DeepEqual(got, []string{"engine-a"}) {
		t.Fatalf("saved report altered selection: %v", got)
	}
}
