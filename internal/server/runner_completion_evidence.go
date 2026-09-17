package server

import (
	"encoding/json"

	"net"
	"net/http"
	"net/url"

	"strconv"
	"strings"

	"synon-go/internal/agentruntime"
)

func sessionRunnerCandidateOutputTool(name string) bool {
	switch normalizeAgentToolName(name) {
	case "write", "filewrite", "edit", "editfile", "filepatch", "filereplace", "patch", "saveartifacts", "artifactregister":
		return true
	default:
		return false
	}
}

func sessionRunnerEvidenceTool(name string) bool {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(strings.ToLower(name), "mcp__") {
		return true
	}
	return agentRuntimeToolSchemaHasCapability(
		agentruntime.ToolSchema{Name: name}, runtimeCapabilitySourceEvidence,
	)
}

// sessionRunnerEvidenceTool uses the governed tool catalog as the source of
// truth for first-party evidence producers. This keeps completion validation
// aligned with the executed tool contract without trusting arbitrary REPL,
// shell, Python, or artifact text as provenance.
func (s *Server) sessionRunnerEvidenceTool(name string) bool {
	// An MCP-shaped name is not provenance. Custom connectors may choose any
	// tool name, so only an executable governed registry entry with the explicit
	// source-evidence capability may authorize MCP-derived identifiers.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "mcp__") {
		if s == nil || s.tools == nil {
			return false
		}
		tool, found := s.registeredTool(strings.TrimSpace(name))
		if !found || !tool.Executable {
			return false
		}
		for _, capability := range tool.Capabilities {
			if capability == "source-evidence" {
				return true
			}
		}
		return false
	}
	if sessionRunnerEvidenceTool(name) {
		return true
	}
	if s == nil || s.tools == nil {
		return false
	}
	tool, found := s.registeredTool(strings.TrimSpace(name))
	if !found || !tool.Executable {
		return false
	}
	for _, capability := range tool.Capabilities {
		if capability == "source-evidence" {
			return true
		}
	}
	return false
}

func successfulSessionRunnerEvidenceResult(content string) bool {
	var envelope any
	if json.Unmarshal([]byte(content), &envelope) != nil {
		return false
	}
	return !agentruntime.ClassifyToolResult(envelope).Failed()
}

func addVerifiedSessionRunnerReferences(target map[string]struct{}, call agentruntime.ToolCall, content string) {
	var envelope any
	if json.Unmarshal([]byte(content), &envelope) != nil || agentruntime.ClassifyToolResult(envelope).Failed() {
		return
	}
	if normalizeAgentToolName(call.Name) == "webresearch" {
		// web_research contains both a broad discovery pool and the smaller set
		// of pages actually read. Only complete, relevant deep-read documents
		// can attest identifiers; candidate titles and snippets remain locators.
		object, ok := envelope.(map[string]any)
		if !ok {
			return
		}
		if nested, nestedOK := object["result"].(map[string]any); nestedOK {
			object = nested
		}
		for _, raw := range anySliceValue(object["documents"]) {
			document := mapValue(raw)
			if !boolValue(mapValue(document["readReceipt"])["deepRead"], false) {
				continue
			}
			collectVerifiedSessionRunnerReferences(target, document, 0, new(int), true)
		}
		return
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(call.Name)), "mcp__") {
		if sessionRunnerMCPDiscoveryTool(call.Name) {
			return
		}
		if wrapper, ok := envelope.(map[string]any); ok {
			if rawResult, ok := wrapper["result"].(string); ok {
				var structuredResult any
				if json.Unmarshal([]byte(rawResult), &structuredResult) == nil {
					if record, isRecord := structuredResult.(map[string]any); isRecord {
						if found, exists := record["found"].(bool); exists && !found {
							return
						}
					}
					collectVerifiedSessionRunnerReferences(target, structuredResult, 0, new(int), false)
					return
				}
			}
		}
	}
	if normalizeAgentToolName(call.Name) == "webfetch" {
		endpoint, requestURL, strictEndpoint := strictSessionRunnerWebFetchEvidenceEndpoint(call.Arguments, envelope)
		if strictEndpoint && requestURL == nil {
			return
		}
		if strictEndpoint {
			// Exact structured-record endpoints are interpreted by endpoint-specific
			// validators. They may legitimately return compact JSON, so the generic
			// long-form page threshold must not discard them before validation.
			switch endpoint {
			case "pubmed":
				addVerifiedSessionRunnerPubMedReferences(target, envelope, requestURL)
				return
			case "europepmc":
				addVerifiedSessionRunnerEuropePMCReferences(target, envelope, requestURL)
				return
			case "pdb":
				addVerifiedSessionRunnerPDBReferences(target, envelope, requestURL)
				return
			case "pdb_polymer_entity":
				addVerifiedSessionRunnerPDBPolymerEntityReferences(target, envelope, requestURL)
				return
			case "crossref":
				// Crossref proves bibliographic lookup status, not the scientific
				// proposition surrounding a DOI. Only the closed 404 negative
				// contract below may consume this endpoint.
				return
			case "clinicaltrials":
				addVerifiedSessionRunnerClinicalTrialsReferences(target, envelope, requestURL)
				return
			case "chembl_mechanism":
				addVerifiedSessionRunnerChEMBLMechanismReferences(target, envelope, requestURL)
				return
			}
		}
	}
	allowText := sessionRunnerEvidenceTextAllowed(call.Name, envelope)
	collectVerifiedSessionRunnerReferences(target, envelope, 0, new(int), allowText)
}

