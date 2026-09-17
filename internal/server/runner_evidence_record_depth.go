package server

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const maxRunnerEvidenceDepthScanBytes = 16 << 20

var runnerEvidenceDepthPatentIdentifierPattern = regexp.MustCompile(`(?i)\b[A-Z]{2}[A-Z0-9-]{4,38}\b`)
var runnerEvidenceDepthPubMedURLPattern = regexp.MustCompile(`(?i)pubmed\.ncbi\.nlm\.nih\.gov/([1-9][0-9]{0,8})(?:[/?#]|$)`)
var runnerEvidenceDepthPMCIDPattern = regexp.MustCompile(`(?i)\bPMC[0-9]+\b`)
var runnerEvidenceDepthPMIDTokenPattern = regexp.MustCompile(`\b[1-9][0-9]{0,8}\b`)

var runnerEvidenceDepthREPLNCTLiteralPattern = regexp.MustCompile(
	`(?i)["']nct_id["']\s*:\s*["'](NCT[0-9]{8})["']|\bnct_id\s*=\s*["'](NCT[0-9]{8})["']`,
)

type runnerEvidenceRecordDepthIndex struct {
	patents           map[string]struct{}
	discoveredPatents map[string]struct{}
	trials            map[string]struct{}
	publications      map[string]struct{}
	web               map[string]struct{}
}

type runnerEvidenceRecordRequirement struct {
	class      string
	identifier string
}

func newRunnerEvidenceRecordDepthIndex() runnerEvidenceRecordDepthIndex {
	return runnerEvidenceRecordDepthIndex{
		patents: make(map[string]struct{}), discoveredPatents: make(map[string]struct{}),
		trials:       make(map[string]struct{}),
		publications: make(map[string]struct{}), web: make(map[string]struct{}),
	}
}

// validateSessionRunnerEvidenceRecordDepth prevents a discovery identifier
// from silently becoming a screened evidence row. It does not require one
// fixed retrieval sequence: any governed source interface may satisfy the
// contract when its durable result proves that the exact record was read.
func (s *Server) validateSessionRunnerEvidenceRecordDepth(
	ctx context.Context,
	projectID string,
	commits []transcriptstore.ArtifactReferenceInput,
	durableEvidence []agentruntime.Message,
	trustedSignals []string,
	_ ...string,
) ([]string, error) {
	latest := make(map[string]transcriptstore.ArtifactReferenceInput)
	for _, commit := range commits {
		if commit.Relation == transcriptstore.ArtifactRelationProduced &&
			strings.TrimSpace(commit.ArtifactID) != "" && strings.TrimSpace(commit.VersionID) != "" {
			latest[commit.ArtifactID] = commit
		}
	}
	if len(latest) == 0 {
		return nil, nil
	}
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("runner evidence-depth authority is unavailable")
	}
	depth := runnerEvidenceRecordDepthIndexFromMessages(durableEvidence)
	runnerEvidenceRecordDepthMergeTrustedSignals(&depth, trustedSignals)
	failures := make([]string, 0)
	classesPresent := make(map[string]bool)
	classesVerified := make(map[string]bool)
	classIdentifiers := make(map[string][]string)
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
			return nil, fmt.Errorf("runner evidence-depth version is unavailable: %s", commit.VersionID)
		}
		name := filepath.Base(strings.TrimSpace(artifact.Name))
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".csv" && ext != ".tsv" {
			_ = reader.Close()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxRunnerEvidenceDepthScanBytes+1))
		closeErr := reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read runner evidence-depth ledger %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close runner evidence-depth ledger %q: %w", name, closeErr)
		}
		if len(data) > maxRunnerEvidenceDepthScanBytes {
			continue
		}
		snapshot := runnerCrossArtifactSnapshot{name: name, text: string(data)}
		if _, _, recognized := runnerEvidenceLedgerRecords(snapshot); !recognized {
			continue
		}
		failures = append(failures, runnerEvidenceRecordDepthFailures(snapshot, depth)...)
		for class := range runnerEvidenceRecordDepthClasses(snapshot, depth) {
			classesPresent[class] = true
		}
		for class := range runnerEvidenceRecordDepthVerifiedClasses(snapshot, depth) {
			classesVerified[class] = true
		}
		for class, identifiers := range runnerEvidenceRecordDepthClassIdentifiers(snapshot) {
			classIdentifiers[class] = append(classIdentifiers[class], identifiers...)
		}
	}
	// A focused review is allowed to leave lower-priority rows at discovery
	// depth, but every source class that appears in a ledger still needs one
	// substantive record read. This preserves a meaningful quality floor while
	// avoiding a correction loop that demands opening every row. When the
	// durable route ledger proves that a class has no remaining advertised
	// source route, retain the limitation as an advisory instead of reopening
	// an unbounded correction loop.
	for _, class := range []string{"patent", "trial", "publication", "web"} {
		if classesPresent[class] && !classesVerified[class] {
			identifiers := uniqueSortedFolded(classIdentifiers[class])
			identifierDetail := ""
			if class != "web" && len(identifiers) > 0 {
				identifierDetail = " identifier=" + identifiers[0]
			}
			if runnerEvidenceRouteExhaustedForClass(class, trustedSignals) {
				failures = append(failures, fmt.Sprintf(
					"evidence_record_depth_unavailable:%s source_type=%s%s reason=all_advertised_source_routes_exhausted",
					class, class, identifierDetail,
				))
			} else {
				failures = append(failures, fmt.Sprintf(
					"evidence_record_depth_required:%s source_type=%s%s reason=at_least_one_substantive_record_read",
					class, class, identifierDetail,
				))
			}
		}
	}
	return uniqueSortedFolded(failures), nil
}

// runnerEvidenceRouteExhaustedForClass reports a durable, server-derived
// exhaustion signal for a source family. It is scoped to the class rather
// than a concrete URL or task so the same convergence rule works for every
// research domain.
func runnerEvidenceRouteExhaustedForClass(class string, signals []string) bool {
	prefix := strings.ToLower(trustedScientificEvidenceRouteExhaustedSignalPrefix + strings.TrimSpace(class) + ":")
	for _, signal := range signals {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(signal)), prefix) {
			return true
		}
	}
	return false
}

