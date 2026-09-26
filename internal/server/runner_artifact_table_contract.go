package server

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

const runnerArtifactTableContractSchema = "synon.artifact-table-contract.v1"

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
