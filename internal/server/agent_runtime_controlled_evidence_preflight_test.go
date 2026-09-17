package server

import (
	"testing"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestControlledEvidenceDerivationRequiresUserSelectedRegistryResolver(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{Name: "primary-engine", ImplementationIdentities: []string{"Primary Engine"}})
	skillCatalog.AddSkill(skills.Skill{Name: "evidence-resolver", ImplementationIdentities: []string{"Resolver Engine"}})
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{
		{ID: "primary-capability", AcceptedEngines: []sciencecapability.EngineDefinition{{ExecutionPack: sciencecapability.ExecutionPack{
			ID: "primary-capability.engine", Mode: "local", Skill: "primary-engine",
			Parameters: []sciencecapability.ExecutionParameter{
				{Name: "point_x", Argument: "--point-x", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "control-point", EvidenceTerms: []string{"control point", "控制点"}},
				{Name: "point_y", Argument: "--point-y", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "control-point"},
				{Name: "point_z", Argument: "--point-z", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "control-point"},
			},
			EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{{EvidenceGroup: "control-point", Skill: "evidence-resolver", Implementation: "Resolver Engine"}},
		}}}},
		{ID: "resolver-capability", AcceptedEngines: []sciencecapability.EngineDefinition{{ExecutionPack: sciencecapability.ExecutionPack{
			ID: "resolver-capability.engine", Mode: "local", Skill: "evidence-resolver", Executable: "python", Script: "executionpacks/resolve.py",
		}}}},
	}}
	run := &sessionRunnerChatRun{RequiredScientificCapabilities: []string{"primary-capability"}, SelectedImplementations: []string{"Primary Engine"}}
	gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: skillCatalog, scienceCapabilities: capabilityCatalog}, taskRun: run}
	input := map[string]any{
		"human_description": "Derive the control point from the input data.",
		"code":              "print('temporary estimate')",
	}
	blocked := gateway.agentRuntimeControlledEvidenceDerivationPreflight("python", input)
	available, _ := blocked["available_resolvers"].([]sciencecapability.ExecutionEvidenceResolver)
	if stringValue(blocked["status"]) != "evidence_resolver_selection_required" ||
		!boolValue(blocked["decision_required"], false) || len(available) != 1 {
		t.Fatalf("unselected resolver derivation=%#v", blocked)
	}
	run.setSelectedEvidenceResolvers(sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "control-point", Skill: "evidence-resolver", Implementation: "Resolver Engine",
	})
	blocked = gateway.agentRuntimeControlledEvidenceDerivationPreflight("python", input)
	if stringValue(blocked["status"]) != "selected_evidence_resolver_entrypoint_required" ||
		stringValue(blocked["required_skill"]) != "evidence-resolver" || boolValue(blocked["decision_required"], true) {
		t.Fatalf("selected resolver ad hoc derivation=%#v", blocked)
	}
	if ordinary := gateway.agentRuntimeControlledEvidenceDerivationPreflight("python", map[string]any{
		"human_description": "Read an unrelated table.", "code": "print('ok')",
	}); ordinary != nil {
		t.Fatalf("unrelated computation was blocked: %#v", ordinary)
	}
}
