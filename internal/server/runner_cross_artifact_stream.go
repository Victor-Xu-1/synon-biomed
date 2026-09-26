package server

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

type runnerArtifactMissingPackage struct{ inventory, name string }

func inspectRunnerArtifactInputs(ctx context.Context, inputs []runnerArtifactScanInput, producedNames map[string]struct{}) (failures []string, resultErr error) {
	store, err := newRunnerArtifactScanStore(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
	var tables []runnerCrossArtifactTable
	tableGroups := make(map[string][]runnerCrossArtifactTable)
	var missing []runnerArtifactMissingPackage
	for _, input := range inputs {
		found, inputTables, packages, err := inspectRunnerArtifactInput(store, input, producedNames)
		if err != nil {
			return nil, err
		}
		failures = append(failures, found...)
		tables = append(tables, inputTables...)
		tableGroups[strings.ToLower(filepath.Base(strings.TrimSpace(input.name)))] = inputTables
		missing = append(missing, packages...)
	}
	tableFailures, err := inspectRunnerArtifactTableSet(store, inputs, tables, tableGroups)
	if err != nil {
		return nil, err
	}
	failures = append(failures, tableFailures...)
	for _, packageRecord := range missing {
		for _, input := range inputs {
			if input.name == packageRecord.inventory {
				continue
			}
			conflict, err := runnerArtifactEnvironmentConflict(ctx, input, packageRecord.name)
			if err != nil {
				return nil, err
			}
			if conflict {
				failures = append(failures, "environment_claim_conflict:"+packageRecord.name+" in "+input.name)
			}
		}
	}
	return failures, nil
}

func inspectRunnerArtifactTableSet(store *runnerArtifactScanStore, inputs []runnerArtifactScanInput, tables []runnerCrossArtifactTable, tableGroups map[string][]runnerCrossArtifactTable) ([]string, error) {
	var failures []string
	contracts, err := inspectRunnerArtifactContracts(store, inputs, tableGroups)
	if err != nil {
		return nil, err
	}
	failures = append(failures, contracts.failures...)
	sort.SliceStable(tables, func(i, j int) bool {
		leftRank, rightRank := runnerCrossArtifactTableSourceRank(tables[i].source), runnerCrossArtifactTableSourceRank(tables[j].source)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return strings.ToLower(tables[i].source) < strings.ToLower(tables[j].source)
	})
	for _, table := range tables {
		found, err := inspectRunnerCrossArtifactRanks(store, table)
		if err != nil {
			return nil, err
		}
		failures = append(failures, found...)
	}
	for left := 0; left < len(tables); left++ {
		for right := left + 1; right < len(tables); right++ {
			pair := runnerCrossArtifactTablePairKey(runnerCrossArtifactTableIdentity(tables[left]), runnerCrossArtifactTableIdentity(tables[right]))
			if _, covered := contracts.covered[pair]; covered {
				continue
			}
			found, err := inspectRunnerCrossArtifactTables(store, tables[left], tables[right])
			if err != nil {
				return nil, err
			}
			failures = append(failures, found...)
			found, err = inspectRunnerCrossArtifactTransposed(store, tables[left], tables[right])
			if err != nil {
				return nil, err
			}
			failures = append(failures, found...)
		}
	}
	return failures, nil
}

func inspectRunnerArtifactInput(store *runnerArtifactScanStore, input runnerArtifactScanInput, produced map[string]struct{}) (failures []string, tables []runnerCrossArtifactTable, packages []runnerArtifactMissingPackage, resultErr error) {
	reader, err := input.openText(store.ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	err = scanRunnerArtifactPattern(store.ctx, reader, runnerCrossArtifactPathPattern, false, func(indices []int64) error {
		name, err := readRunnerArtifactRange(store.ctx, reader, indices[2], indices[3])
		if err != nil {
			return err
		}
		if _, found := produced[strings.ToLower(filepath.Base(name))]; !found {
			failures = append(failures, "missing_artifact_reference:"+name+" in "+input.name)
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, err
	}
	validation, err := scanRunnerMachineValidation(store.ctx, reader, input.name)
	if err != nil {
		return nil, nil, nil, err
	}
	failures = append(failures, validation...)
	validation, err = scanRunnerTemplateFailures(store.ctx, reader, input.name)
	if err != nil {
		return nil, nil, nil, err
	}
	failures = append(failures, validation...)
	ext := strings.ToLower(filepath.Ext(input.name))
	if ext == ".csv" || ext == ".tsv" {
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			return nil, nil, nil, err
		}
		err := visitRunnerEvidenceRows(store.ctx, reader, input.name, runnerEvidenceLedgerHeaderShape, func(row int, headers map[string]int, values []string) error {
			failures = append(failures, runnerEvidenceProvenanceTableFailuresAtRow(input.name, [][]string{nil, values}, headers, row)...)
			return nil
		})
		var parseError *csv.ParseError
		if errors.As(err, &parseError) {
			failures = append(failures, "source_evidence_invalid_delimited:"+input.name)
		} else if err != nil {
			return nil, nil, nil, err
		}
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, err
	}
	tables, err = store.readTables(input.name, reader)
	var parseError *csv.ParseError
	if errors.As(err, &parseError) {
		failures = append(failures, "source_evidence_invalid_delimited:"+input.name)
		tables = nil
	} else if err != nil {
		return nil, nil, nil, err
	}
	lowerName := strings.ToLower(filepath.Base(input.name))
	if strings.Contains(lowerName, "env") && strings.Contains(lowerName, "inventory") {
		seen := make(map[string]bool)
		err = scanRunnerArtifactPattern(store.ctx, reader, runnerCrossArtifactMissingPackagePattern, true, func(indices []int64) error {
			name, err := readRunnerArtifactRange(store.ctx, reader, indices[2], indices[3])
			if err != nil {
				return err
			}
			if !seen[name] {
				packages = append(packages, runnerArtifactMissingPackage{input.name, name})
				seen[name] = true
			}
			return nil
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read artifact environment inventory: %w", err)
		}
	}
	return failures, tables, packages, nil
}
