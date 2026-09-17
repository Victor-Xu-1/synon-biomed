//go:build windows

package server

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenGrantedRegularFileWindowsVerifiesFinalHandlePath(t *testing.T) {
	root := t.TempDir()
	granted := filepath.Join(root, "granted")
	if err := os.MkdirAll(granted, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(granted, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openGrantedRegularFile(inside, []hostGrant{{Path: granted, Mode: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || string(content) != "inside" {
		t.Fatalf("content = %q, %v", content, err)
	}

	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(granted, "escape.txt")
	if err := os.Symlink(outside, link); err == nil {
		if escaped, err := openGrantedRegularFile(link, []hostGrant{{Path: granted, Mode: "read"}}); err == nil {
			_ = escaped.Close()
			t.Fatal("opened reparse-point target outside grant")
		}
	}
}
