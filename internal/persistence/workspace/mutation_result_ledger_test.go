package workspace

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMutationIdempotencyKeyRejectsOversizeControlAndInvisibleUnicode(t *testing.T) {
	invalid := []string{strings.Repeat("a", maxMutationIdempotencyKeyBytes+1), "control\x1fkey", "space key", "invisible\u200bkey"}
	for index, key := range invalid {
		store := openMutationLedgerStore(t)
		ctx := WithMutationIdempotencyKey(context.Background(), key)
		_, _, err := store.SaveArtifactVersionRealtime(ctx, SaveArtifactVersionInput{ArtifactID: "invalid-key-artifact",
			ProjectID: "project-a", Name: "a", Kind: "text/plain", Content: []byte("a")}, "owner-a")
		if !errors.Is(err, ErrInvalidMutationIdempotencyKey) {
			t.Fatalf("invalid key %d error=%v", index, err)
		}
		if _, found, err := store.GetArtifact("invalid-key-artifact"); err != nil || found {
			t.Fatalf("invalid key %d wrote artifact found=%v err=%v", index, found, err)
		}
		if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 0 {
			t.Fatalf("invalid key %d outbox count=%d err=%v", index, count, err)
		}
		var ledger int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM mutation_result_ledger`).Scan(&ledger); err != nil || ledger != 0 {
			t.Fatalf("invalid key %d ledger count=%d err=%v", index, ledger, err)
		}
	}
	valid := strings.Repeat("z", maxMutationIdempotencyKeyBytes)
	store := openMutationLedgerStore(t)
	if _, _, err := store.SaveArtifactVersionRealtime(WithMutationIdempotencyKey(context.Background(), valid),
		SaveArtifactVersionInput{ArtifactID: "max-key-artifact", ProjectID: "project-a", Name: "a", Kind: "text/plain", Content: []byte("a")}, "owner-a"); err != nil {
		t.Fatalf("maximum-length ASCII token rejected: %v", err)
	}
}

func TestMutationResultLedgerNamespacesSameKeyAcrossOwners(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, owner := range []string{"owner-a", "owner-b"} {
		projectID, artifactID := "project-"+owner, "artifact-"+owner
		if _, err := store.CreateProject(CreateProjectInput{ID: projectID, UserID: owner, Name: owner}); err != nil {
			t.Fatal(err)
		}
		ctx := WithMutationIdempotencyKey(context.Background(), "shared-owner-key")
		if _, _, err := store.SaveArtifactVersionRealtime(ctx, SaveArtifactVersionInput{ArtifactID: artifactID,
			ProjectID: projectID, Name: "a", Kind: "text/plain", Content: []byte(owner)}, owner); err != nil {
			t.Fatal(err)
		}
	}
	var owners, rows int
	if err := store.db.QueryRow(`SELECT COUNT(DISTINCT owner_user_id), COUNT(*) FROM mutation_result_ledger
		WHERE idempotency_key = 'shared-owner-key'`).Scan(&owners, &rows); err != nil || owners != 2 || rows != 2 {
		t.Fatalf("ledger owners=%d rows=%d err=%v", owners, rows, err)
	}
}

func TestBlobArtifactVersionAndCopyReplayPersistedMutationResults(t *testing.T) {
	store := openMutationLedgerStore(t)
	ctx := WithMutationIdempotencyKey(context.Background(), "blob-version-retry")
	input := WriteArtifactVersionInput{ArtifactID: "artifact-blob", ProjectID: "project-a", Name: "blob.txt",
		ContentType: "text/plain", Content: strings.NewReader("blob content"), CreatedBy: "owner-a"}
	_, first, err := store.WriteArtifactVersionRealtime(ctx, input, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	input.Content = strings.NewReader("blob content")
	_, replayed, err := store.WriteArtifactVersionRealtime(ctx, input, "owner-a")
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("blob replay=%#v err=%v", replayed, err)
	}
	lineage, _ := store.ArtifactLineage("artifact-blob")
	if len(lineage) != 1 {
		t.Fatalf("blob retry lineage=%#v", lineage)
	}

	copyCtx := WithMutationIdempotencyKey(context.Background(), "copy-retry")
	_, copyFirst, err := store.CopyArtifactRealtime(copyCtx, "artifact-blob", "project-a", "artifact-copy", "copy.txt", "owner-a", "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	_, copyReplay, err := store.CopyArtifactRealtime(copyCtx, "artifact-blob", "project-a", "artifact-copy", "copy.txt", "owner-a", "owner-a")
	if err != nil || copyReplay.ID != copyFirst.ID {
		t.Fatalf("copy replay=%#v err=%v", copyReplay, err)
	}
	copyLineage, _ := store.ArtifactLineage("artifact-copy")
	if len(copyLineage) != 1 {
		t.Fatalf("copy retry lineage=%#v", copyLineage)
	}
}

func TestBlobArtifactVersionCommitsFrameJournalAndOutboxOnce(t *testing.T) {
	store := openMutationLedgerStore(t)
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame-a", ProjectID: "project-a", AgentName: "planner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	ctx := WithMutationIdempotencyKey(context.Background(), "framed-blob-version")
	input := WriteArtifactVersionInput{ArtifactID: "artifact-framed", ProjectID: "project-a", Name: "framed.txt",
		ContentType: "text/plain", Content: strings.NewReader("content"), CreatedBy: "owner-a", FrameID: "frame-a", RootFrameID: "frame-a"}
	_, first, err := store.WriteArtifactVersionRealtime(ctx, input, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	input.Content = strings.NewReader("content")
	_, replayed, err := store.WriteArtifactVersionRealtime(ctx, input, "owner-a")
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("framed replay=%#v err=%v", replayed, err)
	}
	events, err := store.ListFrameEvents("frame-a", 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != "artifact_created" {
		t.Fatalf("frame journal=%#v err=%v", events, err)
	}
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 2 {
		t.Fatalf("framed artifact outbox count=%d err=%v", count, err)
	}
}

func TestApplyArtifactEditReplaysVersionAndCarriedAnnotations(t *testing.T) {
	store := openMutationLedgerStore(t)
	_, parent, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "artifact-edit", ProjectID: "project-a",
		Name: "edit.txt", Kind: "text/plain", Content: []byte("before"), CreatedBy: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	annotation, err := store.CreateAnnotationOwned(context.Background(), CreateAnnotationInput{ProjectID: "project-a",
		TargetKind: "artifact", TargetKey: "av:" + parent.ID, Body: map[string]any{"text": "review"}}, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithMutationIdempotencyKey(context.Background(), "apply-edit-retry")
	input := ApplyArtifactEditInput{ArtifactID: "artifact-edit", ProjectID: "project-a", Name: "edit.txt", Kind: "text/plain",
		Content: []byte("after"), CreatedBy: "owner-a", ParentVersionID: parent.ID, FromAnnotationTargetKey: "av:" + parent.ID}
	_, first, carried, err := store.ApplyArtifactEditRealtime(ctx, input, "owner-a")
	if err != nil || len(carried) != 1 {
		t.Fatalf("first edit=%#v carried=%#v err=%v", first, carried, err)
	}
	_, replayed, replayedAnnotations, err := store.ApplyArtifactEditRealtime(ctx, input, "owner-a")
	if err != nil || replayed.ID != first.ID || len(replayedAnnotations) != 1 || replayedAnnotations[0].ID != carried[0].ID {
		t.Fatalf("edit replay=%#v annotations=%#v err=%v", replayed, replayedAnnotations, err)
	}
	if replayedAnnotations[0].ID == annotation.ID {
		t.Fatal("carried annotation must retain its own persisted id")
	}
	lineage, _ := store.ArtifactLineage("artifact-edit")
	if len(lineage) != 2 {
		t.Fatalf("edit retry lineage=%#v", lineage)
	}
}

func TestAttachmentFinalizeReplaysAfterUploadRowsAreDeleted(t *testing.T) {
	store := openMutationLedgerStore(t)
	upload, err := store.InitAttachmentUpload(context.Background(), InitAttachmentUploadInput{UserID: "owner-a", ProjectID: "project-a",
		Filename: "evidence.bin", ContentType: "application/octet-stream", TotalSize: 4, ChunkSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	for index, chunk := range [][]byte{[]byte("ab"), []byte("cd")} {
		if _, err := store.SaveAttachmentChunk(context.Background(), "owner-a", upload.ID, index, bytes.NewReader(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithMutationIdempotencyKey(context.Background(), "attachment-finalize-retry")
	first, err := store.FinalizeAttachmentUpload(ctx, "owner-a", upload.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.FinalizeAttachmentUpload(ctx, "owner-a", upload.ID, "")
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("attachment replay=%#v err=%v", replayed, err)
	}
	changedCtx := WithMutationIdempotencyKey(context.Background(), "attachment-finalize-retry")
	if _, err := store.FinalizeAttachmentUpload(changedCtx, "owner-a", upload.ID,
		"0000000000000000000000000000000000000000000000000000000000000000"); !errors.Is(err, ErrMutationIdempotencyConflict) {
		t.Fatalf("attachment changed retry error=%v", err)
	}
}

func openMutationLedgerStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	return store
}
