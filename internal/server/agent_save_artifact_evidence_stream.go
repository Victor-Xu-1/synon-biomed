package server

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"synon-go/internal/agentruntime"
)

// Citation semantics are line/record based. Retain their local scope, but never
// turn the preview budget into a complete-artifact validation limit. A single
// exceptionally wide record and the unique diagnostics still consume memory.
func scanAgentSavedArtifactEvidence(ctx context.Context, path string, source io.ReadSeeker, evidence []agentruntime.Message, citations bool, evidenceTool func(string) bool) ([]string, error) {
	var failures []string
	if citations {
		candidates, identity, err := scanAgentSavedCitationCandidates(ctx, source, path, runnerCitationIdentityIndexFromMessages(evidence))
		if err != nil {
			return nil, err
		}
		failures = append(failures, identity...)
		failures = append(failures, unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(evidence, len(evidence), "", nil, &candidates, evidenceTool)...)
	}
	if strings.TrimSpace(path) == "" {
		return failures, nil
	}
	depth := runnerEvidenceRecordDepthIndexFromMessages(evidence)
	corpus := runnerSourceEvidenceCorpus(evidence)
	visit := func(row int, headers map[string]int, values []string) error {
		records := [][]string{nil, values}
		failures = append(failures, runnerEvidenceProvenanceTableFailuresAtRow(path, records, headers, row)...)
		failures = append(failures, runnerEvidenceRecordDepthTableFailuresAtRow(path, records, headers, depth, row)...)
		failures = append(failures, validateRunnerSourceEvidenceLedgerRecordsAtRow(path, records, headers, corpus, row)...)
		return nil
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := newAgentEvidenceRedactedReader(ctx, source)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".tsv":
		err := visitRunnerEvidenceRows(ctx, reader, path, runnerEvidenceLedgerHeaderShape, visit)
		var malformed *csv.ParseError
		if errors.As(err, &malformed) {
			failures = append(failures, "source_evidence_invalid_delimited:"+path)
		} else if err != nil {
			return nil, err
		}
	case ".md", ".markdown":
		err := visitRunnerMarkdownRows(ctx, reader, func(_ int, row int, header, values []string) error {
			headers := make(map[string]int, len(header))
			for index, value := range header {
				headers[normalizeRunnerTableToken(value)] = index
			}
			if runnerEvidenceLedgerHeaderShape(headers) {
				return visit(row, headers, values)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return failures, nil
}

type agentEvidenceRedactedReader struct {
	reader  *bufio.Reader
	pending string
	err     error
}

func newAgentEvidenceRedactedReader(ctx context.Context, source io.Reader) *agentEvidenceRedactedReader {
	if ctx == nil {
		ctx = context.Background()
	}
	return &agentEvidenceRedactedReader{reader: bufio.NewReader(&contextReader{ctx: ctx, reader: source})}
}

func (reader *agentEvidenceRedactedReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if reader.pending == "" && reader.err == nil {
		var line string
		line, reader.err = reader.reader.ReadString('\n')
		reader.pending, _ = redactSessionReviewerDataURIs(line)
	}
	n := copy(buffer, reader.pending)
	reader.pending = reader.pending[n:]
	if reader.pending == "" {
		return n, reader.err
	}
	return n, nil
}

func scanAgentSavedCitationCandidates(ctx context.Context, source io.ReadSeeker, path string, identity runnerCitationIdentityIndex) (sessionRunnerArtifactCandidateReferences, []string, error) {
	candidates := sessionRunnerArtifactCandidateReferences{positive: map[string]struct{}{}, negative: map[string]struct{}{}}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return candidates, nil, err
	}
	// Preserve the historical CSV recognition contract regardless of extension,
	// including quoted multiline fields and all-or-nothing parse recognition.
	reader := csv.NewReader(newAgentEvidenceRedactedReader(ctx, source))
	reader.ReuseRecord = true
	var header []string
	rows := 0
	classified := false
	scientific := map[string]struct{}{}
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var malformed *csv.ParseError
			if !errors.As(err, &malformed) {
				return candidates, nil, err
			}
			rows = 0
			scientific = map[string]struct{}{}
			break
		}
		rows++
		if rows == 1 {
			header = append([]string(nil), record...)
			classified = len(header) >= 2 && containsAny(strings.ToLower(strings.Join(header, " ")), []string{"url", "uri", "link", "doi", "pmid", "nct", "accession", "来源", "链接", "网址", "文献", "专利号", "登记号"})
		} else {
			addSessionRunnerScientificColumns(scientific, [][]string{header, record})
		}
		if classified {
			addSessionRunnerCandidateRecord(candidates.positive, candidates.negative, record)
		}
	}
	classified = classified && rows >= 2
	if !classified {
		candidates.positive, candidates.negative = map[string]struct{}{}, map[string]struct{}{}
	}
	for ref := range scientific {
		candidates.positive[ref] = struct{}{}
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return candidates, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lines := bufio.NewReader(&contextReader{ctx: ctx, reader: source})
	var pipeHeader []string
	var failures []string
	for {
		raw, err := lines.ReadString('\n')
		if len(raw) > 0 {
			if path != "" {
				failures = append(failures, runnerCitationIdentityFailures(path, []byte(raw), identity)...)
			}
			line, _ := redactSessionReviewerDataURIs(strings.TrimSuffix(raw, "\n"))
			if !classified {
				addSessionRunnerCandidateLine(candidates.positive, candidates.negative, line)
			}
			if strings.Contains(line, "|") {
				row := strings.Split(strings.Trim(line, " |"), "|")
				if pipeHeader == nil {
					pipeHeader = row
				} else {
					addSessionRunnerScientificColumns(candidates.positive, [][]string{pipeHeader, row})
				}
			} else {
				pipeHeader = nil
			}
		}
		if errors.Is(err, io.EOF) {
			return candidates, failures, nil
		}
		if err != nil {
			return candidates, nil, err
		}
	}
}
