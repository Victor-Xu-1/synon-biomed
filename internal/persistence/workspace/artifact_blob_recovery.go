package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func (s *Store) reconcileArtifactBlobs(ctx context.Context) error {
	if err := s.ensureMutationResultLedgerSchema(ctx); err != nil {
		return err
	}
	if err := s.recoverBlobCommits(ctx); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT storage_path, content_available
		FROM artifact_versions WHERE storage_path <> ''`)
	if err != nil {
		return fmt.Errorf("list referenced artifact blobs: %w", err)
	}
	referenced := map[string]bool{}
	for rows.Next() {
		var relative string
		var available bool
		if err := rows.Scan(&relative, &available); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan referenced artifact blob: %w", err)
		}
		if !available {
			continue
		}
		absolute, err := s.blobAbsolute(relative)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("invalid referenced artifact blob: %w", err)
		}
		referenced[filepath.Clean(absolute)] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate referenced artifact blobs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close referenced artifact blob scan: %w", err)
	}

	root := filepath.Join(s.blobRoot, "artifact-versions")
	if err := ensurePrivateDirectory(root); err != nil {
		return err
	}
	found := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact blob tree contains symlink %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact blob tree contains non-regular file %q", path)
		}
		path = filepath.Clean(path)
		if referenced[path] {
			found[path] = true
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove orphan artifact blob %q: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile artifact blobs: %w", err)
	}
	missing := make([]string, 0)
	for path := range referenced {
		if !found[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		if err := s.markMissingArtifactBlobsUnavailable(ctx, missing); err != nil {
			return err
		}
	}
	if err := s.reconcileAttachmentBlobs(ctx); err != nil {
		return err
	}
	if err := s.reconcileAttachmentUploads(ctx); err != nil {
		return err
	}
	if err := s.reconcileScientificSubmissionBlobs(ctx); err != nil {
		return err
	}
	return s.cleanArtifactStaging()
}

// markMissingArtifactBlobsUnavailable preserves the immutable version metadata
// and digest while preventing one lost external blob from making the whole
// workspace impossible to reopen. Content readers fail closed through the
// existing content_available contract; a later verified recovery can restore
// the blob and set the version available again without rewriting its identity.
func (s *Store) markMissingArtifactBlobsUnavailable(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin missing artifact recovery: %w", err)
	}
	defer tx.Rollback()
	for _, absolute := range paths {
		relative, err := filepath.Rel(s.blobRoot, absolute)
		if err != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(filepath.ToSlash(relative), "../") {
			return fmt.Errorf("invalid missing artifact blob path %q", absolute)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artifact_versions
			SET content_available=0 WHERE storage_path=? AND content_available=1`,
			filepath.ToSlash(relative)); err != nil {
			return fmt.Errorf("mark missing artifact blob unavailable: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit missing artifact recovery: %w", err)
	}
	return nil
}

func (s *Store) reconcileScientificSubmissionBlobs(ctx context.Context) error {
	root := filepath.Join(s.blobRoot, "scientific-submissions")
	if err := ensurePrivateDirectory(root); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT archive_storage_path
		FROM scientific_compute_submissions WHERE archive_released_at IS NULL`)
	if err != nil {
		return fmt.Errorf("list referenced scientific submission archives: %w", err)
	}
	referenced := map[string]bool{}
	for rows.Next() {
		var relative string
		if err := rows.Scan(&relative); err != nil {
			_ = rows.Close()
			return err
		}
		if !strings.HasPrefix(filepath.ToSlash(filepath.Clean(relative)), "scientific-submissions/") {
			_ = rows.Close()
			return fmt.Errorf("invalid scientific submission archive path %q", relative)
		}
		absolute, err := s.blobAbsolute(relative)
		if err != nil {
			_ = rows.Close()
			return err
		}
		referenced[filepath.Clean(absolute)] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	found := map[string]bool{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("scientific submission archive tree contains symlink %q", path)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("scientific submission archive tree contains non-regular file %q", path)
		}
		path = filepath.Clean(path)
		if referenced[path] {
			found[path] = true
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove orphan scientific submission archive %q: %w", path, err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("reconcile scientific submission archives: %w", err)
	}
	for path := range referenced {
		if !found[path] {
			return fmt.Errorf("referenced scientific submission archive is missing: %s", path)
		}
	}
	return nil
}

func (s *Store) reconcileAttachmentBlobs(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT blob_path FROM attachments WHERE blob_path <> ''`)
	if err != nil {
		return fmt.Errorf("list referenced attachment blobs: %w", err)
	}
	referenced := map[string]bool{}
	for rows.Next() {
		var relative string
		if err := rows.Scan(&relative); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan referenced attachment blob: %w", err)
		}
		absolute, err := s.blobAbsolute(relative)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("invalid referenced attachment blob: %w", err)
		}
		referenced[filepath.Clean(absolute)] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	root := filepath.Join(s.blobRoot, "attachments")
	if err := ensurePrivateDirectory(root); err != nil {
		return err
	}
	found := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("attachment blob tree contains symlink %q", path)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("attachment blob tree contains non-regular file %q", path)
		}
		path = filepath.Clean(path)
		if referenced[path] {
			found[path] = true
			return nil
		}
		return os.Remove(path)
	})
	if err != nil {
		return fmt.Errorf("reconcile attachment blobs: %w", err)
	}
	for path := range referenced {
		if !found[path] {
			return fmt.Errorf("referenced attachment blob is missing: %s", path)
		}
	}
	return nil
}

