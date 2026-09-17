package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type CloneFrameHistoryInput struct {
	SourceStreamUID string
	TargetStreamUID string
	OwnerID         string
	LegacyCutoverID string
	sourceCounts    canonicalCloneSourceCounts
}

type canonicalCloneSourceCounts struct {
	events, attempts, receipts, checkpoints      int
	branches, branchEvents, branchEventsDistinct int
	artifactCommits, artifactRefs                int
}

type CloneFrameHistoryResult struct {
	TargetStream        Stream
	GenesisSHA256       string
	SourceSHA256        string
	EventCount          int
	AttemptCount        int
	CheckpointCount     int
	BranchCount         int
	BranchEventCount    int
	ArtifactCommitCount int
	ArtifactRefCount    int
	ActiveBranchID      string
}

func (tx *ImmediateTransaction) ResolveFrameCloneSource(
	ctx context.Context, ownerID, sessionID string,
) (Stream, string, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return Stream{}, "", errors.New("transcript transaction is required")
	}
	ownerID, sessionID = strings.TrimSpace(ownerID), strings.TrimSpace(sessionID)
	if ownerID == "" || sessionID == "" {
		return Stream{}, "", errors.New("owner and session are required")
	}
	var streamUID, readAuthority, writeAuthority string
	var activationID, genesisID []byte
	err := tx.conn.QueryRowContext(ctx, `SELECT authority.active_stream_uid,
		authority.read_authority,authority.write_authority,authority.activation_id,authority.genesis_id
		FROM transcript_frame_authority authority
		JOIN transcript_streams stream ON stream.stream_uid=authority.active_stream_uid
		WHERE authority.owner_id=? AND authority.session_id=? AND stream.kind='frame_ref'
			AND stream.owner_id=authority.owner_id AND stream.session_id=authority.session_id
			AND stream.epoch=authority.active_epoch`, ownerID, sessionID).Scan(
		&streamUID, &readAuthority, &writeAuthority, &activationID, &genesisID,
	)
	if err != nil {
		return Stream{}, "", schemaError(err)
	}
	stream, err := getStreamConn(ctx, tx.conn, streamUID, ownerID)
	if err != nil {
		return Stream{}, "", schemaError(err)
	}
	authority := FrameAuthority{
		OwnerID: ownerID, SessionID: sessionID, ActiveStreamUID: streamUID,
		ActiveEpoch: stream.Epoch, ReadAuthority: readAuthority, WriteAuthority: writeAuthority,
		ActivationID: activationID, GenesisID: genesisID,
	}
	if authority.TranscriptPayloadActive() {
		return stream, "", nil
	}
	if authority.ReadAuthority != "legacy_mixed_v1" || authority.WriteAuthority != "legacy_frame_ref_v1" ||
		len(authority.ActivationID) != 0 || len(authority.GenesisID) != 0 {
		return Stream{}, "", ErrEventConflict
	}
	rows, err := tx.conn.QueryContext(ctx, `SELECT cutover_id FROM transcript_history_cutover_runs
		WHERE stream_uid=? AND owner_id=? AND status='ready' AND activated=0
		ORDER BY updated_at DESC,cutover_id LIMIT 2`, stream.UID, ownerID)
	if err != nil {
		return Stream{}, "", schemaError(err)
	}
	defer rows.Close()
	cutovers := make([][]byte, 0, 2)
	for rows.Next() {
		var cutoverID []byte
		if err := rows.Scan(&cutoverID); err != nil {
			return Stream{}, "", schemaError(err)
		}
		cutovers = append(cutovers, append([]byte(nil), cutoverID...))
	}
	if err := rows.Err(); err != nil {
		return Stream{}, "", schemaError(err)
	}
	if len(cutovers) != 1 || len(cutovers[0]) != sha256.Size {
		return Stream{}, "", ErrEventConflict
	}
	return stream, hex.EncodeToString(cutovers[0]), nil
}

func (r *Repository) CloneFrameHistory(
	ctx context.Context, input CloneFrameHistoryInput,
) (CloneFrameHistoryResult, error) {
	if r == nil || r.db == nil {
		return CloneFrameHistoryResult{}, ErrSchemaUnavailable
	}
	var err error
	input, err = normalizeCloneFrameHistoryInput(input)
	if err != nil {
		return CloneFrameHistoryResult{}, err
	}
	var result CloneFrameHistoryResult
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		result, err = r.cloneFrameHistoryConn(ctx, conn, input)
		return err
	})
	return result, schemaError(err)
}

