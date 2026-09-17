package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var errAgentSavedArtifactDelimitedInvalid = errors.New("saved delimited artifact is invalid")

// validateAgentSavedArtifactDelimited prevents structurally malformed CSV and
// TSV files from becoming canonical artifacts. It validates record boundaries
// and a consistent field count without imposing a domain schema or requiring
// non-empty data rows.
func validateAgentSavedArtifactDelimited(path string, snapshot *os.File) error {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	if extension != ".csv" && extension != ".tsv" {
		return nil
	}
	if snapshot == nil {
		return errAgentSavedArtifactDelimitedInvalid
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := csv.NewReader(snapshot)
	if extension == ".tsv" {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = 0
	record := 0
	for {
		_, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		record++
		if err != nil {
			_, _ = snapshot.Seek(0, io.SeekStart)
			return fmt.Errorf("%w at record %d: %v", errAgentSavedArtifactDelimitedInvalid, record, err)
		}
	}
	_, err := snapshot.Seek(0, io.SeekStart)
	return err
}

func validateAgentSavedArtifactDelimitedBytes(path string, data []byte) error {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	if extension != ".csv" && extension != ".tsv" {
		return nil
	}
	reader := csv.NewReader(strings.NewReader(string(data)))
	if extension == ".tsv" {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = 0
	record := 0
	for {
		_, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		record++
		if err != nil {
			return fmt.Errorf("%w at record %d: %v", errAgentSavedArtifactDelimitedInvalid, record, err)
		}
	}
}
