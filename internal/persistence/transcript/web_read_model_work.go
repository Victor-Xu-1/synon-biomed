package transcript

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type TranscriptWebProjectionWorkStatus string

const (
	TranscriptWebProjectionWorkMissing  TranscriptWebProjectionWorkStatus = "missing"
	TranscriptWebProjectionWorkDirty    TranscriptWebProjectionWorkStatus = "dirty"
	TranscriptWebProjectionWorkStale    TranscriptWebProjectionWorkStatus = "stale"
	TranscriptWebProjectionWorkNotReady TranscriptWebProjectionWorkStatus = "not_ready"
)

// TranscriptWebProjectionFence reports the canonical source high-water and
// the minimal persisted projection coordinates needed by Web serving code.
// State fields are populated only after owner, stream, and active-branch
// authority have been verified.
type TranscriptWebProjectionFence struct {
	OwnerID                         string
	SessionID                       string
	StreamUID                       string
	BranchID                        string
	BranchGeneration                int64
	ThroughPublicationSequence      int64
	SourceRevision                  int64
	StateFound                      bool
	StateStatus                     string
	StateProjectionRevision         int64
	StateProjectorVersion           int
	StateBranchGeneration           int64
	StateThroughPublicationSequence int64
	StateSourceRevision             int64
	StateMessageCount               int
	VisibleMessageCount             int
	StateArtifactReferenceCount     int
	StateProjectorStateJSON         []byte
	StateSourceChainSHA256          string
}

// TranscriptWebProjectionWork is one active canonical Frame branch whose Web
// read model needs building or repair. Canonical source coordinates come from
// branch heads; projection status is diagnostic and never an authority input.
type TranscriptWebProjectionWork struct {
	OwnerID                    string
	SessionID                  string
	StreamUID                  string
	BranchID                   string
	BranchGeneration           int64
	ThroughOrdinal             int64
	ThroughPublicationSequence int64
	SourceRevision             int64
	DirtyFirstAffectedOrdinal  int64
	DirtyReasonMask            int64
	Reason                     string
	Status                     TranscriptWebProjectionWorkStatus
	ProjectionStatus           string
}

// GetTranscriptWebProjectionFence is an owner-first, constant-row lookup. It
// validates canonical stream and branch authority before consulting projection
// state so a foreign or inactive coordinate cannot be used as a state oracle.
func (r *WebReadModelRepository) GetTranscriptWebProjectionFence(
	ctx context.Context, ownerID, streamUID, branchID string,
) (TranscriptWebProjectionFence, error) {
	// Older supported rows must remain inspectable so the serving layer can
	// reject them as non-ready and perform an exact-stream catch-up. The message
	// page reader still requires the current projector version before serving.
	return r.getTranscriptWebProjectionFence(ctx, ownerID, streamUID, branchID, true)
}

// GetTranscriptWebProjectionUpgradeFence returns the expected-current-state
// metadata required to atomically replace an older supported projector row.
// Serving callers use GetTranscriptWebProjectionFence for metadata only and
// must pass the current-version readiness gate before reading message rows.
func (r *WebReadModelRepository) GetTranscriptWebProjectionUpgradeFence(
	ctx context.Context, ownerID, streamUID, branchID string,
) (TranscriptWebProjectionFence, error) {
	return r.getTranscriptWebProjectionFence(ctx, ownerID, streamUID, branchID, true)
}

