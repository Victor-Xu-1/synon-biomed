package outbox

import (
	"context"
	"errors"
	"time"

	"synon-go/internal/persistence/workspace"
)

type leaseRepository interface {
	RenewOutboxClaim(context.Context, string, string, time.Duration) error
}

// deliverWithLease keeps a long operation owned without a wall-clock deadline.
// Losing its durable claim cancels execution; the deliverer must finish cleanup
// before this worker can accept more work. Domain receipts fence late results.
func (d *Dispatcher) deliverWithLease(ctx context.Context, event workspace.OutboxEvent) error {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(d.lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
			renewCtx, stop := context.WithTimeout(runCtx, d.lease/3)
			err := d.repository.(leaseRepository).RenewOutboxClaim(renewCtx, event.ID, event.ClaimToken, d.lease)
			stop()
			if err != nil {
				cancel(err)
				return
			}
		}
	}()
	err := d.deliverer.Deliver(runCtx, event)
	cause := context.Cause(runCtx)
	cancel(nil)
	<-renewDone
	if err != nil && cause != nil && !errors.Is(err, ErrDeliverySettled) {
		return errors.Join(err, cause)
	}
	return err
}
