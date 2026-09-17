package fileops

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ensureNotSamePath(source string, target string) error {
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if sourceAbs == targetAbs {
		return errors.New("source and target paths must be different")
	}
	return nil
}

func ensureTargetNotInsideSource(source string, target string) error {
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(sourceAbs, targetAbs)
	if err != nil {
		return err
	}
	if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
		return errors.New("target path cannot be inside source directory")
	}
	return nil
}

func ensureTargetCanBeWritten(target string, overwrite bool) error {
	if overwrite {
		return nil
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("target path already exists: %s", filepath.ToSlash(target))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func ensureTargetNotRoot(rel string) error {
	if rel == "." {
		return errors.New("target path cannot be file root")
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func copyFile(source string, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func copyDirectory(source string, target string, overwrite bool) error {
	if overwrite {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(target, 0o700)
		}
		targetPath := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return err
		}
		return copyFile(path, targetPath)
	})
}

func resolveExisting(root string, requestedPath string) (string, string, error) {
	target, rel, err := resolvePath(root, requestedPath)
	if err != nil {
		return "", "", err
	}
	evaluated, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if err := ensureInside(rootAbs, evaluated); err != nil {
		return "", "", err
	}
	return evaluated, rel, nil
}

func resolveForWrite(root string, requestedPath string) (string, string, error) {
	target, rel, err := resolvePath(root, requestedPath)
	if err != nil {
		return "", "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	parent := filepath.Dir(target)
	if _, err := os.Stat(parent); err == nil {
		evaluatedParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return "", "", err
		}
		if err := ensureInside(rootAbs, evaluatedParent); err != nil {
			return "", "", err
		}
	}
	return target, rel, nil
}

func resolvePath(root string, requestedPath string) (string, string, error) {
	if root == "" {
		return "", "", errors.New("file tool root is not configured")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if requestedPath == "" {
		requestedPath = "."
	}
	var target string
	if filepath.IsAbs(requestedPath) {
		target, err = filepath.Abs(requestedPath)
	} else {
		target, err = filepath.Abs(filepath.Join(rootAbs, requestedPath))
	}
	if err != nil {
		return "", "", err
	}
	if err := ensureInside(rootAbs, target); err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil {
		return "", "", err
	}
	if rel == "" {
		rel = "."
	}
	return target, rel, nil
}

func ensureInside(rootAbs string, targetAbs string) error {
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes file root: %s", filepath.ToSlash(rel))
	}
	return nil
}
