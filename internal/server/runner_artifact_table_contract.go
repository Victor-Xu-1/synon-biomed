package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const runnerArtifactTableContractSchema = "synon.artifact-table-contract.v1"

type runnerArtifactTableContractDocument struct {
	Schema string                        `json:"schema"`
	Tables []runnerArtifactTableContract `json:"tables"`
}

type runnerArtifactTableContract struct {
	ID             string                                  `json:"id"`
	IdentityFields []string                                `json:"identity_fields"`
	CompareFields  []string                                `json:"compare_fields"`
	Records        []map[string]string                     `json:"records"`
	Projections    []runnerArtifactTableContractProjection `json:"projections"`
}

type runnerArtifactTableContractProjection struct {
	Artifact   string            `json:"artifact"`
	TableIndex int               `json:"table_index"`
	Columns    map[string]string `json:"columns"`
}

type runnerArtifactTableContractValidation struct {
	failures []string
	covered  map[string]struct{}
}

// runnerCrossArtifactContractValidation validates only an explicit shared
// record contract. File names, translated labels and equal row counts never
// create an implicit identity relationship.
func runnerCrossArtifactContractValidation(snapshots []runnerCrossArtifactSnapshot) runnerArtifactTableContractValidation {
	validation := runnerArtifactTableContractValidation{covered: make(map[string]struct{})}
	tablesByArtifact := make(map[string][]runnerCrossArtifactTable)
	for _, snapshot := range snapshots {
		name := strings.ToLower(filepath.Base(strings.TrimSpace(snapshot.name)))
		if name != "" {
			tablesByArtifact[name] = runnerCrossArtifactTables(snapshot)
		}
	}
	seenContracts := map[string]struct{}{}
	for _, snapshot := range snapshots {
		document, matched, valid := decodeRunnerArtifactTableContract(snapshot.text)
		if !matched {
			continue
		}
		if !valid {
			validation.failures = append(validation.failures,
				"artifact_table_contract_invalid:"+filepath.Base(snapshot.name)+" reason=invalid_schema")
			continue
		}
		for _, contract := range document.Tables {
			contractID := strings.TrimSpace(contract.ID)
			if !validRunnerArtifactTableContract(contract) {
				validation.failures = append(validation.failures,
					"artifact_table_contract_invalid:"+filepath.Base(snapshot.name)+" reason=invalid_table")
				continue
			}
			if _, duplicate := seenContracts[strings.ToLower(contractID)]; duplicate {
				validation.failures = append(validation.failures,
					"artifact_table_contract_invalid:"+filepath.Base(snapshot.name)+" reason=duplicate_id")
				continue
			}
			seenContracts[strings.ToLower(contractID)] = struct{}{}
			validation.validateTable(contract, tablesByArtifact)
		}
	}
	sort.Strings(validation.failures)
	return validation
}

func decodeRunnerArtifactTableContract(content string) (runnerArtifactTableContractDocument, bool, bool) {
	var probe struct {
		Schema string `json:"schema"`
	}
	if json.Unmarshal([]byte(content), &probe) != nil || probe.Schema != runnerArtifactTableContractSchema {
		return runnerArtifactTableContractDocument{}, false, false
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(content)))
	decoder.DisallowUnknownFields()
	var document runnerArtifactTableContractDocument
	if decoder.Decode(&document) != nil || decoder.Decode(&struct{}{}) != io.EOF || document.Schema != runnerArtifactTableContractSchema ||
		len(document.Tables) == 0 || len(document.Tables) > 16 {
		return runnerArtifactTableContractDocument{}, true, false
	}
	return document, true, true
}

