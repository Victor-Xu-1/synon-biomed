package rcsbsearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"synon-go/internal/buildinfo"
	"synon-go/internal/tools/securefetch"
)

const (
	productionSearchBaseURL = "https://search.rcsb.org"
	productionDataBaseURL   = "https://data.rcsb.org"
	searchPath              = "/rcsbsearch/v2/query"
)

type TermMode string

const (
	TermModeAll    TermMode = "all"
	TermModeAny    TermMode = "any"
	TermModePhrase TermMode = "phrase"
)

type SortBy string

const (
	SortReleaseDateDesc SortBy = "release_date_desc"
	SortResolutionAsc   SortBy = "resolution_asc"
	SortRelevance       SortBy = "relevance"
)

type Input struct {
	Query                        string
	TermMode                     TermMode
	Organism                     string
	ExperimentalMethod           string
	MinimumPolymerEntityCount    int
	MinimumNonpolymerEntityCount int
	MaximumResolution            float64
	SortBy                       SortBy
	Limit                        int
	Start                        int
}

type Entry struct {
	EntryID               string    `json:"entry_id"`
	SearchScore           float64   `json:"search_score,omitempty"`
	Title                 string    `json:"title"`
	InitialReleaseDate    string    `json:"initial_release_date"`
	RevisionDate          string    `json:"revision_date,omitempty"`
	ExperimentalMethods   []string  `json:"experimental_methods"`
	ResolutionAngstrom    []float64 `json:"resolution_angstrom,omitempty"`
	PolymerEntityCount    int       `json:"polymer_entity_count"`
	NonpolymerEntityCount int       `json:"nonpolymer_entity_count"`
	PolymerComposition    string    `json:"polymer_composition,omitempty"`
	StructureURL          string    `json:"structure_url"`
	MetadataURL           string    `json:"metadata_url"`
}

type Result struct {
	Query                  string   `json:"query"`
	TermMode               TermMode `json:"term_mode"`
	Organism               string   `json:"organism,omitempty"`
	ExperimentalMethod     string   `json:"experimental_method,omitempty"`
	SortBy                 SortBy   `json:"sort_by"`
	TotalCount             int      `json:"total_count"`
	Entries                []Entry  `json:"entries"`
	Start                  int      `json:"start"`
	RetrievedCount         int      `json:"retrieved_count"`
	HasMore                bool     `json:"has_more"`
	NextStart              int      `json:"next_start,omitempty"`
	RetrievedAt            string   `json:"retrieved_at"`
	SearchRequestSHA256    string   `json:"search_request_sha256"`
	SearchDocumentationURL string   `json:"search_documentation_url"`
}

type Fetcher interface {
	Fetch(context.Context, string, securefetch.Policy) (*securefetch.Response, error)
}

type Options struct {
	Fetcher                    Fetcher
	SearchBaseURL              string
	DataBaseURL                string
	TestOnlyAllowCustomOrigins bool
	MaxBytes                   int64
	Timeout                    time.Duration
	Now                        func() time.Time
}

type Client struct {
	fetcher      Fetcher
	searchOrigin origin
	dataOrigin   origin
	maxBytes     int64
	timeout      time.Duration
	now          func() time.Time
	configErr    error
}

type origin struct {
	baseURL string
	host    string
	port    string
}

var entryIDPattern = regexp.MustCompile(`^[0-9][A-Z0-9]{3}$`)

func New(options Options) *Client {
	searchOrigin, searchErr := parseOrigin(options.SearchBaseURL, productionSearchBaseURL, options.TestOnlyAllowCustomOrigins)
	dataOrigin, dataErr := parseOrigin(options.DataBaseURL, productionDataBaseURL, options.TestOnlyAllowCustomOrigins)
	configErr := searchErr
	if configErr == nil {
		configErr = dataErr
	}
	if options.MaxBytes <= 0 || options.Timeout <= 0 {
		configErr = errors.New("rcsb_search_invalid_configuration")
	}
	fetcher := options.Fetcher
	if fetcher == nil {
		fetcher = securefetch.New(securefetch.Options{})
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		fetcher: fetcher, searchOrigin: searchOrigin, dataOrigin: dataOrigin,
		maxBytes: options.MaxBytes, timeout: options.Timeout, now: now, configErr: configErr,
	}
}

