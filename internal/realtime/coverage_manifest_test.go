package realtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/compat/contracts"
)

func TestRealtimeCoverageManifestProvesEveryRecoveredContract(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "compatibility", "v1.1-realtime-contract-coverage.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read realtime coverage manifest: %v", err)
	}
	var manifest struct {
		SchemaVersion int `json:"schemaVersion"`
		Summary       struct {
			EventsImplemented  int `json:"eventsImplemented"`
			QueriesImplemented int `json:"queriesImplemented"`
			TotalImplemented   int `json:"totalImplemented"`
		} `json:"summary"`
		Events []struct {
			Name, Kind, Status, Evidence string
		} `json:"events"`
		Queries []struct {
			Name, Expression, Status, Evidence string
		} `json:"queries"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode realtime coverage manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Summary.EventsImplemented != 47 ||
		manifest.Summary.QueriesImplemented != 60 || manifest.Summary.TotalImplemented != 107 {
		t.Fatalf("manifest summary=%#v schema=%d", manifest.Summary, manifest.SchemaVersion)
	}
	events := make(map[string]struct{ kind, status, evidence string }, len(manifest.Events))
	for _, item := range manifest.Events {
		if item.Name == "" || item.Status != "implemented" || item.Evidence == "" {
			t.Fatalf("invalid event coverage row=%#v", item)
		}
		if _, duplicate := events[item.Name]; duplicate {
			t.Fatalf("duplicate event coverage row %q", item.Name)
		}
		events[item.Name] = struct{ kind, status, evidence string }{item.Kind, item.Status, item.Evidence}
	}
	for _, baseline := range contracts.EventTypes {
		row, found := events[baseline.Name]
		if !found || row.kind != baseline.Kind {
			t.Fatalf("event coverage %s=%#v found=%v", baseline.Name, row, found)
		}
	}
	queries := make(map[string]struct{ expression, status, evidence string }, len(manifest.Queries))
	for _, item := range manifest.Queries {
		if item.Name == "" || item.Status != "implemented" || item.Evidence == "" {
			t.Fatalf("invalid query coverage row=%#v", item)
		}
		if _, duplicate := queries[item.Name]; duplicate {
			t.Fatalf("duplicate query coverage row %q", item.Name)
		}
		queries[item.Name] = struct{ expression, status, evidence string }{item.Expression, item.Status, item.Evidence}
	}
	for _, baseline := range contracts.QueryKeys {
		row, found := queries[baseline.Name]
		if !found || row.expression != baseline.Expression {
			t.Fatalf("query coverage %s=%#v found=%v", baseline.Name, row, found)
		}
	}
	if len(events) != len(contracts.EventTypes) || len(queries) != len(contracts.QueryKeys) {
		t.Fatalf("manifest row counts events=%d queries=%d", len(events), len(queries))
	}
}
