package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const maxRunnerSourceEvidenceBytes = 8 << 20

// validateSessionRunnerSourceClaimEvidence closes the gap between an existing
// identifier and evidence that actually supports a scientific claim. A source
// ledger may remain a simple bibliography. Once it declares claims_supported,
// each claim must carry a source locator and a non-empty evidence summary,
// while the source record itself must be present in a durable tool receipt from
// the same logical task. A summary may paraphrase the inspected record; only a
// value explicitly labelled as a direct quote must match the receipt verbatim.
func (s *Server) validateSessionRunnerSourceClaimEvidence(
	ctx context.Context,
	projectID string,
	commits []transcriptstore.ArtifactReferenceInput,
	durableEvidence []agentruntime.Message,
) ([]string, error) {
	latest := make(map[string]transcriptstore.ArtifactReferenceInput)
	for _, commit := range commits {
		if commit.Relation != transcriptstore.ArtifactRelationProduced ||
			strings.TrimSpace(commit.ArtifactID) == "" || strings.TrimSpace(commit.VersionID) == "" {
			continue
		}
		latest[commit.ArtifactID] = commit
	}
	if len(latest) == 0 {
		return nil, nil
	}
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(projectID) == "" {
		return nil, errors.New("runner source-claim evidence authority is unavailable")
	}
	corpus := runnerSourceEvidenceCorpus(durableEvidence)
	internalHandles := runnerInternalArtifactHandles(durableEvidence)
	failures := []string{}
	for _, commit := range latest {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}
		artifact, _, reader, found, err := s.workspaceStore.OpenArtifactVersionContent(commit.VersionID)
		if err != nil {
			return nil, err
		}
		if !found {
			if reader != nil {
				_ = reader.Close()
			}
			continue
		}
		if artifact.ProjectID != projectID {
			_ = reader.Close()
			return nil, fmt.Errorf("runner source-evidence version is unavailable: %s", commit.VersionID)
		}
		name := filepath.Base(strings.TrimSpace(artifact.Name))
		ext := strings.ToLower(filepath.Ext(name))
		if !strings.EqualFold(name, "source_evidence.json") && ext != ".csv" && ext != ".tsv" {
			_ = reader.Close()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxRunnerSourceEvidenceBytes+1))
		closeErr := reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read runner source evidence %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close runner source evidence %q: %w", name, closeErr)
		}
		if len(data) > maxRunnerSourceEvidenceBytes {
			failures = append(failures, "source_evidence_too_large:"+name)
			continue
		}
		if !runnerSourceClaimEvidenceArtifact(name, data) {
			continue
		}
		failures = append(failures, runnerInternalArtifactReferenceFailures(name, data, internalHandles)...)
		if strings.EqualFold(filepath.Ext(name), ".json") {
			failures = append(failures, validateRunnerSourceEvidenceDocument(name, data, corpus)...)
		} else {
			failures = append(failures, validateRunnerSourceEvidenceLedger(name, data, corpus)...)
		}
	}
	sort.Strings(failures)
	return failures, nil
}

func runnerSourceClaimEvidenceArtifact(name string, data []byte) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if base == "source_evidence.json" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(base))
	if ext != ".csv" && ext != ".tsv" {
		return false
	}
	_, _, recognized := runnerEvidenceLedgerRecords(runnerCrossArtifactSnapshot{name: name, text: string(data)})
	return recognized
}

// validateRunnerSourceEvidenceLedger verifies the evidence the ledger itself
// claims to have read. A CSV may remain a bibliography and omit excerpts. Once
// it carries an evidence_excerpt column, however, each included row must carry
// a non-empty summary and a locator, while its identifier must occur in a
// successful durable source-tool receipt from this logical task. Direct quotes
// use a separate exact-match rule. This closes the title/snippet-to-conclusion
// gap without rejecting useful paraphrases or imposing a fixed workflow.
func validateRunnerSourceEvidenceLedger(name string, data []byte, corpus string) []string {
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	if strings.EqualFold(filepath.Ext(name), ".tsv") {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		return nil
	}
	headers := make(map[string]int, len(records[0]))
	for index, header := range records[0] {
		headers[normalizeRunnerTableToken(header)] = index
	}
	return validateRunnerSourceEvidenceLedgerRecords(name, records, headers, corpus)
}

