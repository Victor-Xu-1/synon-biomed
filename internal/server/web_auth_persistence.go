package server

import (
	"fmt"
	"os"
	"path/filepath"
)

func replacePrivateJSONFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".web-auth-*.tmp")
	if err != nil {
		return fmt.Errorf("create Web auth temporary file: %w", err)
	}
	tempPath := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure Web auth temporary file: %w", err)
	}
	if _, err := temp.Write(raw); err != nil {
		return fmt.Errorf("write Web auth state: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync Web auth state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close Web auth state: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace Web auth state: %w", err)
	}
	keep = true
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure Web auth state: %w", err)
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
