package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

var (
	errAgentWorkspaceDownloadAuthority = errors.New("workspace download authority is unavailable")
	errAgentWorkspaceDownloadConflict  = errors.New("workspace download target conflicts with an existing file")
)

// ensureAgentWorkspaceDownloadTarget validates a path-free workspace target
// against the capacity contract owned by the caller. Download families must
// not inherit an unrelated source's size limit merely because they share the
// same atomic publication mechanism.
func ensureAgentWorkspaceDownloadTarget(
	ctx context.Context,
	workspaceDir, filename string,
	expectedSize int64,
	expectedSHA string,
	maxBytes int64,
) error {
	rootPath, err := canonicalHostDirectory(workspaceDir)
	if err != nil || filepath.Base(filename) != filename || maxBytes <= 0 {
		return errAgentWorkspaceDownloadAuthority
	}
	if expectedSize > maxBytes {
		return errAgentWorkspaceDownloadConflict
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return errAgentWorkspaceDownloadAuthority
	}
	defer root.Close()
	info, err := root.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errAgentWorkspaceDownloadConflict
	}
	if expectedSize <= 0 || expectedSHA == "" {
		// Identity is not known before transfer. Preserve an existing regular
		// file and make the post-verification check decide whether it is the
		// exact immutable object; publication never overwrites it.
		return nil
	}
	size, digest, err := hashAgentWorkspaceDownloadRootFile(ctx, root, filename, maxBytes)
	if err != nil || size != expectedSize || digest != expectedSHA {
		return errAgentWorkspaceDownloadConflict
	}
	return nil
}

// publishAgentWorkspaceDownloadFile installs verified content without
// overwriting a concurrent writer. The temporary file is linked into place
// only after its exact size and digest have been checked and synced.
func publishAgentWorkspaceDownloadFile(
	ctx context.Context,
	workspaceDir, filename string,
	content io.ReadSeeker,
	expectedSize int64,
	expectedSHA string,
	maxBytes int64,
) error {
	rootPath, err := canonicalHostDirectory(workspaceDir)
	if err != nil || filepath.Base(filename) != filename || expectedSize <= 0 ||
		expectedSize > maxBytes || expectedSHA == "" || maxBytes <= 0 {
		return errAgentWorkspaceDownloadAuthority
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return errAgentWorkspaceDownloadAuthority
	}
	defer root.Close()
	if info, statErr := root.Lstat(filename); statErr == nil {
		if !info.Mode().IsRegular() {
			return errAgentWorkspaceDownloadConflict
		}
		size, digest, hashErr := hashAgentWorkspaceDownloadRootFile(ctx, root, filename, maxBytes)
		if hashErr == nil && size == expectedSize && digest == expectedSHA {
			return nil
		}
		return errAgentWorkspaceDownloadConflict
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errAgentWorkspaceDownloadAuthority
	}
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return errAgentWorkspaceDownloadAuthority
	}
	temporary := ".synon-download-" + uuid.NewString() + ".tmp"
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errAgentWorkspaceDownloadAuthority
	}
	defer func() { _ = root.Remove(temporary) }()
	hasher := sha256.New()
	written, copyErr := io.Copy(
		io.MultiWriter(file, hasher),
		io.LimitReader(&contextReader{ctx: ctx, reader: content}, maxBytes+1),
	)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written != expectedSize ||
		hex.EncodeToString(hasher.Sum(nil)) != expectedSHA {
		return errAgentWorkspaceDownloadConflict
	}
	if err := root.Link(temporary, filename); err != nil {
		if info, statErr := root.Lstat(filename); statErr == nil && info.Mode().IsRegular() {
			size, digest, hashErr := hashAgentWorkspaceDownloadRootFile(ctx, root, filename, maxBytes)
			if hashErr == nil && size == expectedSize && digest == expectedSHA {
				return nil
			}
		}
		return errAgentWorkspaceDownloadConflict
	}
	return nil
}

func hashAgentWorkspaceDownloadRootFile(
	ctx context.Context,
	root *os.Root,
	filename string,
	maxBytes int64,
) (int64, string, error) {
	if root == nil || maxBytes <= 0 {
		return 0, "", errAgentWorkspaceDownloadAuthority
	}
	file, err := root.Open(filename)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hasher := sha256.New()
	sizeBytes, err := io.Copy(hasher, io.LimitReader(&contextReader{ctx: ctx, reader: file}, maxBytes+1))
	if err != nil || sizeBytes > maxBytes {
		return 0, "", errAgentWorkspaceDownloadConflict
	}
	return sizeBytes, hex.EncodeToString(hasher.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