func addVerifiedSessionRunnerNegativeReferences(target map[string]struct{}, call agentruntime.ToolCall, content string) {
	var envelope any
	if json.Unmarshal([]byte(content), &envelope) != nil {
		return
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(call.Name)), "mcp__") {
		result, ok := sessionRunnerMCPNotFoundResult(envelope)
		if !ok {
			return
		}
		collectSessionRunnerNegativeStructuredReferences(target, result, 0, new(int))
		return
	}
	if normalizeAgentToolName(call.Name) != "webfetch" || agentruntime.ClassifyToolResult(envelope).Failed() {
		return
	}
	endpoint, requestURL, strictEndpoint := strictSessionRunnerWebFetchEvidenceEndpoint(call.Arguments, envelope)
	if !strictEndpoint || requestURL == nil || sessionRunnerWebFetchStatusCode(envelope) != http.StatusNotFound {
		return
	}
	switch endpoint {
	case "crossref":
		identifier, ok := sessionRunnerCrossrefRequestDOI(requestURL)
		if !ok {
			return
		}
		addSessionRunnerReference(target, "doi", "", identifier)
	case "clinicaltrials":
		identifier, ok := sessionRunnerClinicalTrialsRequestNCT(requestURL)
		if !ok {
			return
		}
		addSessionRunnerReference(target, "nct", "", identifier)
	}
}

func sessionRunnerMCPNotFoundResult(envelope any) (map[string]any, bool) {
	result, ok := envelope.(map[string]any)
	if !ok {
		return nil, false
	}
	if explicit, exists := result["ok"]; exists {
		if succeeded, valid := explicit.(bool); !valid || !succeeded {
			return nil, false
		}
	}
	if raw, exists := result["result"]; exists {
		switch typed := raw.(type) {
		case string:
			var decoded map[string]any
			if json.Unmarshal([]byte(typed), &decoded) != nil {
				return nil, false
			}
			result = decoded
		case map[string]any:
			result = typed
		}
	}
	found, exists := result["found"]
	foundValue, valid := found.(bool)
	if !exists || !valid || foundValue || boolValue(result["retryable"], false) || boolValue(result["source_unavailable"], false) {
		return nil, false
	}
	return result, true
}

// collectSessionRunnerNegativeStructuredReferences accepts typed identifier
// fields only. Error text and prose are intentionally ignored so an upstream
// message cannot smuggle unrelated identifiers into evidence authority.
func collectSessionRunnerNegativeStructuredReferences(target map[string]struct{}, value any, depth int, visited *int) {
	if depth > 8 || *visited >= 512 {
		return
	}
	(*visited)++
	switch typed := value.(type) {
	case map[string]any:
		recordNamespace := sessionRunnerAccessionNamespace(firstNonEmpty(
			stringValue(typed["database"]), stringValue(typed["db"]), stringValue(typed["namespace"]),
		))
		for key, child := range typed {
			normalizedKey := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(key)))
			switch normalizedKey {
			case "doi", "dois", "digitalobjectidentifier":
				addStructuredSessionRunnerReferences(target, "doi", "", child)
			case "pmid", "pmids", "pubmedid", "pubmedids":
				addStructuredSessionRunnerReferences(target, "pmid", "", child)
			case "nct", "nctid", "nctids", "trialid", "trialids", "clinicaltrialid", "clinicaltrialids":
				addStructuredSessionRunnerReferences(target, "nct", "", child)
			case "pdb", "pdbid", "pdbids":
				addStructuredSessionRunnerReferences(target, "accession", "pdb", child)
			case "uniprot", "uniprotid", "uniprotids", "uniprotaccession", "uniprotaccessions":
				addStructuredSessionRunnerReferences(target, "accession", "uniprot", child)
			case "chembl", "chemblid", "chemblids", "moleculechemblid", "activechemblid", "parentchemblid", "targetchemblid":
				addStructuredSessionRunnerReferences(target, "accession", "chembl", child)
			case "ensembl", "ensemblid", "ensemblids":
				addStructuredSessionRunnerReferences(target, "accession", "ensembl", child)
			case "refseq", "refseqid", "refseqids":
				addStructuredSessionRunnerReferences(target, "accession", "refseq", child)
			case "geo", "geoid", "geoids":
				addStructuredSessionRunnerReferences(target, "accession", "geo", child)
			case "sra", "sraid", "sraids":
				addStructuredSessionRunnerReferences(target, "accession", "sra", child)
			case "pride", "prideid", "prideids":
				addStructuredSessionRunnerReferences(target, "accession", "pride", child)
			case "clinvar", "clinvarid", "clinvarids", "rcv", "rcvid", "rcvids", "vcv", "vcvid", "vcvids":
				addStructuredSessionRunnerReferences(target, "accession", "clinvar", child)
			case "dbsnp", "dbsnpid", "dbsnpids", "rsid", "rsids":
				addStructuredSessionRunnerReferences(target, "accession", "dbsnp", child)
			case "accession", "accessions":
				addStructuredSessionRunnerReferences(target, "accession", recordNamespace, child)
			case "id", "identifier", "ids", "identifiers":
				if recordNamespace != "" {
					addStructuredSessionRunnerReferences(target, "accession", recordNamespace, child)
				}
			}
			if normalizedKey != "error" && normalizedKey != "message" && normalizedKey != "text" && normalizedKey != "content" {
				collectSessionRunnerNegativeStructuredReferences(target, child, depth+1, visited)
			}
		}
	case []any:
		for _, child := range typed {
			collectSessionRunnerNegativeStructuredReferences(target, child, depth+1, visited)
		}
	}
}

