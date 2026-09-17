package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"synon-go/internal/tools/articlefulltext"
	"synon-go/internal/tools/bindingmode"
	"synon-go/internal/tools/patentsearch"
	"synon-go/internal/tools/webfetch"
	"synon-go/internal/tools/websearch"
)

type Field struct {
	Type     string         `json:"type"`
	Required bool           `json:"required"`
	Schema   map[string]any `json:"schema,omitempty"`
}

type Tool struct {
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Capabilities []string         `json:"capabilities"`
	Input        map[string]Field `json:"input"`
	Executable   bool             `json:"executable"`
	// Exposure is part of the registered runtime contract. Consumers may project
	// a deferred Tool after an explicit Skill selection, but they may never
	// promote a hidden service or compatibility operation into a model turn.
	Exposure ToolExposure `json:"exposure"`
}

type Registry struct {
	tools                 map[string]Tool
	serviceOperations     *Registry
	articleFulltextClient *articlefulltext.Client
	bindingModeClient     *bindingmode.Client
	patentSearchClient    *patentsearch.Client
	webFetchOptions       webfetch.Options
	webSearchOptions      websearch.Options
}

type WebFetchResult = webfetch.Result
type WebSearchResult = websearch.Output

// SetWebOptions configures controlled transports on an already partitioned
// catalog without rebuilding or recombining its authority boundary.
func (r *Registry) SetWebOptions(fetchOptions webfetch.Options, searchOptions websearch.Options) {
	if r == nil {
		return
	}
	r.webFetchOptions = fetchOptions
	r.webSearchOptions = searchOptions
	// Patent discovery and record reading share the same operator-selected
	// network route as web_search/web_fetch. A separate default HTTP client here
	// would make the general web tools succeed while patent sources time out.
	r.patentSearchClient = patentsearch.NewClient(patentsearch.Options{
		SearchOptions: searchOptions,
		FetchOptions:  fetchOptions,
	})
}

func New(tools []Tool) *Registry {
	registry, err := Build(tools)
	if err != nil {
		panic(err)
	}
	return registry
}

func (r *Registry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	if !ok && r.serviceOperations != nil {
		return r.serviceOperations.Get(name)
	}
	return tool, ok
}