func parseOrigin(raw, production string, allowCustom bool) (origin, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		raw = production
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(raw != production && !allowCustom) {
		return origin{}, errors.New("rcsb_search_invalid_configuration")
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	return origin{baseURL: raw, host: strings.ToLower(parsed.Hostname()), port: port}, nil
}

func (c *Client) Search(ctx context.Context, input Input) (Result, error) {
	if c == nil || c.configErr != nil {
		if c != nil && c.configErr != nil {
			return Result{}, c.configErr
		}
		return Result{}, errors.New("rcsb_search_invalid_configuration")
	}
	normalized, requestBody, err := normalizeInput(input)
	if err != nil {
		return Result{}, err
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return Result{}, errors.New("rcsb_search_request_encode_failed")
	}
	digest := sha256.Sum256(encoded)
	searchURL := c.searchOrigin.baseURL + searchPath + "?json=" + url.QueryEscape(string(encoded))
	response, err := c.fetch(ctx, searchURL, c.searchOrigin)
	if err != nil {
		return Result{}, mapUpstreamError("search", err)
	}
	searchBody, err := readResponse(response)
	if err != nil {
		return Result{}, err
	}
	if len(searchBody) == 0 {
		return c.emptyResult(normalized, hex.EncodeToString(digest[:])), nil
	}
	var envelope searchEnvelope
	if err := json.Unmarshal(searchBody, &envelope); err != nil {
		return Result{}, errors.New("rcsb_search_response_invalid")
	}
	if envelope.TotalCount < 0 || len(envelope.ResultSet) > normalized.Limit {
		return Result{}, errors.New("rcsb_search_response_invalid")
	}
	entries := make([]Entry, 0, len(envelope.ResultSet))
	for _, hit := range envelope.ResultSet {
		entryID := strings.ToUpper(strings.TrimSpace(hit.Identifier))
		if !entryIDPattern.MatchString(entryID) {
			return Result{}, errors.New("rcsb_search_response_invalid")
		}
		metadataURL := c.dataOrigin.baseURL + "/rest/v1/core/entry/" + url.PathEscape(entryID)
		metadataResponse, fetchErr := c.fetch(ctx, metadataURL, c.dataOrigin)
		if fetchErr != nil {
			return Result{}, mapUpstreamError("metadata", fetchErr)
		}
		metadataBody, readErr := readResponse(metadataResponse)
		if readErr != nil {
			return Result{}, readErr
		}
		entry, parseErr := parseEntryMetadata(entryID, hit.Score, metadataURL, metadataBody)
		if parseErr != nil {
			return Result{}, parseErr
		}
		entries = append(entries, entry)
	}
	hasMore := normalized.Start+len(entries) < envelope.TotalCount
	nextStart := 0
	if hasMore {
		nextStart = normalized.Start + len(entries)
	}
	return Result{
		Query: normalized.Query, TermMode: normalized.TermMode, Organism: normalized.Organism,
		ExperimentalMethod: normalized.ExperimentalMethod, SortBy: normalized.SortBy,
		TotalCount: envelope.TotalCount, Entries: entries, Start: normalized.Start,
		RetrievedCount: len(entries), HasMore: hasMore, NextStart: nextStart,
		RetrievedAt: c.now().UTC().Format(time.RFC3339), SearchRequestSHA256: hex.EncodeToString(digest[:]),
		SearchDocumentationURL: "https://search.rcsb.org/",
	}, nil
}

func (c *Client) emptyResult(input Input, digest string) Result {
	return Result{
		Query: input.Query, TermMode: input.TermMode, Organism: input.Organism,
		ExperimentalMethod: input.ExperimentalMethod, SortBy: input.SortBy,
		Entries: []Entry{}, Start: input.Start, RetrievedCount: 0, HasMore: false,
		RetrievedAt:         c.now().UTC().Format(time.RFC3339),
		SearchRequestSHA256: digest, SearchDocumentationURL: "https://search.rcsb.org/",
	}
}

func (c *Client) fetch(ctx context.Context, target string, source origin) (*securefetch.Response, error) {
	identity := buildinfo.Release()
	return c.fetcher.Fetch(ctx, target, securefetch.Policy{
		AllowedHosts: []string{source.host}, AllowedPorts: []string{source.port},
		AcceptedMediaTypes: []string{"application/json"}, MaxRedirects: 0,
		MaxBytes: c.maxBytes, Timeout: c.timeout,
		UserAgent: identity.MachineSlug + "-rcsb-search/" + identity.Version,
	})
}

func readResponse(response *securefetch.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("rcsb_search_response_invalid")
	}
	body, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil {
		return nil, errors.New("rcsb_search_response_read_failed")
	}
	if closeErr != nil {
		return nil, errors.New("rcsb_search_response_read_failed")
	}
	return body, nil
}

