package webfetch

import (
	"context"
	"errors"
	"net/http"
	"time"

	"synon-go/internal/httpreliability"
	"synon-go/internal/mcpdirectory"
)

func fetchWebResponse(ctx context.Context, target string, options Options,
	clientForURL func(context.Context, string) (*http.Client, error),
) (*http.Response, []httpreliability.Receipt, error) {
	maximum := options.MaxAttempts
	if maximum <= 0 {
		maximum = httpreliability.DefaultMaxAttempts
	}
	headerTimeout := options.HeaderTimeout
	if headerTimeout <= 0 {
		headerTimeout = httpreliability.DefaultHeaderTimeout
	}
	var receipts []httpreliability.Receipt
	for number := 1; number <= maximum; number++ {
		started := time.Now()
		attempt := httpreliability.Begin(ctx, headerTimeout)
		response, err := doWebFetchRequest(attempt, target, options.ReadIdleTimeout, clientForURL)
		receipt := httpreliability.Receipt{Attempt: number, DurationMS: time.Since(started).Milliseconds()}
		if err != nil {
			receipt.Outcome = httpreliability.ErrorKind(err)
			receipts = append(receipts, receipt)
			attempt.Close()
			if ctx.Err() != nil {
				return nil, receipts, ctx.Err()
			}
			if number == maximum || (!httpreliability.TransientError(err) && !mcpdirectory.IsPublicDestinationUnavailable(err)) {
				return nil, receipts, err
			}
			if err := httpreliability.Wait(ctx, httpreliability.Backoff(number-1)); err != nil {
				return nil, receipts, err
			}
			continue
		}
		receipt.StatusCode = response.StatusCode
		receipt.Outcome = "response_received"
		delay := httpreliability.RetryAfter(response.Header.Get("Retry-After"), time.Now())
		receipt.RetryAfterSeconds = delay.Seconds()
		if response.StatusCode >= 400 {
			receipt.Outcome = "http_error"
		}
		receipts = append(receipts, receipt)
		if !httpreliability.TransientStatus(response.StatusCode) || number == maximum || delay > headerTimeout {
			// Long Retry-After advice is returned to the caller, not shortened
			// into an early retry or held as an unbounded inline sleep.
			return response, receipts, nil
		}
		_ = response.Body.Close()
		if err := httpreliability.Wait(ctx, max(delay, httpreliability.Backoff(number-1))); err != nil {
			return nil, receipts, err
		}
	}
	return nil, receipts, errors.New("HTTP attempt policy did not execute a request")
}

func doWebFetchRequest(attempt *httpreliability.Attempt, target string, idle time.Duration,
	clientForURL func(context.Context, string) (*http.Client, error),
) (*http.Response, error) {
	client, err := clientForURL(attempt.Context, target)
	if err != nil {
		return nil, attempt.Error(err)
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(attempt.Context, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	response, err := clone.Do(request)
	if err != nil {
		return nil, attempt.Error(err)
	}
	response.Body, err = attempt.Body(response.Body, idle)
	if err != nil {
		return nil, err
	}
	return response, nil
}