func strictSessionRunnerWebFetchEvidenceEndpoint(arguments json.RawMessage, envelope any) (string, *url.URL, bool) {
	var input map[string]any
	if json.Unmarshal(arguments, &input) != nil {
		return "", nil, false
	}
	rawURL := strings.TrimSpace(stringValue(input["url"]))
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, false
	}
	host := normalizedSessionRunnerEvidenceHost(parsed)
	path := strings.ToLower(strings.TrimRight(parsed.Path, "/"))
	endpoint := ""
	switch {
	case host == "eutils.ncbi.nlm.nih.gov" && strings.EqualFold(strings.TrimSpace(parsed.Query().Get("db")), "pubmed"):
		endpoint = "pubmed"
	case host == "www.ebi.ac.uk" && path == "/europepmc/webservices/rest/search":
		endpoint = "europepmc"
	case host == "data.rcsb.org" && strings.HasPrefix(path, "/rest/v1/core/entry/"):
		endpoint = "pdb"
	case host == "data.rcsb.org" && strings.HasPrefix(path, "/rest/v1/core/polymer_entity/"):
		endpoint = "pdb_polymer_entity"
	case host == "api.crossref.org" && strings.HasPrefix(path, "/works/"):
		endpoint = "crossref"
	case (host == "clinicaltrials.gov" || host == "www.clinicaltrials.gov") && strings.HasPrefix(path, "/api/v2/studies/"):
		endpoint = "clinicaltrials"
	case host == "www.ebi.ac.uk" && path == "/chembl/api/data/mechanism.json":
		endpoint = "chembl_mechanism"
	default:
		return "", nil, false
	}
	_, requestURL, ok := canonicalSessionRunnerURL(rawURL)
	if !ok || !strings.EqualFold(requestURL.Scheme, "https") {
		return endpoint, nil, true
	}
	if endpoint == "crossref" {
		if requestURL.Fragment != "" || requestURL.Port() != "" || !sessionRunnerCrossrefQueryAllowed(requestURL.Query()) {
			return endpoint, nil, true
		}
		if _, ok := sessionRunnerCrossrefRequestDOI(requestURL); !ok || !sessionRunnerWebFetchExactResultURLMatches(requestURL, envelope) {
			return endpoint, nil, true
		}
	} else if endpoint == "clinicaltrials" {
		if requestURL.Fragment != "" || requestURL.Port() != "" || len(requestURL.Query()) != 0 {
			return endpoint, nil, true
		}
		if _, ok := sessionRunnerClinicalTrialsRequestNCT(requestURL); !ok || !sessionRunnerWebFetchResultURLMatches(requestURL, envelope) {
			return endpoint, nil, true
		}
	} else if endpoint == "chembl_mechanism" {
		if _, ok := sessionRunnerChEMBLMechanismRequest(requestURL); !ok ||
			!sessionRunnerWebFetchResultURLMatches(requestURL, envelope) {
			return endpoint, nil, true
		}
	} else if !sessionRunnerWebFetchResultURLMatches(requestURL, envelope) {
		return endpoint, nil, true
	}
	return endpoint, requestURL, true
}

func sessionRunnerChEMBLMechanismRequest(parsed *url.URL) (string, bool) {
	if parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Port() != "" || parsed.Fragment != "" ||
		normalizedSessionRunnerEvidenceHost(parsed) != "www.ebi.ac.uk" ||
		!strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/chembl/api/data/mechanism.json") {
		return "", false
	}
	query := parsed.Query()
	for key, values := range query {
		if len(values) != 1 {
			return "", false
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "molecule_chembl_id":
		case "limit":
			limit, err := strconv.Atoi(strings.TrimSpace(values[0]))
			if err != nil || limit <= 0 || limit > 100 {
				return "", false
			}
		case "offset":
			offset, err := strconv.Atoi(strings.TrimSpace(values[0]))
			if err != nil || offset < 0 || offset > 1000000 {
				return "", false
			}
		default:
			return "", false
		}
	}
	identifier := strings.ToUpper(strings.TrimSpace(query.Get("molecule_chembl_id")))
	if !validSessionRunnerAccession("chembl", identifier) {
		return "", false
	}
	return identifier, true
}

