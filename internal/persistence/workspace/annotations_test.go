package workspace

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAnnotationsPersistLabelsUpdatesAndCarryForward(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateAnnotation(CreateAnnotationInput{
		ProjectID: "project-1", TargetKind: "local", TargetKey: "file:local:/tmp/report.txt",
		ContentChecksum: "checksum-one", Body: map[string]any{"type": "point", "text": "review", "x_percent": 10.0, "y_percent": 20.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateAnnotation(CreateAnnotationInput{
		ProjectID: "project-1", TargetKind: "local", TargetKey: first.TargetKey,
		Body: map[string]any{"type": "text_selection", "text": "rewrite", "selection_text": "old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Public()["label"] != "①" || second.Public()["label"] != "②" {
		t.Fatalf("labels = %#v, %#v", first.Public(), second.Public())
	}
	updated, err := store.UpdateAnnotationText(first.ID, "updated")
	if err != nil || updated.Public()["text"] != "updated" {
		t.Fatalf("updated = %#v, %v", updated.Public(), err)
	}
	carried, err := store.CarryForwardAnnotations("project-1", first.TargetKey, "av:version-2", "checksum-two")
	if err != nil || len(carried) != 2 {
		t.Fatalf("carried = %#v, %v", carried, err)
	}
	if carried[0].ID == first.ID || carried[0].ContentChecksum != "checksum-two" || carried[0].TargetKey != "av:version-2" {
		t.Fatalf("carried annotation = %#v", carried[0])
	}
	if deleted, err := store.DeleteAnnotation(second.ID); err != nil || !deleted {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	remaining, err := store.ListAnnotations("project-1", first.TargetKey)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("remaining = %#v, %v", remaining, err)
	}
}

func TestConcurrentAnnotationLabelsRemainUniqueAndOrdered(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	const count = 24
	errorsByIndex := make([]error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errorsByIndex[index] = store.CreateAnnotation(CreateAnnotationInput{
				ProjectID: "project-1", TargetKind: "remote", TargetKey: "file:provider:path",
				Body: map[string]any{"type": "point", "text": fmt.Sprintf("note-%d", index), "x_percent": 1.0, "y_percent": 2.0},
			})
		}(index)
	}
	wait.Wait()
	for _, err := range errorsByIndex {
		if err != nil {
			t.Fatal(err)
		}
	}
	annotations, err := store.ListAnnotations("project-1", "file:provider:path")
	if err != nil || len(annotations) != count {
		t.Fatalf("annotations = %d, %v", len(annotations), err)
	}
	for index, annotation := range annotations {
		if annotation.LabelIndex != index {
			t.Fatalf("label index %d = %d", index, annotation.LabelIndex)
		}
	}
}

func TestArtifactEditRollsBackVersionWhenAnnotationCarryFails(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, first, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.txt", Kind: "text/plain", Content: []byte("old"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO annotations
			(id, project_id, target_kind, target_key, label_idx, body, created_at)
		VALUES ('bad-body', 'project-1', 'artifact', ?, 0, '{', ?)`, "av:"+first.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = store.ApplyArtifactEdit(ApplyArtifactEditInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Content: []byte("new"),
		CreatedBy: "user", ParentVersionID: first.ID, FromAnnotationTargetKey: "av:" + first.ID,
	})
	if err == nil {
		t.Fatal("malformed carried annotation did not abort edit")
	}
	_, current, found, err := store.GetCurrentArtifactVersion("artifact-1")
	if err != nil || !found || current.ID != first.ID || string(current.Content) != "old" {
		t.Fatalf("current version changed after rollback: %#v found=%v err=%v", current, found, err)
	}
}

func TestSaveArtifactVersionAcceptsExplicitParentFromSameArtifact(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, first, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.txt", Kind: "text/plain", Content: []byte("one"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.txt", Kind: "text/plain", Content: []byte("two"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, branched, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.txt", Kind: "text/plain", Content: []byte("branch"), ParentVersionID: first.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if branched.ParentID != first.ID {
		t.Fatalf("parent = %q, want %q", branched.ParentID, first.ID)
	}
}
