package server

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

// Small-fixture oracle for the previous numerical semantics; production uses
// the complete disk-backed scan and propagates read/cancellation errors.
// legacyCompareRunnerCrossArtifactTransposedTables covers a common report layout:
// machine-readable data keeps entities in rows and measurements in columns,
// while a Markdown comparison table presents entities in columns and
// measurements in rows. The ordinary table comparer intentionally cannot
// match those orientations. This bounded presentation-vs-data comparison uses
// only intersecting labels and numeric cells; it neither infers missing values
// nor assigns scientific meaning to a column.
func legacyCompareRunnerCrossArtifactTransposedTables(left, right runnerCrossArtifactTable) []string {
	if !runnerCrossArtifactPresentationPair(left, right) {
		return nil
	}
	presentation, data := left, right
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(presentation.source)), ".md") {
		presentation, data = data, presentation
	}
	if len(presentation.headers) < 2 || len(data.headers) < 2 {
		return nil
	}

	dataRows := make(map[string][]string, len(data.rows))
	for _, row := range data.rows {
		if len(row) > 0 {
			dataRows[normalizeRunnerTableToken(row[0])] = row
		}
	}
	type transposedEntity struct {
		presentationIndex int
		dataRow           []string
	}
	// Keep the presentation header order. Iterating a map here made identical
	// reports produce different diagnostics across runs, which undermined
	// reproducible validation and made a real mismatch look intermittent.
	entities := make([]transposedEntity, 0, len(presentation.headers)-1)
	for index, header := range presentation.headers[1:] {
		if row, found := dataRows[normalizeRunnerTableToken(header)]; found {
			entities = append(entities, transposedEntity{presentationIndex: index + 1, dataRow: row})
		}
	}
	if len(entities) == 0 {
		return nil
	}

	dataMetrics := make(map[string]int, len(data.headers)-1)
	for index, header := range data.headers[1:] {
		key := canonicalRunnerTransposedMetric(header)
		if key != "" {
			dataMetrics[key] = index + 1
		}
	}
	failures := make([]string, 0)
	matchedMetrics := 0
	for _, row := range presentation.rows {
		if len(row) == 0 {
			continue
		}
		metric := canonicalRunnerTransposedMetric(row[0])
		dataIndex, found := dataMetrics[metric]
		if !found {
			continue
		}
		matchedMetrics++
		for _, entity := range entities {
			presentationIndex, dataRow := entity.presentationIndex, entity.dataRow
			if presentationIndex >= len(row) || dataIndex >= len(dataRow) {
				continue
			}
			leftValue, leftFormat, leftOK := parseRunnerComparableQuantity(row[presentationIndex])
			rightValue, rightFormat, rightOK := parseRunnerComparableQuantity(dataRow[dataIndex])
			if strings.Contains(row[0], "%") || strings.Contains(data.headers[dataIndex], "%") ||
				strings.Contains(row[presentationIndex], "%") || strings.Contains(dataRow[dataIndex], "%") {
				leftFormat.percent, rightFormat.percent = true, true
			}
			if !leftOK || !rightOK || runnerComparableNumbersEqual(
				leftValue, rightValue, leftFormat, rightFormat, metric,
			) {
				continue
			}
			failures = append(failures, fmt.Sprintf(
				"numeric_transposed_table_mismatch:%s<->%s entity=%s metric=%s values=%s|%s",
				data.source, presentation.source, strings.TrimSpace(presentation.headers[presentationIndex]),
				strings.TrimSpace(row[0]), strings.TrimSpace(dataRow[dataIndex]), strings.TrimSpace(row[presentationIndex]),
			))
		}
	}
	if matchedMetrics == 0 {
		return nil
	}
	return failures
}

func legacyCompareRunnerCrossArtifactTables(left, right runnerCrossArtifactTable) []string {
	if left.source == right.source || len(left.headers) < 2 || len(right.headers) < 2 ||
		canonicalRunnerTableHeader(left.headers[0]) != canonicalRunnerTableHeader(right.headers[0]) {
		return nil
	}
	rightHeaders := make(map[string]int, len(right.headers))
	for index, header := range right.headers {
		rightHeaders[canonicalRunnerTableHeader(header)] = index
	}
	shared := make([]runnerCrossArtifactSharedColumn, 0)
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
		return nil
	}
	identityColumns := legacyRunnerCrossArtifactIdentityColumns(left, right, shared)
	if identityColumns < 0 || identityColumns >= len(shared) {
		return nil
	}
	rightRows := make(map[string][]string, len(right.rows))
	for _, row := range right.rows {
		if len(row) > 0 {
			rightRows[runnerCrossArtifactRowKey(row, 0, shared, identityColumns, false)] = row
		}
	}
	failures := make([]string, 0)
	for _, leftRow := range left.rows {
		if len(leftRow) == 0 {
			continue
		}
		rowKey := runnerCrossArtifactRowKey(leftRow, 0, shared, identityColumns, true)
		rightRow, found := rightRows[rowKey]
		if !found {
			rightRow, found = legacyRunnerCrossArtifactApproximateIdentityRow(
				leftRow, right.rows, left.headers[0], shared, identityColumns,
			)
		}
		if !found {
			continue
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
			failures = append(failures, fmt.Sprintf(
				"numeric_table_mismatch:%s<->%s key=%s column=%s values=%s|%s",
				left.source, right.source, runnerCrossArtifactDisplayRowKey(leftRow, shared, identityColumns), column.name,
				strings.TrimSpace(leftRow[column.leftIndex]), strings.TrimSpace(rightRow[column.rightIndex]),
			))
		}
	}
	// A translated or reformatted label is not an identity assertion. Compare
	// only rows whose explicit identity columns resolve to the same value; a
	// matching row count cannot establish that two tables describe the same
	// entities, cohorts, regimens or timepoints.
	return failures
}

