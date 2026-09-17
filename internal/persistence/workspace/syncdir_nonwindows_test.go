//go:build !windows

package workspace

import (
	"path/filepath"
	"testing"
)

func TestSyncDirectoryNonWindowsFailsClosedForMissingDirectory(t *testing.T) {
	if err := syncDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing directory was accepted")
	}
}
