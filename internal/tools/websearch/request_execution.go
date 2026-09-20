package websearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"synon-go/internal/buildinfo"
	"synon-go/internal/httpreliability"
	"synon-go/internal/httptext"
)

func searchOneHTTPBackend(ctx context.Context, input Input, limit int, options Options, index int, backend httpSearchBackend,
	clientForURL func(context.Context, string) (*http.Client, error)) httpSearchBackendResult {
	maximum := options.MaxAttempts
	if maximum <= 0 {
		maximum = httpreliability.DefaultMaxAttempts
	}
	var receipts []httpreliability.Receipt
	var result httpSearchBackendResult
	for number := 1; number <= maximum; number++ {
		started := time.Now()
		result = searchHTTPBackendAttempt(ctx, input, limit, options, index, backend, clientForURL)
		receipts = append(receipts, httpreliability.Receipt{Attempt: number, Outcome: result.Outcome, StatusCode: result.StatusCode,
			DurationMS: time.Since(started).Milliseconds(), RetryAfterSeconds: result.RetryAfter.Seconds()})
		result.Attempts = append([]httpreliability.Receipt(nil), receipts...)
		if result.Err == nil || number == maximum || ctx.Err() != nil {
			return result
		}
		retryable := httpreliability.TransientError(result.Err)
		if result.Outcome == "http_error" {
			retryable = httpreliability.TransientStatus(result.StatusCode)
		}
		if !retryable {
			return result
		}
		if err := httpreliability.Wait(ctx, max(result.RetryAfter, httpreliability.Backoff(number-1))); err != nil {
			result.Err, result.Outcome = err, httpreliability.ErrorKind(err)
			return result
		}
	}
	return result
}

func searchHTTPBackendAttempt(ctx context.Context, input Input, maxResults int, options Options, index int, backend httpSearchBackend,
	clientForURL func(context.Context, string) (*http.Client, error)) httpSearchBackendResult {
	result := httpSearchBackendResult{Index: index, Name: backend.Name}
	limit := httpBackendRequestLimit(backend, maxResults)
	result.RequestedLimit = limit
	target, state, err := searchHTTPBackendTarget(input, backend, limit)
	result.RequestState = state
	if err != nil {
		result.Err, result.Outcome = err, "request_invalid"
		return result
	}
	attempt := httpreliability.Begin(ctx, options.HeaderTimeout)
	defer attempt.Close()
	client, err := clientForURL(attempt.Context, target)
	if err != nil {
		result.Err = attempt.Error(err)
		result.Outcome = httpreliability.ErrorKind(result.Err)
		return result
	}
	request, err := http.NewRequestWithContext(attempt.Context, http.MethodGet, target, nil)
	if err != nil {
		result.Err, result.Outcome = err, "request_invalid"
		return result
	}
	identity := buildinfo.Release()
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36 "+identity.MachineSlug+"-web-search/"+identity.Version)
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	switch strings.TrimSpace(backend.Format) {
	case searchFormatMediaWikiOpenSearch, searchFormatCrossref, searchFormatEuropePMC:
		request.Header.Set("Accept", "application/json")
	default:
		request.Header.Set("Accept", "text/html,application/xhtml+xml")
	}
	response, err := client.Do(request)
	if err != nil {
		result.Err = attempt.Error(err)
		result.Outcome = httpreliability.ErrorKind(result.Err)
		return result
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	result.RetryAfter = httpreliability.RetryAfter(response.Header.Get("Retry-After"), time.Now())
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Err, result.Outcome = fmt.Errorf("HTTP status %d", response.StatusCode), "http_error"
		return result
	}
	body, err := attempt.Body(response.Body, options.ReadIdleTimeout)
	if err != nil {
		result.Err, result.Outcome = err, httpreliability.ErrorKind(err)
		return result
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, maxHTTPResponseBytes+1))
	if err != nil {
		result.Err = attempt.Error(err)
		result.Outcome = httpreliability.ErrorKind(result.Err)
		return result
	}
	if len(raw) > maxHTTPResponseBytes {
		result.Err, result.Outcome = errors.New("search response exceeds bounded reader size"), "response_too_large"
		return result
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" && (backend.Format == "" || backend.Format == searchFormatHTML) {
		contentType = "text/html"
	}
	decoded, err := httptext.Decode(raw, contentType, false)
	if err != nil {
		result.Err, result.Outcome = fmt.Errorf("decode search response: %w", err), "parse_error"
		return result
	}
	parsed, err := parseHTTPBackendResponse(backend, []byte(decoded))
	if err != nil {
		result.Err, result.Outcome = err, "parse_error"
		return result
	}
	result.RawResults, result.TotalResults, result.TotalKnown, result.NextCursor = len(parsed.Results), parsed.TotalResults, parsed.TotalKnown, parsed.NextCursor
	selection := input
	if len(backend.BlockedResultDomains) > 0 {
		selection.BlockedDomains = append(append([]string(nil), input.BlockedDomains...), backend.BlockedResultDomains...)
	}
	result.Hits = selectRelevantHits(parsed.Results, selection, limit)
	result.Outcome = "completed"
	if len(result.Hits) == 0 {
		result.Outcome = "empty"
		if len(parsed.Results) > 0 {
			result.Outcome = "filtered"
		}
	}
	return result
}
