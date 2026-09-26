package server

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWorkspaceFilePublicationCapacityCancellationAndEmptyContent(t *testing.T) {
	for _, scenario := range []string{"limit", "cancel", "empty", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			path := t.TempDir()
			root, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			content, limit := "content", int64(7)
			var source io.Reader = strings.NewReader(content)
			switch scenario {
			case "limit":
				limit = 6
			case "cancel":
				source = &cancelPublicationReader{reader: source, cancel: cancel}
			case "empty":
				content, limit, source = "", 0, strings.NewReader("")
			}
			stage, err := stageWorkspaceFile(ctx, root, source, limit)
			if scenario == "limit" || scenario == "cancel" {
				want := errWorkspaceFileTooLarge
				if scenario == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("error=%v, want %v", err, want)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := stage.publish(ctx, "result.txt"); err != nil {
					t.Fatal(err)
				}
				if err := stage.publish(ctx, "result.txt"); err != nil {
					t.Fatalf("idempotent publication: %v", err)
				}
				if err := stage.close(); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(path, "result.txt"))
				if err != nil || string(got) != content {
					t.Fatalf("content=%q err=%v", got, err)
				}
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".synon-") {
					t.Fatalf("temporary file leaked: %s", entry.Name())
				}
			}
		})
	}
}

type cancelPublicationReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (reader *cancelPublicationReader) Read(buffer []byte) (int, error) {
	n, err := reader.reader.Read(buffer)
	reader.cancel()
	return n, err
}

func TestWorkspaceFilePublicationConcurrentWritersDoNotOverwrite(t *testing.T) {
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	start := make(chan struct{})
	type outcome struct {
		content string
		err     error
	}
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	for _, content := range []string{"first", "second"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			stage, err := stageWorkspaceFile(context.Background(), root, strings.NewReader(content), 10)
			if err != nil {
				results <- outcome{content, err}
				return
			}
			<-start
			err = stage.publish(context.Background(), "result.txt")
			err = errors.Join(err, stage.close())
			results <- outcome{content, err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	winner := ""
	for result := range results {
		if result.err == nil {
			if winner != "" {
				t.Fatal("both different writers succeeded")
			}
			winner = result.content
		} else if !errors.Is(result.err, errWorkspaceFileConflict) {
			t.Fatalf("unexpected publication failure: %v", result.err)
		}
	}
	got, err := os.ReadFile(filepath.Join(path, "result.txt"))
	if winner == "" || err != nil || string(got) != winner {
		t.Fatalf("winner=%q bytes=%q err=%v", winner, got, err)
	}
}

func TestAuthorizedFileAndDirectoryReadsRejectPostValidationSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "selected")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "evidence.txt"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "evidence.txt"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveWebFSTarget(root, filepath.Join(target, "evidence.txt"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if file, err := openWebFSRegularFile(webFSAccess{Root: root, Target: resolved}); err == nil {
		file.Close()
		t.Error("post-validation file escape accepted")
	}
	if _, err := readAuthorizedHostDirectory(root, target); err == nil {
		t.Error("post-validation directory escape accepted")
	}
}
