package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type artifactUploadLedgerResult struct {
	ArtifactID string `json:"artifactId"`
	VersionID  string `json:"versionId"`
	FolderID   string `json:"folderId,omitempty"`
}

type artifactUploadFinalizeBarrierKey struct{}

func withArtifactUploadFinalizeBarrier(ctx context.Context, barrier func()) context.Context {
	return context.WithValue(ctx, artifactUploadFinalizeBarrierKey{}, barrier)
}

func awaitArtifactUploadFinalizeBarrier(ctx context.Context) {
	if ctx == nil {
		return
	}
	if barrier, ok := ctx.Value(artifactUploadFinalizeBarrierKey{}).(func()); ok && barrier != nil {
		barrier()
	}
}

// FinalizeArtifactUpload assembles a durable chunk upload into one artifact
// version. Its outer result ledger closes the crash window between the artifact
// commit and upload cleanup, while the artifact writer prevents duplicate
// versions during a retry.
func (s *Store) FinalizeArtifactUpload(ctx context.Context, userID, uploadID, expectedSHA256 string) (Artifact, ArtifactVersion, string, error) {
	return s.finalizeArtifactUpload(ctx, userID, uploadID, expectedSHA256, "", "")
}

// FinalizeArtifactUploadForFrame preserves the conversation provenance carried
// by the Web workspace upload contract while reusing the same durable upload
// ledger and blob commit path as project-level attachments.
func (s *Store) FinalizeArtifactUploadForFrame(
	ctx context.Context,
	userID, uploadID, expectedSHA256, rootFrameID, frameID string,
) (Artifact, ArtifactVersion, string, error) {
	rootFrameID, frameID = strings.TrimSpace(rootFrameID), strings.TrimSpace(frameID)
	if rootFrameID == "" || frameID == "" {
		return Artifact{}, ArtifactVersion{}, "", errors.New("artifact upload root frame id and frame id are required")
	}
	return s.finalizeArtifactUpload(ctx, userID, uploadID, expectedSHA256, rootFrameID, frameID)
}