func runnerEvidenceClassesFromCorrection(detail, marker string) []string {
	lower := strings.ToLower(strings.TrimSpace(detail))
	marker = strings.ToLower(strings.TrimSpace(marker))
	classes := make([]string, 0, 3)
	for searchFrom := 0; searchFrom < len(lower); {
		index := strings.Index(lower[searchFrom:], marker)
		if index < 0 {
			break
		}
		index += searchFrom
		tail := detail[index+len(marker):]
		typeIndex := strings.Index(strings.ToLower(tail), "source_type=")
		if typeIndex < 0 {
			searchFrom = index + len(marker)
			continue
		}
		value := strings.TrimSpace(tail[typeIndex+len("source_type="):])
		if boundary := strings.Index(strings.ToLower(value), " identifier="); boundary >= 0 {
			value = value[:boundary]
		}
		if boundary := strings.IndexAny(value, ",;\n\r"); boundary >= 0 {
			value = value[:boundary]
		}
		if class := runnerEvidenceDepthClass(value); class != "" {
			classes = append(classes, class)
		}
		searchFrom = index + len(marker)
	}
	return uniqueSortedFolded(classes)
}

func runnerEvidenceRecordRequirementsFromCorrection(detail string) []runnerEvidenceRecordRequirement {
	requirements := make([]runnerEvidenceRecordRequirement, 0, 6)
	for _, class := range runnerEvidenceClassesFromCorrection(detail, "evidence_record_class_missing:") {
		requirements = append(requirements, runnerEvidenceRecordRequirement{class: class})
	}
	requirements = append(requirements, runnerEvidenceRecordRequirementsForMarker(
		detail, "evidence_record_depth_required:",
	)...)
	requirements = append(requirements, runnerEvidenceRecordRequirementsForMarker(
		detail, "evidence_record_depth_missing:",
	)...)
	return requirements
}

func runnerEvidenceRecordRequirementsForMarker(
	detail string,
	marker string,
) []runnerEvidenceRecordRequirement {
	requirements := make([]runnerEvidenceRecordRequirement, 0, 4)
	lower := strings.ToLower(detail)
	marker = strings.ToLower(strings.TrimSpace(marker))
	for searchFrom := 0; searchFrom < len(lower); {
		index := strings.Index(lower[searchFrom:], marker)
		if index < 0 {
			break
		}
		index += searchFrom
		tail := detail[index+len(marker):]
		next := len(tail)
		if boundary := strings.Index(strings.ToLower(tail), marker); boundary >= 0 {
			next = boundary
		}
		segment := tail[:next]
		classes := runnerEvidenceClassesFromCorrection(marker+segment, marker)
		if len(classes) > 0 {
			identifier := ""
			if identifierAt := strings.Index(strings.ToLower(segment), " identifier="); identifierAt >= 0 {
				raw := strings.TrimSpace(segment[identifierAt+len(" identifier="):])
				if boundary := strings.IndexAny(raw, ",;\n\r"); boundary >= 0 {
					raw = raw[:boundary]
				}
				switch classes[0] {
				case "patent":
					identifier = runnerEvidenceCanonicalPatent(raw)
				case "trial":
					identifier = strings.ToUpper(sessionRunnerNCTPattern.FindString(raw))
				case "publication":
					identifier = runnerEvidenceCanonicalPublication(raw)
				}
			}
			requirements = append(requirements, runnerEvidenceRecordRequirement{class: classes[0], identifier: identifier})
		}
		searchFrom = index + len(marker)
	}
	return requirements
}

func runnerEvidenceRecordDepthFailures(
	snapshot runnerCrossArtifactSnapshot,
	depth runnerEvidenceRecordDepthIndex,
) []string {
	ext := strings.ToLower(filepath.Ext(snapshot.name))
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(snapshot.text, "\ufeff")))
	if ext == ".tsv" {
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
	return runnerEvidenceRecordDepthTableFailures(snapshot.name, records, headers, depth)
}

func runnerEvidenceRecordDepthTableFailures(
	name string,
	records [][]string,
	headers map[string]int,
	depth runnerEvidenceRecordDepthIndex,
) []string {
	typeIndex, found := firstRunnerEvidenceColumn(
		headers, "source_type", "sourcetype", "evidence_type", "evidencetype", "来源类型", "证据类型", "标识符类型",
	)
	if !found {
		if len(runnerEvidenceWideIdentifierColumns(headers)) > 0 {
			return runnerEvidenceWideRecordDepthFailures(name, records[1:], headers, depth)
		}
		return runnerEvidenceGenericRecordDepthFailures(
			name, records[1:], runnerEvidenceDepthIdentifierIndexes(headers), depth,
		)
	}
	identifierIndexes := runnerEvidenceDepthIdentifierIndexes(headers)
	failures := make([]string, 0)
	for rowIndex, row := range records[1:] {
		if typeIndex >= len(row) {
			continue
		}
		declaredType := strings.TrimSpace(row[typeIndex])
		class := runnerEvidenceDepthClass(declaredType)
		requiredClass, identifier, satisfied := runnerEvidenceDepthRowRequirement(row, identifierIndexes, class, depth)
		if identifier == "" || satisfied {
			continue
		}
		failures = append(failures, fmt.Sprintf(
			"evidence_record_depth_missing:%s row=%d source_type=%s declared_source_type=%s identifier=%s",
			name, rowIndex+2, requiredClass, declaredType, identifier,
		))
	}
	return failures
}

