package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDomainRealtimeIDsSeparateEqualPayloadMutationsAndStabilizeRetries(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "artifact-a", ProjectID: "project-a", Name: "a", Kind: "text", Content: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateArtifactPriorityRealtime(WithMutationIdempotencyKey(context.Background(), "mutation-one"), "artifact-a", "owner-a", ArtifactPriorityUserStarred); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateArtifactPriorityRealtime(WithMutationIdempotencyKey(context.Background(), "mutation-two"), "artifact-a", "owner-a", ArtifactPriorityUserStarred); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 2 {
		t.Fatalf("equal-payload independent mutation count=%d err=%v", count, err)
	}

	retryCtx := WithMutationIdempotencyKey(context.Background(), "stable-retry")
	if _, err := store.UpdateArtifactPriorityRealtime(retryCtx, "artifact-a", "owner-a", ArtifactPriorityUserHidden); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateArtifactPriorityRealtime(retryCtx, "artifact-a", "owner-a", ArtifactPriorityUserHidden); err != nil {
		t.Fatalf("same mutation retry: %v", err)
	}
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 3 {
		t.Fatalf("stable retry outbox count=%d err=%v", count, err)
	}
	if _, err := store.UpdateArtifactPriorityRealtime(retryCtx, "artifact-a", "owner-a", ArtifactPriorityUserNoPriority); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("same key changed content error=%v", err)
	}
	artifact, found, err := store.GetArtifact("artifact-a")
	if err != nil || !found || artifact.Priority != ArtifactPriorityUserHidden {
		t.Fatalf("conflicting retry escaped rollback artifact=%#v found=%v err=%v", artifact, found, err)
	}
}

