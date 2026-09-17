package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type transcriptWebDeliveryCycleState struct {
	Immediate      bool
	RetryAfter     time.Duration
	HeartbeatAfter time.Duration
}

func runTranscriptWebDeliveryCoordinator(
	ctx context.Context,
	wake <-chan struct{},
	cycle func(context.Context) (transcriptWebDeliveryCycleState, error),
) {
	if ctx == nil || cycle == nil {
		return
	}
	consecutiveErrors := 0
	for {
		state, err := cycle(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if errors.Is(err, transcriptstore.ErrSchemaUnavailable) {
				log.Printf("transcript Web delivery stopped: store unavailable: %v", err)
				return
			}
			consecutiveErrors++
			retryAfter := transcriptDeliveryRetry
			if consecutiveErrors > 1 {
				exponent := consecutiveErrors - 1
				if exponent > transcriptDeliveryMaxErrorBackoffExponent {
					exponent = transcriptDeliveryMaxErrorBackoffExponent
				}
				retryAfter = transcriptDeliveryRetry << exponent
			}
			if retryAfter > transcriptDeliveryMaxErrorBackoff {
				retryAfter = transcriptDeliveryMaxErrorBackoff
			}
			log.Printf("transcript Web delivery cycle failed: %v (%T); retrying in %s", err, err, retryAfter)
			if state.RetryAfter <= 0 || retryAfter > state.RetryAfter {
				state.RetryAfter = retryAfter
			}
		} else {
			consecutiveErrors = 0
		}
		if state.Immediate {
			runtime.Gosched()
			continue
		}
		wait := state.RetryAfter
		if state.HeartbeatAfter > 0 && (wait <= 0 || state.HeartbeatAfter < wait) {
			wait = state.HeartbeatAfter
		}
		if wait <= 0 {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			stopTranscriptWebDeliveryTimer(timer)
			return
		case <-wake:
			stopTranscriptWebDeliveryTimer(timer)
		case <-timer.C:
		}
	}
}

func stopTranscriptWebDeliveryTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

const (
	transcriptWebDestination                = "ws"
	transcriptWebWorkerID                   = "server:web-projection"
	transcriptWebBatch                      = 100
	transcriptWebCoordinatorProjectionBatch = 1
	transcriptWebCoordinatorDeliveryBatch   = 32
	transcriptWebReplayLimit                = 1000
	transcriptWebClaimTTL                   = time.Minute
	transcriptDeliveryRetry                 = time.Second
	transcriptDeliveryMaxAttempts           = 5
	transcriptProjectionStaleMaxAttempts    = 100

	transcriptDeliveryMaxErrorBackoff         = 30 * time.Second
	transcriptDeliveryMaxErrorBackoffExponent = 5
)

var (
	errTranscriptWebReplayIncomplete = errors.New("transcript web replay preparation is incomplete")
	errTranscriptWebReplayPoisoned   = errors.New("transcript web replay contains an irrecoverable delivery")
)

func (s *Server) startTranscriptWebDelivery() {
	if s == nil || s.transcriptStore == nil || s.workspaceStore == nil || s.transcriptContractErr != nil || s.transcriptDeliveryDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.transcriptDeliveryStop = cancel
	s.transcriptDeliveryDone = make(chan struct{})
	s.transcriptDeliveryWake = make(chan struct{}, 1)
	go func() {
		defer close(s.transcriptDeliveryDone)
		runTranscriptWebDeliveryCoordinator(ctx, s.transcriptDeliveryWake, s.runTranscriptWebDeliveryPass)
	}()
}

func (s *Server) signalTranscriptWebDelivery() {
	if s == nil || s.transcriptDeliveryWake == nil {
		return
	}
	select {
	case s.transcriptDeliveryWake <- struct{}{}:
	default:
	}
}

func (s *Server) runTranscriptWebDeliveryCycle(ctx context.Context) error {
	_, err := s.runTranscriptWebDeliveryPass(ctx)
	return err
}

