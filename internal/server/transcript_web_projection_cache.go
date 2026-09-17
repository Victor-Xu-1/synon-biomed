package server

import (
	"container/list"
	"context"
	"encoding/hex"
	"errors"
	"sync"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	defaultTranscriptWebProjectionCacheEntries = 8
	defaultTranscriptWebProjectionCacheBytes   = int64(64 << 20)
)

var errTranscriptWebProjectionBuildFailed = errors.New("transcript web projection build failed")

// transcriptWebProjectionCacheKey binds a projection to every durable
// authority boundary that can change its visible message interpretation.
// Byte slices from FrameAuthority are encoded so the key remains comparable.
type transcriptWebProjectionCacheKey struct {
	ownerID                    string
	sessionID                  string
	authorityStreamUID         string
	authorityEpoch             int64
	authorityGeneration        int64
	readAuthority              string
	writeAuthority             string
	activationID               string
	genesisID                  string
	streamUID                  string
	streamOwnerID              string
	streamSessionID            string
	streamEpoch                int64
	branchID                   string
	branchGeneration           int64
	throughPublicationSequence int64
	artifactReferenceRevision  int64
}

func newTranscriptWebProjectionCacheKey(
	authority transcriptstore.FrameAuthority,
	stream transcriptstore.Stream,
	snapshot transcriptstore.ProjectionSnapshot,
	artifactReferenceRevision int64,
) transcriptWebProjectionCacheKey {
	return transcriptWebProjectionCacheKey{
		ownerID: authority.OwnerID, sessionID: authority.SessionID,
		authorityStreamUID: authority.ActiveStreamUID, authorityEpoch: authority.ActiveEpoch,
		authorityGeneration: authority.AuthorityGeneration,
		readAuthority:       authority.ReadAuthority, writeAuthority: authority.WriteAuthority,
		activationID: hex.EncodeToString(authority.ActivationID), genesisID: hex.EncodeToString(authority.GenesisID),
		streamUID: stream.UID, streamOwnerID: stream.OwnerID, streamSessionID: stream.SessionID, streamEpoch: stream.Epoch,
		branchID: snapshot.BranchID, branchGeneration: snapshot.BranchGeneration,
		throughPublicationSequence: snapshot.ThroughPublicationSequence,
		artifactReferenceRevision:  artifactReferenceRevision,
	}
}

func (key transcriptWebProjectionCacheKey) snapshot() transcriptstore.ProjectionSnapshot {
	return transcriptstore.ProjectionSnapshot{
		StreamUID: key.streamUID, BranchID: key.branchID, BranchGeneration: key.branchGeneration,
		ThroughPublicationSequence: key.throughPublicationSequence,
	}
}

func (key transcriptWebProjectionCacheKey) matchesSnapshot(snapshot transcriptstore.ProjectionSnapshot) bool {
	return snapshot.StreamUID == key.streamUID && snapshot.BranchID == key.branchID &&
		snapshot.BranchGeneration == key.branchGeneration &&
		snapshot.ThroughPublicationSequence == key.throughPublicationSequence
}

type transcriptWebProjectionCache struct {
	mu         sync.Mutex
	entries    map[transcriptWebProjectionCacheKey]*list.Element
	flights    map[transcriptWebProjectionCacheKey]*transcriptWebProjectionFlight
	lru        list.List
	bytes      int64
	maxEntries int
	maxBytes   int64
	builds     int64
}

type transcriptWebProjectionCacheEntry struct {
	key transcriptWebProjectionCacheKey
	// messages is frozen before insertion and must remain immutable. Web page
	// and single-message handlers clone only the records they expose.
	messages []map[string]any
	snapshot transcriptstore.ProjectionSnapshot
	bytes    int64
}

type transcriptWebProjectionFlight struct {
	done     chan struct{}
	waiters  int
	messages []map[string]any
	snapshot transcriptstore.ProjectionSnapshot
	err      error
}

