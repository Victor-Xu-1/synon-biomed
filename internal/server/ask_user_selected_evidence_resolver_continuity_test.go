package server

import (
	"testing"

	"synon-go/internal/sciencecapability"
)

func TestSelectedEvidenceResolverOrdinaryFailureDoesNotReopenChoice(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := &Server{scienceCapabilities: &sciencecapability.Catalog{
		Capabilities: []sciencecapability.Definition{{
			ID: "resolver-capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
				ID: "resolver", ExecutionPack: sciencecapability.ExecutionPack{
					ID: "resolver-capability.resolver", Mode: "local", Skill: "resolver-skill",
					Downloads: []sciencecapability.ExecutionDownload{{
						InputKind: "resolver-archive", URL: "https://example.test/resolver.tar.gz",
						Filename: "resolver.tar.gz", SHA256: digest, SizeBytes: 1024,
					}},
				},
			}},
		}},
	}}
	run := &sessionRunnerChatRun{}
	run.setSelectedEvidenceResolvers(sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine",
	})
	result := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{
		{Label: "Stop", Metadata: map[string]any{"implementation": "terminate"}},
		{Label: "Retry resolver", Metadata: map[string]any{
			"implementation": "Resolver Engine 2.5.1",
			"resources":      map[string]any{"cpu": "unresolved", "memory": "unresolved", "gpu": "unresolved"},
		}},
	}}}}
	correction := server.askUserSelectedEvidenceResolverContinuityCorrection(run, result)
	downloads, _ := correction["registered_downloads"].([]map[string]any)
	if stringValue(correction["status"]) != "selected_evidence_resolver_recovery_required" ||
		boolValue(correction["decision_required"], true) ||
		stringValue(correction["required_tool"]) != "download_public_scientific_file" ||
		len(downloads) != 1 {
		t.Fatalf("resolver continuity correction=%#v", correction)
	}
}