// runnerEvidenceRecordDepthClasses returns the source classes represented by
// identifier-bearing rows. It intentionally ignores empty or unclassified
// rows, which are not evidence claims and should not create a repair loop.
func runnerEvidenceRecordDepthClasses(
	snapshot runnerCrossArtifactSnapshot,
	depth runnerEvidenceRecordDepthIndex,
) map[string]bool {
	classes := make(map[string]bool)
	ext := strings.ToLower(filepath.Ext(snapshot.name))
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(snapshot.text, "\ufeff")))
	if ext == ".tsv" {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		return classes
	}
	headers := make(map[string]int, len(records[0]))
	for index, header := range records[0] {
		headers[normalizeRunnerTableToken(header)] = index
	}
	typeIndex, found := firstRunnerEvidenceColumn(
		headers, "source_type", "sourcetype", "evidence_type", "evidencetype", "来源类型", "证据类型", "标识符类型",
	)
	if !found {
		if len(runnerEvidenceWideIdentifierColumns(headers)) > 0 {
			return runnerEvidenceWideRecordDepthClasses(records[1:], headers, depth, false)
		}
		return runnerEvidenceGenericRecordDepthClasses(
			records[1:], runnerEvidenceDepthIdentifierIndexes(headers), depth, false,
		)
	}
	identifierIndexes := runnerEvidenceDepthIdentifierIndexes(headers)
	for _, row := range records[1:] {
		if typeIndex >= len(row) {
			continue
		}
		class := runnerEvidenceDepthClass(row[typeIndex])
		requiredClass, identifier, _ := runnerEvidenceDepthRowRequirement(row, identifierIndexes, class, depth)
		if identifier != "" && requiredClass != "" {
			classes[requiredClass] = true
		}
	}
	return classes
}

func runnerEvidenceRecordDepthVerifiedClasses(
	snapshot runnerCrossArtifactSnapshot,
	depth runnerEvidenceRecordDepthIndex,
) map[string]bool {
	verified := make(map[string]bool)
	ext := strings.ToLower(filepath.Ext(snapshot.name))
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(snapshot.text, "\ufeff")))
	if ext == ".tsv" {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		return verified
	}
	headers := make(map[string]int, len(records[0]))
	for index, header := range records[0] {
		headers[normalizeRunnerTableToken(header)] = index
	}
	typeIndex, found := firstRunnerEvidenceColumn(
		headers, "source_type", "sourcetype", "evidence_type", "evidencetype", "来源类型", "证据类型", "标识符类型",
	)
	if !found {
		if len(runnerEvidenceWideIdentifierColumns(headers)) > 0 {
			return runnerEvidenceWideRecordDepthClasses(records[1:], headers, depth, true)
		}
		return runnerEvidenceGenericRecordDepthClasses(
			records[1:], runnerEvidenceDepthIdentifierIndexes(headers), depth, true,
		)
	}
	identifierIndexes := runnerEvidenceDepthIdentifierIndexes(headers)
	for _, row := range records[1:] {
		if typeIndex >= len(row) {
			continue
		}
		class := runnerEvidenceDepthClass(row[typeIndex])
		_, identifier, satisfied := runnerEvidenceDepthRowRequirement(row, identifierIndexes, class, depth)
		if identifier != "" && satisfied {
			verified[class] = true
		}
	}
	return verified
}

// runnerEvidenceRecordDepthClassIdentifiers returns the stable identifiers
// actually declared by either supported evidence-table shape. Aggregate depth
// failures carry one deterministic representative identifier so recovery must
// read a record from the affected table rather than accepting an unrelated
// historical receipt from the same broad source class.
func runnerEvidenceRecordDepthClassIdentifiers(
	snapshot runnerCrossArtifactSnapshot,
) map[string][]string {
	result := make(map[string][]string)
	ext := strings.ToLower(filepath.Ext(snapshot.name))
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(snapshot.text, "\ufeff")))
	if ext == ".tsv" {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		return result
	}
	headers := make(map[string]int, len(records[0]))
	for index, header := range records[0] {
		headers[normalizeRunnerTableToken(header)] = index
	}
	typeIndex, typed := firstRunnerEvidenceColumn(
		headers, "source_type", "sourcetype", "evidence_type", "evidencetype", "来源类型", "证据类型", "标识符类型",
	)
	if typed {
		identifierIndexes := runnerEvidenceDepthIdentifierIndexes(headers)
		emptyDepth := newRunnerEvidenceRecordDepthIndex()
		for _, row := range records[1:] {
			if typeIndex >= len(row) {
				continue
			}
			class := runnerEvidenceDepthClass(row[typeIndex])
			requiredClass, identifier, _ := runnerEvidenceDepthRowRequirement(
				row, identifierIndexes, class, emptyDepth,
			)
			if requiredClass != "" && identifier != "" {
				result[requiredClass] = append(result[requiredClass], identifier)
			}
		}
	} else {
		if columns := runnerEvidenceWideIdentifierColumns(headers); len(columns) > 0 {
			for _, row := range records[1:] {
				for _, column := range columns {
					if column.index < len(row) {
						result[column.class] = append(
							result[column.class], runnerEvidenceWideCellIdentifiers(row[column.index], column)...,
						)
					}
				}
			}
		} else {
			for _, row := range records[1:] {
				for _, requirement := range runnerEvidenceGenericRowRequirements(
					row, runnerEvidenceDepthIdentifierIndexes(headers),
				) {
					result[requirement.class] = append(result[requirement.class], requirement.identifier)
				}
			}
		}
	}
	for class, identifiers := range result {
		result[class] = uniqueSortedFolded(identifiers)
	}
	return result
}

type runnerEvidenceWideIdentifierColumn struct {
	index int
	class string
	kind  string
}

func runnerEvidenceGenericRecordDepthFailures(
	name string,
	records [][]string,
	identifierIndexes []int,
	depth runnerEvidenceRecordDepthIndex,
) []string {
	failures := make([]string, 0)
	for rowIndex, row := range records {
		for _, requirement := range runnerEvidenceGenericRowRequirements(row, identifierIndexes) {
			if depth.contains(requirement.class, requirement.identifier) {
				continue
			}
			failures = append(failures, fmt.Sprintf(
				"evidence_record_depth_missing:%s row=%d source_type=%s declared_source_type=identifier identifier=%s",
				name, rowIndex+2, requirement.class, requirement.identifier,
			))
		}
	}
	return uniqueSortedFolded(failures)
}

