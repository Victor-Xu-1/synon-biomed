package patentsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"synon-go/internal/discoveryquery"
	"synon-go/internal/tools/webfetch"
	"synon-go/internal/tools/websearch"
)

const (
	OperationSearch          = "search"
	OperationLookup          = "lookup"
	OperationResolveDownload = "resolve_download"

	DownloadResolved           = "resolved"
	DownloadUnavailable        = "unavailable"
	DownloadUserActionRequired = "user_action_required"

	defaultMaxResults    = 50
	maximumMaxResults    = 200
	patentPageMaxBytes   = webfetch.MaxResponseLimit
	googleSearchMaxBytes = webfetch.MaxResponseLimit
)

var (
	publicationNumberPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{4,38}$`)
	tagPattern               = regexp.MustCompile(`(?is)<(?:meta|a)\b[^>]*>`)
	htmlTagPattern           = regexp.MustCompile(`(?is)<[^>]*>`)
	attributePattern         = regexp.MustCompile(`(?i)([a-z_:][-a-z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>` + "`" + `]+))`)
)

type SearchFunc func(context.Context, websearch.Input, websearch.Options) (websearch.Output, error)
type FetchHTMLFunc func(context.Context, string) (string, error)
type FetchBytesFunc func(context.Context, string) ([]byte, error)

type Options struct {
	Search          SearchFunc
	SearchOptions   websearch.Options
	FetchOptions    webfetch.Options
	FetchHTML       FetchHTMLFunc
	FetchGoogleJSON FetchBytesFunc
}

type Client struct {
	search                    SearchFunc
	searchOptions             websearch.Options
	fetchHTML                 FetchHTMLFunc
	fetchGoogleJSON           FetchBytesFunc
	useStructuredGoogleSearch bool
}

type Input struct {
	Operation         string   `json:"operation"`
	Query             string   `json:"query,omitempty"`
	QueryVariants     []string `json:"queryVariants,omitempty"`
	PublicationNumber string   `json:"publicationNumber,omitempty"`
	Sources           []string `json:"sources,omitempty"`
	MaxResults        int      `json:"maxResults,omitempty"`
	FocusTerms        []string `json:"focusTerms,omitempty"`
	MaxSectionChars   int      `json:"maxSectionChars,omitempty"`
}

type Claim struct {
	Number      string `json:"number"`
	Text        string `json:"text"`
	Independent bool   `json:"independent"`
}

type Passage struct {
	Locator string `json:"locator"`
	Text    string `json:"text"`
}

type Record struct {
	Source              string    `json:"source"`
	SourceLabel         string    `json:"sourceLabel"`
	Title               string    `json:"title"`
	URL                 string    `json:"url"`
	Snippet             string    `json:"snippet,omitempty"`
	PublicationNumber   string    `json:"publicationNumber,omitempty"`
	EvidenceState       string    `json:"evidenceState"`
	Abstract            string    `json:"abstract,omitempty"`
	Inventors           []string  `json:"inventors,omitempty"`
	Assignees           []string  `json:"assignees,omitempty"`
	PriorityDate        string    `json:"priorityDate,omitempty"`
	PublicationDate     string    `json:"publicationDate,omitempty"`
	LegalStatus         string    `json:"legalStatus,omitempty"`
	Claims              []Claim   `json:"claims,omitempty"`
	DescriptionExcerpts []Passage `json:"descriptionExcerpts,omitempty"`
	ExampleExcerpts     []Passage `json:"exampleExcerpts,omitempty"`
	SectionsRead        []string  `json:"sectionsRead,omitempty"`
	MatchedFocusTerms   []string  `json:"matchedFocusTerms,omitempty"`
	FocusRelevant       bool      `json:"focusRelevant"`
	RecordDepth         string    `json:"recordDepth,omitempty"`
}

type SourceStatus struct {
	Source                 string `json:"source"`
	Label                  string `json:"label"`
	SearchURL              string `json:"searchUrl"`
	AccessMode             string `json:"accessMode"`
	DownloadMode           string `json:"downloadMode"`
	RequiresAuthentication bool   `json:"requiresAuthentication"`
	MachineReadable        bool   `json:"machineReadable"`
	Status                 string `json:"status"`
	Note                   string `json:"note,omitempty"`
}

type DownloadResolution struct {
	Source                 string `json:"source"`
	SourceLabel            string `json:"sourceLabel"`
	State                  string `json:"state"`
	PageURL                string `json:"pageUrl"`
	DownloadURL            string `json:"downloadUrl,omitempty"`
	RequiresAuthentication bool   `json:"requiresAuthentication"`
	Instructions           string `json:"instructions"`
}

type Output struct {
	Operation         string               `json:"operation"`
	Query             string               `json:"query,omitempty"`
	PublicationNumber string               `json:"publicationNumber,omitempty"`
	Records           []Record             `json:"records"`
	SourceStatuses    []SourceStatus       `json:"sourceStatuses"`
	Downloads         []DownloadResolution `json:"downloads,omitempty"`
	Warnings          []string             `json:"warnings,omitempty"`
	CandidateLimit    int                  `json:"candidateLimit,omitempty"`
	LimitReached      bool                 `json:"limitReached,omitempty"`
	Retrieval         map[string]any       `json:"retrieval,omitempty"`
	EvidenceDepth     string               `json:"evidenceDepth,omitempty"`
	NextAction        string               `json:"nextAction,omitempty"`
}

type sourceSpec struct {
	id                     string
	label                  string
	domains                []string
	accessMode             string
	downloadMode           string
	requiresAuthentication bool
	machineReadable        bool
	note                   string
	searchURL              func(string) string
	lookupURL              func(string) string
}

var sourceSpecs = []sourceSpec{
	{
		id: "wipo", label: "WIPO PATENTSCOPE",
		domains:         []string{"patentscope.wipo.int"},
		accessMode:      "Public PATENTSCOPE search portal",
		downloadMode:    "Open the official record and use its document PDF or ZIP action",
		machineReadable: false,
		note:            "WIPO PDFs are the legally reliable publication view; OCR text is discovery text.",
		searchURL: func(query string) string {
			return "https://patentscope.wipo.int/search/en/result.jsf?query=" + url.QueryEscape(query)
		},
		lookupURL: func(publicationNumber string) string {
			return "https://patentscope.wipo.int/search/en/result.jsf?query=" + url.QueryEscape("FP:("+publicationNumber+")")
		},
	},
	{
		id: "google_patents", label: "Google Patents",
		domains:         []string{"patents.google.com"},
		accessMode:      "Public web interface",
		downloadMode:    "Resolve the publication page citation PDF when Google exposes one",
		machineReadable: true,
		note:            "Google Patents is a discovery interface, not an official patent-office API.",
		searchURL: func(query string) string {
			return "https://patents.google.com/?q=" + url.QueryEscape(query)
		},
		lookupURL: googlePatentURL,
	},
	{
		id: "epo", label: "European Patent Office",
		domains:         []string{"worldwide.espacenet.com", "publication-bdds.apps.epo.org", "epo.org"},
		accessMode:      "Public Espacenet portal; OPS requires registered API credentials",
		downloadMode:    "Use the public document link interactively or configured EPO OPS credentials",
		machineReadable: true,
		note:            "The tool does not embed, request, or bypass EPO OPS credentials.",
		searchURL: func(query string) string {
			return "https://worldwide.espacenet.com/patent/search?q=" + url.QueryEscape(query)
		},
		lookupURL: func(publicationNumber string) string {
			return "https://worldwide.espacenet.com/patent/search?q=" + url.QueryEscape("pn="+publicationNumber)
		},
	},
	{
		id: "cnipa", label: "中国国家知识产权局",
		domains:                []string{"pss-system.cponline.cnipa.gov.cn", "cnipa.gov.cn"},
		accessMode:             "Official patent search and analysis portal",
		downloadMode:           "Sign in to the official portal and download the selected publication",
		requiresAuthentication: true,
		machineReadable:        false,
		note:                   "Free registration may be required. The tool never bypasses login or CAPTCHA.",
		searchURL: func(string) string {
			return "https://pss-system.cponline.cnipa.gov.cn/conventionalSearch"
		},
		lookupURL: func(string) string {
			return "https://pss-system.cponline.cnipa.gov.cn/conventionalSearch"
		},
	},
}

func NewClient(options Options) *Client {
	search := options.Search
	// An injected search function is the caller's authority for every selected
	// source unless it also supplies Google's source-specific fetcher. This
	// prevents an injected/test client from silently falling back to live I/O.
	useStructuredGoogleSearch := options.FetchGoogleJSON != nil || search == nil
	if search == nil {
		search = websearch.Search
	}
	fetchHTML := options.FetchHTML
	if fetchHTML == nil {
		fetchHTML = func(ctx context.Context, target string) (string, error) {
			result, err := webfetch.FetchWithOptions(ctx, target, patentPageMaxBytes, options.FetchOptions)
			if err != nil {
				return "", err
			}
			if result.SourceUnavailable {
				return "", errors.New(result.Error)
			}
			if result.Binary || strings.TrimSpace(result.Body) == "" {
				return "", errors.New("patent page did not return readable HTML")
			}
			return result.Body, nil
		}
	}
	fetchGoogleJSON := options.FetchGoogleJSON
	if fetchGoogleJSON == nil {
		fetchGoogleJSON = func(ctx context.Context, target string) ([]byte, error) {
			return fetchBoundedGoogleSearchJSON(ctx, target, options.FetchOptions)
		}
	}
	return &Client{
		search: search, searchOptions: options.SearchOptions, fetchHTML: fetchHTML,
		fetchGoogleJSON: fetchGoogleJSON, useStructuredGoogleSearch: useStructuredGoogleSearch,
	}
}

func (client *Client) Run(ctx context.Context, input Input) (Output, error) {
	operation := strings.ToLower(strings.TrimSpace(input.Operation))
	selected, err := selectSources(input.Sources)
	if err != nil {
		return Output{}, err
	}
	output := Output{Operation: operation, Records: []Record{}, SourceStatuses: sourceStatuses(selected)}

	switch operation {
	case OperationSearch:
		query := strings.TrimSpace(input.Query)
		if len([]rune(query)) < 2 {
			return Output{}, errors.New("patent_search.query must contain at least 2 characters")
		}
		queries := normalizedPatentSearchQueries(query, input.QueryVariants)
		output.Query = query
		limit := normalizeMaxResults(input.MaxResults)
		output = client.searchSources(ctx, output, selected, queries, limit)
		output.CandidateLimit = limit
		output.EvidenceDepth = "discovery_only"
		output.NextAction = "Select decision-relevant publication numbers from records, then call patent_search with operation=lookup. Search titles and snippets are not claim-level evidence."
		return output, nil
	case OperationLookup, OperationResolveDownload:
		publicationNumber, err := normalizePublicationNumber(input.PublicationNumber)
		if err != nil {
			return Output{}, err
		}
		output.PublicationNumber = publicationNumber
		if operation == OperationLookup {
			output.Records, output.Warnings = client.lookupRecords(ctx, publicationNumber, selected, input, output.Warnings)
			output.EvidenceDepth = patentLookupEvidenceDepth(output.Records)
			if output.EvidenceDepth == "full_record" {
				output.NextAction = "Screen the returned abstract, independent claims, focused description passages, and examples before adding this family to the evidence ledger."
			} else {
				output.NextAction = "No full record was read. Keep this publication as a candidate or evidence gap; do not make claim-level conclusions from the locator."
			}
			return output, nil
		}
		output.Downloads = client.resolveDownloads(ctx, publicationNumber, selected, &output.Warnings)
		return output, nil
	default:
		return Output{}, fmt.Errorf("patent_search.operation must be one of %q, %q, or %q", OperationSearch, OperationLookup, OperationResolveDownload)
	}
}

func (client *Client) searchSources(
	ctx context.Context,
	output Output,
	selected []sourceSpec,
	queries []string,
	maxResults int,
) Output {
	type sourceResult struct {
		index          int
		records        []Record
		candidateCount int
		attempts       int
		failures       int
		err            error
	}
	results := make(chan sourceResult, len(selected))
	querySemaphore := make(chan struct{}, 8)
	perQuery := maxResults / len(selected)
	if perQuery < 1 {
		perQuery = 1
	}

	var group sync.WaitGroup
	for index, spec := range selected {
		index, spec := index, spec
		group.Add(1)
		go func() {
			defer group.Done()
			queryResult := client.searchPatentQueries(ctx, spec, queries, perQuery, querySemaphore)
			result := sourceResult{
				index: index, records: queryResult.records, candidateCount: queryResult.candidateCount,
				attempts: queryResult.attempts, failures: queryResult.failures,
			}
			if result.failures == result.attempts {
				result.err = queryResult.lastErr
			}
			results <- result
		}()
	}
	group.Wait()
	close(results)

	ordered := make([]sourceResult, len(selected))
	for result := range results {
		ordered[result.index] = result
	}
	seenRecords := map[string]bool{}
	uniqueCandidates := map[string]bool{}
	candidateCount, failedAttempts := 0, 0
	for index, result := range ordered {
		candidateCount += result.candidateCount
		failedAttempts += result.failures
		for _, record := range result.records {
			if identity := patentRecordIdentity(record); identity != "" {
				uniqueCandidates[identity] = true
			}
		}
		if result.err != nil {
			output.SourceStatuses[index].Status = "unavailable"
			output.SourceStatuses[index].Note = appendStatusNote(selected[index].note, "Search unavailable: "+result.err.Error())
			output.Warnings = append(output.Warnings, fmt.Sprintf("%s search unavailable: %v", selected[index].label, result.err))
			continue
		}
		if len(result.records) == 0 {
			output.SourceStatuses[index].Status = "no_results"
			output.SourceStatuses[index].Note = appendStatusNote(selected[index].note, "No matching records were returned for this query.")
		} else {
			output.SourceStatuses[index].Status = "available"
		}
	}
	for row := 0; len(output.Records) < maxResults; row++ {
		added := false
		for _, result := range ordered {
			if row >= len(result.records) {
				continue
			}
			record := result.records[row]
			identity := patentRecordIdentity(record)
			if identity == "" || seenRecords[identity] {
				continue
			}
			seenRecords[identity] = true
			output.Records = append(output.Records, record)
			added = true
			if len(output.Records) >= maxResults {
				break
			}
		}
		if !added {
			break
		}
	}
	truncated := len(uniqueCandidates) > len(output.Records)
	output.LimitReached = truncated
	output.Retrieval = map[string]any{
		"scope": "ranked_deduplicated_candidate_window", "query_variants": len(queries),
		"requested": maxResults, "candidates": candidateCount, "returned": len(output.Records),
		"duplicates": max(0, candidateCount-len(uniqueCandidates)), "truncated": truncated,
		"has_more": truncated, "complete": !truncated, "source_attempts": len(selected) * len(queries),
		"source_failures": failedAttempts,
	}
	return output
}

func (client *Client) searchPatentSourceQuery(
	ctx context.Context,
	spec sourceSpec,
	query string,
	maxResults int,
) ([]Record, error) {
	if spec.id == "google_patents" && client.useStructuredGoogleSearch {
		records, structuredErr := client.searchGooglePatents(ctx, query, maxResults)
		if structuredErr == nil {
			return records, nil
		}
		records, fallbackErr := client.searchPatentWebSource(ctx, spec, query, maxResults)
		if fallbackErr == nil {
			return records, nil
		}
		return nil, errors.Join(
			fmt.Errorf("structured endpoint: %w", structuredErr),
			fmt.Errorf("domain-scoped fallback: %w", fallbackErr),
		)
	}
	return client.searchPatentWebSource(ctx, spec, query, maxResults)
}

func (client *Client) searchPatentWebSource(
	ctx context.Context,
	spec sourceSpec,
	query string,
	maxResults int,
) ([]Record, error) {
	searchOutput, err := client.search(ctx, websearch.Input{
		Query: query, AllowedDomains: append([]string(nil), spec.domains...), MaxResults: maxResults,
	}, client.searchOptions)
	if err != nil {
		return nil, err
	}
	if searchOutput.Failure != nil {
		return nil, errors.New(searchOutput.Failure.Message)
	}
	records := make([]Record, 0, len(searchOutput.Sources))
	for _, evidence := range searchOutput.Sources {
		if strings.TrimSpace(evidence.URL) == "" {
			continue
		}
		records = append(records, Record{
			Source: spec.id, SourceLabel: spec.label, Title: strings.TrimSpace(evidence.Title),
			URL: evidence.URL, Snippet: strings.TrimSpace(evidence.Snippet),
			EvidenceState: "discovered", RecordDepth: "locator_only",
		})
	}
	return records, nil
}

func normalizedPatentSearchQueries(primary string, variants []string) []string {
	queries := make([]string, 0, min(1+len(variants), 7))
	seen := map[string]bool{}
	automatic := discoveryquery.CrossLanguageVariants(primary)
	variantLimit := 6
	if len(automatic) > 0 {
		variantLimit = 5
	}
	candidates := []string{primary}
	for _, candidate := range variants {
		if len(candidates) >= 1+variantLimit {
			break
		}
		candidates = append(candidates, candidate)
	}
	candidates = append(candidates, automatic...)
	for _, raw := range candidates {
		query := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
		key := strings.ToLower(query)
		if len([]rune(query)) < 2 || seen[key] {
			continue
		}
		seen[key] = true
		queries = append(queries, query)
		if len(queries) == 7 {
			break
		}
	}
	return queries
}

func patentRecordIdentity(record Record) string {
	if value := strings.ToUpper(strings.TrimSpace(record.PublicationNumber)); value != "" {
		return "publication:" + value
	}
	return "url:" + strings.ToLower(strings.TrimSpace(record.URL))
}

type googlePatentSearchResponse struct {
	Results struct {
		TotalNumResults int `json:"total_num_results"`
		Clusters        []struct {
			Results []struct {
				Patent struct {
					Title             string `json:"title"`
					Snippet           string `json:"snippet"`
					PublicationNumber string `json:"publication_number"`
				} `json:"patent"`
			} `json:"result"`
		} `json:"cluster"`
	} `json:"results"`
}

func (client *Client) searchGooglePatents(ctx context.Context, query string, maxResults int) ([]Record, error) {
	inner := url.Values{}
	inner.Set("q", query)
	target := "https://patents.google.com/xhr/query?url=" + url.QueryEscape(inner.Encode())
	payload, err := client.fetchGoogleJSON(ctx, target)
	if err != nil {
		return nil, err
	}
	var response googlePatentSearchResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("decode Google Patents search response: %w", err)
	}
	records := make([]Record, 0, maxResults)
	seen := map[string]bool{}
	for _, cluster := range response.Results.Clusters {
		for _, result := range cluster.Results {
			publicationNumber, err := normalizePublicationNumber(result.Patent.PublicationNumber)
			if err != nil || seen[publicationNumber] {
				continue
			}
			seen[publicationNumber] = true
			title := cleanGooglePatentText(result.Patent.Title)
			if title == "" {
				title = publicationNumber
			}
			records = append(records, Record{
				Source: "google_patents", SourceLabel: "Google Patents", Title: title,
				URL: googlePatentURL(publicationNumber), Snippet: cleanGooglePatentText(result.Patent.Snippet),
				PublicationNumber: publicationNumber, EvidenceState: "discovered", RecordDepth: "locator_only",
			})
			if len(records) >= maxResults {
				return records, nil
			}
		}
	}
	if response.Results.TotalNumResults > 0 && len(records) == 0 {
		return nil, errors.New("Google Patents response contained results but no valid publication records")
	}
	return records, nil
}

func fetchBoundedGoogleSearchJSON(ctx context.Context, target string, options webfetch.Options) ([]byte, error) {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "patents.google.com") || parsed.Path != "/xhr/query" {
		return nil, errors.New("Google Patents search URL is outside the approved endpoint")
	}
	response, err := webfetch.FetchWithOptions(ctx, target, googleSearchMaxBytes, options)
	if err != nil {
		return nil, fmt.Errorf("fetch Google Patents search response: %w", err)
	}
	if response.SourceUnavailable {
		return nil, fmt.Errorf("fetch Google Patents search response: %s", response.Error)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || response.Binary || response.Truncated || strings.TrimSpace(response.Body) == "" {
		return nil, fmt.Errorf("Google Patents search response was not complete readable JSON (status=%d type=%q truncated=%t)", response.StatusCode, response.ContentType, response.Truncated)
	}
	return []byte(response.Body), nil
}

func cleanGooglePatentText(value string) string {
	return strings.Join(strings.Fields(html.UnescapeString(htmlTagPattern.ReplaceAllString(value, " "))), " ")
}

func appendStatusNote(existing, addition string) string {
	existing = strings.TrimSpace(existing)
	addition = strings.TrimSpace(addition)
	if existing == "" {
		return addition
	}
	if addition == "" {
		return existing
	}
	return existing + " " + addition
}

func (client *Client) resolveDownloads(ctx context.Context, publicationNumber string, selected []sourceSpec, warnings *[]string) []DownloadResolution {
	downloads := make([]DownloadResolution, 0, len(selected))
	for _, spec := range selected {
		resolution := DownloadResolution{
			Source: spec.id, SourceLabel: spec.label, PageURL: spec.lookupURL(publicationNumber),
			RequiresAuthentication: spec.requiresAuthentication,
		}
		switch spec.id {
		case "google_patents":
			body, err := client.fetchHTML(ctx, resolution.PageURL)
			if err != nil {
				resolution.State = DownloadUnavailable
				resolution.Instructions = "Open the Google Patents record and use its Download PDF action."
				*warnings = append(*warnings, "Google Patents PDF metadata unavailable: "+err.Error())
				break
			}
			pdfURL, err := extractTrustedGooglePDF(resolution.PageURL, body)
			if err != nil {
				resolution.State = DownloadUnavailable
				resolution.Instructions = "Open the Google Patents record and verify its Download PDF action manually."
				*warnings = append(*warnings, "Google Patents PDF URL was not trusted: "+err.Error())
				break
			}
			resolution.State = DownloadResolved
			resolution.DownloadURL = pdfURL
			resolution.Instructions = "Fetch this exact PDF URL with the governed downloader, then save it as a project artifact with source metadata."
		case "wipo":
			resolution.State = DownloadUserActionRequired
			resolution.Instructions = "Open the official PATENTSCOPE record, verify the publication number, then use its Documents PDF or ZIP action."
		case "epo":
			resolution.State = DownloadUserActionRequired
			resolution.Instructions = "Open Espacenet for an interactive document download, or configure registered EPO OPS credentials outside the model context."
		case "cnipa":
			resolution.State = DownloadUserActionRequired
			resolution.Instructions = "Sign in to the official CNIPA portal, search this publication number, and use the portal download action; complete any CAPTCHA yourself."
		}
		downloads = append(downloads, resolution)
	}
	return downloads
}

func selectSources(requested []string) ([]sourceSpec, error) {
	if len(requested) == 0 {
		return append([]sourceSpec(nil), sourceSpecs...), nil
	}
	wanted := map[string]bool{}
	for _, raw := range requested {
		canonical, ok := canonicalSource(raw)
		if !ok {
			return nil, fmt.Errorf("patent_search.sources contains unsupported source %q", raw)
		}
		wanted[canonical] = true
	}
	selected := make([]sourceSpec, 0, len(wanted))
	for _, spec := range sourceSpecs {
		if wanted[spec.id] {
			selected = append(selected, spec)
		}
	}
	return selected, nil
}

func canonicalSource(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "wipo", "patentscope":
		return "wipo", true
	case "google", "google_patents", "google-patents":
		return "google_patents", true
	case "epo", "espacenet":
		return "epo", true
	case "cnipa", "china", "chinese":
		return "cnipa", true
	default:
		return "", false
	}
}

