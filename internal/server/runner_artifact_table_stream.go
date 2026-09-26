package server

import (
	"io"
	"path/filepath"
	"strings"
)

func (store *runnerArtifactScanStore) readTables(name string, source io.Reader) ([]runnerCrossArtifactTable, error) {
	var tables []runnerCrossArtifactTable
	add := func(ordinal int, rowIndex int, headers, row []string) error {
		if ordinal == len(tables) {
			tables = append(tables, runnerCrossArtifactTable{source: name, ordinal: ordinal, headers: append([]string(nil), headers...), scanStore: store, scanScope: store.scope()})
		}
		return store.appendRow(tables[ordinal].scanScope, rowIndex-2, row)
	}
	var err error
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".csv", ".tsv":
		err = visitRunnerDelimitedRows(store.ctx, source, name, func(headers []string) bool { return len(headers) >= 2 }, func(row int, headers, values []string) error { return add(0, row, headers, values) })
	case ".md":
		err = visitRunnerMarkdownRows(store.ctx, source, add)
	}
	return tables, err
}