func validateRunnerSourceEvidenceLedgerRecords(
	name string,
	records [][]string,
	headers map[string]int,
	corpus string,
) []string {
	excerptIndex, hasExcerpt := firstRunnerEvidenceColumn(
		headers, "evidence_excerpt", "evidenceexcerpt", "supporting_excerpt", "supportingexcerpt",
		"evidence_summary", "evidencesummary", "key_finding", "keyfinding", "key_conclusion", "keyconclusion",
		"证据摘录", "原文摘录", "证据摘要", "关键发现", "核心发现", "关键结论", "核心结论", "主要结论",
	)
	if !hasExcerpt {
		return nil
	}
	locatorIndex, hasLocator := firstRunnerEvidenceColumn(
		headers, "source_locator", "sourcelocator", "source_url", "sourceurl", "url", "identifier", "source_id", "sourceid",
		"来源位置", "来源链接", "来源url", "来源网址", "原始链接", "文献链接", "文献url", "链接", "网址",
		"文献来源/参考", "标识", "标识符", "来源标识",
	)
	statusIndex, hasStatus := firstRunnerEvidenceColumn(
		headers, "disposition", "screening_status", "screeningstatus", "inclusion_status", "inclusionstatus", "status", "纳入状态", "筛选状态",
	)
	identifierIndexes := runnerEvidenceDepthIdentifierIndexes(headers)
	directQuoteIndex, hasDirectQuote := firstRunnerEvidenceColumn(
		headers, "direct_quote", "directquote", "verbatim_quote", "verbatimquote", "原文引文", "直接引文",
	)
	failures := make([]string, 0)
	for rowIndex, row := range records[1:] {
		if hasStatus && statusIndex < len(row) && runnerSourceEvidenceRowExcluded(row[statusIndex]) {
			continue
		}
		excerpt := ""
		if excerptIndex < len(row) {
			excerpt = strings.TrimSpace(row[excerptIndex])
		}
		locator := ""
		if hasLocator && locatorIndex < len(row) {
			locator = strings.TrimSpace(row[locatorIndex])
		}
		identifier := runnerSourceEvidenceRowIdentifier(row, identifierIndexes)
		if identifier == "" {
			identifier = fmt.Sprintf("row-%d", rowIndex+2)
		}
		normalizedExcerpt := normalizeRunnerSourceEvidenceText(excerpt)
		if normalizedExcerpt == "" || locator == "" {
			failures = append(failures, fmt.Sprintf(
				"source_ledger_missing_attested_excerpt:%s row=%d source=%s", name, rowIndex+2, identifier,
			))
			continue
		}
		if corpus == "" || !runnerSourceEvidenceCorpusContainsIdentifier(corpus, identifier, locator) {
			failures = append(failures, fmt.Sprintf(
				"source_ledger_source_not_in_durable_receipts:%s row=%d source=%s", name, rowIndex+2, identifier,
			))
			continue
		}
		if hasDirectQuote && directQuoteIndex < len(row) {
			directQuote := normalizeRunnerSourceEvidenceText(row[directQuoteIndex])
			if directQuote != "" && !strings.Contains(corpus, directQuote) {
				failures = append(failures, fmt.Sprintf(
					"source_ledger_direct_quote_not_in_durable_receipts:%s row=%d source=%s", name, rowIndex+2, identifier,
				))
			}
		}
	}
	return failures
}

func runnerSourceEvidenceRowExcluded(value string) bool {
	normalized := normalizeAgentToolName(value)
	return normalized == "excluded" || normalized == "candidate" || normalized == "rejected" ||
		strings.Contains(value, "排除") || strings.Contains(value, "候选") || strings.Contains(value, "不纳入")
}

func runnerSourceEvidenceRowIdentifier(row []string, indexes []int) string {
	for _, index := range indexes {
		if index >= len(row) {
			continue
		}
		if value := strings.TrimSpace(row[index]); value != "" {
			return truncateServerString(value, 160)
		}
	}
	return ""
}