func (s *Server) runTranscriptWebDeliveryPass(ctx context.Context) (transcriptWebDeliveryCycleState, error) {
	var state transcriptWebDeliveryCycleState
	// Realtime delivery gets the first bounded slice of every coordinator
	// pass. Previously a global batch of up to 100 unrelated read-model repairs
	// ran first; one large historical conversation could therefore delay a new
	// conversation's already-durable tokens for more than a minute.
	preDrained, preRemaining, err := s.drainTranscriptWebDeliveriesPass(ctx)
	if err != nil {
		return state, err
	}
	if preDrained == transcriptWebCoordinatorDeliveryBatch && preRemaining {
		// A bounded realtime slice ran to its limit and still has runnable work.
		// Continue directly to the second delivery slice: inserting even one
		// unrelated historical projection here lets internal checkpoints outrun
		// and eventually rebase the active conversation's visible text deltas.
		// A short slice that failed on a stale projection does not take this path;
		// it must immediately give the projector a chance to self-repair.
		state.Immediate = true
	} else {
		beforeReadModel, err := s.transcriptWebReadModelProgress(ctx)
		if err != nil {
			return state, err
		}
		// Advance one background projection chunk only when realtime delivery is
		// caught up or blocked. The coordinator self-schedules while work advances,
		// so repair throughput remains continuous without delaying live tokens.
		if err := s.runTranscriptWebReadModelBatch(ctx, transcriptWebCoordinatorProjectionBatch); err != nil {
			return state, err
		}
		afterReadModel, err := s.transcriptWebReadModelProgress(ctx)
		if err != nil {
			return state, err
		}
		if afterReadModel.pending {
			// Read-model repair and realtime delivery share the same durable source,
			// but the history projector is never a live-delivery barrier. Keep
			// advancing bounded repair work here while ordered Transcript claims
			// continue to publish directly in this same coordinator pass.
			if afterReadModel.advancedSince(beforeReadModel) {
				state.Immediate = true
			} else {
				state.RetryAfter = transcriptDeliveryRetry
			}
		}
	}
	recovered, err := s.recoverTranscriptWebDeliveriesPass(ctx)
	if err != nil {
		return state, err
	}
	postDrained, remaining, err := s.drainTranscriptWebDeliveriesPass(ctx)
	if err != nil {
		return state, err
	}
	drained := preDrained + postDrained
	if remaining {
		if recovered > 0 || drained > 0 {
			state.Immediate = true
		} else {
			state.RetryAfter = transcriptDeliveryRetry
		}
	}
	if err := s.heartbeatTranscriptIMClaims(ctx); err != nil {
		return state, err
	}
	if err := s.drainTranscriptIMDeliveries(ctx); err != nil {
		return state, err
	}
	imRetry, heartbeat, err := s.transcriptIMDeliverySchedule(ctx, time.Now().UTC())
	if err != nil {
		return state, err
	}
	state.RetryAfter = earlierTranscriptWebDeliveryDelay(state.RetryAfter, imRetry)
	state.HeartbeatAfter = heartbeat
	return state, nil
}

func earlierTranscriptWebDeliveryDelay(current, candidate time.Duration) time.Duration {
	if candidate <= 0 || current > 0 && current <= candidate {
		return current
	}
	return candidate
}

type transcriptWebReadModelProgressState struct {
	found            bool
	status           string
	branchGeneration int64
	through          int64
	sourceRevision   int64
	messageCount     int
	artifactCount    int
	sourceChain      string
}

type transcriptWebReadModelProgressSnapshot struct {
	pending bool
	states  map[string]transcriptWebReadModelProgressState
}

func (current transcriptWebReadModelProgressSnapshot) advancedSince(
	previous transcriptWebReadModelProgressSnapshot,
) bool {
	if !current.pending {
		return false
	}
	for key := range previous.states {
		if _, found := current.states[key]; !found {
			return true
		}
	}
	for key, next := range current.states {
		before, found := previous.states[key]
		if !found {
			continue
		}
		if next.found != before.found || next.status != before.status ||
			next.branchGeneration != before.branchGeneration || next.through > before.through ||
			next.sourceRevision != before.sourceRevision || next.messageCount != before.messageCount ||
			next.artifactCount != before.artifactCount || next.sourceChain != before.sourceChain {
			return true
		}
	}
	return false
}

