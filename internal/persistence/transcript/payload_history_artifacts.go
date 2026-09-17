package transcript

import (
	"context"
	"database/sql"
	"errors"
	"hash"
	"strconv"
	"strings"
	"time"
)

type payloadHistoryArtifactCommitPlan struct {
	runnerAttempt               int64
	sourceEventID, boundEventID int64
	ordinal                     int64
	artifactID, versionID       string
	relation                    ArtifactRelation
	createdAt                   time.Time
}

type payloadHistoryArtifactRefPlan struct {
	runnerAttempt, sourceEventID, ordinal int64
	artifactID, versionID                 string
	relation                              ArtifactRelation
	availability                          ArtifactAvailability
	createdAt                             time.Time
}

func writePayloadHistoryArtifactDigest(digest hash.Hash, values ...any) {
	for _, value := range values {
		writePayloadGenesisDigestField(digest, []byte(strings.TrimSpace(stringifyPayloadHistoryArtifactValue(value))))
	}
}

func stringifyPayloadHistoryArtifactValue(value any) string {
	switch typed := value.(type) {
	case int64:
		return strconv.FormatInt(typed, 10)
	case string:
		return typed
	default:
		return ""
	}
}

func validatePayloadHistoryArtifactSourceConn(
	ctx context.Context,
	conn *sql.Conn,
	source Stream,
	artifactID, versionID string,
	relation ArtifactRelation,
	availability ArtifactAvailability,
) error {
	input := ArtifactReferenceInput{ArtifactID: artifactID, VersionID: versionID, Relation: relation}
	err := validateArtifactVersionAuthority(ctx, conn, source, input)
	if err == nil {
		return nil
	}
	if availability == ArtifactAvailable || (!errors.Is(err, ErrArtifactMissing) && !errors.Is(err, ErrArtifactMismatch)) {
		return err
	}
	var artifactProject, artifactOwner string
	artifactErr := conn.QueryRowContext(ctx, `SELECT artifact.project_id,project.user_id
		FROM artifacts artifact JOIN projects project ON project.id=artifact.project_id
		WHERE artifact.id=?`, artifactID).Scan(&artifactProject, &artifactOwner)
	if artifactErr != nil && !errors.Is(artifactErr, sql.ErrNoRows) {
		return artifactErr
	}
	if artifactErr == nil && (artifactProject != source.ProjectID || artifactOwner != source.OwnerID) {
		return ErrArtifactMismatch
	}
	var versionArtifact string
	versionErr := conn.QueryRowContext(ctx, `SELECT artifact_id FROM artifact_versions WHERE id=?`, versionID).
		Scan(&versionArtifact)
	if versionErr != nil && !errors.Is(versionErr, sql.ErrNoRows) {
		return versionErr
	}
	if versionErr == nil && versionArtifact != artifactID {
		return ErrArtifactMismatch
	}
	return nil
}

func validatePayloadHistoryArtifactTargetEventConn(
	ctx context.Context, conn *sql.Conn, streamUID string, attempt, eventID int64,
) error {
	var found int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_events event
		JOIN transcript_runner_attempts attempt
			ON attempt.stream_uid=event.stream_uid AND attempt.attempt=event.runner_attempt
		WHERE event.stream_uid=? AND event.runner_attempt=? AND event.event_id=?`,
		streamUID, attempt, eventID).Scan(&found); err != nil {
		return err
	}
	if found != 1 {
		return ErrEventConflict
	}
	return nil
}

func validPayloadHistoryArtifactCommit(commit payloadHistoryArtifactCommitPlan) bool {
	return commit.runnerAttempt > 0 && commit.sourceEventID > 0 && commit.ordinal >= 0 &&
		strings.TrimSpace(commit.artifactID) != "" && strings.TrimSpace(commit.versionID) != "" &&
		validArtifactRelation(commit.relation) && !commit.createdAt.IsZero()
}

func validPayloadHistoryArtifactRef(ref payloadHistoryArtifactRefPlan) bool {
	return ref.runnerAttempt > 0 && ref.sourceEventID > 0 && ref.ordinal >= 0 &&
		strings.TrimSpace(ref.artifactID) != "" && strings.TrimSpace(ref.versionID) != "" &&
		validArtifactRelation(ref.relation) &&
		(ref.availability == ArtifactAvailable || ref.availability == ArtifactDeleted || ref.availability == ArtifactMissing) &&
		!ref.createdAt.IsZero()
}