func sourceStatuses(selected []sourceSpec) []SourceStatus {
	statuses := make([]SourceStatus, 0, len(selected))
	for _, spec := range selected {
		statuses = append(statuses, SourceStatus{
			Source: spec.id, Label: spec.label, SearchURL: spec.searchURL(""), AccessMode: spec.accessMode,
			DownloadMode: spec.downloadMode, RequiresAuthentication: spec.requiresAuthentication,
			MachineReadable: spec.machineReadable, Status: "configured", Note: spec.note,
		})
	}
	return statuses
}

func normalizePublicationNumber(raw string) (string, error) {
	var builder strings.Builder
	for _, character := range strings.ToUpper(strings.TrimSpace(raw)) {
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			builder.WriteRune(character)
		}
	}
	normalized := builder.String()
	if !publicationNumberPattern.MatchString(normalized) || !strings.ContainsAny(normalized, "0123456789") {
		return "", errors.New("patent_search.publication_number must be a patent publication number such as WO2024123456A1")
	}
	return normalized, nil
}

// NormalizePublicationNumber validates and canonicalizes a patent publication
// number for callers that bind downstream artifacts to a patent_search result.
func NormalizePublicationNumber(raw string) (string, error) {
	return normalizePublicationNumber(raw)
}

func normalizeMaxResults(value int) int {
	if value <= 0 {
		return defaultMaxResults
	}
	if value > maximumMaxResults {
		return maximumMaxResults
	}
	return value
}

