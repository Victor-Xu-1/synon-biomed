package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/skills"
)

func TestFreshAskUserIsNotClassifiedByQuestionWording(t *testing.T) {
	gateway := serverAgentRuntimeToolGateway{
		server:       &Server{},
		allowedTools: []string{"ask_user"},
	}
	call := agentruntime.ToolCall{
		ID:   "environment-choice",
		Name: "ask_user",
		Arguments: json.RawMessage(`{
			"header":"Environment choice",
			"question":"The current environment is unavailable. Which recovery tradeoff do you prefer?",
			"options":[
				{"label":"Reuse an existing environment","description":"Use the existing environment if verification succeeds.","pros":"Avoids new setup work.","cons":"Shares the existing dependency set.","readiness":"Compatibility has not been checked in this task.","readiness_status":"unverified","decision_evidence":["user-input:current-task"],"readiness_evidence":[],"selection_basis":"user_objective","expected_outcome":"Execution continues in the existing environment if compatible.","selection_rationale":"Recommended only when continuity is the user-owned priority.","recommended":true},
				{"label":"Create a dedicated environment","description":"Provision an isolated environment for this task.","pros":"Isolates task dependencies.","cons":"Requires setup and storage.","readiness":"Package availability has not been checked in this task.","readiness_status":"unverified","decision_evidence":["user-input:current-task"],"readiness_evidence":[],"selection_basis":"user_objective","expected_outcome":"Execution continues in an isolated environment if provisionable.","selection_rationale":"Choose when dependency isolation materially changes reliability.","recommended":false}
			]
		}`),
	}
	if diagnostic := gateway.toolCallPreflightDiagnostic(context.Background(), call); diagnostic != "" {
		t.Fatalf("fresh AskUser was rejected by wording instead of typed task state: %q", diagnostic)
	}
}

func TestComputeQuestionCannotSwitchScientificImplementation(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "engine-b-runtime", ImplementationIdentities: []string{"Engine B"},
	})
	schemas := agentComputeToolSchemas()
	gateway := serverAgentRuntimeToolGateway{
		server:       &Server{skillCatalog: catalog},
		taskRun:      &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}},
		allowedTools: []string{askAboutComputeToolName}, toolSchemas: schemas,
		toolValidators: agentRuntimeToolValidators(schemas),
	}
	call := agentruntime.ToolCall{
		ID: "wrong-implementation-compute-question", Name: askAboutComputeToolName,
		Arguments: json.RawMessage(`{
			"provider":"local",
			"question":"Should execution switch to Engine B?",
			"human_description":"Choose Engine B instead"
		}`),
	}
	diagnostic := gateway.toolCallPreflightDiagnostic(context.Background(), call)
	if !strings.Contains(diagnostic, "selected_implementation_recovery_required") ||
		!strings.Contains(diagnostic, "cannot be used to switch") {
		t.Fatalf("compute implementation switch diagnostic=%q", diagnostic)
	}
}