func (s *Store) finalizeArtifactUpload(
	ctx context.Context,
	userID, uploadID, expectedSHA256, rootFrameID, frameID string,
) (Artifact, ArtifactVersion, string, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, "", errors.New("workspace store is closed")
	}
	if err := mutationIdempotencyError(ctx); err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	userID, uploadID = strings.TrimSpace(userID), strings.TrimSpace(uploadID)
	if userID == "" || uploadID == "" {
		return Artifact{}, ArtifactVersion{}, "", errors.New("attachment upload user id and id are required")
	}
	var err error
	expectedSHA256, err = normalizeSHA256(expectedSHA256)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	idempotencyKey := mutationIdempotencyKey(ctx)
	operation := "artifact.upload.finalize:" + uploadID
	requestHash, err := mutationRequestHash(map[string]any{
		"uploadId": uploadID, "expectedSha256": expectedSHA256,
		"rootFrameId": rootFrameID, "frameId": frameID,
	})
	if err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	var persisted artifactUploadLedgerResult
	if replayed, err := s.lookupMutationResult(ctx, userID, idempotencyKey, operation, requestHash, &persisted); err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	} else if replayed {
		return s.replayArtifactUpload(ctx, uploadID, persisted)
	}

	awaitArtifactUploadFinalizeBarrier(ctx)
	s.attachmentMu.Lock()
	// Another finalize with the same key may have committed while this caller
	// was waiting for the upload lock. Recheck the durable ledger after taking
	// the lock so concurrent replays cannot fall through to the now-deleted
	// upload row.
	persisted = artifactUploadLedgerResult{}
	if replayed, err := s.lookupMutationResult(ctx, userID, idempotencyKey, operation, requestHash, &persisted); err != nil {
		s.attachmentMu.Unlock()
		return Artifact{}, ArtifactVersion{}, "", err
	} else if replayed {
		s.attachmentMu.Unlock()
		return s.replayArtifactUpload(ctx, uploadID, persisted)
	}
	defer s.attachmentMu.Unlock()
	if err := s.expireStaleAttachmentUploads(ctx); err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	upload, found, err := s.getAttachmentUpload(ctx, userID, uploadID)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	if !found {
		return Artifact{}, ArtifactVersion{}, "", errors.New("attachment upload not found")
	}
	temporaryPath, _, err := s.assembleAttachmentUpload(ctx, upload, expectedSHA256)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	defer os.Remove(temporaryPath)
	content, err := openRegularFile(temporaryPath)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, "", fmt.Errorf("open assembled artifact upload: %w", err)
	}
	artifact, version, writeErr := s.WriteArtifactVersionRealtime(ctx, WriteArtifactVersionInput{
		ArtifactID: upload.ID, ProjectID: upload.ProjectID, Name: upload.Filename,
		ContentType: upload.ContentType, Content: content, CreatedBy: userID,
		MaxBytes: upload.TotalSize, IsUserUpload: true, RootFrameID: rootFrameID, FrameID: frameID,
	}, userID)
	closeErr := content.Close()
	if writeErr != nil {
		return Artifact{}, ArtifactVersion{}, "", writeErr
	}
	if closeErr != nil {
		return Artifact{}, ArtifactVersion{}, "", fmt.Errorf("close assembled artifact upload: %w", closeErr)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, "", fmt.Errorf("begin artifact upload finalize: %w", err)
	}
	defer tx.Rollback()
	persisted = artifactUploadLedgerResult{}
	if replayed, err := lookupMutationResultTx(ctx, tx, userID, idempotencyKey, operation, requestHash, &persisted); err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	} else if replayed {
		_ = tx.Rollback()
		return s.replayArtifactUpload(ctx, uploadID, persisted)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM attachment_uploads WHERE id = ? AND user_id = ?`, upload.ID, upload.UserID)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, "", fmt.Errorf("delete finalized artifact upload: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Artifact{}, ArtifactVersion{}, "", errors.New("attachment upload changed during artifact finalize")
	}
	persisted = artifactUploadLedgerResult{
		ArtifactID: artifact.ID, VersionID: version.ID, FolderID: upload.FolderID,
	}
	if err := insertMutationResultTx(ctx, tx, userID, idempotencyKey, operation, requestHash, persisted, s.now().UTC()); err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	if err := tx.Commit(); err != nil {
		persisted = artifactUploadLedgerResult{}
		if found, lookupErr := s.lookupMutationResult(ctx, userID, idempotencyKey, operation, requestHash, &persisted); lookupErr == nil && found {
			return s.replayArtifactUpload(ctx, uploadID, persisted)
		}
		return Artifact{}, ArtifactVersion{}, "", fmt.Errorf("commit artifact upload finalize: %w", err)
	}
	if err := s.removeAttachmentUploadChunks(upload.ID); err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	return artifact, version, upload.FolderID, nil
}

func (s *Store) replayArtifactUpload(ctx context.Context, uploadID string, persisted artifactUploadLedgerResult) (Artifact, ArtifactVersion, string, error) {
	if err := s.recoverBlobCommits(ctx); err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	artifact, version, found, err := s.GetArtifactVersionMetadata(persisted.VersionID)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	if !found || artifact.ID != persisted.ArtifactID {
		return Artifact{}, ArtifactVersion{}, "", errors.New("persisted artifact upload result is missing")
	}
	if err := s.removeAttachmentUploadChunks(uploadID); err != nil {
		return Artifact{}, ArtifactVersion{}, "", err
	}
	return artifact, version, persisted.FolderID, nil
}

func (s *Store) removeAttachmentUploadChunks(uploadID string) error {
	uploadID = strings.TrimSpace(uploadID)
	if uploadID == "" || strings.ContainsAny(uploadID, "/\\\x00") || uploadID == "." || uploadID == ".." {
		return errors.New("attachment upload id is invalid")
	}
	uploadRoot := filepath.Join(s.blobRoot, "uploads")
	target := filepath.Join(uploadRoot, uploadID)
	relative, err := filepath.Rel(uploadRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("attachment upload path escapes the workspace root")
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("remove attachment upload chunks: %w", err)
	}
	return nil
}
