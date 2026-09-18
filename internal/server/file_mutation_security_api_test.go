package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestToolsAPIFileMutationsRejectSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	sentinel := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"link.txt": sentinel, "escape": outside} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink privilege unavailable: %v", err)
			}
			t.Fatal(err)
		}
	}
	srv := New(Options{FileRoot: root})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	for _, test := range []struct {
		tool  string
		input map[string]any
	}{
		{"file_write", map[string]any{"path": "link.txt", "content": "changed"}},
		{"file_mkdir", map[string]any{"path": "escape/missing/new", "recursive": true}},
	} {
		t.Run(test.tool, func(t *testing.T) {
			response := postToolInputStatus(t, httpServer.URL, test.tool, test.input)
			if response.status != http.StatusBadRequest {
				t.Fatalf("unsafe mutation status/body: %d %#v", response.status, response.body)
			}
		})
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "outside" {
		t.Fatalf("external content changed: %q, %v", content, err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 {
		t.Fatalf("external directory changed: %v, %v", entries, err)
	}
	response := postToolInputStatus(t, httpServer.URL, "file_write", map[string]any{
		"path": "allowed.txt", "content": "inside",
	})
	if response.status != http.StatusOK {
		t.Fatalf("normal write failed: %d %#v", response.status, response.body)
	}
	if content, err := os.ReadFile(filepath.Join(root, "allowed.txt")); err != nil || string(content) != "inside" {
		t.Fatalf("normal write content: %q, %v", content, err)
	}
}
