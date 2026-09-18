package fileops

import (
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/tools/fileevents"
)

func TestMutationHandleRejectsSymlinkSwapAfterResolution(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "leaf", true: "parent"}[parent], func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(root, "nested", "file.txt"), "inside")
			writeFixture(t, filepath.Join(outside, "file.txt"), "outside")
			fs, err := openMutationFS(root)
			if err != nil {
				t.Fatal(err)
			}
			defer fs.Close()
			target, _, err := fs.resolve("nested/file.txt")
			if err != nil {
				t.Fatal(err)
			}
			if parent {
				if err := os.Rename(filepath.Join(root, "nested"), filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				testSymlink(t, outside, filepath.Join(root, "nested"))
			} else {
				if err := os.Remove(filepath.Join(root, "nested", "file.txt")); err != nil {
					t.Fatal(err)
				}
				testSymlink(t, filepath.Join(outside, "file.txt"), filepath.Join(root, "nested", "file.txt"))
			}
			if err := fs.WriteFile(target, []byte("changed"), 0o600); err == nil {
				t.Fatal("write accepted a post-resolution external symlink")
			}
			if _, err := fs.ReadFile(target); err == nil {
				t.Fatal("read accepted a post-resolution external symlink")
			}
			assertContent(t, filepath.Join(outside, "file.txt"), "outside")
		})
	}
}

func TestMutationRejectsRootAliasesAndSelfTransfers(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "keep.txt"), "keep")
	testSymlink(t, ".", filepath.Join(root, "alias"))
	if _, err := Delete(root, "alias", true); err == nil {
		t.Error("delete accepted an alias of the file root")
	}
	if _, err := Move(root, "alias", "moved", true); err == nil {
		t.Error("move accepted an alias of the file root")
	}
	if _, err := Copy(root, "keep.txt", "alias", true, false); err == nil {
		t.Error("copy accepted the file root as target")
	}
	testSymlink(t, "keep.txt", filepath.Join(root, "same.txt"))
	if _, err := Copy(root, "keep.txt", "same.txt", true, false); err == nil {
		t.Error("copy accepted a source alias as target")
	}
	if err := os.Link(filepath.Join(root, "keep.txt"), filepath.Join(root, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(root, "keep.txt", "hardlink.txt", true, false); err == nil {
		t.Error("copy accepted the same inode as target")
	}
	assertContent(t, filepath.Join(root, "keep.txt"), "keep")
}

func TestMutationDirectoryTransfersPreserveFiles(t *testing.T) {
	root := t.TempDir()
	if _, err := Mkdir(root, "source/nested", true); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "source", "nested", "data.txt"), "payload")
	if _, err := Copy(root, "source", "copy", false, true); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(root, "copy", "nested", "data.txt"), "payload")
	if _, err := Copy(root, "source", "copy", false, true); err == nil {
		t.Fatal("non-overwrite copy replaced existing directory")
	}
	if _, err := Copy(root, "source", "copy", true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(root, "copy", "moved", false); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(root, "moved", "nested", "data.txt"), "payload")
	if _, err := Delete(root, "moved", false); err == nil {
		t.Fatal("non-recursive delete removed directory")
	}
	if _, err := Delete(root, "moved", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "moved")); !os.IsNotExist(err) {
		t.Fatalf("moved directory remains: %v", err)
	}
	for name, transfer := range map[string]func() error{
		"copy-to-ancestor": func() error {
			_, err := Copy(root, "source/nested", "source", true, true)
			return err
		},
		"move-to-ancestor": func() error {
			_, err := Move(root, "source/nested", "source", true)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := transfer(); err == nil {
				t.Fatal("transfer accepted destructive ancestor overlap")
			}
			assertContent(t, filepath.Join(root, "source", "nested", "data.txt"), "payload")
		})
	}
}

func TestMutationPatchAndReplacePreserveContracts(t *testing.T) {
	root := t.TempDir()
	patch := "--- /dev/null\n+++ b/nested/data.txt\n@@ -0,0 +1 @@\n+one\n"
	if result, err := OriginalPatch(root, patch, true); err != nil || !result.DryRun || result.Applied {
		t.Fatalf("dry run: %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "nested")); !os.IsNotExist(err) {
		t.Fatalf("dry run created a directory: %v", err)
	}
	if _, err := OriginalPatch(root, patch, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Replace(root, "nested/data.txt", "one", "two"); err != nil {
		t.Fatal(err)
	}
	if _, err := Patch(root, "nested/data.txt", []PatchOperation{{Type: "append", Content: "three\n"}}); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(root, "nested", "data.txt"), "two\nthree\n")
	writeFixture(t, filepath.Join(root, "data.json"), `{"value":1}`)
	if _, err := JSONPatch(root, "data.json", []JSONPatchOperation{{Op: "replace", Path: "/value", Value: 2}}); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(root, "data.json"), "{\n  \"value\": 2\n}\n")
	if _, err := OriginalPatch(root, "--- a/nested/data.txt\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-two\n-three\n", false); err != nil {
		t.Fatal(err)
	}
}

func TestMutationRejectsTraversalAndSymlinkLoops(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"../outside", filepath.Join(t.TempDir(), "outside")} {
		if _, err := OriginalEdit(root, path, "", "changed", false); err == nil {
			t.Errorf("traversal accepted: %s", path)
		}
	}
	testSymlink(t, "cycle", filepath.Join(root, "cycle"))
	if _, err := Write(root, "cycle", "changed", "utf8", true); err == nil {
		t.Fatal("symlink cycle accepted")
	}
}

func TestMutationPreservesNoOverwriteAndChangeEvents(t *testing.T) {
	root := t.TempDir()
	testSymlink(t, "real.txt", filepath.Join(root, "link.txt"))
	if _, err := Write(root, "link.txt", "unexpected", "utf8", false); err == nil {
		t.Fatal("non-overwrite write accepted an existing dangling link")
	}
	if _, err := os.Stat(filepath.Join(root, "real.txt")); !os.IsNotExist(err) {
		t.Fatalf("non-overwrite write created the link target: %v", err)
	}
	changes := make(chan string, 8)
	fileevents.RegisterChangeHandler(func(path string) {
		if filepath.Dir(path) == root {
			select {
			case changes <- path:
			default:
			}
		}
	})
	if _, err := Write(root, "link.txt", "created", "utf8", true); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{filepath.Join(root, "real.txt"): false, filepath.Join(root, "link.txt"): false}
	for len(changes) > 0 {
		want[<-changes] = true
	}
	for path, notified := range want {
		if !notified {
			t.Errorf("no change notification for %s", path)
		}
	}
}