func (s *Server) transcriptWebReadModelProgress(ctx context.Context) (transcriptWebReadModelProgressSnapshot, error) {
	snapshot := transcriptWebReadModelProgressSnapshot{states: map[string]transcriptWebReadModelProgressState{}}
	if s == nil || s.transcriptWebReadModel == nil || s.transcriptStore == nil || s.transcriptContractErr != nil {
		return snapshot, nil
	}
	owners, err := s.transcriptWebReadModel.ListTranscriptWebProjectionOwners(ctx, transcriptWebBatch)
	if err != nil {
		return snapshot, err
	}
	for _, ownerID := range owners {
		work, err := s.transcriptWebReadModel.ListTranscriptWebProjectionWork(ctx, ownerID, transcriptWebBatch)
		if err != nil {
			return snapshot, err
		}
		for _, item := range work {
			if item.Status == transcriptstore.TranscriptWebProjectionWorkNotReady && item.ProjectionStatus == "quarantined" {
				continue
			}
			key := strings.Join([]string{item.OwnerID, item.StreamUID, item.BranchID}, "\x00")
			if item.Status == transcriptstore.TranscriptWebProjectionWorkStale &&
				item.Reason == "projection_projector_version_stale" {
				// An older projector row is intentionally invalid under the
				// current state decoder. Record only bounded progress metadata so
				// the cycle can replace it; reading it through the serving fence
				// first would deadlock every projector upgrade.
				snapshot.pending = true
				snapshot.states[key] = transcriptWebReadModelProgressState{
					found: true, status: item.ProjectionStatus,
				}
				continue
			}
			fence, err := s.transcriptWebReadModel.GetTranscriptWebProjectionFence(
				ctx, item.OwnerID, item.StreamUID, item.BranchID,
			)
			if err != nil {
				return snapshot, err
			}
			snapshot.pending = true
			snapshot.states[key] = transcriptWebReadModelProgressState{
				found: fence.StateFound, status: fence.StateStatus,
				branchGeneration: fence.StateBranchGeneration,
				through:          fence.StateThroughPublicationSequence, sourceRevision: fence.StateSourceRevision,
				messageCount: fence.StateMessageCount, artifactCount: fence.StateArtifactReferenceCount,
				sourceChain: fence.StateSourceChainSHA256,
			}
		}
	}
	return snapshot, nil
}

func (s *Server) transcriptIMDeliverySchedule(
	ctx context.Context,
	now time.Time,
) (time.Duration, time.Duration, error) {
	if s == nil || s.transcriptStore == nil || s.imOutbound == nil {
		return 0, 0, nil
	}
	s.transcriptIMMu.Lock()
	routes := make(map[string]string, len(s.transcriptIMRoutes))
	for sessionID, ownerID := range s.transcriptIMRoutes {
		routes[sessionID] = ownerID
	}
	activeRoutes := make(map[string]bool, len(s.transcriptIMClaims))
	heartbeatAfter := time.Duration(0)
	for key, claim := range s.transcriptIMClaims {
		activeRoutes[key.sessionID] = true
		delay := claim.ExpiresAt.Sub(now) - transcriptIMRenewBefore
		if delay <= 0 {
			delay = time.Nanosecond
		}
		heartbeatAfter = earlierTranscriptWebDeliveryDelay(heartbeatAfter, delay)
	}
	s.transcriptIMMu.Unlock()

	retryAfter := time.Duration(0)
	for sessionID, ownerID := range routes {
		if activeRoutes[sessionID] {
			continue
		}
		settlement, err := s.transcriptStore.DeliverySettlementState(ctx, ownerID, sessionID)
		if err != nil {
			return 0, 0, err
		}
		if settlement.Runnable {
			retryAfter = transcriptDeliveryRetry
		}
	}
	return retryAfter, heartbeatAfter, nil
}

