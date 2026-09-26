package server

import "strings"

// The retired snapshot parser is retained only as a small-input parity oracle.
func runnerMarkdownArtifactTables(snapshot runnerCrossArtifactSnapshot) []runnerCrossArtifactTable {
	lines := strings.Split(strings.ReplaceAll(snapshot.text, "\r\n", "\n"), "\n")
	tables := make([]runnerCrossArtifactTable, 0)
	for index := 0; index+1 < len(lines); index++ {
		headers, ok := splitRunnerMarkdownTableLine(lines[index])
		if !ok || len(headers) < 2 {
			continue
		}
		separator, ok := splitRunnerMarkdownTableLine(lines[index+1])
		if !ok || len(separator) != len(headers) || !runnerMarkdownSeparatorRow(separator) {
			continue
		}
		rows := make([][]string, 0)
		cursor := index + 2
		for ; cursor < len(lines); cursor++ {
			row, rowOK := splitRunnerMarkdownTableLine(lines[cursor])
			if !rowOK || len(row) != len(headers) {
				break
			}
			rows = append(rows, row)
		}
		if len(rows) > 0 {
			tables = append(tables, runnerCrossArtifactTable{source: snapshot.name, ordinal: len(tables), headers: headers, rows: rows})
		}
		index = cursor - 1
	}
	return tables
}