func (cache *transcriptWebProjectionCache) getOrLoad(
	ctx context.Context,
	key transcriptWebProjectionCacheKey,
	load func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error),
) ([]map[string]any, transcriptstore.ProjectionSnapshot, bool, error) {
	cache.mu.Lock()
	cache.initializeLocked()
	if element := cache.entries[key]; element != nil {
		cache.lru.MoveToFront(element)
		entry := element.Value.(*transcriptWebProjectionCacheEntry)
		messages, snapshot := entry.messages, entry.snapshot
		cache.mu.Unlock()
		return messages, snapshot, true, nil
	}
	if flight := cache.flights[key]; flight != nil {
		flight.waiters++
		done := flight.done
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, transcriptstore.ProjectionSnapshot{}, true, ctx.Err()
		case <-done:
			return flight.messages, flight.snapshot, true, flight.err
		}
	}
	flight := &transcriptWebProjectionFlight{done: make(chan struct{})}
	cache.flights[key] = flight
	cache.builds++
	cache.mu.Unlock()

	messages, snapshot, err := runTranscriptWebProjectionLoad(load)
	if err == nil && !key.matchesSnapshot(snapshot) {
		err = transcriptstore.ErrBranchStateStale
	}
	if err == nil {
		err = ctx.Err()
	}
	var frozen []map[string]any
	var estimatedBytes int64
	if err == nil {
		frozen = cloneTranscriptWebProjectionMessages(messages)
		estimatedBytes = estimateTranscriptWebProjectionBytes(key, frozen)
	}

	cache.mu.Lock()
	delete(cache.flights, key)
	if err == nil {
		cache.insertLocked(key, frozen, snapshot, estimatedBytes)
	}
	flight.messages, flight.snapshot, flight.err = frozen, snapshot, err
	close(flight.done)
	cache.mu.Unlock()
	return frozen, snapshot, false, err
}

func runTranscriptWebProjectionLoad(
	load func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error),
) (messages []map[string]any, snapshot transcriptstore.ProjectionSnapshot, err error) {
	defer func() {
		if recover() != nil {
			messages = nil
			snapshot = transcriptstore.ProjectionSnapshot{}
			err = errTranscriptWebProjectionBuildFailed
		}
	}()
	return load()
}

func (cache *transcriptWebProjectionCache) initializeLocked() {
	if cache.entries == nil {
		cache.entries = make(map[transcriptWebProjectionCacheKey]*list.Element)
	}
	if cache.flights == nil {
		cache.flights = make(map[transcriptWebProjectionCacheKey]*transcriptWebProjectionFlight)
	}
	if cache.maxEntries <= 0 {
		cache.maxEntries = defaultTranscriptWebProjectionCacheEntries
	}
	if cache.maxBytes <= 0 {
		cache.maxBytes = defaultTranscriptWebProjectionCacheBytes
	}
}

func (cache *transcriptWebProjectionCache) insertLocked(
	key transcriptWebProjectionCacheKey,
	messages []map[string]any,
	snapshot transcriptstore.ProjectionSnapshot,
	estimatedBytes int64,
) {
	if estimatedBytes <= 0 || estimatedBytes > cache.maxBytes || cache.maxEntries <= 0 {
		return
	}
	for len(cache.entries) >= cache.maxEntries || cache.bytes+estimatedBytes > cache.maxBytes {
		oldest := cache.lru.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(*transcriptWebProjectionCacheEntry)
		delete(cache.entries, entry.key)
		cache.bytes -= entry.bytes
		cache.lru.Remove(oldest)
	}
	entry := &transcriptWebProjectionCacheEntry{
		key: key, messages: messages, snapshot: snapshot, bytes: estimatedBytes,
	}
	cache.entries[key] = cache.lru.PushFront(entry)
	cache.bytes += estimatedBytes
}

