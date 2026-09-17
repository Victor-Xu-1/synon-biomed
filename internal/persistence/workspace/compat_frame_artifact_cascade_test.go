package workspace

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestDeleteCompatibilityFrameTreeDeletesUnfiledRootArtifactsOnly(t *testing.T) {
	store, _, _ := newKernelLocalOperationFixture(t)
	frame, found, err := store.GetCompatibilityFrame("frame")
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	otherFrame, err := store.CreateFrame(CreateFrameInput{
		ID: "other-frame", ProjectID: frame.ProjectID, AgentName: "planner",
		Status: FrameStatusCompleted, ConversationType: "task",
	})
	if err != nil {
		t.Fatal(err)
	}

	deletedArtifact, deletedVersion, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "unfiled-root-artifact", ProjectID: frame.ProjectID, Name: "delete.txt",
		ContentType: "text/plain", Content: strings.NewReader("delete with owning task"), MaxBytes: 1024,
		RootFrameID: frame.RootFrameID, FrameID: frame.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	retainedArtifact, retainedVersion, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "other-root-artifact", ProjectID: frame.ProjectID, Name: "keep.txt",
		ContentType: "text/plain", Content: strings.NewReader("keep with other task"), MaxBytes: 1024,
		RootFrameID: otherFrame.RootFrameID, FrameID: otherFrame.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	deletedBlob, err := store.blobAbsolute(deletedVersion.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	retainedBlob, err := store.blobAbsolute(retainedVersion.StoragePath)
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := store.DeleteCompatibilityFrameTree(frame.ID, "owner", frame.IncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.FramesDeleted != 1 || deleted.ArtifactsDeleted != 1 || deleted.ArtifactCleanupFailures != 0 {
		t.Fatalf("deleted=%#v", deleted)
	}
	if _, found, err := store.GetArtifact(deletedArtifact.ID); err != nil || found {
		t.Fatalf("deleted artifact found=%t err=%v", found, err)
	}
	if _, found, err := store.GetArtifact(retainedArtifact.ID); err != nil || !found {
		t.Fatalf("retained artifact found=%t err=%v", found, err)
	}
	if _, err := os.Stat(deletedBlob); !os.IsNotExist(err) {
		t.Fatalf("deleted artifact blob stat err=%v", err)
	}
	if _, err := os.Stat(retainedBlob); err != nil {
		t.Fatalf("retained artifact blob stat err=%v", err)
	}
}