// AttachServiceOperations gives a model catalog an explicit resolver for
// non-model contracts. Names still enumerates only model tools, so service
// operations cannot leak into prompts or /api/tools.
func (r *Registry) AttachServiceOperations(operations *Registry) {
	if r == nil {
		return
	}
	r.serviceOperations = operations
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AllNames is reserved for governance and compatibility audits that must
// inspect both disjoint catalogs. Model snapshots and /api/tools must use
// Names, which intentionally excludes service operations.
func (r *Registry) AllNames() []string {
	seen := map[string]struct{}{}
	for _, name := range r.Names() {
		seen[name] = struct{}{}
	}
	if r.serviceOperations != nil {
		for _, name := range r.serviceOperations.Names() {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Validate(name string, input map[string]any) error {
	tool, ok := r.Get(name)
	if !ok {
		return fmt.Errorf("unknown tool: %s", name)
	}
	for fieldName, field := range tool.Input {
		value, exists := input[fieldName]
		if field.Required && !exists {
			return fmt.Errorf("%s.%s is required", name, fieldName)
		}
		if !exists || value == nil {
			continue
		}
		if err := validateFieldType(name, fieldName, field.Type, value); err != nil {
			return err
		}
		if err := validateFieldSchema(name, fieldName, field.Schema, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) Execute(ctx context.Context, name string, input map[string]any) (any, error) {
	tool, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	if !tool.Executable {
		return nil, fmt.Errorf("%s is registered but must be executed through its service API", name)
	}
	if err := r.Validate(name, input); err != nil {
		return nil, err
	}

	switch name {
	case "web_fetch":
		url := input["url"].(string)
		limit := int64(0)
		if rawLimit, ok := input["limit"]; ok {
			limit = numericLimit(rawLimit)
		}
		result, err := webfetch.FetchWithOptions(ctx, url, limit, r.webFetchOptions)
		if err != nil {
			return nil, err
		}
		if message := webFetchNonEvidenceMessage(url, result.Body, result.ContentType); message != "" {
			result.SourceUnavailable = true
			result.Error = message
			result.Body = webFetchSourceUnavailableText(message)
		} else if result.SourceUnavailable && strings.TrimSpace(result.Body) == "" {
			result.Body = webFetchSourceUnavailableText(result.Error)
		}
		return result, nil
	case "web_search":
		searchOptions := r.webSearchOptions
		if strings.TrimSpace(searchOptions.Root) == "" {
			searchOptions.Root = "."
		}
		continuations, err := strictStringMap(input["provider_continuations"])
		if err != nil {
			return nil, err
		}
		output, err := websearch.SearchVariants(ctx, websearch.VariantInput{
			Query:           input["query"].(string),
			QueryVariants:   stringArray(input["query_variants"]),
			AllowedDomains:  stringArray(input["allowed_domains"]),
			BlockedDomains:  stringArray(input["blocked_domains"]),
			MaxResults:      int(numericLimit(input["max_results"])),
			PublishedAfter:  optionalString(input["published_after"]),
			PublishedBefore: optionalString(input["published_before"]),
			Sort:            optionalString(input["sort"]), ProviderContinuations: continuations,
		}, searchOptions)
		if err != nil {
			return nil, err
		}
		return websearch.ToolResult(output), nil
	case "fetch_article_fulltext":
		client := r.articleFulltextClient
		if client == nil {
			client = articlefulltext.NewClient(articlefulltext.Options{})
		}
		return client.Fetch(ctx, articlefulltext.Input{
			DOI:      optionalString(input["doi"]),
			PMCID:    optionalString(input["pmcid"]),
			MaxBytes: numericLimit(input["max_bytes"]),
		})
	case "patent_search":
		client := r.patentSearchClient
		if client == nil {
			client = patentsearch.NewClient(patentsearch.Options{})
		}
		return client.Run(ctx, patentsearch.Input{
			Operation:         optionalString(input["operation"]),
			Query:             optionalString(input["query"]),
			QueryVariants:     stringArray(input["query_variants"]),
			PublicationNumber: optionalString(input["publication_number"]),
			Sources:           stringArray(input["sources"]),
			MaxResults:        int(numericLimit(input["max_results"])),
			FocusTerms:        stringArray(input["focus_terms"]),
			MaxSectionChars:   int(numericLimit(input["max_section_chars"])),
		})
	case "binding_mode_analysis":
		client := r.bindingModeClient
		if client == nil {
			client = bindingmode.NewClient(bindingmode.Options{})
		}
		pdbID := bindingModePDBID(input)
		ligand := optionalString(input["ligand_resname"])
		if ligand == "" {
			ligand = optionalString(input["ligand"])
		}
		return client.Analyze(ctx, bindingmode.Input{
			PDBID: pdbID, LigandResidue: ligand, Chain: optionalString(input["chain"]),
			MaxContacts: int(numericLimit(input["max_contacts"])), CutoffAngstrom: numericFloat(input["cutoff_angstrom"]),
		})
	case "read_memory", "write_memory", "search_memory":
		return nil, fmt.Errorf("%s requires a trusted agent session", name)
	default:
		return nil, fmt.Errorf("%s is registered for server dispatcher execution; standalone registry executor does not implement it", name)
	}
}

func optionalString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func bindingModePDBID(input map[string]any) string {
	for _, key := range []string{"pdbId", "pdb_id", "structure_id"} {
		candidate := strings.TrimSpace(optionalString(input[key]))
		if isPDBID(candidate) {
			return strings.ToUpper(candidate)
		}
	}
	return ""
}

func isPDBID(candidate string) bool {
	if len(candidate) != 4 || candidate[0] < '0' || candidate[0] > '9' {
		return false
	}
	for _, char := range candidate[1:] {
		if char < '0' || char > '9' {
			if char < 'A' || char > 'Z' {
				if char < 'a' || char > 'z' {
					return false
				}
			}
		}
	}
	return true
}

func webFetchNonEvidenceMessage(sourceURL string, body string, contentType string) string {
	if isWebFetchSearchResultURL(sourceURL) {
		return "Search result pages are discovery surfaces, not source evidence."
	} else if _, detectedMessage := detectWebFetchNonEvidenceContent(body, contentType); detectedMessage != "" {
		return detectedMessage
	}
	return ""
}

func isWebFetchSearchResultURL(sourceURL string) bool {
	lower := strings.ToLower(sourceURL)
	return strings.Contains(lower, "google.") && strings.Contains(lower, "/search?") ||
		strings.Contains(lower, "bing.com/search?") ||
		strings.Contains(lower, "duckduckgo.com/") && strings.Contains(lower, "?q=")
}

func detectWebFetchNonEvidenceContent(body string, contentType string) (string, string) {
	lower := strings.ToLower(body)
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		if strings.Contains(lower, "bing search results page") ||
			strings.Contains(lower, "google search") && strings.Contains(lower, "all images") ||
			strings.Contains(lower, "all images videos news maps") {
			return "Non Evidence Page", "Fetched page is a search results page, not source evidence."
		}
	}
	if strings.Contains(lower, "captcha") || strings.Contains(lower, "verify you are human") || strings.Contains(lower, "bot-check") {
		return "Non Evidence Page", "Fetched page is a CAPTCHA or bot-check gate, not source evidence."
	}
	if strings.Contains(lower, "sign in to continue") || strings.Contains(lower, "login required") || strings.Contains(lower, "institutional login required") {
		return "Non Evidence Page", "Fetched page is a login or access gate, not source evidence."
	}
	return "", ""
}

func webFetchSourceUnavailableText(message string) string {
	return "SOURCE UNAVAILABLE\n\n" +
		message + "\n\n" +
		"This page must not be used as source evidence. Search result pages, CAPTCHA gates, and login gates are discovery or access surfaces, not verified sources.\n\n" +
		"Next step: read the actual source page, fetch another verified source, or cite that the source is unavailable."
}

func stringArray(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func strictStringMap(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	result := map[string]string{}
	switch typed := value.(type) {
	case map[string]any:
		for key, raw := range typed {
			text, ok := raw.(string)
			if !ok {
				return nil, errors.New("web_search.provider_continuations values must be strings")
			}
			result[key] = text
		}
	case map[string]string:
		for key, text := range typed {
			result[key] = text
		}
	default:
		return nil, errors.New("web_search.provider_continuations must be an object")
	}
	if len(result) > 8 {
		return nil, errors.New("web_search.provider_continuations supports at most 8 providers")
	}
	return result, nil
}

func numericLimit(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func numericFloat(value any) float64 {
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
