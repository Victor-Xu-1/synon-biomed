package server

import (
	"testing"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestRegisteredExecutionDownloadRequiresExactSelectedPackAuthority(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{
		Name: "primary-skill", ImplementationIdentities: []string{"Primary Engine"},
	})
	skillCatalog.AddSkill(skills.Skill{
		Name: "resolver-skill", ImplementationIdentities: []string{"Resolver Engine"},
	})
	capabilities := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "resolver-capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "resolver", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "resolver-capability.resolver", Mode: "local", Skill: "resolver-skill",
				Downloads: []sciencecapability.ExecutionDownload{{
					InputKind: "resolver-archive", URL: "https://example.test/resolver.tar.gz",
					Filename: "resolver.tar.gz", SHA256: digest, SizeBytes: 1024,
					RedirectHosts: []string{"downloads.example.test"},
				}},
			},
		}},
	}}}
	server := &Server{skillCatalog: skillCatalog, scienceCapabilities: capabilities}
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Primary Engine"}}
	request := agentPublicScientificFileRequest{
		SourceURL: "https://example.test/resolver.tar.gz", Filename: "resolver.tar.gz",
		ExpectedSHA256: digest,
	}
	if source, found := server.registeredExecutionDownloadSource(run, request); found || source != "" {
		t.Fatalf("unselected resolver source was authorized: source=%q found=%t", source, found)
	}
	run.setSelectedEvidenceResolvers(sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine",
	})
	if source, found := server.registeredExecutionDownloadSource(run, request); !found ||
		source != "execution-pack:resolver-capability.resolver:resolver-archive" {
		t.Fatalf("selected resolver source=%q found=%t", source, found)
	}
	download, _, found := server.registeredExecutionDownload(run, request)
	if !found || len(download.RedirectHosts) != 1 ||
		!agentPublicScientificResponseHostAllowed(agentPublicScientificFileRequest{
			SourceHost: "example.test", RedirectHosts: download.RedirectHosts,
		}, "downloads.example.test") ||
		agentPublicScientificResponseHostAllowed(agentPublicScientificFileRequest{
			SourceHost: "example.test", RedirectHosts: download.RedirectHosts,
		}, "untrusted.example.test") {
		t.Fatalf("registered redirect authority=%#v found=%t", download, found)
	}
	request.ExpectedSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if source, found := server.registeredExecutionDownloadSource(run, request); found || source != "" {
		t.Fatalf("checksum-mismatched source was authorized: source=%q found=%t", source, found)
	}
}
