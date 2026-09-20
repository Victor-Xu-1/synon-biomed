package server

import (
	"context"
	"path/filepath"
	"strings"
)

// Historical fixtures keep their concise signatures but run the production
// stream/index validators, not the retired whole-document implementation.
func runnerCrossArtifactTableFailures(snapshots []runnerCrossArtifactSnapshot) []string {
	store, err := newRunnerArtifactScanStore(context.Background())
	if err != nil {
		panic(err)
	}
	defer closeArtifactFixtureStore(store)
	var inputs []runnerArtifactScanInput
	var tables []runnerCrossArtifactTable
	groups := make(map[string][]runnerCrossArtifactTable)
	for _, snapshot := range snapshots {
		inputs = append(inputs, runnerArtifactTextInput(snapshot.name, snapshot.text))
		parsed, err := store.readTables(snapshot.name, strings.NewReader(snapshot.text))
		if err != nil {
			panic(err)
		}
		tables = append(tables, parsed...)
		groups[strings.ToLower(filepath.Base(strings.TrimSpace(snapshot.name)))] = parsed
	}
	failures, err := inspectRunnerArtifactTableSet(store, inputs, tables, groups)
	if err != nil {
		panic(err)
	}
	return failures
}

func runnerCrossArtifactRankFailures(table runnerCrossArtifactTable) []string {
	store, err := newRunnerArtifactScanStore(context.Background())
	if err != nil {
		panic(err)
	}
	defer closeArtifactFixtureStore(store)
	failures, err := inspectRunnerCrossArtifactRanks(store, table)
	if err != nil {
		panic(err)
	}
	return failures
}

func runnerCrossArtifactApproximateIdentityRow(leftRow []string, rightRows [][]string, firstHeader string, shared []runnerCrossArtifactSharedColumn, count int) ([]string, bool) {
	store, err := newRunnerArtifactScanStore(context.Background())
	if err != nil {
		panic(err)
	}
	defer closeArtifactFixtureStore(store)
	table := runnerCrossArtifactTable{rows: rightRows}
	scope, err := store.indexApproximateRows(table, shared, count)
	if err != nil {
		panic(err)
	}
	row, found, err := store.approximateRow(table, scope, leftRow, firstHeader, shared, count)
	if err != nil {
		panic(err)
	}
	return row, found
}

func runnerCrossArtifactTables(snapshot runnerCrossArtifactSnapshot) []runnerCrossArtifactTable {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(snapshot.name))) {
	case ".csv", ".tsv":
		table := runnerCrossArtifactTable{source: snapshot.name}
		err := visitRunnerDelimitedRows(context.Background(), strings.NewReader(snapshot.text), snapshot.name, func(header []string) bool { table.headers = header; return len(header) >= 2 }, func(_ int, _ []string, row []string) error {
			table.rows = append(table.rows, append([]string(nil), row...))
			return nil
		})
		if err != nil || len(table.rows) == 0 {
			return nil
		}
		return []runnerCrossArtifactTable{table}
	case ".md":
		return runnerMarkdownArtifactTables(snapshot)
	default:
		return nil
	}
}

func closeArtifactFixtureStore(store *runnerArtifactScanStore) {
	if err := store.Close(); err != nil {
		panic(err)
	}
}