func (r *WebReadModelRepository) getTranscriptWebProjectionFence(
	ctx context.Context,
	ownerID, streamUID, branchID string,
	allowOlderProjector bool,
) (TranscriptWebProjectionFence, error) {
	if r == nil || r.readDB == nil {
		return TranscriptWebProjectionFence{}, errors.New("Transcript Web read model is closed")
	}
	if strings.TrimSpace(ownerID) != ownerID || strings.TrimSpace(streamUID) != streamUID ||
		strings.TrimSpace(branchID) != branchID || ownerID == "" || streamUID == "" || branchID == "" {
		return TranscriptWebProjectionFence{}, errors.New("owner, stream, and branch ids are required")
	}
	tx, err := r.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TranscriptWebProjectionFence{}, fmt.Errorf("begin Transcript Web fence read: %w", err)
	}
	defer tx.Rollback()
	sessionID, err := validateTranscriptWebFenceAuthority(ctx, tx, ownerID, streamUID, branchID)
	if err != nil {
		return TranscriptWebProjectionFence{}, err
	}
	generation, through, sourceRevision, err := transcriptWebSourceFence(
		ctx, tx, ownerID, streamUID, branchID,
	)
	if err != nil {
		return TranscriptWebProjectionFence{}, err
	}
	fence := TranscriptWebProjectionFence{
		OwnerID: ownerID, SessionID: sessionID, StreamUID: streamUID, BranchID: branchID,
		BranchGeneration: generation, ThroughPublicationSequence: through, SourceRevision: sourceRevision,
	}
	var state TranscriptWebProjectionState
	var found bool
	if allowOlderProjector {
		state, found, err = readTranscriptWebProjectionStateForUpgrade(ctx, tx, streamUID, branchID)
	} else {
		state, found, err = readTranscriptWebProjectionState(ctx, tx, streamUID, branchID)
	}
	if err != nil {
		return TranscriptWebProjectionFence{}, err
	}
	if found {
		fence.StateFound = true
		fence.StateStatus = state.Status
		fence.StateProjectorVersion = state.ProjectorVersion
		fence.StateProjectionRevision = state.ProjectionRevision
		fence.StateBranchGeneration = state.BranchGeneration
		fence.StateThroughPublicationSequence = state.ThroughPublicationSequence
		fence.StateSourceRevision = state.SourceRevision
		fence.StateMessageCount = state.MessageCount
		fence.VisibleMessageCount = state.VisibleMessageCount
		fence.StateArtifactReferenceCount = state.MessageArtifactReferenceCount
		fence.StateProjectorStateJSON = append([]byte(nil), state.ProjectorStateJSON...)
		fence.StateSourceChainSHA256 = state.SourceChainSHA256
	}
	if err := tx.Commit(); err != nil {
		return TranscriptWebProjectionFence{}, fmt.Errorf("commit Transcript Web fence read: %w", err)
	}
	return fence, nil
}

// validateTranscriptWebFenceAuthority deliberately reads no projection table
// or branch-head coordinate. Its result gates both source and projection reads.
func validateTranscriptWebFenceAuthority(
	ctx context.Context, db transcriptWebProjectionQuerier, ownerID, streamUID, branchID string,
) (string, error) {
	var storedOwnerID, kind, sessionID string
	var epoch int64
	if err := db.QueryRowContext(ctx, `
		SELECT owner_id,kind,session_id,epoch FROM transcript_streams WHERE stream_uid=?`, streamUID,
	).Scan(&storedOwnerID, &kind, &sessionID, &epoch); errors.Is(err, sql.ErrNoRows) {
		return "", ErrBranchStateStale
	} else if err != nil {
		return "", fmt.Errorf("read Transcript Web stream authority: %w", err)
	}
	if storedOwnerID != ownerID {
		return "", ErrOwnerMismatch
	}
	if StreamKind(kind) == StreamKindFrameRef {
		var authority FrameAuthority
		if err := db.QueryRowContext(ctx, `
			SELECT owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
				read_authority,write_authority,activation_id,genesis_id,updated_at
			FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`, ownerID, sessionID,
		).Scan(
			&authority.OwnerID, &authority.SessionID, &authority.ActiveStreamUID, &authority.ActiveEpoch,
			&authority.AuthorityGeneration, &authority.ReadAuthority, &authority.WriteAuthority,
			&authority.ActivationID, &authority.GenesisID, &authority.UpdatedAt,
		); errors.Is(err, sql.ErrNoRows) {
			return "", ErrBranchStateStale
		} else if err != nil {
			return "", fmt.Errorf("read Transcript Web frame authority: %w", err)
		}
		if authority.ActiveStreamUID != streamUID || authority.ActiveEpoch != epoch ||
			!authority.CanonicalProjectionReadable() {
			return "", ErrBranchStateStale
		}
	}
	var activeBranchID string
	if err := db.QueryRowContext(ctx, `SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`, streamUID).
		Scan(&activeBranchID); errors.Is(err, sql.ErrNoRows) {
		return "", ErrBranchStateStale
	} else if err != nil {
		return "", fmt.Errorf("read Transcript Web branch authority: %w", err)
	}
	if activeBranchID != branchID {
		return "", ErrBranchStateStale
	}
	return sessionID, nil
}

