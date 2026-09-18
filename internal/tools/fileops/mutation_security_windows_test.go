package fileops

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Directory junctions do not require the Windows symbolic-link privilege.
func TestMutationsRejectExternalWindowsJunction(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "escape")
	command := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
	writeFixture(t, filepath.Join(outside, "sentinel.txt"), "outside")
	if _, err := Write(root, "escape/sentinel.txt", "changed", "utf8", true); err == nil {
		t.Error("write accepted an external junction")
	}
	if _, err := Mkdir(root, "escape/missing/nested", true); err == nil {
		t.Error("mkdir accepted an external junction")
	}
	assertContent(t, filepath.Join(outside, "sentinel.txt"), "outside")
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 {
		t.Fatalf("external directory changed: %v, %v", entries, err)
	}
}