func addVerifiedSessionRunnerChEMBLMechanismReferences(target map[string]struct{}, envelope any, parsed *url.URL) {
	requested, ok := sessionRunnerChEMBLMechanismRequest(parsed)
	statusCode := sessionRunnerWebFetchStatusCode(envelope)
	if !ok || statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return
	}
	result, ok := sessionRunnerWebFetchResultMap(envelope)
	if !ok {
		return
	}
	body := firstNonEmpty(stringValue(result["body"]), stringValue(result["result"]), stringValue(result["content"]))
	var response struct {
		Mechanisms []struct {
			MoleculeChEMBLID       string `json:"molecule_chembl_id"`
			ParentMoleculeChEMBLID string `json:"parent_molecule_chembl_id"`
			TargetChEMBLID         string `json:"target_chembl_id"`
			MechanismRefs          []struct {
				ID   string `json:"ref_id"`
				Type string `json:"ref_type"`
				URL  string `json:"ref_url"`
			} `json:"mechanism_refs"`
			VariantSequence *struct {
				Accession string `json:"accession"`
				TaxID     int64  `json:"tax_id"`
			} `json:"variant_sequence"`
		} `json:"mechanisms"`
	}
	if json.Unmarshal([]byte(body), &response) != nil {
		return
	}
	for _, mechanism := range response.Mechanisms {
		if !strings.EqualFold(strings.TrimSpace(mechanism.MoleculeChEMBLID), requested) {
			continue
		}
		addSessionRunnerURLReference(target, parsed.String())
		addSessionRunnerReference(target, "accession", "chembl", mechanism.MoleculeChEMBLID)
		addSessionRunnerReference(target, "accession", "chembl", mechanism.ParentMoleculeChEMBLID)
		addSessionRunnerReference(target, "accession", "chembl", mechanism.TargetChEMBLID)
		if mechanism.VariantSequence != nil && mechanism.VariantSequence.TaxID > 0 {
			addSessionRunnerReference(target, "accession", "uniprot", mechanism.VariantSequence.Accession)
		}
		for _, reference := range mechanism.MechanismRefs {
			if strings.EqualFold(strings.TrimSpace(reference.Type), "PubMed") {
				addSessionRunnerReference(target, "pmid", "", reference.ID)
			}
			addSessionRunnerURLReference(target, reference.URL)
		}
	}
}

func sessionRunnerWebFetchExactResultURLMatches(requestURL *url.URL, envelope any) bool {
	result, ok := sessionRunnerWebFetchResultMap(envelope)
	if !ok {
		return false
	}
	resultURLValue := strings.TrimSpace(stringValue(result["url"]))
	if resultURLValue == "" {
		return false
	}
	_, resultURL, resultOK := canonicalSessionRunnerURL(resultURLValue)
	if !resultOK || resultURL.Fragment != "" || resultURL.Port() != "" || !sessionRunnerCrossrefQueryAllowed(resultURL.Query()) {
		return false
	}
	requestCopy, resultCopy := *requestURL, *resultURL
	requestCopy.RawQuery, requestCopy.ForceQuery = "", false
	resultCopy.RawQuery, resultCopy.ForceQuery = "", false
	return normalizedSessionRunnerEvidenceURL(&requestCopy) == normalizedSessionRunnerEvidenceURL(&resultCopy)
}

func sessionRunnerCrossrefQueryAllowed(query url.Values) bool {
	for key, values := range query {
		if !strings.EqualFold(strings.TrimSpace(key), "mailto") || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return false
		}
	}
	return true
}

func sessionRunnerWebFetchResultMap(envelope any) (map[string]any, bool) {
	result, ok := envelope.(map[string]any)
	if !ok {
		return nil, false
	}
	if nested, nestedOK := result["result"].(map[string]any); nestedOK {
		result = nested
	}
	return result, true
}

func sessionRunnerWebFetchStatusCode(envelope any) int {
	result, ok := sessionRunnerWebFetchResultMap(envelope)
	if !ok {
		return 0
	}
	statusCode := int(numberValue(result["statusCode"]))
	if statusCode == 0 {
		statusCode = int(numberValue(result["code"]))
	}
	return statusCode
}

func sessionRunnerCrossrefRequestDOI(parsed *url.URL) (string, bool) {
	if parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || normalizedSessionRunnerEvidenceHost(parsed) != "api.crossref.org" {
		return "", false
	}
	escapedPath := parsed.EscapedPath()
	const prefix = "/works/"
	if len(escapedPath) <= len(prefix) || !strings.EqualFold(escapedPath[:len(prefix)], prefix) {
		return "", false
	}
	identifier, err := url.PathUnescape(escapedPath[len(prefix):])
	if err != nil {
		return "", false
	}
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	return identifier, sessionRunnerDOIExactPattern.MatchString(identifier)
}

