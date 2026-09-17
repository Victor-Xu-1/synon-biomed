package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	adaptercommon "synon-go/internal/adapters/common"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	taskstore "synon-go/internal/persistence/tasks"
	transcriptstore "synon-go/internal/persistence/transcript"
)

type inboundLiveSessionRecord struct {
	Platform        string
	ChatID          string
	MessageID       string
	MessageType     string
	SenderID        string
	Text            string
	Downloads       any
	SourceEventID   string
	ClientMessageID string
	TaskID          string
	TaskTitle       string
	ContextToken    string
	TargetType      string
	TargetID        string
	OwnerUserID     string
}

type imInboundSessionLock struct {
	mu   sync.Mutex
	refs int
}

const transcriptIMStagedRecoveryLimit = 100

type inboundTranscriptProjection struct {
	Text               string `json:"text"`
	Platform           string `json:"platform"`
	ChatID             string `json:"chat_id"`
	MessageID          string `json:"message_id"`
	MessageType        string `json:"message_type"`
	SourceEventID      string `json:"source_event_id"`
	SenderID           string `json:"sender_id"`
	TargetType         string `json:"target_type"`
	TargetID           string `json:"target_id"`
	TaskTitle          string `json:"task_title"`
	Downloads          any    `json:"downloads"`
	DownloadsSHA256    string `json:"downloads_sha256"`
	ContextTokenSHA256 string `json:"context_token_sha256"`
}

