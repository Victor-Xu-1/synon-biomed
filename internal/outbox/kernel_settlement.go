package outbox

import (
	"context"
	"errors"
	"fmt"

	workspace "synon-go/internal/persistence/workspace"
)

type KernelSettlementRepository interface {
	DeliverKernelBackgroundSettlement(context.Context, workspace.OutboxEvent) error
}

// KernelSettlementDeliverer advances a previously staged kernel outcome to its
// durable terminal state. The workspace transaction remains the authority for
// idempotency, terminal fencing, notifications, and realtime projection.
type KernelSettlementDeliverer struct {
	Store       KernelSettlementRepository
	OnDelivered func(context.Context, workspace.OutboxEvent) error
}

func (d KernelSettlementDeliverer) Deliver(ctx context.Context, event workspace.OutboxEvent) error {
	if d.Store == nil {
		return errors.New("kernel settlement outbox store is required")
	}
	if event.Topic != workspace.KernelResultSettlementOutboxTopic {
		return fmt.Errorf("unexpected kernel settlement outbox topic %q", event.Topic)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.Store.DeliverKernelBackgroundSettlement(ctx, event); err != nil {
		return err
	}
	if d.OnDelivered != nil {
		return d.OnDelivered(ctx, event)
	}
	return nil
}