func runnerEvidenceGenericRecordDepthClasses(
	records [][]string,
	identifierIndexes []int,
	depth runnerEvidenceRecordDepthIndex,
	verifiedOnly bool,
) map[string]bool {
	classes := make(map[string]bool)
	for _, row := range records {
		for _, requirement := range runnerEvidenceGenericRowRequirements(row, identifierIndexes) {
			if !verifiedOnly || depth.contains(requirement.class, requirement.identifier) {
				classes[requirement.class] = true
			}
		}
	}
	return classes
}

func runnerEvidenceGenericRowRequirements(
	row []string,
	identifierIndexes []int,
) []runnerEvidenceRecordRequirement {
	requirements := make([]runnerEvidenceRecordRequirement, 0, len(identifierIndexes))
	seen := make(map[string]struct{})
	appendRequirement := func(class, identifier string) {
		if class == "" || identifier == "" {
			return
		}
		key := class + "\x00" + identifier
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		requirements = append(requirements, runnerEvidenceRecordRequirement{class: class, identifier: identifier})
	}
	for _, index := range identifierIndexes {
		if index >= len(row) {
			continue
		}
		value := strings.TrimSpace(row[index])
		if value == "" {
			continue
		}
		if identifier := strings.ToUpper(sessionRunnerNCTPattern.FindString(value)); identifier != "" {
			appendRequirement("trial", identifier)
			continue
		}
		if identifier := runnerEvidenceCanonicalPublication(value); identifier != "" {
			appendRequirement("publication", identifier)
			continue
		}
		if identifier := runnerEvidenceCanonicalPatent(value); identifier != "" {
			appendRequirement("patent", identifier)
			continue
		}
		if identifier := runnerEvidenceCanonicalWeb(value); identifier != "" {
			appendRequirement("web", identifier)
		}
	}
	return requirements
}

// Wide comparison tables keep one entity per row and place stable source
// identifiers in typed columns. They are as auditable as a one-source-per-row
// ledger when the column semantics are explicit, so the validator recognizes
// both shapes without forcing a scientific comparison matrix into a private
// internal schema.
func runnerEvidenceWideIdentifierColumns(headers map[string]int) []runnerEvidenceWideIdentifierColumn {
	columns := make([]runnerEvidenceWideIdentifierColumn, 0, 6)
	for header, index := range headers {
		normalized := normalizeRunnerTableToken(header)
		compact := normalizeAgentToolName(normalized)
		add := func(class, kind string) {
			columns = append(columns, runnerEvidenceWideIdentifierColumn{index: index, class: class, kind: kind})
		}
		if (strings.Contains(compact, "patent") && containsAny(compact, []string{"number", "identifier", "publication", "application", "id"})) ||
			containsAny(normalized, []string{"专利号", "专利编号", "专利申请号", "专利公开号"}) {
			add("patent", "patent")
		}
		if strings.Contains(compact, "nct") ||
			(strings.Contains(compact, "clinicaltrial") && containsAny(compact, []string{"number", "identifier", "registration", "registry", "id"})) ||
			containsAny(normalized, []string{"临床试验编号", "临床试验注册号", "试验注册号"}) {
			add("trial", "trial")
		}
		if strings.Contains(compact, "doi") {
			add("publication", "doi")
		}
		if strings.Contains(compact, "pmcid") {
			add("publication", "pmcid")
		} else if strings.Contains(compact, "pmid") {
			add("publication", "pmid")
		}
		if (strings.Contains(normalized, "文献") || strings.Contains(compact, "publication")) &&
			containsAny(compact, []string{"number", "identifier", "accession", "id"}) &&
			!strings.Contains(compact, "doi") && !strings.Contains(compact, "pmid") && !strings.Contains(compact, "pmcid") {
			add("publication", "publication")
		}
	}
	sort.Slice(columns, func(left, right int) bool {
		if columns[left].index != columns[right].index {
			return columns[left].index < columns[right].index
		}
		if columns[left].class != columns[right].class {
			return columns[left].class < columns[right].class
		}
		return columns[left].kind < columns[right].kind
	})
	return columns
}

func runnerEvidenceWideRecordDepthFailures(
	name string,
	records [][]string,
	headers map[string]int,
	depth runnerEvidenceRecordDepthIndex,
) []string {
	columns := runnerEvidenceWideIdentifierColumns(headers)
	failures := make([]string, 0)
	for rowIndex, row := range records {
		for _, column := range columns {
			if column.index >= len(row) {
				continue
			}
			for _, identifier := range runnerEvidenceWideCellIdentifiers(row[column.index], column) {
				if depth.contains(column.class, identifier) {
					continue
				}
				failures = append(failures, fmt.Sprintf(
					"evidence_record_depth_missing:%s row=%d source_type=%s declared_source_type=wide_column identifier=%s",
					name, rowIndex+2, column.class, identifier,
				))
			}
		}
	}
	return uniqueSortedFolded(failures)
}

func runnerEvidenceWideRecordDepthClasses(
	records [][]string,
	headers map[string]int,
	depth runnerEvidenceRecordDepthIndex,
	verifiedOnly bool,
) map[string]bool {
	classes := make(map[string]bool)
	columns := runnerEvidenceWideIdentifierColumns(headers)
	for _, row := range records {
		for _, column := range columns {
			if column.index >= len(row) {
				continue
			}
			identifiers := runnerEvidenceWideCellIdentifiers(row[column.index], column)
			if len(identifiers) == 0 {
				continue
			}
			if !verifiedOnly {
				classes[column.class] = true
				continue
			}
			for _, identifier := range identifiers {
				if depth.contains(column.class, identifier) {
					classes[column.class] = true
					break
				}
			}
		}
	}
	return classes
}

