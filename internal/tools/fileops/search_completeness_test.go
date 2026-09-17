package fileops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSearchReportsWhenTheResultLimitTruncatesMatches(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "evidence.txt")
	if err := os.WriteFile(path, []byte("target one\ntarget two\ntarget three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Search(root, ".", "target", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 || !result.Truncated || result.AppliedLimit != 2 {
		t.Fatalf("bounded search result=%#v", result)
	}
	complete, err := Search(root, ".", "target", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(complete.Matches) != 3 || complete.Truncated || complete.AppliedLimit != 10 {
		t.Fatalf("complete search result=%#v", complete)
	}
}