func googlePatentURL(publicationNumber string) string {
	return "https://patents.google.com/patent/" + url.PathEscape(publicationNumber) + "/en"
}

func extractTrustedGooglePDF(pageURL, body string) (string, error) {
	var candidates []string
	for _, tag := range tagPattern.FindAllString(body, -1) {
		attributes := tagAttributes(tag)
		if strings.EqualFold(attributes["name"], "citation_pdf_url") && strings.TrimSpace(attributes["content"]) != "" {
			candidates = append(candidates, attributes["content"])
		}
		if href := strings.TrimSpace(attributes["href"]); href != "" && strings.Contains(strings.ToLower(href), ".pdf") {
			candidates = append(candidates, href)
		}
	}
	for _, candidate := range candidates {
		resolved, err := trustedGooglePDFURL(pageURL, html.UnescapeString(strings.TrimSpace(candidate)))
		if err == nil {
			return resolved, nil
		}
	}
	return "", errors.New("page did not expose a PDF URL on a trusted Google Patents host")
}

func tagAttributes(tag string) map[string]string {
	attributes := map[string]string{}
	for _, match := range attributePattern.FindAllStringSubmatch(tag, -1) {
		value := match[2]
		if value == "" {
			value = match[3]
		}
		if value == "" {
			value = match[4]
		}
		attributes[strings.ToLower(match[1])] = value
	}
	return attributes
}

func trustedGooglePDFURL(pageURL, candidate string) (string, error) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(candidate)
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != "https" {
		return "", errors.New("PDF URL must use HTTPS")
	}
	if resolved.User != nil || resolved.Port() != "" || resolved.RawQuery != "" || resolved.Fragment != "" {
		return "", errors.New("PDF URL must not contain credentials, a custom port, query parameters, or a fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(resolved.Hostname()), ".")
	if host != "patents.google.com" && host != "patentimages.storage.googleapis.com" {
		return "", fmt.Errorf("untrusted PDF host %q", host)
	}
	if !strings.HasSuffix(strings.ToLower(resolved.Path), ".pdf") {
		return "", errors.New("download URL is not a PDF")
	}
	return resolved.String(), nil
}
