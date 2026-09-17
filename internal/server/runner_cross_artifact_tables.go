package server

import (
	"encoding/csv"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type runnerCrossArtifactTable struct {
	source  string
	ordinal int
	headers []string
	rows    [][]string
}

type runnerCrossArtifactSharedColumn struct {
	leftIndex  int
	rightIndex int
	name       string
	canonical  string
}

func runnerCrossArtifactTableFailures(snapshots []runnerCrossArtifactSnapshot) []string {
	contractValidation := runnerCrossArtifactContractValidation(snapshots)
	tables := make([]runnerCrossArtifactTable, 0)
	for _, snapshot := range snapshots {
		tables = append(tables, runnerCrossArtifactTables(snapshot)...)
	}
	sort.SliceStable(tables, func(i, j int) bool {
		leftRank, rightRank := runnerCrossArtifactTableSourceRank(tables[i].source), runnerCrossArtifactTableSourceRank(tables[j].source)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return strings.ToLower(tables[i].source) < strings.ToLower(tables[j].source)
	})
	failures := append([]string(nil), contractValidation.failures...)
	for _, table := range tables {
		failures = append(failures, runnerCrossArtifactRankFailures(table)...)
	}
	for left := 0; left < len(tables); left++ {
		for right := left + 1; right < len(tables); right++ {
			pair := runnerCrossArtifactTablePairKey(
				runnerCrossArtifactTableIdentity(tables[left]), runnerCrossArtifactTableIdentity(tables[right]),
			)
			if _, explicitlyCovered := contractValidation.covered[pair]; explicitlyCovered {
				continue
			}
			failures = append(failures, compareRunnerCrossArtifactTables(tables[left], tables[right])...)
			failures = append(failures, compareRunnerCrossArtifactTransposedTables(tables[left], tables[right])...)
		}
	}
	return failures
}

// compareRunnerCrossArtifactTransposedTables covers a common report layout:
// machine-readable data keeps entities in rows and measurements in columns,
// while a Markdown comparison table presents entities in columns and
// measurements in rows. The ordinary table comparer intentionally cannot
// match those orientations. This bounded presentation-vs-data comparison uses
// only intersecting labels and numeric cells; it neither infers missing values
// nor assigns scientific meaning to a column.
func compareRunnerCrossArtifactTransposedTables(left, right runnerCrossArtifactTable) []string {
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

func canonicalRunnerTransposedMetric(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexAny(value, "(（"); index > 0 {
		value = value[:index]
	}
	return normalizeRunnerTableToken(value)
}

func parseRunnerComparableQuantity(value string) (float64, runnerComparableNumberFormat, bool) {
	if number, format, ok := parseRunnerComparableNumber(value); ok {
		return number, format, true
	}
	value = strings.TrimSpace(strings.Trim(value, "*_`\u00a0"))
	end := 0
	for index, char := range value {
		if (char >= '0' && char <= '9') || char == '+' || char == '-' || char == '.' || char == ',' {
			end = index + len(string(char))
			continue
		}
		break
	}
	if end == 0 {
		return 0, runnerComparableNumberFormat{}, false
	}
	numberText := strings.TrimSpace(value[:end])
	number, format, ok := parseRunnerComparableNumber(numberText)
	if !ok {
		return 0, format, false
	}
	format.percent = strings.Contains(value[end:], "%")
	return number, format, true
}

func runnerCrossArtifactTableSourceRank(name string) int {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".csv", ".tsv":
		return 0
	case ".md":
		return 1
	default:
		return 2
	}
}

func runnerCrossArtifactTables(snapshot runnerCrossArtifactSnapshot) []runnerCrossArtifactTable {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(snapshot.name))) {
	case ".csv":
		return runnerDelimitedArtifactTables(snapshot, ',')
	case ".tsv":
		return runnerDelimitedArtifactTables(snapshot, '\t')
	case ".md":
		return runnerMarkdownArtifactTables(snapshot)
	default:
		return nil
	}
}

