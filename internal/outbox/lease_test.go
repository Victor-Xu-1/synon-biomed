package outbox

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/persistence/workspace"
)

type leaseTestDeliverer struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (d *leaseTestDeliverer) Deliver(ctx context.Context, _ workspace.OutboxEvent) error {
	if _, ok := ctx.Deadline(); ok {
		return context.DeadlineExceeded
	}
	if d.calls.Add(1) == 1 {
		close(d.started)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.release:
		return nil
	}
}

func TestLongRunningDispatcherRenewsLeaseWithoutConcurrentDelivery(t *testing.T) {
	store := openStoreAt(t, filepath.Join(t.TempDir(), "workspace.sqlite"))
	event := enqueueEvent(t, store, 1, 8)
	delivery := &leaseTestDeliverer{started: make(chan struct{}), release: make(chan struct{})}
	dispatcher, err := NewDispatcher(store, delivery, Options{WorkerID: "long-worker", BatchSize: 1, Lease: 180 * time.Millisecond, LongRunning: true, PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("dispatcher did not stop")
		}
	}()
	select {
	case <-delivery.started:
	case <-time.After(time.Second):
		t.Fatal("delivery did not start")
	}
	time.Sleep(500 * time.Millisecond)
	claimed, err := store.ClaimOutbox(ctx, workspace.ClaimOutboxInput{WorkerID: "competitor", Topics: []string{event.Topic}, Limit: 1, Lease: time.Second})
	if err != nil || len(claimed) != 0 {
		t.Fatalf("live operation stolen: %#v %v", claimed, err)
	}
	close(delivery.release)
	deadline := time.Now().Add(time.Second)
	for {
		current, err := store.GetOutboxEvent(ctx, event.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == workspace.OutboxStatusDelivered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delivery did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if delivery.calls.Load() != 1 {
		t.Fatalf("duplicate executions=%d", delivery.calls.Load())
	}
}

func TestLongRunningDispatcherCancelsExecutionOnClaimLoss(t *testing.T) {
	store := openStoreAt(t, filepath.Join(t.TempDir(), "workspace.sqlite"))
	event := enqueueEvent(t, store, 1, 8)
	delivery := &leaseTestDeliverer{started: make(chan struct{}), release: make(chan struct{})}
	dispatcher, err := NewDispatcher(store, delivery, Options{WorkerID: "long-worker", BatchSize: 1, Lease: 90 * time.Millisecond, LongRunning: true})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimOutbox(context.Background(), workspace.ClaimOutboxInput{WorkerID: "owner", Topics: []string{event.Topic}, Limit: 1, Lease: time.Second})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v %v", claimed, err)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- dispatcher.deliverWithLease(ctx, claimed[0]) }()
	select {
	case <-delivery.started:
	case <-time.After(time.Second):
		t.Fatal("delivery did not start")
	}
	if err := store.AckOutbox(ctx, event.ID, claimed[0].ClaimToken); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("claim loss did not cancel execution")
		}
	case <-time.After(time.Second):
		t.Fatal("execution continued after losing claim")
	}
}
