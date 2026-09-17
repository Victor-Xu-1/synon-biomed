package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"synon-go/internal/agentruntime"
)

const (
	sessionRunnerRejectedReferenceSchemaV1       = "synon.rejected_reference.v1"
	sessionRunnerRejectedReferenceArtifactNameV1 = sessionRunnerRejectedReferenceSchemaV1 + ".json"
	maxSessionRunnerRejectedReferences           = 256
	maxSessionRunnerRejectedTitleBytes           = 1024
)

type sessionRunnerRejectedReferenceDocumentV1 struct {
	Schema  string                                   `json:"schema"`
	Records []sessionRunnerRejectedReferenceRecordV1 `json:"records"`
}

type sessionRunnerRejectedReferenceRecordV1 struct {
	Reference   sessionRunnerRejectedReferenceValueV1    `json:"reference"`
	Disposition string                                   `json:"disposition"`
	Evidence    sessionRunnerRejectedReferenceEvidenceV1 `json:"evidence"`
}

type sessionRunnerRejectedReferenceValueV1 struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type sessionRunnerRejectedReferenceEvidenceV1 struct {
	Provider      string `json:"provider"`
	ToolCallID    string `json:"tool_call_id"`
	HTTPStatus    int    `json:"http_status"`
	ObservedDOI   string `json:"observed_doi,omitempty"`
	ObservedTitle string `json:"observed_title,omitempty"`
}

type sessionRunnerRejectedReferenceCandidate struct {
	reference   string
	disposition string
	evidence    sessionRunnerRejectedReferenceEvidenceV1
	origin      string
}

type sessionRunnerToolEvidencePair struct {
	call    agentruntime.ToolCall
	content string
}

const sessionRunnerReferenceEvidenceLedgerSchemaV1 = "synon.reference_evidence_ledger.v1"

type sessionRunnerReferenceEvidenceLedgerV1 struct {
	Schema   string                                         `json:"schema"`
	Crossref []sessionRunnerReferenceEvidenceLedgerRecordV1 `json:"crossref"`
}

type sessionRunnerReferenceEvidenceLedgerRecordV1 struct {
	Reference     sessionRunnerRejectedReferenceValueV1 `json:"reference"`
	Provider      string                                `json:"provider"`
	ToolCallID    string                                `json:"tool_call_id"`
	HTTPStatus    int                                   `json:"http_status"`
	ObservedDOI   string                                `json:"observed_doi,omitempty"`
	ObservedTitle string                                `json:"observed_title,omitempty"`
}

func sessionRunnerUniqueToolEvidencePairs(messages []agentruntime.Message) map[string]sessionRunnerToolEvidencePair {
	calls := map[string][]agentruntime.ToolCall{}
	results := map[string][]string{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if id := strings.TrimSpace(call.ID); id != "" {
				calls[id] = append(calls[id], call)
			}
		}
		if message.Role == "tool" {
			if id := strings.TrimSpace(message.ToolCallID); id != "" {
				results[id] = append(results[id], message.Content)
			}
		}
	}
	result := map[string]sessionRunnerToolEvidencePair{}
	for id, groupedCalls := range calls {
		groupedResults := results[id]
		if len(groupedCalls) == 0 || len(groupedCalls) != len(groupedResults) {
			continue
		}
		canonicalCall := groupedCalls[0]
		canonicalResult := groupedResults[0]
		unique := true
		for index := 1; index < len(groupedCalls); index++ {
			if !sessionRunnerEvidencePairEquivalent(
				canonicalCall, canonicalResult, groupedCalls[index], groupedResults[index],
			) {
				unique = false
				break
			}
		}
		if unique {
			result[id] = sessionRunnerToolEvidencePair{call: canonicalCall, content: canonicalResult}
		}
	}
	return result
}