func (s *Server) recordInboundLiveSession(record inboundLiveSessionRecord) (string, int64, error) {
	if s == nil || s.sessionStore == nil || s.eventJournal == nil {
		return "", 0, nil
	}
	platform, chatID, ownerUserID, payload, err := inboundTranscriptPayload(record)
	if err != nil {
		return "", 0, err
	}

	sessionID := imLiveSessionID(platform, chatID)
	unlock := s.lockIMInboundSession(sessionID)
	defer unlock()
	legacyClientMessageID := strings.TrimSpace(record.ClientMessageID)
	message := eventjournal.Message{
		"type": "im_message", "role": "user", "platform": platform,
		"chatId": chatID, "ownerUserId": ownerUserID,
	}
	addNonEmptyJournalField(message, "messageId", record.MessageID)
	addNonEmptyJournalField(message, "messageType", record.MessageType)
	addNonEmptyJournalField(message, "senderId", record.SenderID)
	addNonEmptyJournalField(message, "text", record.Text)
	addNonEmptyJournalField(message, "sourceEventId", record.SourceEventID)
	addNonEmptyJournalField(message, "taskId", record.TaskID)
	addNonEmptyJournalField(message, "taskTitle", record.TaskTitle)
	if record.Downloads != nil {
		message["downloads"] = record.Downloads
	}
	metadata := eventjournal.Metadata{ClientMessageID: legacyClientMessageID}
	journalProjection := true
	streamUID := ""
	staged := transcriptstore.StageUserEventResult{}
	if s.transcriptStore != nil {
		if s.transcriptContractErr != nil {
			return "", 0, s.transcriptContractErr
		}
		clientMessageID := firstNonEmpty(record.ClientMessageID, record.SourceEventID, record.MessageID)
		if strings.TrimSpace(clientMessageID) == "" {
			return "", 0, errors.New("IM transcript input requires a stable message identity")
		}
		legacyClientMessageID = clientMessageID
		metadata.ClientMessageID = clientMessageID
		if err := s.eventJournal.PreflightAppend(sessionID, message, metadata); err != nil {
			if !isJournalProjectionCapacityError(err) {
				return "", 0, err
			}
			journalProjection = false
		}
		stream, found, err := s.transcriptStore.GetStreamBySession(context.Background(), ownerUserID, sessionID)
		if err != nil {
			return "", 0, fmt.Errorf("resolve IM transcript stream: %w", err)
		}
		if found {
			if stream.ExternalID != sessionID || stream.SessionID != sessionID ||
				(stream.Kind != transcriptstore.StreamKindStandalone && stream.Kind != transcriptstore.StreamKindTaskRun) {
				return "", 0, transcriptstore.ErrEventConflict
			}
			streamUID = stream.UID
		} else {
			streamUID = transcriptIMStreamUID(sessionID)
			if _, err := s.transcriptStore.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
				UID: streamUID, OwnerID: ownerUserID, ExternalID: sessionID, SessionID: sessionID,
				Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
			}); err != nil {
				return "", 0, fmt.Errorf("create IM transcript stream: %w", err)
			}
		}
		staged, err = s.transcriptStore.StageUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
			StreamUID: streamUID, OwnerID: ownerUserID, ClientMessageID: clientMessageID, PayloadJSON: payload,
		})
		if err != nil {
			return "", 0, fmt.Errorf("stage IM transcript input: %w", err)
		}
	}

	var entry *eventjournal.Entry
	var journalCreated bool
	var entries []eventjournal.Entry
	if !journalProjection {
		// SQLite Transcript is authoritative. A bounded compatibility journal
		// must never make canonical input permanently unclaimable.
	} else if s.transcriptStore != nil && staged.Created {
		entry, err = s.eventJournal.Append(sessionID, message, metadata)
		journalCreated = entry != nil
	} else {
		entry, journalCreated, entries, err = s.eventJournal.AppendIdempotentSnapshot(sessionID, message, metadata)
	}
	if err != nil {
		if errors.Is(err, eventjournal.ErrIdempotencyConflict) {
			return "", 0, transcriptstore.ErrEventConflict
		}
		return "", 0, err
	}
	if s.imProjectionHook != nil {
		s.imProjectionHook()
	}
	if err := s.sessionStore.Upsert(sessionstore.Session{
		ID:      sessionID,
		Title:   fmt.Sprintf("%s %s", platform, chatID),
		WorkDir: s.fileRoot,
	}); err != nil {
		return "", 0, err
	}
	if !journalProjection {
		// Session JSON is retained only as a discoverable compatibility shell.
	} else if journalCreated {
		if err := s.sessionStore.AppendMessage(sessionID, "user"); err != nil {
			return "", 0, err
		}
	} else {
		session, found, err := s.sessionStore.Get(sessionID)
		if err != nil {
			return "", 0, err
		}
		if !found {
			return "", 0, errors.New("IM session projection is missing")
		}
		projection := sessionWithJournalStats(session, entries)
		if err := s.sessionStore.ReconcileMessageStats(sessionID, projection.MessageCount, projection.LastRole, projection.LastUserMessageAt); err != nil {
			return "", 0, err
		}
	}
	var binding *adaptercommon.IMSessionBinding
	if s.imOutbound != nil {
		candidate := adaptercommon.IMSessionBinding{
			SessionID: sessionID, Platform: platform, ChatID: chatID,
			SenderID: record.SenderID, MessageID: record.MessageID,
			ContextToken: record.ContextToken, TargetType: record.TargetType,
			TargetID: record.TargetID, OwnerUserID: ownerUserID,
		}
		if err := s.imOutbound.Bind(candidate); err != nil {
			return "", 0, fmt.Errorf("bind IM outbound route: %w", err)
		}
		if err := s.persistIMOutboundBindingState(candidate); err != nil {
			if unbinder, ok := s.imOutbound.(adaptercommon.SessionOutboundUnbinder); ok {
				unbinder.Unbind(sessionID)
			}
			return "", 0, fmt.Errorf("persist IM outbound route: %w", err)
		}
		binding = &candidate
	}
	if s.transcriptStore != nil && binding == nil {
		return "", 0, errors.New("canonical IM outbound route is unavailable")
	}
	if binding != nil && s.transcriptStore != nil {
		configured, err := s.configureTranscriptIMRoute(context.Background(), binding.OwnerUserID, binding.SessionID)
		if err != nil {
			if unbinder, ok := s.imOutbound.(adaptercommon.SessionOutboundUnbinder); ok {
				unbinder.Unbind(sessionID)
			}
			return "", 0, fmt.Errorf("activate IM transcript route: %w", err)
		}
		if !configured {
			if unbinder, ok := s.imOutbound.(adaptercommon.SessionOutboundUnbinder); ok {
				unbinder.Unbind(sessionID)
			}
			return "", 0, errors.New("canonical IM transcript route is unavailable")
		}
	}
	if s.transcriptStore != nil {
		if _, _, err := s.transcriptStore.AdmitUserEvent(context.Background(), transcriptstore.AdmitUserEventInput{
			StreamUID: streamUID, OwnerID: ownerUserID, ClientMessageID: legacyClientMessageID,
		}); err != nil {
			return "", 0, fmt.Errorf("admit IM transcript input: %w", err)
		}
	}
	if entry == nil {
		return sessionID, 0, nil
	}
	return sessionID, entry.EventID, nil
}

