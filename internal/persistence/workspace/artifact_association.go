package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// AssociateArtifactVersionInput records a new transcript relationship for an
// already materialized current version. It deliberately has no content reader:
// promoting an unchanged file must not stage or copy a large blob again.
type AssociateArtifactVersionInput struct {
	ArtifactID            string
	ProjectID             string
	VersionID             string
	RootFrameID           string
	FrameID               string
	TranscriptAssociation *ArtifactTranscriptAssociation
}

// AssociateArtifactVersionRealtime appends a realtime transcript association
// without creating another immutable version or touching blob storage. The
// artifact, version, project owner, current-version identity, and transcript
// authority are checked in one transaction.
func (s *Store) AssociateArtifactVersionRealtime(
	ctx context.Context,
	input AssociateArtifactVersionInput,
	ownerUserID string,
) (Artifact, ArtifactVersion, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := mutationIdempotencyError(ctx); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	input.ArtifactID = strings.TrimSpace(input.ArtifactID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.VersionID = strings.TrimSpace(input.VersionID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if input.ArtifactID == "" || input.ProjectID == "" || input.VersionID == "" ||
		input.RootFrameID == "" || input.FrameID == "" || input.TranscriptAssociation == nil {
		return Artifact{}, ArtifactVersion{}, errors.New("artifact association identity and transcript association are required")
	}

	requestHash, err := mutationRequestHash(map[string]any{
		"artifactId":            input.ArtifactID,
		"projectId":             input.ProjectID,
		"versionId":             input.VersionID,
		"rootFrameId":           input.RootFrameID,
		"frameId":               input.FrameID,
		"transcriptAssociation": artifactTranscriptAssociationHashInput(input.TranscriptAssociation),
	})
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	idempotencyKey := mutationIdempotencyKey(ctx)
	operation := "artifact.version.association:" + input.ArtifactID + ":" + input.VersionID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("begin artifact association transaction: %w", err)
	}
	defer tx.Rollback()

	var persisted artifactVersionLedgerResult
	replayed, err := lookupMutationResultTx(
		ctx, tx, ownerUserID, idempotencyKey, operation, requestHash, &persisted,
	)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	if replayed {
		artifact, version, loadErr := loadArtifactVersionResultTx(ctx, tx, persisted.ArtifactID, persisted.VersionID)
		if loadErr != nil {
			return Artifact{}, ArtifactVersion{}, loadErr
		}
		return artifact, version, nil
	}

	if err := requireProjectOwnerTx(ctx, tx, input.ProjectID, ownerUserID); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	var storedProjectID string
	var currentVersionNumber, versionNumber int
	if err := tx.QueryRowContext(ctx, `
		SELECT artifact.project_id,artifact.current_version_number,version.version_number
		FROM artifacts artifact JOIN artifact_versions version ON version.artifact_id=artifact.id
		WHERE artifact.id=? AND version.id=?`, input.ArtifactID, input.VersionID,
	).Scan(&storedProjectID, &currentVersionNumber, &versionNumber); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Artifact{}, ArtifactVersion{}, errors.New("artifact association target is unavailable")
		}
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up artifact association target: %w", err)
	}
	if storedProjectID != input.ProjectID {
		return Artifact{}, ArtifactVersion{}, errors.New("artifact association target belongs to another project")
	}
	if currentVersionNumber != versionNumber {
		return Artifact{}, ArtifactVersion{}, errors.New("artifact association target is not the current version")
	}

	now := s.now().UTC()
	var existingRelation string
	existingErr := tx.QueryRowContext(ctx, `
		SELECT relation FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND source_event_id=? AND artifact_id=? AND version_id=?`,
		strings.TrimSpace(input.TranscriptAssociation.StreamUID), input.TranscriptAssociation.Attempt,
		input.TranscriptAssociation.SourceEventID, input.ArtifactID, input.VersionID,
	).Scan(&existingRelation)
	if errors.Is(existingErr, sql.ErrNoRows) {
		writeInput := WriteArtifactVersionInput{
			ArtifactID: input.ArtifactID, ProjectID: input.ProjectID,
			RootFrameID: input.RootFrameID, FrameID: input.FrameID,
			TranscriptAssociation: input.TranscriptAssociation,
		}
		if err := insertArtifactTranscriptCommitTx(ctx, tx, writeInput, ownerUserID, ArtifactVersion{
			ID: input.VersionID,
		}, now); err != nil {
			return Artifact{}, ArtifactVersion{}, err
		}
	} else if existingErr != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up existing artifact association: %w", existingErr)
	} else if existingRelation != "produced" {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("artifact version already has transcript relation %q", existingRelation)
	}

	artifact, version, err := loadArtifactVersionResultTx(ctx, tx, input.ArtifactID, input.VersionID)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	if err := insertMutationResultTx(ctx, tx, ownerUserID, idempotencyKey, operation, requestHash,
		artifactVersionLedgerResult{ArtifactID: artifact.ID, VersionID: version.ID}, now); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		var persisted artifactVersionLedgerResult
		if found, lookupErr := s.lookupMutationResult(ctx, ownerUserID, idempotencyKey, operation, requestHash, &persisted); lookupErr == nil && found {
			loadedArtifact, loadedVersion, loaded, loadErr := s.GetArtifactVersionMetadata(persisted.VersionID)
			if loadErr == nil && loaded && loadedArtifact.ID == persisted.ArtifactID {
				return loadedArtifact, loadedVersion, nil
			}
		}
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("commit artifact association: %w", err)
	}
	return artifact, version, nil
}
