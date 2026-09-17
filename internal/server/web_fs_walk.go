package server

import (
	"encoding/base64"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func buildWebFSTree(root, directory string, count *int) ([]webFSNode, error) {
	items, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	nodes := make([]webFSNode, 0, len(items))
	for _, item := range items {
		if item.Type()&os.ModeSymlink != 0 {
			continue
		}
		*count = *count + 1
		if *count > maxWebFSTreeEntries {
			return nil, errWebFSTooMany
		}
		fullPath := filepath.Join(directory, item.Name())
		info, err := item.Info()
		if err != nil {
			return nil, err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		relative, err := filepath.Rel(root, fullPath)
		if err != nil {
			return nil, err
		}
		node := webFSNode{
			Name: item.Name(), FullPath: fullPath, RelativePath: filepath.ToSlash(relative),
			IsDir: info.IsDir(), IsFile: info.Mode().IsRegular(),
		}
		if info.IsDir() {
			node.Children, err = buildWebFSTree(root, fullPath, count)
			if err != nil {
				return nil, err
			}
		}
		nodes = append(nodes, node)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].IsDir != nodes[j].IsDir {
			return nodes[i].IsDir
		}
		return nodes[i].Name < nodes[j].Name
	})
	return nodes, nil
}

func listWebFSFiles(root, directory string) ([]webFSFlatFile, error) {
	files := make([]webFSFlatFile, 0)
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == directory || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if len(files) >= maxWebFSTreeEntries {
			return errWebFSTooMany
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, webFSFlatFile{
			Name: entry.Name(), FullPath: path, RelativePath: filepath.ToSlash(relative),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	return files, nil
}

func readWebFSFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errWebFSUnsupported
	}
	if info.Size() > limit {
		return nil, errWebFSTooLarge
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errWebFSTooLarge
	}
	return data, nil
}

func webFSImageMediaType(data []byte, extension, declared string) (string, error) {
	declaredType, _, _ := mime.ParseMediaType(declared)
	detected := http.DetectContentType(data)
	extensionType := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(extension)))
	if parsed, _, err := mime.ParseMediaType(extensionType); err == nil {
		extensionType = parsed
	}
	if strings.HasPrefix(detected, "image/") {
		return detected, nil
	}
	if extensionType == "image/svg+xml" && looksLikeWebFSSVG(data) {
		return extensionType, nil
	}
	if declaredType == "image/svg+xml" && looksLikeWebFSSVG(data) {
		return declaredType, nil
	}
	return "", errWebFSUnsupported
}

func looksLikeWebFSSVG(data []byte) bool {
	value := strings.ToLower(strings.TrimSpace(string(data)))
	if strings.HasPrefix(value, "<svg") {
		return true
	}
	return strings.HasPrefix(value, "<?xml") &&
		strings.Contains(value[:min(len(value), 4096)], "<svg")
}

func webFSDataURL(mediaType string, data []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func validWebFSBaseName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." ||
		len(value) > 255 || filepath.Base(value) != value {
		return false
	}
	if strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, char := range value {
		if char < 32 || char == 127 {
			return false
		}
	}
	return true
}
