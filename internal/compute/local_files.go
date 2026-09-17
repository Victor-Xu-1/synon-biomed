package compute

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const LocalDirectoryEntryCap = 1000

type LocalFileEntry struct {
	Name        string `json:"name"`
	IsDirectory bool   `json:"isDirectory"`
	Size        int64  `json:"size"`
	MTime       int64  `json:"mtime"`
}

type LocalDirectoryListing struct {
	Entries      []LocalFileEntry  `json:"entries"`
	Truncated    bool              `json:"truncated"`
	Roots        map[string]string `json:"roots"`
	ResolvedPath string            `json:"resolvedPath"`
}

func ListLocalDirectory(path, home string) (LocalDirectoryListing, error) {
	path = strings.TrimSpace(path)
	home = strings.TrimSpace(home)
	if path == "" {
		path = home
	}
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return LocalDirectoryListing{}, errors.New("path must be absolute and contain no control characters")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return LocalDirectoryListing{}, err
	}
	items, err := os.ReadDir(resolved)
	if err != nil {
		return LocalDirectoryListing{}, err
	}
	sort.Slice(items, func(i, j int) bool {
		leftDir, rightDir := items[i].IsDir(), items[j].IsDir()
		if leftDir != rightDir {
			return leftDir
		}
		return items[i].Name() < items[j].Name()
	})
	truncated := len(items) > LocalDirectoryEntryCap
	if truncated {
		items = items[:LocalDirectoryEntryCap]
	}
	entries := make([]LocalFileEntry, 0, len(items))
	for _, item := range items {
		info, err := os.Stat(filepath.Join(resolved, item.Name()))
		entry := LocalFileEntry{Name: item.Name(), IsDirectory: item.IsDir()}
		if err == nil {
			entry.IsDirectory = info.IsDir()
			if !entry.IsDirectory {
				entry.Size = info.Size()
			}
			entry.MTime = info.ModTime().UnixMilli()
		}
		entries = append(entries, entry)
	}
	return LocalDirectoryListing{Entries: entries, Truncated: truncated, Roots: map[string]string{"home": home}, ResolvedPath: resolved}, nil
}
