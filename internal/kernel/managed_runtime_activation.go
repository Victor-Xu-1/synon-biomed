package kernel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const managedRuntimePointerSchemaVersion = 1

type managedRuntimePointer struct {
	SchemaVersion int    `json:"schemaVersion"`
	Generation    string `json:"generation"`
}

// activateManagedRuntimeGeneration publishes a tiny, content-addressed
// pointer rather than relying on directory replacement. This works on Unix,
// Windows, and macOS without requiring a privileged symlink; the platform
// implementation replaces the pointer atomically (or with the Windows
// replace-file primitive) while the immutable generation remains untouched.
func activateManagedRuntimeGeneration(active, generation string) error {
	active = filepath.Clean(strings.TrimSpace(active))
	generation = filepath.Clean(strings.TrimSpace(generation))
	if active == "" || generation == "" || !filepath.IsAbs(active) || !filepath.IsAbs(generation) {
		return errors.New("managed runtime activation paths must be absolute")
	}
	generationInfo, err := os.Stat(generation)
	if err != nil || !generationInfo.IsDir() {
		return errors.New("managed runtime generation is unavailable")
	}
	root := filepath.Dir(active)
	relative, err := filepath.Rel(root, generation)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return errors.New("managed runtime generation escapes its activation root")
	}
	pointer := managedRuntimePointer{SchemaVersion: managedRuntimePointerSchemaVersion, Generation: filepath.ToSlash(relative)}
	raw, err := json.Marshal(pointer)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(root, ".synon-runtime-pointer-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceManagedRuntimePointer(temporaryPath, active)
}

func resolveManagedRuntimeGeneration(active string) (string, error) {
	active = filepath.Clean(strings.TrimSpace(active))
	if active == "" || !filepath.IsAbs(active) {
		return "", errors.New("managed runtime activation path is invalid")
	}
	info, err := os.Lstat(active)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(active)
		if err != nil {
			return "", err
		}
		return verifyManagedRuntimeGenerationPath(filepath.Dir(active), resolved)
	}
	if info.IsDir() {
		resolved, err := filepath.EvalSymlinks(active)
		if err != nil {
			return "", err
		}
		return verifyManagedRuntimeGenerationPath(filepath.Dir(active), resolved)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("managed runtime activation pointer is invalid")
	}
	file, err := os.Open(active)
	if err != nil {
		return "", err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16*1024))
	decoder.DisallowUnknownFields()
	var pointer managedRuntimePointer
	if err := decoder.Decode(&pointer); err != nil {
		return "", errors.New("managed runtime activation pointer is invalid")
	}
	if pointer.SchemaVersion != managedRuntimePointerSchemaVersion || strings.TrimSpace(pointer.Generation) == "" || strings.Contains(pointer.Generation, "\\") || filepath.IsAbs(pointer.Generation) {
		return "", errors.New("managed runtime activation pointer is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("managed runtime activation pointer is invalid")
	}
	resolved := filepath.Join(filepath.Dir(active), filepath.FromSlash(pointer.Generation))
	return verifyManagedRuntimeGenerationPath(filepath.Dir(active), resolved)
}

func verifyManagedRuntimeGenerationPath(root, candidate string) (string, error) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(rootResolved, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("managed runtime activation points outside its root")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("managed runtime generation is unavailable")
	}
	return resolved, nil
}
