package workspace

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTaskOperationStartIsFencedAndSeparateFromQueueClaims(t *testing.T) {
	store, operation := taskOperationFixture(t)
	ctx := context.Background()
	if _, err := store.EnqueueTaskOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	claim := func() OutboxEvent {
		events, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "worker", Topics: []string{TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
		if err != nil || len(events) != 1 {
			t.Fatalf("claim=%v %v", events, err)
		}
		return events[0]
	}
	first := claim()
	if _, err := store.RetryOutbox(ctx, first.ID, first.ClaimToken, "not dispatched", 0); err != nil {
		t.Fatal(err)
	}
	second := claim()
	if started, cancelled, err := store.BeginTaskOperation(ctx, second); err != nil || started || cancelled {
		t.Fatalf("first start=%v %v %v", started, cancelled, err)
	}
	if _, _, err := store.BeginTaskOperation(ctx, second); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("same claim dispatched twice: %v", err)
	}
	if _, err := store.RetryOutbox(ctx, second.ID, second.ClaimToken, "interrupted after start", 0); err != nil {
		t.Fatal(err)
	}
	third := claim()
	if started, cancelled, err := store.BeginTaskOperation(ctx, third); err != nil || !started || cancelled {
		t.Fatalf("resumed start=%v %v %v", started, cancelled, err)
	}
	if _, _, err := store.BeginTaskOperation(ctx, second); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("stale claim=%v", err)
	}
}

func TestTaskOperationConcurrentStartHasOneWinner(t *testing.T) {
	store, operation := taskOperationFixture(t)
	ctx := context.Background()
	if _, err := store.EnqueueTaskOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	events, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "worker", Topics: []string{TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
	if err != nil || len(events) != 1 {
		t.Fatalf("claim=%v %v", events, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := store.BeginTaskOperation(ctx, events[0]); results <- err }()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrOutboxClaimLost) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("execution owners=%d", winners)
	}
}