func sessionRunnerPDBRequestID(parsed *url.URL) (string, bool) {
	if parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Port() != "" || len(parsed.Query()) != 0 {
		return "", false
	}
	host := normalizedSessionRunnerEvidenceHost(parsed)
	segments := strings.FieldsFunc(parsed.EscapedPath(), func(r rune) bool { return r == '/' })
	identifier := ""
	switch {
	case host == "data.rcsb.org" && len(segments) == 5 && strings.EqualFold(segments[0], "rest") &&
		segments[1] == "v1" && strings.EqualFold(segments[2], "core") && strings.EqualFold(segments[3], "entry"):
		identifier = segments[4]
	case (host == "rcsb.org" || host == "www.rcsb.org") && len(segments) == 2 && strings.EqualFold(segments[0], "structure"):
		identifier = segments[1]
	case host == "files.rcsb.org" && len(segments) == 2 && strings.EqualFold(segments[0], "download"):
		filename, err := url.PathUnescape(segments[1])
		if err != nil {
			return "", false
		}
		filename = strings.TrimSuffix(strings.TrimSuffix(filename, ".gz"), ".cif")
		filename = strings.TrimSuffix(strings.TrimSuffix(filename, ".bcif"), ".pdb")
		identifier = filename
	default:
		return "", false
	}
	identifier, err := url.PathUnescape(identifier)
	if err != nil || !sessionRunnerPDBExactPattern.MatchString(identifier) {
		return "", false
	}
	return strings.ToUpper(identifier), true
}

func sessionRunnerPDBPolymerEntityRequest(parsed *url.URL) (string, string, bool) {
	if parsed == nil || !strings.EqualFold(parsed.Scheme, "https") ||
		normalizedSessionRunnerEvidenceHost(parsed) != "data.rcsb.org" ||
		parsed.Port() != "" || parsed.Fragment != "" || len(parsed.Query()) != 0 {
		return "", "", false
	}
	segments := strings.FieldsFunc(parsed.EscapedPath(), func(r rune) bool { return r == '/' })
	if len(segments) != 6 || !strings.EqualFold(segments[0], "rest") || segments[1] != "v1" ||
		!strings.EqualFold(segments[2], "core") || !strings.EqualFold(segments[3], "polymer_entity") {
		return "", "", false
	}
	pdbID := strings.ToUpper(strings.TrimSpace(segments[4]))
	entityID := strings.TrimSpace(segments[5])
	entityNumber, err := strconv.Atoi(entityID)
	if !sessionRunnerPDBExactPattern.MatchString(pdbID) || err != nil || entityNumber <= 0 || strconv.Itoa(entityNumber) != entityID {
		return "", "", false
	}
	return pdbID, entityID, true
}

func sessionRunnerPubMedRequestIdentifiers(parsed *url.URL) map[string]struct{} {
	if parsed == nil || !strings.EqualFold(parsed.Scheme, "https") ||
		normalizedSessionRunnerEvidenceHost(parsed) != "eutils.ncbi.nlm.nih.gov" ||
		!strings.EqualFold(strings.TrimSpace(parsed.Query().Get("db")), "pubmed") {
		return nil
	}
	path := strings.ToLower(strings.TrimSpace(parsed.Path))
	if !strings.HasSuffix(path, "/efetch.fcgi") && !strings.HasSuffix(path, "/esummary.fcgi") {
		return nil
	}
	requested := map[string]struct{}{}
	for _, identifier := range strings.FieldsFunc(parsed.Query().Get("id"), func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		identifier = strings.TrimSpace(identifier)
		if sessionRunnerPMIDExactPattern.MatchString(identifier) {
			requested[identifier] = struct{}{}
		}
	}
	return requested
}

func sessionRunnerClinicalTrialsRequestNCT(parsed *url.URL) (string, bool) {
	if parsed == nil || !strings.EqualFold(parsed.Scheme, "https") {
		return "", false
	}
	host := normalizedSessionRunnerEvidenceHost(parsed)
	if host != "clinicaltrials.gov" && host != "www.clinicaltrials.gov" {
		return "", false
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 4 || !strings.EqualFold(segments[0], "api") ||
		!strings.EqualFold(segments[1], "v2") || !strings.EqualFold(segments[2], "studies") {
		return "", false
	}
	identifier, err := url.PathUnescape(segments[3])
	if err != nil || !sessionRunnerNCTExactPattern.MatchString(identifier) {
		return "", false
	}
	return strings.ToUpper(identifier), true
}

func addVerifiedSessionRunnerClinicalTrialsReferences(target map[string]struct{}, envelope any, parsed *url.URL) {
	requested, ok := sessionRunnerClinicalTrialsRequestNCT(parsed)
	if !ok || sessionRunnerWebFetchStatusCode(envelope) < http.StatusOK || sessionRunnerWebFetchStatusCode(envelope) >= http.StatusMultipleChoices {
		return
	}
	result, ok := sessionRunnerWebFetchResultMap(envelope)
	if !ok {
		return
	}
	body := firstNonEmpty(stringValue(result["body"]), stringValue(result["result"]), stringValue(result["content"]))
	var record map[string]any
	if json.Unmarshal([]byte(body), &record) != nil {
		return
	}
	protocol, _ := record["protocolSection"].(map[string]any)
	identification, _ := protocol["identificationModule"].(map[string]any)
	identifier := strings.ToUpper(strings.TrimSpace(stringValue(identification["nctId"])))
	if identifier == requested {
		addSessionRunnerReference(target, "nct", "", requested)
	}
}

func sessionRunnerWebFetchResultURLMatches(requestURL *url.URL, envelope any) bool {
	result, ok := sessionRunnerWebFetchResultMap(envelope)
	if !ok {
		return false
	}
	resultURLValue := strings.TrimSpace(stringValue(result["url"]))
	if resultURLValue == "" {
		return false
	}
	_, resultURL, resultOK := canonicalSessionRunnerURL(resultURLValue)
	return resultOK && normalizedSessionRunnerEvidenceURL(requestURL) == normalizedSessionRunnerEvidenceURL(resultURL)
}

func normalizedSessionRunnerEvidenceHost(value *url.URL) string {
	if value == nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(value.Hostname()), ".")
}

