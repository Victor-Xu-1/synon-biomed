package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestWorkspaceRecordDirectoryExposesLateRecordsWithoutDiscardingNestedData(t *testing.T) {
	rows := []map[string]any{}
	for i := 0; i < 30; i++ {
		rows = append(rows, map[string]any{"id": fmt.Sprintf("study-%02d", i), "title": fmt.Sprintf("Distinct scientific record %02d", i), "authors": []any{map[string]any{"affiliations": strings.Repeat("institution ", 1000)}}, "abstract": strings.Repeat("Full scientific source. ", 100)})
	}
	raw, _ := json.Marshal(rows)
	ctx := withAgentWorkspaceReadBudget(context.Background(), 16000)
	input := map[string]any{"version_id": "record-version"}
	value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "records.json", "application/json", int64(len(raw)), input)
	if err != nil {
		t.Fatal(err)
	}
	content := stringValue(mapValue(value)["content"])
	for i := 0; i < 30; i++ {
		if !strings.Contains(content, fmt.Sprintf("study-%02d", i)) {
			t.Errorf("record %d hidden behind preceding nested metadata", i)
		}
	}
	if !agentWorkspaceReadResultFits(ctx, value) {
		t.Fatal("record directory exceeded transport")
	}
	input["json_pointer"] = "/29/authors/0/affiliations"
	selected, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "records.json", "application/json", int64(len(raw)), input)
	if err != nil || !strings.Contains(stringValue(mapValue(selected)["content"]), "institution institution") {
		t.Fatalf("complete nested data became inaccessible: %v", err)
	}
}

func TestWorkspaceRecordDirectoryCapturedReplay(t *testing.T) {
	path := os.Getenv("SYNON_TEST_RECORD_SOURCE")
	if path == "" {
		t.Skip("no record source selected")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	ctx := withAgentWorkspaceReadBudget(context.Background(), 16000)
	value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "records.json", "application/json", int64(len(raw)), map[string]any{"version_id": "record-replay"})
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Split(stringValue(mapValue(value)["content"]), "--- complete JSON data ---")[0]
	visible := 0
	for i := range rows {
		if strings.Contains(content, fmt.Sprintf("/%d ", i)) {
			visible++
		}
	}
	t.Logf("source_bytes=%d records=%d visible_record_directory_entries=%d next_offset=%v", len(raw), len(rows), visible, mapValue(value)["next_offset"])
	if visible != len(rows) {
		t.Fatalf("captured record set not navigable from its first reading page: %d/%d", visible, len(rows))
	}
}
