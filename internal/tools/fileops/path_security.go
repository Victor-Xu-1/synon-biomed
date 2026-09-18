package fileops

import (
	"errors"
	"fmt"
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

func ensureTargetNotRoot(rel string) error {
	if rel == "." {
		return errors.New("target path cannot be file root")
	}
	return nil
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
