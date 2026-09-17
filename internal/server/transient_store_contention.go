package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"synon-go/internal/sqliteutil"
)

const (
	sessionRunnerStoreContentionMaxAttempts = 4
	sessionRunnerStoreContentionRetryDelay  = 50 * time.Millisecond
)

// retryTransientStoreContention retries an idempotent persistence operation
// when another SQLite writer briefly owns the database. The attempt bound and
// context cancellation prevent a lock holder from turning recovery into an
// unbounded stall; callers must still surface the final causal error.
func retryTransientStoreContention(ctx context.Context, operation string, fn func() error) error {
	if fn == nil {
		return errors.New("transient store operation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		operation = "workspace_write"
	}
	for attempt := 1; attempt <= sessionRunnerStoreContentionMaxAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !isTransientSQLiteContention(err) {
			return err
		}
		if attempt == sessionRunnerStoreContentionMaxAttempts {
			return fmt.Errorf("%s exhausted %d transient store attempts: %w", operation, attempt, err)
		}
		delay := sessionRunnerStoreContentionRetryDelay * time.Duration(1<<(attempt-1))
		log.Printf(
			"session_runner_store_contention_retry operation=%s attempt=%d next_delay_ms=%d error=%v",
			operation, attempt, delay.Milliseconds(), err,
		)
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
	return errors.New("transient store retry exhausted")
}

func isTransientSQLiteContention(err error) bool {
	return sqliteutil.IsTransientContention(err)
}

func isTransientSQLiteContentionMessage(message string) bool {
	return sqliteutil.IsTransientContention(errors.New(message))
}
