package server

import (
	"fmt"
	"path/filepath"
	"strings"
)

func inspectRunnerCrossArtifactTransposed(store *runnerArtifactScanStore, left, right runnerCrossArtifactTable) ([]string, error) {
	if !runnerCrossArtifactPresentationPair(left, right) {
		return nil, nil
	}
	presentation, data := left, right
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(presentation.source)), ".md") {
		presentation, data = data, presentation
	}
	if len(presentation.headers) < 2 || len(data.headers) < 2 {
		return nil, nil
	}
	dataIndex, _, err := store.indexRows(data, func(row []string) string { return normalizeRunnerTableToken(row[0]) }, false)
	if err != nil {
		return nil, err
	}
	type entity struct {
		presentationIndex int
		row               []string
	}
	var entities []entity
	for index, header := range presentation.headers[1:] {
		row, found, err := store.findIndexedRow(data, dataIndex, normalizeRunnerTableToken(header))
		if err != nil {
			return nil, err
		}
		if found {
			entities = append(entities, entity{index + 1, row})
		}
	}
	if _, err := store.exec(`DELETE FROM scan_keys WHERE scope=?`, dataIndex); err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	metrics := make(map[string]int, len(data.headers)-1)
	for index, header := range data.headers[1:] {
		if key := canonicalRunnerTransposedMetric(header); key != "" {
			metrics[key] = index + 1
		}
	}
	var failures []string
	err = presentation.eachRow(store.ctx, func(_ int, row []string) error {
		if len(row) == 0 {
			return nil
		}
		metric := canonicalRunnerTransposedMetric(row[0])
		column, found := metrics[metric]
		if !found {
			return nil
		}
		for _, entity := range entities {
			presentationIndex, dataRow := entity.presentationIndex, entity.row
			if presentationIndex >= len(row) || column >= len(dataRow) {
				continue
			}
			leftValue, leftFormat, leftOK := parseRunnerComparableQuantity(row[presentationIndex])
			rightValue, rightFormat, rightOK := parseRunnerComparableQuantity(dataRow[column])
			if strings.Contains(row[0], "%") || strings.Contains(data.headers[column], "%") || strings.Contains(row[presentationIndex], "%") || strings.Contains(dataRow[column], "%") {
				leftFormat.percent, rightFormat.percent = true, true
			}
			if !leftOK || !rightOK || runnerComparableNumbersEqual(leftValue, rightValue, leftFormat, rightFormat, metric) {
				continue
			}
			failures = append(failures, fmt.Sprintf("numeric_transposed_table_mismatch:%s<->%s entity=%s metric=%s values=%s|%s", data.source, presentation.source, strings.TrimSpace(presentation.headers[presentationIndex]), strings.TrimSpace(row[0]), strings.TrimSpace(dataRow[column]), strings.TrimSpace(row[presentationIndex])))
		}
		return nil
	})
	return failures, err
}
