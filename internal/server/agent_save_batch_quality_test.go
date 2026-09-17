package server

import (
	"context"
	"reflect"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestResearchSaveBatchQualityAdvisoryRunsAfterArtifactsExistAndIsStable(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	save := func(id, name, kind, content string) map[string]any {
		t.Helper()
		artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
			ArtifactID: id, ProjectID: "project-a", Name: name, Kind: kind,
			Content: []byte(content), CreatedBy: "runner",
		})
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{
			"artifact_id": artifact.ID, "version_id": version.ID, "size_bytes": version.SizeBytes,
			"input_path": name,
		}
	}
	contract := save("contract", "table-contract.json", "application/json", `{
		"schema":"synon.artifact-table-contract.v1","tables":[{
			"id":"pipeline","identity_fields":["entity","regimen","as_of"],"compare_fields":["phase"],
			"records":[{"entity":"C1","regimen":"mono","as_of":"2026-09-10","phase":"Phase 1"}],
			"projections":[
				{"artifact":"evidence.csv","table_index":0,"columns":{"entity":"entity","regimen":"regimen","as_of":"as_of","phase":"phase"}},
				{"artifact":"report.md","table_index":0,"columns":{"entity":"entity","regimen":"regimen","as_of":"as_of","phase":"phase"}}
			]
		}]}`)
	data := save("data", "evidence.csv", "text/csv", "entity,regimen,as_of,phase\nC1,mono,2026-09-10,Phase 1\n")
	report := save("report", "report.md", "text/markdown", "| entity | regimen | as_of | phase |\n|---|---|---|---|\n| C1 | mono | 2026-09-10 | Preclinical |\n")
	server := &Server{workspaceStore: store}
	first := server.agentSavedArtifactBatchQualityAdvisory(context.Background(), "project-a", []any{contract, data, report})
	second := server.agentSavedArtifactBatchQualityAdvisory(context.Background(), "project-a", []any{report, contract, data})
	if first == nil || first["code"] != "artifact_consistency_advisory" || boolValue(first["blocking"], true) ||
		boolValue(first["completion_pending"], false) || first["quality_snapshot_id"] == "" ||
		first["quality_snapshot_id"] != second["quality_snapshot_id"] {
		t.Fatalf("batch advisories first=%#v second=%#v", first, second)
	}
	want := []string{"artifact_table_contract_value_mismatch:pipeline artifact=report.md row=C1/mono/2026-09-10 field=phase values=Phase 1|Preclinical"}
	if !reflect.DeepEqual(first["findings"], want) || boolValue(first["completion_blocking"], true) {
		t.Fatalf("batch advisory classification=%#v", first)
	}
}
