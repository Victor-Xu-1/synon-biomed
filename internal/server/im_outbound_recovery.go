package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	adaptercommon "synon-go/internal/adapters/common"
	eventjournal "synon-go/internal/persistence/journal"
	secretstore "synon-go/internal/persistence/secrets"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	imOutboundRouteNamespace      = "im-outbound-routes"
	imOutboundDeliveryNamespace   = "im-outbound-delivery"
	internalIMRouteSecretPrefix   = "_synon_internal_im_route_"
	internalIMRouteSecretProvider = "_synon_internal_im_route"
	imOutboundRecoveryBatchSize   = 500
	imOutboundRecoveryLimit       = 5000
)

type imOutboundRouteState struct {
	SchemaVersion   int    `json:"schemaVersion"`
	SessionID       string `json:"sessionId"`
	Platform        string `json:"platform"`
	ChatID          string `json:"chatId"`
	SenderID        string `json:"senderId,omitempty"`
	MessageID       string `json:"messageId,omitempty"`
	TargetType      string `json:"targetType"`
	TargetID        string `json:"targetId"`
	OwnerUserID     string `json:"ownerUserId"`
	ContextSecretID string `json:"contextSecretId,omitempty"`
}

type imOutboundDeliveryState struct {
	SchemaVersion     int    `json:"schemaVersion"`
	CommittedEventID  int64  `json:"committedEventId"`
	LastFailedEventID int64  `json:"lastFailedEventId,omitempty"`
	LastMessageType   string `json:"lastMessageType,omitempty"`
	LastAttempts      int    `json:"lastAttempts,omitempty"`
	Status            string `json:"status"`
	Error             string `json:"error,omitempty"`
}

type IMOutboundRecoveryReport struct {
	Routes   int      `json:"routes"`
	Restored int      `json:"restored"`
	Skipped  int      `json:"skipped"`
	Replayed int      `json:"replayed"`
	Warnings []string `json:"warnings,omitempty"`
}

func (s *Server) persistIMOutboundBindingState(binding adaptercommon.IMSessionBinding) error {
	if s == nil || s.runtimeStore == nil || s.secretStore == nil {
		return errors.New("IM outbound persistence is not configured")
	}
	binding.SessionID = strings.TrimSpace(binding.SessionID)
	binding.Platform = strings.ToLower(strings.TrimSpace(binding.Platform))
	binding.OwnerUserID = strings.TrimSpace(binding.OwnerUserID)
	if binding.SessionID == "" || binding.Platform == "" || binding.OwnerUserID == "" {
		return errors.New("IM outbound binding persistence requires session, platform, and owner")
	}
	if s.transcriptStore != nil {
		stream, found, err := s.transcriptStore.GetStreamBySession(context.Background(), binding.OwnerUserID, binding.SessionID)
		if err != nil {
			return err
		}
		if !found || stream.Kind != transcriptstore.StreamKindStandalone && stream.Kind != transcriptstore.StreamKindTaskRun {
			return errors.New("canonical IM transcript stream is unavailable")
		}
	}

	state := imOutboundRouteState{
		SchemaVersion: 1,
		SessionID:     binding.SessionID,
		Platform:      binding.Platform,
		ChatID:        strings.TrimSpace(binding.ChatID),
		SenderID:      strings.TrimSpace(binding.SenderID),
		MessageID:     strings.TrimSpace(binding.MessageID),
		TargetType:    strings.TrimSpace(binding.TargetType),
		TargetID:      strings.TrimSpace(binding.TargetID),
		OwnerUserID:   binding.OwnerUserID,
	}
	contextToken := strings.TrimSpace(binding.ContextToken)
	if binding.Platform == "wechat" && contextToken == "" {
		return errors.New("wechat IM outbound binding requires a context token")
	}
	if contextToken != "" {
		state.ContextSecretID = internalIMRouteSecretID(binding.SessionID, binding.OwnerUserID)
		if err := s.upsertInternalIMRouteSecret(state.ContextSecretID, binding.OwnerUserID, contextToken); err != nil {
			return err
		}
	}
	if _, err := s.runtimeStore.Set(imOutboundRouteNamespace, imOutboundStateKey(binding.SessionID), state); err != nil {
		return err
	}
	return nil
}