func isJournalProjectionCapacityError(err error) bool {
	return errors.Is(err, eventjournal.ErrEntrySizeLimit) || errors.Is(err, eventjournal.ErrFileSizeLimit) ||
		errors.Is(err, eventjournal.ErrEntryCountLimit)
}

func imLiveSessionID(platform string, chatID string) string {
	return fmt.Sprintf("im:%s:%s", platform, chatID)
}

func transcriptIMStreamUID(sessionID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
	return "im-" + hex.EncodeToString(sum[:16])
}

func inboundLiveSessionFingerprint(record inboundLiveSessionRecord) (string, error) {
	_, _, ownerUserID, payload, err := inboundTranscriptPayload(record)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(ownerUserID))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func inboundTranscriptPayload(record inboundLiveSessionRecord) (string, string, string, []byte, error) {
	platform := strings.TrimSpace(record.Platform)
	chatID := strings.TrimSpace(record.ChatID)
	if platform == "" || chatID == "" {
		return "", "", "", nil, errors.New("im live session platform and chat id are required")
	}
	ownerUserID := strings.TrimSpace(record.OwnerUserID)
	if ownerUserID == "" {
		return "", "", "", nil, errors.New("IM live session owner authority is required")
	}
	downloadsDigest, err := inboundDownloadsDigest(record.Downloads)
	if err != nil {
		return "", "", "", nil, err
	}
	contextTokenDigest := ""
	if token := strings.TrimSpace(record.ContextToken); token != "" {
		digest := sha256.Sum256([]byte(token))
		contextTokenDigest = hex.EncodeToString(digest[:])
	}
	payload, err := json.Marshal(map[string]any{
		"text": record.Text, "platform": platform, "chat_id": chatID,
		"message_id": record.MessageID, "message_type": record.MessageType,
		"source_event_id": record.SourceEventID, "sender_id": record.SenderID,
		"target_type": record.TargetType, "target_id": record.TargetID,
		"task_title": record.TaskTitle,
		"downloads":  record.Downloads, "downloads_sha256": downloadsDigest,
		"context_token_sha256": contextTokenDigest,
	})
	if err != nil {
		return "", "", "", nil, fmt.Errorf("encode IM transcript input: %w", err)
	}
	return platform, chatID, ownerUserID, payload, nil
}

