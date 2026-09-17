package registry

import (
	"fmt"
	"strings"
	"testing"
)

func TestScientificHarnessAdvertisesCanonicalInteractiveAndSkillContracts(t *testing.T) {
	reg := Default()

	ask, ok := reg.Get("ask_user")
	if !ok {
		t.Fatal("ask_user is not registered")
	}
	for _, field := range []string{"question", "header", "options", "multi_select"} {
		if _, found := ask.Input[field]; !found {
			t.Fatalf("ask_user is missing %q: %#v", field, ask.Input)
		}
	}
	if _, found := ask.Input["questions"]; found {
		t.Fatalf("ask_user still advertises the retired questions wrapper: %#v", ask.Input)
	}
	if err := reg.Validate("ask_user", map[string]any{
		"question": "Which result should be the primary deliverable?",
		"header":   "Deliverable",
		"options": []any{
			map[string]any{
				"label": "Report", "description": "Deliver a reviewed report.",
				"pros": "Easy to review", "cons": "Less reusable for calculation",
				"readiness": "Execution readiness is not established by the analysis receipt.", "readiness_status": "unverified",
				"decision_evidence":   []any{"tool-call:analysis-1"},
				"readiness_evidence":  []any{},
				"selection_basis":     "scientific_evidence",
				"expected_outcome":    "A review-ready Markdown report.",
				"selection_rationale": "Recommended because the decision audience requested a narrative.",
				"recommended":         true, "metadata": map[string]any{"kind": "report"},
			},
			map[string]any{
				"label": "Dataset", "description": "Deliver the normalized evidence table.",
				"pros": "Reusable for downstream analysis", "cons": "Needs a separate narrative",
				"readiness": "Execution readiness is not established by the analysis receipt.", "readiness_status": "unverified",
				"decision_evidence":   []any{"tool-call:analysis-1"},
				"readiness_evidence":  []any{},
				"selection_basis":     "scientific_evidence",
				"expected_outcome":    "An editable CSV evidence table.",
				"selection_rationale": "Choose when machine-readable reuse is more important than narrative review.",
				"recommended":         false,
			},
		},
		"multi_select": false,
	}); err != nil {
		t.Fatalf("canonical ask_user input failed: %v", err)
	}
	optionsSchema, _ := ask.Input["options"].Schema["items"].(map[string]any)
	properties, _ := optionsSchema["properties"].(map[string]any)
	requiredFields, _ := optionsSchema["required"].([]string)
	required := make(map[string]bool, len(requiredFields))
	for _, field := range requiredFields {
		required[field] = true
	}
	for _, field := range []string{
		"description", "pros", "cons", "readiness", "readiness_status", "decision_evidence", "readiness_evidence", "selection_basis",
		"expected_outcome", "selection_rationale", "recommended",
	} {
		if _, found := properties[field]; !found {
			t.Errorf("ask_user option schema is missing %q", field)
		}
		if !required[field] {
			t.Errorf("ask_user option schema does not require %q", field)
		}
	}
	readinessSchema, _ := properties["readiness"].(map[string]any)
	labelSchema, _ := properties["label"].(map[string]any)
	descriptionSchema, _ := properties["description"].(map[string]any)
	decisionEvidenceSchema, _ := properties["decision_evidence"].(map[string]any)
	evidenceSchema, _ := properties["readiness_evidence"].(map[string]any)
	resourcesSchema, _ := properties["resources"].(map[string]any)
	resourceProperties, _ := resourcesSchema["properties"].(map[string]any)
	implementationSchema, _ := properties["implementation"].(map[string]any)
	executionParametersSchema, _ := properties["execution_parameter_values"].(map[string]any)
	rationaleSchema, _ := properties["selection_rationale"].(map[string]any)
	if !strings.Contains(fmt.Sprint(readinessSchema["description"]), "service generates the user-visible readiness statement") ||
		!strings.Contains(fmt.Sprint(labelSchema["description"]), "concrete checked or provisionable engine/service") ||
		!strings.Contains(fmt.Sprint(descriptionSchema["description"]), "scientific method") ||
		!strings.Contains(fmt.Sprint(decisionEvidenceSchema["description"]), "user-input:current-task") ||
		!strings.Contains(fmt.Sprint(evidenceSchema["description"]), "typed readiness authority") ||
		!strings.Contains(fmt.Sprint(resourcesSchema["description"]), "CPU, memory, and GPU/VRAM") ||
		len(resourceProperties) != 3 || resourceProperties["cpu"] == nil ||
		resourceProperties["memory"] == nil || resourceProperties["gpu"] == nil ||
		resourcesSchema["additionalProperties"] != false || required["resources"] ||
		!strings.Contains(fmt.Sprint(implementationSchema["description"]), "Exact public engine") || required["implementation"] ||
		!strings.Contains(fmt.Sprint(executionParametersSchema["description"]), "exact evidence_group") || required["execution_parameter_values"] ||
		!strings.Contains(fmt.Sprint(rationaleSchema["description"]), "without enlarging its scope") {
		t.Fatalf("ask_user decision guidance is incomplete: label=%#v description=%#v readiness=%#v evidence=%#v resources=%#v rationale=%#v", labelSchema, descriptionSchema, readinessSchema, evidenceSchema, resourcesSchema, rationaleSchema)
	}
	plan, ok := reg.Get("generate_plan")
	if !ok {
		t.Fatal("generate_plan is not registered")
	}
	for _, field := range []string{"task_summary", "phases", "desired_outputs", "feasibility", "approve"} {
		if _, found := plan.Input[field]; !found {
			t.Fatalf("generate_plan is missing %q: %#v", field, plan.Input)
		}
	}
	if _, found := plan.Input["steps"]; found {
		t.Fatalf("generate_plan still advertises the retired flat steps contract: %#v", plan.Input)
	}
	for _, marker := range []string{
		"durable working plan",
		"ordered control state",
		"one substantive output module or decision question",
		"source discovery and extraction happen inside that module",
		"pauses only in explicit plan-review mode",
	} {
		if !strings.Contains(plan.Description, marker) {
			t.Fatalf("generate_plan description missing %q: %s", marker, plan.Description)
		}
	}
	if err := reg.Validate("generate_plan", map[string]any{"approve": true}); err != nil {
		t.Fatalf("generate_plan approve-only input failed: %v", err)
	}

	status, ok := reg.Get("update_step_status")
	if !ok {
		t.Fatal("update_step_status is not registered")
	}
	statusEnum, _ := status.Input["status"].Schema["enum"].([]string)
	wantStatuses := []string{"in_progress", "completed", "blocked", "skipped"}
	if len(statusEnum) != len(wantStatuses) {
		t.Fatalf("update_step_status enum=%v want=%v", statusEnum, wantStatuses)
	}
	for index := range wantStatuses {
		if statusEnum[index] != wantStatuses[index] {
			t.Fatalf("update_step_status enum=%v want=%v", statusEnum, wantStatuses)
		}
	}

	for _, canonical := range []string{"search_skills", "skill"} {
		if _, found := reg.Get(canonical); !found {
			t.Fatalf("canonical Skill tool %q is not registered", canonical)
		}
	}
	for _, legacy := range []string{"SkillSearch", "Skill"} {
		for _, advertised := range reg.Names() {
			if advertised == legacy {
				t.Fatalf("legacy Skill alias %q is still model-visible", legacy)
			}
		}
	}
}