func runnerEvidenceWideCellIdentifiers(
	value string,
	column runnerEvidenceWideIdentifierColumn,
) []string {
	parts := strings.FieldsFunc(value, func(character rune) bool {
		return character == ';' || character == '|' || character == '\n' || character == '\r'
	})
	identifiers := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch column.kind {
		case "patent":
			for _, match := range runnerEvidenceDepthPatentIdentifierPattern.FindAllString(part, -1) {
				if identifier := runnerEvidenceCanonicalPatent(match); identifier != "" {
					identifiers = append(identifiers, identifier)
				}
			}
		case "trial":
			for _, match := range sessionRunnerNCTPattern.FindAllString(part, -1) {
				identifiers = append(identifiers, strings.ToUpper(match))
			}
		case "doi":
			for _, match := range sessionRunnerDOIPattern.FindAllString(part, -1) {
				if identifier := runnerEvidenceCanonicalPublication(match); identifier != "" {
					identifiers = append(identifiers, identifier)
				}
			}
		case "pmcid":
			for _, match := range runnerEvidenceDepthPMCIDPattern.FindAllString(part, -1) {
				identifiers = append(identifiers, "pmcid:"+strings.ToUpper(match))
			}
		case "pmid":
			for _, match := range runnerEvidenceDepthPMIDTokenPattern.FindAllString(part, -1) {
				identifiers = append(identifiers, "pmid:"+match)
			}
		case "publication":
			if identifier := runnerEvidenceCanonicalPublication(part); identifier != "" {
				identifiers = append(identifiers, identifier)
			}
		}
	}
	return uniqueSortedFolded(identifiers)
}

func runnerEvidenceDepthIdentifierIndexes(headers map[string]int) []int {
	keys := []string{
		"identifier", "source_id", "sourceid", "source_identifier", "sourceidentifier", "publication_number", "publicationnumber",
		"patent_number", "patentnumber", "nct_id", "nctid", "doi", "pmid", "pmcid",
		"source_url", "sourceurl", "url", "source_locator", "sourcelocator",
		"标识", "标识符", "标识符/编号", "来源id", "专利号", "注册号", "来源链接", "来源url", "来源网址",
		"原始链接", "文献链接", "文献url", "链接", "网址", "文献来源/参考",
	}
	return runnerEvidenceColumnIndexes(headers, keys...)
}

func runnerEvidenceDepthClass(value string) string {
	if runnerEvidenceNonAuthoritativeType(value) {
		return ""
	}
	normalized := normalizeRunnerTableToken(value)
	compact := normalizeAgentToolName(normalized)
	switch {
	case strings.Contains(normalized, "专利") || strings.Contains(compact, "patent"):
		return "patent"
	case compact == "nct" || strings.Contains(compact, "clinicaltrial") || compact == "trial":
		return "trial"
	case strings.Contains(normalized, "临床试验"):
		return "trial"
	case strings.Contains(normalized, "文献") || strings.Contains(normalized, "论文") || strings.Contains(normalized, "综述") ||
		strings.Contains(compact, "literature") || strings.Contains(compact, "publication") ||
		strings.Contains(compact, "article") || strings.Contains(compact, "review") || compact == "preprint" ||
		compact == "paper" || compact == "doi" ||
		compact == "pmid" || compact == "pmcid":
		return "publication"
	default:
		if normalized != "" {
			return "web"
		}
		return ""
	}
}

func runnerEvidenceDepthRowRequirement(
	row []string,
	indexes []int,
	class string,
	depth runnerEvidenceRecordDepthIndex,
) (requiredClass, identifier string, satisfied bool) {
	if class != "" && class != "web" {
		identifier = runnerEvidenceDepthRowIdentifier(row, indexes, class)
		if identifier != "" {
			if depth.contains(class, identifier) {
				return class, identifier, true
			}
			if webIdentifier := runnerEvidenceDepthRowIdentifier(row, indexes, "web"); webIdentifier != "" && depth.contains("web", webIdentifier) {
				return class, identifier, true
			}
			return class, identifier, false
		}
	}
	identifier = runnerEvidenceDepthRowIdentifier(row, indexes, "web")
	if identifier == "" {
		return class, "", false
	}
	return "web", identifier, depth.contains("web", identifier)
}

func runnerEvidenceDepthRowIdentifier(row []string, indexes []int, class string) string {
	for _, index := range indexes {
		if index >= len(row) {
			continue
		}
		value := strings.TrimSpace(row[index])
		if value == "" {
			continue
		}
		switch class {
		case "patent":
			if identifier := runnerEvidenceCanonicalPatent(value); identifier != "" {
				return identifier
			}
		case "trial":
			if identifier := strings.ToUpper(sessionRunnerNCTPattern.FindString(value)); identifier != "" {
				return identifier
			}
		case "publication":
			if identifier := runnerEvidenceCanonicalPublication(value); identifier != "" {
				return identifier
			}
		case "web":
			if identifier := runnerEvidenceCanonicalWeb(value); identifier != "" {
				return identifier
			}
		}
	}
	return ""
}

func runnerEvidenceCanonicalWeb(value string) string {
	canonical := canonicalWebResearchURL(strings.TrimSpace(value))
	if canonical == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("%x", digest[:])
}

func runnerEvidenceCanonicalPatent(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	for _, match := range runnerEvidenceDepthPatentIdentifierPattern.FindAllString(value, -1) {
		normalized := strings.NewReplacer("-", "", " ", "", "/", "").Replace(match)
		if strings.IndexFunc(normalized, func(character rune) bool {
			return character >= '0' && character <= '9'
		}) >= 0 {
			return normalized
		}
	}
	return ""
}