func (s *Server) upsertInternalIMRouteSecret(id string, userID string, contextToken string) error {
	stored, found, err := s.secretStore.ResolveForUser(id, userID)
	if err != nil {
		return fmt.Errorf("resolve encrypted IM route token: %w", err)
	}
	if found {
		if stored.Provider != internalIMRouteSecretProvider {
			return errors.New("encrypted IM route token id collides with a user secret")
		}
		_, err = s.secretStore.UpdateForUser(id, userID, func(candidate *secretstore.Secret) error {
			candidate.Value = contextToken
			return nil
		})
		if err != nil {
			return fmt.Errorf("update encrypted IM route token: %w", err)
		}
		return nil
	}
	_, err = s.secretStore.Create(secretstore.Secret{
		ID: id, UserID: userID, Provider: internalIMRouteSecretProvider,
		Name: "Synon internal IM route", Value: contextToken,
	})
	if err != nil {
		return fmt.Errorf("create encrypted IM route token: %w", err)
	}
	return nil
}

func (s *Server) RecordIMOutboundResult(result adaptercommon.SessionOutboundResult) error {
	if settled, err := s.settleTranscriptIMDelivery(context.Background(), result); settled {
		return err
	}
	if s == nil || s.runtimeStore == nil || strings.TrimSpace(result.SessionID) == "" {
		return nil
	}
	key := imOutboundStateKey(result.SessionID)
	state, err := s.loadIMOutboundDeliveryState(key)
	if err != nil {
		return err
	}
	state.SchemaVersion = 1
	state.LastMessageType = strings.TrimSpace(result.MessageType)
	state.LastAttempts = result.Delivery.Attempts
	if result.EventID > 0 && result.EventID <= state.CommittedEventID {
		return nil
	}
	if !result.Delivery.Delivered {
		if result.EventID > state.CommittedEventID &&
			(state.LastFailedEventID == 0 || result.EventID < state.LastFailedEventID) {
			state.LastFailedEventID = result.EventID
		}
		state.Status = "failed"
		state.Error = boundedIMOutboundError(firstNonEmpty(result.Delivery.Error, result.Report.Error))
	} else {
		if result.EventID > 0 && result.EventID == state.LastFailedEventID {
			state.LastFailedEventID = 0
			state.Status = "pending"
			state.Error = ""
		}
		if !imOutboundTerminalMessage(result.MessageType) {
			if state.Status != "pending" {
				return nil
			}
		} else if state.LastFailedEventID > state.CommittedEventID && state.LastFailedEventID < result.EventID {
			state.Status = "failed"
		} else {
			if result.EventID > state.CommittedEventID {
				state.CommittedEventID = result.EventID
			}
			state.LastFailedEventID = 0
			state.Status = "committed"
			state.Error = ""
		}
	}
	_, err = s.runtimeStore.Set(imOutboundDeliveryNamespace, key, state)
	return err
}

func (s *Server) AuthorizeIMOutbound(binding adaptercommon.IMSessionBinding) error {
	if s == nil || s.pairingStore == nil {
		return errors.New("IM outbound pairing authority is not configured")
	}
	platform := strings.ToLower(strings.TrimSpace(binding.Platform))
	senderID := strings.TrimSpace(binding.SenderID)
	if platform == "" || senderID == "" {
		return errors.New("IM outbound route is missing paired sender identity")
	}
	pairedUser, paired, err := s.pairingStore.Get(platform, senderID)
	if err != nil {
		return fmt.Errorf("check IM outbound pairing: %w", err)
	}
	if !paired {
		return errors.New("IM outbound route is no longer paired")
	}
	ownerUserID := strings.TrimSpace(pairedUser.OwnerUserID)
	if ownerUserID == "" && (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) {
		ownerUserID = secretstore.DefaultUserID
	}
	if ownerUserID == "" || ownerUserID != strings.TrimSpace(binding.OwnerUserID) {
		return errors.New("IM outbound route owner is not authorized")
	}
	return nil
}