func inboundDownloadsDigest(downloads any) (string, error) {
	if downloads == nil {
		return "", nil
	}
	encoded, err := json.Marshal(downloads)
	if err != nil {
		return "", fmt.Errorf("encode IM download identity: %w", err)
	}
	if len(encoded) > 1<<20 {
		return "", errors.New("IM download identity exceeds size limit")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Server) reconcileStagedIMInputs(
	ctx context.Context,
	binding adaptercommon.IMSessionBinding,
) (transcriptstore.Stream, []transcriptstore.Event, error) {
	if s == nil || s.transcriptStore == nil || s.eventJournal == nil || s.sessionStore == nil {
		return transcriptstore.Stream{}, nil, nil
	}
	stream, events, err := s.transcriptStore.ListStagedUserEventsBySession(
		ctx, binding.OwnerUserID, binding.SessionID, transcriptIMStagedRecoveryLimit,
	)
	if err != nil {
		return transcriptstore.Stream{}, nil, err
	}
	if stream.Kind != transcriptstore.StreamKindStandalone && stream.Kind != transcriptstore.StreamKindTaskRun {
		return transcriptstore.Stream{}, nil, transcriptstore.ErrEventConflict
	}
	for _, event := range events {
		if err := s.reconcileStagedIMInput(ctx, binding, stream, event); err != nil {
			return transcriptstore.Stream{}, nil, err
		}
	}
	return stream, events, nil
}

func (s *Server) reconcileStagedIMInput(
	ctx context.Context,
	binding adaptercommon.IMSessionBinding,
	stream transcriptstore.Stream,
	event transcriptstore.Event,
) error {
	payload, err := decodeInboundTranscriptProjection(event.PayloadJSON)
	if event.Source != transcriptstore.EventSourcePayload || event.ClientMessageID == "" || err != nil {
		return transcriptstore.ErrEventConflict
	}
	platform := strings.ToLower(strings.TrimSpace(payload.Platform))
	chatID := strings.TrimSpace(payload.ChatID)
	if imLiveSessionID(platform, chatID) != binding.SessionID || platform != strings.ToLower(strings.TrimSpace(binding.Platform)) ||
		stream.OwnerID != strings.TrimSpace(binding.OwnerUserID) || stream.SessionID != binding.SessionID {
		return transcriptstore.ErrEventConflict
	}
	if digest, err := inboundDownloadsDigest(payload.Downloads); err != nil || digest != payload.DownloadsSHA256 {
		return transcriptstore.ErrEventConflict
	}
	message := eventjournal.Message{
		"type": "im_message", "role": "user", "platform": platform, "chatId": chatID,
		"ownerUserId": stream.OwnerID,
	}
	addNonEmptyJournalField(message, "messageId", payload.MessageID)
	addNonEmptyJournalField(message, "messageType", payload.MessageType)
	addNonEmptyJournalField(message, "senderId", payload.SenderID)
	addNonEmptyJournalField(message, "text", payload.Text)
	addNonEmptyJournalField(message, "sourceEventId", payload.SourceEventID)
	addNonEmptyJournalField(message, "taskTitle", payload.TaskTitle)
	if payload.Downloads != nil {
		message["downloads"] = payload.Downloads
	}
	if err := s.eventJournal.PreflightAppendIdempotent(
		binding.SessionID, message, eventjournal.Metadata{ClientMessageID: event.ClientMessageID},
	); err != nil {
		if isJournalProjectionCapacityError(err) {
			return s.sessionStore.Upsert(sessionstore.Session{
				ID: binding.SessionID, Title: fmt.Sprintf("%s %s", platform, chatID), WorkDir: s.fileRoot,
			})
		}
		if errors.Is(err, eventjournal.ErrIdempotencyConflict) {
			return transcriptstore.ErrEventConflict
		}
		return err
	}
	strict, err := s.eventJournal.ReadAllStrict(binding.SessionID)
	if err != nil {
		return err
	}
	for _, existing := range strict {
		if existing.ClientMessageID != event.ClientMessageID {
			continue
		}
		if err := s.validateExistingIMJournalProjection(
			existing.Message, payload, stream.OwnerID, binding.SessionID, event.ClientMessageID,
		); err != nil {
			return err
		}
		message = existing.Message
		break
	}
	entry, created, entries, err := s.eventJournal.AppendIdempotentSnapshot(
		binding.SessionID, message, eventjournal.Metadata{ClientMessageID: event.ClientMessageID},
	)
	if err != nil || entry == nil {
		if err == nil {
			err = errors.New("staged IM projection did not produce a journal entry")
		}
		return err
	}
	if err := s.sessionStore.Upsert(sessionstore.Session{
		ID: binding.SessionID, Title: fmt.Sprintf("%s %s", platform, chatID), WorkDir: s.fileRoot,
	}); err != nil {
		return err
	}
	if created {
		if err := s.sessionStore.AppendMessage(binding.SessionID, "user"); err != nil {
			return err
		}
	} else {
		session, found, err := s.sessionStore.Get(binding.SessionID)
		if err != nil || !found {
			if err == nil {
				err = errors.New("IM session projection is missing")
			}
			return err
		}
		projection := sessionWithJournalStats(session, entries)
		if err := s.sessionStore.ReconcileMessageStats(
			binding.SessionID, projection.MessageCount, projection.LastRole, projection.LastUserMessageAt,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) validateExistingIMJournalProjection(
	message eventjournal.Message,
	payload inboundTranscriptProjection,
	ownerID, sessionID, clientMessageID string,
) error {
	allowed := map[string]struct{}{
		"type": {}, "role": {}, "platform": {}, "chatId": {}, "ownerUserId": {},
		"messageId": {}, "messageType": {}, "senderId": {}, "text": {}, "sourceEventId": {},
		"taskId": {}, "taskTitle": {}, "downloads": {}, "eventId": {}, "createdAt": {},
		"clientMessageId": {}, "runId": {},
	}
	for key := range message {
		if _, ok := allowed[key]; !ok {
			return transcriptstore.ErrEventConflict
		}
	}
	expected := map[string]string{
		"type": "im_message", "role": "user", "platform": strings.ToLower(strings.TrimSpace(payload.Platform)),
		"chatId": strings.TrimSpace(payload.ChatID), "ownerUserId": strings.TrimSpace(ownerID),
		"messageId": strings.TrimSpace(payload.MessageID), "messageType": strings.TrimSpace(payload.MessageType),
		"senderId": strings.TrimSpace(payload.SenderID), "text": strings.TrimSpace(payload.Text),
		"sourceEventId": strings.TrimSpace(payload.SourceEventID), "taskTitle": strings.TrimSpace(payload.TaskTitle),
	}
	for key, value := range expected {
		actual, found := message[key].(string)
		if !found && message[key] != nil {
			return transcriptstore.ErrEventConflict
		}
		if strings.TrimSpace(actual) != value {
			return transcriptstore.ErrEventConflict
		}
	}
	canonicalDownloads, err := json.Marshal(payload.Downloads)
	if err != nil {
		return transcriptstore.ErrEventConflict
	}
	journalDownloads, err := json.Marshal(message["downloads"])
	if err != nil || !bytes.Equal(canonicalDownloads, journalDownloads) {
		return transcriptstore.ErrEventConflict
	}
	if rawTaskID := message["taskId"]; rawTaskID != nil {
		taskID, ok := rawTaskID.(string)
		if !ok {
			return transcriptstore.ErrEventConflict
		}
		taskID = strings.TrimSpace(taskID)
		task, found, err := s.taskStore.Get(taskID)
		if err != nil || !found || !inboundTaskMatches(task, sessionID, clientMessageID, payload.TaskTitle) {
			return transcriptstore.ErrEventConflict
		}
	}
	return nil
}

func decodeInboundTranscriptProjection(data []byte) (inboundTranscriptProjection, error) {
	var payload inboundTranscriptProjection
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return inboundTranscriptProjection{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return inboundTranscriptProjection{}, errors.New("IM transcript payload has trailing content")
	}
	if strings.TrimSpace(payload.Platform) == "" || strings.TrimSpace(payload.ChatID) == "" ||
		strings.TrimSpace(payload.SenderID) == "" ||
		strings.TrimSpace(payload.TargetType) == "" || strings.TrimSpace(payload.TargetID) == "" ||
		(strings.TrimSpace(payload.Text) == "" && payload.Downloads == nil) {
		return inboundTranscriptProjection{}, errors.New("IM transcript payload is missing required identity")
	}
	return payload, nil
}

func (s *Server) lockIMInboundSession(sessionID string) func() {
	s.imInboundMu.Lock()
	lock := s.imInboundLocks[sessionID]
	if lock == nil {
		lock = &imInboundSessionLock{}
		s.imInboundLocks[sessionID] = lock
	}
	lock.refs++
	s.imInboundMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.imInboundMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.imInboundLocks, sessionID)
		}
		s.imInboundMu.Unlock()
	}
}

