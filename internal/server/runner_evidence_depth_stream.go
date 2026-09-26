package server

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strings"
)

type runnerEvidenceDepthScan struct {
	failures       []string
	present        map[string]bool
	verified       map[string]bool
	representative map[string]string
}

// Parsing retains one record. Aggregate class state retains only the single
// representative used in a correction, not every identifier in a large table.
func scanRunnerEvidenceDepthLedger(ctx context.Context, source io.Reader, name string, depth runnerEvidenceRecordDepthIndex) (runnerEvidenceDepthScan, error) {
	result := runnerEvidenceDepthScan{present: make(map[string]bool), verified: make(map[string]bool), representative: make(map[string]string)}
	err := visitRunnerEvidenceRows(ctx, source, name, runnerEvidenceLedgerHeaderShape,
		func(rowNumber int, headers map[string]int, row []string) error {
			records := [][]string{nil, row}
			result.failures = append(result.failures, runnerEvidenceRecordDepthTableFailuresAtRow(name, records, headers, depth, rowNumber)...)
			for class := range runnerEvidenceRecordDepthTableClasses(records, headers, depth) {
				result.present[class] = true
			}
			for class := range runnerEvidenceRecordDepthTableVerifiedClasses(records, headers, depth) {
				result.verified[class] = true
			}
			for class, identifiers := range runnerEvidenceRecordDepthTableIdentifiers(records, headers) {
				for _, identifier := range identifiers {
					retainRunnerEvidenceRepresentative(result.representative, class, identifier)
				}
			}
			return nil
		})
	if ctx != nil && ctx.Err() != nil {
		return runnerEvidenceDepthScan{}, ctx.Err()
	}
	var parseError *csv.ParseError
	if errors.As(err, &parseError) {
		// Malformed model-authored data enters the existing artifact repair
		// path; cancellation and storage failures are not misreported as data.
		return runnerEvidenceDepthScan{failures: []string{"source_evidence_invalid_delimited:" + name}}, nil
	}
	if err != nil {
		return runnerEvidenceDepthScan{}, err
	}
	return result, nil
}

func retainRunnerEvidenceRepresentative(target map[string]string, class, identifier string) {
	if previous := target[class]; previous == "" || strings.ToLower(identifier) < strings.ToLower(previous) {
		target[class] = identifier
	}
}
