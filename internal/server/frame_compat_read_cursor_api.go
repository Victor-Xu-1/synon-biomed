package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityFrameReadCursor(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	switch r.Method {
	case http.MethodGet:
		cursor, found, err := s.getCompatibilityFrameReadCursor(r.Context(), frame)
		if err != nil {
			log.Printf("compatibility read cursor GET failed frame=%q err_type=%T err=%v", frame.ID, err, err)
			var authorityConflict *compatibilityReadCursorAuthorityConflict
			if errors.As(err, &authorityConflict) {
				writeCompatibilityReadCursorAuthorityConflict(w, authorityConflict)
				return
			}
			writeV11StoreError(w, err)
			return
		}
		if !found {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeJSON(w, http.StatusOK, compatibilityReadCursorResponse(cursor))
	case http.MethodPut:
		var input struct {
			MessageUUID          *string `json:"message_uuid"`
			MessageIndex         *int    `json:"message_index"`
			ObservedMessageUUID  string  `json:"observed_message_uuid"`
			ObservedMessageIndex *int    `json:"observed_message_index"`
			Repair               bool    `json:"repair"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid read cursor: "+err.Error())
			return
		}
		if input.MessageIndex == nil {
			writeV11Detail(w, http.StatusBadRequest, "message_index is required")
			return
		}
		messageUUID := ""
		if input.MessageUUID != nil {
			messageUUID = strings.TrimSpace(*input.MessageUUID)
		}
		cursor, err := s.putCompatibilityFrameReadCursor(
			r.Context(), frame, messageUUID, *input.MessageIndex,
			input.ObservedMessageUUID, input.ObservedMessageIndex, input.Repair,
		)
		if err != nil {
			log.Printf("compatibility read cursor PUT failed frame=%q err_type=%T err=%v", frame.ID, err, err)
			writeV11StoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, compatibilityReadCursorResponse(cursor))
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) getCompatibilityFrameReadCursor(
	ctx context.Context, frame workspace.CompatibilityFrame,
) (workspace.FrameReadCursor, bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		cursor, found, err := s.getCompatibilityFrameReadCursorOnce(ctx, frame)
		if err == nil || !retryableReadCursorAuthorityError(err) || attempt == 2 {
			return cursor, found, err
		}
	}
	return workspace.FrameReadCursor{}, false, workspace.ErrReadCursorConflict
}

func (s *Server) getCompatibilityFrameReadCursorOnce(
	ctx context.Context, frame workspace.CompatibilityFrame,
) (workspace.FrameReadCursor, bool, error) {
	cursor, found, err := s.workspaceStore.GetReadCursor(frame.ID)
	if err != nil || !found {
		return cursor, found, err
	}
	projection, active, err := s.compatibilityFrameReadCursorCoordinates(ctx, frame)
	if err != nil || !active {
		return cursor, found, err
	}
	matches, err := s.canonicalReadCursorProjectionMatches(ctx, projection, cursor.MessageUUID, cursor.MessageIndex)
	if err != nil || matches {
		return cursor, found, err
	}
	coordinate, relocatedIndex, relocated, err := s.locateCanonicalReadCursorProjectionCoordinate(
		ctx, projection, cursor.MessageUUID,
	)
	if err != nil {
		return workspace.FrameReadCursor{}, false, err
	}
	if relocated {
		var repaired workspace.FrameReadCursor
		err = s.transcriptStore.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
			if err := tx.ValidateFrameProjectionAuthority(
				ctx, projection.ownerID, frame.ID, projection.authority, projection.snapshot,
			); err != nil {
				return err
			}
			var err error
			repaired, err = s.workspaceStore.PutStableReadCursorImmediate(
				ctx, tx, frame.ID, coordinate.id, relocatedIndex,
				cursor.MessageUUID, cursor.MessageIndex, true,
			)
			return err
		})
		return repaired, err == nil, err
	}
	if !projection.authority.LegacyActivationActive() {
		return workspace.FrameReadCursor{}, false, newCompatibilityReadCursorAuthorityConflict(cursor, projection)
	}
	translated, translatedFound, err := s.transcriptStore.ResolveActivatedStoredReadCursor(
		ctx, projection.ownerID, frame.ID, cursor.MessageUUID, cursor.MessageIndex)
	if err != nil {
		return workspace.FrameReadCursor{}, false, err
	}
	translatedMatches, matchErr := s.canonicalReadCursorProjectionMatches(
		ctx, projection, translated.StableMessageID, translated.TargetMessageIndex,
	)
	if matchErr != nil {
		return workspace.FrameReadCursor{}, false, matchErr
	}
	if !translatedFound || !translatedMatches {
		return workspace.FrameReadCursor{}, false, newCompatibilityReadCursorAuthorityConflict(cursor, projection)
	}
	var repaired workspace.FrameReadCursor
	err = s.transcriptStore.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := tx.ValidateFrameProjectionAuthority(
			ctx, projection.ownerID, frame.ID, projection.authority, projection.snapshot,
		); err != nil {
			return err
		}
		var err error
		repaired, err = s.workspaceStore.PutStableReadCursorImmediate(
			ctx, tx, frame.ID, translated.StableMessageID, translated.TargetMessageIndex,
			cursor.MessageUUID, cursor.MessageIndex, true,
		)
		return err
	})
	return repaired, err == nil, err
}

func (s *Server) putCompatibilityFrameReadCursor(
	ctx context.Context, frame workspace.CompatibilityFrame, messageUUID string, messageIndex int,
	observedMessageUUID string, observedMessageIndex *int, repair bool,
) (workspace.FrameReadCursor, error) {
	for attempt := 0; attempt < 3; attempt++ {
		cursor, err := s.putCompatibilityFrameReadCursorOnce(
			ctx, frame, messageUUID, messageIndex, observedMessageUUID, observedMessageIndex, repair,
		)
		if err == nil || !retryableReadCursorAuthorityError(err) || attempt == 2 {
			return cursor, err
		}
	}
	return workspace.FrameReadCursor{}, workspace.ErrReadCursorConflict
}

func (s *Server) putCompatibilityFrameReadCursorOnce(
	ctx context.Context, frame workspace.CompatibilityFrame, messageUUID string, messageIndex int,
	observedMessageUUID string, observedMessageIndex *int, repair bool,
) (workspace.FrameReadCursor, error) {
	if repair && (strings.TrimSpace(observedMessageUUID) == "" || observedMessageIndex == nil || *observedMessageIndex < 0) {
		return workspace.FrameReadCursor{}, fmt.Errorf("%w: complete observed read cursor is required", workspace.ErrReadCursorConflict)
	}
	projection, active, err := s.compatibilityFrameReadCursorCoordinates(ctx, frame)
	if err != nil {
		return workspace.FrameReadCursor{}, err
	}
	if !active {
		if repair {
			return s.workspaceStore.PutStableReadCursorObserved(
				frame.ID, messageUUID, messageIndex, observedMessageUUID, *observedMessageIndex, true,
			)
		}
		return s.workspaceStore.PutReadCursor(frame.ID, messageUUID, messageIndex)
	}
	matches, err := s.canonicalReadCursorProjectionMatches(ctx, projection, messageUUID, messageIndex)
	if err != nil {
		return workspace.FrameReadCursor{}, err
	}
	if !matches {
		// The renderer can observe a newly streamed message before the durable
		// transcript read model has projected it. This is an authority/convergence
		// conflict, not an internal storage failure; a 409 lets the existing client
		// reload the current durable winner and retry after history converges.
		return workspace.FrameReadCursor{}, fmt.Errorf(
			"%w: requested coordinate does not identify an active transcript message",
			workspace.ErrReadCursorConflict,
		)
	}
	rawCursor, rawFound, err := s.workspaceStore.GetReadCursor(frame.ID)
	if err != nil {
		return workspace.FrameReadCursor{}, err
	}
	var translatedRaw *transcriptstore.ActivatedLegacyCursor
	rawMatches := false
	if rawFound {
		rawMatches, err = s.canonicalReadCursorProjectionMatches(
			ctx, projection, rawCursor.MessageUUID, rawCursor.MessageIndex,
		)
		if err != nil {
			return workspace.FrameReadCursor{}, err
		}
	}
	if rawFound && !rawMatches {
		if projection.authority.LegacyActivationActive() {
			translated, translatedFound, err := s.transcriptStore.ResolveActivatedStoredReadCursor(
				ctx, projection.ownerID, frame.ID, rawCursor.MessageUUID, rawCursor.MessageIndex,
			)
			if err != nil {
				return workspace.FrameReadCursor{}, err
			}
			translatedMatches, matchErr := s.canonicalReadCursorProjectionMatches(
				ctx, projection, translated.StableMessageID, translated.TargetMessageIndex,
			)
			if matchErr != nil {
				return workspace.FrameReadCursor{}, matchErr
			}
			if !translatedFound || !translatedMatches {
				return workspace.FrameReadCursor{}, newCompatibilityReadCursorAuthorityConflict(rawCursor, projection)
			}
			translatedRaw = &translated
		} else if !repair {
			return workspace.FrameReadCursor{}, newCompatibilityReadCursorAuthorityConflict(rawCursor, projection)
		}
	}
	currentObservedUUID := rawCursor.MessageUUID
	currentObservedIndex := rawCursor.MessageIndex
	if translatedRaw != nil {
		currentObservedUUID = translatedRaw.StableMessageID
		currentObservedIndex = translatedRaw.TargetMessageIndex
	}
	if repair {
		if !rawFound || observedMessageIndex == nil {
			return workspace.FrameReadCursor{}, fmt.Errorf("%w: repair target does not exist", workspace.ErrReadCursorConflict)
		}
		if strings.TrimSpace(observedMessageUUID) != currentObservedUUID || *observedMessageIndex != currentObservedIndex {
			return workspace.FrameReadCursor{}, fmt.Errorf("%w: repair authority changed", workspace.ErrReadCursorConflict)
		}
	}
	var winner workspace.FrameReadCursor
	err = s.transcriptStore.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := tx.ValidateFrameProjectionAuthority(
			ctx, projection.ownerID, frame.ID, projection.authority, projection.snapshot,
		); err != nil {
			return err
		}
		if translatedRaw != nil {
			if _, err := s.workspaceStore.PutStableReadCursorImmediate(
				ctx, tx, frame.ID, translatedRaw.StableMessageID, translatedRaw.TargetMessageIndex,
				rawCursor.MessageUUID, rawCursor.MessageIndex, true,
			); err != nil {
				return err
			}
		}
		var err error
		winner, err = s.workspaceStore.PutStableReadCursorImmediate(
			ctx, tx, frame.ID, messageUUID, messageIndex,
			strings.TrimSpace(observedMessageUUID), observedReadCursorIndex(observedMessageIndex), repair,
		)
		return err
	})
	return winner, err
}

func retryableReadCursorAuthorityError(err error) bool {
	return errors.Is(err, transcriptstore.ErrBranchStateStale) || errors.Is(err, workspace.ErrReadCursorConflict)
}

func observedReadCursorIndex(index *int) int {
	if index == nil {
		return -1
	}
	return *index
}

type canonicalReadCursorCoordinate struct {
	id, messageID string
}

type compatibilityReadCursorAuthorityConflict struct {
	Observed                   workspace.FrameReadCursor
	AuthorityGeneration        int64
	BranchID                   string
	BranchGeneration           int64
	ThroughPublicationSequence int64
}

func (e *compatibilityReadCursorAuthorityConflict) Error() string {
	return workspace.ErrReadCursorConflict.Error()
}

func (e *compatibilityReadCursorAuthorityConflict) Unwrap() error {
	return workspace.ErrReadCursorConflict
}

type canonicalReadCursorProjection struct {
	coordinates []canonicalReadCursorCoordinate
	ownerID     string
	authority   transcriptstore.FrameAuthority
	snapshot    transcriptstore.ProjectionSnapshot
	readModel   *activatedTranscriptWebReadModel
}

type canonicalReadCursorCache struct {
	mu      sync.Mutex
	flights map[string]*canonicalReadCursorFlight
	slots   chan struct{}
}

type canonicalReadCursorFlight struct {
	done        chan struct{}
	coordinates []canonicalReadCursorCoordinate
	snapshot    transcriptstore.ProjectionSnapshot
	active      bool
	err         error
}

const maxConcurrentReadCursorProjections = 4

var errReadCursorProjectionFailed = errors.New("read cursor projection failed")

func canonicalReadCursorMatches(coordinates []canonicalReadCursorCoordinate, messageUUID string, messageIndex int) bool {
	messageUUID = strings.TrimSpace(messageUUID)
	if messageUUID == "" || messageIndex < 0 || messageIndex >= len(coordinates) {
		return false
	}
	coordinate := coordinates[messageIndex]
	return coordinate.id == messageUUID || coordinate.messageID == messageUUID
}

func (s *Server) canonicalReadCursorProjectionMatches(
	ctx context.Context,
	projection canonicalReadCursorProjection,
	messageUUID string,
	messageIndex int,
) (bool, error) {
	if projection.readModel == nil {
		return canonicalReadCursorMatches(projection.coordinates, messageUUID, messageIndex), nil
	}
	messageUUID = strings.TrimSpace(messageUUID)
	if messageUUID == "" || messageIndex < 0 {
		return false, nil
	}
	index, found, err := s.transcriptWebReadModel.LocateTranscriptWebMessage(
		ctx, projection.readModel.transcriptWebMessageIdentity(projection.ownerID, messageUUID),
	)
	return found && index == messageIndex, err
}

func (s *Server) locateCanonicalReadCursorProjectionCoordinate(
	ctx context.Context,
	projection canonicalReadCursorProjection,
	messageUUID string,
) (canonicalReadCursorCoordinate, int, bool, error) {
	if projection.readModel == nil {
		return locateCanonicalReadCursorCoordinate(projection.coordinates, messageUUID)
	}
	messageUUID = strings.TrimSpace(messageUUID)
	if messageUUID == "" {
		return canonicalReadCursorCoordinate{}, -1, false, nil
	}
	input := projection.readModel.transcriptWebMessageIdentity(projection.ownerID, messageUUID)
	index, found, err := s.transcriptWebReadModel.LocateTranscriptWebMessage(ctx, input)
	if err != nil || !found {
		return canonicalReadCursorCoordinate{}, -1, found, err
	}
	record, found, err := s.transcriptWebReadModel.GetTranscriptWebMessageByID(ctx, input)
	if err != nil || !found {
		return canonicalReadCursorCoordinate{}, -1, found, err
	}
	return canonicalReadCursorCoordinate{id: record.MessageID, messageID: record.ClientMessageID}, index, true, nil
}

func locateCanonicalReadCursorCoordinate(
	coordinates []canonicalReadCursorCoordinate, messageUUID string,
) (canonicalReadCursorCoordinate, int, bool, error) {
	messageUUID = strings.TrimSpace(messageUUID)
	if messageUUID == "" {
		return canonicalReadCursorCoordinate{}, -1, false, nil
	}
	matchIndex := -1
	var match canonicalReadCursorCoordinate
	for index, coordinate := range coordinates {
		if coordinate.id != messageUUID && coordinate.messageID != messageUUID {
			continue
		}
		if matchIndex >= 0 && matchIndex != index {
			return canonicalReadCursorCoordinate{}, -1, false, workspace.ErrReadCursorConflict
		}
		matchIndex = index
		match = coordinate
	}
	return match, matchIndex, matchIndex >= 0, nil
}

func newCompatibilityReadCursorAuthorityConflict(
	observed workspace.FrameReadCursor, projection canonicalReadCursorProjection,
) *compatibilityReadCursorAuthorityConflict {
	return &compatibilityReadCursorAuthorityConflict{
		Observed: observed, AuthorityGeneration: projection.authority.AuthorityGeneration,
		BranchID: projection.snapshot.BranchID, BranchGeneration: projection.snapshot.BranchGeneration,
		ThroughPublicationSequence: projection.snapshot.ThroughPublicationSequence,
	}
}

func writeCompatibilityReadCursorAuthorityConflict(
	w http.ResponseWriter, conflict *compatibilityReadCursorAuthorityConflict,
) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"code": "READ_CURSOR_AUTHORITY_CHANGED", "message": "Read cursor authority changed",
		"details": map[string]any{
			"observed_cursor":      compatibilityReadCursorResponse(conflict.Observed),
			"authority_generation": conflict.AuthorityGeneration,
			"branch_id":            conflict.BranchID, "branch_generation": conflict.BranchGeneration,
			"through_publication_sequence": conflict.ThroughPublicationSequence,
		},
	})
}

func (s *Server) compatibilityFrameReadCursorCoordinates(
	ctx context.Context, frame workspace.CompatibilityFrame,
) (canonicalReadCursorProjection, bool, error) {
	if s == nil || s.transcriptStore == nil || frame.ID == "" {
		return canonicalReadCursorProjection{}, false, nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frame.ID)
	if err != nil || !found {
		return canonicalReadCursorProjection{}, false, err
	}
	authority, found, err := s.transcriptStore.GetFrameAuthorityBySession(ctx, frameContext.UserID, frame.ID)
	if err != nil || !found || !authority.CanonicalProjectionReadable() {
		return canonicalReadCursorProjection{}, false, err
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, frameContext.UserID, frame.ID)
	if err != nil || !found || stream.UID != authority.ActiveStreamUID || stream.Epoch != authority.ActiveEpoch {
		if err == nil {
			err = transcriptstore.ErrEventConflict
		}
		return canonicalReadCursorProjection{}, true, err
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		return canonicalReadCursorProjection{}, true, err
	}
	if s.transcriptWebReadModel != nil {
		fence, err := s.transcriptWebReadModel.GetTranscriptWebProjectionFence(
			ctx, frameContext.UserID, stream.UID, snapshot.BranchID,
		)
		if err != nil {
			return canonicalReadCursorProjection{}, true, err
		}
		if !fence.StateFound || fence.StateStatus != "ready" ||
			fence.StateBranchGeneration != snapshot.BranchGeneration ||
			fence.StateThroughPublicationSequence != snapshot.ThroughPublicationSequence ||
			fence.StateSourceRevision != fence.SourceRevision {
			return canonicalReadCursorProjection{}, true, transcriptstore.ErrTranscriptWebProjectionStale
		}
		active := &activatedTranscriptWebReadModel{stream: stream, snapshot: snapshot, fence: fence}
		return canonicalReadCursorProjection{
			ownerID: frameContext.UserID, authority: authority, snapshot: snapshot, readModel: active,
		}, true, nil
	}
	key := canonicalReadCursorProjectionKey(authority, stream, snapshot)
	coordinates, projectedSnapshot, active, err := s.projectReadCursorCoordinates(ctx, key, func() (
		[]canonicalReadCursorCoordinate, transcriptstore.ProjectionSnapshot, bool, error,
	) {
		coordinates, projectedSnapshot, err := s.projectTranscriptWebCoordinates(
			ctx, stream, frameContext.UserID, frame.ID, "",
		)
		return coordinates, projectedSnapshot, true, err
	})
	if err != nil || !active {
		return canonicalReadCursorProjection{}, active, err
	}
	currentAuthority, found, err := s.transcriptStore.GetFrameAuthorityBySession(ctx, frameContext.UserID, frame.ID)
	if err != nil {
		return canonicalReadCursorProjection{}, true, err
	}
	if !found || currentAuthority.ActiveStreamUID != authority.ActiveStreamUID ||
		currentAuthority.ActiveEpoch != authority.ActiveEpoch ||
		currentAuthority.AuthorityGeneration != authority.AuthorityGeneration ||
		hex.EncodeToString(currentAuthority.ActivationID) != hex.EncodeToString(authority.ActivationID) ||
		hex.EncodeToString(currentAuthority.GenesisID) != hex.EncodeToString(authority.GenesisID) {
		return canonicalReadCursorProjection{}, true, transcriptstore.ErrBranchStateStale
	}
	return canonicalReadCursorProjection{
		coordinates: coordinates, ownerID: frameContext.UserID, authority: authority, snapshot: projectedSnapshot,
	}, true, nil
}

func canonicalReadCursorProjectionKey(
	authority transcriptstore.FrameAuthority, stream transcriptstore.Stream, snapshot transcriptstore.ProjectionSnapshot,
) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%d\x00%d", stream.OwnerID, stream.SessionID,
		hex.EncodeToString(authority.ActivationID), hex.EncodeToString(authority.GenesisID),
		authority.AuthorityGeneration, stream.Epoch, snapshot.BranchID,
		snapshot.BranchGeneration, snapshot.ThroughPublicationSequence)
}

func (s *Server) projectReadCursorCoordinates(
	ctx context.Context, key string,
	load func() ([]canonicalReadCursorCoordinate, transcriptstore.ProjectionSnapshot, bool, error),
) ([]canonicalReadCursorCoordinate, transcriptstore.ProjectionSnapshot, bool, error) {
	for {
		s.readCursorCache.mu.Lock()
		if s.readCursorCache.flights == nil {
			s.readCursorCache.flights = map[string]*canonicalReadCursorFlight{}
			s.readCursorCache.slots = make(chan struct{}, maxConcurrentReadCursorProjections)
		}
		if flight := s.readCursorCache.flights[key]; flight != nil {
			done := flight.done
			s.readCursorCache.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, transcriptstore.ProjectionSnapshot{}, false, ctx.Err()
			case <-done:
				return flight.coordinates, flight.snapshot, flight.active, flight.err
			}
		}
		slots := s.readCursorCache.slots
		s.readCursorCache.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, transcriptstore.ProjectionSnapshot{}, false, ctx.Err()
		case slots <- struct{}{}:
		}
		s.readCursorCache.mu.Lock()
		if existing := s.readCursorCache.flights[key]; existing != nil {
			s.readCursorCache.mu.Unlock()
			<-slots
			continue
		}
		flight := &canonicalReadCursorFlight{done: make(chan struct{})}
		s.readCursorCache.flights[key] = flight
		s.readCursorCache.mu.Unlock()

		func() {
			defer func() {
				if recover() != nil {
					flight.coordinates = nil
					flight.snapshot = transcriptstore.ProjectionSnapshot{}
					flight.active = true
					flight.err = errReadCursorProjectionFailed
				}
				s.readCursorCache.mu.Lock()
				delete(s.readCursorCache.flights, key)
				close(flight.done)
				s.readCursorCache.mu.Unlock()
				<-slots
			}()
			flight.coordinates, flight.snapshot, flight.active, flight.err = load()
		}()
		return flight.coordinates, flight.snapshot, flight.active, flight.err
	}
}
