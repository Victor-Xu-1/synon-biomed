package main

import (
	"context"
	"errors"
	"testing"
)

func TestRetryTranscriptFixtureContentionRecoversOnlyTransientLocks(t *testing.T) {
	calls := 0
	err := retryTranscriptFixtureContention(context.Background(), "append_fixture_event", func() error {
		calls++
		if calls < 3 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}

	sentinel := errors.New("invalid fixture identity")
	calls = 0
	err = retryTranscriptFixtureContention(context.Background(), "append_fixture_event", func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("non-transient calls=%d err=%v", calls, err)
	}
}

func TestRetryTranscriptFixtureContentionHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retryTranscriptFixtureContention(ctx, "append_fixture_event", func() error {
		calls++
		cancel()
		return errors.New("SQLITE_BUSY")
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
