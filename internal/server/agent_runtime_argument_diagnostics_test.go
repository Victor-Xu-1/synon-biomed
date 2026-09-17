package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
)

func TestAgentRuntimeAdmissionDiagnosticExplainsEditFileSchemaFailure(t *testing.T) {
	schema := agentruntime.ToolSchema{
		Name: "edit_file",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []any{"path", "content"},
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
		},
	}
	gateway := serverAgentRuntimeToolGateway{
		server:          New(Options{FileRoot: t.TempDir()}),
		toolSchemas:     []agentruntime.ToolSchema{schema},
		toolValidators:  agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}),
		hasToolSnapshot: true,
	}
	call := agentruntime.ToolCall{Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"report.md"}`)}
	if gateway.AdmitsToolCall(call) {
		t.Fatal("invalid edit_file arguments were admitted")
	}
	diagnostic := gateway.ToolCallAdmissionDiagnostic(call)
	for _, want := range []string{"invalid_tool_arguments", "expectedArguments", "path", "content"} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("diagnostic %q does not contain %q", diagnostic, want)
		}
	}
}

func TestInvalidSingleOptionAskUserContinuesResolvedImplementationPrivately(t *testing.T) {
	schema := agentruntime.ToolSchema{
		Name: "ask_user",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []any{"header", "question", "options"},
			"properties": map[string]any{
				"header": map[string]any{"type": "string"}, "question": map[string]any{"type": "string"},
				"options": map[string]any{"type": "array"},
			},
		},
	}
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}},
		toolSchemas: []agentruntime.ToolSchema{schema}, toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}),
		hasToolSnapshot: true,
	}
	call := agentruntime.ToolCall{Name: "ask_user", Arguments: json.RawMessage(`{
		"implementation":"Engine A","label":"Continue Engine A","description":"Retry setup"
	}`)}
	diagnostic := gateway.ToolCallAdmissionDiagnostic(call)
	if !strings.Contains(diagnostic, "selected_implementation_already_resolved") ||
		!strings.Contains(diagnostic, "continue it is not a user-owned decision") {
		t.Fatalf("resolved implementation AskUser diagnostic=%q", diagnostic)
	}
}

func TestGeneratePlanContractPreflightKeepsIncompletePlanPrivate(t *testing.T) {
	if result := agentRuntimeGeneratePlanContractPreflight("generate_plan", map[string]any{
		"task_summary": "multi-stage scientific task",
	}); result == nil || result["status"] != "generate_plan_contract_preflight_required" {
		t.Fatalf("incomplete plan preflight=%#v", result)
	}
	completePlan := map[string]any{
		"task_summary": "multi-stage scientific task",
		"phases": []any{map[string]any{
			"name": "analysis", "delegations": []any{map[string]any{
				"name": "delivery", "steps": []any{map[string]any{
					"title": "Deliver result", "description": "Create the requested result.", "kind": "delivery",
				}},
			}},
		}},
		"feasibility": map[string]any{"confidence": "high", "rationale": "The local path is available."},
	}
	if result := agentRuntimeGeneratePlanContractPreflight("generate_plan", completePlan); result != nil {
		t.Fatalf("complete plan was blocked: %#v", result)
	}
	presentationSummary := copyMapAny(completePlan)
	delete(presentationSummary, "task_summary")
	presentationSummary["human_description"] = "multi-stage scientific task"
	if result := agentRuntimeGeneratePlanContractPreflight("generate_plan", presentationSummary); result != nil {
		t.Fatalf("normalizable presentation summary was blocked: %#v", result)
	}
	if result := agentRuntimeGeneratePlanContractPreflight("generate_plan", map[string]any{"approve": true}); result != nil {
		t.Fatalf("approval-only plan was blocked: %#v", result)
	}
}

type recordingManagedPythonSourcePreflighter struct {
	environment string
	source      string
	result      kernelruntime.ManagedPythonSourcePreflight
	err         error
}

func (p *recordingManagedPythonSourcePreflighter) PreflightManagedPythonSource(
	_ context.Context,
	environment string,
	source string,
) (kernelruntime.ManagedPythonSourcePreflight, error) {
	p.environment = environment
	p.source = source
	return p.result, p.err
}

func TestPythonEnvironmentAPIPreflightUsesSelectedRuntimeInsteadOfAnAPIBlacklist(t *testing.T) {
	verifier := &recordingManagedPythonSourcePreflighter{
		result: kernelruntime.ManagedPythonSourcePreflight{Missing: []string{"scipy.integrate.cumtrapz"}},
	}
	code := "from scipy.integrate import cumtrapz\narea = cumtrapz(y, x)"
	result := agentRuntimePythonEnvironmentAPIPreflight("python", map[string]any{
		"environment": "analysis", "code": code,
	}, verifier)
	if result == nil || result["status"] != "python_environment_api_preflight_required" ||
		!strings.Contains(stringValue(result["message"]), "scipy.integrate.cumtrapz") ||
		result["executed"] != false || verifier.environment != "analysis" || verifier.source != code {
		t.Fatalf("environment API preflight=%#v verifier=%#v", result, verifier)
	}
}

func TestPythonEnvironmentAPIPreflightFailsOpenWhenWitnessIsUnavailable(t *testing.T) {
	verifier := &recordingManagedPythonSourcePreflighter{err: errors.New("probe unavailable")}
	if result := agentRuntimePythonEnvironmentAPIPreflight("python", map[string]any{
		"environment": "analysis", "code": "import numpy as np\narea = np.trapz(y, x)",
	}, verifier); result != nil {
		t.Fatalf("unavailable environment witness blocked execution=%#v", result)
	}
}

func TestPythonEnvironmentAPIPreflightDoesNotBlockReplLocalModules(t *testing.T) {
	verifier := &recordingManagedPythonSourcePreflighter{
		result: kernelruntime.ManagedPythonSourcePreflight{Missing: []string{"PIL"}},
	}
	if result := agentRuntimePythonEnvironmentAPIPreflight("repl", map[string]any{
		"environment": "control", "code": "from PIL import Image",
	}, verifier); result != nil {
		t.Fatalf("repl local-module import was blocked by managed-environment preflight=%#v", result)
	}
	if verifier.environment != "" || verifier.source != "" {
		t.Fatalf("repl unexpectedly invoked managed-environment witness=%#v", verifier)
	}
}
