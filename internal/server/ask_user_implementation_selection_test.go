package server

import (
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestAskUserImplementationSelectionRequiresDecisionReadyOptions(t *testing.T) {
	run := &sessionRunnerChatRun{ImplementationSelectionRequired: true}
	incomplete := map[string]any{"questions": []askUserQuestion{{
		Question: "Which engine?",
		Options: []askUserQuestionOption{
			{Label: "Engine A", Metadata: map[string]any{"implementation": "Engine A"}},
			{Label: "Engine B", Metadata: map[string]any{}},
		},
	}}}
	correction := askUserImplementationSelectionContractCorrection(run, incomplete)
	if stringValue(correction["status"]) != "implementation_decision_contract_incomplete" {
		t.Fatalf("correction=%#v", correction)
	}

	resourceProfile := func() map[string]any {
		return map[string]any{"cpu": "4 cores", "memory": "8 GB", "gpu": "remote"}
	}
	complete := map[string]any{"questions": []askUserQuestion{{
		Question: "Which engine?",
		Options: []askUserQuestionOption{
			{Label: "Engine A", Metadata: map[string]any{"implementation": "Engine A", "resources": resourceProfile(), "decision_evidence": []string{"tool-call:preflight-a"}}},
			{Label: "Engine B", Metadata: map[string]any{"implementation": "Engine B", "resources": resourceProfile(), "decision_evidence": []string{"tool-call:preflight-b"}}},
			{Label: "Engine C", Metadata: map[string]any{"implementation": "Engine C", "resources": resourceProfile(), "decision_evidence": []string{"tool-call:preflight-c"}}},
		},
	}}}
	if correction := askUserImplementationSelectionContractCorrection(run, complete); correction != nil {
		t.Fatalf("complete decision was blocked: %#v", correction)
	}
	complete["questions"] = []askUserQuestion{{
		Question: "Which recovery engine?",
		Options: []askUserQuestionOption{
			{Label: "Engine A", Metadata: map[string]any{"implementation": "Engine A", "resources": resourceProfile(), "decision_evidence": []string{"tool-call:preflight-a"}}},
			{Label: "Engine B", Metadata: map[string]any{"implementation": "Engine B", "resources": resourceProfile(), "decision_evidence": []string{"tool-call:preflight-b"}}},
		},
	}}
	if correction := askUserImplementationSelectionContractCorrection(run, complete); correction != nil {
		t.Fatalf("two verified implementation choices required an invented third option: %#v", correction)
	}
}

func TestAskUserPendingImplementationDoesNotBlockUnrelatedDecision(t *testing.T) {
	run := &sessionRunnerChatRun{ImplementationSelectionRequired: true}
	run.addRequiredScientificCapabilities("structure-analysis")
	result := map[string]any{"questions": []askUserQuestion{{
		Question: "Which input should be analyzed?",
		Options:  []askUserQuestionOption{{Label: "Input A"}, {Label: "Input B"}},
	}}}
	if correction := askUserImplementationSelectionContractCorrection(run, result); correction != nil {
		t.Fatalf("unrelated input choice inherited the implementation gate: %#v", correction)
	}
	if !run.implementationSelectionRequiredSnapshot() || len(run.selectedImplementationsSnapshot()) != 0 {
		t.Fatal("input decision changed implementation authorization")
	}
}

func TestAskUserImplementationValidationIsQuestionScoped(t *testing.T) {
	run := &sessionRunnerChatRun{ImplementationSelectionRequired: true}
	result := map[string]any{"questions": []askUserQuestion{
		{Question: "Which input?", Options: []askUserQuestionOption{{Label: "Input A"}, {Label: "Input B"}}},
		{Question: "Which engine?", Options: []askUserQuestionOption{
			{Label: "Engine A", Metadata: map[string]any{"implementation": "Engine A"}},
			{Label: "Engine B"},
		}},
	}}
	correction := askUserImplementationSelectionContractCorrection(run, result)
	issues := strings.Join(stringArrayValue(correction["issues"]), "\n")
	if !strings.Contains(issues, "question 2 option 2 has no exact implementation") || strings.Contains(issues, "question 1") {
		t.Fatalf("decision scope leaked or incomplete engine option escaped validation: %#v", correction)
	}
}

func TestAskUserSelectedImplementationParameterChoiceDoesNotReopenEngineDecision(t *testing.T) {
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"AutoDock Vina"}}
	resources := map[string]any{"cpu": "1-16 cores", "memory": "1-8 GB", "gpu": "not required"}
	result := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{
		{Label: "Use P2Rank detection", Metadata: map[string]any{"evidence_resolver": map[string]any{
			"evidence_group": "binding-site-center", "skill": "pocket-skill", "implementation": "P2Rank",
		}}},
		{Label: "Use the current box", Metadata: map[string]any{"implementation": "AutoDock Vina", "resources": resources}},
		{Label: "Provide another box", Metadata: map[string]any{"implementation": "AutoDock Vina", "resources": resources}},
	}}}}
	if correction := askUserImplementationSelectionContractCorrection(run, result); correction != nil {
		t.Fatalf("parameter choice reopened the primary engine decision: %#v", correction)
	}
}

