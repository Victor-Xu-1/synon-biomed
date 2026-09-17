package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

// RealtimeFanout receives only events that already exist in realtime_events.
// It may complete idempotent derived projections before ephemeral fanout; it
// must never create or mutate the underlying business authority.
type RealtimeFanout interface {
	FanoutRealtimeOutbox(workspace.RealtimeEvent, *workspace.RealtimeFrameProjection) error
}

type RealtimeDeliverer struct {
	Store  *workspace.Store
	Fanout RealtimeFanout
}

func (d RealtimeDeliverer) Deliver(ctx context.Context, event workspace.OutboxEvent) error {
	if d.Store == nil {
		return errors.New("realtime outbox store is required")
	}
	if d.Fanout == nil {
		return errors.New("realtime outbox fanout is required")
	}
	envelope, err := workspace.DecodeRealtimeOutboxEnvelope(event)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if envelope.Event.OccurredAt.IsZero() {
		envelope.Event.OccurredAt = event.OccurredAt
	}
	var projection *workspace.RealtimeFrameProjection
	if envelope.FrameEventID != "" {
		captured, found, err := d.Store.FindRealtimeFrameProjectionForOutbox(ctx, envelope.FrameEventID)
		if err != nil {
			return err
		}
		if found && (strings.TrimSpace(envelope.FrameIncarnationID) == "" ||
			captured.Context.Frame.IncarnationID != envelope.FrameIncarnationID ||
			captured.Source.FrameID != envelope.Event.FrameID ||
			captured.Context.Frame.ID != envelope.Event.FrameID ||
			captured.Context.UserID != envelope.Event.UserID ||
			captured.Context.Frame.ProjectID != envelope.Event.ProjectID ||
			captured.Context.Frame.RootFrameID != envelope.Event.RootFrameID) {
			return fmt.Errorf("realtime source frame event %q is unavailable", envelope.FrameEventID)
		}
		if !found {
			retired, retirementErr := d.Store.RealtimeOutboxSourceRetired(ctx, event, envelope)
			if retirementErr != nil {
				return retirementErr
			}
			if retired {
				return nil
			}
			return fmt.Errorf("realtime source frame event %q is unavailable", envelope.FrameEventID)
		}
		projection = &captured
	}
	durable, err := d.Store.AppendRealtimeEvent(envelope.Event)
	if err != nil {
		return fmt.Errorf("materialize realtime event %q: %w", envelope.Event.ID, err)
	}
	if err := d.Fanout.FanoutRealtimeOutbox(durable, projection); err != nil {
		return fmt.Errorf("fan out realtime event %q: %w", durable.ID, err)
	}
	return nil
}