func cloneTranscriptWebProjectionMessages(messages []map[string]any) []map[string]any {
	if messages == nil {
		return nil
	}
	cloned := make([]map[string]any, len(messages))
	for index, message := range messages {
		cloned[index] = cloneTranscriptWebProjectionMap(message)
	}
	return cloned
}

func cloneTranscriptWebProjectionMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = cloneTranscriptWebProjectionValue(item)
	}
	return cloned
}

func cloneTranscriptWebProjectionValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneTranscriptWebProjectionMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneTranscriptWebProjectionValue(item)
		}
		return cloned
	case []map[string]any:
		cloned := make([]map[string]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneTranscriptWebProjectionMap(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func estimateTranscriptWebProjectionBytes(
	key transcriptWebProjectionCacheKey,
	messages []map[string]any,
) int64 {
	size := int64(256 + len(key.ownerID) + len(key.sessionID) + len(key.authorityStreamUID) +
		len(key.readAuthority) + len(key.writeAuthority) + len(key.activationID) + len(key.genesisID) +
		len(key.streamUID) + len(key.streamOwnerID) + len(key.streamSessionID) + len(key.branchID))
	for _, message := range messages {
		size = addTranscriptWebProjectionBytes(size, estimateTranscriptWebProjectionValue(message))
	}
	return size
}

func estimateTranscriptWebProjectionValue(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 8
	case string:
		return int64(16 + len(typed))
	case []byte:
		return int64(24 + len(typed))
	case map[string]any:
		size := int64(48)
		for key, item := range typed {
			size = addTranscriptWebProjectionBytes(size, int64(16+len(key)))
			size = addTranscriptWebProjectionBytes(size, estimateTranscriptWebProjectionValue(item))
		}
		return size
	case []any:
		size := int64(24 + 16*len(typed))
		for _, item := range typed {
			size = addTranscriptWebProjectionBytes(size, estimateTranscriptWebProjectionValue(item))
		}
		return size
	case []map[string]any:
		size := int64(24 + 8*len(typed))
		for _, item := range typed {
			size = addTranscriptWebProjectionBytes(size, estimateTranscriptWebProjectionValue(item))
		}
		return size
	case []string:
		size := int64(24 + 16*len(typed))
		for _, item := range typed {
			size = addTranscriptWebProjectionBytes(size, int64(len(item)))
		}
		return size
	default:
		return 16
	}
}

func addTranscriptWebProjectionBytes(left, right int64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if right > maxInt64-left {
		return maxInt64
	}
	return left + right
}

func (s *Server) projectCachedActivatedTranscriptWebHistory(
	ctx context.Context,
	authority transcriptstore.FrameAuthority,
	stream transcriptstore.Stream,
	ownerID, sessionID, branchID string,
) ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
	const maxSnapshotAttempts = 3
	for attempt := 0; attempt < maxSnapshotAttempts; attempt++ {
		var snapshot transcriptstore.ProjectionSnapshot
		var err error
		if branchID == "" {
			snapshot, err = s.transcriptStore.GetProjectionSnapshot(ctx, stream.UID, ownerID)
		} else {
			snapshot, err = s.transcriptStore.GetBranchProjectionSnapshot(ctx, stream.UID, ownerID, branchID)
		}
		if errors.Is(err, transcriptstore.ErrBranchStateStale) {
			continue
		}
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, err
		}
		artifactReferenceRevision, err := s.transcriptStore.ArtifactReferenceRevision(ctx, stream.UID, ownerID)
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, err
		}
		key := newTranscriptWebProjectionCacheKey(authority, stream, snapshot, artifactReferenceRevision)
		messages, projectedSnapshot, _, err := s.transcriptWebCache.getOrLoad(ctx, key, func() (
			[]map[string]any, transcriptstore.ProjectionSnapshot, error,
		) {
			return s.projectTranscriptWebHistory(ctx, stream, ownerID, sessionID, branchID)
		})
		if errors.Is(err, transcriptstore.ErrBranchStateStale) {
			continue
		}
		return messages, projectedSnapshot, err
	}
	return nil, transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrBranchStateStale
}

