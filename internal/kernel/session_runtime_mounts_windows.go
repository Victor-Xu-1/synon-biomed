//go:build windows

package kernel

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// The manager has resolved the selected interpreter and its runtime prefix
// before constructing these grants. No worker-provided argument or environment
// value is consulted to extend the AppContainer filesystem authority.
func platformSessionRuntimeMounts(executable, prefix, worker string) ([]WorkerMount, error) {
	if err := validateWindowsKernelPath(executable, true, true); err != nil {
		return nil, fmt.Errorf("Windows session executable: %w", err)
	}
	if err := validateWindowsKernelPath(worker, true, true); err != nil {
		return nil, fmt.Errorf("Windows session worker asset: %w", err)
	}
	root := prefix
	if root == "" {
		root = filepath.Dir(executable)
		if strings.EqualFold(filepath.Base(root), "Scripts") {
			root = filepath.Dir(root)
		}
	}
	if err := validateWindowsKernelPath(root, true, false); err != nil {
		return nil, fmt.Errorf("Windows session runtime root: %w", err)
	}
	if !windowsKernelPathContains(root, executable) {
		return nil, errors.New("Windows session executable is outside its verified runtime")
	}
	assetRoot := filepath.Dir(worker)
	if err := validateWindowsKernelPath(assetRoot, true, false); err != nil {
		return nil, fmt.Errorf("Windows session asset root: %w", err)
	}
	mounts := []WorkerMount{TrustedReadOnlyDirectoryMount(root)}
	if !strings.EqualFold(root, assetRoot) {
		mounts = append(mounts, TrustedReadOnlyDirectoryMount(assetRoot))
	}
	return mounts, nil
}
