package workspace

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestKernelControlRenewalChecksExpiryAfterWaitingForConnection(t *testing.T) {
	testKernelControlClockAfterWait(t, false)
}

func TestKernelControlAcquisitionChecksExpiryAfterWaitingForConnection(t *testing.T) {
	testKernelControlClockAfterWait(t, true)
}

func testKernelControlClockAfterWait(t *testing.T, acquire bool) {
	t.Helper()
	store, backend, lease, _ := newAcceptedDetachedKernelExecutionFixture(t)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	store.now = func() time.Time { return time.Unix(0, clock.Load()) }
	store.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	waits := store.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		if acquire {
			_, _, err := store.AcquireKernelExecutionBackendControl(ctx, AcquireKernelExecutionBackendControlInput{
				BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration, Token: lease.Token,
				LeaseExpiresAt: lease.ExpiresAt.Add(time.Minute),
			})
			done <- err
			return
		}
		_, _, err := store.RenewKernelExecutionBackendControl(ctx, RenewKernelExecutionBackendControlInput{
			BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
			ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, LeaseExpiresAt: lease.ExpiresAt.Add(time.Minute),
		})
		done <- err
	}()
	for store.db.Stats().WaitCount == waits {
		select {
		case <-ctx.Done():
			t.Fatal("renewal did not wait for the database")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	clock.Store(lease.ExpiresAt.Add(time.Second).UnixNano())
	if acquire {
		clock.Store(lease.ExpiresAt.Add(2 * time.Minute).UnixNano())
	}
	_ = held.Close()
	if err := <-done; !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("expired controller was renewed: %v", err)
	}
}
