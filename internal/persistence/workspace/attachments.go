package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	maxAttachmentChunk   int64 = 64 << 20
	maxAttachmentNameLen       = 255
)

type Attachment struct {
	ID          string    `json:"id"`
	UserID      string    `json:"userId,omitempty"`
	ProjectID   string    `json:"projectId"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"contentType"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	BlobPath    string    `json:"-"`
	Ephemeral   bool      `json:"ephemeral"`
	FolderID    string    `json:"folderId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type CreateAttachmentInput struct {
	UserID      string
	ProjectID   string
	Filename    string
	ContentType string
	Ephemeral   bool
	FolderID    string
}

const attachmentSelect = `
	SELECT id, user_id, project_id, filename, content_type, size_bytes, sha256,
		blob_path, ephemeral, COALESCE(folder_id, ''), created_at, updated_at
	FROM attachments`

func (s *Store) CreateAttachment(ctx context.Context, input CreateAttachmentInput, content io.Reader) (Attachment, error) {
	if s == nil || s.db == nil {
		return Attachment{}, errors.New("workspace store is closed")
	}
	if content == nil {
		return Attachment{}, errors.New("attachment content is required")
	}
	input, err := normalizeAttachmentInput(input)
	if err != nil {
		return Attachment{}, err
	}
	s.attachmentMu.Lock()
	defer s.attachmentMu.Unlock()
	id := uuid.NewString()
	relativePath := attachmentBlobPath(id)
	temporary, size, digest, err := s.writeTemporaryBlob(ctx, content, 0)
	if err != nil {
		return Attachment{}, err
	}
	stagingPath, err := s.blobRelative(temporary)
	if err != nil {
		_ = os.Remove(temporary)
		return Attachment{}, err
	}
	now := s.now().UTC()
	attachment := Attachment{
		ID: id, UserID: input.UserID, ProjectID: input.ProjectID, Filename: input.Filename,
		ContentType: input.ContentType, Size: size, SHA256: digest, BlobPath: relativePath,
		Ephemeral: input.Ephemeral, FolderID: input.FolderID, CreatedAt: now, UpdatedAt: now,
	}
	commitAttempted := false
	defer func() {
		if !commitAttempted {
			_ = os.Remove(temporary)
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attachment{}, fmt.Errorf("begin attachment create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireProjectOwnerTx(ctx, tx, input.ProjectID, input.UserID); err != nil {
		return Attachment{}, err
	}
	if err := s.insertAttachment(ctx, tx, attachment); err != nil {
		return Attachment{}, err
	}
	marker := blobCommitMarker{ID: "attachment:" + attachment.ID, Kind: "attachment", AggregateID: attachment.ID,
		StagingPath: stagingPath, FinalPath: relativePath, SHA256: attachment.SHA256,
		SizeBytes: attachment.Size, CreatedAt: now}
	if err := s.insertBlobCommitMarkerTx(ctx, tx, marker); err != nil {
		return Attachment{}, err
	}
	commitAttempted = true
	if err := tx.Commit(); err != nil {
		return Attachment{}, fmt.Errorf("commit attachment create: %w", err)
	}
	if err := s.finalizeBlobCommit(ctx, marker); err != nil {
		return Attachment{}, fmt.Errorf("finalize committed attachment blob: %w", err)
	}
	return attachment, nil
}

func (s *Store) GetAttachment(ctx context.Context, userID, id string) (Attachment, bool, error) {
	if s == nil || s.db == nil {
		return Attachment{}, false, errors.New("workspace store is closed")
	}
	userID, id = strings.TrimSpace(userID), strings.TrimSpace(id)
	if userID == "" || id == "" {
		return Attachment{}, false, errors.New("attachment user id and id are required")
	}
	return scanAttachment(s.db.QueryRowContext(ctx, attachmentSelect+` WHERE id = ? AND user_id = ?`, id, userID))
}

func (s *Store) OpenAttachment(ctx context.Context, userID, id string) (Attachment, *os.File, bool, error) {
	attachment, found, err := s.GetAttachment(ctx, userID, id)
	if err != nil || !found {
		return attachment, nil, found, err
	}
	absolute, err := s.blobAbsolute(attachment.BlobPath)
	if err != nil {
		return Attachment{}, nil, false, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return Attachment{}, nil, false, fmt.Errorf("stat attachment blob: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Attachment{}, nil, false, errors.New("attachment blob is not a regular file")
	}
	if info.Size() != attachment.Size {
		return Attachment{}, nil, false, fmt.Errorf("attachment blob size mismatch: got %d want %d", info.Size(), attachment.Size)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return Attachment{}, nil, false, fmt.Errorf("open attachment blob: %w", err)
	}
	return attachment, file, true, nil
}

func normalizeAttachmentInput(input CreateAttachmentInput) (CreateAttachmentInput, error) {
	input.UserID = strings.TrimSpace(input.UserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.Filename = strings.TrimSpace(input.Filename)
	input.ContentType = strings.TrimSpace(input.ContentType)
	input.FolderID = strings.TrimSpace(input.FolderID)
	if input.UserID == "" || input.ProjectID == "" || input.Filename == "" {
		return CreateAttachmentInput{}, errors.New("attachment user id, project id, and filename are required")
	}
	if len(input.Filename) > maxAttachmentNameLen || strings.ContainsAny(input.Filename, "/\\\x00") ||
		input.Filename == "." || input.Filename == ".." {
		return CreateAttachmentInput{}, errors.New("attachment filename is invalid")
	}
	if input.ContentType == "" {
		input.ContentType = mime.TypeByExtension(filepath.Ext(input.Filename))
		if input.ContentType == "" {
			input.ContentType = "application/octet-stream"
		}
	}
	if len(input.ContentType) > 255 || strings.ContainsAny(input.ContentType, "\r\n\x00") {
		return CreateAttachmentInput{}, errors.New("attachment content type is invalid")
	}
	return input, nil
}

func normalizeSHA256(value string) (string, error) {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
	if value == "" {
		return "", nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("attachment checksum must be a 64-character SHA-256 hex digest")
	}
	return value, nil
}

func (s *Store) requireProject(ctx context.Context, projectID string) error {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ?`, projectID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("project %q does not exist", projectID)
	}
	if err != nil {
		return fmt.Errorf("look up attachment project: %w", err)
	}
	return nil
}

type attachmentRow interface {
	Scan(dest ...any) error
}

func scanAttachment(row attachmentRow) (Attachment, bool, error) {
	var attachment Attachment
	err := row.Scan(
		&attachment.ID, &attachment.UserID, &attachment.ProjectID, &attachment.Filename,
		&attachment.ContentType, &attachment.Size, &attachment.SHA256, &attachment.BlobPath,
		&attachment.Ephemeral, &attachment.FolderID, &attachment.CreatedAt, &attachment.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, false, nil
	}
	if err != nil {
		return Attachment{}, false, fmt.Errorf("get attachment: %w", err)
	}
	return attachment, true, nil
}

type attachmentExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) insertAttachment(ctx context.Context, execer attachmentExecer, attachment Attachment) error {
	if execer == nil {
		execer = s.db
	}
	_, err := execer.ExecContext(ctx, `
		INSERT INTO attachments
			(id, user_id, project_id, filename, content_type, size_bytes, sha256,
			 blob_path, ephemeral, folder_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attachment.ID, attachment.UserID, attachment.ProjectID, attachment.Filename,
		attachment.ContentType, attachment.Size, attachment.SHA256, attachment.BlobPath,
		attachment.Ephemeral, nullableString(attachment.FolderID), attachment.CreatedAt, attachment.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert attachment: %w", err)
	}
	return nil
}

func attachmentBlobPath(id string) string {
	prefix := id
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.ToSlash(filepath.Join("attachments", prefix, id+".blob"))
}

func (s *Store) writeTemporaryBlob(ctx context.Context, content io.Reader, limit int64) (string, int64, string, error) {
	stagingDir := filepath.Join(s.blobRoot, "staging")
	if err := ensurePrivateDirectory(stagingDir); err != nil {
		return "", 0, "", err
	}
	return writeTemporaryFile(ctx, stagingDir, content, limit)
}

func writeTemporaryFile(ctx context.Context, directory string, content io.Reader, limit int64) (string, int64, string, error) {
	file, err := os.CreateTemp(directory, ".part-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create attachment staging file: %w", err)
	}
	path := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", 0, "", fmt.Errorf("secure attachment staging file: %w", err)
	}
	hasher := sha256.New()
	var source io.Reader = &contextReader{ctx: ctx, reader: content}
	if limit > 0 {
		source = io.LimitReader(source, limit+1)
	}
	written, err := io.Copy(io.MultiWriter(file, hasher), source)
	if err != nil {
		return "", 0, "", fmt.Errorf("write attachment staging file: %w", err)
	}
	if limit > 0 && written > limit {
		return "", 0, "", fmt.Errorf("attachment content exceeds %d bytes", limit)
	}
	if err := file.Sync(); err != nil {
		return "", 0, "", fmt.Errorf("sync attachment staging file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, "", fmt.Errorf("close attachment staging file: %w", err)
	}
	remove = false
	return path, written, hex.EncodeToString(hasher.Sum(nil)), nil
}

func (s *Store) blobAbsolute(relativePath string) (string, error) {
	relativePath = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relativePath)))
	if relativePath == "." || filepath.IsAbs(relativePath) {
		return "", errors.New("attachment blob path is invalid")
	}
	absolute := filepath.Join(s.blobRoot, relativePath)
	rel, err := filepath.Rel(s.blobRoot, absolute)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("attachment blob path escapes the workspace root")
	}
	return absolute, nil
}

func (s *Store) mustBlobAbsolute(relativePath string) string {
	absolute, _ := s.blobAbsolute(relativePath)
	return absolute
}

func openRegularFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
	}
	return os.Open(path)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(buffer)
	}
}
