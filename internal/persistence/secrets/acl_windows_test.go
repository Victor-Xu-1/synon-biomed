//go:build windows

package secrets

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStoreProtectsWindowsVaultACLs(t *testing.T) {
	root := t.TempDir()
	if _, err := New(root).Create(Secret{ID: "acl", Provider: "windows"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", lockFileName, "master.key", "vault.enc"} {
		path := filepath.Join(root, "secrets", name)
		descriptor, err := windows.GetNamedSecurityInfo(
			path,
			windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION,
		)
		if err != nil {
			t.Fatalf("read DACL for %s: %v", path, err)
		}
		control, _, err := descriptor.Control()
		if err != nil {
			t.Fatalf("read security descriptor control for %s: %v", path, err)
		}
		if control&windows.SE_DACL_PROTECTED == 0 {
			t.Errorf("DACL is inherited for %s", path)
		}
	}
}
