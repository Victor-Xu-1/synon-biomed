package server

import (
	"reflect"
	"testing"
)

func TestResearchSharedTableContractComparesExplicitMultilingualProjections(t *testing.T) {
	contract := runnerCrossArtifactSnapshot{name: "table-contract.json", text: `{
		"schema":"synon.artifact-table-contract.v1",
		"tables":[{
			"id":"pipeline",
			"identity_fields":["entity","regimen","as_of"],
			"compare_fields":["phase"],
			"records":[{"entity":"C1","regimen":"monotherapy","as_of":"2026-09-10","phase":"Phase 1"}],
			"projections":[
				{"artifact":"evidence.csv","table_index":0,"columns":{"entity":"compound","regimen":"regimen","as_of":"as_of","phase":"phase"}},
				{"artifact":"report.md","table_index":0,"columns":{"entity":"分子","regimen":"方案","as_of":"资料截止日","phase":"阶段"}}
			]
		}]
	}`}
	data := runnerCrossArtifactSnapshot{name: "evidence.csv", text: "compound,regimen,as_of,phase\nC1,monotherapy,2026-09-10,Phase 1\n"}
	report := runnerCrossArtifactSnapshot{name: "report.md", text: "| 分子 | 方案 | 资料截止日 | 阶段 |\n|---|---|---|---|\n| C1 | monotherapy | 2026-09-10 | Preclinical |\n"}

	failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{contract, data, report})
	want := []string{"artifact_table_contract_value_mismatch:pipeline artifact=report.md row=C1/monotherapy/2026-09-10 field=phase values=Phase 1|Preclinical"}
	if !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}

func TestResearchSharedTableContractKeepsRegimenAndTimeInRowIdentity(t *testing.T) {
	contract := runnerCrossArtifactSnapshot{name: "table-contract.json", text: `{
		"schema":"synon.artifact-table-contract.v1",
		"tables":[{
			"id":"pipeline","identity_fields":["entity","regimen","as_of"],"compare_fields":["phase"],
			"records":[
				{"entity":"C1","regimen":"monotherapy","as_of":"2025-01-01","phase":"Phase 1"},
				{"entity":"C1","regimen":"combination","as_of":"2026-09-10","phase":"Phase 2"}
			],
			"projections":[
				{"artifact":"evidence.csv","table_index":0,"columns":{"entity":"entity","regimen":"regimen","as_of":"as_of","phase":"phase"}},
				{"artifact":"report.md","table_index":0,"columns":{"entity":"entity","regimen":"regimen","as_of":"as_of","phase":"phase"}}
			]
		}]
	}`}
	data := runnerCrossArtifactSnapshot{name: "evidence.csv", text: "entity,regimen,as_of,phase\nC1,monotherapy,2025-01-01,Phase 1\nC1,combination,2026-09-10,Phase 2\n"}
	report := runnerCrossArtifactSnapshot{name: "report.md", text: "| entity | regimen | as_of | phase |\n|---|---|---|---|\n| C1 | monotherapy | 2025-01-01 | Phase 1 |\n| C1 | combination | 2026-09-10 | Phase 2 |\n"}
	if failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{contract, data, report}); len(failures) != 0 {
		t.Fatalf("scoped shared records were rejected: %#v", failures)
	}
}

func TestResearchSharedTableContractRejectsUnknownFields(t *testing.T) {
	contract := runnerCrossArtifactSnapshot{name: "table-contract.json", text: `{
		"schema":"synon.artifact-table-contract.v1","unexpected":true,"tables":[]
	}`}
	want := []string{"artifact_table_contract_invalid:table-contract.json reason=invalid_schema"}
	if failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{contract}); !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}