const transcriptWebProjectionWorkSQLPrefix = `
	WITH candidate AS (
		SELECT stream.owner_id,stream.session_id,stream.stream_uid,
			branch_state.active_branch_id AS branch_id,branch_state.generation AS branch_generation,
			head.through_ordinal,head.through_publication_seq,head.source_revision,
			COALESCE(dirty.first_affected_ordinal,0) AS dirty_first_affected_ordinal,
			COALESCE(dirty.reason_mask,0) AS dirty_reason_mask,
			COALESCE(projection.status,'') AS projection_status,
			COALESCE(projection.last_error_code,'') AS last_error_code,
			COALESCE(projection.projector_version,0) AS projector_version,
			CASE
				WHEN projection.stream_uid IS NOT NULL AND projection.projector_version<>? THEN 'stale'
				WHEN dirty.stream_uid IS NOT NULL THEN 'dirty'
				WHEN projection.stream_uid IS NULL THEN 'missing'
				WHEN projection.branch_generation<>branch_state.generation
					OR projection.through_publication_seq<>head.through_publication_seq
					OR projection.source_revision<>head.source_revision THEN 'stale'
				WHEN projection.status<>'ready' THEN 'not_ready'
				ELSE ''
			END AS work_status
		FROM transcript_frame_authority authority
		CROSS JOIN transcript_streams stream
		CROSS JOIN transcript_branch_state branch_state
		CROSS JOIN transcript_branch_heads head
		LEFT JOIN transcript_web_projection_state projection
			ON projection.stream_uid=stream.stream_uid
			AND projection.branch_id=branch_state.active_branch_id
		LEFT JOIN transcript_web_projection_dirty dirty
			ON dirty.stream_uid=stream.stream_uid
			AND dirty.branch_id=branch_state.active_branch_id
		WHERE authority.owner_id=?`

const transcriptWebProjectionWorkSQLSuffix = `
			AND stream.stream_uid=authority.active_stream_uid
			AND stream.owner_id=authority.owner_id
			AND stream.session_id=authority.session_id
			AND stream.epoch=authority.active_epoch
			AND stream.kind='frame_ref'
			AND branch_state.stream_uid=stream.stream_uid
			AND head.stream_uid=stream.stream_uid
			AND head.branch_id=branch_state.active_branch_id
			AND (
				(authority.read_authority='legacy_mixed_v1'
					AND authority.write_authority='legacy_frame_ref_v1'
					AND authority.activation_id IS NULL AND authority.genesis_id IS NULL)
				OR
				(authority.read_authority='transcript_payload_v1'
					AND authority.write_authority='transcript_payload_v1'
					AND ((authority.activation_id IS NOT NULL AND authority.genesis_id IS NULL)
						OR (authority.activation_id IS NULL AND authority.genesis_id IS NOT NULL)))
			)
	)
	SELECT owner_id,session_id,stream_uid,branch_id,branch_generation,
		through_ordinal,through_publication_seq,source_revision,
		dirty_first_affected_ordinal,dirty_reason_mask,
		CASE work_status
			WHEN 'missing' THEN 'projection_state_missing'
			WHEN 'dirty' THEN 'projection_source_dirty'
			WHEN 'stale' THEN CASE
				WHEN projector_version<>? THEN 'projection_projector_version_stale'
				ELSE 'projection_source_fence_stale' END
			WHEN 'not_ready' THEN CASE
				WHEN last_error_code<>'' THEN last_error_code ELSE projection_status END
		END AS reason,
		work_status,projection_status
	FROM candidate
	WHERE work_status<>''
	ORDER BY CASE work_status
		WHEN 'missing' THEN 0
		WHEN 'dirty' THEN 1
		WHEN 'stale' THEN 2
		WHEN 'not_ready' THEN 3
		ELSE 4
	END,session_id,stream_uid,branch_id
	LIMIT ?`

const listTranscriptWebProjectionWorkSQL = transcriptWebProjectionWorkSQLPrefix +
	transcriptWebProjectionWorkSQLSuffix

const getTranscriptWebProjectionWorkSQL = transcriptWebProjectionWorkSQLPrefix + `
			AND stream.stream_uid=?
			AND branch_state.active_branch_id=?` + transcriptWebProjectionWorkSQLSuffix

