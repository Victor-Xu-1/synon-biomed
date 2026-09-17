package server

import (
	"testing"

	"synon-go/internal/sciencecapability"
)

func TestManagedExecutionParameterAcceptsOnlySelectedEvidenceResolver(t *testing.T) {
	pack := sciencecapability.ExecutionPack{
		ID: "example-capability.resolver", Skill: "evidence-resolver",
		Parameters: []sciencecapability.ExecutionParameter{{
			Name: "method", Argument: "--method", Type: "string",
			Evidence: "selected-evidence-resolver",
		}},
	}
	selected := []sciencecapability.ExecutionEvidenceResolver{{
		EvidenceGroup: "control-point", Skill: "evidence-resolver", Implementation: "ResolverEngine",
	}}
	if blocked := managedExecutionPackParameterEvidencePreflight(
		pack, "python resolver.py --method ResolverEngine-2.5.1", nil, "en", selected,
	); blocked != nil {
		t.Fatalf("selected resolver implementation was rejected: %#v", blocked)
	}
	for name, command := range map[string]string{
		"missing":   "python resolver.py",
		"different": "python resolver.py --method DifferentEngine",
	} {
		t.Run(name, func(t *testing.T) {
			blocked := managedExecutionPackParameterEvidencePreflight(pack, command, nil, "en", selected)
			if blocked == nil || blocked["status"] != "execution_selected_resolver_parameter_required" {
				t.Fatalf("unselected resolver parameter was accepted: %#v", blocked)
			}
		})
	}
}