func mapUpstreamError(stage string, err error) error {
	if status, ok := securefetch.HTTPStatus(err); ok {
		switch {
		case status == 429:
			return errors.New("rcsb_search_rate_limited")
		case status >= 500:
			return errors.New("rcsb_search_upstream_unavailable")
		default:
			return fmt.Errorf("rcsb_search_%s_failed", stage)
		}
	}
	return err
}

func normalizeInput(input Input) (Input, map[string]any, error) {
	input.Query = strings.TrimSpace(input.Query)
	input.Organism = strings.TrimSpace(input.Organism)
	input.ExperimentalMethod = strings.TrimSpace(input.ExperimentalMethod)
	if len([]byte(input.Query)) < 2 || len([]byte(input.Query)) > 512 ||
		len([]byte(input.Organism)) > 160 || len([]byte(input.ExperimentalMethod)) > 160 {
		return Input{}, nil, errors.New("rcsb_search_invalid_request")
	}
	if input.TermMode == "" {
		input.TermMode = TermModeAll
	}
	if input.SortBy == "" {
		input.SortBy = SortReleaseDateDesc
	}
	if input.Limit == 0 {
		input.Limit = 10
	}
	if input.Limit < 1 || input.Limit > 25 || input.Start < 0 || input.Start > 1_000_000 || input.MinimumPolymerEntityCount < 0 ||
		input.MinimumPolymerEntityCount > 100 || input.MinimumNonpolymerEntityCount < 0 ||
		input.MinimumNonpolymerEntityCount > 100 || input.MaximumResolution < 0 || input.MaximumResolution > 100 {
		return Input{}, nil, errors.New("rcsb_search_invalid_request")
	}
	queryValue, err := fullTextValue(input.Query, input.TermMode)
	if err != nil {
		return Input{}, nil, err
	}
	nodes := []any{terminal("full_text", map[string]any{"value": queryValue})}
	if input.Organism != "" {
		nodes = append(nodes, terminal("text", map[string]any{
			"attribute": "rcsb_entity_source_organism.ncbi_scientific_name",
			"operator":  "exact_match", "value": input.Organism,
		}))
	}
	if input.ExperimentalMethod != "" {
		nodes = append(nodes, terminal("text", map[string]any{
			"attribute": "exptl.method", "operator": "exact_match", "value": input.ExperimentalMethod,
		}))
	}
	if input.MinimumPolymerEntityCount > 0 {
		nodes = append(nodes, terminal("text", map[string]any{
			"attribute": "rcsb_entry_info.polymer_entity_count", "operator": "greater_or_equal",
			"value": input.MinimumPolymerEntityCount,
		}))
	}
	if input.MinimumNonpolymerEntityCount > 0 {
		nodes = append(nodes, terminal("text", map[string]any{
			"attribute": "rcsb_entry_info.nonpolymer_entity_count", "operator": "greater_or_equal",
			"value": input.MinimumNonpolymerEntityCount,
		}))
	}
	if input.MaximumResolution > 0 {
		nodes = append(nodes, terminal("text", map[string]any{
			"attribute": "rcsb_entry_info.resolution_combined", "operator": "less_or_equal",
			"value": input.MaximumResolution,
		}))
	}
	query := nodes[0]
	if len(nodes) > 1 {
		query = map[string]any{"type": "group", "logical_operator": "and", "nodes": nodes}
	}
	requestOptions := map[string]any{
		"paginate":          map[string]any{"start": input.Start, "rows": input.Limit},
		"results_verbosity": "minimal", "results_content_type": []string{"experimental"},
	}
	switch input.SortBy {
	case SortReleaseDateDesc:
		requestOptions["sort"] = []any{map[string]any{
			"sort_by": "rcsb_accession_info.initial_release_date", "direction": "desc",
		}}
	case SortResolutionAsc:
		requestOptions["sort"] = []any{map[string]any{
			"sort_by": "rcsb_entry_info.resolution_combined", "direction": "asc",
		}}
	case SortRelevance:
	default:
		return Input{}, nil, errors.New("rcsb_search_invalid_request")
	}
	return input, map[string]any{
		"query": query, "request_options": requestOptions, "return_type": "entry",
	}, nil
}

