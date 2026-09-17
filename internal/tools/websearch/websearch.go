package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"synon-go/internal/subprocess"
)

const providerName = "synon-websearch"
const defaultHTTPEndpoint = "https://duckduckgo.com/html/?q={query}"
const maxHTTPResponseBytes = 4 * 1024 * 1024

const (
	defaultSearchResultLimit      = 50
	maxSearchResultLimit          = 200
	maxSearchSnippetCharacters    = 600
	maxBingSearchResultLimit      = 50
	maxWikipediaSearchResultLimit = 50
)

const (
	searchFormatHTML                = "html"
	searchFormatMediaWikiOpenSearch = "mediawiki-opensearch"
	searchFormatCrossref            = "crossref"
	searchFormatEuropePMC           = "europe-pmc"
)

var crossrefSupplementaryDOIPattern = regexp.MustCompile(`(?i)\.s\d+$`)

var defaultHTTPBackends = []httpSearchBackend{
	{Name: "bing-html", Endpoint: "https://www.bing.com/search?q={query}&count={limit}&setlang=en-US&cc=us", Format: searchFormatHTML, BlockedResultDomains: []string{"bing.com"}},
	{Name: "mojeek", Endpoint: "https://www.mojeek.com/search?q={query}", Format: searchFormatHTML, BlockedResultDomains: []string{"mojeek.com"}},
	{Name: "wikipedia", Endpoint: "https://en.wikipedia.org/w/api.php?action=opensearch&search={query}&limit={limit}&namespace=0&format=json&redirects=resolve", Format: searchFormatMediaWikiOpenSearch},
	{Name: "crossref", Endpoint: "https://api.crossref.org/works?query.bibliographic={query}&rows={limit}&filter=type:journal-article&select=DOI,title,URL,abstract,publisher,published,author,container-title", Format: searchFormatCrossref},
	{Name: "europe-pmc", Endpoint: "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query={query}&pageSize={limit}&resultType=core&format=json", Format: searchFormatEuropePMC},
	{Name: "duckduckgo-lite", Endpoint: "https://lite.duckduckgo.com/lite/?q={query}", Format: searchFormatHTML, BlockedResultDomains: []string{"duckduckgo.com"}},
}

type Input struct {
	Query                 string
	AllowedDomains        []string
	BlockedDomains        []string
	MaxResults            int
	PublishedAfter        string
	PublishedBefore       string
	Sort                  string
	ProviderContinuations map[string]string
}

type Output struct {
	Query           string         `json:"query"`
	Results         []any          `json:"results"`
	DurationSeconds float64        `json:"durationSeconds"`
	Sources         []Evidence     `json:"sources"`
	Retrieval       map[string]any `json:"retrieval,omitempty"`
	Failure         *Failure       `json:"failure,omitempty"`
	Diagnostics     map[string]any `json:"diagnostics,omitempty"`
}

type SearchResult struct {
	ToolUseID string `json:"tool_use_id"`
	Content   []Hit  `json:"content"`
}

type Hit struct {
	Title   string        `json:"title"`
	URL     string        `json:"url"`
	Snippet string        `json:"snippet,omitempty"`
	Record  *SourceRecord `json:"record,omitempty"`
}

type Failure struct {
	Kind        string `json:"kind"`
	Message     string `json:"message"`
	Recoverable bool   `json:"recoverable"`
}

type Evidence struct {
	SchemaVersion  int            `json:"schemaVersion"`
	Kind           string         `json:"kind"`
	Status         string         `json:"status"`
	EvidenceState  string         `json:"evidenceState"`
	SourceQuality  string         `json:"sourceQuality"`
	URL            string         `json:"url"`
	CanonicalURL   string         `json:"canonicalUrl,omitempty"`
	Host           string         `json:"host,omitempty"`
	DomainKey      string         `json:"domainKey,omitempty"`
	Title          string         `json:"title,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Rank           int            `json:"rank,omitempty"`
	Snippet        string         `json:"snippet,omitempty"`
	QualityScore   int            `json:"qualityScore,omitempty"`
	QualitySignals []string       `json:"qualitySignals,omitempty"`
	RetrievedAt    string         `json:"retrievedAt,omitempty"`
	Record         *SourceRecord  `json:"record,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type Options struct {
	Root           string
	Now            func() time.Time
	Timeout        time.Duration
	HTTPBackends   []httpSearchBackend
	ClientForURL   func(context.Context, string) (*http.Client, error)
	BaseHTTPClient *http.Client
}

type httpSearchBackend struct {
	Name                 string
	Endpoint             string
	Format               string
	BlockedResultDomains []string
}

