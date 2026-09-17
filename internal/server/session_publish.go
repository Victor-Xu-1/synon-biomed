package server

import (
	"strings"

	adaptercommon "synon-go/internal/adapters/common"
	eventjournal "synon-go/internal/persistence/journal"
)

func (s *Server) publishSessionMessage(sessionID string, eventID int64, message eventjournal.Message) bool {
	if s == nil || strings.TrimSpace(sessionID) == "" || message == nil {
		return false
	}
	delivered := false
	if s.sessionSockets != nil {
		delivered = s.sessionSockets.Send(sessionID, message)
	}
	s.transcriptIMMu.Lock()
	_, transcriptIM := s.transcriptIMRoutes[sessionID]
	s.transcriptIMMu.Unlock()
	if !transcriptIM && s.imOutbound != nil && s.imOutbound.Enqueue(adaptercommon.SessionOutboundEvent{
		SessionID: sessionID,
		EventID:   eventID,
		Message:   adaptercommon.ServerMessage(message),
	}) {
		delivered = true
	}
	return delivered
}

func (s *Server) publishSessionEntry(entry *eventjournal.Entry) bool {
	if entry == nil {
		return false
	}
	return s.publishSessionMessage(entry.SessionID, entry.EventID, entry.Message)
}
