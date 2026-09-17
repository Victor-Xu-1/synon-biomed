//go:build linux

package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type agentWorkspaceEditAuthority struct {
	parentFD int
	name     string
}

type agentWorkspaceStagingFile struct {
	file *os.File
	name string
}

func openAgentWorkspaceRegularFile(root, relative string) (*os.File, error) {
	parentFD, name, err := openAgentWorkspaceParent(root, relative, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, errors.New("workspace target is not a regular file")
	}
	return os.NewFile(uintptr(fd), filepath.Join(root, relative)), nil
}

func openAgentWorkspaceEditAuthority(root, relative string, createParents bool) (*agentWorkspaceEditAuthority, error) {
	parentFD, name, err := openAgentWorkspaceParent(root, relative, createParents)
	if err != nil {
		return nil, err
	}
	return &agentWorkspaceEditAuthority{parentFD: parentFD, name: name}, nil
}

func openAgentWorkspaceParent(root, relative string, createParents bool) (int, string, error) {
	parts, err := secureAgentWorkspacePathParts(relative)
	if err != nil {
		return -1, "", err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, "", err
	}
	current := rootFD
	fail := func(err error) (int, string, error) {
		_ = unix.Close(current)
		return -1, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, syscall.ENOENT) && createParents {
			if mkdirErr := unix.Mkdirat(current, part, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, syscall.EEXIST) {
				return fail(mkdirErr)
			}
			next, openErr = unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			return fail(openErr)
		}
		if current != rootFD {
			_ = unix.Close(current)
		} else {
			_ = unix.Close(rootFD)
		}
		current = next
		rootFD = -1
	}
	return current, parts[len(parts)-1], nil
}

func (authority *agentWorkspaceEditAuthority) openCurrent() (*os.File, os.FileMode, bool, error) {
	fd, err := unix.Openat(authority.parentFD, authority.name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil, 0o600, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, 0, false, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, 0, false, errors.New("workspace target must be one regular file")
	}
	return os.NewFile(uintptr(fd), authority.name), os.FileMode(stat.Mode & 0o777), true, nil
}

func (authority *agentWorkspaceEditAuthority) createStaging(mode os.FileMode) (*agentWorkspaceStagingFile, error) {
	if mode == 0 {
		mode = 0o600
	}
	for attempt := 0; attempt < 16; attempt++ {
		name := ".synon-edit-" + uuid.NewString()
		fd, err := unix.Openat(
			authority.parentFD,
			name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			uint32(mode.Perm()),
		)
		if errors.Is(err, syscall.EEXIST) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := unix.Fchmod(fd, uint32(mode.Perm())); err != nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(authority.parentFD, name, 0)
			return nil, err
		}
		return &agentWorkspaceStagingFile{file: os.NewFile(uintptr(fd), name), name: name}, nil
	}
	return nil, errors.New("could not allocate a workspace staging file")
}

func (authority *agentWorkspaceEditAuthority) commit(staging *agentWorkspaceStagingFile) error {
	if staging == nil || staging.file == nil || staging.name == "" {
		return errors.New("workspace staging file is unavailable")
	}
	if err := staging.file.Sync(); err != nil {
		return err
	}
	if err := staging.file.Close(); err != nil {
		return err
	}
	staging.file = nil
	if err := unix.Renameat(authority.parentFD, staging.name, authority.parentFD, authority.name); err != nil {
		return err
	}
	staging.name = ""
	return unix.Fsync(authority.parentFD)
}

func (authority *agentWorkspaceEditAuthority) actualPath() (string, error) {
	parent, err := filepath.EvalSymlinks("/proc/self/fd/" + strconv.Itoa(authority.parentFD))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, authority.name), nil
}

func (authority *agentWorkspaceEditAuthority) close() error {
	if authority == nil || authority.parentFD < 0 {
		return nil
	}
	err := unix.Close(authority.parentFD)
	authority.parentFD = -1
	return err
}

func (staging *agentWorkspaceStagingFile) discard(authority *agentWorkspaceEditAuthority) {
	if staging == nil {
		return
	}
	if staging.file != nil {
		_ = staging.file.Close()
		staging.file = nil
	}
	if staging.name != "" && authority != nil && authority.parentFD >= 0 {
		_ = unix.Unlinkat(authority.parentFD, staging.name, 0)
		staging.name = ""
	}
}

func secureEnsureAgentWorkspaceDirectory(path string, mode os.FileMode) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || !filepath.IsAbs(absolute) {
		return "", errors.New("workspace directory path is invalid")
	}
	parts := strings.FieldsFunc(filepath.Clean(absolute), func(r rune) bool { return r == filepath.Separator })
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(current) }()
	for _, part := range parts {
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, syscall.ENOENT) {
			if mkdirErr := unix.Mkdirat(current, part, uint32(mode.Perm())); mkdirErr != nil && !errors.Is(mkdirErr, syscall.EEXIST) {
				return "", mkdirErr
			}
			next, openErr = unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			return "", openErr
		}
		_ = unix.Close(current)
		current = next
	}
	resolved, err := filepath.EvalSymlinks("/proc/self/fd/" + strconv.Itoa(current))
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("workspace directory did not resolve to an absolute path")
	}
	return filepath.Clean(resolved), nil
}
