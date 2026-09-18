package server

import (
	"context"
	"log"
	"time"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolprogress"
)

// Reuse the public progress stream, but let the durable outbox claim own writes.
// Observing never starts a second operation or changes a computation's outcome.
func (s *Server) observeTaskOperation(ctx context.Context, event workspace.OutboxEvent, execute func(context.Context) (map[string]any, error)) (map[string]any, error) {
	operation, err := workspace.DecodeTaskOperation(event)
	if err != nil {
		return nil, err
	}
	if operation.Observation == nil {
		return execute(ctx)
	}
	reportedFailure := false
	publish := func(progress *toolprogress.Update, elapsed time.Duration, _ int) {
		if progress == nil || ctx.Err() != nil {
			return
		}
		// UI observations have a bounded write budget and never backpressure
		// installer stdout. Terminal facts use the separate atomic settlement.
		writeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		err := s.workspaceStore.AppendTaskOperationProgress(writeCtx, event, publicToolProgressPayload(*progress, elapsed))
		cancel()
		if err != nil {
			if !reportedFailure && ctx.Err() == nil {
				log.Printf("task operation progress persistence unavailable: %v", err)
				reportedFailure = true
			}
			return
		}
		s.signalTranscriptWebDelivery()
	}
	publish(&toolprogress.Update{Phase: "processing", Indeterminate: true}, 0, 0)
	return toolprogress.Observe(ctx, 0, execute, publish)
}
