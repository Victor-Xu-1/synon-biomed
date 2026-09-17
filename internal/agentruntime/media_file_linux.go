//go:build linux

package agentruntime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func readAllowedMediaFileHandle(path string, roots []string, maxBytes int64) ([]byte, error) {
	for _, rawRoot := range roots {
		root := strings.TrimSpace(rawRoot)
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(resolvedRoot, path)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		file, err := openMediaFileAtRoot(resolvedRoot, relative)
		if err != nil {
			return nil, errors.New("media file could not be opened safely")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.Size() <= 0 || info.Size() > maxBytes {
			return nil, fmt.Errorf("media file size must be between 1 and %d bytes", maxBytes)
		}
		data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
		if err != nil {
			return nil, errors.New("media file could not be read")
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("media part exceeds limit of %d bytes", maxBytes)
		}
		return data, nil
	}
	return nil, errors.New("media file is outside allowed roots")
}

func openMediaFileAtRoot(root, relative string) (*os.File, error) {
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	if len(parts) == 0 {
		return nil, errors.New("media file path is invalid")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.IndexByte(part, 0) >= 0 {
			return nil, errors.New("media file path is invalid")
		}
	}
	current, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return nil, openErr
		}
		current = next
	}
	fd, err := unix.Openat(current, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	_ = unix.Close(current)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, errors.New("media target must be one regular file")
	}
	return os.NewFile(uintptr(fd), filepath.Base(relative)), nil
}
