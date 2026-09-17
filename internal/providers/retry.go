package providers

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	providerRetryBaseDelay = 800 * time.Millisecond
	providerRetryMaxDelay  = 6 * time.Second
	providerRetryAfterCap  = 30 * time.Second
)

type providerRetryError struct {
	err        error
	retryAfter time.Duration
}

func (e *providerRetryError) Error() string { return e.err.Error() }
func (e *providerRetryError) Unwrap() error { return e.err }

func withProviderRetryAfter(err error, header http.Header, now time.Time) error {
	if err == nil {
		return nil
	}
	return &providerRetryError{err: err, retryAfter: parseProviderRetryAfter(header.Get("Retry-After"), now)}
}

func parseProviderRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return min(time.Duration(seconds)*time.Second, providerRetryAfterCap)
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := when.Sub(now)
	if delay <= 0 {
		return 0
	}
	return min(delay, providerRetryAfterCap)
}

func providerRetryDelay(attempt int, err error) time.Duration {
	var retryErr *providerRetryError
	if errors.As(err, &retryErr) && retryErr.retryAfter > 0 {
		return retryErr.retryAfter
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := providerRetryBaseDelay
	for step := 1; step < attempt && delay < providerRetryMaxDelay; step++ {
		delay *= 2
	}
	delay = min(delay, providerRetryMaxDelay)
	// Half-to-full jitter avoids synchronized retries while keeping latency bounded.
	half := delay / 2
	if half <= 0 {
		return delay
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func waitProviderRetry(ctx context.Context, attempt int, err error) error {
	timer := time.NewTimer(providerRetryDelay(attempt, err))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// providerHTTPStatusRetryable distinguishes transient throttling from a provider account that has no remaining quota.
func providerHTTPStatusRetryable(status int, body []byte) bool {
	if status == http.StatusTooManyRequests && providerQuotaExhausted(body) {
		return false
	}
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}
func providerQuotaExhausted(body []byte) bool {
	normalized := strings.ToLower(strings.TrimSpace(string(body)))
	if normalized == "" {
		return false
	}
	for _, marker := range []string{"accountquotaexceeded", "quota exceeded", "quota exhausted", "usage quota", "insufficient_quota", "billing quota", "resource exhausted"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
