package server

import (
	"testing"

	"synon-go/internal/agentruntime"
)

func TestAgentRuntimeToolSearchChineseQueryFindsNativeAndMCPTools(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "WebSearch", Description: "Search public web sources."},
		{Name: "mcp__chembl__search_compounds", Description: "Search ChEMBL compounds and properties."},
		{Name: "calendar_create", Description: "Create a calendar event."},
	}

	matches := agentRuntimeToolSchemaSearchMatches("搜索化合物", schemas, 10)
	assertContainsString(t, matches, "WebSearch")
	assertContainsString(t, matches, "mcp__chembl__search_compounds")
}

func TestAgentRuntimeToolSearchFindsRegulatoryLabelsForPharmacokineticSourceIntent(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "mcp__literature__openalex_search_works", Description: "Search scholarly literature and authoritative sources."},
		{Name: "mcp__literature__pubmed_search", Description: "Search biomedical literature and article metadata."},
		{Name: "mcp__literature__openalex_search_authors", Description: "Search literature authors and sources."},
		{Name: "mcp__research-resources__find_antibodies_by_catalog", Description: "Search authoritative research resources."},
		{Name: "mcp__research-resources__get_antibody", Description: "Get an authoritative research resource."},
		{
			Name:        "mcp__drug-regulatory__search_drug_labels",
			Description: "Retrieve authoritative FDA drug product labels, prescribing information, drug label parameters, clinical pharmacology, pharmacokinetics / PK, pharmacodynamics, absorption, distribution, metabolism, elimination, dosage, safety, warnings, contraindications, and adverse reactions.",
		},
	}

	matches := agentRuntimeToolSchemaSearchMatches(
		"theophylline pharmacokinetics parameters authoritative source literature search", schemas, 5,
	)
	assertContainsString(t, matches, "mcp__drug-regulatory__search_drug_labels")
}

func TestChineseDiscoveryDoesNotBypassAllowedToolFilter(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "web_search", Description: "Search public web sources."},
		{Name: "mcp__chembl__search_compounds", Description: "Search ChEMBL compounds and properties."},
	}

	filtered := filterAgentRuntimeToolSchemas(schemas, []string{"WebSearch"})
	matches := agentRuntimeToolSchemaSearchMatches("搜索化合物", filtered, 10)
	assertContainsString(t, matches, "web_search")
	for _, match := range matches {
		if match == "mcp__chembl__search_compounds" {
			t.Fatalf("language expansion bypassed allowed-tool filtering: %v", matches)
		}
	}
}

func assertContainsString(t *testing.T, values []string, expected string) {
	t.Helper()
	for _, value := range values {
		if value == expected {
			return
		}
	}
	t.Fatalf("%v does not contain %q", values, expected)
}
