//go:build windows

package workspace

import "testing"

func TestSyncDirectoryUsesWindowsDurabilityBoundary(t *testing.T) {
	if err := syncDirectory(t.TempDir()); err != nil {
		t.Fatalf("Windows directory durability boundary failed: %v", err)
	}
}