func TestArtifactDomainMutationRollsBackWhenOutboxIdentityConflicts(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "artifact-a", ProjectID: "project-a", Name: "before", Kind: "text", Content: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	ctx := WithMutationIdempotencyKey(context.Background(), "rename-conflict")
	eventID := domainRealtimeEventID(ctx, "artifact_renamed", "artifact-a")
	if err := store.WithTransaction(ctx, func(tx *sql.Tx) error {
		_, err := store.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{ID: eventID, UserID: "owner-a",
			ProjectID: "project-a", Type: "artifact_renamed", Payload: map[string]any{
				"project_id": "project-a", "artifact_id": "artifact-a", "new_filename": "different",
			}}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenameArtifactRealtime(ctx, "artifact-a", "owner-a", "after"); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("rename conflict error=%v", err)
	}
	artifact, found, err := store.GetArtifact("artifact-a")
	if err != nil || !found || artifact.Name != "before" {
		t.Fatalf("outbox failure did not roll back artifact=%#v found=%v err=%v", artifact, found, err)
	}
}

func TestArtifactDomainMutationRechecksOwnerInsideTransaction(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "artifact-a", ProjectID: "project-a", Name: "before", Kind: "text", Content: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenameArtifactRealtime(context.Background(), "artifact-a", "owner-b", "stolen"); err == nil {
		t.Fatal("cross-owner rename succeeded")
	}
	artifact, _, _ := store.GetArtifact("artifact-a")
	if artifact.Name != "before" {
		t.Fatalf("cross-owner rename changed artifact: %#v", artifact)
	}
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 0 {
		t.Fatalf("cross-owner outbox count=%d err=%v", count, err)
	}
}

func TestFolderAndNoteMutationsRollBackOnOutboxFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame-a", ProjectID: "project-a", AgentName: "planner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	seedConflict := func(ctx context.Context, eventType, aggregateID string) {
		t.Helper()
		eventID := domainRealtimeEventID(ctx, eventType, aggregateID)
		if err := store.WithTransaction(ctx, func(tx *sql.Tx) error {
			_, err := store.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{ID: eventID, UserID: "owner-a",
				ProjectID: "project-a", Type: eventType, Payload: map[string]any{"project_id": "project-a", "conflict": true}}, "")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	folderCtx := WithMutationIdempotencyKey(context.Background(), "folder-create-conflict")
	seedConflict(folderCtx, "folder_created", "folder-a")
	if _, err := store.CreateArtifactFolderRealtime(folderCtx, CreateArtifactFolderInput{ID: "folder-a", ProjectID: "project-a", Name: "Folder"}, "owner-a"); err == nil {
		t.Fatal("folder create unexpectedly survived outbox conflict")
	}
	if _, found, err := store.GetArtifactFolder("folder-a"); err != nil || found {
		t.Fatalf("rolled back folder found=%v err=%v", found, err)
	}

	noteCtx := WithMutationIdempotencyKey(context.Background(), "note-create-conflict")
	seedConflict(noteCtx, "note_update", "note-a")
	if _, err := store.CreateProjectNoteRealtime(noteCtx, CreateProjectNoteInput{ID: "note-a", ProjectID: "project-a",
		UserID: "owner-a", TargetType: "frame", TargetFrameID: "frame-a", Content: "Note"}, "owner-a"); err == nil {
		t.Fatal("note create unexpectedly survived outbox conflict")
	}
	var notes int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE id = 'note-a'`).Scan(&notes); err != nil || notes != 0 {
		t.Fatalf("rolled back note count=%d err=%v", notes, err)
	}
}

func TestConcurrentArtifactVersionsPreserveNumbersParentsAndLineageEvents(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	const writers = 12
	errorsByWriter := make(chan error, writers)
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx := WithMutationIdempotencyKey(context.Background(), fmt.Sprintf("version-%d", index))
			_, _, err := store.SaveArtifactVersionRealtime(ctx, SaveArtifactVersionInput{
				ArtifactID: "artifact-a", ProjectID: "project-a", Name: "a.txt", Kind: "text/plain",
				Content: []byte(fmt.Sprintf("version %d", index)), CreatedBy: "owner-a",
			}, "owner-a")
			errorsByWriter <- err
		}()
	}
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatal(err)
		}
	}
	lineage, err := store.ArtifactLineage("artifact-a")
	if err != nil || len(lineage) != writers {
		t.Fatalf("lineage length=%d err=%v", len(lineage), err)
	}
	for index, version := range lineage {
		wantNumber := writers - index
		if version.VersionNumber != wantNumber {
			t.Fatalf("lineage[%d] version=%d want=%d", index, version.VersionNumber, wantNumber)
		}
		if index < len(lineage)-1 && version.ParentID != lineage[index+1].ID {
			t.Fatalf("lineage[%d] parent=%q want=%q", index, version.ParentID, lineage[index+1].ID)
		}
	}
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != writers*2 {
		t.Fatalf("version outbox count=%d err=%v", count, err)
	}
}

func TestArtifactVersionIdempotencyKeyPreventsDuplicateVersionOnRetry(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	ctx := WithMutationIdempotencyKey(context.Background(), "stable-artifact-version")
	input := SaveArtifactVersionInput{ArtifactID: "artifact-a", ProjectID: "project-a", Name: "a.txt",
		Kind: "text/plain", Content: []byte("same request"), CreatedBy: "owner-a"}
	_, first, err := store.SaveArtifactVersionRealtime(ctx, input, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	_, replayed, err := store.SaveArtifactVersionRealtime(ctx, input, "owner-a")
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("version retry replayed=%#v err=%v", replayed, err)
	}
	lineage, err := store.ArtifactLineage("artifact-a")
	if err != nil || len(lineage) != 1 || lineage[0].ID != first.ID {
		t.Fatalf("retry created duplicate lineage=%#v err=%v", lineage, err)
	}
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 2 {
		t.Fatalf("version retry outbox count=%d err=%v", count, err)
	}
	changed := input
	changed.Content = []byte("changed request")
	if _, _, err := store.SaveArtifactVersionRealtime(ctx, changed, "owner-a"); !errors.Is(err, ErrMutationIdempotencyConflict) {
		t.Fatalf("changed retry error=%v", err)
	}
}