func (tx *ImmediateTransaction) CloneFrameHistory(
	ctx context.Context, input CloneFrameHistoryInput,
) (CloneFrameHistoryResult, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return CloneFrameHistoryResult{}, errors.New("transcript transaction is required")
	}
	input, err := normalizeCloneFrameHistoryInput(input)
	if err != nil {
		return CloneFrameHistoryResult{}, err
	}
	result, err := tx.repository.cloneFrameHistoryConn(ctx, tx.conn, input)
	return result, schemaError(err)
}

func normalizeCloneFrameHistoryInput(input CloneFrameHistoryInput) (CloneFrameHistoryInput, error) {
	input.SourceStreamUID = strings.TrimSpace(input.SourceStreamUID)
	input.TargetStreamUID = strings.TrimSpace(input.TargetStreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.LegacyCutoverID = strings.ToLower(strings.TrimSpace(input.LegacyCutoverID))
	if input.SourceStreamUID == "" || input.TargetStreamUID == "" || input.SourceStreamUID == input.TargetStreamUID ||
		input.OwnerID == "" ||
		(input.LegacyCutoverID != "" && len(input.LegacyCutoverID) != sha256.Size*2) {
		return CloneFrameHistoryInput{}, errors.New("complete canonical clone identity is required")
	}
	return input, nil
}

func (r *Repository) cloneFrameHistoryConn(
	ctx context.Context, conn *sql.Conn, input CloneFrameHistoryInput,
) (CloneFrameHistoryResult, error) {
	source, err := getStreamConn(ctx, conn, input.SourceStreamUID, input.OwnerID)
	if err != nil {
		return CloneFrameHistoryResult{}, err
	}
	target, err := getStreamConn(ctx, conn, input.TargetStreamUID, input.OwnerID)
	if err != nil {
		return CloneFrameHistoryResult{}, err
	}
	if source.Kind != StreamKindFrameRef || target.Kind != StreamKindFrameRef ||
		source.ProjectID == "" || source.ProjectID != target.ProjectID || source.RootFrameID == target.RootFrameID ||
		target.Epoch != 1 {
		return CloneFrameHistoryResult{}, ErrEventConflict
	}
	var sourceStatus, targetStatus string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM frames WHERE id=?`, source.FrameID).Scan(&sourceStatus); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	if err := conn.QueryRowContext(ctx, `SELECT status FROM frames WHERE id=?`, target.FrameID).Scan(&targetStatus); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	if !canonicalCloneSourceSettled(sourceStatus) || source.InputRevision != source.ConsumedInputRevision ||
		targetStatus != "completed" {
		return CloneFrameHistoryResult{}, ErrEventConflict
	}
	input.sourceCounts, err = loadCanonicalCloneSourceCountsConn(ctx, conn, source.UID)
	if err != nil {
		return CloneFrameHistoryResult{}, fmt.Errorf("snapshot canonical clone source: %w", err)
	}
	stage, err := prepareCanonicalCloneStageConn(ctx, conn, source, target, input)
	if err != nil {
		return CloneFrameHistoryResult{}, fmt.Errorf("stage canonical clone source: %w", err)
	}
	defer func() { _ = cleanupCanonicalCloneStageConn(context.Background(), conn, target.UID) }()
	if existing, found, err := loadExistingCanonicalCloneStreamingConn(ctx, conn, source, target, input, stage); err != nil {
		return CloneFrameHistoryResult{}, err
	} else if found {
		return existing, nil
	}
	if target.InputRevision != 0 || target.ConsumedInputRevision != 0 ||
		target.NextEventID != 1 || target.NextPublication != 1 || target.NextCheckpoint != 1 {
		return CloneFrameHistoryResult{}, ErrEventConflict
	}
	if err := validateEmptyCloneTargetConn(ctx, conn, target); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`,
		target.OwnerID, target.SessionID); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM transcript_payload_genesis_receipts WHERE stream_uid=?`, target.UID); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM transcript_branch_state WHERE stream_uid=?`, target.UID); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM transcript_branches WHERE stream_uid=?`, target.UID); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	materialized, sourceDigest, err := materializeCanonicalCloneStageConn(
		ctx, conn, source, target, stage, input.sourceCounts,
	)
	if err != nil {
		return CloneFrameHistoryResult{}, fmt.Errorf("materialize canonical clone: %w", err)
	}
	now := r.now().UTC()
	if _, err := conn.ExecContext(ctx, `UPDATE transcript_streams SET input_revision=?,consumed_input_revision=?,
		next_event_id=?,next_publication_seq=?,next_checkpoint_sequence=?,updated_at=? WHERE stream_uid=?`,
		source.InputRevision, source.InputRevision, int64(stage.eventCount+1), int64(stage.eventCount+1),
		source.NextCheckpoint, now, target.UID); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	genesisID := sha256.Sum256([]byte(HistoryPayloadGenesisContractID + "\x00canonical_clone\x00" +
		target.OwnerID + "\x00" + target.SessionID + "\x00" + target.UID + "\x001\x00" +
		source.UID + "\x00" + strconv.FormatInt(source.Epoch, 10) + "\x00" +
		strconv.Itoa(stage.eventCount) + "\x00" + strconv.FormatInt(stage.branchGeneration, 10) + "\x00" + string(sourceDigest)))
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_payload_genesis_receipts(
		genesis_id,stream_uid,owner_id,session_id,epoch,active_branch_id,branch_generation,
		authority_generation,source_kind,source_stream_uid,source_epoch,source_event_count,source_sha256,status,created_at)
		VALUES(?,?,?,?,1,?,?,1,'canonical_clone',?,?,?,?,'active',?)`,
		genesisID[:], target.UID, target.OwnerID, target.SessionID, materialized.activeTarget, stage.branchGeneration,
		source.UID, source.Epoch, stage.eventCount, sourceDigest, now); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id,updated_at)
		VALUES(?,?,?,?,1,'transcript_payload_v1','transcript_payload_v1',NULL,?,?)`,
		target.OwnerID, target.SessionID, target.UID, target.Epoch, genesisID[:], now); err != nil {
		return CloneFrameHistoryResult{}, err
	}
	target, err = getStreamConn(ctx, conn, target.UID, target.OwnerID)
	if err != nil {
		return CloneFrameHistoryResult{}, err
	}
	return CloneFrameHistoryResult{
		TargetStream: target, GenesisSHA256: hexDigest(genesisID[:]), SourceSHA256: hexDigest(sourceDigest),
		EventCount: stage.eventCount, AttemptCount: materialized.attempts, CheckpointCount: materialized.checkpoints,
		BranchCount: materialized.branches, BranchEventCount: materialized.branchEvents,
		ArtifactCommitCount: materialized.artifactCommits, ArtifactRefCount: materialized.artifactRefs,
		ActiveBranchID: materialized.activeTarget,
	}, nil
}

func loadCanonicalCloneSourceCountsConn(
	ctx context.Context, conn *sql.Conn, streamUID string,
) (canonicalCloneSourceCounts, error) {
	var counts canonicalCloneSourceCounts
	err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_runner_checkpoints WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=?),
		(SELECT COUNT(DISTINCT event_id) FROM transcript_branch_events WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_artifact_commits WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_artifact_refs WHERE stream_uid=?)`,
		streamUID, streamUID, streamUID, streamUID, streamUID, streamUID, streamUID, streamUID, streamUID,
	).Scan(
		&counts.events, &counts.attempts, &counts.receipts, &counts.checkpoints,
		&counts.branches, &counts.branchEvents, &counts.branchEventsDistinct,
		&counts.artifactCommits, &counts.artifactRefs,
	)
	if err != nil {
		return canonicalCloneSourceCounts{}, err
	}
	if counts.events < 0 || counts.attempts < 0 || counts.receipts < 0 || counts.checkpoints < 0 ||
		counts.branches <= 0 || counts.branchEvents < 0 || counts.artifactCommits < 0 || counts.artifactRefs < 0 ||
		counts.receipts > counts.attempts || counts.branchEventsDistinct != counts.events {
		return canonicalCloneSourceCounts{}, ErrEventConflict
	}
	return counts, nil
}

