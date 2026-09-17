package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"synon-go/internal/mcpdirectory"
	runtimekv "synon-go/internal/persistence/runtimekv"
)

func TestMCPConnectorUsageProjectionAggregatesPerUserAndExposesZero(t *testing.T) {
	runtimeStore := runtimekv.New(filepath.Join(t.TempDir(), "runtime-state.sqlite"))
	defer runtimeStore.Close()
	server := &Server{runtimeStore: runtimeStore}

	server.recordMCPConnectorInvocation(context.Background(), "user-a", "bundled:pubmed", "bundled", "pubmed", "search_articles", nil)
	server.recordMCPConnectorInvocation(context.Background(), "user-a", "bundled:pubmed", "bundled", "pubmed", "fetch_article", context.Canceled)
	server.recordMCPConnectorInvocation(context.Background(), "user-b", "bundled:pubmed", "bundled", "pubmed", "search_articles", nil)

	connectors := []mcpdirectory.Connector{
		{ID: "bundled:pubmed", Name: "pubmed", Source: "bundled"},
		{ID: "bundled:chembl", Name: "chembl", Source: "bundled"},
	}
	if err := server.attachMCPConnectorUsage("user-a", connectors); err != nil {
		t.Fatal(err)
	}
	if connectors[0].Usage.InvocationCount != 2 || connectors[0].Usage.LastUsedAt == nil {
		t.Fatalf("aggregated MCP usage = %#v", connectors[0].Usage)
	}
	if connectors[1].Usage.InvocationCount != 0 || connectors[1].Usage.LastUsedAt != nil {
		t.Fatalf("unused MCP usage = %#v", connectors[1].Usage)
	}

	payload, err := json.Marshal(connectors[1])
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" || !containsJSONNull(payload, "lastUsedAt") {
		t.Fatalf("unused MCP usage must expose null lastUsedAt: %s", payload)
	}
}

func TestMCPConnectorUsageProjectionSupportsLegacyNameRecords(t *testing.T) {
	runtimeStore := runtimekv.New(filepath.Join(t.TempDir(), "runtime-state.sqlite"))
	defer runtimeStore.Close()
	server := &Server{runtimeStore: runtimeStore}

	server.recordMCPConnectorInvocation(context.Background(), "user-a", "", "", "legacy_pubmed", "search_articles", nil)
	connectors := []mcpdirectory.Connector{{ID: "bundled:pubmed", Name: "legacy_pubmed", Source: "bundled"}}
	if err := server.attachMCPConnectorUsage("user-a", connectors); err != nil {
		t.Fatal(err)
	}
	if connectors[0].Usage.InvocationCount != 1 || connectors[0].Usage.LastUsedAt == nil {
		t.Fatalf("legacy name usage = %#v", connectors[0].Usage)
	}
}

func containsJSONNull(payload []byte, key string) bool {
	var record map[string]any
	if err := json.Unmarshal(payload, &record); err != nil {
		return false
	}
	value, ok := record["usage"].(map[string]any)
	if !ok {
		return false
	}
	lastUsedAt, ok := value[key]
	return ok && lastUsedAt == nil
}
