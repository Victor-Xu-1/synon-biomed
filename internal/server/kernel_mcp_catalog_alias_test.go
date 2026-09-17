package server

import "testing"

func TestKernelMCPCatalogResolvesOnlyAnUnambiguousAuthorizedIdentity(t *testing.T) {
	connectors := []workspaceMCPRuntimeConnector{{ID: "bundled:source", Name: "Source database", Enabled: true}, {ID: "custom:other", Name: "other", Enabled: true}}
	matched := kernelMCPConnectorMatches(connectors, "source")
	if len(matched) != 1 || matched[0].ID != "bundled:source" {
		t.Fatalf("bare identity not discoverable: %v", matched)
	}
	connectors = append(connectors, workspaceMCPRuntimeConnector{ID: "custom:source", Name: "source", Enabled: true})
	matched = kernelMCPConnectorMatches(connectors, "bundled:source")
	if len(matched) != 1 || matched[0].ID != "bundled:source" {
		t.Fatal("explicit connector identity was replaced")
	}
}
