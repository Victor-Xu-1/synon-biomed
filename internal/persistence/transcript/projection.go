package transcript

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// ValidateFrameProjectionAuthority proves that a previously projected cursor
// coordinate still belongs to the exact active history and branch snapshot.
// Callers use it inside RunImmediate immediately before their workspace write.
func (tx *ImmediateTransaction) ValidateFrameProjectionAuthority(
	ctx context.Context, ownerID, sessionID string, authority FrameAuthority, snapshot ProjectionSnapshot,
) error {
	if tx == nil || tx.conn == nil || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(sessionID) == "" ||
		authority.ActiveStreamUID == "" || authority.ActiveEpoch <= 0 || authority.AuthorityGeneration <= 0 ||
		!authority.CanonicalProjectionReadable() || snapshot.StreamUID != authority.ActiveStreamUID ||
		snapshot.BranchID == "" || snapshot.BranchGeneration <= 0 || snapshot.ThroughPublicationSequence < 0 {
		return ErrEventConflict
	}
	var streamUID, activationID, genesisID, readAuthority, writeAuthority, kind string
	var epoch, generation int64
	if err := tx.QueryRowContext(ctx, `SELECT authority.active_stream_uid,authority.active_epoch,
		authority.authority_generation,COALESCE(lower(hex(authority.activation_id)),''),
		COALESCE(lower(hex(authority.genesis_id)),''),authority.read_authority,
		authority.write_authority,stream.kind
		FROM transcript_frame_authority authority
		JOIN transcript_streams stream ON stream.stream_uid=authority.active_stream_uid
		WHERE authority.owner_id=? AND authority.session_id=? AND stream.owner_id=authority.owner_id
			AND stream.session_id=authority.session_id`, ownerID, sessionID).Scan(
		&streamUID, &epoch, &generation, &activationID, &genesisID, &readAuthority, &writeAuthority, &kind,
	); err != nil {
		return err
	}
	if streamUID != authority.ActiveStreamUID || epoch != authority.ActiveEpoch ||
		generation != authority.AuthorityGeneration || activationID != hex.EncodeToString(authority.ActivationID) ||
		genesisID != hex.EncodeToString(authority.GenesisID) ||
		readAuthority != authority.ReadAuthority || writeAuthority != authority.WriteAuthority || kind != string(StreamKindFrameRef) {
		return ErrBranchStateStale
	}
	var activeBranch string
	var branchGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT active_branch_id,generation FROM transcript_branch_state
		WHERE stream_uid=?`, streamUID).Scan(&activeBranch, &branchGeneration); err != nil {
		return err
	}
	if activeBranch != snapshot.BranchID || branchGeneration != snapshot.BranchGeneration {
		return ErrBranchStateStale
	}
	var through int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(event.publication_seq),0)
		FROM transcript_branches branch
		LEFT JOIN transcript_branch_events membership
			ON membership.stream_uid=branch.stream_uid AND membership.branch_id=branch.branch_id
		LEFT JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE branch.stream_uid=? AND branch.branch_id=?
		GROUP BY branch.stream_uid,branch.branch_id`, streamUID, activeBranch).Scan(&through); err != nil {
		return err
	}
	if through != snapshot.ThroughPublicationSequence {
		return ErrBranchStateStale
	}
	return nil
}

const maxProjectedEvents = 1000

