package server

import (
	"os"
	"path/filepath"
	"sort"
)

// readAuthorizedHostDirectory keeps the grant root open through both directory
// enumeration and metadata reads. Re-resolving an absolute target after the
// grant check would allow directory-to-symlink replacement to escape the grant.
func readAuthorizedHostDirectory(grantedRoot, target string) ([]os.FileInfo, error) {
	if !hostPathWithin(grantedRoot, target) {
		return nil, errWebFSForbidden
	}
	relative, err := filepath.Rel(grantedRoot, target)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(grantedRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.OpenRoot(relative)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	file, err := directory.Open(".")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	items := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := directory.Lstat(entry.Name())
		if os.IsNotExist(err) {
			continue
		} // A concurrently removed entry is not a readable result.
		if err != nil {
			return nil, err
		}
		items = append(items, info)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
	return items, nil
}
