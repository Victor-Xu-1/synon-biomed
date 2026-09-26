package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"syscall"

	"github.com/google/uuid"
)

var (
	errWorkspaceFileTooLarge = errors.New("workspace file exceeds its byte limit")
	errWorkspaceFileConflict = errors.New("workspace file conflicts with existing content")
)

// stagedWorkspaceFile is private, synced content within an already authorized
// root. All families use the same bounded copy and no-overwrite publication;
// each caller still owns its source authority, naming and capacity policy.
type stagedWorkspaceFile struct {
	root   *os.Root
	name   string
	size   int64
	digest string
}

func stageWorkspaceFile(ctx context.Context, root *os.Root, source io.Reader, maxBytes int64) (*stagedWorkspaceFile, error) {
	if root == nil || maxBytes < 0 || maxBytes == math.MaxInt64 {
		return nil, errWorkspaceFileTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := ".synon-incoming-" + uuid.NewString() + ".tmp"
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(&contextReader{ctx: ctx, reader: source}, maxBytes+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	err = errors.Join(copyErr, syncErr, closeErr, ctx.Err())
	if size > maxBytes {
		err = errors.Join(err, errWorkspaceFileTooLarge)
	}
	if err != nil {
		return nil, errors.Join(err, root.Remove(name))
	}
	return &stagedWorkspaceFile{root: root, name: name, size: size, digest: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (stage *stagedWorkspaceFile) close() error { return stage.root.Remove(stage.name) }

func (stage *stagedWorkspaceFile) publish(ctx context.Context, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Linking is atomic and never replaces another writer's directory entry.
	// Both paths remain relative to the same held root, including nested upload
	// directories. A symlink replacement cannot redirect the operation outside.
	if err := stage.root.Link(stage.name, destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := stage.root.Lstat(destination)
	if err != nil || !info.Mode().IsRegular() || info.Size() != stage.size {
		return errWorkspaceFileConflict
	}
	size, digest, err := hashWorkspaceRootFile(ctx, stage.root, destination, stage.size)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errWorkspaceFileConflict
	}
	if size != stage.size || digest != stage.digest {
		return errWorkspaceFileConflict
	}
	return nil
}

func hashWorkspaceRootFile(ctx context.Context, root *os.Root, name string, maxBytes int64) (int64, string, error) {
	if root == nil || maxBytes < 0 || maxBytes == math.MaxInt64 {
		return 0, "", errWorkspaceFileTooLarge
	}
	file, err := openRegularWorkspaceRootFile(root, name)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return 0, "", errWorkspaceFileConflict
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(&contextReader{ctx: ctx, reader: file}, maxBytes+1))
	if err != nil {
		return 0, "", err
	}
	if size > maxBytes {
		return 0, "", errWorkspaceFileTooLarge
	}
	return size, hex.EncodeToString(hash.Sum(nil)), ctx.Err()
}

func openRegularWorkspaceRootFile(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errWorkspaceFileConflict
	}
	// Nonblocking open prevents a concurrent regular-file-to-FIFO replacement
	// from hanging before we can inspect the opened descriptor. It has no
	// effect for a regular file (and is ignored by the Windows file opener).
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errWorkspaceFileConflict
	}
	return file, nil
}
