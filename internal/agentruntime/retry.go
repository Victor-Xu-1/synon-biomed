package agentruntime

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
	modelRetryBaseDelay = 800 * time.Millisecond
	modelRetryMaxDelay  = 6 * time.Second
	modelRetryAfterCap  = 30 * time.Second
)

type modelRetryError struct {
	err        error
	retryAfter time.Duration
}

func (e *modelRetryError) Error() string { return e.err.Error() }
func (e *modelRetryError) Unwrap() error { return e.err }

func withModelRetryAfter(err error, header http.Header, now time.Time) error {
	if err == nil {
		return nil
	}
	return &modelRetryError{err: err, retryAfter: parseModelRetryAfter(header.Get("Retry-After"), now)}
}

func parseModelRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return min(time.Duration(seconds)*time.Second, modelRetryAfterCap)
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := when.Sub(now)
	if delay <= 0 {
		return 0
	}
	return min(delay, modelRetryAfterCap)
}

func modelRetryDelay(attempt int, err error) time.Duration {
	var retryErr *modelRetryError
	if errors.As(err, &retryErr) && retryErr.retryAfter > 0 {
		return retryErr.retryAfter
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := modelRetryBaseDelay
	for step := 1; step < attempt && delay < modelRetryMaxDelay; step++ {
		delay *= 2
	}
	delay = min(delay, modelRetryMaxDelay)
	half := delay / 2
	if half <= 0 {
		return delay
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func waitModelRetry(ctx context.Context, attempt int, err error) error {
	timer := time.NewTimer(modelRetryDelay(attempt, err))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
