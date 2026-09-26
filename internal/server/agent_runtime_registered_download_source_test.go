package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
	"synon-go/internal/toolgateway"
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
	if download, source, found := server.registeredExecutionDownloadCatalogSource(request); !found ||
		source != "execution-catalog:resolver-capability.resolver:resolver-archive" ||
		download.SizeBytes != 1024 {
		t.Fatalf("catalog source=%q found=%t download=%#v", source, found, download)
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
	if _, source, found := server.registeredExecutionDownloadCatalogSource(request); found || source != "" {
		t.Fatalf("checksum-mismatched catalog source was authorized: source=%q found=%t", source, found)
	}
}

func TestRegisteredExecutionDownloadHintsIncludeUnselectedLocalPack(t *testing.T) {
	catalog, err := sciencecapability.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{scienceCapabilities: &catalog}
	hints := server.registeredExecutionDownloadHints()
	for _, hint := range hints {
		if hint.URL == "https://github.com/rdk/p2rank/releases/download/2.5.1/p2rank_2.5.1.tar.gz" &&
			hint.Filename == "p2rank_2.5.1.tar.gz" {
			return
		}
	}
	t.Fatalf("default catalog did not expose the registered P2Rank download: %#v", hints)
}

func TestRegisteredBinaryRoutePreflightSurvivesApprovalResume(t *testing.T) {
	catalog, err := sciencecapability.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{scienceCapabilities: &catalog}
	call := agentruntime.ToolCall{
		ID: "approved-web-fetch", Name: "web_fetch",
		Arguments: json.RawMessage(`{"url":"https://github.com/rdk/p2rank/releases/download/2.5.1/p2rank_2.5.1.tar.gz"}`),
	}
	execution := &serverAgentRuntimeGatewayExecution{
		gateway: serverAgentRuntimeToolGateway{
			server: server, resumeAfterApproval: true,
		},
		call: call,
	}
	invocation := toolgateway.NewInvocation(
		context.Background(), call.ID, call.Name, call.Arguments, execution,
	)
	invocation.CanonicalName = "web_fetch"
	invocation.Input = map[string]any{
		"url": "https://github.com/rdk/p2rank/releases/download/2.5.1/p2rank_2.5.1.tar.gz",
	}
	serverAgentRuntimeGatewayPreflight(invocation)
	if invocation.ContinuationState() != toolgateway.AuditOnly {
		t.Fatalf("approved binary web_fetch continued to execution: status=%q value=%#v", invocation.Status, invocation.Value)
	}
	value, ok := invocation.Value.(map[string]any)
	if !ok || value["status"] != "registered_binary_download_preflight_required" ||
		!strings.Contains(stringValue(value["recovery"]), "download_public_scientific_file") {
		t.Fatalf("approved binary route preflight=%#v", invocation.Value)
	}
}

func TestRegisteredBinaryRouteStopsFullGatewayBeforeNetwork(t *testing.T) {
	catalog, err := sciencecapability.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{scienceCapabilities: &catalog}
	call := agentruntime.ToolCall{
		ID: "approved-web-fetch", Name: "web_fetch",
		Arguments: json.RawMessage(`{"url":"https://github.com/rdk/p2rank/releases/download/2.5.1/p2rank_2.5.1.tar.gz"}`),
	}
	receipt := (serverAgentRuntimeToolGateway{
		server: server, allowedTools: []string{"web_fetch"}, resumeAfterApproval: true,
	}).executeGateway(context.Background(), call)
	value := mapValue(receipt.Value)
	if value["status"] != "registered_binary_download_preflight_required" ||
		!strings.Contains(stringValue(value["recovery"]), "download_public_scientific_file") {
		t.Fatalf("full gateway route was not blocked before network: receipt=%#v", receipt)
	}
}

