package v11reuse

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type objectKind string

const (
	objectFile    objectKind = "file"
	objectSymlink objectKind = "symlink"
)

type sourceObject struct {
	Path          string
	Kind          objectKind
	Mode          fs.FileMode
	Size          int64
	ContentSHA256 string
	LinkTarget    string
}

func walkObjects(root string) ([]sourceObject, error) {
	objects := make([]sourceObject, 0)
	err := filepath.WalkDir(root, func(path string, directoryEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || directoryEntry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("make source path relative: %w", err)
		}
		relative = filepath.ToSlash(relative)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("stat baseline object %s: %w", relative, err)
		}

		switch {
		case info.Mode().IsRegular():
			contentHash, err := hashFile(path)
			if err != nil {
				return fmt.Errorf("hash baseline file %s: %w", relative, err)
			}
			objects = append(objects, sourceObject{
				Path:          relative,
				Kind:          objectFile,
				Mode:          info.Mode(),
				Size:          info.Size(),
				ContentSHA256: contentHash,
			})
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read baseline symlink %s: %w", relative, err)
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("resolve baseline symlink %s: %w", relative, err)
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return fmt.Errorf("resolve baseline symlink absolute path %s: %w", relative, err)
			}
			if !withinRoot(root, resolved) {
				return fmt.Errorf("%w: symlink %s resolves to %s", ErrPathEscape, relative, resolved)
			}
			objects = append(objects, sourceObject{
				Path:       relative,
				Kind:       objectSymlink,
				Mode:       info.Mode(),
				LinkTarget: filepath.ToSlash(target),
			})
		default:
			return fmt.Errorf("unsupported baseline object %s with mode %s", relative, info.Mode())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].Path < objects[j].Path
	})
	return objects, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func hashSourceObjects(objects []sourceObject) string {
	hashValue := sha256.New()
	for _, object := range objects {
		writeHashField(hashValue, object.Path)
		writeHashField(hashValue, string(object.Kind))
		writeHashField(hashValue, object.Mode.String())
		writeHashField(hashValue, fmt.Sprintf("%d", object.Size))
		writeHashField(hashValue, object.ContentSHA256)
		writeHashField(hashValue, object.LinkTarget)
	}
	return hex.EncodeToString(hashValue.Sum(nil))
}

func writeHashField(hashValue hash.Hash, value string) {
	_, _ = io.WriteString(hashValue, value)
	_, _ = hashValue.Write([]byte{0})
}
