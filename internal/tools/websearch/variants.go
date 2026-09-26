package websearch

import (
	"context"
	"errors"
	"strings"
	"sync"
)

const maxQueryVariantsPerCall = 7

// VariantInput is one observable discovery action. QueryVariants broaden the
// same request without starting an autonomous search/read/synthesis workflow.
type VariantInput struct {
	Query                 string
	QueryVariants         []string
	AllowedDomains        []string
	BlockedDomains        []string
	MaxResults            int
	PublishedAfter        string
	PublishedBefore       string
	Sort                  string
	ProviderContinuations map[string]string
}

// SearchVariants executes explicit query variants as one deterministic,
// bounded discovery operation and merges their results by canonical URL.
func SearchVariants(ctx context.Context, input VariantInput, options Options) (Output, error) {
	queries := NormalizeQueries(input.Query, input.QueryVariants)
	if len(queries) > 1 && len(input.ProviderContinuations) > 0 {
		return Output{}, errors.New("web_search provider continuations require one query without query_variants")
	}
	limit := EffectiveResultLimit(input.MaxResults)
	outputs := make([]Output, len(queries))
	errorsByQuery := make([]error, len(queries))

	var wait sync.WaitGroup
	for index, query := range queries {
		wait.Add(1)
		go func(index int, query string) {
			defer wait.Done()
			outputs[index], errorsByQuery[index] = Search(ctx, Input{
				Query: query, AllowedDomains: input.AllowedDomains,
				BlockedDomains: input.BlockedDomains, MaxResults: limit,
				PublishedAfter: input.PublishedAfter, PublishedBefore: input.PublishedBefore,
				Sort: input.Sort, ProviderContinuations: input.ProviderContinuations,
			}, options)
		}(index, query)
	}
	wait.Wait()

	output, err := MergeVariantOutputs(queries, outputs, errorsByQuery, limit)
	if err != nil {
		return Output{}, err
	}
	if output.Diagnostics == nil {
		output.Diagnostics = map[string]any{}
	}
	output.Diagnostics["callerRequestedMaxResults"] = input.MaxResults
	output.Diagnostics["appliedMaxResults"] = limit
	output.Diagnostics["queryVariants"] = queries
	output.Diagnostics["queryVariantCount"] = len(queries)
	return output, nil
}