type httpSearchBackendResult struct {
	Index          int
	Name           string
	Hits           []Hit
	StatusCode     int
	RawResults     int
	RequestedLimit int
	TotalResults   int
	TotalKnown     bool
	NextCursor     string
	RequestState   backendRequestState
	Err            error
}

type scriptResult struct {
	Title   string        `json:"title"`
	URL     string        `json:"url"`
	Href    string        `json:"href"`
	Link    string        `json:"link"`
	Snippet string        `json:"snippet"`
	Body    string        `json:"body"`
	Record  *SourceRecord `json:"record,omitempty"`
}

func Search(ctx context.Context, input Input, options Options) (Output, error) {
	started := time.Now()
	now := options.Now
	if now == nil {
		now = time.Now
	}
	query := strings.TrimSpace(input.Query)
	maxResults := normalizeMaxResults(input.MaxResults)
	diagnostics := map[string]any{
		"provider":            providerName,
		"requestedMaxResults": maxResults,
	}
	if query == "" {
		return Output{}, errors.New("WebSearch.query is required")
	}
	if len([]rune(query)) < 2 {
		return Output{}, errors.New("WebSearch.query must contain at least 2 characters")
	}
	if len(input.AllowedDomains) > 0 && len(input.BlockedDomains) > 0 {
		return Output{}, errors.New("WebSearch cannot specify both allowed_domains and blocked_domains")
	}
	if err := validateSearchConstraints(input); err != nil {
		return Output{}, err
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SYNON_WEBSEARCH_MODE")), "disabled") {
		return unavailable(query, started, "Web search is disabled in settings.", diagnostics), nil
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	searchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	script := resolveScriptPath(options.Root)
	diagnostics["scriptPath"] = script
	diagnostics["scriptExists"] = fileExists(script)
	if shouldTryGoHTTPBackend(script) {
		hits, httpDiagnostics, err := searchHTTPBackends(searchCtx, input, maxResults, options)
		for key, value := range httpDiagnostics {
			diagnostics[key] = value
		}
		if err == nil && len(hits) > 0 {
			diagnostics["backend"] = "go-http"
			diagnostics["returnedResults"] = len(hits)
			return outputFromHits(query, started, hits, diagnostics, now), nil
		}
		if err != nil {
			diagnostics["goHTTPError"] = compactString(err.Error(), 500)
		}
	}
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	if errors.Is(searchCtx.Err(), context.DeadlineExceeded) {
		diagnostics["error"] = "websearch total time budget expired"
		diagnostics["timedOut"] = true
		diagnostics["timeoutSeconds"] = timeout.Seconds()
		return unavailable(query, started, "WebSearch timed out after trying the available search backends. Retry later, refine the query, or use a source-specific scientific tool.", diagnostics), nil
	}
	if script == "" || !fileExists(script) {
		return unavailable(query, started, "WebSearch failed: Go HTTP backend returned no usable links and no optional Python search helper was explicitly configured.", diagnostics), nil
	}

	python, pythonExists := resolvePython(options.Root)
	diagnostics["pythonPath"] = python
	diagnostics["pythonExists"] = pythonExists
	if !pythonExists {
		return unavailable(query, started, "WebSearch failed: the configured Python runtime for the optional search helper was not found.", diagnostics), nil
	}

	args := []string{
		script,
		queryWithAllowedDomains(query, input.AllowedDomains),
		"--max-results",
		fmt.Sprintf("%d", max(maxResults*4, 16)),
	}
	cmd := exec.CommandContext(searchCtx, python, args...)
	control, controlErr := subprocess.Prepare(cmd)
	if controlErr != nil {
		diagnostics["error"] = "websearch subprocess control unavailable"
		return unavailable(query, started, "WebSearch failed: the optional Python search helper could not be safely started.", diagnostics), nil
	}
	defer control.Close()
	stdout := newBoundedBuffer(maxHTTPResponseBytes)
	stderr := newBoundedBuffer(64 * 1024)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		diagnostics["error"] = compactString(err.Error(), 500)
		return unavailable(query, started, "WebSearch failed: the optional Python search helper could not be started.", diagnostics), nil
	}
	if err := control.Attach(cmd); err != nil {
		_ = control.Kill(cmd)
		_ = cmd.Wait()
		diagnostics["error"] = "websearch subprocess isolation unavailable"
		return unavailable(query, started, "WebSearch failed: the optional Python search helper could not be isolated.", diagnostics), nil
	}
	if err := cmd.Wait(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return Output{}, contextErr
		}
		if errors.Is(searchCtx.Err(), context.DeadlineExceeded) {
			diagnostics["error"] = "websearch total time budget expired"
			diagnostics["timedOut"] = true
			diagnostics["timeoutSeconds"] = timeout.Seconds()
			return unavailable(query, started, "WebSearch timed out after trying the available search backends. Retry later, refine the query, or use a source-specific scientific tool.", diagnostics), nil
		}
		diagnostics["error"] = compactString(err.Error(), 500)
		if stderr.Len() > 0 {
			diagnostics["stderr"] = compactString(stderr.String(), 1000)
		}
		return unavailable(query, started, "WebSearch failed: Synon built-in websearch.py returned no usable links.", diagnostics), nil
	}
	if stdout.Exceeded() || stderr.Exceeded() {
		diagnostics["error"] = "websearch helper output exceeded its bounded channel"
		return unavailable(query, started, "WebSearch failed: the optional Python search helper returned excessive output.", diagnostics), nil
	}

	var raw []scriptResult
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		diagnostics["error"] = "websearch.py returned non-array JSON"
		return unavailable(query, started, "WebSearch failed: Synon built-in websearch.py returned no usable links.", diagnostics), nil
	}
	hits := selectRelevantHits(raw, input, maxResults)
	diagnostics["returnedResults"] = len(hits)
	if len(hits) == 0 {
		return unavailable(query, started, "WebSearch failed: Synon built-in websearch.py returned no usable links.", diagnostics), nil
	}

	diagnostics["backend"] = "python-ddgs"
	return outputFromHits(query, started, hits, diagnostics, now), nil
}