func (r *Repository) GetProjectionSnapshot(
	ctx context.Context,
	streamUID, ownerID string,
) (ProjectionSnapshot, error) {
	db := r.readDatabase()
	if db == nil {
		return ProjectionSnapshot{}, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" {
		return ProjectionSnapshot{}, errors.New("stream and owner are required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectionSnapshot{}, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, _, snapshot, err := projectionSnapshotTx(ctx, tx, streamUID, ownerID)
	if err != nil {
		return ProjectionSnapshot{}, schemaError(err)
	}
	if err := tx.Commit(); err != nil {
		return ProjectionSnapshot{}, schemaError(err)
	}
	return snapshot, nil
}

func (r *Repository) GetBranchProjectionSnapshot(
	ctx context.Context,
	streamUID, ownerID, branchID string,
) (ProjectionSnapshot, error) {
	db := r.readDatabase()
	if db == nil {
		return ProjectionSnapshot{}, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	branchID = strings.TrimSpace(branchID)
	if streamUID == "" || ownerID == "" || !validTranscriptBranchID(branchID) {
		return ProjectionSnapshot{}, errors.New("stream, owner, and branch are required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectionSnapshot{}, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, _, snapshot, err := projectionSnapshotForBranchTx(ctx, tx, streamUID, ownerID, branchID)
	if err != nil {
		return ProjectionSnapshot{}, schemaError(err)
	}
	if err := tx.Commit(); err != nil {
		return ProjectionSnapshot{}, schemaError(err)
	}
	return snapshot, nil
}

func (r *Repository) ListProjectedEvents(
	ctx context.Context,
	input ListProjectedEventsInput,
) ([]ProjectedEvent, error) {
	return r.listProjectedEvents(ctx, input, true)
}

// ListProjectedCoordinateEvents resolves the exact event payload projection
// without loading artifact details that cannot affect Web message identity or
// index allocation.
func (r *Repository) ListProjectedCoordinateEvents(
	ctx context.Context,
	input ListProjectedEventsInput,
) ([]ProjectedEvent, error) {
	return r.listProjectedEvents(ctx, input, false)
}

func (r *Repository) listProjectedEvents(
	ctx context.Context, input ListProjectedEventsInput, includeArtifacts bool,
) ([]ProjectedEvent, error) {
	db := r.readDatabase()
	if db == nil {
		return nil, ErrSchemaUnavailable
	}
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.BranchID = strings.TrimSpace(input.BranchID)
	if input.AfterPublicationSequence > 0 && input.BranchID == "" {
		return nil, ErrBranchStateStale
	}
	if input.StreamUID == "" || input.OwnerID == "" || input.AfterPublicationSequence < 0 ||
		input.ThroughPublicationSequence < 0 ||
		(input.BranchID == "") != (input.BranchGeneration == 0) || input.BranchGeneration < 0 ||
		(input.BranchID != "" && input.ThroughPublicationSequence == 0) ||
		input.Limit <= 0 || input.Limit > maxProjectedEvents {
		return nil, errors.New("stream, owner, nonnegative cursor, and bounded limit are required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	kind, frameID, snapshot, err := projectionSnapshotForBranchTx(
		ctx, tx, input.StreamUID, input.OwnerID, input.BranchID,
	)
	if err != nil {
		return nil, schemaError(err)
	}
	if input.BranchID != "" && input.BranchGeneration != snapshot.BranchGeneration {
		return nil, ErrBranchStateStale
	}
	if input.ThroughPublicationSequence > 0 &&
		input.BranchID != "" && input.ThroughPublicationSequence > snapshot.ThroughPublicationSequence {
		return nil, ErrBranchStateStale
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT event.stream_uid,event.event_id,event.publication_seq,event.client_message_id,event.event_type,event.source,
			event.runner_attempt,event.payload_json,event.frame_event_id,event.created_at,
			frame.frame_id,frame.event_type,frame.payload
		FROM transcript_branch_events membership
		JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		LEFT JOIN frame_events frame ON event.source='frame_ref' AND frame.id=event.frame_event_id
		WHERE membership.stream_uid=? AND membership.branch_id=? AND event.publication_seq>?
			AND (?=0 OR event.publication_seq<=?)
		ORDER BY event.publication_seq LIMIT ?`, input.StreamUID, snapshot.BranchID, input.AfterPublicationSequence,
		input.ThroughPublicationSequence, input.ThroughPublicationSequence, input.Limit)
	if err != nil {
		return nil, schemaError(err)
	}
	projected := []ProjectedEvent{}
	for rows.Next() {
		event, resolvedPayload, err := scanProjectedEvent(rows, StreamKind(kind), frameID)
		if err != nil {
			_ = rows.Close()
			return nil, schemaError(err)
		}
		projected = append(projected, ProjectedEvent{
			Event: event, ResolvedPayloadJSON: resolvedPayload, ArtifactReferences: []ArtifactReference{},
		})
	}
	if err := rows.Close(); err != nil {
		return nil, schemaError(err)
	}
	if err := rows.Err(); err != nil {
		return nil, schemaError(err)
	}
	if includeArtifacts {
		refs, err := projectedArtifactReferencesTx(ctx, tx, input, snapshot.BranchID)
		if err != nil {
			return nil, schemaError(err)
		}
		for index := range projected {
			projected[index].ArtifactReferences = refs[projected[index].Event.EventID]
			if projected[index].ArtifactReferences == nil {
				projected[index].ArtifactReferences = []ArtifactReference{}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, schemaError(err)
	}
	return projected, nil
}

func projectedArtifactReferencesTx(
	ctx context.Context,
	tx *sql.Tx,
	input ListProjectedEventsInput,
	branchID string,
) (map[int64][]ArtifactReference, error) {
	rows, err := tx.QueryContext(ctx, `
		WITH page AS (
			SELECT event.event_id,event.runner_attempt,event.event_type
			FROM transcript_branch_events membership
			JOIN transcript_events event
				ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
			WHERE membership.stream_uid=? AND membership.branch_id=?
				AND event.publication_seq>? AND (?=0 OR event.publication_seq<=?)
			ORDER BY event.publication_seq LIMIT ?
		)
		SELECT page.event_id,ref.stream_uid,ref.runner_attempt,ref.source_event_id,ref.ordinal,
			ref.artifact_id,ref.version_id,ref.relation,
			CASE
				WHEN ref.availability='deleted' THEN 'deleted'
				WHEN version.id IS NULL OR artifact.id IS NULL THEN 'missing'
				ELSE ref.availability
			END,
			ref.created_at
		FROM page
		JOIN transcript_artifact_refs ref
			ON ref.stream_uid=? AND ref.runner_attempt=page.runner_attempt
		LEFT JOIN artifact_versions version ON version.id=ref.version_id AND version.artifact_id=ref.artifact_id
		LEFT JOIN artifacts artifact ON artifact.id=ref.artifact_id
		WHERE ref.source_event_id=page.event_id OR page.event_type IN ('runner_finished','runner_reclaimed')
		ORDER BY page.event_id,ref.source_event_id,ref.ordinal`,
		input.StreamUID, branchID, input.AfterPublicationSequence, input.ThroughPublicationSequence,
		input.ThroughPublicationSequence, input.Limit, input.StreamUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64][]ArtifactReference{}
	for rows.Next() {
		var eventID int64
		var ref ArtifactReference
		var relation, availability string
		if err := rows.Scan(
			&eventID, &ref.StreamUID, &ref.RunnerAttempt, &ref.SourceEventID, &ref.Ordinal,
			&ref.ArtifactID, &ref.VersionID, &relation, &availability, &ref.CreatedAt,
		); err != nil {
			return nil, err
		}
		ref.Relation = ArtifactRelation(relation)
		ref.Availability = ArtifactAvailability(availability)
		ref.Ordinal = len(result[eventID])
		result[eventID] = append(result[eventID], ref)
	}
	return result, rows.Err()
}

func projectionSnapshotTx(
	ctx context.Context,
	tx *sql.Tx,
	streamUID, ownerID string,
) (string, string, ProjectionSnapshot, error) {
	return projectionSnapshotForBranchTx(ctx, tx, streamUID, ownerID, "")
}

func projectionSnapshotForBranchTx(
	ctx context.Context,
	tx *sql.Tx,
	streamUID, ownerID, requestedBranchID string,
) (string, string, ProjectionSnapshot, error) {
	var storedOwnerID, kind, frameID, activeBranchID string
	var generation int64
	err := tx.QueryRowContext(ctx, `
		SELECT stream.owner_id,stream.kind,stream.frame_id,state.active_branch_id,state.generation
		FROM transcript_streams stream
		JOIN transcript_branch_state state ON state.stream_uid=stream.stream_uid
		WHERE stream.stream_uid=?`, streamUID,
	).Scan(&storedOwnerID, &kind, &frameID, &activeBranchID, &generation)
	if err != nil {
		return "", "", ProjectionSnapshot{}, err
	}
	if storedOwnerID != ownerID {
		return "", "", ProjectionSnapshot{}, ErrOwnerMismatch
	}
	branchID := strings.TrimSpace(requestedBranchID)
	if branchID == "" {
		branchID = activeBranchID
	}
	var through int64
	err = tx.QueryRowContext(ctx, `
		SELECT through_publication_seq FROM transcript_branch_heads
		WHERE stream_uid=? AND branch_id=?`, streamUID, branchID,
	).Scan(&through)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ProjectionSnapshot{}, ErrBranchTargetNotFound
	}
	if err != nil {
		return "", "", ProjectionSnapshot{}, err
	}
	return kind, frameID, ProjectionSnapshot{
		StreamUID: streamUID, BranchID: branchID, BranchGeneration: generation,
		ThroughPublicationSequence: through,
	}, nil
}

func artifactReferencesForEventConn(ctx context.Context, conn *sql.Conn, event Event) ([]ArtifactReference, error) {
	if event.RunnerAttempt == nil {
		return []ArtifactReference{}, nil
	}
	refs, err := listArtifactReferencesConn(ctx, conn, event.StreamUID, *event.RunnerAttempt)
	if err != nil {
		return nil, err
	}
	if event.Type == "runner_finished" || event.Type == "runner_reclaimed" {
		for index := range refs {
			refs[index].Ordinal = index
		}
		return refs, nil
	}
	filtered := make([]ArtifactReference, 0, len(refs))
	for _, ref := range refs {
		if ref.SourceEventID == event.EventID {
			filtered = append(filtered, ref)
		}
	}
	return filtered, nil
}

func scanProjectedEvent(row rowScanner, kind StreamKind, streamFrameID string) (Event, []byte, error) {
	var event Event
	var source string
	var runnerAttempt sql.NullInt64
	var frameEventID sql.NullString
	var payload []byte
	var canonicalFrameID, canonicalType, canonicalPayload sql.NullString
	err := row.Scan(
		&event.StreamUID, &event.EventID, &event.PublicationSeq, &event.ClientMessageID, &event.Type,
		&source, &runnerAttempt, &payload, &frameEventID, &event.CreatedAt,
		&canonicalFrameID, &canonicalType, &canonicalPayload,
	)
	if err != nil {
		return Event{}, nil, err
	}
	event.Source = EventSource(source)
	event.PayloadJSON = append([]byte(nil), payload...)
	if runnerAttempt.Valid {
		value := runnerAttempt.Int64
		event.RunnerAttempt = &value
	}
	if frameEventID.Valid {
		value := frameEventID.String
		event.FrameEventID = &value
	}
	resolved, err := materializedEventPayload(
		event, kind, streamFrameID, canonicalFrameID, canonicalType, canonicalPayload,
	)
	return event, resolved, err
}

func resolveEventPayloadConn(ctx context.Context, conn *sql.Conn, event Event) ([]byte, error) {
	if event.Source == EventSourcePayload {
		return append([]byte(nil), event.PayloadJSON...), nil
	}
	if event.Source != EventSourceFrameRef || event.FrameEventID == nil {
		return nil, ErrEventConflict
	}
	var kind, streamFrameID string
	var canonicalFrameID, canonicalType, canonicalPayload sql.NullString
	err := conn.QueryRowContext(ctx, `
		SELECT stream.kind,stream.frame_id,frame.frame_id,frame.event_type,frame.payload
		FROM transcript_streams stream
		LEFT JOIN frame_events frame ON frame.id=?
		WHERE stream.stream_uid=?`, *event.FrameEventID, event.StreamUID,
	).Scan(&kind, &streamFrameID, &canonicalFrameID, &canonicalType, &canonicalPayload)
	if err != nil {
		return nil, err
	}
	return materializedEventPayload(event, StreamKind(kind), streamFrameID, canonicalFrameID, canonicalType, canonicalPayload)
}

func materializedEventPayload(
	event Event,
	kind StreamKind,
	streamFrameID string,
	canonicalFrameID, canonicalType, canonicalPayload sql.NullString,
) ([]byte, error) {
	if event.Source == EventSourcePayload {
		return append([]byte(nil), event.PayloadJSON...), nil
	}
	if event.Source != EventSourceFrameRef || event.FrameEventID == nil || kind != StreamKindFrameRef ||
		!canonicalFrameID.Valid || canonicalFrameID.String != streamFrameID ||
		!canonicalType.Valid || canonicalType.String != event.Type || !canonicalPayload.Valid {
		return nil, ErrEventConflict
	}
	payload := []byte(canonicalPayload.String)
	if len(payload) == 0 || len(payload) > maxEventPayloadBytes || !json.Valid(payload) {
		return nil, ErrEventConflict
	}
	return payload, nil
}
