package server

import (
	"fmt"
	"path/filepath"
	"strings"
)

func (validation *runnerArtifactTableContractValidation) inspectTable(store *runnerArtifactScanStore, contract runnerArtifactTableContract, tablesByArtifact map[string][]runnerCrossArtifactTable) error {
	fields := append(append([]string(nil), contract.IdentityFields...), contract.CompareFields...)
	records := make(map[string]map[string]string, len(contract.Records))
	displayKeys := make(map[string]string, len(contract.Records))
	for _, record := range contract.Records {
		key, display := runnerArtifactTableContractRecordKey(record, contract.IdentityFields)
		if key == "" || records[key] != nil {
			validation.failures = append(validation.failures, "artifact_table_contract_invalid:"+contract.ID+" reason=duplicate_record_identity")
			return nil
		}
		records[key], displayKeys[key] = record, display
	}
	projectionKeys := make([]string, 0, len(contract.Projections))
	for _, projection := range contract.Projections {
		artifactKey := strings.ToLower(filepath.Base(projection.Artifact))
		tables := tablesByArtifact[artifactKey]
		if projection.TableIndex >= len(tables) {
			validation.failures = append(validation.failures, fmt.Sprintf("artifact_table_contract_projection_missing:%s artifact=%s table_index=%d", contract.ID, projection.Artifact, projection.TableIndex))
			continue
		}
		table := tables[projection.TableIndex]
		projectionKeys = append(projectionKeys, runnerCrossArtifactTableIdentity(table))
		columns, ok := runnerArtifactTableContractColumns(table, projection.Columns, fields)
		if !ok {
			validation.failures = append(validation.failures, fmt.Sprintf("artifact_table_contract_projection_invalid:%s artifact=%s table_index=%d", contract.ID, projection.Artifact, projection.TableIndex))
			continue
		}
		seenScope := store.scope()
		err := table.eachRow(store.ctx, func(ordinal int, row []string) error {
			actual := make(map[string]string, len(fields))
			for _, field := range fields {
				index := columns[strings.ToLower(field)]
				if index < len(row) {
					actual[field] = strings.TrimSpace(row[index])
				}
			}
			key, display := runnerArtifactTableContractRecordKey(actual, contract.IdentityFields)
			duplicate := key == ""
			if !duplicate {
				result, err := store.exec(`INSERT INTO scan_keys(scope,key,ordinal) VALUES(?,?,?) ON CONFLICT(scope,key) DO NOTHING`, seenScope, key, ordinal)
				if err != nil {
					return err
				}
				count, err := result.RowsAffected()
				if err != nil {
					return err
				}
				duplicate = count == 0
			}
			if duplicate {
				validation.failures = append(validation.failures, fmt.Sprintf("artifact_table_contract_projection_invalid:%s artifact=%s reason=duplicate_or_empty_identity", contract.ID, projection.Artifact))
				return nil
			}
			expected := records[key]
			if expected == nil {
				validation.failures = append(validation.failures, fmt.Sprintf("artifact_table_contract_row_unexpected:%s artifact=%s row=%s", contract.ID, projection.Artifact, display))
				return nil
			}
			for _, field := range contract.CompareFields {
				expectedValue, _ := runnerContractMapValue(expected, field)
				actualValue, _ := runnerContractMapValue(actual, field)
				if runnerArtifactTableContractValuesEqual(expectedValue, actualValue, field) {
					continue
				}
				validation.failures = append(validation.failures, fmt.Sprintf("artifact_table_contract_value_mismatch:%s artifact=%s row=%s field=%s values=%s|%s", contract.ID, projection.Artifact, displayKeys[key], field, expectedValue, actualValue))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for key, display := range displayKeys {
			var count int
			if err := store.tx.QueryRowContext(store.ctx, `SELECT COUNT(*) FROM scan_keys WHERE scope=? AND key=?`, seenScope, key).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				validation.failures = append(validation.failures, fmt.Sprintf("artifact_table_contract_row_missing:%s artifact=%s row=%s", contract.ID, projection.Artifact, display))
			}
		}
		if _, err := store.exec(`DELETE FROM scan_keys WHERE scope=?`, seenScope); err != nil {
			return err
		}
	}
	for left := 0; left < len(projectionKeys); left++ {
		for right := left + 1; right < len(projectionKeys); right++ {
			validation.covered[runnerCrossArtifactTablePairKey(projectionKeys[left], projectionKeys[right])] = struct{}{}
		}
	}
	return nil
}