func canonicalCloneSourceSettled(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func loadCloneSourceFrameAuthorityConn(
	ctx context.Context, conn *sql.Conn, source Stream,
) (FrameAuthority, error) {
	var authority FrameAuthority
	if err := conn.QueryRowContext(ctx, `SELECT owner_id,session_id,active_stream_uid,active_epoch,
		authority_generation,read_authority,write_authority,activation_id,genesis_id,updated_at
		FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`, source.OwnerID, source.SessionID).Scan(
		&authority.OwnerID, &authority.SessionID, &authority.ActiveStreamUID, &authority.ActiveEpoch,
		&authority.AuthorityGeneration, &authority.ReadAuthority, &authority.WriteAuthority,
		&authority.ActivationID, &authority.GenesisID, &authority.UpdatedAt,
	); err != nil {
		return FrameAuthority{}, err
	}
	if authority.ActiveStreamUID != source.UID || authority.ActiveEpoch != source.Epoch ||
		authority.AuthorityGeneration <= 0 || !authority.CanonicalProjectionReadable() {
		return FrameAuthority{}, ErrEventConflict
	}
	return authority, nil
}

func digestCanonicalCloneRunnerFactsConn(ctx context.Context, conn *sql.Conn, streamUID string) ([]byte, error) {
	digest := sha256.New()
	queries := []string{
		`SELECT attempt,runner_id,claim_token_sha256,claimed_input_revision,resume_source,
			resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,
			claimed_at,expires_at,finished_event_id,finished_at
			FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt`,
		`SELECT attempt,event_id,status,finished_at FROM transcript_runner_receipts
			WHERE stream_uid=? ORDER BY attempt`,
		`SELECT checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at
			FROM transcript_runner_checkpoints WHERE stream_uid=? ORDER BY checkpoint_sequence`,
	}
	for _, query := range queries {
		if err := digestHistoryActivationRows(ctx, conn, digest, query, streamUID); err != nil {
			return nil, err
		}
	}
	return digest.Sum(nil), nil
}

type canonicalCloneAttemptFact struct {
	attempt, claimedRevision, resumeCheckpoint, phaseSequence, lastCheckpoint int64
	runnerID, resumeSource, status, phase                                     string
	claimDigest                                                               []byte
	claimedAt, expiresAt                                                      time.Time
	finishedEvent                                                             sql.NullInt64
	finishedAt                                                                sql.NullTime
}

func scanCanonicalCloneAttempt(scanner interface{ Scan(...any) error }) (canonicalCloneAttemptFact, error) {
	var fact canonicalCloneAttemptFact
	err := scanner.Scan(
		&fact.attempt, &fact.runnerID, &fact.claimDigest, &fact.claimedRevision,
		&fact.resumeSource, &fact.resumeCheckpoint, &fact.status, &fact.phase,
		&fact.phaseSequence, &fact.lastCheckpoint, &fact.claimedAt, &fact.expiresAt,
		&fact.finishedEvent, &fact.finishedAt,
	)
	return fact, err
}

func canonicalCloneAttemptEqual(source, target canonicalCloneAttemptFact, mappedFinished int64) bool {
	return mappedFinished > 0 && target.attempt == source.attempt && target.runnerID == source.runnerID &&
		bytes.Equal(target.claimDigest, source.claimDigest) && target.claimedRevision == source.claimedRevision &&
		target.resumeSource == source.resumeSource && target.resumeCheckpoint == source.resumeCheckpoint &&
		target.status == source.status && target.phase == source.phase && target.phaseSequence == source.phaseSequence &&
		target.lastCheckpoint == source.lastCheckpoint && target.claimedAt.Equal(source.claimedAt) &&
		target.expiresAt.Equal(source.expiresAt) && target.finishedEvent.Valid && target.finishedEvent.Int64 == mappedFinished &&
		target.finishedAt.Valid && target.finishedAt.Time.Equal(source.finishedAt.Time)
}

func validateEmptyCloneTargetConn(ctx context.Context, conn *sql.Conn, target Stream) error {
	var facts, genesis int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?)+
		(SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?)+
		(SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=?)`,
		target.UID, target.UID, target.UID).Scan(&facts); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_frame_authority authority
		JOIN transcript_payload_genesis_receipts receipt ON receipt.genesis_id=authority.genesis_id
		WHERE authority.owner_id=? AND authority.session_id=? AND authority.active_stream_uid=?
			AND authority.active_epoch=1 AND authority.authority_generation=1
			AND receipt.source_kind='empty' AND receipt.source_event_count=0`,
		target.OwnerID, target.SessionID, target.UID).Scan(&genesis); err != nil {
		return err
	}
	if facts != 0 || genesis != 1 {
		return ErrEventConflict
	}
	return nil
}

func canonicalCloneAskUserOriginMatchesSource(origin AskUserOriginV1, source Stream) bool {
	return origin.StreamUID == source.UID && origin.FrameID == source.FrameID && origin.Epoch == source.Epoch
}

func hexDigest(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, current := range value {
		result[index*2], result[index*2+1] = digits[current>>4], digits[current&15]
	}
	return string(result)
}

func combinePayloadHistoryCloneDigests(values ...[]byte) []byte {
	digest := sha256.New()
	for _, value := range values {
		writePayloadGenesisDigestField(digest, value)
	}
	return digest.Sum(nil)
}