func TestAskUserImplementationCapabilityRejectsScientificallyWeakerFiller(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "pocket-engine", ImplementationIdentities: []string{"Pocket Engine"},
		RequiredCapabilities: []string{"pocket-conditioned-molecule-generation", "gpu"},
	})
	catalog.AddSkill(skills.Skill{
		Name: "analog-engine", ImplementationIdentities: []string{"Analog Engine"},
		RequiredCapabilities: []string{"ligand-analog-enumeration"},
	})
	run := &sessionRunnerChatRun{}
	run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
	result := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{
		{Metadata: map[string]any{"implementation": "Pocket Engine"}},
		{Metadata: map[string]any{"implementation": "Analog Engine"}},
	}}}}
	correction := askUserImplementationCapabilityContractCorrection(catalog, nil, run, result)
	if stringValue(correction["status"]) != "implementation_decision_contract_incomplete" ||
		!strings.Contains(strings.Join(stringArrayValue(correction["issues"]), " "), "pocket-conditioned-molecule-generation") {
		t.Fatalf("scientifically weaker filler was accepted: %#v", correction)
	}
	result["questions"] = []askUserQuestion{{Options: []askUserQuestionOption{
		{Metadata: map[string]any{"implementation": "Pocket Engine"}},
	}}}
	if correction := askUserImplementationCapabilityContractCorrection(catalog, nil, run, result); correction != nil {
		t.Fatalf("matching implementation capability was rejected: %#v", correction)
	}
}

func TestAskUserImplementationSelectionAppliesBeforeEnvironmentMutation(t *testing.T) {
	run := &sessionRunnerChatRun{}
	result := map[string]any{"questions": []askUserQuestion{{
		Question: "Which engine?",
		Options: []askUserQuestionOption{
			{Label: "Engine A", Metadata: map[string]any{
				"implementation":    "Engine A",
				"resources":         map[string]any{"cpu": "4 cores", "memory": "8 GB", "gpu": "optional"},
				"decision_evidence": []string{askUserCurrentTaskEvidenceReference},
			}},
			{Label: "Engine B", Metadata: map[string]any{
				"implementation":    "Engine B",
				"resources":         map[string]any{"cpu": "8 cores", "memory": "16 GB", "gpu": "required"},
				"decision_evidence": []string{askUserCurrentTaskEvidenceReference},
			}},
		},
	}}}
	correction := askUserImplementationSelectionContractCorrection(run, result)
	if stringValue(correction["status"]) != "implementation_decision_contract_incomplete" {
		t.Fatalf("unpreflighted first implementation choice was accepted: %#v", correction)
	}
}

func TestAskUserSelectedImplementationCannotSwitchAfterOrdinaryInstallFailure(t *testing.T) {
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"GraphBP"}}
	resourceProfile := map[string]any{"cpu": "4 cores", "memory": "8 GB", "gpu": "required, 4 GB VRAM"}
	result := map[string]any{"questions": []askUserQuestion{{
		Question: "Switch after the install failed?",
		Options: []askUserQuestionOption{
			{Label: "Keep GraphBP", Metadata: map[string]any{
				"implementation": "GraphBP", "resources": resourceProfile,
				"decision_evidence":  []string{"tool-call:graphbp-preflight"},
				"preflight_feasible": true, "preflight_setup_state": "new_setup_feasible",
			}},
			{Label: "Use another engine", Metadata: map[string]any{
				"implementation": "Other Engine", "resources": resourceProfile,
				"decision_evidence": []string{"tool-call:other-preflight"},
			}},
		},
	}}}
	correction := askUserImplementationSelectionContractCorrection(run, result)
	if stringValue(correction["status"]) != "selected_implementation_recovery_required" ||
		boolValue(correction["decision_required"], true) {
		t.Fatalf("ordinary install failure allowed implementation switching: %#v", correction)
	}
}

func TestAskUserSelectedImplementationCanChangeAfterTypedCapacityBlocker(t *testing.T) {
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}}
	resourceProfile := map[string]any{"cpu": "8 cores", "memory": "32 GB", "gpu": "required, 24 GB VRAM"}
	result := map[string]any{"questions": []askUserQuestion{{
		Question: "The selected engine exceeds current hardware. Choose another route?",
		Options: []askUserQuestionOption{
			{Label: "Engine A", Metadata: map[string]any{
				"implementation": "Engine A", "resources": resourceProfile,
				"decision_evidence":  []string{"tool-call:engine-a-preflight"},
				"preflight_feasible": false, "preflight_setup_state": "new_setup_blocked",
				"preflight_blockers": []string{"insufficient_accelerator_memory"},
			}},
			{Label: "Engine B", Metadata: map[string]any{
				"implementation": "Engine B", "resources": resourceProfile,
				"decision_evidence": []string{"tool-call:engine-b-preflight"},
			}},
		},
	}}}
	if correction := askUserImplementationSelectionContractCorrection(run, result); correction != nil {
		t.Fatalf("typed capacity blocker did not allow a new user decision: %#v", correction)
	}
}
