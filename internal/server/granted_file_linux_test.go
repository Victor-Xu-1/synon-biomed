//go:build linux

package server

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenGrantedRegularFileLinuxResistsSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	granted := filepath.Join(root, "granted")
	if err := os.MkdirAll(granted, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(granted, "inside.txt")
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(granted, "link.txt")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	grants := []hostGrant{{Path: granted, Mode: "read"}}
	var wait sync.WaitGroup
	wait.Add(1)
	stop := make(chan struct{})
	go func() {
		defer wait.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(link)
			_ = os.Symlink(outside, link)
			_ = os.Remove(link)
			_ = os.Symlink(inside, link)
		}
	}()
	for attempt := 0; attempt < 500; attempt++ {
		file, err := openGrantedRegularFile(link, grants)
		if err != nil {
			continue
		}
		content, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			close(stop)
			wait.Wait()
			t.Fatal(readErr)
		}
		if string(content) != "inside" {
			close(stop)
			wait.Wait()
			t.Fatalf("opened content outside grant: %q", content)
		}
	}
	close(stop)
	wait.Wait()
}
