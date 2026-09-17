package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestAttachmentUploadReservationTTLReclaimsStaleAndCountsActiveUploads(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-reservations", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	stale, err := store.InitAttachmentUpload(context.Background(), InitAttachmentUploadInput{
		UserID: "user-1", ProjectID: "project-reservations", Filename: "stale.bin",
		ContentType: "application/octet-stream", TotalSize: 11, ChunkSize: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	staleDir := filepath.Join(store.blobRoot, "uploads", stale.ID)
	now = now.Add(attachmentUploadReservationTTL + time.Minute)
	active, err := store.InitAttachmentUpload(context.Background(), InitAttachmentUploadInput{
		UserID: "user-1", ProjectID: "project-reservations", Filename: "active.bin",
		ContentType: "application/octet-stream", TotalSize: 13, ChunkSize: 13,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.getAttachmentUpload(context.Background(), "user-1", stale.ID); err != nil || found {
		t.Fatalf("stale reservation found=%t err=%v", found, err)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale reservation directory remains: %v", err)
	}
	reserved, err := store.pendingAttachmentUploadReservedBytes(
		context.Background(), now.Add(-attachmentUploadReservationTTL),
	)
	if err != nil || reserved != uint64(active.TotalSize*2) {
		t.Fatalf("active reserved bytes=%d err=%v", reserved, err)
	}
	refreshed, found, err := store.GetAttachmentUpload(context.Background(), "user-1", active.ID)
	if err != nil || !found || !refreshed.UpdatedAt.Equal(now) {
		t.Fatalf("active reservation refresh=%#v found=%t err=%v", refreshed, found, err)
	}
}

func TestWorkspaceRestartReconcilesOrphanAttachmentUploadChunks(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "workspace.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-orphan", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	upload, err := store.InitAttachmentUpload(context.Background(), InitAttachmentUploadInput{
		UserID: "user-1", ProjectID: "project-orphan", Filename: "orphan.bin",
		ContentType: "application/octet-stream", TotalSize: 4, ChunkSize: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveAttachmentChunk(context.Background(), "user-1", upload.ID, 0, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatal(err)
	}
	uploadDir := filepath.Join(store.blobRoot, "uploads", upload.ID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	legacy, err := sql.Open(sqliteDriver, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`DELETE FROM attachment_uploads WHERE id=?`, upload.ID); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(dbPath)
	if err != nil {
		t.Fatalf("workspace restart must recover orphan staging metadata: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	var chunks int
	if err := restarted.db.QueryRow(`SELECT COUNT(*) FROM attachment_upload_chunks WHERE upload_id=?`, upload.ID).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 {
		t.Fatalf("orphan chunk rows=%d", chunks)
	}
	if _, err := os.Stat(uploadDir); !os.IsNotExist(err) {
		t.Fatalf("orphan upload directory remains: %v", err)
	}
}

func TestAttachmentStorePersistsUserScopedMetadataAndContent(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "workspace.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	payload := []byte("real attachment content")
	attachment, err := store.CreateAttachment(context.Background(), CreateAttachmentInput{
		UserID: "user-1", ProjectID: "project-1", Filename: "evidence.txt",
		ContentType: "text/plain", Ephemeral: true,
	}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if attachment.ID == "" || attachment.Size != int64(len(payload)) || attachment.SHA256 == "" ||
		attachment.Filename != "evidence.txt" || !attachment.Ephemeral {
		t.Fatalf("attachment = %#v", attachment)
	}
	if filepath.Base(attachment.BlobPath) == attachment.Filename {
		t.Fatalf("blob path must not contain the untrusted filename: %q", attachment.BlobPath)
	}

	got, found, err := store.GetAttachment(context.Background(), "user-1", attachment.ID)
	if err != nil || !found || got.SHA256 != attachment.SHA256 {
		t.Fatalf("GetAttachment() = %#v, %v, %v", got, found, err)
	}
	if _, found, err := store.GetAttachment(context.Background(), "user-2", attachment.ID); err != nil || found {
		t.Fatalf("cross-user GetAttachment() found=%v err=%v", found, err)
	}
	opened, file, found, err := store.OpenAttachment(context.Background(), "user-1", attachment.ID)
	if err != nil || !found {
		t.Fatalf("OpenAttachment() = %#v, %v, %v", opened, found, err)
	}
	data, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("attachment content = %q, %v", data, err)
	}
	info, err := os.Stat(filepath.Join(store.blobRoot, filepath.FromSlash(attachment.BlobPath)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("blob mode = %o", info.Mode().Perm())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if _, found, err := restarted.GetAttachment(context.Background(), "user-1", attachment.ID); err != nil || !found {
		t.Fatalf("attachment did not survive restart: found=%v err=%v", found, err)
	}
}

func TestChunkedAttachmentUploadIsDurableIdempotentAndVerified(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	payload := []byte("abcdefghij")
	upload, err := store.InitAttachmentUpload(context.Background(), InitAttachmentUploadInput{
		UserID: "user-1", ProjectID: "project-1", Filename: "large.bin",
		ContentType: "application/octet-stream", TotalSize: int64(len(payload)), ChunkSize: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upload.ExpectedChunks != 3 || upload.ReceivedBytes != 0 {
		t.Fatalf("upload = %#v", upload)
	}
	for index, chunk := range [][]byte{payload[:4], payload[4:8], payload[8:]} {
		status, err := store.SaveAttachmentChunk(context.Background(), "user-1", upload.ID, index, bytes.NewReader(chunk))
		if err != nil {
			t.Fatalf("save chunk %d: %v", index, err)
		}
		if index == 0 {
			replayed, err := store.SaveAttachmentChunk(context.Background(), "user-1", upload.ID, index, bytes.NewReader(chunk))
			if err != nil || replayed.ReceivedBytes != status.ReceivedBytes {
				t.Fatalf("idempotent replay = %#v, %v", replayed, err)
			}
		}
	}
	status, found, err := store.GetAttachmentUpload(context.Background(), "user-1", upload.ID)
	if err != nil || !found {
		t.Fatalf("upload status = %#v, %v, %v", status, found, err)
	}
	if !reflect.DeepEqual(status.ReceivedChunks, []int{0, 1, 2}) || status.ReceivedBytes != int64(len(payload)) {
		t.Fatalf("upload status = %#v", status)
	}
	if _, found, err := store.GetAttachmentUpload(context.Background(), "user-2", upload.ID); err != nil || found {
		t.Fatalf("cross-user upload status found=%v err=%v", found, err)
	}
	digest := sha256.Sum256(payload)
	attachment, err := store.FinalizeAttachmentUpload(context.Background(), "user-1", upload.ID, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	_, file, found, err := store.OpenAttachment(context.Background(), "user-1", attachment.ID)
	if err != nil || !found {
		t.Fatalf("open finalized attachment: found=%v err=%v", found, err)
	}
	data, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("finalized content = %q, %v", data, err)
	}
	if _, found, err := store.GetAttachmentUpload(context.Background(), "user-1", upload.ID); err != nil || found {
		t.Fatalf("finalized upload still exists: found=%v err=%v", found, err)
	}
}

func TestConcurrentArtifactUploadFinalizeRechecksDurableResultAfterLock(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	payload := []byte("concurrent onboarding upload")
	upload, err := store.InitAttachmentUpload(context.Background(), InitAttachmentUploadInput{
		UserID: "user-1", ProjectID: "project-1", Filename: "cohort.csv",
		ContentType: "text/csv", TotalSize: int64(len(payload)), ChunkSize: int64(len(payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveAttachmentChunk(context.Background(), "user-1", upload.ID, 0, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	ctx := withArtifactUploadFinalizeBarrier(context.Background(), func() {
		ready <- struct{}{}
		<-release
	})
	ctx = WithMutationIdempotencyKey(ctx, "onboarding-concurrent-finalize")
	type finalizeResult struct {
		artifact Artifact
		version  ArtifactVersion
		err      error
	}
	results := make(chan finalizeResult, 2)
	var finalizers sync.WaitGroup
	for range 2 {
		finalizers.Add(1)
		go func() {
			defer finalizers.Done()
			artifact, version, _, err := store.FinalizeArtifactUpload(ctx, "user-1", upload.ID, "")
			results <- finalizeResult{artifact: artifact, version: version, err: err}
		}()
	}
	<-ready
	<-ready
	close(release)
	finalizers.Wait()
	close(results)

	var expected finalizeResult
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent finalize: %v", result.err)
		}
		if expected.artifact.ID == "" {
			expected = result
			continue
		}
		if result.artifact.ID != expected.artifact.ID || result.version.ID != expected.version.ID {
			t.Fatalf("concurrent finalize mismatch: %#v != %#v", result, expected)
		}
	}
	if expected.artifact.ID == "" || expected.version.ID == "" {
		t.Fatalf("concurrent finalize result = %#v", expected)
	}
}

func TestChunkedAttachmentUploadRejectsCorruptionAndCancelCleansFiles(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	upload, err := store.InitAttachmentUpload(context.Background(), InitAttachmentUploadInput{
		UserID: "user-1", ProjectID: "project-1", Filename: "cancel.bin",
		ContentType: "application/octet-stream", TotalSize: 4, ChunkSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveAttachmentChunk(context.Background(), "user-1", upload.ID, 0, bytes.NewReader([]byte("ab"))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveAttachmentChunk(context.Background(), "user-1", upload.ID, 0, bytes.NewReader([]byte("zz"))); err == nil {
		t.Fatal("conflicting chunk replay should fail")
	}
	if _, err := store.FinalizeAttachmentUpload(context.Background(), "user-1", upload.ID, "deadbeef"); err == nil {
		t.Fatal("incomplete upload should not finalize")
	}
	uploadDir := filepath.Join(store.blobRoot, "uploads", upload.ID)
	if err := store.CancelAttachmentUpload(context.Background(), "user-2", upload.ID); err == nil {
		t.Fatal("cross-user cancel should fail")
	}
	if err := store.CancelAttachmentUpload(context.Background(), "user-1", upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(uploadDir); !os.IsNotExist(err) {
		t.Fatalf("upload directory remains after cancel: %v", err)
	}
}
