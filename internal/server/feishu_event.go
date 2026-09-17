package server

import (
	"context"
	"errors"
	"net/http"

	adaptercommon "synon-go/internal/adapters/common"
	adapterfeishu "synon-go/internal/adapters/feishu"
)

func (s *Server) handleFeishuEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Feishu event endpoint only supports POST")
		return
	}

	event, err := adapterfeishu.ParseWebhookEvent(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if event.Challenge != "" {
		writeJSON(w, http.StatusOK, map[string]any{"challenge": event.Challenge})
		return
	}
	if event.Inbound == nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "feishu inbound event is required")
		return
	}

	result, err := s.HandleFeishuInbound(r.Context(), *event.Inbound)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) HandleFeishuInbound(ctx context.Context, inbound adapterfeishu.InboundEvent) (result map[string]any, err error) {
	if s == nil || s.taskStore == nil {
		return nil, errors.New("task store is not configured")
	}
	result = map[string]any{
		"ok":           true,
		"platform":     "feishu",
		"eventId":      inbound.EventID,
		"messageId":    inbound.MessageID,
		"chatId":       inbound.ChatID,
		"chatType":     inbound.ChatType,
		"senderOpenId": inbound.SenderOpenID,
		"messageType":  inbound.MessageType,
		"text":         inbound.Text,
		"downloads":    inbound.Downloads,
	}
	pairing, err := s.checkInboundPairing("feishu", inbound.SenderOpenID)
	if err != nil {
		return nil, err
	}
	applyPairingMap(result, pairing)
	if pairing.Blocked {
		return result, nil
	}
	record := inboundLiveSessionRecord{
		Platform: "feishu", ChatID: inbound.ChatID, MessageID: inbound.MessageID,
		MessageType: inbound.MessageType, SenderID: inbound.SenderOpenID,
		Text: inbound.Text, Downloads: inbound.Downloads, SourceEventID: inbound.EventID,
		ClientMessageID: inbound.DedupID, TaskTitle: inbound.TaskTitle, TargetType: "chat", TargetID: inbound.ChatID,
		OwnerUserID: pairing.OwnerUserID,
	}
	fingerprint, err := inboundLiveSessionFingerprint(record)
	if err != nil {
		return nil, err
	}
	reservation, duplicate, err := s.feishuDedup.Acquire(ctx, inbound.DedupID, fingerprint)
	if err != nil {
		return nil, err
	}
	if duplicate {
		result["deduplicated"] = true
		return result, nil
	}
	if reservation != nil {
		defer adaptercommon.CompleteMessageDedupReservation(reservation, &err)
	}
	result["deduplicated"] = false

	sessionID := imLiveSessionID("feishu", inbound.ChatID)
	task, err := s.ensureInboundTask(sessionID, inbound.DedupID, inbound.TaskTitle)
	if err != nil {
		return nil, err
	}
	record.TaskID, record.TaskTitle = task.ID, task.Title
	recordedSessionID, journalEventID, err := s.recordInboundLiveSession(record)
	if err != nil {
		rollbackErr := s.rollbackUnprojectedInboundTask(sessionID, inbound.DedupID, task.ID)
		return nil, errors.Join(err, rollbackErr)
	}
	result["task"] = task
	if recordedSessionID != "" {
		result["sessionId"] = recordedSessionID
		result["journalEventId"] = journalEventID
	}
	return result, nil
}