func normalizedSessionRunnerEvidenceURL(value *url.URL) string {
	if value == nil {
		return ""
	}
	normalized := *value
	host := normalizedSessionRunnerEvidenceHost(value)
	if port := value.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	normalized.Scheme = strings.ToLower(normalized.Scheme)
	normalized.Host = host
	return normalized.String()
}

func addVerifiedSessionRunnerPDBReferences(target map[string]struct{}, envelope any, parsed *url.URL) {
	result, ok := envelope.(map[string]any)
	if !ok {
		return
	}
	if nested, nestedOK := result["result"].(map[string]any); nestedOK {
		result = nested
	}
	requested, found := sessionRunnerPDBRequestID(parsed)
	if !found {
		return
	}
	body := firstNonEmpty(stringValue(result["body"]), stringValue(result["result"]), stringValue(result["content"]))
	var response struct {
		Entry struct {
			ID string `json:"id"`
		} `json:"entry"`
		Citation []struct {
			ID        string `json:"id"`
			IsPrimary string `json:"rcsb_is_primary"`
			DOI       string `json:"pdbx_database_id_DOI"`
		} `json:"citation"`
	}
	if json.Unmarshal([]byte(body), &response) != nil || !strings.EqualFold(strings.TrimSpace(response.Entry.ID), requested) {
		return
	}
	addSessionRunnerReference(target, "accession", "pdb", requested)
	for _, citation := range response.Citation {
		if !strings.EqualFold(strings.TrimSpace(citation.IsPrimary), "Y") &&
			!strings.EqualFold(strings.TrimSpace(citation.ID), "primary") {
			continue
		}
		addSessionRunnerReference(target, "doi", "", citation.DOI)
	}
}

func addVerifiedSessionRunnerPDBPolymerEntityReferences(target map[string]struct{}, envelope any, parsed *url.URL) {
	result, ok := sessionRunnerWebFetchResultMap(envelope)
	if !ok {
		return
	}
	requestedPDB, requestedEntity, found := sessionRunnerPDBPolymerEntityRequest(parsed)
	if !found {
		return
	}
	body := firstNonEmpty(stringValue(result["body"]), stringValue(result["result"]), stringValue(result["content"]))
	var response struct {
		Identifiers struct {
			EntryID    string   `json:"entry_id"`
			EntityID   string   `json:"entity_id"`
			RCSBID     string   `json:"rcsb_id"`
			UniProtIDs []string `json:"uniprot_ids"`
			References []struct {
				Accession  string `json:"database_accession"`
				Database   string `json:"database_name"`
				Provenance string `json:"provenance_source"`
			} `json:"reference_sequence_identifiers"`
		} `json:"rcsb_polymer_entity_container_identifiers"`
	}
	if json.Unmarshal([]byte(body), &response) != nil ||
		!strings.EqualFold(strings.TrimSpace(response.Identifiers.EntryID), requestedPDB) ||
		strings.TrimSpace(response.Identifiers.EntityID) != requestedEntity ||
		!strings.EqualFold(strings.TrimSpace(response.Identifiers.RCSBID), requestedPDB+"_"+requestedEntity) {
		return
	}
	sifts := make(map[string]struct{}, len(response.Identifiers.References))
	for _, reference := range response.Identifiers.References {
		if !strings.EqualFold(strings.TrimSpace(reference.Database), "UniProt") ||
			!strings.EqualFold(strings.TrimSpace(reference.Provenance), "SIFTS") {
			continue
		}
		accession := strings.ToUpper(strings.TrimSpace(reference.Accession))
		if sessionRunnerUniProtExactPattern.MatchString(accession) {
			sifts[accession] = struct{}{}
		}
	}
	for _, raw := range response.Identifiers.UniProtIDs {
		accession := strings.ToUpper(strings.TrimSpace(raw))
		if _, verified := sifts[accession]; verified {
			addSessionRunnerReference(target, "accession", "uniprot", accession)
		}
	}
}

