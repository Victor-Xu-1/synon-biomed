package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAttachmentStoreRejectsSymlinkRootAndBlob(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	dbPath := filepath.Join(root, "workspace.db")
	if err := os.Symlink(outside, dbPath+".blobs"); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(dbPath); err == nil {
		_ = store.Close()
		t.Fatal("Open accepted a symbolic-link blob root")
	}
	if err := os.Remove(dbPath + ".blobs"); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.CreateAttachment(context.Background(), CreateAttachmentInput{
		UserID: "user-1", ProjectID: "project-1", Filename: "safe.txt",
	}, bytes.NewReader([]byte("safe")))
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(store.blobRoot, filepath.FromSlash(attachment.BlobPath))
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "outside")
	if err := os.WriteFile(outsideFile, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, blob); err != nil {
		t.Fatal(err)
	}
	if _, file, found, err := store.OpenAttachment(context.Background(), "user-1", attachment.ID); err == nil || found || file != nil {
		t.Fatalf("OpenAttachment accepted symlink: found=%v file=%v err=%v", found, file, err)
	}
}

func TestAttachmentStoreRejectsTraversalOversizeAndWrongChecksum(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAttachment(context.Background(), CreateAttachmentInput{
		UserID: "user-1", ProjectID: "project-1", Filename: "../escape.txt",
	}, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("path traversal filename was accepted")
	}
	upload, err := store.InitAttachmentUpload(context.Background(), InitAttachmentUploadInput{
		UserID: "user-1", ProjectID: "project-1", Filename: "checked.bin",
		ContentType: "application/octet-stream", TotalSize: 4, ChunkSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveAttachmentChunk(context.Background(), "user-1", upload.ID, 0, bytes.NewReader([]byte("abc"))); err == nil {
		t.Fatal("oversized chunk was accepted")
	}
	for index, chunk := range [][]byte{[]byte("ab"), []byte("cd")} {
		if _, err := store.SaveAttachmentChunk(context.Background(), "user-1", upload.ID, index, bytes.NewReader(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinalizeAttachmentUpload(context.Background(), "user-1", upload.ID,
		"0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("wrong final checksum was accepted")
	}
	if status, found, err := store.GetAttachmentUpload(context.Background(), "user-1", upload.ID); err != nil || !found ||
		status.ReceivedBytes != 4 {
		t.Fatalf("wrong checksum destroyed resumable upload: %#v found=%v err=%v", status, found, err)
	}
}