const listTranscriptWebProjectionOwnersSQL = `
	SELECT DISTINCT authority.owner_id
	FROM transcript_frame_authority authority
	CROSS JOIN transcript_streams stream
	JOIN transcript_branch_state branch_state ON branch_state.stream_uid=stream.stream_uid
	JOIN transcript_branch_heads head
		ON head.stream_uid=stream.stream_uid AND head.branch_id=branch_state.active_branch_id
	LEFT JOIN transcript_web_projection_state projection
		ON projection.stream_uid=stream.stream_uid AND projection.branch_id=branch_state.active_branch_id
	LEFT JOIN transcript_web_projection_dirty dirty
		ON dirty.stream_uid=stream.stream_uid AND dirty.branch_id=branch_state.active_branch_id
	WHERE stream.stream_uid=authority.active_stream_uid
		AND stream.owner_id=authority.owner_id
		AND stream.session_id=authority.session_id
		AND stream.epoch=authority.active_epoch
		AND stream.kind='frame_ref'
		AND (
			(authority.read_authority='legacy_mixed_v1'
				AND authority.write_authority='legacy_frame_ref_v1'
				AND authority.activation_id IS NULL AND authority.genesis_id IS NULL)
			OR
			(authority.read_authority='transcript_payload_v1'
				AND authority.write_authority='transcript_payload_v1'
				AND ((authority.activation_id IS NOT NULL AND authority.genesis_id IS NULL)
					OR (authority.activation_id IS NULL AND authority.genesis_id IS NOT NULL)))
		)
		AND (
			dirty.stream_uid IS NOT NULL
			OR projection.stream_uid IS NULL
			OR projection.projector_version<>?
			OR projection.branch_generation<>branch_state.generation
			OR projection.through_publication_seq<>head.through_publication_seq
			OR projection.source_revision<>head.source_revision
			OR projection.status='building'
			OR projection.status='quarantined'
		)
	ORDER BY authority.owner_id
	LIMIT ?`

// ListTranscriptWebProjectionOwners returns a caller-sized, stable batch of
// owners with active readable Frame transcript authority. Owners are deduped
// across sessions without consulting projection, event, or message history.
func (r *WebReadModelRepository) ListTranscriptWebProjectionOwners(
	ctx context.Context, limit int,
) ([]string, error) {
	if r == nil || r.readDB == nil {
		return nil, errors.New("Transcript Web read model is closed")
	}
	if limit <= 0 {
		return nil, errors.New("positive owner limit is required")
	}
	tx, err := r.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin Transcript Web owner read: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, listTranscriptWebProjectionOwnersSQL, TranscriptWebProjectorVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("query Transcript Web projection owners: %w", err)
	}
	defer rows.Close()
	owners := make([]string, 0, min(limit, 64))
	for rows.Next() {
		var ownerID string
		if err := rows.Scan(&ownerID); err != nil {
			return nil, fmt.Errorf("scan Transcript Web projection owner: %w", err)
		}
		if strings.TrimSpace(ownerID) != ownerID || ownerID == "" {
			return nil, errors.New("invalid Transcript Web projection owner")
		}
		owners = append(owners, ownerID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Transcript Web projection owners: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Transcript Web projection owners: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Transcript Web owner read: %w", err)
	}
	return owners, nil
}

// ListTranscriptWebProjectionWork lists one caller-sized batch without a
// lifetime/history cap. The query begins at the owner-keyed Frame authority
// index and touches only stream, active-branch, head, projection, and dirty
// metadata; transcript events and materialized messages are not loaded.
func (r *WebReadModelRepository) ListTranscriptWebProjectionWork(
	ctx context.Context, ownerID string, limit int,
) ([]TranscriptWebProjectionWork, error) {
	return r.listTranscriptWebProjectionWork(ctx, listTranscriptWebProjectionWorkSQL,
		[]any{TranscriptWebProjectorVersion, ownerID, TranscriptWebProjectorVersion, limit}, ownerID, limit)
}

