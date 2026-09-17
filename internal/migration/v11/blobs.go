package v11

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var safeArtifactVersionID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func copyArtifactBlobs(ctx context.Context, snapshotDB, sourceRoot, targetBlobRoot string) error {
	db, err := sql.Open("sqlite", readOnlyDSN(snapshotDB))
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, storage_path, checksum, size_bytes FROM artifact_versions ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list v1.1 artifact versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, storagePath, expectedHash string
		var expectedSize int64
		if err := rows.Scan(&id, &storagePath, &expectedHash, &expectedSize); err != nil {
			return err
		}
		if !safeArtifactVersionID.MatchString(id) {
			return fmt.Errorf("artifact version id %q is unsafe for migration", id)
		}
		source, err := secureArtifactSource(sourceRoot, storagePath)
		if err != nil {
			return fmt.Errorf("artifact version %s: %w", id, err)
		}
		prefix := id
		if len(prefix) > 2 {
			prefix = prefix[:2]
		}
		target := filepath.Join(targetBlobRoot, "artifact-versions", prefix, id+".blob")
		if err := copyVerifiedFile(ctx, source, target, expectedSize, expectedHash); err != nil {
			return fmt.Errorf("migrate artifact version %s: %w", id, err)
		}
	}
	return rows.Err()
}

func secureArtifactSource(root, relative string) (string, error) {
	root, err := canonicalDirectory(root)
	if err != nil {
		return "", fmt.Errorf("artifact root: %w", err)
	}
	relative = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relative)))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact storage path escapes the source root")
	}
	path := filepath.Join(root, relative)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("artifact file %q is unavailable: %w", relative, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("artifact source is not a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !pathContains(root, resolved) || resolved != path {
		return "", errors.New("artifact path contains symbolic links")
	}
	return path, nil
}

func copyVerifiedFile(ctx context.Context, source, target string, expectedSize int64, expectedHash string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = output.Close()
		if !ok {
			_ = os.Remove(target)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(output, hash), &contextFileReader{ctx: ctx, file: input})
	if err != nil {
		return err
	}
	actualHash := hex.EncodeToString(hash.Sum(nil))
	if expectedSize >= 0 && written != expectedSize {
		return fmt.Errorf("artifact size mismatch: got %d, expected %d", written, expectedSize)
	}
	if len(expectedHash) == 64 && !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("artifact checksum mismatch: got %s, expected %s", actualHash, expectedHash)
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

type contextFileReader struct {
	ctx  context.Context
	file *os.File
}

func (r *contextFileReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.file.Read(buffer)
	}
}
