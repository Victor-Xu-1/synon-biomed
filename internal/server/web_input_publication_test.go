package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWebInputPublicationRejectsOversizeAndPreservesExistingFiles(t *testing.T) {
	for _, scenario := range []string{"oversize", "symlink", "digest_collision"} {
		t.Run(scenario, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "evidence.txt")
			payload := []byte("new evidence")
			if err := os.WriteFile(source, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			protected := filepath.Join(root, "evidence.txt")
			want := "original evidence"
			switch scenario {
			case "oversize":
				if err := os.Truncate(source, maxWebConversationInputFileBytes+1); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := filepath.Join(t.TempDir(), "private.txt")
				if err := os.WriteFile(outside, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, protected); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "digest_collision":
				if err := os.WriteFile(protected, []byte(want), 0o600); err != nil {
					t.Fatal(err)
				}
				sum := fmt.Sprintf("%x", sha256.Sum256(payload))
				protected = filepath.Join(root, "evidence-"+sum[:12]+".txt")
				if err := os.WriteFile(protected, []byte(want), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			file, err := os.Open(source)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			directory, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			if _, _, err := copyWebConversationInputFile(context.Background(), file, filepath.Base(source), directory, maxWebConversationInputFileBytes); err == nil {
				t.Error("unsafe publication succeeded")
			}
			if scenario == "digest_collision" {
				got, err := os.ReadFile(protected)
				if err != nil || string(got) != want {
					t.Fatalf("existing bytes overwritten: %q, %v", got, err)
				}
			}
		})
	}
}

func TestWebInputPublicationRejectsEscapedTaskInputDirectory(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "project-input-escape", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-input-escape", ProjectID: project.ID, AgentName: "OPERON", Status: "completed", ConversationType: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	compat, found, err := store.GetCompatibilityFrame(frame.ID)
	if err != nil || !found {
		t.Fatalf("frame: %v, %v", found, err)
	}
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), frame.ID)
	if err != nil || !found {
		t.Fatalf("access: %v, %v", found, err)
	}
	taskRoot, err := app.defaultAgentKernelTaskWorkspace(access.Frame.ProjectID, access.Frame.RootFrameID, access.RootFrameIncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(taskRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(taskRoot, "inputs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.materializeWebConversationInputFiles(context.Background(), compat, []string{source}); err == nil {
		t.Error("escaped task input root accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed: %v, %v", entries, err)
	}
}

func TestWebUploadPublicationRejectsEscapedContentDirectory(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "uploads")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := storeWebFSUpload(context.Background(), root, "evidence.txt", strings.NewReader("evidence")); err == nil {
		t.Error("escaped upload root accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed: %v, %v", entries, err)
	}
}
