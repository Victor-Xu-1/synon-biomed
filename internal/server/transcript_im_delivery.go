package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptIMWorkerID    = "server:im-delivery"
	transcriptIMMaxAttempts = 5
	transcriptIMClaimTTL    = time.Minute
	transcriptIMRenewBefore = 20 * time.Second
	transcriptIMRecoverMax  = 1000
)

type transcriptIMClaimKey struct {
	sessionID string
	eventID   int64
}

func (s *Server) registerTranscriptIMRoute(ownerID, sessionID string) {
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || ownerID == "" || sessionID == "" {
		return
	}
	s.transcriptIMMu.Lock()
	s.transcriptIMRoutes[sessionID] = ownerID
	s.transcriptIMMu.Unlock()
}

func (s *Server) transcriptIMRouteOwner(sessionID string) (string, bool, error) {
	if s == nil || s.runtimeStore == nil {
		return "", false, nil
	}
	entry, found, err := s.runtimeStore.Get(imOutboundRouteNamespace, imOutboundStateKey(sessionID))
	if err != nil || !found {
		return "", false, err
	}
	var state imOutboundRouteState
	if err := decodeRuntimeValue(entry.Value, &state); err != nil {
		return "", true, err
	}
	if state.SchemaVersion != 1 || strings.TrimSpace(state.SessionID) != strings.TrimSpace(sessionID) || strings.TrimSpace(state.OwnerUserID) == "" {
		return "", true, errors.New("persisted IM transcript route has invalid ownership")
	}
	return strings.TrimSpace(state.OwnerUserID), true, nil
}

func (s *Server) configureTranscriptIMRoute(ctx context.Context, ownerID, sessionID string) (bool, error) {
	if s == nil || s.transcriptStore == nil {
		return false, nil
	}
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	s.transcriptIMMu.Lock()
	registeredOwner, registered := s.transcriptIMRoutes[sessionID]
	s.transcriptIMMu.Unlock()
	if registered && registeredOwner != ownerID {
		return false, transcriptstore.ErrOwnerMismatch
	}
	if persistedOwner, found, err := s.transcriptIMRouteOwner(sessionID); err != nil {
		return false, err
	} else if found && persistedOwner != ownerID {
		return false, transcriptstore.ErrOwnerMismatch
	}
	stream, found, err := s.transcriptStore.GetStreamBySession(ctx, ownerID, sessionID)
	if err != nil || !found {
		return false, err
	}
	if stream.Kind != transcriptstore.StreamKindStandalone && stream.Kind != transcriptstore.StreamKindTaskRun {
		return false, transcriptstore.ErrEventConflict
	}
	if _, _, err := s.transcriptStore.ActivateDeliveryRoute(ctx, ownerID, stream.UID, sessionID); err != nil {
		return false, err
	}
	s.registerTranscriptIMRoute(ownerID, sessionID)
	if err := s.signalTranscriptDeliveryIfRunnable(ctx, ownerID, sessionID); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) revokeTranscriptIMRoute(ctx context.Context, ownerID, sessionID string) error {
	if s == nil {
		return nil
	}
	s.transcriptIMMu.Lock()
	delete(s.transcriptIMRoutes, sessionID)
	for key := range s.transcriptIMClaims {
		if key.sessionID == sessionID {
			delete(s.transcriptIMClaims, key)
		}
	}
	s.transcriptIMMu.Unlock()
	if s.transcriptStore == nil {
		return nil
	}
	stream, found, err := s.transcriptStore.GetStreamBySession(ctx, ownerID, sessionID)
	if err != nil || !found {
		return err
	}
	route, found, err := s.transcriptStore.GetDeliveryRoute(ctx, ownerID, stream.UID, sessionID)
	if err != nil || !found || route.Status == "revoked" {
		return err
	}
	_, err = s.transcriptStore.RevokeDeliveryRoute(ctx, ownerID, stream.UID, sessionID, route.Generation)
	return err
}

