package compute

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const LocalDownloadMaxBytes int64 = 2 << 30
const LocalImportMaxBytes int64 = 50 << 20

type LocalDownload struct {
	File     *os.File
	Filename string
	Size     int64
	MTime    int64
}

func OpenLocalDownload(path, confine, workspaceRoot string) (LocalDownload, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return LocalDownload{}, errors.New("path must be absolute and contain no control characters")
	}
	if confine != "" && confine != "workspaces" {
		return LocalDownload{}, errors.New("confine must be workspaces")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return LocalDownload{}, err
	}
	if confine == "workspaces" {
		root, err := filepath.EvalSymlinks(filepath.Clean(strings.TrimSpace(workspaceRoot)))
		if err != nil || !filepath.IsAbs(root) {
			return LocalDownload{}, errors.New("workspace root is unavailable")
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return LocalDownload{}, errors.New("path is outside the workspace root")
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return LocalDownload{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return LocalDownload{}, errors.New("path is not a non-empty regular file")
	}
	if info.Size() > LocalDownloadMaxBytes {
		return LocalDownload{}, errors.New("file exceeds the 2 GiB download limit")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return LocalDownload{}, err
	}
	return LocalDownload{File: file, Filename: filepath.Base(resolved), Size: info.Size(), MTime: info.ModTime().UnixMilli()}, nil
}
