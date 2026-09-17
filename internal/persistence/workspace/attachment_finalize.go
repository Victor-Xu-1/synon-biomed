package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func (s *Store) FinalizeAttachmentUpload(ctx context.Context, userID, uploadID, expectedSHA256 string) (Attachment, error) {
	if s == nil || s.db == nil {
		return Attachment{}, errors.New("workspace store is closed")
	}
	if err := mutationIdempotencyError(ctx); err != nil {
		return Attachment{}, err
	}
	userID, uploadID = strings.TrimSpace(userID), strings.TrimSpace(uploadID)
	var err error
	expectedSHA256, err = normalizeSHA256(expectedSHA256)
	if err != nil {
		return Attachment{}, err
	}
	idempotencyKey := mutationIdempotencyKey(ctx)
	operation := "attachment.finalize:" + uploadID
	requestHash, err := mutationRequestHash(map[string]any{"uploadId": uploadID, "expectedSha256": expectedSHA256})
	if err != nil {
		return Attachment{}, err
	}
	var persisted attachmentLedgerResult
	if replayed, err := s.lookupMutationResult(ctx, userID, idempotencyKey, operation, requestHash, &persisted); err != nil {
		return Attachment{}, err
	} else if replayed {
		if err := s.recoverBlobCommits(ctx); err != nil {
			return Attachment{}, err
		}
		attachment, found, err := s.GetAttachment(ctx, userID, persisted.AttachmentID)
		if err != nil || !found {
			return Attachment{}, fmt.Errorf("persisted attachment mutation result is missing: %w", err)
		}
		if err := s.removeAttachmentUploadChunks(uploadID); err != nil {
			return Attachment{}, err
		}
		return attachment, nil
	}
	s.attachmentMu.Lock()
	defer s.attachmentMu.Unlock()
	if err := s.expireStaleAttachmentUploads(ctx); err != nil {
		return Attachment{}, err
	}
	upload, found, err := s.getAttachmentUpload(ctx, userID, uploadID)
	if err != nil {
		return Attachment{}, err
	}
	if !found {
		return Attachment{}, errors.New("attachment upload not found")
	}
	temporaryPath, digest, err := s.assembleAttachmentUpload(ctx, upload, expectedSHA256)
	if err != nil {
		return Attachment{}, err
	}
	commitAttempted := false
	defer func() {
		if !commitAttempted {
			_ = os.Remove(temporaryPath)
		}
	}()
	id := uuid.NewString()
	relativePath := attachmentBlobPath(id)
	stagingPath, err := s.blobRelative(temporaryPath)
	if err != nil {
		return Attachment{}, err
	}
	now := s.now().UTC()
	attachment := Attachment{
		ID: id, UserID: upload.UserID, ProjectID: upload.ProjectID, Filename: upload.Filename,
		ContentType: upload.ContentType, Size: upload.TotalSize, SHA256: digest, BlobPath: relativePath,
		Ephemeral: upload.Ephemeral, FolderID: upload.FolderID, CreatedAt: now, UpdatedAt: now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attachment{}, fmt.Errorf("begin finalize attachment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	persisted = attachmentLedgerResult{}
	if replayed, err := lookupMutationResultTx(ctx, tx, userID, idempotencyKey, operation, requestHash, &persisted); err != nil {
		return Attachment{}, err
	} else if replayed {
		attachment, found, err := scanAttachment(tx.QueryRowContext(ctx, attachmentSelect+` WHERE id = ? AND user_id = ?`, persisted.AttachmentID, userID))
		if err != nil || !found {
			return Attachment{}, fmt.Errorf("persisted attachment mutation result is missing: %w", err)
		}
		_ = tx.Rollback()
		if err := s.recoverBlobCommits(ctx); err != nil {
			return Attachment{}, err
		}
		if err := s.removeAttachmentUploadChunks(uploadID); err != nil {
			return Attachment{}, err
		}
		return attachment, nil
	}
	if err := requireProjectOwnerTx(ctx, tx, upload.ProjectID, upload.UserID); err != nil {
		return Attachment{}, err
	}
	if err := s.insertAttachment(ctx, tx, attachment); err != nil {
		return Attachment{}, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM attachment_uploads WHERE id = ? AND user_id = ?`, upload.ID, upload.UserID)
	if err != nil {
		return Attachment{}, fmt.Errorf("delete finalized upload: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Attachment{}, errors.New("attachment upload changed during finalize")
	}
	marker := blobCommitMarker{ID: "attachment:" + attachment.ID, Kind: "attachment", AggregateID: attachment.ID,
		StagingPath: stagingPath, FinalPath: relativePath, SHA256: attachment.SHA256,
		SizeBytes: attachment.Size, CreatedAt: now}
	if err := s.insertBlobCommitMarkerTx(ctx, tx, marker); err != nil {
		return Attachment{}, err
	}
	if err := insertMutationResultTx(ctx, tx, userID, idempotencyKey, operation, requestHash,
		attachmentLedgerResult{AttachmentID: attachment.ID}, now); err != nil {
		return Attachment{}, err
	}
	commitAttempted = true
	if err := tx.Commit(); err != nil {
		persisted = attachmentLedgerResult{}
		if found, lookupErr := s.lookupMutationResult(ctx, userID, idempotencyKey, operation, requestHash, &persisted); lookupErr == nil && found {
			if recoverErr := s.recoverBlobCommits(ctx); recoverErr != nil {
				return Attachment{}, recoverErr
			}
			if recovered, found, loadErr := s.GetAttachment(ctx, userID, persisted.AttachmentID); loadErr == nil && found {
				if cleanupErr := s.removeAttachmentUploadChunks(uploadID); cleanupErr != nil {
					return Attachment{}, cleanupErr
				}
				return recovered, nil
			}
		}
		return Attachment{}, fmt.Errorf("commit finalized attachment: %w", err)
	}
	if err := s.finalizeBlobCommit(ctx, marker); err != nil {
		return Attachment{}, fmt.Errorf("finalize committed attachment blob: %w", err)
	}
	if err := s.removeAttachmentUploadChunks(upload.ID); err != nil {
		return Attachment{}, err
	}
	return attachment, nil
}

func (s *Store) assembleAttachmentUpload(ctx context.Context, upload AttachmentUpload, expectedSHA256 string) (string, string, error) {
	if err := s.checkAttachmentUploadSpace(ctx, 0); err != nil {
		return "", "", err
	}
	if len(upload.ReceivedChunks) != upload.ExpectedChunks || upload.ReceivedBytes != upload.TotalSize {
		return "", "", fmt.Errorf("attachment upload is incomplete: received %d of %d chunks and %d of %d bytes",
			len(upload.ReceivedChunks), upload.ExpectedChunks, upload.ReceivedBytes, upload.TotalSize)
	}
	for index, value := range upload.ReceivedChunks {
		if index != value {
			return "", "", fmt.Errorf("attachment upload is missing chunk %d", index)
		}
	}
	stagingDir := filepath.Join(s.blobRoot, "staging")
	if err := ensurePrivateDirectory(stagingDir); err != nil {
		return "", "", err
	}
	temporary, err := os.CreateTemp(stagingDir, ".final-*")
	if err != nil {
		return "", "", fmt.Errorf("create finalized attachment: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", "", fmt.Errorf("secure finalized attachment: %w", err)
	}
	hasher := sha256.New()
	writer := io.MultiWriter(temporary, hasher)
	var written int64
	for index := 0; index < upload.ExpectedChunks; index++ {
		chunkPath := filepath.Join(s.blobRoot, "uploads", upload.ID, fmt.Sprintf("%08d.chunk", index))
		chunk, err := openRegularFile(chunkPath)
		if err != nil {
			return "", "", fmt.Errorf("open chunk %d: %w", index, err)
		}
		copied, copyErr := io.Copy(writer, &contextReader{ctx: ctx, reader: chunk})
		closeErr := chunk.Close()
		if copyErr != nil {
			return "", "", fmt.Errorf("assemble chunk %d: %w", index, copyErr)
		}
		if closeErr != nil {
			return "", "", fmt.Errorf("close chunk %d: %w", index, closeErr)
		}
		written += copied
	}
	if written != upload.TotalSize {
		return "", "", fmt.Errorf("assembled attachment is %d bytes; expected %d", written, upload.TotalSize)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if expectedSHA256 != "" && digest != expectedSHA256 {
		return "", "", fmt.Errorf("attachment checksum mismatch: got %s want %s", digest, expectedSHA256)
	}
	if err := temporary.Sync(); err != nil {
		return "", "", fmt.Errorf("sync finalized attachment: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", "", fmt.Errorf("close finalized attachment: %w", err)
	}
	keep = true
	return temporaryPath, digest, nil
}

func (s *Store) CancelAttachmentUpload(ctx context.Context, userID, uploadID string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	s.attachmentMu.Lock()
	defer s.attachmentMu.Unlock()
	upload, found, err := s.getAttachmentUpload(ctx, strings.TrimSpace(userID), strings.TrimSpace(uploadID))
	if err != nil {
		return err
	}
	if !found {
		return errors.New("attachment upload not found")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM attachment_uploads WHERE id = ? AND user_id = ?`, upload.ID, upload.UserID)
	if err != nil {
		return fmt.Errorf("cancel attachment upload: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("attachment upload changed during cancel")
	}
	if err := s.removeAttachmentUploadChunks(upload.ID); err != nil {
		return err
	}
	return nil
}