func (s *Server) RecoverIMOutbound(ctx context.Context) (IMOutboundRecoveryReport, error) {
	report := IMOutboundRecoveryReport{}
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.imOutbound == nil || s.runtimeStore == nil || s.secretStore == nil || s.eventJournal == nil {
		return report, nil
	}
	entries, err := s.runtimeStore.List(imOutboundRouteNamespace)
	if err != nil {
		return report, fmt.Errorf("list persisted IM outbound routes: %w", err)
	}
	report.Routes = len(entries)
	for _, entry := range entries {
		if err := context.Cause(ctx); err != nil {
			return report, err
		}
		var state imOutboundRouteState
		if err := decodeRuntimeValue(entry.Value, &state); err != nil {
			return report, fmt.Errorf("decode persisted IM outbound route %s: %w", entry.Key, err)
		}
		binding, warning, err := s.restoreIMOutboundBinding(state)
		if err != nil {
			return report, err
		}
		if warning != "" {
			report.Skipped++
			report.Warnings = append(report.Warnings, warning)
			continue
		}
		if err := s.AuthorizeIMOutbound(binding); err != nil {
			report.Skipped++
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %s", state.SessionID, boundedIMOutboundError(err.Error())))
			continue
		}
		var stagedStream transcriptstore.Stream
		var stagedEvents []transcriptstore.Event
		if s.transcriptStore != nil {
			stagedStream, stagedEvents, err = s.reconcileStagedIMInputs(ctx, binding)
			if err != nil {
				return report, fmt.Errorf("reconcile staged IM input for %s: %w", state.SessionID, err)
			}
		}
		if err := s.imOutbound.Bind(binding); err != nil {
			report.Skipped++
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %s", state.SessionID, boundedIMOutboundError(err.Error())))
			continue
		}
		configured, err := s.configureTranscriptIMRoute(ctx, binding.OwnerUserID, binding.SessionID)
		if err != nil {
			return report, err
		}
		if s.transcriptStore != nil && !configured {
			if unbinder, ok := s.imOutbound.(adaptercommon.SessionOutboundUnbinder); ok {
				unbinder.Unbind(binding.SessionID)
			}
			report.Skipped++
			report.Warnings = append(report.Warnings, binding.SessionID+": canonical transcript stream is unavailable")
			continue
		}
		if configured {
			for _, event := range stagedEvents {
				if _, _, err := s.transcriptStore.AdmitUserEvent(ctx, transcriptstore.AdmitUserEventInput{
					StreamUID: stagedStream.UID, OwnerID: stagedStream.OwnerID, ClientMessageID: event.ClientMessageID,
				}); err != nil {
					return report, fmt.Errorf("admit recovered IM input for %s: %w", state.SessionID, err)
				}
			}
		}
		if configured {
			if _, err := s.transcriptStore.RecoverFailedDeliveryIntents(
				ctx, binding.OwnerUserID, binding.SessionID, transcriptIMRecoverMax,
			); err != nil {
				return report, err
			}
			if err := s.drainTranscriptIMRoute(ctx, binding.OwnerUserID, binding.SessionID); err != nil {
				return report, err
			}
			report.Restored++
			// Transcript delivery receipts are authoritative for this route. The
			// JSONL journal remains a compatibility projection and must never be
			// replayed as a second outbound source.
			continue
		}
		report.Restored++
		delivery, err := s.loadIMOutboundDeliveryState(imOutboundStateKey(binding.SessionID))
		if err != nil {
			return report, err
		}
		replayed, err := s.replayIMOutboundSession(ctx, binding.SessionID, delivery.CommittedEventID)
		if err != nil {
			return report, err
		}
		report.Replayed += replayed
	}
	return report, nil
}

