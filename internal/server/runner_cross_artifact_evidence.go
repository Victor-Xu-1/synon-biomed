package server

import (
	"encoding/csv"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var runnerEvidenceStableLocatorPattern = regexp.MustCompile(`(?i)(?:https?://\S+|\b10\.\d{4,9}/\S+|\bPMID\s*:?\s*\d+\b|\bNCT\d{8}\b|\b(?:PDB|UniProt|ChEMBL|PubChem\s+CID|NDA|BLA|ANDA)\s*:?\s*[A-Z0-9][A-Z0-9._/-]*\b)`)

// runnerEvidenceProvenanceFailures requires authoritative evidence rows to
// carry a stable, independently resolvable locator. A source label such as
// "FDA label" or "literature" is a category, not provenance. This gate is
// deliberately domain-neutral: URLs, DOIs, registry IDs, accessions, and
// regulatory application IDs are accepted without interpreting the claim.
func runnerEvidenceProvenanceFailures(snapshot runnerCrossArtifactSnapshot) []string {
	records, headers, ok := runnerEvidenceLedgerRecords(snapshot)
	if !ok {
		return nil
	}
	return runnerEvidenceProvenanceTableFailures(snapshot.name, records, headers)
}

func runnerEvidenceProvenanceTableFailures(
	name string,
	records [][]string,
	headers map[string]int,
) []string {
	sourceTypeIndex, found := firstRunnerEvidenceColumn(headers, "source_type", "sourcetype", "来源类型", "证据类型")
	locatorIndexes := runnerEvidenceLocatorIndexes(headers)
	statusIndex, hasStatus := firstRunnerEvidenceColumn(
		headers, "disposition", "screening_status", "screeningstatus", "inclusion_status", "inclusionstatus", "status", "纳入状态", "筛选状态",
	)
	failures := make([]string, 0)
	for rowIndex, row := range records[1:] {
		if hasStatus && statusIndex < len(row) && runnerSourceEvidenceRowExcluded(row[statusIndex]) {
			continue
		}
		sourceType := "evidence"
		if found {
			if sourceTypeIndex >= len(row) {
				continue
			}
			sourceType = strings.TrimSpace(row[sourceTypeIndex])
			if sourceType == "" || runnerEvidenceNonAuthoritativeType(strings.ToLower(sourceType)) {
				continue
			}
		}
		if !runnerEvidenceRowHasContent(row) {
			continue
		}
		located := false
		for _, index := range locatorIndexes {
			if index < len(row) && runnerEvidenceStableLocator(strings.TrimSpace(row[index])) {
				located = true
				break
			}
		}
		if !located {
			failures = append(failures, fmt.Sprintf(
				"evidence_source_locator_missing:%s row=%d source_type=%s",
				name, rowIndex+2, sourceType,
			))
		}
	}
	return failures
}

func runnerEvidenceLedgerRecords(
	snapshot runnerCrossArtifactSnapshot,
) ([][]string, map[string]int, bool) {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(snapshot.name)))
	ext := strings.ToLower(filepath.Ext(base))
	if ext != ".csv" && ext != ".tsv" {
		return nil, nil, false
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(snapshot.text, "\ufeff")))
	if ext == ".tsv" {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		return nil, nil, false
	}
	headers := make(map[string]int, len(records[0]))
	for index, header := range records[0] {
		headers[normalizeRunnerTableToken(header)] = index
	}
	if !runnerEvidenceLedgerHeaderShape(headers) {
		return nil, nil, false
	}
	return records, headers, true
}

func runnerEvidenceLedgerHeaderShape(headers map[string]int) bool {
	_, hasSource := firstRunnerEvidenceColumn(
		headers, "source", "source_name", "sourcename", "reference", "citation", "来源", "证据来源", "参考来源", "文献来源/参考",
	)
	_, hasSummary := firstRunnerEvidenceColumn(
		headers, "claim", "claims", "finding", "findings", "conclusion", "key_conclusion", "keyconclusion",
		"evidence_summary", "evidencesummary", "evidence_excerpt", "evidenceexcerpt", "摘要", "证据摘要", "关键发现", "核心发现", "关键结论", "核心结论", "主要结论",
	)
	_, hasSourceType := firstRunnerEvidenceColumn(headers, "source_type", "sourcetype", "evidence_type", "evidencetype", "来源类型", "证据类型")
	hasLocator := len(runnerEvidenceLocatorIndexes(headers)) > 0
	hasIdentifier := len(runnerEvidenceDepthIdentifierIndexes(headers)) > 0
	return hasLocator && (hasSource || hasSummary || hasSourceType || hasIdentifier)
}

