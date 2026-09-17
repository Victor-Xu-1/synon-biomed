package websearch

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

const MaxProviderContinuationBytes = 4096

type backendRequestState struct {
	PublishedFilterApplied bool
	SortApplied            bool
	ContinuationApplied    bool
	Unsupported            []string
}

func (state backendRequestState) mapValue() map[string]any {
	return map[string]any{
		"publishedFilterApplied": state.PublishedFilterApplied,
		"sortApplied":            state.SortApplied,
		"continuationApplied":    state.ContinuationApplied,
		"unsupported":            append([]string(nil), state.Unsupported...),
	}
}

func validateSearchConstraints(input Input) error {
	after, before := strings.TrimSpace(input.PublishedAfter), strings.TrimSpace(input.PublishedBefore)
	for name, value := range map[string]string{"published_after": after, "published_before": before} {
		if value == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return fmt.Errorf("WebSearch.%s must use YYYY-MM-DD", name)
		}
	}
	if after != "" && before != "" && after > before {
		return errors.New("WebSearch.published_after must not be later than published_before")
	}
	switch strings.TrimSpace(input.Sort) {
	case "", "relevance", "published_desc":
	default:
		return errors.New("WebSearch.sort must be relevance or published_desc")
	}
	if len(input.ProviderContinuations) > 8 {
		return errors.New("WebSearch.provider_continuations supports at most 8 providers")
	}
	for provider, cursor := range input.ProviderContinuations {
		if strings.TrimSpace(provider) == "" || len(provider) > 100 || strings.TrimSpace(cursor) == "" || len(cursor) > MaxProviderContinuationBytes {
			return errors.New("WebSearch.provider_continuations contains an invalid provider or cursor")
		}
	}
	return nil
}

func searchHTTPBackendTarget(input Input, backend httpSearchBackend, limit int) (string, backendRequestState, error) {
	state := backendRequestState{}
	if err := validateSearchConstraints(input); err != nil {
		return "", state, err
	}
	target := searchHTTPEndpointForLimit(strings.TrimSpace(input.Query), backend.Endpoint, limit)
	parsed, err := url.Parse(target)
	if err != nil {
		return "", state, fmt.Errorf("parse search backend URL: %w", err)
	}
	values := parsed.Query()
	after, before := strings.TrimSpace(input.PublishedAfter), strings.TrimSpace(input.PublishedBefore)
	sortMode := strings.TrimSpace(input.Sort)
	continuation := providerContinuation(input.ProviderContinuations, backend.Name)

	switch strings.TrimSpace(backend.Format) {
	case searchFormatCrossref:
		filters := splitSearchFilters(values.Get("filter"))
		if after != "" {
			filters = append(filters, "from-pub-date:"+after)
			state.PublishedFilterApplied = true
		}
		if before != "" {
			filters = append(filters, "until-pub-date:"+before)
			state.PublishedFilterApplied = true
		}
		if len(filters) > 0 {
			values.Set("filter", strings.Join(uniqueStringsStable(filters), ","))
		}
		if sortMode == "published_desc" {
			values.Set("sort", "published")
			values.Set("order", "desc")
			state.SortApplied = true
		}
		if continuation == "" {
			continuation = "*"
		} else {
			state.ContinuationApplied = true
		}
		values.Set("cursor", continuation)
		values.Set("rows", fmt.Sprintf("%d", normalizeMaxResults(limit)))
	case searchFormatEuropePMC:
		query := strings.TrimSpace(input.Query)
		if after != "" && before != "" {
			query = fmt.Sprintf("(%s) AND FIRST_PDATE:[%s TO %s]", query, after, before)
			state.PublishedFilterApplied = true
		} else if after != "" || before != "" {
			state.Unsupported = append(state.Unsupported, "open_ended_published_filter")
		}
		if sortMode == "published_desc" {
			query += " sort_date:y"
			state.SortApplied = true
		}
		values.Set("query", query)
		if continuation == "" {
			continuation = "*"
		} else {
			state.ContinuationApplied = true
		}
		values.Set("cursorMark", continuation)
		values.Set("pageSize", fmt.Sprintf("%d", normalizeMaxResults(limit)))
	default:
		if after != "" || before != "" {
			state.Unsupported = append(state.Unsupported, "published_filter")
		}
		if sortMode == "published_desc" {
			state.Unsupported = append(state.Unsupported, "published_sort")
		}
		if continuation != "" {
			state.Unsupported = append(state.Unsupported, "continuation")
		}
	}
	sort.Strings(state.Unsupported)
	parsed.RawQuery = values.Encode()
	return parsed.String(), state, nil
}

func providerContinuation(values map[string]string, provider string) string {
	provider = strings.TrimSpace(provider)
	for key, value := range values {
		if strings.EqualFold(strings.TrimSpace(key), provider) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitSearchFilters(value string) []string {
	result := []string{}
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func uniqueStringsStable(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, strings.TrimSpace(value))
	}
	return result
}
