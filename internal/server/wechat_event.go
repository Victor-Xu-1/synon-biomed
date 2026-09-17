package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	adaptercommon "synon-go/internal/adapters/common"
	adapterwechat "synon-go/internal/adapters/wechat"
	taskstore "synon-go/internal/persistence/tasks"
)

type WeChatInboundResult struct {
	OK            bool                           `json:"ok"`
	Platform      string                         `json:"platform"`
	MessageID     int64                          `json:"messageId"`
	Seq           int64                          `json:"seq"`
	ChatID        string                         `json:"chatId"`
	ContextToken  string                         `json:"contextToken,omitempty"`
	Text          string                         `json:"text,omitempty"`
	Downloads     []adapterwechat.MediaCandidate `json:"downloads"`
	Deduplicated  bool                           `json:"deduplicated"`
	Paired        bool                           `json:"paired"`
	Blocked       bool                           `json:"blocked"`
	BlockedReason string                         `json:"blockedReason,omitempty"`
	SessionID     string                         `json:"sessionId,omitempty"`
	JournalEvent  int64                          `json:"journalEventId,omitempty"`
	Task          *taskstore.Task                `json:"task,omitempty"`
}

func (s *Server) handleWeChatEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "WeChat event endpoint only supports POST")
		return
	}
	message, err := decodeWeChatMessage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	result, err := s.HandleWeChatInbound(r.Context(), message)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) HandleWeChatInbound(ctx context.Context, message adapterwechat.Message) (result WeChatInboundResult, err error) {
	if s.taskStore == nil {
		return WeChatInboundResult{}, errors.New("task store is not configured")
	}

	text := adapterwechat.ExtractText(message.ItemList)
	downloads := adapterwechat.CollectMediaCandidates(message.ItemList)
	if message.FromUserID == "" || (text == "" && len(downloads) == 0) {
		return WeChatInboundResult{}, errors.New("wechat from_user_id, text, or media is required")
	}

	result = WeChatInboundResult{
		OK:           true,
		Platform:     "wechat",
		MessageID:    message.MessageID,
		Seq:          message.Seq,
		ChatID:       message.FromUserID,
		ContextToken: message.ContextToken,
		Text:         text,
		Downloads:    downloads,
	}
	pairing, err := s.checkInboundPairing("wechat", message.FromUserID)
	if err != nil {
		return WeChatInboundResult{}, err
	}
	result.Paired = pairing.Paired
	result.Blocked = pairing.Blocked
	result.BlockedReason = pairing.BlockedReason
	if pairing.Blocked {
		return result, nil
	}
	dedupID := adapterwechat.DedupID(message)
	taskTitle := adapterwechat.TaskTitle(text, downloads)
	record := inboundLiveSessionRecord{
		Platform: "wechat", ChatID: message.FromUserID,
		MessageID: strconv.FormatInt(message.MessageID, 10), SenderID: message.FromUserID,
		Text: text, Downloads: downloads, ClientMessageID: dedupID, TaskTitle: taskTitle,
		ContextToken: message.ContextToken, TargetType: "user", TargetID: message.FromUserID,
		OwnerUserID: pairing.OwnerUserID,
	}
	fingerprint, err := inboundLiveSessionFingerprint(record)
	if err != nil {
		return WeChatInboundResult{}, err
	}
	reservation, duplicate, err := s.wechatDedup.Acquire(ctx, dedupID, fingerprint)
	if err != nil {
		return WeChatInboundResult{}, err
	}
	if duplicate {
		result.Deduplicated = true
		return result, nil
	}
	if reservation != nil {
		defer adaptercommon.CompleteMessageDedupReservation(reservation, &err)
	}
	sessionID := imLiveSessionID("wechat", message.FromUserID)
	task, err := s.ensureInboundTask(sessionID, dedupID, taskTitle)
	if err != nil {
		return WeChatInboundResult{}, err
	}
	record.TaskID, record.TaskTitle = task.ID, task.Title
	recordedSessionID, journalEventID, err := s.recordInboundLiveSession(record)
	if err != nil {
		rollbackErr := s.rollbackUnprojectedInboundTask(sessionID, dedupID, task.ID)
		return WeChatInboundResult{}, errors.Join(err, rollbackErr)
	}
	result.SessionID = recordedSessionID
	result.JournalEvent = journalEventID
	result.Task = &task
	return result, nil
}

func decodeWeChatMessage(r *http.Request) (adapterwechat.Message, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return adapterwechat.Message{}, err
	}
	var envelope struct {
		Messages []adapterwechat.Message `json:"msgs"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return adapterwechat.Message{}, err
	}
	if len(envelope.Messages) > 1 {
		return adapterwechat.Message{}, errors.New("wechat event endpoint accepts one message at a time")
	}
	if len(envelope.Messages) == 1 {
		return envelope.Messages[0], nil
	}
	var message adapterwechat.Message
	if err := json.Unmarshal(raw, &message); err != nil {
		return adapterwechat.Message{}, err
	}
	return message, nil
}