func (s *Server) drainTranscriptIMDeliveries(ctx context.Context) error {
	if s == nil || s.transcriptStore == nil || s.imOutbound == nil {
		return nil
	}
	s.transcriptIMMu.Lock()
	routes := make(map[string]string, len(s.transcriptIMRoutes))
	for sessionID, ownerID := range s.transcriptIMRoutes {
		routes[sessionID] = ownerID
	}
	s.transcriptIMMu.Unlock()
	for sessionID, ownerID := range routes {
		if err := s.drainTranscriptIMRoute(ctx, ownerID, sessionID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) drainTranscriptIMRoute(ctx context.Context, ownerID, sessionID string) error {
	result, err := s.transcriptStore.ClaimNextDelivery(ctx, transcriptstore.ClaimDeliveryInput{
		OwnerID: ownerID, Destination: sessionID, WorkerID: transcriptIMWorkerID, TTL: transcriptIMClaimTTL,
	})
	if err != nil || !result.Claimed {
		return err
	}
	message, err := transcriptIMMessage(result.Claim)
	if err != nil {
		_, failErr := s.transcriptStore.FailDelivery(ctx, transcriptstore.FailDeliveryInput{
			Claim: result.Claim, ErrorCode: "im_projection_failed", RetryAfter: time.Second, MaxAttempts: transcriptIMMaxAttempts,
		})
		if failErr == nil {
			failErr = s.signalTranscriptDeliveryIfRunnable(ctx, ownerID, sessionID)
		}
		return errors.Join(err, failErr)
	}
	key := transcriptIMClaimKey{sessionID: sessionID, eventID: result.Claim.PublicationSeq}
	s.transcriptIMMu.Lock()
	if existing, exists := s.transcriptIMClaims[key]; exists && existing.AttemptCount >= result.Claim.AttemptCount {
		s.transcriptIMMu.Unlock()
		return errors.New("transcript IM delivery claim is already registered")
	}
	s.transcriptIMClaims[key] = result.Claim
	s.transcriptIMMu.Unlock()
	if s.imOutbound.Enqueue(adaptercommon.SessionOutboundEvent{
		SessionID: sessionID, EventID: result.Claim.PublicationSeq,
		ReceiptToken: result.Claim.ClaimToken, Message: message,
	}) {
		return nil
	}
	if _, found := s.transcriptIMClaim(key, result.Claim.ClaimToken); !found {
		// The dispatcher reports queue saturation synchronously through OnResult.
		return nil
	}
	_, failErr := s.transcriptStore.FailDelivery(ctx, transcriptstore.FailDeliveryInput{
		Claim: result.Claim, ErrorCode: "im_delivery_failed", RetryAfter: time.Second, MaxAttempts: transcriptIMMaxAttempts,
	})
	if failErr == nil {
		s.removeTranscriptIMClaim(key, result.Claim.ClaimToken)
		failErr = s.signalTranscriptDeliveryIfRunnable(ctx, ownerID, sessionID)
	}
	return failErr
}

func (s *Server) heartbeatTranscriptIMClaims(ctx context.Context) error {
	if s == nil || s.transcriptStore == nil {
		return nil
	}
	now := time.Now().UTC()
	s.transcriptIMMu.Lock()
	claims := make(map[transcriptIMClaimKey]transcriptstore.DeliveryClaim, len(s.transcriptIMClaims))
	for key, claim := range s.transcriptIMClaims {
		if claim.ExpiresAt.Sub(now) <= transcriptIMRenewBefore {
			claims[key] = claim
		}
	}
	s.transcriptIMMu.Unlock()
	for key, claim := range claims {
		result, err := s.transcriptStore.HeartbeatDelivery(ctx, transcriptstore.HeartbeatDeliveryInput{
			Claim: claim, TTL: transcriptIMClaimTTL,
		})
		if errors.Is(err, transcriptstore.ErrDeliveryClaimStale) {
			s.removeTranscriptIMClaim(key, claim.ClaimToken)
			continue
		}
		if err != nil {
			return err
		}
		if result.Renewed {
			s.transcriptIMMu.Lock()
			if current, found := s.transcriptIMClaims[key]; found && current.ClaimToken == claim.ClaimToken {
				current.ExpiresAt = result.ExpiresAt
				s.transcriptIMClaims[key] = current
			}
			s.transcriptIMMu.Unlock()
		}
	}
	return nil
}

func (s *Server) settleTranscriptIMDelivery(
	ctx context.Context,
	result adaptercommon.SessionOutboundResult,
) (bool, error) {
	if s == nil || s.transcriptStore == nil {
		return false, nil
	}
	key := transcriptIMClaimKey{sessionID: strings.TrimSpace(result.SessionID), eventID: result.EventID}
	claim, found := s.transcriptIMClaim(key, result.ReceiptToken)
	if !found {
		if strings.TrimSpace(result.ReceiptToken) != "" {
			return true, transcriptstore.ErrDeliveryClaimStale
		}
		return false, nil
	}
	if claim.ClaimToken != result.ReceiptToken {
		return true, transcriptstore.ErrDeliveryClaimStale
	}
	if !result.Delivery.Delivered {
		_, err := s.transcriptStore.FailDelivery(ctx, transcriptstore.FailDeliveryInput{
			Claim: claim, ErrorCode: "im_delivery_failed", RetryAfter: time.Second, MaxAttempts: transcriptIMMaxAttempts,
		})
		if err == nil {
			s.removeTranscriptIMClaim(key, claim.ClaimToken)
			err = s.signalTranscriptDeliveryIfRunnable(ctx, claim.OwnerID, claim.Destination)
		}
		return true, err
	}
	if _, err := s.transcriptStore.AcknowledgeDelivery(ctx, transcriptstore.AcknowledgeDeliveryInput{Claim: claim}); err != nil {
		return true, err
	}
	s.removeTranscriptIMClaim(key, claim.ClaimToken)
	return true, s.drainTranscriptIMRoute(ctx, claim.OwnerID, claim.Destination)
}

func (s *Server) transcriptIMClaim(key transcriptIMClaimKey, token string) (transcriptstore.DeliveryClaim, bool) {
	s.transcriptIMMu.Lock()
	defer s.transcriptIMMu.Unlock()
	claim, found := s.transcriptIMClaims[key]
	return claim, found && claim.ClaimToken == token
}

func (s *Server) removeTranscriptIMClaim(key transcriptIMClaimKey, token string) {
	s.transcriptIMMu.Lock()
	if claim, found := s.transcriptIMClaims[key]; found && claim.ClaimToken == token {
		delete(s.transcriptIMClaims, key)
	}
	s.transcriptIMMu.Unlock()
}

func transcriptIMMessage(claim transcriptstore.DeliveryClaim) (adaptercommon.ServerMessage, error) {
	payload, err := transcriptPayloadObject(claim.ResolvedPayloadJSON)
	if err != nil {
		return nil, err
	}
	text := transcriptPayloadText(payload)
	switch claim.Event.Type {
	case "content_delta", "content_reset":
		return adaptercommon.ServerMessage{"type": "content_delta", "text": text}, nil
	case "assistant_message":
		return adaptercommon.ServerMessage{"type": "content_delta", "text": text}, nil
	case "runner_finished":
		status := strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["status"])))
		if status == "completed" {
			return adaptercommon.ServerMessage{"type": "message_complete", "status": status}, nil
		}
		if strings.TrimSpace(text) == "" {
			text = "task " + firstNonEmpty(status, "failed")
		}
		return adaptercommon.ServerMessage{"type": "error", "status": status, "message": text}, nil
	case "runner_checkpoint":
		message := adaptercommon.ServerMessage{"type": "runner_checkpoint", "text": text}
		for key, value := range payload {
			message[key] = value
		}
		return message, nil
	case "system_message":
		return adaptercommon.ServerMessage{"type": "transcript_noop"}, nil
	case "reasoning_delta", "thinking", "tool_use", "tool_use_complete", "tool_result", "permission_request", "error", "message_complete":
		message := adaptercommon.ServerMessage{"type": claim.Event.Type}
		for key, value := range payload {
			message[key] = value
		}
		return message, nil
	default:
		return nil, fmt.Errorf("unsupported transcript IM event type %q", claim.Event.Type)
	}
}