func runnerEmbeddedMarkdownEvidenceTables(snapshot runnerCrossArtifactSnapshot) []runnerCrossArtifactTable {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(snapshot.name)))
	if ext != ".md" && ext != ".markdown" {
		return nil
	}
	result := make([]runnerCrossArtifactTable, 0)
	for _, table := range runnerMarkdownArtifactTables(snapshot) {
		headers := make(map[string]int, len(table.headers))
		for index, header := range table.headers {
			headers[normalizeRunnerTableToken(header)] = index
		}
		if runnerEvidenceLedgerHeaderShape(headers) {
			result = append(result, table)
		}
	}
	return result
}

func runnerEvidenceRowHasContent(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func runnerEvidenceStableLocator(value string) bool {
	return runnerEvidenceStableLocatorPattern.MatchString(value) || runnerEvidenceCanonicalPatent(value) != ""
}

func runnerEvidenceLocatorIndexes(headers map[string]int) []int {
	keys := []string{
		"source", "source_id", "sourceid", "source_url", "sourceurl", "source_locator", "sourcelocator", "url", "doi", "pmid", "nct_id", "nctid",
		"accession", "source_identifier", "sourceidentifier", "identifier", "reference_id", "referenceid",
		"publication_id", "publicationid", "regulatory_id", "regulatoryid", "references", "bibliography",
		"来源", "来源url", "来源网址", "来源链接", "原始链接", "文献url", "文献链接",
		"参考文献", "参考文献url", "参考文献链接", "链接", "网址", "来源id", "来源标识", "文献标识", "文献来源/参考", "标识", "标识符", "标识符/编号", "注册号", "申请号", "专利号",
	}
	return runnerEvidenceColumnIndexes(headers, keys...)
}

func firstRunnerEvidenceColumn(headers map[string]int, keys ...string) (int, bool) {
	for _, key := range keys {
		if index, found := headers[normalizeRunnerTableToken(key)]; found {
			return index, true
		}
	}
	for _, key := range keys {
		best := -1
		for header, index := range headers {
			if runnerEvidenceHeaderMatchesAny(header, key) && (best < 0 || index < best) {
				best = index
			}
		}
		if best >= 0 {
			return best, true
		}
	}
	return 0, false
}

// runnerEvidenceColumnIndexes maps table headers by semantic components. A
// producer may combine compatible locator labels such as DOI/URL in one column;
// punctuation is presentation, not a new schema identity. Exact aliases remain
// authoritative, and unknown labels are left unmapped rather than declared
// missing evidence.
func runnerEvidenceColumnIndexes(headers map[string]int, aliases ...string) []int {
	result := make([]int, 0, len(headers))
	seen := make(map[int]struct{}, len(headers))
	for header, index := range headers {
		if !runnerEvidenceHeaderMatchesAny(header, aliases...) {
			continue
		}
		if _, duplicate := seen[index]; duplicate {
			continue
		}
		seen[index] = struct{}{}
		result = append(result, index)
	}
	sort.Ints(result)
	return result
}

func runnerEvidenceHeaderMatchesAny(header string, aliases ...string) bool {
	wanted := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		if normalized := normalizeRunnerTableToken(alias); normalized != "" {
			wanted[normalized] = struct{}{}
		}
	}
	for _, component := range runnerEvidenceHeaderComponents(header) {
		if _, found := wanted[component]; found {
			return true
		}
	}
	return false
}

func runnerEvidenceHeaderComponents(header string) []string {
	normalized := normalizeRunnerTableToken(header)
	if normalized == "" {
		return nil
	}
	result := []string{normalized}
	for _, component := range strings.FieldsFunc(normalized, func(char rune) bool {
		switch char {
		case '/', '／', '|', '\\', '&', '+', '、', ',', '，', ';', '；', '(', ')', '（', '）':
			return true
		default:
			return false
		}
	}) {
		if component = normalizeRunnerTableToken(component); component != "" {
			result = append(result, component)
		}
	}
	return uniqueStrings(result)
}

func runnerEvidenceNonAuthoritativeType(value string) bool {
	value = normalizeRunnerTableToken(value)
	for _, token := range []string{
		"unknown", "assumption", "hypothesis", "model", "model_derived", "calculated", "derived",
		"user_provided", "internal", "scenario", "未知", "假设", "模型", "计算", "推导", "用户提供", "内部", "情景",
		"inference", "information_gap", "推断", "信息缺口", "信息空白",
	} {
		if value == normalizeRunnerTableToken(token) {
			return true
		}
	}
	return false
}