func validRunnerArtifactTableContract(contract runnerArtifactTableContract) bool {
	if !boundedRunnerContractToken(contract.ID) || len(contract.IdentityFields) == 0 || len(contract.IdentityFields) > 8 ||
		len(contract.CompareFields) == 0 || len(contract.CompareFields) > 64 || len(contract.Records) == 0 || len(contract.Records) > 2000 ||
		len(contract.Projections) < 2 || len(contract.Projections) > 16 {
		return false
	}
	fields := append(append([]string(nil), contract.IdentityFields...), contract.CompareFields...)
	fieldSet := map[string]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if !boundedRunnerContractToken(field) {
			return false
		}
		key := strings.ToLower(field)
		if _, duplicate := fieldSet[key]; duplicate {
			return false
		}
		fieldSet[key] = struct{}{}
	}
	for _, record := range contract.Records {
		if len(record) != len(fieldSet) {
			return false
		}
		for field := range fieldSet {
			value, found := runnerContractMapValue(record, field)
			if !found || strings.TrimSpace(value) == "" || len(value) > 4096 {
				return false
			}
		}
	}
	for _, projection := range contract.Projections {
		artifact := strings.TrimSpace(projection.Artifact)
		if artifact == "" || filepath.Base(artifact) != artifact || len(artifact) > 255 || projection.TableIndex < 0 ||
			len(projection.Columns) != len(fieldSet) {
			return false
		}
		for field := range fieldSet {
			header, found := runnerContractMapValue(projection.Columns, field)
			if !found || strings.TrimSpace(header) == "" || len(header) > 512 {
				return false
			}
		}
	}
	return true
}

func (validation *runnerArtifactTableContractValidation) validateTable(contract runnerArtifactTableContract, tablesByArtifact map[string][]runnerCrossArtifactTable) {
	fields := append(append([]string(nil), contract.IdentityFields...), contract.CompareFields...)
	records := make(map[string]map[string]string, len(contract.Records))
	displayKeys := make(map[string]string, len(contract.Records))
	for _, record := range contract.Records {
		key, display := runnerArtifactTableContractRecordKey(record, contract.IdentityFields)
		if key == "" || records[key] != nil {
			validation.failures = append(validation.failures,
				"artifact_table_contract_invalid:"+contract.ID+" reason=duplicate_record_identity")
			return
		}
		records[key], displayKeys[key] = record, display
	}
	projectionKeys := make([]string, 0, len(contract.Projections))
	for _, projection := range contract.Projections {
		artifactKey := strings.ToLower(filepath.Base(projection.Artifact))
		tables := tablesByArtifact[artifactKey]
		if projection.TableIndex >= len(tables) {
			validation.failures = append(validation.failures, fmt.Sprintf(
				"artifact_table_contract_projection_missing:%s artifact=%s table_index=%d",
				contract.ID, projection.Artifact, projection.TableIndex))
			continue
		}
		table := tables[projection.TableIndex]
		projectionKey := runnerCrossArtifactTableIdentity(table)
		projectionKeys = append(projectionKeys, projectionKey)
		columns, ok := runnerArtifactTableContractColumns(table, projection.Columns, fields)
		if !ok {
			validation.failures = append(validation.failures, fmt.Sprintf(
				"artifact_table_contract_projection_invalid:%s artifact=%s table_index=%d",
				contract.ID, projection.Artifact, projection.TableIndex))
			continue
		}
		seenRows := map[string]struct{}{}
		for _, row := range table.rows {
			actual := make(map[string]string, len(fields))
			for _, field := range fields {
				index := columns[strings.ToLower(field)]
				if index < len(row) {
					actual[field] = strings.TrimSpace(row[index])
				}
			}
			key, display := runnerArtifactTableContractRecordKey(actual, contract.IdentityFields)
			if _, duplicate := seenRows[key]; duplicate || key == "" {
				validation.failures = append(validation.failures, fmt.Sprintf(
					"artifact_table_contract_projection_invalid:%s artifact=%s reason=duplicate_or_empty_identity",
					contract.ID, projection.Artifact))
				continue
			}
			seenRows[key] = struct{}{}
			expected := records[key]
			if expected == nil {
				validation.failures = append(validation.failures, fmt.Sprintf(
					"artifact_table_contract_row_unexpected:%s artifact=%s row=%s", contract.ID, projection.Artifact, display))
				continue
			}
			for _, field := range contract.CompareFields {
				expectedValue, _ := runnerContractMapValue(expected, field)
				actualValue, _ := runnerContractMapValue(actual, field)
				if runnerArtifactTableContractValuesEqual(expectedValue, actualValue, field) {
					continue
				}
				validation.failures = append(validation.failures, fmt.Sprintf(
					"artifact_table_contract_value_mismatch:%s artifact=%s row=%s field=%s values=%s|%s",
					contract.ID, projection.Artifact, displayKeys[key], field,
					truncateServerString(expectedValue, 160), truncateServerString(actualValue, 160)))
			}
		}
		for key, display := range displayKeys {
			if _, found := seenRows[key]; !found {
				validation.failures = append(validation.failures, fmt.Sprintf(
					"artifact_table_contract_row_missing:%s artifact=%s row=%s", contract.ID, projection.Artifact, display))
			}
		}
	}
	for left := 0; left < len(projectionKeys); left++ {
		for right := left + 1; right < len(projectionKeys); right++ {
			validation.covered[runnerCrossArtifactTablePairKey(projectionKeys[left], projectionKeys[right])] = struct{}{}
		}
	}
}

