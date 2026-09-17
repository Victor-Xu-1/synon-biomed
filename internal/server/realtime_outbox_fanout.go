package server

import (
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/realtime"
)

// FanoutRealtimeOutbox publishes an already durable event. Re-delivery after
// an ack failure is harmless because SSE and WebSocket consumers fence on the
// stable realtime sequence, and the underlying durable row is idempotent.
func (s *Server) FanoutRealtimeOutbox(event workspace.RealtimeEvent, projection *workspace.RealtimeFrameProjection) error {
	if s == nil {
		return nil
	}
	if projection != nil {
		if s.transcriptStore == nil || !projection.WebConversation || projection.TranscriptBacked {
			if err := s.publishWebFrameEventProjectionSnapshot(
				projection.Context, projection.Source, projection.WebConversation, projection.TranscriptBacked,
			); err != nil {
				return err
			}
		}
	}
	if event.Kind != realtime.DeliveryNone {
		s.compatEvents.Publish(event)
	}
	if projection != nil {
		s.workspaceEvents.Publish(projection.Source)
	}
	return nil
}