func sessionRunnerEvidenceTextAllowed(toolName string, envelope any) bool {
	switch normalizeAgentToolName(toolName) {
	case "webfetch":
		result, ok := envelope.(map[string]any)
		if !ok {
			return false
		}
		if nested, nestedOK := result["result"].(map[string]any); nestedOK {
			result = nested
		}
		statusCode := int(numberValue(result["statusCode"]))
		if statusCode == 0 {
			statusCode = int(numberValue(result["code"]))
		}
		if statusCode < 200 || statusCode >= 300 || boolValue(result["partial"], false) ||
			boolValue(result["truncated"], false) || boolValue(result["binary"], false) ||
			boolValue(result["sourceUnavailable"], false) {
			return false
		}
		body := firstNonEmpty(stringValue(result["body"]), stringValue(result["content"]), stringValue(result["text"]))
		return len([]rune(strings.TrimSpace(body))) >= webResearchMinimumSubstantiveCharacters
	case "fetcharticlefulltext":
		result, ok := envelope.(map[string]any)
		if !ok {
			return false
		}
		if nested, nestedOK := result["result"].(map[string]any); nestedOK {
			result = nested
		}
		return !rejectedSessionRunnerEvidenceRecord(result) &&
			runnerEvidenceResultHasSubstantiveField(
				result, runnerPublicationSubstantiveFields, 0, new(int),
			)
	default:
		return false
	}
}

func sessionRunnerMCPDiscoveryTool(name string) bool {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(name)), "__", 3)
	if len(parts) != 3 || parts[0] != "mcp" {
		return false
	}
	method := normalizeAgentToolName(parts[2])
	for _, marker := range []string{"search", "list", "find", "query", "discover", "collect"} {
		if strings.Contains(method, marker) {
			return true
		}
	}
	return false
}

func collectVerifiedSessionRunnerReferences(
	target map[string]struct{},
	value any,
	depth int,
	visited *int,
	verifiedText bool,
) {
	if depth > 16 || *visited >= 4096 {
		return
	}
	(*visited)++
	switch typed := value.(type) {
	case map[string]any:
		if agentruntime.ClassifyToolResult(typed).Failed() || rejectedSessionRunnerEvidenceRecord(typed) {
			return
		}
		status := strings.ToLower(strings.TrimSpace(stringValue(typed["status"])))
		mapVerified := verifiedText || status == "fetched" || status == "verified" || status == "available" || status == "success"
		recordNamespace := sessionRunnerAccessionNamespace(firstNonEmpty(
			stringValue(typed["database"]), stringValue(typed["db"]), stringValue(typed["namespace"]),
		))
		for key, child := range typed {
			normalizedKey := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(key)))
			switch normalizedKey {
			case "doi", "dois", "digitalobjectidentifier":
				addStructuredSessionRunnerReferences(target, "doi", "", child)
			case "pmid", "pmids", "pubmedid", "pubmedids":
				addStructuredSessionRunnerReferences(target, "pmid", "", child)
			case "nct", "nctid", "nctids", "trialid", "trialids", "clinicaltrialid", "clinicaltrialids":
				addStructuredSessionRunnerReferences(target, "nct", "", child)
			case "url", "urls", "link", "links", "href", "sourceurl", "sourceurls":
				addStructuredSessionRunnerReferences(target, "url", "", child)
			case "pdb", "pdbid", "pdbids":
				addStructuredSessionRunnerReferences(target, "accession", "pdb", child)
			case "uniprot", "uniprotid", "uniprotids", "uniprotaccession", "uniprotaccessions":
				addStructuredSessionRunnerReferences(target, "accession", "uniprot", child)
			case "chembl", "chemblid", "chemblids", "moleculechemblid", "activechemblid", "parentchemblid", "targetchemblid":
				addStructuredSessionRunnerReferences(target, "accession", "chembl", child)
			case "ensembl", "ensemblid", "ensemblids":
				addStructuredSessionRunnerReferences(target, "accession", "ensembl", child)
			case "refseq", "refseqid", "refseqids":
				addStructuredSessionRunnerReferences(target, "accession", "refseq", child)
			case "geo", "geoid", "geoids":
				addStructuredSessionRunnerReferences(target, "accession", "geo", child)
			case "sra", "sraid", "sraids":
				addStructuredSessionRunnerReferences(target, "accession", "sra", child)
			case "pride", "prideid", "prideids":
				addStructuredSessionRunnerReferences(target, "accession", "pride", child)
			case "clinvar", "clinvarid", "clinvarids", "rcv", "rcvid", "rcvids", "vcv", "vcvid", "vcvids":
				addStructuredSessionRunnerReferences(target, "accession", "clinvar", child)
			case "dbsnp", "dbsnpid", "dbsnpids", "rsid", "rsids":
				addStructuredSessionRunnerReferences(target, "accession", "dbsnp", child)
			case "accession", "accessions":
				addStructuredSessionRunnerReferences(target, "accession", recordNamespace, child)
			case "id", "identifier", "ids", "identifiers":
				// Nested cross-reference records (e.g. ClinVar xrefs) use
				// generic "db" + "id" keys. Map them through the same-layer
				// database namespace so UniProt/PDB/ChEMBL identifiers from
				// evidence tools are recognized instead of rejected as
				// unsupported citations.
				if recordNamespace != "" {
					addStructuredSessionRunnerReferences(target, "accession", recordNamespace, child)
				} else if text, ok := child.(string); ok {
					addSessionRunnerReferences(target, text)
				}
			case "content", "text", "abstract", "fulltext", "markdown", "body", "result":
				if mapVerified {
					if text, ok := child.(string); ok {
						addSessionRunnerReferences(target, text)
					}
				}
			case "preview":
				// A large tool result is externalized to an artifact and the
				// verified evidence message carries only its bounded preview.
				// The preview is the same trusted JSON the runner materialized,
				// so extract every supported identifier from it exactly like the
				// inline result path does. Without this, DOIs/PMIDs that were
				// genuinely returned by an evidence tool are treated as
				// unsupported citations and the completion is rejected.
				if text, ok := child.(string); ok {
					// The preview is a JSON-encoded representation of the same
					// trusted tool result. Decode it first so escaped identifiers
					// inside the nested payload are collected structurally, then
					// fall back to plain-text scanning for non-JSON previews.
					var decoded any
					if json.Unmarshal([]byte(text), &decoded) == nil {
						collectVerifiedSessionRunnerReferences(target, decoded, depth+1, visited, verifiedText)
					} else {
						addSessionRunnerReferences(target, text)
					}
				}
			}
			collectVerifiedSessionRunnerReferences(target, child, depth+1, visited, mapVerified)
		}
	case []any:
		for _, child := range typed {
			collectVerifiedSessionRunnerReferences(target, child, depth+1, visited, verifiedText)
		}
	}
}