func (s *Server) stopTranscriptWebDelivery(ctx context.Context) error {
	if s == nil || s.transcriptDeliveryStop == nil || s.transcriptDeliveryDone == nil {
		return nil
	}
	s.transcriptDeliveryStop()
	select {
	case <-s.transcriptDeliveryDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) drainTranscriptWebDeliveries(ctx context.Context) error {
	for {
		drained, remaining, err := s.drainTranscriptWebDeliveriesPass(ctx)
		if err != nil {
			return err
		}
		if !remaining {
			return nil
		}
		if drained == 0 {
			return errTranscriptWebReplayIncomplete
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func (s *Server) drainTranscriptWebDeliveriesPass(ctx context.Context) (int, bool, error) {
	if s == nil || s.transcriptStore == nil || s.workspaceStore == nil {
		return 0, false, nil
	}
	if s.transcriptContractErr != nil {
		return 0, false, s.transcriptContractErr
	}
	owners, err := s.transcriptStore.ListDeliveryOwners(ctx, transcriptWebDestination, transcriptWebBatch)
	if err != nil {
		return 0, false, err
	}
	drained := 0
	for _, ownerID := range owners {
		result, err := s.drainTranscriptWebOwnerPass(ctx, ownerID, false)
		if err != nil {
			return drained, false, err
		}
		drained += result
	}
	remaining, err := s.hasRunnableTranscriptDeliveries(ctx, transcriptWebDestination)
	return drained, remaining, err
}

func (s *Server) hasRunnableTranscriptDeliveries(ctx context.Context, destination string) (bool, error) {
	owners, err := s.transcriptStore.ListDeliveryOwners(ctx, destination, transcriptWebBatch)
	if err != nil {
		return false, err
	}
	for _, ownerID := range owners {
		settlement, err := s.transcriptStore.DeliverySettlementState(ctx, ownerID, destination)
		if err != nil {
			return false, err
		}
		if settlement.Runnable {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) signalTranscriptDeliveryIfRunnable(ctx context.Context, ownerID, destination string) error {
	settlement, err := s.transcriptStore.DeliverySettlementState(ctx, ownerID, destination)
	if err != nil {
		return err
	}
	if settlement.Runnable {
		s.signalTranscriptWebDelivery()
	}
	return nil
}

func (s *Server) recoverTranscriptWebDeliveries(ctx context.Context) error {
	_, err := s.recoverTranscriptWebDeliveriesPass(ctx)
	return err
}

func (s *Server) recoverTranscriptWebDeliveriesPass(ctx context.Context) (int64, error) {
	recovered, err := s.transcriptStore.RecoverRetryableWebProjectionDeliveryIntents(ctx, transcriptWebBatch)
	if err != nil {
		return 0, err
	}
	rebased, err := s.transcriptStore.RebaseCompletedWebDeliveryIntents(ctx, transcriptWebBatch)
	if err != nil {
		return recovered, err
	}
	recovered += rebased
	terminalOwners, err := s.transcriptStore.ListTerminalProjectionOwners(ctx, transcriptWebBatch)
	if err != nil {
		return recovered, err
	}
	for _, ownerID := range terminalOwners {
		count, err := s.transcriptStore.RecoverTerminalDeliveryIntents(ctx, ownerID)
		if err != nil {
			return recovered, err
		}
		recovered += count
	}
	return recovered, nil
}

func (s *Server) prepareTranscriptWebReplay(ctx context.Context, ownerID string) error {
	if s == nil || s.transcriptStore == nil || s.workspaceStore == nil {
		return nil
	}
	if s.transcriptContractErr != nil {
		return s.transcriptContractErr
	}
	if _, err := s.transcriptStore.RecoverTerminalDeliveryIntents(ctx, ownerID); err != nil {
		return err
	}
	if _, err := s.transcriptStore.RecoverFailedDeliveryIntents(ctx, ownerID, transcriptWebDestination, transcriptWebReplayLimit); err != nil {
		return err
	}
	return s.drainTranscriptWebOwner(ctx, ownerID, true)
}

func (s *Server) drainTranscriptWebOwner(ctx context.Context, ownerID string, requireComplete bool) error {
	_, err := s.drainTranscriptWebOwnerPass(ctx, ownerID, requireComplete)
	return err
}

func (s *Server) drainTranscriptWebOwnerPass(ctx context.Context, ownerID string, requireComplete bool) (int, error) {
	s.transcriptDeliveryMu.Lock()
	defer s.transcriptDeliveryMu.Unlock()
	processed := 0
	deliveryLimit := transcriptWebCoordinatorDeliveryBatch
	if requireComplete {
		deliveryLimit = transcriptWebReplayLimit
	}
	for delivered := 0; delivered < deliveryLimit; delivered++ {
		result, err := s.transcriptStore.ClaimNextDelivery(ctx, transcriptstore.ClaimDeliveryInput{
			OwnerID: ownerID, Destination: transcriptWebDestination, WorkerID: transcriptWebWorkerID, TTL: transcriptWebClaimTTL,
		})
		if err != nil {
			return processed, err
		}
		if !result.Claimed {
			settlement, err := s.transcriptStore.DeliverySettlementState(ctx, ownerID, transcriptWebDestination)
			if err != nil {
				return processed, err
			}
			if settlement.Poisoned {
				if requireComplete {
					return processed, errTranscriptWebReplayPoisoned
				}
				return processed, nil
			}
			if requireComplete && settlement.Unsettled() {
				return processed, errTranscriptWebReplayIncomplete
			}
			return processed, nil
		}
		processed++
		if err := s.publishTranscriptWebClaim(ctx, result.Claim); err != nil {
			_, failErr := s.transcriptStore.FailDelivery(ctx, transcriptstore.FailDeliveryInput{
				Claim: result.Claim, ErrorCode: transcriptDeliveryErrorCode(err), RetryAfter: transcriptDeliveryRetry,
				MaxAttempts: transcriptDeliveryMaxAttemptsForError(err),
			})
			if failErr != nil {
				return processed, errors.Join(err, failErr)
			}
			continue
		}
		if _, err := s.transcriptStore.AcknowledgeDelivery(ctx, transcriptstore.AcknowledgeDeliveryInput{Claim: result.Claim}); err != nil {
			return processed, err
		}
	}
	settlement, err := s.transcriptStore.DeliverySettlementState(ctx, ownerID, transcriptWebDestination)
	if err != nil {
		return processed, err
	}
	if settlement.Poisoned {
		if requireComplete {
			return processed, errTranscriptWebReplayPoisoned
		}
		return processed, nil
	}
	if requireComplete && settlement.Unsettled() {
		return processed, errTranscriptWebReplayIncomplete
	}
	return processed, nil
}

func (s *Server) publishTranscriptWebClaim(ctx context.Context, claim transcriptstore.DeliveryClaim) error {
	stream, err := s.transcriptStore.GetStream(ctx, claim.StreamUID, claim.OwnerID)
	if err != nil {
		return err
	}
	if stream.Kind != transcriptstore.StreamKindFrameRef || stream.SessionID == "" {
		return transcriptstore.ErrTerminalProjectionUnavailable
	}
	switch claim.Event.Type {
	case "user_input_response", transcriptstore.TerminalToolRecoveryEventType:
		// These are durable model/runtime settlement facts. They intentionally have
		// no user-facing projection and must not depend on a live Frame or read-model fence.
		return nil
	}
	payload, err := transcriptPayloadObject(claim.ResolvedPayloadJSON)
	if err != nil {
		return err
	}
	attempt := int64(0)
	if claim.Event.RunnerAttempt != nil {
		attempt = *claim.Event.RunnerAttempt
	}
	askUser, richAskUser, err := transcriptAskUserProtocolFact(claim.Event, claim.ResolvedPayloadJSON, payload)
	if err != nil {
		return err
	}
	waitingForModel := transcriptWebModelSelectionInterruption(payload)
	if claim.Event.Type == "runner_checkpoint" && !richAskUser && !waitingForModel &&
		!transcriptRunnerAttemptStarted(payload, attempt) && !transcriptWebRuntimeDrainToolBoundary(payload) {
		// Planning, model-resolution, memory, review-policy and similar durable
		// checkpoints are valuable for recovery/audit, but they have no distinct
		// browser projection. Acknowledging them before projection catch-up keeps
		// the single Transcript Web lane available for visible start, text, tool,
		// AskUser and terminal publications.
		return nil
	}
	// The delivery claim is already an ordered, durable Transcript fact. Publish
	// it directly instead of waiting for the history read model to catch up.
	// History projection remains the authority for paginated/reloaded history,
	// while this delivery lane is the sole authority for live increments. Tying
	// the two together made every token run a projection transaction and let one
	// stale projection add seconds of head-of-line blocking to an active task.
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(stream.FrameID)
	if err != nil {
		return err
	}
	if !found || frameContext.UserID != stream.OwnerID || frameContext.Frame.ID != stream.SessionID ||
		frameContext.Frame.ProjectID != stream.ProjectID || frameContext.Frame.RootFrameID != stream.RootFrameID {
		return transcriptstore.ErrOwnerMismatch
	}
	refs := transcriptArtifactReferences(claim.ArtifactReferences)
	baseID := fmt.Sprintf("transcript-web:%s:%d", stream.UID, claim.PublicationSeq)
	createdAt := claim.Event.CreatedAt.UnixMilli()
	if richAskUser {
		var source workspace.FrameEvent
		if !askUser.Typed {
			if claim.Event.FrameEventID == nil {
				return transcriptstore.ErrEventConflict
			}
			var found bool
			source, found, err = s.workspaceStore.GetFrameEventByID(*claim.Event.FrameEventID)
			if err != nil {
				return err
			}
			if !found || source.FrameID != stream.FrameID {
				return transcriptstore.ErrEventConflict
			}
		}
		phase := "waiting_for_lock"
		if askUser.Kind == "use" || askUser.Pending {
			if err := s.publishWebConfirmationProjection(frameContext, source); err != nil {
				return err
			}
			phase = "validating"
		}
		return s.publishWebRuntimeStatus(frameContext, baseID+":ask-user-runtime", phase, "")
	}
	messageID, err := transcriptAssistantMessageIDFromPayload(payload, stream.SessionID, attempt)
	if err != nil {
		return err
	}
	if messageID == "" {
		messageID = transcriptAssistantMessageID(stream.SessionID, attempt, 1)
	}
	switch claim.Event.Type {
	case "user_message":
		userID := strings.TrimSpace(claim.Event.ClientMessageID)
		if userID == "" {
			userID = strings.TrimSpace(webString(payload["messageUuid"]))
		}
		if userID == "" {
			return transcriptstore.ErrEventConflict
		}
		if err := s.publishWebFrameEvent(frameContext, baseID+":user", "message.userCreated", map[string]any{
			"conversation_id": stream.SessionID, "msg_id": userID, "content": transcriptPayloadText(payload),
			"position": "right", "status": "finish", "hidden": false, "created_at": createdAt,
		}); err != nil {
			return err
		}
		if err := s.publishWebMessageStream(frameContext, baseID+":start", map[string]any{
			"type": "start", "data": map[string]any{}, "msg_id": userID, "turn_id": stream.SessionID,
			"conversation_id": stream.SessionID, "created_at": createdAt, "status": "pending", "artifact_refs": refs,
			"source_publication_sequence": claim.PublicationSeq, "publication_boundary_id": baseID,
		}); err != nil {
			return err
		}
		return s.publishWebRuntimeStatus(frameContext, baseID+":runtime", "waiting_for_lock", "")
	case "content_delta", "content_reset":
		if claim.Event.Type == "content_reset" {
			targetIdentity, narrow, resetErr := transcriptAssistantSegmentResetTarget(payload, stream.SessionID, attempt)
			if resetErr != nil {
				return resetErr
			}
			if narrow {
				if transcriptPayloadText(payload) != "" {
					return transcriptstore.ErrEventConflict
				}
				messageID = targetIdentity
			}
		}
		streamType := "text"
		streamData := any(transcriptPayloadText(payload))
		if firstNonEmpty(webString(payload["block_type"]), webString(payload["blockType"])) == "thinking" {
			streamType = "thinking"
			streamData = map[string]any{"content": transcriptPayloadText(payload), "status": "thinking"}
		}
		streamPayload := map[string]any{
			"type": streamType, "data": streamData, "msg_id": messageID, "turn_id": stream.SessionID,
			"conversation_id": stream.SessionID, "created_at": createdAt, "position": "left", "status": "pending",
			"replace": claim.Event.Type == "content_reset", "artifact_refs": refs,
			"source_publication_sequence": claim.PublicationSeq, "publication_boundary_id": baseID,
		}
		if segment, present, segmentErr := transcriptstore.ParseAssistantSegmentV1(payload); segmentErr != nil {
			return segmentErr
		} else if present {
			streamPayload["assistant_attempt_id"] = transcriptAssistantMessageID(stream.SessionID, attempt, 1)
			// Segment-scoped resets are translated to a precise msg_id replacement
			// above. Keep the internal scope out of the browser protocol so there is
			// only one generic message replacement path on the client.
			if segment.ReplaceScope == transcriptstore.AssistantReplaceScopeAttempt {
				streamPayload["replace_scope"] = segment.ReplaceScope
			}
		}
		return s.publishWebMessageStream(frameContext, baseID+":delta", streamPayload)
	case "assistant_message":
		streamPayload := map[string]any{
			"type": "content", "data": transcriptPayloadText(payload), "msg_id": messageID, "turn_id": stream.SessionID,
			"conversation_id": stream.SessionID, "created_at": createdAt, "position": "left", "status": "finish",
			"replace": true, "artifact_refs": refs,
			"source_publication_sequence": claim.PublicationSeq, "publication_boundary_id": baseID,
		}
		if _, present, segmentErr := transcriptstore.ParseAssistantSegmentV1(payload); segmentErr != nil {
			return segmentErr
		} else if present {
			streamPayload["assistant_attempt_id"] = transcriptAssistantMessageID(stream.SessionID, attempt, 1)
		}
		return s.publishWebMessageStream(frameContext, baseID+":assistant", streamPayload)
	case "runner_finished":
		projection, err := s.transcriptStore.GetTerminalProjection(ctx, stream.OwnerID, stream.UID, claim.Event.EventID)
		if err != nil {
			return err
		}
		projection = s.publicTranscriptTerminalProjection(ctx, stream, projection)
		return s.publishTranscriptTerminal(frameContext, baseID, projection, messageID)
	case transcriptstore.ToolOperationObservationEventType:
		toolPayload, visible, err := transcriptWebToolStreamPayload(claim, payload, stream.SessionID, baseID, refs)
		if err != nil || !visible {
			return err
		}
		return s.publishWebMessageStream(frameContext, baseID+":tool", toolPayload)
	case "runner_checkpoint":
		attemptStarted := transcriptRunnerAttemptStarted(payload, attempt)
		toolBoundary := transcriptWebRuntimeDrainToolBoundary(payload)
		if attemptStarted {
			if err := s.publishWebMessageStream(frameContext, baseID+":start", map[string]any{
				"type": "start", "data": map[string]any{}, "msg_id": messageID,
				"turn_id": stream.SessionID, "conversation_id": stream.SessionID,
				"created_at": createdAt, "position": "left", "status": "pending",
				"artifact_refs":               refs,
				"source_publication_sequence": claim.PublicationSeq, "publication_boundary_id": baseID,
			}); err != nil {
				return err
			}
		}
		if toolBoundary {
			toolPayload, visible, toolErr := transcriptWebToolStreamPayload(
				claim, payload, stream.SessionID, baseID, refs,
			)
			if toolErr != nil {
				return toolErr
			}
			if visible {
				if err := s.publishWebMessageStream(frameContext, baseID+":tool", toolPayload); err != nil {
					return err
				}
			}
		}
		runtimePayload := map[string]any{
			"resource": "acp_tool", "resource_id": frameContext.Frame.AgentName,
			"scope": map[string]any{"kind": "conversation", "id": stream.SessionID},
			"phase": "waiting_for_lock", "artifact_refs": refs,
		}
		if waitingForModel {
			runtimePayload["phase"] = "waiting_input"
			runtimePayload["reason_code"] = sessionRunnerModelProviderUnavailableReasonCode
			runtimePayload["message"] = firstNonEmpty(
				webString(payload["resume_detail"]),
				"No active model configuration is available. Configure or select a model, then continue this same task.",
			)
			if err := s.publishWebFrameEvent(frameContext, baseID+":runtime", "runtime.statusChanged", runtimePayload); err != nil {
				return err
			}
			return s.publishWebFrameEvent(frameContext, baseID+":frame", "frame_update", map[string]any{
				"status": "paused", "runtime_interruption_reason": sessionRunnerModelProviderUnavailableReasonCode,
			})
		}
		if attemptStarted {
			runtimePayload["boundary_kind"] = "attempt"
		}
		if toolBoundary {
			runtimePayload["boundary_kind"] = "tool"
			runtimePayload["tool_call_id"] = strings.TrimSpace(webString(payload["toolCallId"]))
			runtimePayload["tool_name"] = strings.TrimSpace(webString(payload["toolName"]))
			runtimePayload["source_publication_sequence"] = claim.PublicationSeq
			runtimePayload["publication_boundary_id"] = baseID
		}
		return s.publishWebFrameEvent(frameContext, baseID+":runtime", "runtime.statusChanged", runtimePayload)
	case "runner_reclaimed":
		return s.publishWebFrameEvent(frameContext, baseID+":runtime", "runtime.statusChanged", map[string]any{
			"resource": "acp_tool", "resource_id": frameContext.Frame.AgentName,
			"scope": map[string]any{"kind": "conversation", "id": stream.SessionID},
			"phase": "waiting_for_lock", "artifact_refs": refs,
		})
	default:
		return fmt.Errorf("unsupported transcript web event type %q", claim.Event.Type)
	}
}

func transcriptWebModelSelectionInterruption(payload map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(webString(payload["status"])), "interrupted") &&
		strings.EqualFold(
			strings.TrimSpace(firstNonEmpty(webString(payload["reason_code"]), webString(payload["reasonCode"]))),
			sessionRunnerModelProviderUnavailableReasonCode,
		) && !compatibilityPlanBool(payload["auto_resume"])
}

func (s *Server) publishTranscriptTerminal(
	frameContext workspace.FrameRealtimeContext,
	baseID string,
	projection transcriptstore.TerminalProjection,
	messageID string,
) error {
	refs := transcriptArtifactReferences(projection.ArtifactReferences)
	sourcePublicationSequence := projection.PublicationSeq
	publicationBoundaryID := baseID
	messageStatus := "finish"
	phase := "ready"
	turnStatus := "finished"
	state := "ai_waiting_input"
	if projection.TerminalStatus == "failed" {
		messageStatus, phase, turnStatus, state = "error", "failed", "error", "error"
	} else if projection.TerminalStatus == "cancelled" {
		turnStatus = "cancelled"
	}
	if err := s.publishWebMessageStream(frameContext, baseID+":terminal", map[string]any{
		"type": projection.StreamType, "terminal_status": projection.TerminalStatus, "data": projection.Detail,
		"msg_id": messageID, "turn_id": projection.SessionID, "conversation_id": projection.SessionID,
		"created_at": projection.CreatedAt.UnixMilli(), "position": "left", "status": messageStatus,
		"replace":                     projection.TerminalStatus == "failed" || projection.TerminalStatus == "cancelled",
		"artifact_refs":               refs,
		"source_publication_sequence": sourcePublicationSequence,
		"publication_boundary_id":     publicationBoundaryID,
	}); err != nil {
		return err
	}
	if err := s.publishWebFrameEvent(frameContext, baseID+":runtime", "runtime.statusChanged", map[string]any{
		"resource": "acp_tool", "resource_id": frameContext.Frame.AgentName,
		"scope": map[string]any{"kind": "conversation", "id": projection.SessionID},
		"phase": phase, "terminal_status": projection.TerminalStatus, "message": projection.Detail,
		"artifact_refs":               refs,
		"source_publication_sequence": sourcePublicationSequence,
		"publication_boundary_id":     publicationBoundaryID,
	}); err != nil {
		return err
	}
	return s.publishWebFrameEvent(frameContext, baseID+":turn", "turn.completed", map[string]any{
		"session_id": projection.SessionID, "turn_id": projection.SessionID, "conversation_id": projection.SessionID,
		"status": turnStatus, "terminal_status": projection.TerminalStatus, "state": state, "detail": projection.Detail,
		"can_send_message": true, "artifact_refs": refs,
		"source_publication_sequence": sourcePublicationSequence,
		"publication_boundary_id":     publicationBoundaryID,
		"runtime": map[string]any{"state": "idle", "can_send_message": true, "has_task": false,
			"task_status": turnStatus, "is_processing": false, "pending_confirmations": 0, "turn_id": nil},
		"last_message": map[string]any{"id": messageID, "type": "content", "content": projection.Detail,
			"status": messageStatus, "created_at": projection.CreatedAt.UnixMilli(), "artifact_refs": refs},
	})
}

func transcriptPayloadObject(raw []byte) (map[string]any, error) {
	var payload map[string]any
	if len(raw) == 0 || len(raw) > 1<<20 || json.Unmarshal(raw, &payload) != nil || payload == nil {
		return nil, fmt.Errorf("%w: invalid transcript projection payload", transcriptstore.ErrEventConflict)
	}
	return payload, nil
}

func transcriptPayloadText(payload map[string]any) string {
	for _, key := range []string{"text", "content", "detail", "message"} {
		if value := webString(payload[key]); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func transcriptRunnerAttemptStarted(payload map[string]any, attempt int64) bool {
	return attempt > 0 &&
		strings.EqualFold(strings.TrimSpace(webString(payload["status"])), "running") &&
		strings.EqualFold(strings.TrimSpace(webString(payload["detail"])), "runner chat started")
}

func transcriptArtifactReferences(refs []transcriptstore.ArtifactReference) []map[string]any {
	projected := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		projected = append(projected, map[string]any{
			"artifact_id": ref.ArtifactID, "version_id": ref.VersionID, "relation": string(ref.Relation),
			"availability": string(ref.Availability), "attempt": ref.RunnerAttempt,
			"source_event_id": ref.SourceEventID, "ordinal": ref.Ordinal,
		})
	}
	return projected
}

func transcriptDeliveryErrorCode(err error) string {
	if errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
		return "projection_stale"
	}
	if errors.Is(err, transcriptstore.ErrOwnerMismatch) || errors.Is(err, transcriptstore.ErrTerminalProjectionUnavailable) {
		return "mapping_unresolved"
	}
	return "projection_failed"
}

func transcriptDeliveryMaxAttemptsForError(err error) int {
	if errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
		return transcriptProjectionStaleMaxAttempts
	}
	return transcriptDeliveryMaxAttempts
}
