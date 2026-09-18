package fileops

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("content %q = %q, %v; want %q", path, got, err, want)
	}
}

func TestMutationsRejectExternalTargetSymlink(t *testing.T) {
	operations := map[string]func(string, string) error{
		"write": func(root, path string) error {
			_, err := Write(root, path, "changed", "utf8", true)
			return err
		},
		"original-write": func(root, path string) error {
			_, err := OriginalWrite(root, path, "changed")
			return err
		},
		"edit": func(root, path string) error {
			_, err := OriginalEdit(root, path, "original", "changed", false)
			return err
		},
		"copy": func(root, path string) error {
			writeFixture(t, filepath.Join(root, "source.txt"), "changed")
			_, err := Copy(root, "source.txt", path, true, false)
			return err
		},
	}
	for name, run := range operations {
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			sentinel := filepath.Join(outside, "sentinel.txt")
			writeFixture(t, sentinel, "original")
			testSymlink(t, sentinel, filepath.Join(root, "link.txt"))
			if err := run(root, "link.txt"); err == nil {
				t.Error("external target symlink was accepted")
			}
			assertContent(t, sentinel, "original")
		})
	}
}

func TestMutationsRejectExternalAncestorWithMissingDirectories(t *testing.T) {
	operations := map[string]func(string) error{
		"edit-create": func(root string) error {
			_, err := OriginalEdit(root, "escape/missing/new.txt", "", "changed", false)
			return err
		},
		"mkdir": func(root string) error {
			_, err := Mkdir(root, "escape/missing/nested", true)
			return err
		},
		"patch-create": func(root string) error {
			_, err := OriginalPatch(root, "--- /dev/null\n+++ b/escape/missing/new.txt\n@@ -0,0 +1 @@\n+changed\n", false)
			return err
		},
		"copy": func(root string) error {
			writeFixture(t, filepath.Join(root, "source.txt"), "changed")
			_, err := Copy(root, "source.txt", "escape/missing/new.txt", false, false)
			return err
		},
		"move": func(root string) error {
			writeFixture(t, filepath.Join(root, "source.txt"), "changed")
			_, err := Move(root, "source.txt", "escape/missing/new.txt", false)
			return err
		},
	}
	for name, run := range operations {
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			testSymlink(t, outside, filepath.Join(root, "escape"))
			if err := run(root); err == nil {
				t.Error("external ancestor symlink was accepted")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("external directory was modified: %v, %v", entries, err)
			}
		})
	}
}

func TestMutationPreservesInternalSymlinksAndAbsolutePaths(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(map[bool]string{false: "relative", true: "absolute"}[absolute], func(t *testing.T) {
			root := t.TempDir()
			target := "real.txt"
			if absolute {
				target = filepath.Join(root, target)
			}
			testSymlink(t, target, filepath.Join(root, "link.txt"))
			if _, err := OriginalWrite(root, filepath.Join(root, "link.txt"), "first"); err != nil {
				t.Fatal(err)
			}
			result, err := OriginalEdit(root, "link.txt", "first", "second", false)
			if err != nil || result.FilePath != "link.txt" {
				t.Fatalf("internal edit = %#v, %v", result, err)
			}
			assertContent(t, filepath.Join(root, "real.txt"), "second")
		})
	}
}