func runnerArtifactTableContractColumns(table runnerCrossArtifactTable, declared map[string]string, fields []string) (map[string]int, bool) {
	headers := make(map[string]int, len(table.headers))
	for index, header := range table.headers {
		key := normalizeRunnerTableToken(header)
		if _, duplicate := headers[key]; duplicate || key == "" {
			return nil, false
		}
		headers[key] = index
	}
	result := make(map[string]int, len(fields))
	for _, field := range fields {
		header, _ := runnerContractMapValue(declared, field)
		index, found := headers[normalizeRunnerTableToken(header)]
		if !found {
			return nil, false
		}
		result[strings.ToLower(field)] = index
	}
	return result, true
}

func runnerArtifactTableContractRecordKey(record map[string]string, identityFields []string) (string, string) {
	keyParts, displayParts := make([]string, 0, len(identityFields)), make([]string, 0, len(identityFields))
	for _, field := range identityFields {
		value, found := runnerContractMapValue(record, field)
		if !found || strings.TrimSpace(value) == "" {
			return "", ""
		}
		keyParts = append(keyParts, normalizeRunnerTableIdentityValue(value, strings.ToLower(field)))
		displayParts = append(displayParts, strings.TrimSpace(value))
	}
	return strings.Join(keyParts, "\x1f"), strings.Join(displayParts, "/")
}

func runnerArtifactTableContractValuesEqual(left, right, field string) bool {
	if leftNumber, leftFormat, leftOK := parseRunnerComparableNumber(left); leftOK {
		if rightNumber, rightFormat, rightOK := parseRunnerComparableNumber(right); rightOK {
			return runnerComparableNumbersEqual(leftNumber, rightNumber, leftFormat, rightFormat, strings.ToLower(field))
		}
	}
	left, right = strings.Join(strings.Fields(left), " "), strings.Join(strings.Fields(right), " ")
	leftURL, leftErr := url.ParseRequestURI(left)
	rightURL, rightErr := url.ParseRequestURI(right)
	if leftErr == nil && rightErr == nil && leftURL.Scheme != "" && rightURL.Scheme != "" {
		return left == right
	}
	return strings.EqualFold(left, right)
}

func runnerContractMapValue(values map[string]string, key string) (string, bool) {
	for candidate, value := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(key)) {
			return value, true
		}
	}
	return "", false
}

func boundedRunnerContractToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f || character == '/' || character == '\\' {
			return false
		}
	}
	return true
}

func runnerCrossArtifactTableIdentity(table runnerCrossArtifactTable) string {
	return strings.ToLower(filepath.Base(strings.TrimSpace(table.source))) + "#" + strconv.Itoa(table.ordinal)
}

func runnerCrossArtifactTablePairKey(left, right string) string {
	if left > right {
		left, right = right, left
	}
	return left + "\x00" + right
}