func runnerEvidenceCanonicalPublication(value string) string {
	if doi := sessionRunnerDOIPattern.FindString(value); doi != "" {
		return "doi:" + strings.ToLower(strings.TrimRight(doi, ".,;"))
	}
	if pmcid := regexp.MustCompile(`(?i)\bPMC[0-9]+\b`).FindString(value); pmcid != "" {
		return "pmcid:" + strings.ToUpper(pmcid)
	}
	if pmid := sessionRunnerPMIDLabelPattern.FindStringSubmatch(value); len(pmid) == 2 {
		if token := sessionRunnerPMIDTokenPattern.FindString(pmid[1]); token != "" {
			return "pmid:" + token
		}
	}
	if match := runnerEvidenceDepthPubMedURLPattern.FindStringSubmatch(value); len(match) == 2 {
		return "pmid:" + match[1]
	}
	return ""
}

// runnerEvidenceRecordDepthMergeTrustedSignals closes the small consistency
// window between a server-verified source terminal and the asynchronously
// rebuilt durable evidence projection. These signals are authored only by the
// server from successful governed source results and are persisted on terminal
// checkpoints; model text, files and stdout cannot manufacture them.
func runnerEvidenceRecordDepthMergeTrustedSignals(index *runnerEvidenceRecordDepthIndex, signals []string) {
	if index == nil {
		return
	}
	for _, signal := range signals {
		class, identifier, found := runnerEvidenceRecordSignal(signal)
		if !found {
			continue
		}
		switch class {
		case "patent":
			index.patents[identifier] = struct{}{}
		case "trial":
			index.trials[identifier] = struct{}{}
		case "publication":
			index.publications[identifier] = struct{}{}
		case "web":
			index.web[identifier] = struct{}{}
		}
	}
}

func (index runnerEvidenceRecordDepthIndex) contains(class, identifier string) bool {
	switch class {
	case "patent":
		_, found := index.patents[identifier]
		return found
	case "trial":
		_, found := index.trials[identifier]
		return found
	case "publication":
		_, found := index.publications[identifier]
		return found
	case "web":
		_, found := index.web[identifier]
		return found
	default:
		return false
	}
}

func runnerEvidenceRecordDepthIndexFromMessages(messages []agentruntime.Message) runnerEvidenceRecordDepthIndex {
	index := newRunnerEvidenceRecordDepthIndex()
	for _, receipt := range sessionRunnerEvidenceReceipts(messages) {
		var arguments map[string]any
		if json.Unmarshal(receipt.call.Arguments, &arguments) != nil {
			continue
		}
		var rawResult any
		if json.Unmarshal([]byte(strings.TrimSpace(receipt.result)), &rawResult) != nil {
			continue
		}
		normalizedTool := normalizeAgentToolName(receipt.call.Name)
		if normalizedTool == "repl" && runnerCorrectionIsSuccessfulMCPRepl(receipt.call, rawResult) {
			runnerEvidenceCollectExactMCPREPLRecord(arguments, &index)
			continue
		}
		result := runnerEvidenceDepthResultObject(receipt.result)
		if result == nil {
			continue
		}
		switch {
		case normalizedTool == "websearch":
			runnerEvidenceCollectSearchPublicationRecords(result, &index)
		case normalizedTool == "patentsearch":
			operation := strings.TrimSpace(stringValue(arguments["operation"]))
			if strings.EqualFold(operation, "search") {
				runnerEvidenceCollectDiscoveredPatents(result, &index)
				continue
			}
			if !strings.EqualFold(operation, "lookup") ||
				!strings.EqualFold(strings.TrimSpace(stringValue(result["evidenceDepth"])), "full_record") {
				continue
			}
			publicationNumber := firstNonEmpty(
				stringValue(arguments["publicationNumber"]),
				stringValue(arguments["publication_number"]),
			)
			identifier := runnerEvidenceCanonicalPatent(publicationNumber)
			focusTerms := stringArrayValue(arguments["focusTerms"])
			if len(focusTerms) == 0 {
				focusTerms = stringArrayValue(arguments["focus_terms"])
			}
			_, discovered := index.discoveredPatents[identifier]
			if len(focusTerms) > 0 && !runnerPatentLookupContainsFocusRelevantFullRecord(result) && !discovered {
				continue
			}
			if identifier != "" {
				index.patents[identifier] = struct{}{}
			}
		case strings.Contains(normalizedTool, "clinicaltrials") && strings.Contains(normalizedTool, "gettrialdetails"):
			if found, recorded := result["found"].(bool); recorded && !found {
				continue
			}
			if !runnerEvidenceResultHasSubstantiveField(result, runnerTrialSubstantiveFields, 0, new(int)) {
				continue
			}
			if identifier := strings.ToUpper(sessionRunnerNCTPattern.FindString(stringValue(arguments["nct_id"]))); identifier != "" {
				index.trials[identifier] = struct{}{}
			}
		case normalizedTool == "fetcharticlefulltext":
			if available, recorded := result["available"].(bool); recorded && !available &&
				!boolValue(result["recordAvailable"], false) {
				continue
			}
			if !runnerEvidenceResultHasSubstantiveField(result, runnerPublicationSubstantiveFields, 0, new(int)) {
				continue
			}
			for _, key := range []string{"doi", "pmcid"} {
				if identifier := runnerEvidenceCanonicalPublication(stringValue(arguments[key])); identifier != "" {
					index.publications[identifier] = struct{}{}
				}
			}
		case normalizedTool == "webfetch":
			runnerEvidenceCollectDeepWebRecord(arguments, rawResult, result, &index)
		case normalizedTool == "webresearch":
			runnerEvidenceCollectDeepWebResearchRecords(result, &index)
		case runnerEvidenceDepthStructuredPublicationTool(normalizedTool):
			if !runnerEvidenceResultHasSubstantiveField(result, runnerPublicationSubstantiveFields, 0, new(int)) {
				continue
			}
			runnerEvidenceCollectPublicationIdentifiers(result, &index, 0, new(int))
		}
	}
	return index
}

