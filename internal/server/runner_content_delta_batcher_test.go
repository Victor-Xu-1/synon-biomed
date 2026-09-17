package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestContentDeltaPersistenceDeadlineIsTypedAsResumableInterruption(t *testing.T) {
	err := classifySessionRunnerContentDeltaPersistenceError(
		3, errors.Join(errors.New("transcript schema is unavailable"), context.DeadlineExceeded),
	)
	var interruption sessionRunnerPersistenceInterruption
	if !errors.As(err, &interruption) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline classification=%T %v", err, err)
	}
	if got := err.Error(); got != "persist model content delta batch 3: transcript schema is unavailable\ncontext deadline exceeded" {
		t.Fatalf("deadline detail=%q", got)
	}

	schemaErr := classifySessionRunnerContentDeltaPersistenceError(4, errors.New("transcript schema is unavailable"))
	if errors.As(schemaErr, &interruption) {
		t.Fatalf("deterministic schema failure was misclassified as resumable: %v", schemaErr)
	}
}

func TestSessionRunnerContentDeltaBatcherFlushesSparseTailOnTimer(t *testing.T) {
	var mu sync.Mutex
	batches := make([]string, 0, 2)
	indexes := make([]int, 0, 2)
	persisted := make(chan struct{}, 2)
	batcher := newSessionRunnerContentDeltaBatcher(40*time.Millisecond, 1024,
		func(_ context.Context, delta string, index int) error {
			mu.Lock()
			batches = append(batches, delta)
			indexes = append(indexes, index)
			mu.Unlock()
			persisted <- struct{}{}
			return nil
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := batcher.startTimer(ctx, 100*time.Millisecond, nil)
	defer stop()

	if err := batcher.append(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-persisted:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("first content delta was not flushed immediately")
	}
	if err := batcher.append(ctx, "tail"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-persisted:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("sparse content tail was not flushed by the timer")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 2 || batches[0] != "first" || batches[1] != "tail" ||
		indexes[0] != 1 || indexes[1] != 2 {
		t.Fatalf("batches=%#v indexes=%#v", batches, indexes)
	}
}

func TestSessionRunnerContentDeltaBatcherBoundsBlockedPersistence(t *testing.T) {
	var calls int
	batcher := newSessionRunnerContentDeltaBatcher(20*time.Millisecond, 1024,
		func(ctx context.Context, _ string, _ int) error {
			calls++
			if calls == 1 {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		},
	)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	timerFailure := make(chan error, 1)
	stop := batcher.startTimer(ctx, 30*time.Millisecond, func(err error) {
		timerFailure <- err
		cancel(err)
	})

	if err := batcher.append(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := batcher.append(ctx, "blocked-tail"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-timerFailure:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timer failure=%v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("blocked persistence did not fail within its operation deadline")
	}
	if err := stop(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop error=%v", err)
	}
}

func TestSessionRunnerContentDeltaBatcherFlushesFirstPacketAfterBoundary(t *testing.T) {
	batches := make([]string, 0, 2)
	batcher := newSessionRunnerContentDeltaBatcher(time.Hour, 1024,
		func(_ context.Context, delta string, _ int) error {
			batches = append(batches, delta)
			return nil
		},
	)
	ctx := context.Background()
	if err := batcher.append(ctx, "round-one"); err != nil {
		t.Fatal(err)
	}
	if err := batcher.boundary(ctx); err != nil {
		t.Fatal(err)
	}
	if err := batcher.append(ctx, "round-two"); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || batches[0] != "round-one" || batches[1] != "round-two" {
		t.Fatalf("batches=%#v", batches)
	}
}
