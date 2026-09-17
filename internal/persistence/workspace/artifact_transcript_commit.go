package workspace

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var errArtifactTranscriptClaimStale = errors.New("artifact transcript runner claim is stale")

func insertArtifactTranscriptCommitTx(
	ctx context.Context,
	tx *sql.Tx,
	input WriteArtifactVersionInput,
	ownerUserID string,
	version ArtifactVersion,
	now time.Time,
) error {
	association := input.TranscriptAssociation
	if association == nil {
		return nil
	}
	streamUID := strings.TrimSpace(association.StreamUID)
	runnerID := strings.TrimSpace(association.RunnerID)
	claimToken := strings.TrimSpace(association.ClaimToken)
	relation := strings.TrimSpace(association.Relation)
	if streamUID == "" || runnerID == "" || claimToken == "" || association.Attempt <= 0 || association.SourceEventID <= 0 ||
		!validArtifactTranscriptRelation(relation) {
		return errors.New("complete artifact transcript association is required")
	}
	var streamOwner, projectID, rootFrameID, frameID string
	if err := tx.QueryRowContext(ctx, `
		SELECT owner_id,project_id,root_frame_id,frame_id
		FROM transcript_streams WHERE stream_uid=? AND kind='frame_ref'`, streamUID,
	).Scan(&streamOwner, &projectID, &rootFrameID, &frameID); err != nil {
		return errArtifactTranscriptClaimStale
	}
	if streamOwner != ownerUserID || projectID != input.ProjectID || rootFrameID != input.RootFrameID || frameID != input.FrameID {
		return errArtifactTranscriptClaimStale
	}
	var storedRunnerID, status string
	var claimDigest []byte
	var expiresAt time.Time
	if err := tx.QueryRowContext(ctx, `
		SELECT runner_id,claim_token_sha256,status,expires_at
		FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`, streamUID, association.Attempt,
	).Scan(&storedRunnerID, &claimDigest, &status, &expiresAt); err != nil {
		return errArtifactTranscriptClaimStale
	}
	digest := sha256.Sum256([]byte(claimToken))
	if storedRunnerID != runnerID || status != "running" || !expiresAt.After(now) ||
		len(claimDigest) != len(digest) || subtle.ConstantTimeCompare(claimDigest, digest[:]) != 1 {
		return errArtifactTranscriptClaimStale
	}
	var eventType string
	if err := tx.QueryRowContext(ctx, `
		SELECT event_type FROM transcript_events
		WHERE stream_uid=? AND runner_attempt=? AND event_id=?`,
		streamUID, association.Attempt, association.SourceEventID,
	).Scan(&eventType); err != nil || eventType != "runner_checkpoint" {
		return errArtifactTranscriptClaimStale
	}
	var ordinal int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(ordinal)+1,0) FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND source_event_id=?`,
		streamUID, association.Attempt, association.SourceEventID,
	).Scan(&ordinal); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO transcript_artifact_commits(
			stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,bound_event_id,created_at
		) VALUES(?,?,?,?,?,?,?,NULL,?)`,
		streamUID, association.Attempt, association.SourceEventID, ordinal,
		input.ArtifactID, version.ID, relation, now,
	)
	return err
}

func validArtifactTranscriptRelation(value string) bool {
	return value == "produced" || value == "consumed" || value == "cited" || value == "attached"
}

func artifactTranscriptAssociationHashInput(value *ArtifactTranscriptAssociation) any {
	if value == nil {
		return nil
	}
	return map[string]any{
		"streamUid": value.StreamUID, "runnerId": value.RunnerID, "attempt": value.Attempt,
		"sourceEventId": value.SourceEventID, "relation": value.Relation,
		"reuseCurrentVersionIfUnchanged": value.ReuseCurrentVersionIfUnchanged,
	}
}
