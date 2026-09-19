package server

import "testing"

func TestExplicitNamedDeliverablesCannotBeHiddenAsWorkingData(t *testing.T) {
	input := map[string]any{
		"files": []any{"pcsk9_report.md", "source_ledger.csv", "raw_records.json"},
		"destination": map[string]any{
			"pcsk9_report.md":   "working_data",
			"source_ledger.csv": "working_data",
			"raw_records.json":  "working_data",
		},
	}
	got := sessionRunnerNormalizeExplicitDeliverableDestinations(
		"输出 pcsk9_report.md 和 source_ledger.csv，保留原始记录为工作数据。", input,
	)
	destination := got["destination"].(map[string]any)
	if destination["pcsk9_report.md"] != "snapshot" || destination["source_ledger.csv"] != "snapshot" {
		t.Fatalf("required destination=%#v", destination)
	}
	if destination["raw_records.json"] != "working_data" {
		t.Fatalf("raw working data was exposed: %#v", destination)
	}
}

func TestExplicitDestinationNormalizationLeavesUnrequestedWorkingFilesPrivate(t *testing.T) {
	input := map[string]any{
		"files":       []any{"notes.md", "scratch.csv"},
		"destination": map[string]any{"notes.md": "working_data", "scratch.csv": "working_data"},
	}
	got := sessionRunnerNormalizeExplicitDeliverableDestinations("分析现有数据并给出结论。", input)
	destination := got["destination"].(map[string]any)
	if destination["notes.md"] != "working_data" || destination["scratch.csv"] != "working_data" {
		t.Fatalf("unrequested files changed=%#v", destination)
	}
}

func TestExplicitMarkdownAndCSVDeliverablesCannotBeStagedAsWorkingData(t *testing.T) {
	input := map[string]any{
		"files": []any{"decision-report.md", "evidence.csv"},
		"destination": map[string]any{
			"decision-report.md": "working_data",
			"evidence.csv":       "snapshot",
		},
	}
	got := sessionRunnerNormalizeExplicitDeliverableDestinations(
		"生成可直接使用的Markdown专业报告与CSV证据表。", input,
	)
	destination := got["destination"].(map[string]any)
	if destination["decision-report.md"] != "snapshot" || destination["evidence.csv"] != "snapshot" {
		t.Fatalf("required destination=%#v", destination)
	}
}

func TestGenericDurableOutputRequestPromotesUnclassifiedFiles(t *testing.T) {
	input := map[string]any{
		"files":       []any{"result.bin"},
		"destination": map[string]any{"result.bin": "working_data"},
	}
	got := sessionRunnerNormalizeExplicitDeliverableDestinations("Make every result downloadable.", input)
	destination := got["destination"].(map[string]any)
	if destination["result.bin"] != "snapshot" {
		t.Fatalf("generic durable output was not promoted=%#v", destination)
	}
}
