package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestNoProgressClosedRegisteredBinaryRoutePointsToDownloadTool(t *testing.T) {
	const (
		sourceURL = "https://example.test/releases/engine.tar.gz"
		filename  = "engine.tar.gz"
		sha256    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)

	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "managed-engine-skill", ImplementationIdentities: []string{"Managed Engine"},
	})
	scienceCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "managed-capability",
		AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "managed-engine",
			ExecutionPack: sciencecapability.ExecutionPack{
				ID:   "managed-capability.engine",
				Mode: "local", Skill: "managed-engine-skill",
				Downloads: []sciencecapability.ExecutionDownload{{
					URL: sourceURL, Filename: filename, SHA256: sha256, SizeBytes: 123,
				}},
			},
		}},
	}}}
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Managed Engine"}}
	gateway := serverAgentRuntimeToolGateway{
		server:       &Server{skillCatalog: catalog, scienceCapabilities: scienceCatalog},
		taskRun:      run,
		allowedTools: []string{"web_fetch"},
	}

	call := agentruntime.ToolCall{
		ID: "web-fetch", Name: "web_fetch",
		Arguments: json.RawMessage(`{"url":"https://example.test/releases/engine.tar.gz"}`),
	}
	var immediate map[string]any
	if err := json.Unmarshal([]byte(gateway.toolCallPreflightDiagnostic(context.Background(), call)), &immediate); err != nil {
		t.Fatalf("immediate binary preflight is not JSON: %v", err)
	}
	if immediate["code"] != "registered_binary_download_preflight_required" ||
		!strings.Contains(immediate["recovery"].(string), filename) ||
		!strings.Contains(immediate["recovery"].(string), sha256) {
		t.Fatalf("immediate binary preflight=%#v", immediate)
	}
	var diagnostic map[string]any
	if err := json.Unmarshal([]byte(gateway.noProgressClosedRouteDiagnostic(call)), &diagnostic); err != nil {
		t.Fatalf("diagnostic is not JSON: %v", err)
	}
	if diagnostic["next_tool"] != "download_public_scientific_file" {
		t.Fatalf("next tool=%v", diagnostic["next_tool"])
	}
	recovery := diagnostic["recovery"].(string)
	if !strings.Contains(recovery, filename) || !strings.Contains(recovery, sha256) {
		t.Fatalf("recovery=%q", recovery)
	}
	download := diagnostic["download"].(map[string]any)
	if download["url"] != sourceURL || download["filename"] != filename || download["expected_sha256"] != sha256 {
		t.Fatalf("download hint=%#v", download)
	}

	unrelated := agentruntime.ToolCall{
		ID: "unrelated", Name: "web_fetch",
		Arguments: json.RawMessage(`{"url":"https://example.test/pages/index.html"}`),
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(gateway.noProgressClosedRouteDiagnostic(unrelated)), &generic); err != nil {
		t.Fatalf("generic diagnostic is not JSON: %v", err)
	}
	if _, found := generic["next_tool"]; found {
		t.Fatalf("unrelated web_fetch received a binary recovery hint: %#v", generic)
	}
}
