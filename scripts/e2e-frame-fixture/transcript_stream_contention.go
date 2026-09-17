package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"synon-go/internal/sqliteutil"
)

const (
	transcriptFixtureContentionAttempts = 3
	transcriptFixtureContentionDelay    = 50 * time.Millisecond
)

// retryTranscriptFixtureContention retries only idempotent fixture writes.
// The source preview and this short-lived helper intentionally share the real
// SQLite store, so a just-finished browser case can briefly overlap the
// backend projector. A strict attempt bound and context cancellation keep the
// diagnostic fixture from hiding persistent lock ownership.
func retryTranscriptFixtureContention(ctx context.Context, operation string, fn func() error) error {
	if fn == nil {
		return errors.New("transcript fixture contention operation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 1; attempt <= transcriptFixtureContentionAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !sqliteutil.IsTransientContention(err) {
			return err
		}
		if attempt == transcriptFixtureContentionAttempts {
			return fmt.Errorf(
				"%s exhausted %d transient SQLite attempts: %w",
				operation, attempt, err,
			)
		}
		delay := transcriptFixtureContentionDelay * time.Duration(1<<(attempt-1))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("transcript fixture contention retry exhausted")
}