func fullTextValue(query string, mode TermMode) (string, error) {
	words := strings.Fields(query)
	if len(words) == 0 {
		return "", errors.New("rcsb_search_invalid_request")
	}
	switch mode {
	case TermModeAll:
		return strings.Join(words, " + "), nil
	case TermModeAny:
		return strings.Join(words, " "), nil
	case TermModePhrase:
		return `"` + strings.Join(words, " ") + `"`, nil
	default:
		return "", errors.New("rcsb_search_invalid_request")
	}
}

func terminal(service string, parameters map[string]any) map[string]any {
	return map[string]any{"type": "terminal", "service": service, "parameters": parameters}
}

type searchEnvelope struct {
	TotalCount int         `json:"total_count"`
	ResultSet  []searchHit `json:"result_set"`
}

type searchHit struct {
	Identifier string  `json:"identifier"`
	Score      float64 `json:"score"`
}

type entryMetadata struct {
	Entry struct {
		ID string `json:"id"`
	} `json:"entry"`
	Struct struct {
		Title string `json:"title"`
	} `json:"struct"`
	Accession struct {
		InitialReleaseDate string `json:"initial_release_date"`
		RevisionDate       string `json:"revision_date"`
	} `json:"rcsb_accession_info"`
	Experimental []struct {
		Method string `json:"method"`
	} `json:"exptl"`
	EntryInfo struct {
		ResolutionCombined    []float64 `json:"resolution_combined"`
		PolymerEntityCount    int       `json:"polymer_entity_count"`
		NonpolymerEntityCount int       `json:"nonpolymer_entity_count"`
		PolymerComposition    string    `json:"polymer_composition"`
	} `json:"rcsb_entry_info"`
}

func parseEntryMetadata(entryID string, score float64, metadataURL string, body []byte) (Entry, error) {
	var metadata entryMetadata
	if len(body) == 0 || json.Unmarshal(body, &metadata) != nil ||
		strings.ToUpper(strings.TrimSpace(metadata.Entry.ID)) != entryID ||
		strings.TrimSpace(metadata.Struct.Title) == "" || strings.TrimSpace(metadata.Accession.InitialReleaseDate) == "" {
		return Entry{}, errors.New("rcsb_search_metadata_invalid")
	}
	methods := make([]string, 0, len(metadata.Experimental))
	seen := map[string]struct{}{}
	for _, method := range metadata.Experimental {
		value := strings.TrimSpace(method.Method)
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		methods = append(methods, value)
	}
	sort.Strings(methods)
	return Entry{
		EntryID: entryID, SearchScore: score, Title: strings.TrimSpace(metadata.Struct.Title),
		InitialReleaseDate: strings.TrimSpace(metadata.Accession.InitialReleaseDate),
		RevisionDate:       strings.TrimSpace(metadata.Accession.RevisionDate), ExperimentalMethods: methods,
		ResolutionAngstrom:    append([]float64(nil), metadata.EntryInfo.ResolutionCombined...),
		PolymerEntityCount:    metadata.EntryInfo.PolymerEntityCount,
		NonpolymerEntityCount: metadata.EntryInfo.NonpolymerEntityCount,
		PolymerComposition:    strings.TrimSpace(metadata.EntryInfo.PolymerComposition),
		StructureURL:          "https://www.rcsb.org/structure/" + entryID, MetadataURL: metadataURL,
	}, nil
}