func (s *Store) reconcileAttachmentUploads(ctx context.Context) error {
	root := filepath.Join(s.blobRoot, "uploads")
	if err := ensurePrivateDirectory(root); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM attachment_uploads`)
	if err != nil {
		return fmt.Errorf("list active attachment uploads: %w", err)
	}
	active := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		active[id] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Older SQLite connections did not always have foreign-key enforcement
	// enabled while finalizing an upload. A crash or legacy finalize could
	// therefore leave chunk receipts after the owning upload row was removed.
	// They are staging metadata, not committed attachments. Reconcile those
	// orphan receipts before validating active upload paths; their directories
	// are removed by the same active-upload sweep below. Treating them as a
	// fatal path violation makes an otherwise healthy workspace unbootable.
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM attachment_upload_chunks
		WHERE NOT EXISTS (
			SELECT 1 FROM attachment_uploads upload
			WHERE upload.id=attachment_upload_chunks.upload_id
		)`); err != nil {
		return fmt.Errorf("remove orphan attachment upload chunks: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("list attachment upload directories: %w", err)
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("attachment upload root contains symlink %q", path)
		}
		if !entry.IsDir() {
			return fmt.Errorf("attachment upload root contains non-directory %q", path)
		}
		if !active[entry.Name()] {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove orphan attachment upload %q: %w", path, err)
			}
		}
	}
	chunkRows, err := s.db.QueryContext(ctx, `SELECT upload_id, size_bytes, sha256, blob_path FROM attachment_upload_chunks`)
	if err != nil {
		return fmt.Errorf("list attachment upload chunks: %w", err)
	}
	defer chunkRows.Close()
	for chunkRows.Next() {
		var uploadID, digest, relative string
		var size int64
		if err := chunkRows.Scan(&uploadID, &size, &digest, &relative); err != nil {
			return err
		}
		if !active[uploadID] || !strings.HasPrefix(filepath.ToSlash(filepath.Clean(relative)), "uploads/"+uploadID+"/") {
			return fmt.Errorf("attachment chunk has invalid upload path %q", relative)
		}
		absolute, err := s.blobAbsolute(relative)
		if err != nil {
			return err
		}
		valid, err := validateCommittedBlob(absolute, size, digest)
		if err != nil {
			return fmt.Errorf("referenced attachment chunk is unavailable %q: %w", relative, err)
		}
		if !valid {
			return fmt.Errorf("referenced attachment chunk is missing %q", relative)
		}
	}
	return chunkRows.Err()
}

func (s *Store) cleanArtifactStaging() error {
	staging := filepath.Join(s.blobRoot, "staging")
	entries, err := os.ReadDir(staging)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list artifact staging files: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !isRecoverableBlobStagingName(entry.Name()) {
			continue
		}
		path := filepath.Join(staging, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact staging contains symlink %q", path)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove abandoned artifact staging file: %w", err)
		}
	}
	return nil
}

func isRecoverableBlobStagingName(name string) bool {
	for _, prefix := range []string{".artifact-part-", ".artifact-write-", ".part-", ".final-", ".scientific-submission-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