func (s *Server) ensureInboundTask(sessionID, clientMessageID, title string) (taskstore.Task, error) {
	if s == nil || s.taskStore == nil {
		return taskstore.Task{}, errors.New("task store is not configured")
	}
	if s.eventJournal != nil {
		entries, err := s.eventJournal.ReadAll(sessionID)
		if err == nil {
			for _, entry := range entries {
				if entry.ClientMessageID != clientMessageID {
					continue
				}
				taskID := strings.TrimSpace(fmt.Sprint(entry.Message["taskId"]))
				if taskID == "" {
					break
				}
				if task, found, err := s.taskStore.Get(taskID); err != nil {
					return taskstore.Task{}, err
				} else if found && inboundTaskMatches(task, sessionID, clientMessageID, title) {
					return task, nil
				} else if found {
					return taskstore.Task{}, transcriptstore.ErrEventConflict
				}
				break
			}
		}
	}
	tasks, err := s.taskStore.List()
	if err != nil {
		return taskstore.Task{}, err
	}
	var matched *taskstore.Task
	for index := range tasks {
		task := &tasks[index]
		if task.Metadata == nil {
			continue
		}
		metadataSession, sessionOK := task.Metadata["imSessionId"].(string)
		metadataClient, clientOK := task.Metadata["imClientMessageId"].(string)
		if !sessionOK || !clientOK || strings.TrimSpace(metadataSession) != strings.TrimSpace(sessionID) ||
			strings.TrimSpace(metadataClient) != strings.TrimSpace(clientMessageID) {
			continue
		}
		if !inboundTaskMatches(*task, sessionID, clientMessageID, title) || matched != nil {
			return taskstore.Task{}, transcriptstore.ErrEventConflict
		}
		matched = task
	}
	if matched != nil {
		return *matched, nil
	}
	return s.taskStore.CreateWithOptions(taskstore.CreateOptions{
		Title: title, Status: "open", Metadata: map[string]any{
			"imSessionId": sessionID, "imClientMessageId": clientMessageID,
		},
	})
}

