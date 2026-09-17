package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ApplyArtifactEditRealtime atomically advances an inline artifact, carries
// annotations, and enqueues artifact_created plus lineage_ready.
func (s *Store) ApplyArtifactEditRealtime(ctx context.Context, input ApplyArtifactEditInput, ownerUserID string) (Artifact, ArtifactVersion, []Annotation, error) {
	if err := mutationIdempotencyError(ctx); err != nil {
		return Artifact{}, ArtifactVersion{}, nil, err
	}
	input.ArtifactID, input.ProjectID = strings.TrimSpace(input.ArtifactID), strings.TrimSpace(input.ProjectID)
	input.ParentVersionID, ownerUserID = strings.TrimSpace(input.ParentVersionID), strings.TrimSpace(ownerUserID)
	if input.ArtifactID == "" || input.ProjectID == "" || input.ParentVersionID == "" || ownerUserID == "" {
		return Artifact{}, ArtifactVersion{}, nil, errors.New("artifact, project, parent version, and owner ids are required")
	}
	contentDigest := sha256.Sum256(input.Content)
	requestHash, err := mutationRequestHash(map[string]any{"artifactId": input.ArtifactID, "projectId": input.ProjectID,
		"name": input.Name, "kind": input.Kind, "contentSha256": hex.EncodeToString(contentDigest[:]),
		"createdBy": input.CreatedBy, "parentVersionId": input.ParentVersionID,
		"fromAnnotationTargetKey": input.FromAnnotationTargetKey})
	if err != nil {
		return Artifact{}, ArtifactVersion{}, nil, err
	}
	idempotencyKey := mutationIdempotencyKey(ctx)
	operation := "artifact.apply-edit:" + input.ArtifactID
	var artifact Artifact
	var version ArtifactVersion
	var carried []Annotation
	err = s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var persisted artifactEditLedgerResult
		replayed, err := lookupMutationResultTx(ctx, tx, ownerUserID, idempotencyKey, operation, requestHash, &persisted)
		if err != nil {
			return err
		}
		if replayed {
			artifact, version, err = loadArtifactVersionResultTx(ctx, tx, persisted.ArtifactID, persisted.VersionID)
			if err != nil {
				return err
			}
			carried, err = loadAnnotationsByIDTx(ctx, tx, persisted.AnnotationIDs)
			return err
		}
		if err := requireProjectOwnerTx(ctx, tx, input.ProjectID, ownerUserID); err != nil {
			return err
		}
		artifact, err = scanArtifactTx(tx.QueryRowContext(ctx, artifactSelect+` WHERE id = ?`, input.ArtifactID))
		if err != nil {
			return fmt.Errorf("load artifact for edit: %w", err)
		}
		if artifact.ProjectID != input.ProjectID {
			return errors.New("artifact belongs to another project")
		}
		var parentArtifactID string
		if err := tx.QueryRowContext(ctx, `SELECT artifact_id FROM artifact_versions WHERE id = ?`, input.ParentVersionID).Scan(&parentArtifactID); err != nil {
			return fmt.Errorf("parent artifact version not found: %w", err)
		}
		if parentArtifactID != artifact.ID {
			return errors.New("parent artifact version belongs to another artifact")
		}
		now := s.now().UTC()
		digest := contentDigest
		version = ArtifactVersion{ID: uuid.NewString(), ArtifactID: artifact.ID,
			VersionNumber: artifact.CurrentVersionNumber + 1, ParentID: input.ParentVersionID,
			Content: append([]byte(nil), input.Content...), ContentSHA256: hex.EncodeToString(digest[:]),
			SizeBytes: int64(len(input.Content)), CreatedBy: strings.TrimSpace(input.CreatedBy), CreatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_versions
			(id, artifact_id, version_number, parent_id, content, content_sha256, storage_path, size_bytes, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?)`, version.ID, version.ArtifactID, version.VersionNumber,
			version.ParentID, version.Content, version.ContentSHA256, version.SizeBytes, version.CreatedBy, version.CreatedAt); err != nil {
			return fmt.Errorf("insert edited artifact version: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE artifacts SET current_version_number = ?, updated_at = ?
			WHERE id = ? AND current_version_number = ?`, version.VersionNumber, now, artifact.ID, artifact.CurrentVersionNumber)
		if err != nil {
			return fmt.Errorf("advance edited artifact: %w", err)
		}
		if err := requireOneMutationRow(result, "artifact", artifact.ID); err != nil {
			return fmt.Errorf("artifact changed concurrently: %w", err)
		}
		carried, err = s.carryForwardAnnotationsTx(ctx, tx, artifact.ProjectID,
			strings.TrimSpace(input.FromAnnotationTargetKey), "av:"+version.ID, version.ContentSHA256, now)
		if err != nil {
			return err
		}
		artifact.CurrentVersionNumber, artifact.UpdatedAt = version.VersionNumber, now
		if err := s.enqueueArtifactVersionEventsTx(ctx, tx, ownerUserID, artifact, version); err != nil {
			return err
		}
		ids := make([]string, 0, len(carried))
		for _, annotation := range carried {
			ids = append(ids, annotation.ID)
		}
		return insertMutationResultTx(ctx, tx, ownerUserID, idempotencyKey, operation, requestHash,
			artifactEditLedgerResult{ArtifactID: artifact.ID, VersionID: version.ID, AnnotationIDs: ids}, now)
	})
	if err != nil {
		var persisted artifactEditLedgerResult
		if found, lookupErr := s.lookupMutationResult(ctx, ownerUserID, idempotencyKey, operation, requestHash, &persisted); lookupErr == nil && found {
			loadedArtifact, loadedVersion, loaded, loadErr := s.GetArtifactVersion(persisted.VersionID)
			if loadErr == nil && loaded && loadedArtifact.ID == persisted.ArtifactID {
				annotations, annotationErr := s.ListAnnotations(loadedArtifact.ProjectID, "av:"+loadedVersion.ID)
				if annotationErr == nil {
					return loadedArtifact, loadedVersion, annotations, nil
				}
			}
		}
	}
	return artifact, version, carried, err
}

func loadAnnotationsByIDTx(ctx context.Context, tx *sql.Tx, ids []string) ([]Annotation, error) {
	annotations := make([]Annotation, 0, len(ids))
	for _, id := range ids {
		annotation, err := scanAnnotation(tx.QueryRowContext(ctx, annotationSelect+` WHERE id = ?`, id))
		if err != nil {
			return nil, fmt.Errorf("load persisted annotation result: %w", err)
		}
		annotations = append(annotations, annotation)
	}
	return annotations, nil
}