func (s *Server) revokeIMOutboundRoutes(platform string, senderID string) error {
	if s == nil || s.runtimeStore == nil {
		return nil
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	senderID = strings.TrimSpace(senderID)
	entries, err := s.runtimeStore.List(imOutboundRouteNamespace)
	if err != nil {
		return fmt.Errorf("list IM outbound routes for revoke: %w", err)
	}
	var cleanupErrors []error
	for _, entry := range entries {
		var state imOutboundRouteState
		if err := decodeRuntimeValue(entry.Value, &state); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("decode IM outbound route %s: %w", entry.Key, err))
			continue
		}
		if !strings.EqualFold(state.Platform, platform) || strings.TrimSpace(state.SenderID) != senderID {
			continue
		}
		if unbinder, ok := s.imOutbound.(adaptercommon.SessionOutboundUnbinder); ok {
			unbinder.Unbind(state.SessionID)
		}
		if err := s.revokeTranscriptIMRoute(context.Background(), state.OwnerUserID, state.SessionID); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		if _, err := s.runtimeStore.Delete(imOutboundRouteNamespace, entry.Key); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		if _, err := s.runtimeStore.Delete(imOutboundDeliveryNamespace, imOutboundStateKey(state.SessionID)); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		if state.ContextSecretID != "" && s.secretStore != nil {
			if _, err := s.secretStore.DeleteForUser(state.ContextSecretID, state.OwnerUserID); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	return errors.Join(cleanupErrors...)
}

func (s *Server) restoreIMOutboundBinding(state imOutboundRouteState) (adaptercommon.IMSessionBinding, string, error) {
	if state.SchemaVersion != 1 || strings.TrimSpace(state.SessionID) == "" || strings.TrimSpace(state.OwnerUserID) == "" {
		return adaptercommon.IMSessionBinding{}, "", errors.New("persisted IM outbound route has an unsupported schema or missing identity")
	}
	binding := adaptercommon.IMSessionBinding{
		SessionID: state.SessionID, Platform: state.Platform, ChatID: state.ChatID,
		SenderID: state.SenderID, MessageID: state.MessageID, TargetType: state.TargetType,
		TargetID: state.TargetID, OwnerUserID: state.OwnerUserID,
	}
	if state.ContextSecretID != "" {
		secret, found, err := s.secretStore.ResolveForUser(state.ContextSecretID, state.OwnerUserID)
		if err != nil {
			return adaptercommon.IMSessionBinding{}, "", fmt.Errorf("resolve encrypted IM outbound context: %w", err)
		}
		if !found || secret.Provider != internalIMRouteSecretProvider || strings.TrimSpace(secret.Value) == "" {
			return adaptercommon.IMSessionBinding{}, fmt.Sprintf("%s: encrypted IM route context is unavailable", state.SessionID), nil
		}
		binding.ContextToken = secret.Value
	} else if strings.EqualFold(state.Platform, "wechat") {
		return adaptercommon.IMSessionBinding{}, fmt.Sprintf("%s: encrypted WeChat route context is unavailable", state.SessionID), nil
	}
	return binding, "", nil
}

func (s *Server) replayIMOutboundSession(ctx context.Context, sessionID string, afterEventID int64) (int, error) {
	replayed := 0
	cursor := afterEventID
	for replayed < imOutboundRecoveryLimit {
		if err := context.Cause(ctx); err != nil {
			return replayed, err
		}
		entries, err := s.eventJournal.ReadAfter(sessionID, cursor, imOutboundRecoveryBatchSize)
		if err != nil {
			return replayed, fmt.Errorf("read IM outbound recovery journal %s: %w", sessionID, err)
		}
		if len(entries) == 0 {
			return replayed, nil
		}
		for _, entry := range entries {
			cursor = entry.EventID
			if !imOutboundReplayableMessage(entry.Message) {
				continue
			}
			if !s.imOutbound.Enqueue(adaptercommon.SessionOutboundEvent{
				SessionID: sessionID, EventID: entry.EventID,
				Message: adaptercommon.ServerMessage(entry.Message),
			}) {
				return replayed, fmt.Errorf("enqueue IM outbound recovery event %s/%d", sessionID, entry.EventID)
			}
			replayed++
			if replayed >= imOutboundRecoveryLimit {
				return replayed, fmt.Errorf("IM outbound recovery limit exceeded for %s", sessionID)
			}
		}
		if len(entries) < imOutboundRecoveryBatchSize {
			return replayed, nil
		}
	}
	return replayed, nil
}

func (s *Server) loadIMOutboundDeliveryState(key string) (imOutboundDeliveryState, error) {
	entry, found, err := s.runtimeStore.Get(imOutboundDeliveryNamespace, key)
	if err != nil {
		return imOutboundDeliveryState{}, fmt.Errorf("load IM outbound delivery state: %w", err)
	}
	if !found {
		return imOutboundDeliveryState{SchemaVersion: 1}, nil
	}
	var state imOutboundDeliveryState
	if err := decodeRuntimeValue(entry.Value, &state); err != nil {
		return imOutboundDeliveryState{}, fmt.Errorf("decode IM outbound delivery state: %w", err)
	}
	if state.SchemaVersion != 1 {
		return imOutboundDeliveryState{}, errors.New("IM outbound delivery state has an unsupported schema")
	}
	return state, nil
}

func decodeRuntimeValue(value any, target any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func imOutboundReplayableMessage(message eventjournal.Message) bool {
	switch strings.TrimSpace(fmt.Sprint(message["type"])) {
	case "content_delta", "message", "runner_checkpoint", "runner_finished", "reasoning_delta", "thinking", "tool_use", "tool_use_complete", "tool_result", "permission_request", "error", "message_complete":
		return true
	default:
		return false
	}
}

func imOutboundTerminalMessage(messageType string) bool {
	switch strings.TrimSpace(messageType) {
	case "message_complete", "error":
		return true
	default:
		return false
	}
}

func internalIMRouteSecretID(sessionID string, userID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(userID) + "\x00" + strings.TrimSpace(sessionID)))
	return internalIMRouteSecretPrefix + hex.EncodeToString(sum[:16])
}

func imOutboundStateKey(sessionID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
	return hex.EncodeToString(sum[:16])
}

func boundedIMOutboundError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 512 {
		return value
	}
	return value[:512] + "..."
}

func isInternalSecret(secret secretstore.Secret) bool {
	return secret.Provider == internalIMRouteSecretProvider ||
		secret.Provider == messageChannelSecretProvider ||
		strings.HasPrefix(secret.ID, internalIMRouteSecretPrefix) ||
		strings.HasPrefix(secret.ID, messageChannelSecretPrefix)
}

func isReservedInternalSecretID(id string) bool {
	id = strings.TrimSpace(id)
	return strings.HasPrefix(id, internalIMRouteSecretPrefix) || strings.HasPrefix(id, messageChannelSecretPrefix)
}