type transcriptWebArtifactReferenceIdentity struct {
	attempt       int64
	sourceEventID int64
	artifactID    string
	versionID     string
	relation      string
}

// refreshCachedTranscriptWebArtifactAvailability preserves the dynamic
// tombstone contract. Artifact deletion and explicit unavailability do not
// advance a transcript snapshot, so cached message structure is reusable but
// each referenced runner attempt must be reconciled against the live ledger.
func (s *Server) refreshCachedTranscriptWebArtifactAvailability(
	ctx context.Context,
	stream transcriptstore.Stream,
	ownerID string,
	messages []map[string]any,
) error {
	referencesByAttempt := make(map[int64]map[transcriptWebArtifactReferenceIdentity][]map[string]any)
	attempts := make([]int64, 0)
	for _, message := range messages {
		for _, reference := range transcriptWebArtifactReferenceMaps(message["artifact_refs"]) {
			if _, dynamic := reference["availability"]; !dynamic {
				continue
			}
			attempt, ok := transcriptWebProjectionInt64(reference["attempt"])
			if !ok || attempt <= 0 {
				return transcriptstore.ErrEventConflict
			}
			sourceEventID, ok := transcriptWebProjectionInt64(reference["source_event_id"])
			if !ok || sourceEventID <= 0 {
				return transcriptstore.ErrEventConflict
			}
			identity := transcriptWebArtifactReferenceIdentity{
				attempt: attempt, sourceEventID: sourceEventID,
				artifactID: webString(reference["artifact_id"]), versionID: webString(reference["version_id"]),
				relation: webString(reference["relation"]),
			}
			if identity.artifactID == "" || identity.versionID == "" || identity.relation == "" {
				return transcriptstore.ErrEventConflict
			}
			attemptReferences := referencesByAttempt[attempt]
			if attemptReferences == nil {
				attemptReferences = make(map[transcriptWebArtifactReferenceIdentity][]map[string]any)
				referencesByAttempt[attempt] = attemptReferences
				attempts = append(attempts, attempt)
			}
			attemptReferences[identity] = append(attemptReferences[identity], reference)
		}
	}
	for _, attempt := range attempts {
		liveReferences, err := s.transcriptStore.ListArtifactReferences(ctx, stream.UID, ownerID, attempt)
		if err != nil {
			return err
		}
		live := make(map[transcriptWebArtifactReferenceIdentity]transcriptstore.ArtifactAvailability, len(liveReferences))
		for _, reference := range liveReferences {
			identity := transcriptWebArtifactReferenceIdentity{
				attempt: reference.RunnerAttempt, sourceEventID: reference.SourceEventID,
				artifactID: reference.ArtifactID, versionID: reference.VersionID, relation: string(reference.Relation),
			}
			live[identity] = reference.Availability
		}
		for identity, cachedReferences := range referencesByAttempt[attempt] {
			availability, found := live[identity]
			if !found {
				availability = transcriptstore.ArtifactMissing
			}
			for _, reference := range cachedReferences {
				reference["availability"] = string(availability)
			}
		}
	}
	return nil
}

func transcriptWebArtifactReferenceMaps(value any) []map[string]any {
	switch references := value.(type) {
	case []map[string]any:
		return references
	case []any:
		projected := make([]map[string]any, 0, len(references))
		for _, reference := range references {
			if record, ok := reference.(map[string]any); ok {
				projected = append(projected, record)
			}
		}
		return projected
	default:
		return nil
	}
}

func transcriptWebProjectionInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int64:
		return number, true
	case int32:
		return int64(number), true
	case float64:
		converted := int64(number)
		return converted, float64(converted) == number
	default:
		return 0, false
	}
}
