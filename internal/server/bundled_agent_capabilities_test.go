package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/persistence/workspace"
)

func TestBundledAgentCapabilitiesUseVerifiedCatalog(t *testing.T) {
	server := newV11TestServer(t, Options{})
	if server.skillCatalog == nil {
		t.Fatal("skill catalog is nil")
	}
	availableSkills := make(map[string]struct{})
	for _, skill := range server.skillCatalog.Skills() {
		availableSkills[strings.ToLower(strings.TrimSpace(skill.Name))] = struct{}{}
	}
	var capabilityProfiles int
	for _, agent := range server.agentCatalog.Agents() {
		capabilities, found := capabilitiesFromAgent(agent)
		if !found {
			continue
		}
		capabilityProfiles++
		name := agent.Name
		if len(capabilities.Skills) < 8 || len(capabilities.Connectors) < 6 {
			t.Errorf("%s has too few default capabilities: %#v", name, capabilities)
		}
		for _, skillName := range capabilities.Skills {
			if _, found := availableSkills[strings.ToLower(strings.TrimSpace(skillName))]; !found {
				t.Errorf("%s references missing skill %q", name, skillName)
			}
		}
		for _, connectorID := range capabilities.Connectors {
			if _, found := bundledAgentConnectorIDs[connectorID]; !found {
				t.Errorf("%s references unknown connector %q", name, connectorID)
			}
		}
	}
	if capabilityProfiles != 10 {
		t.Fatalf("capability profile count = %d, want 10", capabilityProfiles)
	}
	for _, restricted := range []string{"REVIEWER", "BOOKMARKER", "ONBOARDING", "OPERON"} {
		if capabilities, found := server.bundledAgentCapabilityDefaults(restricted); found || len(capabilities.Skills) != 0 || len(capabilities.Connectors) != 0 {
			t.Fatalf("restricted agent %s unexpectedly has specialist defaults: %#v", restricted, capabilities)
		}
	}

	defaults := server.bundledAgentDefaultSkillNames("AIDD_EXPERT")
	if len(defaults) == 0 {
		t.Fatal("AIDD_EXPERT has no default skills")
	}
	defaults[0] = "mutated"
	if server.bundledAgentDefaultSkillNames("AIDD_EXPERT")[0] == "mutated" {
		t.Fatal("capability defaults leaked their backing slice")
	}

	profile := workspace.Agent{ConnectorTombstones: []string{"bundled:pubmed"}}
	connectorIDs, err := server.bundledAgentConnectorIDsForProfile("test-user", "AIDD_EXPERT", profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, connectorID := range connectorIDs {
		if connectorID == "bundled:pubmed" {
			t.Fatal("connector tombstone did not suppress the default connector")
		}
	}
}

func TestSpecialistAgentsDiscoverRoutedSkills(t *testing.T) {
	server := newV11TestServer(t, Options{})
	for _, agentName := range []string{"AIDD_EXPERT", "COMPUTATIONAL_CHEM_EXPERT", "MEDCHEM_EXPERT"} {
		if !slices.Contains(server.bundledAgentDefaultSkillNames(agentName), "autodock-vina") {
			t.Fatalf("%s does not discover autodock-vina", agentName)
		}
	}
	if !slices.Contains(server.bundledAgentDefaultSkillNames("GENOMICS_BIOINFO_EXPERT"), "ngs-analysis-router") {
		t.Fatal("GENOMICS_BIOINFO_EXPERT does not discover ngs-analysis-router")
	}

	response := httptest.NewRecorder()
	request := newLoopbackTestRequest(http.MethodGet,
		"/api/agents?names=AIDD_EXPERT&names=COMPUTATIONAL_CHEM_EXPERT&names=MEDCHEM_EXPERT&names=GENOMICS_BIOINFO_EXPERT&include_metadata=true", nil)
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET docking specialist agents status=%d body=%s", response.Code, response.Body.String())
	}
	var agents []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 4 {
		t.Fatalf("docking specialist agents = %#v", agents)
	}
	for _, agent := range agents {
		names := anyStringSlice(t, agent["skillNames"])
		want := "autodock-vina"
		if agent["name"] == "GENOMICS_BIOINFO_EXPERT" {
			want = "ngs-analysis-router"
		}
		if !slices.Contains(names, want) {
			t.Fatalf("agent %v skillNames = %#v, want %q", agent["name"], names, want)
		}
	}
}

func TestBundledAgentsProjectSpecialistCapabilities(t *testing.T) {
	server := newV11TestServer(t, Options{})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, newLoopbackTestRequest(http.MethodGet, "/api/agents?names=AIDD_EXPERT&include_metadata=true", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET specialist agents status=%d body=%s", response.Code, response.Body.String())
	}
	var agents []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents=%#v", agents)
	}
	if skills, ok := agents[0]["skillNames"].([]any); !ok || len(skills) < 8 {
		t.Fatalf("specialist skillNames=%#v", agents[0]["skillNames"])
	}
	if connectors, ok := agents[0]["connectorIds"].([]any); !ok || len(connectors) < 6 {
		t.Fatalf("specialist connectorIds=%#v", agents[0]["connectorIds"])
	}
	metadata, ok := agents[0]["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("specialist metadata=%#v", agents[0]["metadata"])
	}
	if skills, ok := metadata["skills"].([]any); !ok || len(skills) < 8 {
		t.Fatalf("specialist metadata skills=%#v", metadata["skills"])
	}
	if servers, ok := metadata["mcp_servers"].([]any); !ok || len(servers) < 6 {
		t.Fatalf("specialist metadata mcp_servers=%#v", metadata["mcp_servers"])
	}
}
