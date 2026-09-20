package server

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

type runnerCrossArtifactTable struct {
	source    string
	ordinal   int
	headers   []string
	rows      [][]string
	scanStore *runnerArtifactScanStore
	scanScope int64
}

type runnerCrossArtifactSharedColumn struct {
	leftIndex  int
	rightIndex int
	name       string
	canonical  string
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