func validateRunnerSourceEvidenceDocument(name string, data []byte, corpus string) []string {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return []string{"source_evidence_invalid_json:" + name}
	}
	sources, _ := document["sources"].([]any)
	failures := []string{}
	for sourceIndex, rawSource := range sources {
		source, _ := rawSource.(map[string]any)
		claims, declared := source["claims_supported"].([]any)
		if !declared || len(claims) == 0 {
			continue
		}
		sourceID := strings.TrimSpace(stringValue(source["id"]))
		if sourceID == "" {
			sourceID = fmt.Sprintf("index-%d", sourceIndex)
		}
		for claimIndex, rawClaim := range claims {
			claim, ok := rawClaim.(map[string]any)
			if !ok {
				label := strings.TrimSpace(stringValue(rawClaim))
				if label == "" {
					label = fmt.Sprintf("index-%d", claimIndex)
				}
				failures = append(failures, fmt.Sprintf(
					"source_claim_missing_attested_excerpt:%s source=%s claim=%s", name, sourceID, label,
				))
				continue
			}
			claimID := strings.TrimSpace(runnerSourceFirstStringValue(claim, "claim_id", "claimId", "claim"))
			if claimID == "" {
				claimID = fmt.Sprintf("index-%d", claimIndex)
			}
			excerpt := strings.TrimSpace(runnerSourceFirstStringValue(claim, "evidence_excerpt", "evidenceExcerpt", "excerpt"))
			locator := strings.TrimSpace(runnerSourceFirstStringValue(claim, "source_locator", "sourceLocator", "locator"))
			normalizedExcerpt := normalizeRunnerSourceEvidenceText(excerpt)
			if normalizedExcerpt == "" || locator == "" {
				failures = append(failures, fmt.Sprintf(
					"source_claim_missing_attested_excerpt:%s source=%s claim=%s", name, sourceID, claimID,
				))
				continue
			}
			sourceIdentifier := strings.TrimSpace(runnerSourceFirstStringValue(
				source, "identifier", "doi", "pmid", "trial_id", "trialId", "patent_number", "patentNumber", "url",
			))
			if sourceIdentifier == "" {
				sourceIdentifier = sourceID
			}
			if corpus == "" || !runnerSourceEvidenceCorpusContainsIdentifier(corpus, sourceIdentifier, locator) {
				failures = append(failures, fmt.Sprintf(
					"source_claim_source_not_in_durable_receipts:%s source=%s claim=%s", name, sourceID, claimID,
				))
				continue
			}
			directQuote := normalizeRunnerSourceEvidenceText(runnerSourceFirstStringValue(
				claim, "direct_quote", "directQuote", "verbatim_quote", "verbatimQuote",
			))
			if directQuote != "" && !strings.Contains(corpus, directQuote) {
				failures = append(failures, fmt.Sprintf(
					"source_claim_direct_quote_not_in_durable_receipts:%s source=%s claim=%s", name, sourceID, claimID,
				))
			}
		}
	}
	return failures
}

func runnerSourceEvidenceCorpusContainsIdentifier(corpus, identifier, locator string) bool {
	for _, candidate := range []string{identifier, locator} {
		normalized := normalizeRunnerSourceEvidenceText(candidate)
		if len([]rune(normalized)) >= 6 && strings.Contains(corpus, normalized) {
			return true
		}
	}
	return false
}

func runnerSourceEvidenceCorpus(messages []agentruntime.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			if content := normalizeRunnerSourceEvidenceText(message.Content); content != "" {
				parts = append(parts, content)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// Internal artifact and version handles are valid runtime control identities,
// but they are not portable source locators. Derive the exact handles from
// durable tool receipts and reject only those exact values from a user-facing
// source ledger. This avoids filename, header, task, and domain-specific rules.
func runnerInternalArtifactHandles(messages []agentruntime.Message) map[string]struct{} {
	handles := make(map[string]struct{})
	var collect func(any)
	collect = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := normalizeAgentToolName(key)
				if normalized == "artifactid" || normalized == "versionid" {
					if handle := strings.TrimSpace(stringValue(child)); len(handle) >= 16 {
						handles[handle] = struct{}{}
					}
				}
				collect(child)
			}
		case []any:
			for _, child := range typed {
				collect(child)
			}
		}
	}
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var value any
		if json.Unmarshal([]byte(message.Content), &value) == nil {
			collect(value)
		}
	}
	return handles
}

func runnerInternalArtifactReferenceFailures(name string, data []byte, handles map[string]struct{}) []string {
	if len(handles) == 0 || len(data) == 0 {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if ext == ".csv" || ext == ".tsv" {
		reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
		if ext == ".tsv" {
			reader.Comma = '\t'
		}
		reader.FieldsPerRecord = -1
		records, err := reader.ReadAll()
		if err != nil {
			return nil
		}
		failures := make([]string, 0)
		for rowIndex, row := range records[1:] {
			for _, cell := range row {
				if _, exposed := handles[strings.TrimSpace(cell)]; exposed {
					failures = append(failures, fmt.Sprintf(
						"machine_validation_internal_runtime_reference:%s row=%d", name, rowIndex+2,
					))
					break
				}
			}
		}
		return failures
	}
	var value any
	if !strings.EqualFold(ext, ".json") || json.Unmarshal(data, &value) != nil {
		return nil
	}
	exposed := false
	var inspect func(any)
	inspect = func(current any) {
		if exposed {
			return
		}
		switch typed := current.(type) {
		case string:
			_, exposed = handles[strings.TrimSpace(typed)]
		case map[string]any:
			for _, child := range typed {
				inspect(child)
			}
		case []any:
			for _, child := range typed {
				inspect(child)
			}
		}
	}
	inspect(value)
	if exposed {
		return []string{"machine_validation_internal_runtime_reference:" + name}
	}
	return nil
}

func normalizeRunnerSourceEvidenceText(value string) string {
	return strings.ToLower(strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " "))
}

func runnerSourceFirstStringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringValue(values[key])); value != "" {
			return value
		}
	}
	return ""
}
