package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type AttachmentUpload struct {
	ID             string    `json:"id"`
	UserID         string    `json:"userId,omitempty"`
	ProjectID      string    `json:"projectId"`
	Filename       string    `json:"filename"`
	ContentType    string    `json:"contentType"`
	TotalSize      int64     `json:"totalSize"`
	ChunkSize      int64     `json:"chunkSize"`
	ExpectedChunks int       `json:"expectedChunks"`
	ReceivedChunks []int     `json:"receivedChunks"`
	ReceivedBytes  int64     `json:"receivedBytes"`
	Ephemeral      bool      `json:"ephemeral"`
	FolderID       string    `json:"folderId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type InitAttachmentUploadInput struct {
	UserID      string
	ProjectID   string
	Filename    string
	ContentType string
	TotalSize   int64
	ChunkSize   int64
	Ephemeral   bool
	FolderID    string
}

const attachmentUploadSelect = `
	SELECT id, user_id, project_id, filename, content_type, total_size, chunk_size,
		expected_chunks, ephemeral, COALESCE(folder_id, ''), created_at, updated_at
	FROM attachment_uploads`

func (s *Store) InitAttachmentUpload(ctx context.Context, input InitAttachmentUploadInput) (AttachmentUpload, error) {
	if s == nil || s.db == nil {
		return AttachmentUpload{}, errors.New("workspace store is closed")
	}
	normalized, err := normalizeAttachmentInput(CreateAttachmentInput{
		UserID: input.UserID, ProjectID: input.ProjectID, Filename: input.Filename,
		ContentType: input.ContentType, Ephemeral: input.Ephemeral, FolderID: input.FolderID,
	})
	if err != nil {
		return AttachmentUpload{}, err
	}
	if input.TotalSize <= 0 {
		return AttachmentUpload{}, errors.New("attachment total size must be positive")
	}
	if input.ChunkSize <= 0 || input.ChunkSize > maxAttachmentChunk {
		return AttachmentUpload{}, fmt.Errorf("attachment chunk size must be between 1 and %d bytes", maxAttachmentChunk)
	}
	expectedChunks64 := (input.TotalSize-1)/input.ChunkSize + 1
	if expectedChunks64 > int64(^uint(0)>>1) {
		return AttachmentUpload{}, errors.New("attachment upload has too many chunks")
	}
	s.attachmentMu.Lock()
	defer s.attachmentMu.Unlock()
	if err := s.expireStaleAttachmentUploads(ctx); err != nil {
		return AttachmentUpload{}, err
	}
	owned, err := s.ProjectOwnedBy(normalized.ProjectID, normalized.UserID)
	if err != nil {
		return AttachmentUpload{}, err
	}
	if !owned {
		return AttachmentUpload{}, fmt.Errorf("project %q is unavailable to owner", normalized.ProjectID)
	}
	if err := s.checkAttachmentUploadSpace(ctx, input.TotalSize); err != nil {
		return AttachmentUpload{}, err
	}
	now := s.now().UTC()
	upload := AttachmentUpload{
		ID: uuid.NewString(), UserID: normalized.UserID, ProjectID: normalized.ProjectID,
		Filename: normalized.Filename, ContentType: normalized.ContentType,
		TotalSize: input.TotalSize, ChunkSize: input.ChunkSize, ExpectedChunks: int(expectedChunks64),
		ReceivedChunks: []int{}, Ephemeral: normalized.Ephemeral, FolderID: normalized.FolderID,
		CreatedAt: now, UpdatedAt: now,
	}
	uploadDir := filepath.Join(s.blobRoot, "uploads", upload.ID)
	if err := ensurePrivateDirectory(uploadDir); err != nil {
		return AttachmentUpload{}, fmt.Errorf("prepare attachment upload: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO attachment_uploads
			(id, user_id, project_id, filename, content_type, total_size, chunk_size,
			 expected_chunks, ephemeral, folder_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		upload.ID, upload.UserID, upload.ProjectID, upload.Filename, upload.ContentType,
		upload.TotalSize, upload.ChunkSize, upload.ExpectedChunks, upload.Ephemeral,
		nullableString(upload.FolderID), upload.CreatedAt, upload.UpdatedAt)
	if err != nil {
		_ = os.RemoveAll(uploadDir)
		return AttachmentUpload{}, fmt.Errorf("insert attachment upload: %w", err)
	}
	return upload, nil
}

func (s *Store) SaveAttachmentChunk(ctx context.Context, userID, uploadID string, chunkIndex int, content io.Reader) (AttachmentUpload, error) {
	if s == nil || s.db == nil {
		return AttachmentUpload{}, errors.New("workspace store is closed")
	}
	if content == nil {
		return AttachmentUpload{}, errors.New("attachment chunk content is required")
	}
	s.attachmentMu.Lock()
	defer s.attachmentMu.Unlock()
	if err := s.expireStaleAttachmentUploads(ctx); err != nil {
		return AttachmentUpload{}, err
	}
	upload, found, err := s.getAttachmentUpload(ctx, strings.TrimSpace(userID), strings.TrimSpace(uploadID))
	if err != nil {
		return AttachmentUpload{}, err
	}
	if !found {
		return AttachmentUpload{}, errors.New("attachment upload not found")
	}
	if chunkIndex < 0 || chunkIndex >= upload.ExpectedChunks {
		return AttachmentUpload{}, fmt.Errorf("chunk index %d is outside [0,%d)", chunkIndex, upload.ExpectedChunks)
	}
	if err := s.checkAttachmentUploadSpace(ctx, 0); err != nil {
		return AttachmentUpload{}, err
	}
	expectedSize := upload.ChunkSize
	if chunkIndex == upload.ExpectedChunks-1 {
		expectedSize = upload.TotalSize - int64(chunkIndex)*upload.ChunkSize
	}
	stagingDir := filepath.Join(s.blobRoot, "staging")
	if err := ensurePrivateDirectory(stagingDir); err != nil {
		return AttachmentUpload{}, err
	}
	temporary, size, digest, err := writeTemporaryFile(ctx, stagingDir, content, expectedSize)
	if err != nil {
		return AttachmentUpload{}, err
	}
	commitAttempted := false
	defer func() {
		if !commitAttempted {
			_ = os.Remove(temporary)
		}
	}()
	if size != expectedSize {
		return AttachmentUpload{}, fmt.Errorf("chunk %d size is %d bytes; expected %d", chunkIndex, size, expectedSize)
	}
	var storedSize int64
	var storedDigest string
	err = s.db.QueryRowContext(ctx, `
		SELECT size_bytes, sha256 FROM attachment_upload_chunks
		WHERE upload_id = ? AND chunk_index = ?`, upload.ID, chunkIndex).Scan(&storedSize, &storedDigest)
	if err == nil {
		if storedSize != size || storedDigest != digest {
			return AttachmentUpload{}, fmt.Errorf("chunk %d was already uploaded with different content", chunkIndex)
		}
		return s.mustAttachmentUpload(ctx, upload.UserID, upload.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AttachmentUpload{}, fmt.Errorf("look up attachment chunk: %w", err)
	}
	relativePath := filepath.ToSlash(filepath.Join("uploads", upload.ID, fmt.Sprintf("%08d.chunk", chunkIndex)))
	stagingPath, err := s.blobRelative(temporary)
	if err != nil {
		return AttachmentUpload{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AttachmentUpload{}, fmt.Errorf("begin attachment chunk commit: %w", err)
	}
	defer tx.Rollback()
	var txUserID, txProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT user_id, project_id FROM attachment_uploads WHERE id = ? AND user_id = ?`, upload.ID, upload.UserID).Scan(&txUserID, &txProjectID); err != nil {
		return AttachmentUpload{}, errors.New("attachment upload changed during chunk save")
	}
	if err := requireProjectOwnerTx(ctx, tx, txProjectID, txUserID); err != nil {
		return AttachmentUpload{}, err
	}
	var existingSize int64
	var existingDigest string
	if err := tx.QueryRowContext(ctx, `SELECT size_bytes, sha256 FROM attachment_upload_chunks WHERE upload_id = ? AND chunk_index = ?`, upload.ID, chunkIndex).Scan(&existingSize, &existingDigest); err == nil {
		if existingSize != size || existingDigest != digest {
			return AttachmentUpload{}, fmt.Errorf("chunk %d was already uploaded with different content", chunkIndex)
		}
		_ = tx.Rollback()
		return s.mustAttachmentUpload(ctx, upload.UserID, upload.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AttachmentUpload{}, fmt.Errorf("recheck attachment chunk: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO attachment_upload_chunks
			(upload_id, chunk_index, size_bytes, sha256, blob_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, upload.ID, chunkIndex, size, digest, relativePath, now)
	if err != nil {
		return AttachmentUpload{}, fmt.Errorf("insert attachment chunk: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attachment_uploads SET updated_at = ? WHERE id = ?`, now, upload.ID); err != nil {
		return AttachmentUpload{}, fmt.Errorf("touch attachment upload: %w", err)
	}
	marker := blobCommitMarker{ID: fmt.Sprintf("attachment-chunk:%s:%d", upload.ID, chunkIndex), Kind: "attachment_chunk",
		AggregateID: upload.ID, StagingPath: stagingPath, FinalPath: relativePath,
		SHA256: digest, SizeBytes: size, CreatedAt: now}
	if err := s.insertBlobCommitMarkerTx(ctx, tx, marker); err != nil {
		return AttachmentUpload{}, err
	}
	commitAttempted = true
	if err := tx.Commit(); err != nil {
		var committedSize int64
		var committedDigest string
		if lookupErr := s.db.QueryRowContext(ctx, `SELECT size_bytes, sha256 FROM attachment_upload_chunks
			WHERE upload_id = ? AND chunk_index = ?`, upload.ID, chunkIndex).Scan(&committedSize, &committedDigest); lookupErr == nil && committedSize == size && committedDigest == digest {
			if recoverErr := s.recoverBlobCommits(ctx); recoverErr == nil {
				return s.mustAttachmentUpload(ctx, upload.UserID, upload.ID)
			}
		}
		return AttachmentUpload{}, fmt.Errorf("commit attachment chunk: %w", err)
	}
	if err := s.finalizeBlobCommit(ctx, marker); err != nil {
		return AttachmentUpload{}, fmt.Errorf("finalize attachment chunk: %w", err)
	}
	return s.mustAttachmentUpload(ctx, upload.UserID, upload.ID)
}

func (s *Store) GetAttachmentUpload(ctx context.Context, userID, uploadID string) (AttachmentUpload, bool, error) {
	if s == nil || s.db == nil {
		return AttachmentUpload{}, false, errors.New("workspace store is closed")
	}
	s.attachmentMu.Lock()
	defer s.attachmentMu.Unlock()
	if err := s.expireStaleAttachmentUploads(ctx); err != nil {
		return AttachmentUpload{}, false, err
	}
	upload, found, err := s.getAttachmentUpload(ctx, strings.TrimSpace(userID), strings.TrimSpace(uploadID))
	if err != nil || !found {
		return upload, found, err
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE attachment_uploads SET updated_at = ? WHERE id = ? AND user_id = ?`, now, upload.ID, upload.UserID); err != nil {
		return AttachmentUpload{}, false, fmt.Errorf("refresh attachment upload reservation: %w", err)
	}
	upload.UpdatedAt = now
	return upload, true, nil
}

// expireStaleAttachmentUploads is called only while attachmentMu is held. A
// reservation remains recoverable while status/chunk activity refreshes its
// updated_at timestamp; abandoned uploads stop consuming logical capacity
// after the explicit TTL and their private chunk directories are reclaimed.
func (s *Store) expireStaleAttachmentUploads(ctx context.Context) error {
	cutoff := s.now().UTC().Add(-attachmentUploadReservationTTL)
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM attachment_uploads WHERE updated_at < ? ORDER BY id`, cutoff)
	if err != nil {
		return fmt.Errorf("inspect stale attachment uploads: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		result, err := s.db.ExecContext(ctx, `DELETE FROM attachment_uploads WHERE id = ? AND updated_at < ?`, id, cutoff)
		if err != nil {
			return fmt.Errorf("expire attachment upload: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			if err := s.removeAttachmentUploadChunks(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) getAttachmentUpload(ctx context.Context, userID, uploadID string) (AttachmentUpload, bool, error) {
	if userID == "" || uploadID == "" {
		return AttachmentUpload{}, false, errors.New("attachment upload user id and id are required")
	}
	var upload AttachmentUpload
	err := s.db.QueryRowContext(ctx, attachmentUploadSelect+` WHERE id = ? AND user_id = ?`, uploadID, userID).Scan(
		&upload.ID, &upload.UserID, &upload.ProjectID, &upload.Filename, &upload.ContentType,
		&upload.TotalSize, &upload.ChunkSize, &upload.ExpectedChunks, &upload.Ephemeral,
		&upload.FolderID, &upload.CreatedAt, &upload.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AttachmentUpload{}, false, nil
	}
	if err != nil {
		return AttachmentUpload{}, false, fmt.Errorf("get attachment upload: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT chunk_index, size_bytes FROM attachment_upload_chunks
		WHERE upload_id = ? ORDER BY chunk_index`, upload.ID)
	if err != nil {
		return AttachmentUpload{}, false, fmt.Errorf("list attachment chunks: %w", err)
	}
	defer rows.Close()
	upload.ReceivedChunks = []int{}
	for rows.Next() {
		var index int
		var size int64
		if err := rows.Scan(&index, &size); err != nil {
			return AttachmentUpload{}, false, fmt.Errorf("scan attachment chunk: %w", err)
		}
		upload.ReceivedChunks = append(upload.ReceivedChunks, index)
		upload.ReceivedBytes += size
	}
	if err := rows.Err(); err != nil {
		return AttachmentUpload{}, false, fmt.Errorf("iterate attachment chunks: %w", err)
	}
	return upload, true, nil
}

func (s *Store) mustAttachmentUpload(ctx context.Context, userID, uploadID string) (AttachmentUpload, error) {
	upload, found, err := s.getAttachmentUpload(ctx, userID, uploadID)
	if err != nil {
		return AttachmentUpload{}, err
	}
	if !found {
		return AttachmentUpload{}, errors.New("attachment upload not found")
	}
	return upload, nil
}