func TestRegisteredAcquisitionRejectsRepositoryAlternativesBeforeNetwork(t *testing.T) {
	const source = "https://github.com/example/engine/releases/download/3.2.1/engine.tar.gz"
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{Name: "engine-contract", ImplementationIdentities: []string{"Engine"}})
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Engine"}}
	server := &Server{skillCatalog: skillCatalog, scienceCapabilities: &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine", ExecutionPack: sciencecapability.ExecutionPack{ID: "capability.engine", Mode: "local", Skill: "engine-contract",
				Downloads: []sciencecapability.ExecutionDownload{{URL: source, Filename: "engine.tar.gz", SHA256: digest}},
			},
		}},
	}}}}
	for _, url := range []string{
		"https://codeload.github.com/example/engine/zip/refs/heads/master",
		"https://github.com/example/engine/archive/refs/heads/main.zip",
		"https://github.com/example/engine/releases/download/2.0/engine.tar.gz",
		"https://codeload.github.com/ex%61mple/engine/zip/refs/heads/master",
		"https://github.com:443/example/engine/archive/refs/heads/main.zip?download=1",
		"http://github.com/example/engine/archive/refs/heads/main.zip",
	} {
		for _, tool := range []string{"web_fetch", "download_public_scientific_file"} {
			t.Run(tool+"/"+url, func(t *testing.T) {
				call := agentruntime.ToolCall{ID: "alternative", Name: tool, Arguments: mustMarshalRawMessage(map[string]any{
					"url": url, "filename": "alternative.zip", "human_description": "acquire engine",
				})}
				receipt := (serverAgentRuntimeToolGateway{server: server, taskRun: run, allowedTools: []string{tool}, resumeAfterApproval: true}).executeGateway(context.Background(), call)
				result := mapValue(receipt.Value)
				if result["status"] != "registered_execution_download_required" || result["executed"] != false {
					t.Fatalf("repository alternative bypassed registered acquisition: %#v", receipt)
				}
				if download := mapValue(result["download"]); download["url"] != source || download["expected_sha256"] != digest {
					t.Fatalf("correction did not retain exact registered acquisition: %#v", result)
				}
			})
		}
	}
	valid := map[string]any{"url": source, "filename": "engine.tar.gz", "expected_sha256": digest}
	if result := server.registeredAcquisitionPreflight(run, "download_public_scientific_file", valid); result != nil {
		t.Fatalf("valid pinned acquisition was rejected: %#v", result)
	}
	delete(valid, "expected_sha256")
	if result := server.registeredAcquisitionPreflight(run, "download_public_scientific_file", valid); mapValue(result["download"])["expected_sha256"] != digest {
		t.Fatalf("missing checksum did not retain registered authority: %#v", result)
	}
	malformed := agentruntime.ToolCall{Name: "download_public_scientific_file", Arguments: mustMarshalRawMessage(map[string]any{
		"url": source, "filename": "engine.tar.gz", "expected_sha256": "invented", "human_description": "acquire engine",
	})}
	gateway := serverAgentRuntimeToolGateway{server: server, taskRun: run, allowedTools: []string{malformed.Name}, toolSchemas: []agentruntime.ToolSchema{agentPublicScientificFileToolSchema()}}
	var feedback map[string]any
	if raw := gateway.ToolCallAdmissionDiagnostic(malformed); json.Unmarshal([]byte(raw), &feedback) != nil || mapValue(feedback["download"])["expected_sha256"] != digest {
		t.Fatalf("malformed digest lost the known exact correction: %s", raw)
	}
	for _, unrelated := range []string{
		"https://github.com/example/other/archive/refs/heads/main.zip",
		"https://github.com/example/engine/blob/main/README.md",
		"https://data.example.org/measurement.csv",
	} {
		if result := server.registeredAcquisitionPreflight(run, "download_public_scientific_file", map[string]any{"url": unrelated}); result != nil {
			t.Fatalf("unrelated scientific resource was rejected: %#v", result)
		}
	}
	if result := server.registeredAcquisitionPreflight(nil, "download_public_scientific_file", map[string]any{"url": "https://codeload.github.com/example/engine/zip/refs/heads/master"}); result != nil {
		t.Fatalf("unselected repository was treated as task execution authority: %#v", result)
	}
}
