package compute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListLocalDirectoryMatchesV11ShapeAndOrdering(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	listing, err := ListLocalDirectory(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if listing.ResolvedPath != root || listing.Roots["home"] != root || listing.Truncated || len(listing.Entries) != 2 ||
		!listing.Entries[0].IsDirectory || listing.Entries[0].Name != "folder" || listing.Entries[1].Name != "file.txt" || listing.Entries[1].Size != 5 {
		t.Fatalf("listing = %#v", listing)
	}
}

func TestListLocalDirectoryRejectsRelativeAndControlPaths(t *testing.T) {
	for _, path := range []string{"relative", "/tmp/bad\npath"} {
		if _, err := ListLocalDirectory(path, t.TempDir()); err == nil {
			t.Fatalf("path %q unexpectedly succeeded", path)
		}
	}
}