func sessionRunnerReferenceEvidenceLedgerJSON(messages []agentruntime.Message) string {
	records := make([]sessionRunnerReferenceEvidenceLedgerRecordV1, 0)
	for toolCallID, pair := range sessionRunnerUniqueToolEvidencePairs(messages) {
		if normalizeAgentToolName(pair.call.Name) != "webfetch" {
			continue
		}
		var envelope any
		if json.Unmarshal([]byte(pair.content), &envelope) != nil || agentruntime.ClassifyToolResult(envelope).Failed() {
			continue
		}
		endpoint, requestURL, strict := strictSessionRunnerWebFetchEvidenceEndpoint(pair.call.Arguments, envelope)
		if !strict || endpoint != "crossref" || requestURL == nil {
			continue
		}
		doi, ok := sessionRunnerCrossrefRequestDOI(requestURL)
		if !ok {
			continue
		}
		record := sessionRunnerReferenceEvidenceLedgerRecordV1{
			Reference:  sessionRunnerRejectedReferenceValueV1{Kind: "doi", Value: doi},
			Provider:   "crossref",
			ToolCallID: toolCallID,
			HTTPStatus: sessionRunnerWebFetchStatusCode(envelope),
		}
		switch record.HTTPStatus {
		case http.StatusNotFound:
		case http.StatusOK:
			result, found := sessionRunnerWebFetchResultMap(envelope)
			if !found {
				continue
			}
			body := firstNonEmpty(stringValue(result["body"]), stringValue(result["result"]), stringValue(result["content"]))
			var response struct {
				Status  string `json:"status"`
				Message struct {
					DOI   string   `json:"DOI"`
					Title []string `json:"title"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(body), &response) != nil ||
				!strings.EqualFold(strings.TrimSpace(response.Status), "ok") ||
				strings.ToLower(strings.TrimSpace(response.Message.DOI)) != doi || len(response.Message.Title) == 0 {
				continue
			}
			title := strings.TrimSpace(response.Message.Title[0])
			if title == "" || len(title) > maxSessionRunnerRejectedTitleBytes ||
				strings.IndexFunc(title, func(r rune) bool { return unicode.IsControl(r) }) >= 0 {
				continue
			}
			record.ObservedDOI = doi
			record.ObservedTitle = title
		default:
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Reference.Value != records[j].Reference.Value {
			return records[i].Reference.Value < records[j].Reference.Value
		}
		return records[i].ToolCallID < records[j].ToolCallID
	})
	if len(records) > maxSessionRunnerRejectedReferences {
		records = records[:maxSessionRunnerRejectedReferences]
	}
	if len(records) == 0 {
		return ""
	}
	encoded, err := json.Marshal(sessionRunnerReferenceEvidenceLedgerV1{
		Schema: sessionRunnerReferenceEvidenceLedgerSchemaV1, Crossref: records,
	})
	if err != nil {
		return ""
	}
	return string(encoded)
}

// decodeSessionRunnerRejectedReferenceV1 recognizes only a complete, typed
// JSON document. A malformed or future rejected-reference schema is still
// recognized and fails closed instead of falling back to prose scanning.
func decodeSessionRunnerRejectedReferenceV1(
	content, origin string,
) ([]sessionRunnerRejectedReferenceCandidate, bool, string) {
	var probe struct {
		Schema json.RawMessage `json:"schema"`
	}
	if json.Unmarshal([]byte(content), &probe) != nil || len(probe.Schema) == 0 {
		return nil, false, ""
	}
	var schema string
	if json.Unmarshal(probe.Schema, &schema) != nil || !strings.HasPrefix(schema, "synon.rejected_reference.") {
		return nil, false, ""
	}
	failure := func(code string) ([]sessionRunnerRejectedReferenceCandidate, bool, string) {
		return nil, true, truncateSessionRunnerReferenceDiagnostic(origin+"#"+code, 512)
	}
	if schema != sessionRunnerRejectedReferenceSchemaV1 {
		return failure("unsupported_schema")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	var document sessionRunnerRejectedReferenceDocumentV1
	if decoder.Decode(&document) != nil {
		return failure("invalid_document")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return failure("trailing_data")
	}
	if document.Schema != sessionRunnerRejectedReferenceSchemaV1 || len(document.Records) == 0 ||
		len(document.Records) > maxSessionRunnerRejectedReferences {
		return failure("invalid_record_count")
	}
	seen := make(map[string]struct{}, len(document.Records))
	result := make([]sessionRunnerRejectedReferenceCandidate, 0, len(document.Records))
	for index, record := range document.Records {
		pointer := origin + "#/records/" + strconv.Itoa(index)
		value := strings.TrimSpace(record.Reference.Value)
		if record.Reference.Kind != "doi" || value != strings.ToLower(value) ||
			strings.TrimRight(value, ".,;:") != value ||
			strings.HasSuffix(value, ")") && strings.Count(value, ")") > strings.Count(value, "(") ||
			!sessionRunnerDOIExactPattern.MatchString(value) {
			return failure("invalid_reference_" + strconv.Itoa(index))
		}
		if _, duplicate := seen[value]; duplicate {
			return failure("duplicate_reference_" + strconv.Itoa(index))
		}
		seen[value] = struct{}{}
		evidence := record.Evidence
		evidence.ToolCallID = strings.TrimSpace(evidence.ToolCallID)
		if evidence.Provider != "crossref" || evidence.ToolCallID == "" || len(evidence.ToolCallID) > 256 ||
			strings.IndexFunc(evidence.ToolCallID, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
			return failure("invalid_evidence_" + strconv.Itoa(index))
		}
		switch record.Disposition {
		case "not_found":
			if evidence.HTTPStatus != http.StatusNotFound || evidence.ObservedDOI != "" || evidence.ObservedTitle != "" {
				return failure("invalid_not_found_evidence_" + strconv.Itoa(index))
			}
		default:
			return failure("invalid_disposition_" + strconv.Itoa(index))
		}
		result = append(result, sessionRunnerRejectedReferenceCandidate{
			reference: "doi:" + value, disposition: record.Disposition, evidence: evidence, origin: pointer,
		})
	}
	return result, true, ""
}

func verifiedSessionRunnerRejectedReference(
	candidate sessionRunnerRejectedReferenceCandidate,
	pairs map[string]sessionRunnerToolEvidencePair,
) bool {
	pair, found := pairs[candidate.evidence.ToolCallID]
	if !found || normalizeAgentToolName(pair.call.Name) != "webfetch" {
		return false
	}
	var envelope any
	if json.Unmarshal([]byte(pair.content), &envelope) != nil || agentruntime.ClassifyToolResult(envelope).Failed() {
		return false
	}
	endpoint, requestURL, strict := strictSessionRunnerWebFetchEvidenceEndpoint(pair.call.Arguments, envelope)
	if !strict || endpoint != "crossref" || requestURL == nil {
		return false
	}
	doi, ok := sessionRunnerCrossrefRequestDOI(requestURL)
	if !ok || candidate.reference != "doi:"+doi || sessionRunnerWebFetchStatusCode(envelope) != candidate.evidence.HTTPStatus {
		return false
	}
	switch candidate.disposition {
	case "not_found":
		return candidate.evidence.HTTPStatus == http.StatusNotFound
	default:
		return false
	}
}

func addSessionRunnerArtifactCandidateReferencesWithOrigin(
	target *sessionRunnerArtifactCandidateReferences,
	content, origin string,
) {
	if target == nil {
		return
	}
	addSessionRunnerCandidateReferences(target.positive, target.negative, content)
	for index, line := range strings.Split(content, "\n") {
		positive, negative := map[string]struct{}{}, map[string]struct{}{}
		addSessionRunnerCandidateReferences(positive, negative, line)
		lineOrigin := origin + "#line=" + strconv.Itoa(index+1)
		for identifier := range positive {
			appendSessionRunnerReferenceOrigin(target.origins, identifier, lineOrigin)
		}
		for identifier := range negative {
			appendSessionRunnerReferenceOrigin(target.origins, identifier, lineOrigin)
		}
	}
	for identifier := range target.positive {
		if len(target.origins[identifier]) == 0 {
			appendSessionRunnerReferenceOrigin(target.origins, identifier, origin)
		}
	}
	for identifier := range target.negative {
		if len(target.origins[identifier]) == 0 {
			appendSessionRunnerReferenceOrigin(target.origins, identifier, origin)
		}
	}
}

func appendSessionRunnerReferenceOrigin(target map[string][]string, identifier, origin string) {
	if target == nil || identifier == "" || origin == "" {
		return
	}
	for _, existing := range target[identifier] {
		if existing == origin {
			return
		}
	}
	target[identifier] = append(target[identifier], origin)
}

func sessionRunnerReferenceDiagnostics(
	references []string,
	candidates *sessionRunnerArtifactCandidateReferences,
) []string {
	result := make([]string, 0, len(references))
	for _, reference := range references {
		diagnostic := reference
		if candidates != nil && len(candidates.origins[reference]) > 0 {
			diagnostic += " @ " + strings.Join(candidates.origins[reference], ",")
		}
		result = append(result, diagnostic)
	}
	return result
}
