package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBundledAgentsCompatibilityAPIListsV11Catalog(t *testing.T) {
	server := newV11TestServer(t, Options{})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, newLoopbackTestRequest(http.MethodGet, "/api/agents", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/agents status=%d body=%s", response.Code, response.Body.String())
	}
	var agents []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 14 || agents[0]["name"] != "AIDD_EXPERT" {
		t.Fatalf("agent list count=%d first=%#v", len(agents), agents[0])
	}
	operon := agents[11]
	operonSkills, ok := operon["skillNames"].([]any)
	if operon["name"] != "OPERON" || operon["displayName"] != "通用科研助手" || !ok || len(operonSkills) == 0 {
		t.Fatalf("OPERON response=%#v", operon)
	}
	documentWorkbenchAllowed := false
	for _, skill := range operonSkills {
		if skill == "document-workbench" {
			documentWorkbenchAllowed = true
			break
		}
	}
	if !documentWorkbenchAllowed {
		t.Fatalf("OPERON authority omitted document-workbench: %#v", operonSkills)
	}
	if _, present := agents[0]["systemPrompt"]; !present || agents[0]["systemPrompt"] != nil {
		t.Fatalf("default bundled systemPrompt=%#v", agents[0]["systemPrompt"])
	}
	if operon["systemPrompt"] != "" || operon["iconKey"] != "lightning" || operon["colorKey"] != "accent-main" {
		t.Fatalf("OPERON compatibility fields=%#v", operon)
	}
	wantGreeting := "Hi, let's set Synon Biomed up for your science.\n\nSynon Biomed works best when it understands what you research and how you research it."
	if agents[9]["greeting"] != wantGreeting {
		t.Fatalf("ONBOARDING greeting=%q", agents[9]["greeting"])
	}

	method := httptest.NewRecorder()
	server.Handler().ServeHTTP(method, newLoopbackTestRequest(http.MethodPost, "/api/agents", strings.NewReader("{}")))
	if method.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/agents status=%d", method.Code)
	}
}

func TestBundledAgentsCompatibilityAPISupportsNamesMetadataAndLite(t *testing.T) {
	server := newV11TestServer(t, Options{})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, newLoopbackTestRequest(http.MethodGet, "/api/agents?names=AIDD_EXPERT%2COPERON&include_metadata=true&lite=true", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET filtered agents status=%d body=%s", response.Code, response.Body.String())
	}
	var agents []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0]["name"] != "AIDD_EXPERT" || agents[1]["name"] != "OPERON" {
		t.Fatalf("filtered agents=%#v", agents)
	}
	operonSkillNames, ok := agents[1]["skillNames"].([]any)
	if !ok || len(operonSkillNames) == 0 {
		t.Fatalf("filtered OPERON skillNames=%#v", agents[1]["skillNames"])
	}
	if _, present := agents[0]["parameters"]; present {
		t.Fatal("lite response retained parameters")
	}
	if _, present := agents[0]["systemPrompt"]; present {
		t.Fatal("lite response retained top-level systemPrompt")
	}
	metadata, ok := agents[0]["metadata"].(map[string]any)
	if !ok || metadata["name"] != "AIDD_EXPERT" {
		t.Fatalf("metadata=%#v", agents[0]["metadata"])
	}
	if excluded, ok := metadata["excluded_tools"].([]any); !ok || len(excluded) != 0 {
		t.Fatalf("metadata excluded_tools=%#v", metadata["excluded_tools"])
	}
	if _, present := metadata["system_prompt"]; present {
		t.Fatal("lite metadata retained system_prompt")
	}
	operonMetadata := agents[1]["metadata"].(map[string]any)
	if listed, ok := operonMetadata["skills"].([]any); !ok || len(listed) != len(operonSkillNames) {
		t.Fatalf("OPERON metadata skills=%#v", operonMetadata["skills"])
	}

	full := httptest.NewRecorder()
	server.Handler().ServeHTTP(full, newLoopbackTestRequest(http.MethodGet, "/api/agents?names=AIDD_EXPERT&include_metadata=true", nil))
	var fullAgents []map[string]any
	if err := json.NewDecoder(full.Body).Decode(&fullAgents); err != nil {
		t.Fatal(err)
	}
	fullMetadata := fullAgents[0]["metadata"].(map[string]any)
	prompt, ok := fullMetadata["system_prompt"].(string)
	if !ok || !strings.Contains(prompt, "You are AI Drug Discovery") {
		t.Fatalf("full metadata system prompt missing: %#v", fullMetadata)
	}
}

func TestHealthReportsVerifiedAgentCatalog(t *testing.T) {
	server := newV11TestServer(t, Options{})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, newLoopbackTestRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /health status=%d body=%s", response.Code, response.Body.String())
	}
	var health map[string]any
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health["status"] != "healthy" || health["service"] != "gateway" || health["agents_registered"] != float64(14) || health["agent_catalog_ready"] != true {
		t.Fatalf("health=%#v", health)
	}
}
