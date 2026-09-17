package server

import (
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestPythonSyntaxPreflightRejectsInvalidCellWithoutEchoingSource(t *testing.T) {
	value := agentRuntimePythonSyntaxPreflight("repl", map[string]any{
		"code": `secret_value = "must-not-be-echoed"
rows.append(f"{source},identifier}")`,
	})
	if value == nil || value["status"] != "python_syntax_preflight_required" || value["executed"] != false ||
		!strings.Contains(stringValue(value["message"]), "line 2") ||
		strings.Contains(stringValue(value["message"]), "must-not-be-echoed") {
		t.Fatalf("syntax preflight=%#v", value)
	}
	if allowed := agentRuntimePythonSyntaxPreflight("python", map[string]any{
		"code": `rows.append(f"{source},{identifier}")`,
	}); allowed != nil {
		t.Fatalf("valid standard Python was blocked: %#v", allowed)
	}
	if warning := agentRuntimePythonSyntaxPreflight("python", map[string]any{
		"code": `year = 2016(近五年有后续研究)`,
	}); warning == nil || !strings.Contains(stringValue(warning["message"]), "not callable") {
		t.Fatalf("deterministic literal-call warning was not rejected before execution: %#v", warning)
	}
	if shell := agentRuntimePythonSyntaxPreflight("bash", map[string]any{"code": `if (`}); shell != nil {
		t.Fatalf("non-Python tool was parsed as Python: %#v", shell)
	}
}

func TestGatewayPrivatelyPreflightsPythonSyntaxBeforeToolStart(t *testing.T) {
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: &sessionRunnerChatRun{}, allowedTools: []string{"repl"},
		toolSchemas:     []agentruntime.ToolSchema{{Name: "repl", Parameters: agentKernelReplToolSchema().Parameters}},
		hasToolSnapshot: true,
	}
	call := agentruntime.ToolCall{ID: "syntax", Name: "repl", Arguments: json.RawMessage(
		`{"code":"rows.append(f\"{source},identifier}\")","human_description":"Writing evidence"}`,
	)}
	diagnostic := gateway.ToolCallPreflightDiagnostic(call)
	if !strings.Contains(diagnostic, "python_syntax_preflight_required") || !strings.Contains(diagnostic, "syntax") {
		t.Fatalf("gateway syntax diagnostic=%s", diagnostic)
	}
}

func TestGatewayStatelessPreflightsRemainActiveWithoutTaskRun(t *testing.T) {
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, allowedTools: []string{"python", "repl"},
	}

	invalidPython := agentruntime.ToolCall{
		Name: "python", Arguments: json.RawMessage(`{"code":"print(f\"broken}\")"}`),
	}
	if diagnostic := gateway.ToolCallPreflightDiagnostic(invalidPython); !strings.Contains(diagnostic, "python_syntax_preflight_required") {
		t.Fatalf("recovery gateway omitted syntax preflight: %q", diagnostic)
	}

	thirdPartyREPL := agentruntime.ToolCall{
		Name: "repl", Arguments: json.RawMessage(`{"code":"import pandas as pd\nprint(pd.DataFrame())"}`),
	}
	if diagnostic := gateway.ToolCallPreflightDiagnostic(thirdPartyREPL); !strings.Contains(diagnostic, "code_preflight_required") || !strings.Contains(diagnostic, "verified environment") {
		t.Fatalf("recovery gateway omitted REPL runtime preflight: %q", diagnostic)
	}
}

func TestGatewayPrivatelyRejectsOptionalFormatterBeforeKernelLifecycle(t *testing.T) {
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, allowedTools: []string{"python"},
	}
	call := agentruntime.ToolCall{
		Name: "python", Arguments: json.RawMessage(`{"code":"print(frame.to_markdown())"}`),
	}
	diagnostic := gateway.ToolCallPreflightDiagnostic(call)
	if !strings.Contains(diagnostic, "code_preflight_required") ||
		!strings.Contains(diagnostic, "optional pandas tabulate formatter") {
		t.Fatalf("gateway optional-formatter diagnostic=%q", diagnostic)
	}
}

func TestReviewerLetsThirdPartyImportReachBoundedRuntimeCorrection(t *testing.T) {
	schema := sessionReviewerReplToolSchema()
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: &sessionRunnerChatRun{}, allowedTools: []string{"repl"},
		toolSchemas: []agentruntime.ToolSchema{schema}, hasToolSnapshot: true,
		reviewerEvidence: &sessionReviewerEvidenceScope{},
	}
	call := agentruntime.ToolCall{
		Name: "repl", Arguments: json.RawMessage(`{"code":"import pandas as pd\nprint(pd.DataFrame())","human_description":"Checking evidence"}`),
	}
	if diagnostic := gateway.ToolCallPreflightDiagnostic(call); diagnostic != "" {
		t.Fatalf("reviewer third-party import was terminated before bounded runtime correction: %q", diagnostic)
	}
}
