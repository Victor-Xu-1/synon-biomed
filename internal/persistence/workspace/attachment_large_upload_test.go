package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

// acceptance_id: conversation-attachments-v1. The fixture exercises upload
// metadata without allocating a multi-gigabyte file.
func TestAttachmentUploadAboveEightGiB(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Capacity is enforced separately from format/geometry. Use a not-yet-created
	// blob root so this regression proves there is no fixed 8 GiB product cap
	// without depending on how much free disk a CI worker happens to have.
	store.blobRoot = filepath.Join(t.TempDir(), "capacity-unavailable")
	if _, err = store.CreateProject(CreateProjectInput{ID: "large-input", UserID: "owner", Name: "Large file input"}); err != nil {
		t.Fatal(err)
	}
	upload, err := store.InitAttachmentUpload(context.Background(), InitAttachmentUploadInput{UserID: "owner", ProjectID: "large-input", Filename: "measurement.unknown", TotalSize: 9 << 30, ChunkSize: 8 << 20})
	if err != nil {
		t.Fatalf("large input rejected: %v", err)
	}
	if upload.TotalSize != 9<<30 || upload.ExpectedChunks != 1152 {
		t.Fatalf("large file geometry: %#v", upload)
	}
	if err = store.CancelAttachmentUpload(context.Background(), "owner", upload.ID); err != nil {
		t.Fatal(err)
	}
}
