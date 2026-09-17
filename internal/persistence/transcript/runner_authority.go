package transcript

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// TrustedRunnerAuthority is a server-persisted proof of the runner claim that
// owns an external resource. It deliberately carries only the claim digest,
// never the raw claim token.
type TrustedRunnerAuthority struct {
	StreamUID            string
	OwnerID              string
	ProjectID            string
	RootFrameID          string
	FrameID              string
	StreamEpoch          int64
	RunnerID             string
	Attempt              int64
	ClaimSHA256          string
	ClaimedInputRevision int64
	ResumeSource         ResumeSource
	ResumeCheckpoint     int64
}

// ValidateLiveTrustedRunnerAuthority verifies a digest-only server authority
// against the current active stream and unexpired running attempt inside the
// caller's BEGIN IMMEDIATE transaction.
func (tx *ImmediateTransaction) ValidateLiveTrustedRunnerAuthority(
	ctx context.Context,
	authority TrustedRunnerAuthority,
) (Stream, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return Stream{}, ErrSchemaUnavailable
	}
	authority.StreamUID = strings.TrimSpace(authority.StreamUID)
	authority.OwnerID = strings.TrimSpace(authority.OwnerID)
	authority.ProjectID = strings.TrimSpace(authority.ProjectID)
	authority.RootFrameID = strings.TrimSpace(authority.RootFrameID)
	authority.FrameID = strings.TrimSpace(authority.FrameID)
	authority.RunnerID = strings.TrimSpace(authority.RunnerID)
	authority.ClaimSHA256 = strings.ToLower(strings.TrimSpace(authority.ClaimSHA256))
	if authority.StreamUID == "" || authority.OwnerID == "" || authority.ProjectID == "" ||
		authority.RootFrameID == "" || authority.FrameID == "" || authority.StreamEpoch <= 0 ||
		authority.RunnerID == "" || authority.Attempt <= 0 || authority.ClaimedInputRevision <= 0 ||
		authority.ResumeCheckpoint < 0 {
		return Stream{}, ErrClaimStale
	}
	if err := validateResumeRequest(authority.ResumeSource, authority.ResumeCheckpoint); err != nil {
		return Stream{}, ErrClaimStale
	}
	claimDigest, err := hex.DecodeString(authority.ClaimSHA256)
	if err != nil || len(claimDigest) != 32 {
		return Stream{}, ErrClaimStale
	}
	stream, err := getStreamConn(ctx, tx.conn, authority.StreamUID, authority.OwnerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrOwnerMismatch) || errors.Is(err, ErrEventConflict) {
			return Stream{}, ErrClaimStale
		}
		return Stream{}, err
	}
	if stream.ProjectID != authority.ProjectID || stream.RootFrameID != authority.RootFrameID ||
		stream.FrameID != authority.FrameID || stream.Epoch != authority.StreamEpoch || stream.Kind != StreamKindFrameRef {
		return Stream{}, ErrClaimStale
	}
	if err := validateRunnerAttemptAuthorityConn(ctx, tx.conn, runnerAttemptAuthority{
		StreamUID: authority.StreamUID, RunnerID: authority.RunnerID, Attempt: authority.Attempt,
		ClaimDigest: claimDigest, ClaimedInputRevision: authority.ClaimedInputRevision,
		ResumeSource: authority.ResumeSource, ResumeCheckpoint: authority.ResumeCheckpoint,
	}, tx.repository.now().UTC(), true); err != nil {
		return Stream{}, err
	}
	return stream, nil
}

type runnerAttemptAuthority struct {
	StreamUID            string
	RunnerID             string
	Attempt              int64
	ClaimDigest          []byte
	ClaimedInputRevision int64
	ResumeSource         ResumeSource
	ResumeCheckpoint     int64
}

func validateRunnerAttemptAuthorityConn(
	ctx context.Context,
	conn *sql.Conn,
	authority runnerAttemptAuthority,
	now time.Time,
	requireLive bool,
) error {
	var runnerID, resumeSource, status string
	var storedDigest []byte
	var claimedRevision, resumeCheckpoint int64
	var expiresAt time.Time
	if err := conn.QueryRowContext(ctx, `
		SELECT runner_id,claim_token_sha256,claimed_input_revision,resume_source,
			resume_checkpoint_sequence,status,expires_at
		FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`,
		authority.StreamUID, authority.Attempt,
	).Scan(&runnerID, &storedDigest, &claimedRevision, &resumeSource, &resumeCheckpoint, &status, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClaimStale
		}
		return err
	}
	if runnerID != authority.RunnerID || !equalDigestBytes(storedDigest, authority.ClaimDigest) ||
		claimedRevision != authority.ClaimedInputRevision || resumeSource != string(authority.ResumeSource) ||
		resumeCheckpoint != authority.ResumeCheckpoint || requireLive && (status != "running" || !expiresAt.After(now)) {
		return ErrClaimStale
	}
	return nil
}

func equalDigestBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