// legacyRunnerCrossArtifactApproximateIdentityRow matches presentation-rounded
// numeric row keys to their machine-readable source row. Reports commonly
// render a dose, time, concentration, or score with fewer decimals than the
// companion CSV. Requiring byte-identical numeric labels turns harmless
// presentation rounding into a false cross-artifact failure and can prevent
// the independent scientific reviewer from running. The match is accepted
// only when exactly one source row is equal at the coarser displayed
// precision; ambiguous matches fail closed and retain the original mismatch.
func legacyRunnerCrossArtifactApproximateIdentityRow(
	leftRow []string,
	rightRows [][]string,
	firstHeader string,
	shared []runnerCrossArtifactSharedColumn,
	identityColumns int,
) ([]string, bool) {
	if len(leftRow) == 0 {
		return nil, false
	}
	leftValue, leftFormat, leftOK := parseRunnerComparableNumber(leftRow[0])
	if !leftOK {
		return nil, false
	}
	column := canonicalRunnerTableHeader(firstHeader)
	var matched []string
	for _, candidate := range rightRows {
		if len(candidate) == 0 {
			continue
		}
		rightValue, rightFormat, rightOK := parseRunnerComparableNumber(candidate[0])
		if !rightOK || !runnerComparableNumbersEqual(leftValue, rightValue, leftFormat, rightFormat, column) {
			continue
		}
		identityMatches := true
		for _, identity := range shared[:identityColumns] {
			if identity.leftIndex >= len(leftRow) || identity.rightIndex >= len(candidate) ||
				normalizeRunnerTableIdentityValue(leftRow[identity.leftIndex], identity.canonical) !=
					normalizeRunnerTableIdentityValue(candidate[identity.rightIndex], identity.canonical) {
				identityMatches = false
				break
			}
		}
		if !identityMatches {
			continue
		}
		if matched != nil {
			return nil, false
		}
		matched = candidate
	}
	return matched, matched != nil
}

// legacyRunnerCrossArtifactIdentityColumns returns the smallest shared-column prefix
// needed in addition to column zero to identify every row. This supports
// repeated entities such as one parameter evaluated at +10% and -10% without
// assuming any scientific domain or task-specific column names.
func legacyRunnerCrossArtifactIdentityColumns(left, right runnerCrossArtifactTable, shared []runnerCrossArtifactSharedColumn) int {
	for count := 0; count < len(shared); count++ {
		if legacyRunnerCrossArtifactRowsUnique(left.rows, shared, count, true) &&
			legacyRunnerCrossArtifactRowsUnique(right.rows, shared, count, false) {
			return count
		}
	}
	return -1
}

func legacyRunnerCrossArtifactRowsUnique(rows [][]string, shared []runnerCrossArtifactSharedColumn, identityColumns int, left bool) bool {
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		key := runnerCrossArtifactRowKey(row, 0, shared, identityColumns, left)
		if _, found := seen[key]; found {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

// legacyRunnerCrossArtifactRankFailures makes scenario rankings independently
// auditable. A rank without its underlying score cannot prove that rows were
// mapped back from a sorted index correctly, which is a common numerical error.
func legacyRunnerCrossArtifactRankFailures(table runnerCrossArtifactTable) []string {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(table.source)))
	if extension != ".csv" && extension != ".tsv" {
		return nil
	}
	headers := make(map[string]int, len(table.headers))
	for index, header := range table.headers {
		headers[canonicalRunnerTableHeader(header)] = index
	}

	failures := make([]string, 0)
	for _, pair := range runnerCrossArtifactScenarioRankPairs {
		rankIndex, hasRank := headers[pair.rank]
		if !hasRank {
			continue
		}
		scoreIndex, hasScore := headers[pair.score]
		if !hasScore {
			failures = append(failures, fmt.Sprintf(
				"rank_score_pair_missing:%s rank=%s score=%s", table.source, pair.rank, pair.score,
			))
			continue
		}
		failures = append(failures, legacyRunnerCrossArtifactRankPairFailures(table, scoreIndex, rankIndex, pair)...)
	}
	return failures
}

func legacyRunnerCrossArtifactRankPairFailures(
	table runnerCrossArtifactTable,
	scoreIndex int,
	rankIndex int,
	pair runnerCrossArtifactRankPair,
) []string {
	type rowValue struct {
		key   string
		score float64
		rank  float64
	}
	values := make([]rowValue, 0, len(table.rows))
	failures := make([]string, 0)
	for _, row := range table.rows {
		if len(row) <= scoreIndex || len(row) <= rankIndex || len(row) == 0 {
			continue
		}
		score, _, scoreOK := parseRunnerComparableNumber(row[scoreIndex])
		rank, _, rankOK := parseRunnerComparableNumber(row[rankIndex])
		if !scoreOK || !rankOK {
			failures = append(failures, fmt.Sprintf(
				"rank_score_non_numeric:%s key=%s columns=%s/%s", table.source,
				strings.TrimSpace(row[0]), pair.score, pair.rank,
			))
			continue
		}
		values = append(values, rowValue{key: strings.TrimSpace(row[0]), score: score, rank: rank})
	}
	for _, value := range values {
		expectedRank := 1
		for _, candidate := range values {
			if candidate.score > value.score+1e-12 {
				expectedRank++
			}
		}
		if math.Abs(value.rank-float64(expectedRank)) <= 1e-9 {
			continue
		}
		failures = append(failures, fmt.Sprintf(
			"rank_score_mismatch:%s key=%s columns=%s/%s score=%g rank=%g expected_rank=%d",
			table.source, value.key, pair.score, pair.rank, value.score, value.rank, expectedRank,
		))
	}
	return failures
}