func inboundTaskMatches(task taskstore.Task, sessionID, clientMessageID, title string) bool {
	if strings.TrimSpace(task.Title) != strings.TrimSpace(title) || task.Metadata == nil {
		return false
	}
	session, sessionOK := task.Metadata["imSessionId"].(string)
	client, clientOK := task.Metadata["imClientMessageId"].(string)
	return sessionOK && clientOK && strings.TrimSpace(session) == strings.TrimSpace(sessionID) &&
		strings.TrimSpace(client) == strings.TrimSpace(clientMessageID)
}

func (s *Server) rollbackUnprojectedInboundTask(sessionID, clientMessageID, taskID string) error {
	if s == nil || s.taskStore == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	if s.eventJournal != nil {
		entries, err := s.eventJournal.ReadAllStrict(sessionID)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.ClientMessageID == clientMessageID && strings.TrimSpace(fmt.Sprint(entry.Message["taskId"])) == taskID {
				return nil
			}
		}
	}
	_, _, _, err := s.taskStore.UpdateWithOptions(taskID, taskstore.UpdateOptions{Delete: true})
	return err
}

func addNonEmptyJournalField(message eventjournal.Message, key string, value string) {
	if value := strings.TrimSpace(value); value != "" {
		message[key] = value
	}
}