func NormalizeQueries(primary string, variants []string) []string {
	queries := make([]string, 0, min(maxQueryVariantsPerCall, len(variants)+1))
	seen := map[string]struct{}{}
	for _, candidate := range append([]string{primary}, variants...) {
		candidate = strings.TrimSpace(candidate)
		key := strings.ToLower(candidate)
		if len([]rune(candidate)) < 2 {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		queries = append(queries, candidate)
		if len(queries) == maxQueryVariantsPerCall {
			break
		}
	}
	return queries
}

func MergeVariantOutputs(queries []string, outputs []Output, errorsByQuery []error, limit int) (Output, error) {
	if len(queries) == 0 {
		return Output{}, errors.New("web_search requires at least one valid query")
	}
	limit = EffectiveResultLimit(limit)
	merged := Output{Query: queries[0], Diagnostics: map[string]any{}}
	if len(outputs) > 0 {
		for key, value := range outputs[0].Diagnostics {
			merged.Diagnostics[key] = value
		}
	}
	perQuery := make([]map[string]any, 0, len(queries))
	seen := map[string]struct{}{}
	for rank := 0; len(merged.Sources) < limit; rank++ {
		moreCandidates := false
		for index := range queries {
			if index >= len(outputs) || rank >= len(outputs[index].Sources) {
				continue
			}
			moreCandidates = true
			source := outputs[index].Sources[rank]
			key := canonicalURL(firstNonEmpty(source.CanonicalURL, source.URL))
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(source.URL))
			}
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			source.Rank = len(merged.Sources) + 1
			if source.Metadata == nil {
				source.Metadata = map[string]any{}
			}
			source.Metadata["matchedQueryVariant"] = queries[index]
			merged.Sources = append(merged.Sources, source)
			if len(merged.Sources) == limit {
				break
			}
		}
		if !moreCandidates {
			break
		}
	}

	var firstError error
	candidateCount := 0
	upstreamTruncated := false
	continuationsByQuery := make(map[string]any)
	for index, query := range queries {
		state := map[string]any{"query": query}
		if index < len(outputs) {
			state["returned"] = len(outputs[index].Sources)
			candidateCount += len(outputs[index].Sources)
			if truncated, _ := outputs[index].Retrieval["truncated"].(bool); truncated {
				upstreamTruncated = true
			}
			if continuations, ok := outputs[index].Retrieval["provider_continuations"].(map[string]string); ok && len(continuations) > 0 {
				state["provider_continuations"] = continuations
				continuationsByQuery[query] = continuations
			}
			state["durationSeconds"] = outputs[index].DurationSeconds
			merged.DurationSeconds = max(merged.DurationSeconds, outputs[index].DurationSeconds)
			if outputs[index].Failure != nil {
				state["unavailable"] = true
				state["failure"] = *outputs[index].Failure
			}
			if backends, found := outputs[index].Diagnostics["httpBackends"]; found {
				state["httpBackends"] = backends
			}
		}
		if index < len(errorsByQuery) && errorsByQuery[index] != nil {
			state["error"] = "query execution failed"
			if firstError == nil {
				firstError = errorsByQuery[index]
			}
		}
		perQuery = append(perQuery, state)
	}
	merged.Diagnostics["perQuery"] = perQuery
	merged.Diagnostics["returnedResults"] = len(merged.Sources)
	merged.Diagnostics["selectionMode"] = "multi_query_interleave_rank_and_canonical_url_deduplicate"
	merged.Retrieval = map[string]any{
		"scope": "multi_query_discovery_window", "query_variants": len(queries),
		"candidates": candidateCount, "returned": len(merged.Sources),
		"duplicates_or_overflow": max(0, candidateCount-len(merged.Sources)),
		"truncated":              upstreamTruncated || len(merged.Sources) >= limit,
		"exhaustive":             false, "provider_total_known": false,
	}
	if len(continuationsByQuery) > 0 {
		merged.Retrieval["query_continuations"] = continuationsByQuery
		if len(queries) == 1 {
			merged.Retrieval["provider_continuations"] = continuationsByQuery[queries[0]]
		}
	}
	if len(merged.Sources) == 0 {
		if firstError != nil {
			return Output{}, firstError
		}
		for _, output := range outputs {
			if output.Failure != nil {
				merged.Failure = output.Failure
				merged.Results = output.Results
				return merged, nil
			}
		}
		merged.Results = []any{"The completed search returned no relevant sources.", SearchResult{ToolUseID: "synon-websearch-web-search", Content: []Hit{}}}
		return merged, nil
	}
	hits := make([]Hit, 0, len(merged.Sources))
	for _, source := range merged.Sources {
		hits = append(hits, Hit{Title: source.Title, URL: source.URL, Snippet: source.Snippet})
	}
	merged.Results = []any{"Search provider: " + providerName, SearchResult{
		ToolUseID: "synon-websearch-web-search", Content: searchDisplayHits(hits),
	}}
	return merged, nil
}

func EffectiveResultLimit(requested int) int {
	return normalizeMaxResults(requested)
}

// ToolResult preserves the map-shaped unavailable envelope used by the agent
// runtime while returning successful Output values without a second encoding.
func ToolResult(output Output) any {
	if output.Failure == nil {
		return output
	}
	return map[string]any{
		"query": output.Query, "results": output.Results,
		"durationSeconds": output.DurationSeconds, "sources": []any{},
		"retrieval": output.Retrieval, "diagnostics": output.Diagnostics,
		"failure": map[string]any{
			"kind": output.Failure.Kind, "message": output.Failure.Message,
			"recoverable": output.Failure.Recoverable,
		},
	}
}
