package compute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenLocalDownloadConfinesRealPathAndOpensRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.txt")
	if err := os.WriteFile(path, []byte("download"), 0o600); err != nil {
		t.Fatal(err)
	}
	download, err := OpenLocalDownload(path, "workspaces", root)
	if err != nil {
		t.Fatal(err)
	}
	defer download.File.Close()
	if download.Filename != "result.txt" || download.Size != 8 || download.MTime <= 0 {
		t.Fatalf("download = %#v", download)
	}
}

func TestOpenLocalDownloadRejectsOutsideEmptyAndDirectory(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(root, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, empty, root} {
		if download, err := OpenLocalDownload(path, "workspaces", root); err == nil {
			download.File.Close()
			t.Fatalf("path %q unexpectedly succeeded", path)
		}
	}
}