func rejectedSessionRunnerEvidenceRecord(record map[string]any) bool {
	if boolValue(record["sourceUnavailable"], false) {
		return true
	}
	if available, present := record["available"]; present && !boolValue(available, false) {
		return true
	}
	status := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(stringValue(record["status"]))))
	switch status {
	case "notfound", "notavailable", "sourceunavailable", "temporarilyunavailable", "upstreamunavailable",
		"missing", "invalid", "rejected", "denied", "cancelled", "canceled", "timeout", "timedout", "expired":
		return true
	default:
		return false
	}
}

func sessionRunnerAccessionNamespace(value string) string {
	normalized := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case "pdb", "rcsbpdb":
		return "pdb"
	case "uniprot", "uniprotkb":
		return "uniprot"
	case "chembl":
		return "chembl"
	case "ensembl":
		return "ensembl"
	case "clinvar":
		return "clinvar"
	case "dbsnp":
		return "dbsnp"
	case "refseq":
		return "refseq"
	case "geo":
		return "geo"
	case "sra":
		return "sra"
	case "pride":
		return "pride"
	default:
		return ""
	}
}

func addStructuredSessionRunnerReferences(target map[string]struct{}, kind, namespace string, value any) {
	switch typed := value.(type) {
	case string:
		switch kind {
		case "url":
			addSessionRunnerReferences(target, typed)
		case "accession":
			if namespace == "" {
				if !addExactSessionRunnerAccessionReference(target, typed) {
					addSessionRunnerReferences(target, typed)
				}
			} else {
				addSessionRunnerReference(target, kind, namespace, typed)
			}
		default:
			addSessionRunnerReference(target, kind, namespace, typed)
		}
	case float64:
		if kind == "pmid" && typed >= 1 && typed <= 999999999 && typed == float64(int64(typed)) {
			addSessionRunnerReference(target, kind, namespace, strconv.FormatInt(int64(typed), 10))
		}
	case []any:
		for _, item := range typed {
			addStructuredSessionRunnerReferences(target, kind, namespace, item)
		}
	}
}

// addExactSessionRunnerAccessionReference handles generic structured records
// such as {"accession":"Q00987"}. The record is already inside a verified
// tool result, but without an adjacent database label the prose scanner cannot
// classify it. Infer only unambiguous, whole-string scientific identifier
// grammars; arbitrary strings continue through the conservative prose path.
func addExactSessionRunnerAccessionReference(target map[string]struct{}, value string) bool {
	identifier := strings.TrimSpace(value)
	if identifier == "" {
		return false
	}
	if sessionRunnerPDBExactPattern.MatchString(identifier) {
		addSessionRunnerReference(target, "accession", "pdb", identifier)
		return true
	}
	if sessionRunnerUniProtExactPattern.MatchString(identifier) {
		addSessionRunnerReference(target, "accession", "uniprot", identifier)
		return true
	}
	for _, definition := range sessionRunnerAccessionPatterns {
		match := definition.pattern.FindString(identifier)
		if strings.EqualFold(match, identifier) {
			addSessionRunnerReference(target, "accession", definition.namespace, identifier)
			return true
		}
	}
	return false
}

const sessionRunnerArtifactReferenceMarker = "{{artifact:"
