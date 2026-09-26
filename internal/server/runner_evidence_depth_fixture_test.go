package server

import (
	"context"
	"path/filepath"
	"strings"
)

func runnerMachineValidationFailures(name, content string) []string {
	failures, err := scanRunnerMachineValidation(context.Background(), strings.NewReader(content), name)
	if err != nil {
		panic(err)
	}
	return failures
}

func runnerEvidenceProvenanceFailures(snapshot runnerCrossArtifactSnapshot) []string {
	ext := strings.ToLower(filepath.Ext(snapshot.name))
	if ext != ".csv" && ext != ".tsv" {
		return nil
	}
	var failures []string
	err := visitRunnerEvidenceRows(context.Background(), strings.NewReader(snapshot.text), snapshot.name, runnerEvidenceLedgerHeaderShape,
		func(row int, headers map[string]int, values []string) error {
			failures = append(failures, runnerEvidenceProvenanceTableFailuresAtRow(snapshot.name, [][]string{nil, values}, headers, row)...)
			return nil
		})
	if err != nil {
		panic(err)
	}
	return failures
}

func validateRunnerSourceEvidenceLedger(name string, data []byte, corpus string) []string {
	failures, err := scanRunnerSourceEvidenceLedger(context.Background(), strings.NewReader(string(data)), name, corpus, nil, false)
	if err != nil {
		panic(err)
	}
	return failures
}

// Snapshot fixtures exercise the production row validators through the same
// streaming parser. They do not supply a second whole-file production path.
func runnerEvidenceRecordDepthFailures(snapshot runnerCrossArtifactSnapshot, depth runnerEvidenceRecordDepthIndex) []string {
	var failures []string
	err := visitRunnerEvidenceRows(context.Background(), strings.NewReader(snapshot.text), snapshot.name, nil,
		func(row int, headers map[string]int, values []string) error {
			failures = append(failures, runnerEvidenceRecordDepthTableFailuresAtRow(snapshot.name, [][]string{nil, values}, headers, depth, row)...)
			return nil
		})
	if err != nil {
		panic(err)
	}
	return failures
}

func runnerEvidenceRecordDepthClasses(snapshot runnerCrossArtifactSnapshot, depth runnerEvidenceRecordDepthIndex) map[string]bool {
	return runnerEvidenceDepthFixtureClasses(snapshot, depth, false)
}

func runnerEvidenceRecordDepthVerifiedClasses(snapshot runnerCrossArtifactSnapshot, depth runnerEvidenceRecordDepthIndex) map[string]bool {
	return runnerEvidenceDepthFixtureClasses(snapshot, depth, true)
}

func runnerEvidenceDepthFixtureClasses(snapshot runnerCrossArtifactSnapshot, depth runnerEvidenceRecordDepthIndex, verified bool) map[string]bool {
	result := make(map[string]bool)
	err := visitRunnerEvidenceRows(context.Background(), strings.NewReader(snapshot.text), snapshot.name, nil,
		func(_ int, headers map[string]int, values []string) error {
			var classes map[string]bool
			if verified {
				classes = runnerEvidenceRecordDepthTableVerifiedClasses([][]string{nil, values}, headers, depth)
			} else {
				classes = runnerEvidenceRecordDepthTableClasses([][]string{nil, values}, headers, depth)
			}
			for class := range classes {
				result[class] = true
			}
			return nil
		})
	if err != nil {
		panic(err)
	}
	return result
}

func runnerEvidenceRecordDepthClassIdentifiers(snapshot runnerCrossArtifactSnapshot) map[string][]string {
	result := make(map[string][]string)
	err := visitRunnerEvidenceRows(context.Background(), strings.NewReader(snapshot.text), snapshot.name, nil,
		func(_ int, headers map[string]int, values []string) error {
			for class, identifiers := range runnerEvidenceRecordDepthTableIdentifiers([][]string{nil, values}, headers) {
				result[class] = append(result[class], identifiers...)
			}
			return nil
		})
	if err != nil {
		panic(err)
	}
	for class, identifiers := range result {
		result[class] = uniqueSortedFolded(identifiers)
	}
	return result
}
