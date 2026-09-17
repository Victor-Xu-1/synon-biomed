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
	"time"
)

const blobCommitSchema = `CREATE TABLE IF NOT EXISTS blob_commit_markers (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	aggregate_id TEXT NOT NULL,
	staging_path TEXT NOT NULL UNIQUE,
	final_path TEXT NOT NULL UNIQUE,
	sha256 TEXT NOT NULL,
	size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
	created_at TIMESTAMP NOT NULL
)`

type blobCommitMarker struct {
	ID, Kind, AggregateID, StagingPath, FinalPath, SHA256 string
	SizeBytes                                             int64
	CreatedAt                                             time.Time
}

func (s *Store) ensureBlobCommitSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, blobCommitSchema); err != nil {
		return fmt.Errorf("create blob commit marker schema: %w", err)
	}
	return nil
}

func (s *Store) insertBlobCommitMarkerTx(ctx context.Context, tx workspaceTransaction, marker blobCommitMarker) error {
	if tx == nil {
		return errors.New("blob commit marker transaction is required")
	}
	for field, value := range map[string]string{"id": marker.ID, "kind": marker.Kind, "aggregate id": marker.AggregateID,
		"staging path": marker.StagingPath, "final path": marker.FinalPath, "sha256": marker.SHA256} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("blob commit marker %s is required", field)
		}
	}
	if marker.SizeBytes < 0 {
		return errors.New("blob commit marker size must be non-negative")
	}
	stagingClean := filepath.ToSlash(filepath.Clean(marker.StagingPath))
	finalClean := filepath.ToSlash(filepath.Clean(marker.FinalPath))
	if !strings.HasPrefix(stagingClean, "staging/") {
		return errors.New("blob commit staging path must be inside staging")
	}
	if !(strings.HasPrefix(finalClean, "artifact-versions/") || strings.HasPrefix(finalClean, "attachments/") ||
		strings.HasPrefix(finalClean, "uploads/") || strings.HasPrefix(finalClean, "scientific-submissions/") ||
		strings.HasPrefix(finalClean, "large-tool-results/")) {
		return errors.New("blob commit final path has unsupported namespace")
	}
	if _, err := s.blobAbsolute(marker.StagingPath); err != nil {
		return fmt.Errorf("invalid blob staging marker path: %w", err)
	}
	if _, err := s.blobAbsolute(marker.FinalPath); err != nil {
		return fmt.Errorf("invalid blob final marker path: %w", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO blob_commit_markers
		(id, kind, aggregate_id, staging_path, final_path, sha256, size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, marker.ID, marker.Kind, marker.AggregateID,
		marker.StagingPath, marker.FinalPath, marker.SHA256, marker.SizeBytes, marker.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert blob commit marker: %w", err)
	}
	return nil
}

func (s *Store) blobRelative(path string) (string, error) {
	relative, err := filepath.Rel(s.blobRoot, path)
	if err != nil {
		return "", fmt.Errorf("make blob path relative: %w", err)
	}
	relative = filepath.Clean(relative)
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("blob staging path escapes blob root")
	}
	return filepath.ToSlash(relative), nil
}

// finalizeBlobCommit is idempotent. A crash before marker deletion is handled
// by recoverBlobCommits on the next Open.
func (s *Store) finalizeBlobCommit(ctx context.Context, marker blobCommitMarker) error {
	staging, err := s.blobAbsolute(marker.StagingPath)
	if err != nil {
		return err
	}
	final, err := s.blobAbsolute(marker.FinalPath)
	if err != nil {
		return err
	}
	if err := ensurePrivateDirectory(filepath.Dir(final)); err != nil {
		return err
	}
	finalOK, err := validateCommittedBlob(final, marker.SizeBytes, marker.SHA256)
	if err != nil {
		return err
	}
	if !finalOK {
		stagingOK, err := validateCommittedBlob(staging, marker.SizeBytes, marker.SHA256)
		if err != nil {
			return err
		}
		if !stagingOK {
			return fmt.Errorf("blob commit %q has neither valid staging nor final content", marker.ID)
		}
		if err := os.Rename(staging, final); err != nil {
			return fmt.Errorf("finalize blob commit %q: %w", marker.ID, err)
		}
		if err := syncDirectory(filepath.Dir(final)); err != nil {
			return err
		}
	} else if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove redundant blob staging file: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM blob_commit_markers WHERE id = ?`, marker.ID); err != nil {
		return fmt.Errorf("clear blob commit marker %q: %w", marker.ID, err)
	}
	return nil
}

func (s *Store) recoverBlobCommits(ctx context.Context) error {
	if err := s.ensureBlobCommitSchema(ctx); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, aggregate_id, staging_path, final_path, sha256, size_bytes, created_at
		FROM blob_commit_markers ORDER BY created_at, id`)
	if err != nil {
		return fmt.Errorf("list blob commit markers: %w", err)
	}
	markers := []blobCommitMarker{}
	for rows.Next() {
		var marker blobCommitMarker
		if err := rows.Scan(&marker.ID, &marker.Kind, &marker.AggregateID, &marker.StagingPath,
			&marker.FinalPath, &marker.SHA256, &marker.SizeBytes, &marker.CreatedAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan blob commit marker: %w", err)
		}
		markers = append(markers, marker)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, marker := range markers {
		if err := s.finalizeBlobCommit(ctx, marker); err != nil {
			return fmt.Errorf("recover blob commit: %w", err)
		}
	}
	return nil
}

func validateCommittedBlob(path string, size int64, digest string) (bool, error) {
	entry, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lstat committed blob %q: %w", path, err)
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return false, fmt.Errorf("committed blob %q is not a regular non-symlink file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open committed blob %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("stat committed blob %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false, fmt.Errorf("committed blob %q has invalid type or size", path)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return false, fmt.Errorf("hash committed blob %q: %w", path, err)
	}
	if hex.EncodeToString(hasher.Sum(nil)) != strings.ToLower(strings.TrimSpace(digest)) {
		return false, fmt.Errorf("committed blob %q checksum mismatch", path)
	}
	return true, nil
}
