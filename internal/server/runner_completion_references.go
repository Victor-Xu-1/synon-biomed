package server

import (
	"encoding/csv"
	"encoding/json"

	"fmt"

	"net"

	"net/url"

	"sort"

	"strings"
	"unicode/utf8"

	"synon-go/internal/agentruntime"
)

func formatSessionRunnerReferenceDiagnostics(references []string) string {
	const (
		maxDiagnosticReferences      = 32
		maxDiagnosticIdentifierBytes = 256
		maxDiagnosticTotalBytes      = 2048
	)
	if len(references) == 0 {
		return "none"
	}
	references = append([]string(nil), references...)
	sort.Strings(references)
	parts := make([]string, 0, min(len(references), maxDiagnosticReferences))
	used := 0
	for _, reference := range references {
		if len(parts) >= maxDiagnosticReferences {
			break
		}
		part := truncateSessionRunnerReferenceDiagnostic(reference, maxDiagnosticIdentifierBytes)
		separatorBytes := 0
		if len(parts) > 0 {
			separatorBytes = 2
		}
		if used+separatorBytes+len(part) > maxDiagnosticTotalBytes {
			break
		}
		parts = append(parts, part)
		used += separatorBytes + len(part)
	}
	for len(parts) > 0 {
		remaining := len(references) - len(parts)
		if remaining <= 0 {
			break
		}
		suffix := fmt.Sprintf(", and %d more", remaining)
		detail := strings.Join(parts, ", ")
		if len(detail)+len(suffix) <= maxDiagnosticTotalBytes {
			return detail + suffix
		}
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d identifiers omitted", len(references))
	}
	return strings.Join(parts, ", ")
}

func truncateSessionRunnerReferenceDiagnostic(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	const suffix = "..."
	limit := maxBytes - len(suffix)
	if limit <= 0 {
		return suffix[:maxBytes]
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + suffix
}

func unsupportedSessionRunnerCitationCount(
	messages []agentruntime.Message,
	candidateStart int,
	finalContent string,
	artifactTexts []string,
) int {
	return len(unsupportedSessionRunnerCitationReferences(messages, candidateStart, finalContent, artifactTexts))
}

func unsupportedSessionRunnerCitationReferences(
	messages []agentruntime.Message,
	candidateStart int,
	finalContent string,
	artifactTexts []string,
) []string {
	return unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		messages, candidateStart, finalContent, artifactTexts, nil,
	)
}

type sessionRunnerArtifactCandidateReferences struct {
	positive         map[string]struct{}
	negative         map[string]struct{}
	rejected         []sessionRunnerRejectedReferenceCandidate
	origins          map[string][]string
	contractFailures []string
}

func unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
	messages []agentruntime.Message,
	candidateStart int,
	finalContent string,
	artifactTexts []string,
	artifactCandidates *sessionRunnerArtifactCandidateReferences,
) []string {
	return unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(
		messages, candidateStart, finalContent, artifactTexts, artifactCandidates, sessionRunnerEvidenceTool,
	)
}

func unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(
	messages []agentruntime.Message,
	candidateStart int,
	finalContent string,
	artifactTexts []string,
	artifactCandidates *sessionRunnerArtifactCandidateReferences,
	evidenceTool func(string) bool,
) []string {
	if evidenceTool == nil {
		evidenceTool = sessionRunnerEvidenceTool
	}
	evidence := map[string]struct{}{}
	negativeEvidence := map[string]struct{}{}
	evidencePairs := sessionRunnerUniqueToolEvidencePairs(messages)
	for _, pair := range evidencePairs {
		if !pair.call.VerifiedEvidence && !evidenceTool(pair.call.Name) {
			continue
		}
		if successfulSessionRunnerEvidenceResult(pair.content) {
			addVerifiedSessionRunnerReferences(evidence, pair.call, pair.content)
		}
		// A governed source may return an authoritative absence receipt. It is
		// evidence for a negative statement only and must never be promoted into
		// the positive evidence set above.
		addVerifiedSessionRunnerNegativeReferences(negativeEvidence, pair.call, pair.content)
	}
	candidate := map[string]struct{}{}
	negativeCandidate := map[string]struct{}{}
	addSessionRunnerCandidateReferences(candidate, negativeCandidate, finalContent)
	for _, content := range artifactTexts {
		addSessionRunnerCandidateReferences(candidate, negativeCandidate, content)
	}
	if artifactCandidates != nil {
		for identifier := range artifactCandidates.positive {
			candidate[identifier] = struct{}{}
		}
		for identifier := range artifactCandidates.negative {
			negativeCandidate[identifier] = struct{}{}
		}
	}
	if candidateStart < 0 || candidateStart > len(messages) {
		candidateStart = len(messages)
	}
	for _, message := range messages[candidateStart:] {
		for _, call := range message.ToolCalls {
			if !sessionRunnerCandidateOutputTool(call.Name) {
				continue
			}
			var input map[string]any
			if json.Unmarshal(call.Arguments, &input) == nil {
				addSessionRunnerCandidateReferences(candidate, negativeCandidate, stringValue(input["content"]))
			}
		}
	}
	unsupported := map[string]struct{}{}
	for identifier := range candidate {
		if _, found := evidence[identifier]; !found {
			unsupported[identifier] = struct{}{}
		}
	}
	for identifier := range negativeCandidate {
		if _, alsoPositive := candidate[identifier]; alsoPositive {
			continue
		}
		if _, verifiedAbsent := negativeEvidence[identifier]; !verifiedAbsent {
			unsupported[identifier] = struct{}{}
		}
	}
	if artifactCandidates != nil {
		for _, rejected := range artifactCandidates.rejected {
			if _, alsoPositive := candidate[rejected.reference]; alsoPositive {
				continue
			}
			if !verifiedSessionRunnerRejectedReference(rejected, evidencePairs) {
				unsupported[rejected.reference] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(unsupported))
	for identifier := range unsupported {
		result = append(result, identifier)
	}
	sort.Strings(result)
	return result
}

// verifiedSessionRunnerStructuredReferencesUsing exposes the same positive
// evidence authority used by the completion gate as a bounded repair aid.
// URLs are deliberately excluded: they can contain sensitive query material
// and are not interchangeable with record-level scientific identifiers.
func verifiedSessionRunnerStructuredReferencesUsing(
	messages []agentruntime.Message,
	evidenceTool func(string) bool,
) []string {
	if evidenceTool == nil {
		evidenceTool = sessionRunnerEvidenceTool
	}
	evidence := map[string]struct{}{}
	for _, pair := range sessionRunnerUniqueToolEvidencePairs(messages) {
		if (!pair.call.VerifiedEvidence && !evidenceTool(pair.call.Name)) ||
			!successfulSessionRunnerEvidenceResult(pair.content) {
			continue
		}
		addVerifiedSessionRunnerReferences(evidence, pair.call, pair.content)
	}
	result := make([]string, 0, len(evidence))
	for reference := range evidence {
		if strings.HasPrefix(reference, "pmid:") || strings.HasPrefix(reference, "doi:") ||
			strings.HasPrefix(reference, "nct:") || strings.HasPrefix(reference, "accession:") {
			result = append(result, reference)
		}
	}
	sort.Strings(result)
	return result
}

func addSessionRunnerCandidateReferences(positive, negative map[string]struct{}, value string) {
	addSessionRunnerScientificTableReferences(positive, value)
	if records, ok := sessionRunnerReferenceCSVRecords(value); ok {
		for _, record := range records {
			rowReferences := map[string]struct{}{}
			for _, field := range record {
				addSessionRunnerReferences(rowReferences, field)
			}
			rowText := strings.Join(record, " ")
			negativeRowReferences := sessionRunnerExplicitNegativeReferences(rowText)
			for identifier := range rowReferences {
				if _, markedNegative := negativeRowReferences[identifier]; markedNegative {
					negative[identifier] = struct{}{}
					continue
				}
				positive[identifier] = struct{}{}
			}
		}
		return
	}
	for _, line := range strings.Split(value, "\n") {
		lineReferences := map[string]struct{}{}
		addSessionRunnerReferences(lineReferences, line)
		negativeLineReferences := sessionRunnerExplicitNegativeReferences(line)
		for identifier := range lineReferences {
			if _, markedNegative := negativeLineReferences[identifier]; markedNegative {
				negative[identifier] = struct{}{}
				continue
			}
			if sessionRunnerCandidateReferenceLabelOnly(line, identifier) {
				continue
			}
			positive[identifier] = struct{}{}
		}
	}
}

func sessionRunnerReferenceCSVRecords(value string) ([][]string, bool) {
	reader := csv.NewReader(strings.NewReader(value))
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 || len(records[0]) < 2 {
		return nil, false
	}
	header := strings.ToLower(strings.Join(records[0], " "))
	if !containsAny(header, []string{
		"url", "uri", "link", "doi", "pmid", "nct", "accession",
		"来源", "链接", "网址", "文献", "专利号", "登记号",
	}) {
		return nil, false
	}
	return records, true
}

func sessionRunnerExplicitNegativeReferences(line string) map[string]struct{} {
	result := map[string]struct{}{}
	if match := sessionRunnerNegativeDOIAfter.FindStringSubmatch(line); len(match) == 2 {
		addSessionRunnerReference(result, "doi", "", match[1])
	} else if match := sessionRunnerNegativeDOIBefore.FindStringSubmatch(line); len(match) == 2 {
		addSessionRunnerReference(result, "doi", "", match[1])
	}
	if match := sessionRunnerNegativeNCTAfter.FindStringSubmatch(line); len(match) == 2 {
		addSessionRunnerReference(result, "nct", "", match[1])
	} else if match := sessionRunnerNegativeNCTBefore.FindStringSubmatch(line); len(match) == 2 {
		addSessionRunnerReference(result, "nct", "", match[1])
	}
	// Natural-language reports should not expose an internal phrase such as
	// "HTTP 404 verified" merely to pass the evidence gate. Infer absence only
	// when one structured identifier and an unambiguous negative statement
	// share the same line. Ambiguous wording remains fail-closed.
	if sessionRunnerNaturalNegativeClaim.MatchString(line) && !sessionRunnerUncertainNegative.MatchString(line) {
		lineReferences := map[string]struct{}{}
		addSessionRunnerReferences(lineReferences, line)
		structured := ""
		for identifier := range lineReferences {
			if strings.HasPrefix(identifier, "url:") {
				continue
			}
			if structured != "" {
				structured = ""
				break
			}
			structured = identifier
		}
		if structured != "" {
			result[structured] = struct{}{}
		}
	}
	return result
}

func sessionRunnerCandidateReferenceLabelOnly(line, identifier string) bool {
	if strings.TrimSpace(identifier) == "" {
		return false
	}
	visible := strings.TrimSpace(line)
	if !strings.HasPrefix(visible, "#") {
		return false
	}
	// A Markdown heading may introduce the record evaluated by the following
	// prose. Only a bounded set of navigation labels is neutral; a heading such
	// as "NCT... 正在招募" remains a positive assertion and is validated.
	references := map[string]struct{}{}
	addSessionRunnerReferences(references, visible)
	_, present := references[identifier]
	if !present || len(references) != 1 {
		return false
	}
	parts := strings.Split(identifier, ":")
	rawIdentifier := parts[len(parts)-1]
	remainder := strings.ToLower(visible)
	remainder = strings.TrimLeft(remainder, "# \t")
	remainder = strings.ReplaceAll(remainder, strings.ToLower(rawIdentifier), "")
	for _, label := range []string{
		"当前状态", "状态核查", "状态验证", "核查结果", "验证结果", "记录详情",
		"状态", "核查", "验证", "详情", "结果", "记录", "条目",
		"current status", "status check", "status verification", "verification result",
		"record details", "details", "detail", "result", "record", "entry",
	} {
		remainder = strings.ReplaceAll(remainder, label, "")
	}
	remainder = strings.Trim(remainder, " \t*_`：:—–-()（）[]【】")
	return remainder == ""
}

func addSessionRunnerReferences(target map[string]struct{}, value string) {
	for _, identifier := range sessionRunnerDOIPattern.FindAllString(strings.ToLower(value), -1) {
		addSessionRunnerReference(target, "doi", "", identifier)
	}
	for _, identifier := range sessionRunnerNCTPattern.FindAllString(value, -1) {
		addSessionRunnerReference(target, "nct", "", identifier)
	}
	for _, identifier := range sessionRunnerURLPattern.FindAllString(value, -1) {
		addSessionRunnerURLReference(target, identifier)
	}
	for _, match := range sessionRunnerPMIDLabelPattern.FindAllStringSubmatch(value, -1) {
		if len(match) != 2 {
			continue
		}
		for _, identifier := range sessionRunnerPMIDTokenPattern.FindAllString(match[1], -1) {
			addSessionRunnerReference(target, "pmid", "", identifier)
		}
	}
	addSessionRunnerScientificTableReferences(target, value)
	for _, match := range sessionRunnerPDBPattern.FindAllStringSubmatch(value, -1) {
		if len(match) == 2 {
			addSessionRunnerReference(target, "accession", "pdb", match[1])
		}
	}
	for _, match := range sessionRunnerPDBListPattern.FindAllStringSubmatch(value, -1) {
		if len(match) != 2 {
			continue
		}
		for _, identifier := range sessionRunnerPDBTokenPattern.FindAllString(match[1], -1) {
			addSessionRunnerReference(target, "accession", "pdb", identifier)
		}
	}
	addSessionRunnerPDBProseReferences(target, value)
	for _, match := range sessionRunnerUniProtPattern.FindAllStringSubmatch(value, -1) {
		if len(match) == 2 {
			addSessionRunnerReference(target, "accession", "uniprot", match[1])
		}
	}
	for _, definition := range sessionRunnerAccessionPatterns {
		for _, identifier := range definition.pattern.FindAllString(value, -1) {
			addSessionRunnerReference(target, "accession", definition.namespace, identifier)
		}
	}
}

func addSessionRunnerPDBProseReferences(target map[string]struct{}, value string) {
	const (
		maxPDBTailBytes  = 256
		maxPDBReferences = 32
	)
	for _, label := range sessionRunnerPDBLabelPattern.FindAllStringIndex(value, -1) {
		tail := value[label[1]:]
		if len(tail) > maxPDBTailBytes {
			tail = tail[:maxPDBTailBytes]
		}
		offset := 0
		for found := 0; found < maxPDBReferences; found++ {
			offset = skipSessionRunnerPDBListPadding(tail, offset)
			if hasSessionRunnerListConjunction(tail, offset, "and") {
				offset += len("and")
				offset = skipSessionRunnerPDBListPadding(tail, offset)
			} else if hasSessionRunnerListConjunction(tail, offset, "or") {
				offset += len("or")
				offset = skipSessionRunnerPDBListPadding(tail, offset)
			}
			match := sessionRunnerPDBTokenPattern.FindStringIndex(tail[offset:])
			if match == nil || match[0] != 0 {
				break
			}
			identifier := tail[offset : offset+match[1]]
			addSessionRunnerReference(target, "accession", "pdb", identifier)
			offset += match[1]
			for offset < len(tail) && (tail[offset] == ' ' || tail[offset] == '\t') {
				offset++
			}
			if offset >= len(tail) || tail[offset] == ')' {
				break
			}
			if tail[offset] != ',' && tail[offset] != ';' && tail[offset] != '/' {
				break
			}
			offset++
		}
	}
}

func skipSessionRunnerPDBListPadding(value string, offset int) int {
	for offset < len(value) {
		switch value[offset] {
		case ' ', '\t', '\r', '\n', ':', '#', '=', '-', '(':
			offset++
		default:
			return offset
		}
	}
	return offset
}

func hasSessionRunnerListConjunction(value string, offset int, conjunction string) bool {
	if offset < 0 || offset+len(conjunction) > len(value) || !strings.EqualFold(value[offset:offset+len(conjunction)], conjunction) {
		return false
	}
	end := offset + len(conjunction)
	return end == len(value) || value[end] == ' ' || value[end] == '\t' || value[end] == '\r' || value[end] == '\n'
}

func addSessionRunnerReference(target map[string]struct{}, kind, namespace, identifier string) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	identifier = strings.TrimSpace(identifier)
	switch kind {
	case "doi":
		identifier = strings.ToLower(strings.TrimRight(identifier, ".,;:"))
		for strings.HasSuffix(identifier, ")") && strings.Count(identifier, ")") > strings.Count(identifier, "(") {
			identifier = strings.TrimSuffix(identifier, ")")
		}
		if !sessionRunnerDOIExactPattern.MatchString(identifier) {
			return
		}
	case "nct":
		identifier = strings.ToUpper(strings.TrimRight(identifier, ".,;:"))
		if !sessionRunnerNCTExactPattern.MatchString(identifier) {
			return
		}
	case "pmid":
		if !sessionRunnerPMIDExactPattern.MatchString(identifier) {
			return
		}
	case "accession":
		identifier = strings.ToUpper(strings.TrimRight(identifier, ".,;:"))
		if namespace == "uniprot" {
			// ClinVar xrefs carry variant suffixes (Q9UM73#VAR_063857);
			// the base accession is the citation contract.
			if cut, _, found := strings.Cut(identifier, "#VAR"); found {
				identifier = strings.TrimSpace(cut)
			}
		}
		if namespace == "clinvar" {
			// ClinVar records include a version suffix (VCV000012582.68);
			// the base accession is the citation contract.
			if dot := strings.IndexByte(identifier, '.'); dot > 0 {
				identifier = identifier[:dot]
			}
		}
		if !validSessionRunnerAccession(namespace, identifier) {
			return
		}
	default:
		return
	}
	if identifier == "" {
		return
	}
	key := kind + ":" + identifier
	if kind == "accession" {
		key = kind + ":" + namespace + ":" + identifier
	}
	target[key] = struct{}{}
}

func validSessionRunnerAccession(namespace, identifier string) bool {
	switch namespace {
	case "pdb":
		return sessionRunnerPDBExactPattern.MatchString(identifier)
	case "uniprot":
		return sessionRunnerUniProtExactPattern.MatchString(identifier)
	case "chembl", "ensembl", "clinvar", "dbsnp", "refseq", "geo", "sra", "pride":
		for _, definition := range sessionRunnerAccessionPatterns {
			if definition.namespace == namespace && strings.EqualFold(definition.pattern.FindString(identifier), identifier) {
				return true
			}
		}
	}
	return false
}

func addSessionRunnerURLReference(target map[string]struct{}, raw string) {
	canonical, parsed, ok := canonicalSessionRunnerURL(raw)
	if !ok || !publicSessionRunnerCitationHost(parsed.Hostname()) {
		return
	}
	host := strings.ToLower(parsed.Hostname())
	segments := strings.FieldsFunc(parsed.EscapedPath(), func(r rune) bool { return r == '/' })
	if host == "doi.org" || host == "dx.doi.org" {
		identifier, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
		if err == nil && sessionRunnerDOIExactPattern.MatchString(identifier) {
			addSessionRunnerReference(target, "doi", "", identifier)
			return
		}
	}
	if host == "api.crossref.org" && sessionRunnerCrossrefQueryAllowed(parsed.Query()) {
		if identifier, found := sessionRunnerCrossrefRequestDOI(parsed); found {
			addSessionRunnerReference(target, "doi", "", identifier)
			return
		}
	}
	if identifier, found := sessionRunnerPDBRequestID(parsed); found {
		addSessionRunnerReference(target, "accession", "pdb", identifier)
		return
	}
	if identifiers := sessionRunnerPubMedRequestIdentifiers(parsed); len(identifiers) > 0 {
		for identifier := range identifiers {
			addSessionRunnerReference(target, "pmid", "", identifier)
		}
		return
	}
	if host == "pubmed.ncbi.nlm.nih.gov" && len(segments) > 0 && sessionRunnerPMIDExactPattern.MatchString(segments[0]) {
		addSessionRunnerReference(target, "pmid", "", segments[0])
		return
	}
	if (host == "ncbi.nlm.nih.gov" || host == "www.ncbi.nlm.nih.gov") && len(segments) > 1 && strings.EqualFold(segments[0], "pubmed") && sessionRunnerPMIDExactPattern.MatchString(segments[1]) {
		addSessionRunnerReference(target, "pmid", "", segments[1])
		return
	}
	if host == "clinicaltrials.gov" || host == "www.clinicaltrials.gov" {
		for _, segment := range segments {
			if sessionRunnerNCTExactPattern.MatchString(segment) {
				addSessionRunnerReference(target, "nct", "", segment)
				return
			}
		}
	}
	if sessionRunnerSourceLandingURL(parsed) {
		return
	}
	target["url:"+canonical] = struct{}{}
}

// Source landing pages identify a governed database, not a record inside it.
// Treating them as record citations makes an otherwise fully grounded answer
// impossible to complete, while record URLs and identifier-bearing API URLs
// remain subject to the evidence gate above.
func sessionRunnerSourceLandingURL(parsed *url.URL) bool {
	if parsed == nil || parsed.RawQuery != "" {
		return false
	}
	_, allowed := map[string]struct{}{
		"https://clinicaltrials.gov/":     {},
		"https://www.clinicaltrials.gov/": {},
		"https://www.ebi.ac.uk/chembl/":   {},
	}[parsed.String()]
	return allowed
}

func canonicalSessionRunnerURL(raw string) (string, *url.URL, bool) {
	raw = strings.Trim(strings.TrimSpace(raw), "<>\"'`")
	if boundary := strings.Index(raw, "]("); boundary > 0 {
		raw = strings.TrimSpace(raw[:boundary])
	}
	for raw != "" {
		last := raw[len(raw)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?', ']', '}':
			raw = strings.TrimSpace(raw[:len(raw)-1])
			continue
		case ')':
			if strings.Count(raw, ")") > strings.Count(raw, "(") {
				raw = strings.TrimSpace(raw[:len(raw)-1])
				continue
			}
		}
		break
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" {
		return "", nil, false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", nil, false
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		if port == "" {
			parsed.Host = "[" + host + "]"
		} else {
			parsed.Host = net.JoinHostPort(host, port)
		}
	} else if port == "" {
		parsed.Host = host
	} else {
		parsed.Host = net.JoinHostPort(host, port)
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	parsed.RawQuery = parsed.Query().Encode()
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), parsed, true
}

func publicSessionRunnerCitationHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast()
	}
	return true
}

func addSessionRunnerScientificTableReferences(target map[string]struct{}, value string) {
	records, err := csv.NewReader(strings.NewReader(value)).ReadAll()
	if err == nil {
		addSessionRunnerScientificColumns(target, records)
	}
	lines := strings.Split(value, "\n")
	for index := 0; index < len(lines); {
		if !strings.Contains(lines[index], "|") {
			index++
			continue
		}
		pipeRows := make([][]string, 0, 8)
		for index < len(lines) && strings.Contains(lines[index], "|") {
			pipeRows = append(pipeRows, strings.Split(strings.Trim(lines[index], " |"), "|"))
			index++
		}
		addSessionRunnerScientificColumns(target, pipeRows)
	}
}

func addSessionRunnerScientificColumns(target map[string]struct{}, rows [][]string) {
	if len(rows) < 2 || len(rows[0]) < 2 {
		return
	}
	pmidColumns := []int{}
	pdbColumns := []int{}
	for index, heading := range rows[0] {
		heading = strings.ToLower(strings.TrimSpace(heading))
		if strings.Contains(heading, "pmid") {
			pmidColumns = append(pmidColumns, index)
		}
		if strings.Contains(heading, "pdb") {
			pdbColumns = append(pdbColumns, index)
		}
	}
	for _, row := range rows[1:] {
		for _, index := range pmidColumns {
			if index >= len(row) {
				continue
			}
			for _, token := range sessionRunnerPMIDTokenPattern.FindAllString(row[index], -1) {
				addSessionRunnerReference(target, "pmid", "", token)
			}
		}
		for _, index := range pdbColumns {
			if index >= len(row) {
				continue
			}
			for _, token := range sessionRunnerPDBTokenPattern.FindAllString(row[index], -1) {
				addSessionRunnerReference(target, "accession", "pdb", token)
			}
		}
	}
}
