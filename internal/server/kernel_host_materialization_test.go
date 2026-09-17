package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type kernelMaterializationReader struct{ *bytes.Reader }

func (kernelMaterializationReader) Close() error { return nil }

func TestKernelMaterializationRepairsOnlyItsDamagedCache(t *testing.T) {
	root := t.TempDir()
	source := []byte("complete immutable source\n末尾")
	digest := sha256.Sum256(source)
	materialize := func() (string, error) {
		return materializeKernelImmutableContent(context.Background(), root, t.TempDir(), "version-source", "source.json", int64(len(source)), hex.EncodeToString(digest[:]), kernelMaterializationReader{bytes.NewReader(source)})
	}
	path, err := materialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("damaged cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(root, "user-report.txt")
	if err := os.WriteFile(userPath, []byte("user work"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := materialize()
	if err != nil || restored != path {
		t.Fatalf("cache corruption became a permanent failure: %q %v", restored, err)
	}
	content, err := os.ReadFile(restored)
	if err != nil || !bytes.Equal(content, source) {
		t.Fatalf("restored source differs: %v", err)
	}
	user, err := os.ReadFile(userPath)
	if err != nil || string(user) != "user work" {
		t.Fatal("cache repair changed user work")
	}
}

func TestKernelMaterializationPreservesSourceAndRejectsUnsafeTargets(t *testing.T) {
	source := []byte("original data")
	digest := sha256.Sum256(source)
	for _, kind := range []string{"directory-symlink", "file-symlink", "file-hardlink", "source-mismatch", "source-overflow", "cancelled", "version-traversal"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			external := filepath.Join(outside, "external.txt")
			if err := os.WriteFile(external, source, 0o600); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(root, ".synon-artifacts")
			version, filename := "version-source", "source.json"
			target := filepath.Join(directory, version+"-"+filename)
			ctx := context.Background()
			content := source
			if kind == "directory-symlink" {
				if err := os.Symlink(outside, directory); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "file-symlink":
				if err := os.Symlink(external, target); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "file-hardlink":
				if err := os.Link(external, target); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
				}
			case "source-mismatch":
				content = []byte("modified data")
			case "source-overflow":
				content = append(append([]byte{}, source...), 'x')
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "version-traversal":
				version = "../../outside"
			}
			_, err := materializeKernelImmutableContent(ctx, root, t.TempDir(), version, filename, int64(len(source)), hex.EncodeToString(digest[:]), kernelMaterializationReader{bytes.NewReader(content)})
			if err == nil {
				t.Fatal("unsafe or incomplete source was accepted")
			}
			if kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			actual, err := os.ReadFile(external)
			if err != nil || !bytes.Equal(actual, source) {
				t.Fatal("materialization modified an external file")
			}
			if kind == "source-mismatch" || kind == "source-overflow" || kind == "cancelled" {
				files, err := os.ReadDir(directory)
				if err != nil || len(files) != 0 {
					t.Fatal("failed materialization left a published or partial cache")
				}
			}
		})
	}
}

func TestKernelMaterializationConcurrentReplayAndTaskIsolation(t *testing.T) {
	root := t.TempDir()
	source := bytes.Repeat([]byte("shared immutable source\n"), 10000)
	digest := sha256.Sum256(source)
	var wait sync.WaitGroup
	paths := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			path, err := materializeKernelImmutableContent(context.Background(), root, t.TempDir(), "shared-version", "source.json", int64(len(source)), hex.EncodeToString(digest[:]), kernelMaterializationReader{bytes.NewReader(source)})
			if err != nil {
				t.Error(err)
			}
			paths <- path
		}()
	}
	wait.Wait()
	close(paths)
	canonical := ""
	for path := range paths {
		if canonical == "" {
			canonical = path
		}
		if path != canonical {
			t.Fatal("concurrent reads produced different identities")
		}
	}
	otherRoot := t.TempDir()
	other, err := materializeKernelImmutableContent(context.Background(), otherRoot, t.TempDir(), "shared-version", "source.json", int64(len(source)), hex.EncodeToString(digest[:]), kernelMaterializationReader{bytes.NewReader(source)})
	if err != nil || other == canonical || !hostPathWithin(otherRoot, other) {
		t.Fatal("source cache leaked across task directories")
	}
}

type kernelCancellingContentReader struct {
	*bytes.Reader
	cancel context.CancelFunc
}

func (r kernelCancellingContentReader) Read(buffer []byte) (int, error) {
	count, err := r.Reader.Read(buffer)
	r.cancel()
	return count, err
}

func (kernelCancellingContentReader) Close() error { return nil }

func TestKernelMaterializationCancellationDuringCopyDoesNotPublish(t *testing.T) {
	root := t.TempDir()
	content := bytes.Repeat([]byte("source"), 100000)
	digest := sha256.Sum256(content)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := materializeKernelImmutableContent(ctx, root, t.TempDir(), "cancelled-source", "source.json", int64(len(content)), hex.EncodeToString(digest[:]), kernelCancellingContentReader{bytes.NewReader(content), cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-copy cancellation became %v", err)
	}
	files, err := os.ReadDir(filepath.Join(root, ".synon-artifacts"))
	if err != nil || len(files) != 0 {
		t.Fatal("cancelled copy left partial or published evidence")
	}
}
