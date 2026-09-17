//go:build windows

package secrets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRejectsWindowsDirectoryJunction(t *testing.T) {
	container := t.TempDir()
	outside := t.TempDir()
	root := filepath.Join(container, "root-junction")
	output, err := exec.Command("cmd", "/c", "mklink", "/J", root, outside).CombinedOutput()
	if err != nil {
		t.Skipf("directory junctions unavailable: %v: %s", err, output)
	}
	t.Cleanup(func() {
		_ = os.Remove(root)
	})
	if _, err := New(root).List(); err == nil || !strings.Contains(err.Error(), "reparse point") {
		t.Fatalf("junction parent error = %v", err)
	}
}