func runnerDelimitedArtifactTables(snapshot runnerCrossArtifactSnapshot, comma rune) []runnerCrossArtifactTable {
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(snapshot.text, "\ufeff")))
	reader.Comma = comma
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 || len(records[0]) < 2 {
		return nil
	}
	return []runnerCrossArtifactTable{{source: snapshot.name, ordinal: 0, headers: records[0], rows: records[1:]}}
}

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

func splitRunnerMarkdownTableLine(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "|") {
		return nil, false
	}
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	if len(parts) < 2 {
		return nil, false
	}
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts, true
}

func runnerMarkdownSeparatorRow(cells []string) bool {
	for _, cell := range cells {
		cell = strings.Trim(strings.TrimSpace(cell), ":")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func compareRunnerCrossArtifactTables(left, right runnerCrossArtifactTable) []string {
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
	identityColumns := runnerCrossArtifactIdentityColumns(left, right, shared)
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
			rightRow, found = runnerCrossArtifactApproximateIdentityRow(
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

// runnerCrossArtifactApproximateIdentityRow matches presentation-rounded
// numeric row keys to their machine-readable source row. Reports commonly
// render a dose, time, concentration, or score with fewer decimals than the
// companion CSV. Requiring byte-identical numeric labels turns harmless
// presentation rounding into a false cross-artifact failure and can prevent
// the independent scientific reviewer from running. The match is accepted
// only when exactly one source row is equal at the coarser displayed
// precision; ambiguous matches fail closed and retain the original mismatch.
func runnerCrossArtifactApproximateIdentityRow(
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

func runnerCrossArtifactPresentationPair(left, right runnerCrossArtifactTable) bool {
	leftPresentation := strings.EqualFold(filepath.Ext(strings.TrimSpace(left.source)), ".md")
	rightPresentation := strings.EqualFold(filepath.Ext(strings.TrimSpace(right.source)), ".md")
	return leftPresentation != rightPresentation
}

func runnerCrossArtifactDisplayRowKey(row []string, shared []runnerCrossArtifactSharedColumn, identityColumns int) string {
	parts := []string{strings.TrimSpace(row[0])}
	for _, column := range shared[:identityColumns] {
		value := ""
		if column.leftIndex < len(row) {
			value = normalizeRunnerTableIdentityValue(row[column.leftIndex], column.canonical)
		}
		parts = append(parts, column.canonical+"="+value)
	}
	return strings.Join(parts, "/")
}

// runnerCrossArtifactIdentityColumns returns the smallest shared-column prefix
// needed in addition to column zero to identify every row. This supports
// repeated entities such as one parameter evaluated at +10% and -10% without
// assuming any scientific domain or task-specific column names.
func runnerCrossArtifactIdentityColumns(left, right runnerCrossArtifactTable, shared []runnerCrossArtifactSharedColumn) int {
	for count := 0; count < len(shared); count++ {
		if runnerCrossArtifactRowsUnique(left.rows, shared, count, true) &&
			runnerCrossArtifactRowsUnique(right.rows, shared, count, false) {
			return count
		}
	}
	return -1
}

func runnerCrossArtifactRowsUnique(rows [][]string, shared []runnerCrossArtifactSharedColumn, identityColumns int, left bool) bool {
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

func runnerCrossArtifactRowKey(row []string, firstIndex int, shared []runnerCrossArtifactSharedColumn, identityColumns int, left bool) string {
	parts := []string{normalizeRunnerTableToken(row[firstIndex])}
	for _, column := range shared[:identityColumns] {
		index := column.rightIndex
		if left {
			index = column.leftIndex
		}
		if index >= len(row) {
			parts = append(parts, "")
			continue
		}
		parts = append(parts, normalizeRunnerTableIdentityValue(row[index], column.canonical))
	}
	return strings.Join(parts, "\x1f")
}

func normalizeRunnerTableIdentityValue(value, column string) string {
	if number, _, ok := parseRunnerComparableNumber(value); ok {
		return strconv.FormatFloat(number, 'g', -1, 64)
	}
	return normalizeRunnerTableToken(value)
}

type runnerComparableNumberFormat struct {
	decimals int
	percent  bool
}

func parseRunnerComparableNumber(value string) (float64, runnerComparableNumberFormat, bool) {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.Trim(cleaned, "*_`\u00a0")
	format := runnerComparableNumberFormat{percent: strings.HasSuffix(cleaned, "%")}
	cleaned = strings.TrimSpace(strings.TrimSuffix(cleaned, "%"))
	cleaned = strings.ReplaceAll(cleaned, ",", "")
	if cleaned == "" || strings.ContainsAny(cleaned, "–—~≈<>?") {
		return 0, format, false
	}
	number, err := strconv.ParseFloat(cleaned, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, format, false
	}
	format.decimals = runnerDisplayedDecimals(cleaned)
	return number, format, true
}

func runnerDisplayedDecimals(value string) int {
	value = strings.ToLower(value)
	if exponent := strings.IndexByte(value, 'e'); exponent >= 0 {
		value = value[:exponent]
	}
	point := strings.IndexByte(value, '.')
	if point < 0 {
		return 0
	}
	return len(value) - point - 1
}

func runnerComparableNumbersEqual(left, right float64, leftFormat, rightFormat runnerComparableNumberFormat, column string) bool {
	if leftFormat.percent != rightFormat.percent && !strings.HasSuffix(column, "_percent") {
		return false
	}
	precision := leftFormat.decimals
	if rightFormat.decimals < precision {
		precision = rightFormat.decimals
	}
	tolerance := 0.5 * math.Pow10(-precision)
	if tolerance < 1e-9 {
		tolerance = 1e-9
	}
	return math.Abs(left-right) <= tolerance+1e-12
}

func canonicalRunnerTableHeader(value string) string {
	normalized := normalizeRunnerTableToken(value)
	switch normalized {
	case "parameter", "参数":
		return "parameter"
	case "change_percent", "change(%)", "变化幅度":
		return "change_percent"
	case "relative_change_k", "relativechange(%)", "降解速率相对变化":
		return "relative_change_k"
	case "form", "形态", "晶型":
		return "form"
	case "type", "类型":
		return "type"
	case "topsis_score", "topsis综合得分", "topsis得分", "综合得分":
		return "topsis_score"
	case "rank", "排名":
		return "rank"
	case "rank_pessimistic", "pessimistic_rank", "悲观情景排名", "悲观排名":
		return "rank_pessimistic"
	case "rank_neutral", "neutral_rank", "中性情景排名", "中性排名":
		return "rank_neutral"
	case "rank_optimistic", "optimistic_rank", "乐观情景排名", "乐观排名":
		return "rank_optimistic"
	case "score_pessimistic", "pessimistic_score", "悲观情景得分", "悲观得分":
		return "score_pessimistic"
	case "score_neutral", "neutral_score", "中性情景得分", "中性得分":
		return "score_neutral"
	case "score_optimistic", "optimistic_score", "乐观情景得分", "乐观得分":
		return "score_optimistic"
	case "physical_stability_score", "物理稳定性":
		return "physical_stability_score"
	case "first_rank_count", "排名第一次数":
		return "first_rank_count"
	case "first_rank_percent", "占比":
		return "first_rank_percent"
	case "mean_score", "平均得分":
		return "mean_score"
	case "solubility", "溶解度(mg/ml)":
		return "solubility"
	case "idr", "idr(mg/cm²/min)":
		return "idr"
	case "dvs_gain", "dvs增重(%)":
		return "dvs_gain"
	case "relative_lattice_energy", "相对晶格能(kj/mol)":
		return "relative_lattice_energy"
	case "residual_ethanol", "残留乙醇(%)":
		return "residual_ethanol"
	default:
		return normalized
	}
}

func normalizeRunnerTableToken(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "*_`\u00a0"))
	value = strings.ToLower(value)
	value = strings.Join(strings.Fields(value), "")
	return value
}
