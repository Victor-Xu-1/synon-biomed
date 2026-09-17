package server

import (
	"testing"

	"synon-go/internal/tools/registry"
)

func TestSynonPlanContractHasOneRuntimeAuthority(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	for _, retired := range []string{"EnterPlanMode", "ExitPlanMode"} {
		if _, found := server.tools.Get(retired); found {
			t.Fatalf("retired plan tool %s remains registered", retired)
		}
	}
	tool, found := server.tools.Get("generate_plan")
	if !found {
		t.Fatal("generate_plan tool is not registered")
	}
	assertGeneratePlanToolSchema(t, tool)
	properties := mapValue(chatToolParameters(tool)["properties"])
	for _, runtimeOwned := range []string{"session_id", "frame_id", "tool_call_id", "version", "phase_id", "delegation_id", "step_id"} {
		if _, exposed := properties[runtimeOwned]; exposed {
			t.Fatalf("generate_plan exposed runtime-owned field %s: %#v", runtimeOwned, properties)
		}
	}
}

func assertGeneratePlanToolSchema(t *testing.T, tool registry.Tool) {
	t.Helper()
	for _, field := range []string{"human_description", "task_summary", "phases", "desired_outputs", "feasibility", "approve"} {
		definition, found := tool.Input[field]
		if !found || definition.Required {
			t.Fatalf("generate_plan.%s must exist and remain optional for approve-only calls: %#v", field, tool.Input)
		}
	}
	phaseItems := mapValue(tool.Input["phases"].Schema["items"])
	phaseProperties := mapValue(phaseItems["properties"])
	delegations := mapValue(phaseProperties["delegations"])
	delegationItems := mapValue(delegations["items"])
	delegationProperties := mapValue(delegationItems["properties"])
	steps := mapValue(delegationProperties["steps"])
	stepItems := mapValue(steps["items"])
	stepProperties := mapValue(stepItems["properties"])
	if phaseItems["type"] != "object" || numberValue(tool.Input["phases"].Schema["maxItems"]) != maxGeneratedPlanPhases ||
		numberValue(delegations["maxItems"]) != maxGeneratedDelegations || numberValue(steps["maxItems"]) != maxGeneratedPlanSteps ||
		stepItems["type"] != "object" {
		t.Fatalf("generate_plan nested phases schema = %#v", tool.Input["phases"].Schema)
	}
	required := stringValueSlice(stepItems["required"])
	kind := mapValue(stepProperties["kind"])
	if !stringSliceContains(required, "kind") || len(anySliceValue(kind["enum"])) != 4 {
		t.Fatalf("generate_plan step kind must distinguish research, synthesis, and delivery: %#v", stepItems)
	}
	for _, field := range []string{"output_module", "research_question", "research_depth", "discovery_queries"} {
		if stepProperties[field] == nil {
			t.Fatalf("generate_plan research step omits %s: %#v", field, stepItems)
		}
	}
	if queries := mapValue(stepProperties["discovery_queries"]); numberValue(queries["minItems"]) != 0 {
		t.Fatalf("optional discovery_queries rejects the model-equivalent empty array: %#v", queries)
	}
	if tool.Input["feasibility"].Type != "object" {
		t.Fatalf("generate_plan feasibility schema = %#v", tool.Input["feasibility"])
	}
	progress, found := New(Options{FileRoot: t.TempDir()}).tools.Get(updateStepStatusToolName)
	if !found || !progress.Input["step"].Required || !progress.Input["status"].Required {
		t.Fatalf("update_step_status schema = %#v", progress.Input)
	}
	for _, field := range []string{"notes", "observations", "source_refs", "follow_ups"} {
		definition, present := progress.Input[field]
		if !present || definition.Required {
			t.Fatalf("update_step_status.%s must be optional durable navigation data: %#v", field, progress.Input)
		}
	}
}