func outputFromHits(query string, started time.Time, hits []Hit, diagnostics map[string]any, now func() time.Time) Output {
	retrievedAt := now().UTC().Format(time.RFC3339Nano)
	result := SearchResult{
		ToolUseID: "synon-websearch-web-search",
		Content:   searchDisplayHits(hits),
	}
	sources := make([]Evidence, 0, len(hits))
	for index, hit := range hits {
		sources = append(sources, evidenceForHit(query, hit, index+1, retrievedAt))
	}
	return Output{
		Query:           query,
		Results:         []any{"Search provider: " + providerName, result},
		Sources:         sources,
		Retrieval:       webSearchRetrievalCoverage(len(hits), diagnostics),
		DurationSeconds: time.Since(started).Seconds(),
		Diagnostics:     diagnostics,
	}
}

func webSearchRetrievalCoverage(returned int, diagnostics map[string]any) map[string]any {
	requested := int(numberFromDiagnostic(diagnostics["requestedMaxResults"]))
	candidates := int(numberFromDiagnostic(diagnostics["candidateResults"]))
	if candidates < returned {
		candidates = returned
	}
	truncated, _ := diagnostics["providerPageMayBeTruncated"].(bool)
	if requested > 0 && returned >= requested {
		truncated = true
	}
	coverage := map[string]any{
		"scope":                "discovery_window",
		"requested":            requested,
		"candidates":           candidates,
		"returned":             returned,
		"truncated":            truncated,
		"exhaustive":           false,
		"provider_total_known": false,
	}
	if values, ok := diagnostics["providerTotals"].(map[string]any); ok && len(values) > 0 {
		coverage["provider_totals"] = values
	}
	if values, ok := diagnostics["providerContinuations"].(map[string]string); ok && len(values) > 0 {
		coverage["provider_continuations"] = values
	}
	return coverage
}

func numberFromDiagnostic(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	default:
		return 0
	}
}

func unavailable(query string, started time.Time, message string, diagnostics map[string]any) Output {
	return Output{
		Query:   query,
		Results: []any{message},
		Sources: []Evidence{},
		Retrieval: map[string]any{
			"scope": "discovery_window", "requested": int(numberFromDiagnostic(diagnostics["requestedMaxResults"])),
			"candidates": 0, "returned": 0, "exhaustive": false, "provider_total_known": false, "source_unavailable": true,
		},
		DurationSeconds: time.Since(started).Seconds(),
		Failure: &Failure{
			Kind:        "search_unavailable",
			Message:     message,
			Recoverable: true,
		},
		Diagnostics: diagnostics,
	}
}

func shouldTryGoHTTPBackend(script string) bool {
	if strings.TrimSpace(os.Getenv("SYNON_WEBSEARCH_ENDPOINT")) != "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SYNON_WEBSEARCH_GO")), "false") {
		return false
	}
	if strings.TrimSpace(os.Getenv("SYNON_WEBSEARCH_SCRIPT")) != "" {
		return false
	}
	return true
}
