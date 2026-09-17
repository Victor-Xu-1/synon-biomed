package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// An array of structured records needs an overview of records, not hundreds
// of affiliation/trace lines from just its first item. This is format-driven
// navigation: no field is treated as a title, DOI, abstract or preferred fact.
// The full JSON follows the directory, and RFC 6901 selects any complete value.
func agentWorkspaceRecordView(ctx context.Context, raw []byte, filename string, size int64, input map[string]any) (map[string]any, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' {
		return nil, false, nil
	}
	var records []map[string]json.RawMessage
	if json.Unmarshal(raw, &records) != nil || len(records) == 0 {
		return nil, false, nil
	}
	var directory strings.Builder
	directory.WriteString("JSON record directory (json_pointer; summaries are not complete values)\n")
	columnSet := map[string]bool{}
	for _, record := range records {
		for key := range record {
			columnSet[key] = true
		}
	}
	columns := make([]string, 0, len(columnSet))
	for key := range columnSet {
		columns = append(columns, key)
	}
	sort.Strings(columns)
	header, _ := json.Marshal(columns)
	fmt.Fprintf(&directory, "columns: %s\n", header)
	for i, record := range records {
		if record == nil {
			return nil, false, nil
		}
		if err := context.Cause(ctx); err != nil {
			return nil, true, err
		}
		row := make([]any, len(columns))
		for column, key := range columns {
			value, present := record[key]
			if !present {
				row[column] = map[string]any{"present": false}
				continue
			}
			var decoded any
			if json.Unmarshal(value, &decoded) != nil {
				return nil, false, nil
			}
			switch v := decoded.(type) {
			case []any:
				row[column] = fmt.Sprintf("array(%d)", len(v))
			case map[string]any:
				row[column] = fmt.Sprintf("object(%d)", len(v))
			case string:
				// A cell preview is layout only. Large strings are not sliced or
				// substituted in the original data or its full-value selector.
				if len(v) > 256 {
					row[column] = fmt.Sprintf("string(%d bytes)", len(v))
				} else {
					row[column] = v
				}
			default:
				row[column] = json.RawMessage(value)
			}
		}
		encoded, _ := json.Marshal(row)
		fmt.Fprintf(&directory, "/%d %s\n", i, encoded)
	}
	directory.WriteString("--- complete JSON data ---\n")
	formatted, _, err := agentWorkspaceJSONReadView(raw)
	if err != nil {
		return nil, true, err
	}
	directory.Write(formatted)
	metadata := map[string]any{"view_format": "json-record-directory-display-lines", "source_records": len(records), "source_size_bytes": size, "raw_read_with": agentWorkspaceSourceReadInput(input, "")}
	result, err := readAgentWorkspaceDocumentPage(ctx, directory.String(), filename, size, input, metadata)
	return result, true, err
}