// A search provider may return a complete bibliographic abstract record in
// the same response as discovery hits. Preserve that measured record depth;
// ordinary snippets and incomplete metadata remain discovery-only.
func runnerEvidenceCollectSearchPublicationRecords(result map[string]any, index *runnerEvidenceRecordDepthIndex) {
	if index == nil {
		return
	}
	for _, rawSource := range anySliceValue(result["sources"]) {
		record := mapValue(mapValue(rawSource)["record"])
		if !strings.EqualFold(strings.TrimSpace(stringValue(record["record_depth"])), "abstract_record") ||
			!boolValue(record["abstract_complete"], false) || len([]rune(strings.TrimSpace(stringValue(record["abstract"])))) < 24 {
			continue
		}
		if identifier := runnerEvidenceCanonicalPublication(stringValue(record["citation_handle"])); identifier != "" {
			index.publications[identifier] = struct{}{}
		}
		for _, rawIdentifier := range anySliceValue(record["identifiers"]) {
			identifier := mapValue(rawIdentifier)
			namespace := strings.ToLower(strings.TrimSpace(stringValue(identifier["namespace"])))
			value := strings.TrimSpace(stringValue(identifier["value"]))
			if namespace == "" || value == "" {
				continue
			}
			if canonical := runnerEvidenceCanonicalPublication(strings.ToUpper(namespace) + ":" + value); canonical != "" {
				index.publications[canonical] = struct{}{}
			}
		}
	}
}

// runnerEvidenceCollectDiscoveredPatents preserves the identity continuity
// between a broad, task-scoped patent discovery and a later exact record read.
// This is intentionally separate from screened evidence: a discovery hit alone
// can never satisfy the ledger, but reading the same identifier in full does not
// become "off topic" merely because discovery and record pages use different
// languages or terminology.
func runnerEvidenceCollectDiscoveredPatents(result map[string]any, index *runnerEvidenceRecordDepthIndex) {
	if index == nil {
		return
	}
	var walk func(any, int)
	walk = func(value any, depth int) {
		if depth > 8 {
			return
		}
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := normalizeAgentToolName(key)
				if normalized == "publicationnumber" || normalized == "patentnumber" {
					if identifier := runnerEvidenceCanonicalPatent(stringValue(child)); identifier != "" {
						index.discoveredPatents[identifier] = struct{}{}
					}
				}
				walk(child, depth+1)
			}
		case []any:
			for _, child := range typed {
				walk(child, depth+1)
			}
		}
	}
	walk(result, 0)
}

func runnerEvidenceCollectDeepWebRecord(
	arguments map[string]any,
	rawResult any,
	result map[string]any,
	index *runnerEvidenceRecordDepthIndex,
) {
	if index == nil || len(trustedScientificValidatedPublicWebFetchSignals(arguments, rawResult)) == 0 {
		return
	}
	body := strings.TrimSpace(firstNonEmpty(
		stringValue(result["body"]), stringValue(result["content"]), stringValue(result["result"]),
	))
	if len([]rune(body)) < webResearchMinimumSubstantiveCharacters {
		return
	}
	// A substantive fetch of a patent-office page is an authoritative patent
	// record even though it travels through the generic web transport. Preserve
	// the exact publication identifier so a patent evidence row can be satisfied
	// without forcing the model into an unavailable connector-specific REPL.
	for _, candidate := range []string{stringValue(arguments["url"]), stringValue(result["url"])} {
		lowerCandidate := strings.ToLower(candidate)
		if !strings.Contains(lowerCandidate, "patent") &&
			!strings.Contains(lowerCandidate, "espacenet") &&
			!strings.Contains(lowerCandidate, "patentscope") {
			continue
		}
		if identifier := runnerEvidenceCanonicalPatent(candidate); identifier != "" {
			index.patents[identifier] = struct{}{}
			break
		}
	}
	for _, candidate := range []string{stringValue(arguments["url"]), stringValue(result["url"])} {
		if identifier := runnerEvidenceCanonicalWeb(candidate); identifier != "" {
			index.web[identifier] = struct{}{}
		}
	}
	// A complete substantive response fetched from a locator that itself names
	// an exact DOI, PMID, or PMCID is the corresponding publication record even
	// when the article headings are localized or the publisher uses non-English
	// markup. Bind the identifier from the governed request/response boundary;
	// do not require the model to copy an internal artifact handle into the
	// user-facing evidence table.
	for _, candidate := range []string{stringValue(arguments["url"]), stringValue(result["url"])} {
		if identifier := runnerEvidenceCanonicalPublication(candidate); identifier != "" {
			index.publications[identifier] = struct{}{}
			break
		}
	}
	var decoded any
	structured := json.Unmarshal([]byte(body), &decoded) == nil
	lower := strings.ToLower(body)
	publicationSubstantive := structured && runnerEvidenceResultHasSubstantiveField(
		decoded, runnerPublicationSubstantiveFields, 0, new(int),
	) || strings.Contains(lower, "abstract") &&
		(strings.Contains(lower, "method") || strings.Contains(lower, "result") || strings.Contains(lower, "conclusion"))
	trialSubstantive := structured && runnerEvidenceResultHasSubstantiveField(
		decoded, runnerTrialSubstantiveFields, 0, new(int),
	) || strings.Contains(lower, "intervention") && strings.Contains(lower, "outcome") &&
		(strings.Contains(lower, "eligibility") || strings.Contains(lower, "study design"))
	candidates := []string{
		stringValue(arguments["url"]), stringValue(result["url"]), body,
	}
	if publicationSubstantive {
		for _, candidate := range candidates {
			if identifier := runnerEvidenceCanonicalPublication(candidate); identifier != "" {
				index.publications[identifier] = struct{}{}
				break
			}
		}
	}
	if trialSubstantive {
		for _, candidate := range candidates {
			if identifier := strings.ToUpper(sessionRunnerNCTPattern.FindString(candidate)); identifier != "" {
				index.trials[identifier] = struct{}{}
				break
			}
		}
	}
}

