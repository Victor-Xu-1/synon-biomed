package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (store *runnerArtifactScanStore) identityIndex(left, right runnerCrossArtifactTable, shared []runnerCrossArtifactSharedColumn) (int, int64, error) {
	for count := 0; count < len(shared); count++ {
		leftIndex, unique, err := store.indexRows(left, func(row []string) string { return runnerCrossArtifactRowKey(row, 0, shared, count, true) }, true)
		if err != nil {
			return -1, 0, err
		}
		if !unique {
			continue
		}
		rightIndex, rightUnique, err := store.indexRows(right, func(row []string) string { return runnerCrossArtifactRowKey(row, 0, shared, count, false) }, true)
		if _, cleanupErr := store.exec(`DELETE FROM scan_keys WHERE scope=?`, leftIndex); cleanupErr != nil {
			return -1, 0, cleanupErr
		}
		if err != nil {
			return -1, 0, err
		}
		if rightUnique {
			return count, rightIndex, nil
		}
	}
	return -1, 0, nil
}

func runnerCrossArtifactIdentitySuffix(row []string, shared []runnerCrossArtifactSharedColumn, count int, left bool) string {
	parts := make([]string, 0, count)
	for _, column := range shared[:count] {
		index := column.rightIndex
		if left {
			index = column.leftIndex
		}
		value := ""
		if index < len(row) {
			value = normalizeRunnerTableIdentityValue(row[index], column.canonical)
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, "\x1f")
}

func (store *runnerArtifactScanStore) indexApproximateRows(table runnerCrossArtifactTable, shared []runnerCrossArtifactSharedColumn, count int) (int64, error) {
	scope := store.scope()
	err := table.eachRow(store.ctx, func(ordinal int, row []string) error {
		if len(row) == 0 {
			return nil
		}
		number, _, ok := parseRunnerComparableNumber(row[0])
		if !ok {
			return nil
		}
		_, err := store.exec(`INSERT INTO scan_numbers(scope,identity,number,ordinal) VALUES(?,?,?,?)`, scope, runnerCrossArtifactIdentitySuffix(row, shared, count, false), number, ordinal)
		return err
	})
	return scope, err
}

func (store *runnerArtifactScanStore) approximateRow(table runnerCrossArtifactTable, scope int64, leftRow []string, firstHeader string, shared []runnerCrossArtifactSharedColumn, count int) ([]string, bool, error) {
	if len(leftRow) == 0 {
		return nil, false, nil
	}
	leftValue, leftFormat, leftOK := parseRunnerComparableNumber(leftRow[0])
	if !leftOK {
		return nil, false, nil
	}
	// The existing displayed-precision comparison has a maximum tolerance of
	// one half unit. Use that superset for the index, then apply its exact rule.
	rows, err := store.tx.QueryContext(store.ctx, `SELECT ordinal FROM scan_numbers WHERE scope=? AND identity=? AND number>=? AND number<=? ORDER BY ordinal`, scope, runnerCrossArtifactIdentitySuffix(leftRow, shared, count, true), leftValue-0.500000000001, leftValue+0.500000000001)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var matched []string
	for rows.Next() {
		var ordinal int
		if err := rows.Scan(&ordinal); err != nil {
			return nil, false, err
		}
		candidate, err := store.rowAt(table, ordinal)
		if err != nil {
			return nil, false, err
		}
		rightValue, rightFormat, rightOK := parseRunnerComparableNumber(candidate[0])
		if !rightOK || !runnerComparableNumbersEqual(leftValue, rightValue, leftFormat, rightFormat, canonicalRunnerTableHeader(firstHeader)) {
			continue
		}
		identityMatches := true
		for _, identity := range shared[:count] {
			if identity.leftIndex >= len(leftRow) || identity.rightIndex >= len(candidate) || normalizeRunnerTableIdentityValue(leftRow[identity.leftIndex], identity.canonical) != normalizeRunnerTableIdentityValue(candidate[identity.rightIndex], identity.canonical) {
				identityMatches = false
				break
			}
		}
		if !identityMatches {
			continue
		}
		if matched != nil {
			return nil, false, nil
		}
		matched = candidate
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return matched, matched != nil, nil
}

func (store *runnerArtifactScanStore) rowAt(table runnerCrossArtifactTable, ordinal int) ([]string, error) {
	if table.scanStore == nil {
		return table.rows[ordinal], nil
	}
	var raw []byte
	if err := store.tx.QueryRowContext(store.ctx, `SELECT cells FROM scan_rows WHERE scope=? AND ordinal=?`, table.scanScope, ordinal).Scan(&raw); err != nil {
		return nil, err
	}
	var row []string
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	return row, nil
}

func inspectRunnerCrossArtifactTables(store *runnerArtifactScanStore, left, right runnerCrossArtifactTable) ([]string, error) {
	if left.source == right.source || len(left.headers) < 2 || len(right.headers) < 2 || canonicalRunnerTableHeader(left.headers[0]) != canonicalRunnerTableHeader(right.headers[0]) {
		return nil, nil
	}
	rightHeaders := make(map[string]int, len(right.headers))
	for index, header := range right.headers {
		rightHeaders[canonicalRunnerTableHeader(header)] = index
	}
	var shared []runnerCrossArtifactSharedColumn
	for leftIndex, header := range left.headers {
		if leftIndex == 0 {
			continue
		}
		normalized := canonicalRunnerTableHeader(header)
		if rightIndex, found := rightHeaders[normalized]; found && rightIndex > 0 {
			shared = append(shared, runnerCrossArtifactSharedColumn{leftIndex: leftIndex, rightIndex: rightIndex, name: strings.TrimSpace(header), canonical: normalized})
		}
	}
	if len(shared) == 0 {
		return nil, nil
	}
	identityColumns, rightIndex, err := store.identityIndex(left, right, shared)
	if err != nil {
		return nil, err
	}
	if identityColumns < 0 || identityColumns >= len(shared) {
		return nil, nil
	}
	var approximateIndex int64
	var failures []string
	err = left.eachRow(store.ctx, func(_ int, leftRow []string) error {
		if len(leftRow) == 0 {
			return nil
		}
		key := runnerCrossArtifactRowKey(leftRow, 0, shared, identityColumns, true)
		rightRow, found, err := store.findIndexedRow(right, rightIndex, key)
		if err != nil {
			return err
		}
		if !found {
			if approximateIndex == 0 {
				approximateIndex, err = store.indexApproximateRows(right, shared, identityColumns)
				if err != nil {
					return err
				}
			}
			rightRow, found, err = store.approximateRow(right, approximateIndex, leftRow, left.headers[0], shared, identityColumns)
			if err != nil {
				return err
			}
		}
		if !found {
			return nil
		}
		for _, column := range shared[identityColumns:] {
			if column.leftIndex >= len(leftRow) || column.rightIndex >= len(rightRow) {
				continue
			}
			leftValue, leftFormat, leftOK := parseRunnerComparableNumber(leftRow[column.leftIndex])
			rightValue, rightFormat, rightOK := parseRunnerComparableNumber(rightRow[column.rightIndex])
			if !leftOK || !rightOK || runnerComparableNumbersEqual(leftValue, rightValue, leftFormat, rightFormat, column.canonical) {
				continue
			}
			failures = append(failures, fmt.Sprintf("numeric_table_mismatch:%s<->%s key=%s column=%s values=%s|%s", left.source, right.source, runnerCrossArtifactDisplayRowKey(leftRow, shared, identityColumns), column.name, strings.TrimSpace(leftRow[column.leftIndex]), strings.TrimSpace(rightRow[column.rightIndex])))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := store.exec(`DELETE FROM scan_keys WHERE scope=?`, rightIndex); err != nil {
		return nil, err
	}
	if approximateIndex != 0 {
		if _, err := store.exec(`DELETE FROM scan_numbers WHERE scope=?`, approximateIndex); err != nil {
			return nil, err
		}
	}
	return failures, nil
}