// GetTranscriptWebProjectionWork returns pending work for one already
// owner-scoped active stream. Request-side catch-up uses this constant-row
// lookup so concurrent conversations never scan or rebuild each other's work.
func (r *WebReadModelRepository) GetTranscriptWebProjectionWork(
	ctx context.Context, ownerID, streamUID, branchID string,
) (TranscriptWebProjectionWork, bool, error) {
	if strings.TrimSpace(streamUID) != streamUID || strings.TrimSpace(branchID) != branchID ||
		streamUID == "" || branchID == "" {
		return TranscriptWebProjectionWork{}, false, errors.New("stream and branch ids are required")
	}
	work, err := r.listTranscriptWebProjectionWork(ctx, getTranscriptWebProjectionWorkSQL,
		[]any{TranscriptWebProjectorVersion, ownerID, streamUID, branchID, TranscriptWebProjectorVersion, 1},
		ownerID, 1)
	if err != nil || len(work) == 0 {
		return TranscriptWebProjectionWork{}, false, err
	}
	return work[0], true, nil
}

func (r *WebReadModelRepository) listTranscriptWebProjectionWork(
	ctx context.Context, query string, args []any, ownerID string, limit int,
) ([]TranscriptWebProjectionWork, error) {
	if r == nil || r.readDB == nil {
		return nil, errors.New("Transcript Web read model is closed")
	}
	if strings.TrimSpace(ownerID) != ownerID || ownerID == "" || limit <= 0 {
		return nil, errors.New("owner id and positive limit are required")
	}
	tx, err := r.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin Transcript Web work read: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query Transcript Web projection work: %w", err)
	}
	defer rows.Close()
	work := make([]TranscriptWebProjectionWork, 0, min(limit, 64))
	for rows.Next() {
		var item TranscriptWebProjectionWork
		var status string
		if err := rows.Scan(
			&item.OwnerID, &item.SessionID, &item.StreamUID, &item.BranchID, &item.BranchGeneration,
			&item.ThroughOrdinal, &item.ThroughPublicationSequence, &item.SourceRevision,
			&item.DirtyFirstAffectedOrdinal, &item.DirtyReasonMask, &item.Reason,
			&status, &item.ProjectionStatus,
		); err != nil {
			return nil, fmt.Errorf("scan Transcript Web projection work: %w", err)
		}
		item.Status = TranscriptWebProjectionWorkStatus(status)
		if err := validateTranscriptWebProjectionWork(item, ownerID); err != nil {
			return nil, err
		}
		work = append(work, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Transcript Web projection work: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Transcript Web projection work: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Transcript Web work read: %w", err)
	}
	return work, nil
}

func validateTranscriptWebProjectionWork(item TranscriptWebProjectionWork, ownerID string) error {
	if item.OwnerID != ownerID || strings.TrimSpace(item.OwnerID) != item.OwnerID ||
		strings.TrimSpace(item.SessionID) != item.SessionID || strings.TrimSpace(item.StreamUID) != item.StreamUID ||
		strings.TrimSpace(item.BranchID) != item.BranchID || item.OwnerID == "" || item.SessionID == "" ||
		item.StreamUID == "" || item.BranchID == "" || item.BranchGeneration <= 0 ||
		item.ThroughOrdinal < 0 || item.ThroughPublicationSequence < 0 || item.SourceRevision < 0 ||
		item.DirtyFirstAffectedOrdinal < 0 || item.DirtyReasonMask < 0 || item.Reason == "" {
		return errors.New("invalid Transcript Web projection work row")
	}
	hasDirtyCoordinate := item.DirtyFirstAffectedOrdinal > 0 && item.DirtyReasonMask > 0
	if (item.DirtyFirstAffectedOrdinal > 0) != (item.DirtyReasonMask > 0) ||
		item.Status == TranscriptWebProjectionWorkDirty && !hasDirtyCoordinate {
		return errors.New("invalid Transcript Web dirty work row")
	}
	switch item.Status {
	case TranscriptWebProjectionWorkMissing:
		if item.ProjectionStatus != "" {
			return errors.New("invalid Transcript Web missing work row")
		}
	case TranscriptWebProjectionWorkDirty, TranscriptWebProjectionWorkStale:
		if item.Status == TranscriptWebProjectionWorkDirty && item.ProjectionStatus == "" {
			break
		}
		if item.ProjectionStatus != "ready" && item.ProjectionStatus != "building" &&
			item.ProjectionStatus != "quarantined" {
			return errors.New("invalid Transcript Web source work row")
		}
	case TranscriptWebProjectionWorkNotReady:
		if item.ProjectionStatus != "building" && item.ProjectionStatus != "quarantined" {
			return errors.New("invalid Transcript Web not-ready work row")
		}
	default:
		return errors.New("invalid Transcript Web projection work status")
	}
	return nil
}