func runnerEvidenceCollectDeepWebResearchRecords(result map[string]any, index *runnerEvidenceRecordDepthIndex) {
	if index == nil {
		return
	}
	for _, raw := range anySliceValue(result["documents"]) {
		document := mapValue(raw)
		if !boolValue(mapValue(document["readReceipt"])["deepRead"], false) {
			continue
		}
		for _, candidate := range []string{stringValue(document["url"]), stringValue(document["sourceLocator"])} {
			if identifier := runnerEvidenceCanonicalWeb(candidate); identifier != "" {
				index.web[identifier] = struct{}{}
			}
		}
	}
}

// runnerEvidenceCollectExactMCPREPLRecord closes the transport boundary between
// a governed host.mcp record read and the outer REPL receipt. The connector's
// full payload intentionally stays inside the kernel, but the literal source
// identifier and exact detail method remain durable in the admitted cell. A
// successful call whose evidence metadata was inspected therefore proves that
// exact record was read without exposing the raw connector payload or adding a
// competing flattened MCP tool path.
func runnerEvidenceCollectExactMCPREPLRecord(arguments map[string]any, index *runnerEvidenceRecordDepthIndex) {
	if index == nil {
		return
	}
	code := stringValue(arguments["code"])
	calls := parseLiteralMCPCalls(code)
	if len(calls) != 1 {
		return
	}
	call := calls[0]
	server := normalizeAgentToolName(call.server)
	method := normalizeAgentToolName(call.method)
	if !strings.Contains(server, "clinicaltrial") || !strings.Contains(method, "gettrialdetails") {
		return
	}
	match := runnerEvidenceDepthREPLNCTLiteralPattern.FindStringSubmatch(code)
	for _, candidate := range match[1:] {
		if identifier := strings.ToUpper(sessionRunnerNCTPattern.FindString(candidate)); identifier != "" {
			index.trials[identifier] = struct{}{}
			return
		}
	}
}

func runnerPatentLookupContainsFocusRelevantFullRecord(result map[string]any) bool {
	for _, raw := range anySliceValue(result["records"]) {
		record := mapValue(raw)
		if strings.EqualFold(strings.TrimSpace(stringValue(record["recordDepth"])), "full_record") &&
			boolValue(record["focusRelevant"], false) {
			return true
		}
	}
	return false
}

func runnerPatentLookupContainsFullRecord(result map[string]any) bool {
	for _, raw := range anySliceValue(result["records"]) {
		if strings.EqualFold(strings.TrimSpace(stringValue(mapValue(raw)["recordDepth"])), "full_record") {
			return true
		}
	}
	return false
}

var runnerPublicationSubstantiveFields = map[string]bool{
	"abstract": true, "abstracttext": true, "abstractinvertedindex": true,
	"fulltext": true, "body": true, "content": true, "sections": true,
}

var runnerTrialSubstantiveFields = map[string]bool{
	"briefsummary": true, "detaileddescription": true, "description": true,
	"design": true, "outcomes": true, "primaryoutcomes": true, "secondaryoutcomes": true,
	"eligibility": true, "interventions": true,
}

// runnerEvidenceResultHasSubstantiveField distinguishes a record read from a
// title/identifier hit. Source-specific detail methods remain free to evolve
// their envelope shape; the evidence contract is expressed through the
// substantive scientific fields rather than one hard-coded response path.
func runnerEvidenceResultHasSubstantiveField(
	value any,
	allowed map[string]bool,
	depth int,
	visited *int,
) bool {
	if visited == nil || depth > 10 || *visited >= 2048 {
		return false
	}
	(*visited)++
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if allowed[normalizeAgentToolName(key)] && runnerEvidenceSubstantiveValue(child) {
				return true
			}
			if runnerEvidenceResultHasSubstantiveField(child, allowed, depth+1, visited) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if runnerEvidenceResultHasSubstantiveField(child, allowed, depth+1, visited) {
				return true
			}
		}
	}
	return false
}

func runnerEvidenceSubstantiveValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return false
	}
}

func runnerEvidenceDepthStructuredPublicationTool(normalized string) bool {
	return (strings.Contains(normalized, "pubmed") || strings.Contains(normalized, "literature") ||
		strings.Contains(normalized, "openalex")) &&
		(strings.Contains(normalized, "metadata") || strings.Contains(normalized, "getwork") ||
			strings.Contains(normalized, "fulltext") || strings.Contains(normalized, "lookuparticle"))
}

func runnerEvidenceDepthResultObject(content string) map[string]any {
	var value any
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &value) != nil {
		return nil
	}
	for depth := 0; depth < 6; depth++ {
		if encoded, wrapped := value.(string); wrapped {
			var decoded any
			if json.Unmarshal([]byte(strings.TrimSpace(encoded)), &decoded) != nil {
				return nil
			}
			value = decoded
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		if okValue, recorded := object["ok"].(bool); recorded && !okValue {
			return nil
		}
		nested, found := object["result"]
		if !found {
			return object
		}
		switch typed := nested.(type) {
		case map[string]any:
			value = typed
		case string:
			var decoded any
			if json.Unmarshal([]byte(typed), &decoded) != nil {
				return object
			}
			value = decoded
		default:
			return object
		}
	}
	object, _ := value.(map[string]any)
	return object
}

func runnerEvidenceCollectPublicationIdentifiers(
	value any,
	index *runnerEvidenceRecordDepthIndex,
	depth int,
	visited *int,
) {
	if index == nil || visited == nil || depth > 10 || *visited >= 2048 {
		return
	}
	(*visited)++
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := normalizeAgentToolName(key)
			if normalized == "doi" || normalized == "pmid" || normalized == "pmcid" {
				if text, ok := child.(string); ok {
					candidate := text
					if normalized == "pmid" {
						candidate = "PMID:" + text
					}
					if identifier := runnerEvidenceCanonicalPublication(candidate); identifier != "" {
						index.publications[identifier] = struct{}{}
					}
				}
			}
			runnerEvidenceCollectPublicationIdentifiers(child, index, depth+1, visited)
		}
	case []any:
		for _, child := range typed {
			runnerEvidenceCollectPublicationIdentifiers(child, index, depth+1, visited)
		}
	}
}
