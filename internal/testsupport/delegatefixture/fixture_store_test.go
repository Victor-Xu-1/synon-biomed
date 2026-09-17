package delegatefixture

import (
	"context"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestSeedPersistsMultiLevelParallelTreeInSQLite(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixture, err := Seed(store)
	if err != nil {
		t.Fatal(err)
	}
	parent := fixtureFrame(t, store, fixture.ParentFrameID)
	if parent.Status != "processing" || parent.MessageCount != 9 || len(parent.ChildIDs) != 2 {
		t.Fatalf("parent = %#v", parent)
	}
	if parent.ContextData["_tool_id_to_frame_id"].(map[string]any)[fixture.SecondToolUseID] != fixture.SecondChildFrameID {
		t.Fatalf("parent context = %#v", parent.ContextData)
	}
	firstChild := fixtureFrame(t, store, fixture.ChildFrameID)
	if firstChild.Status != "completed" || firstChild.MessageCount != 4 ||
		len(firstChild.ChildIDs) != 1 || firstChild.ChildIDs[0] != fixture.GrandchildFrameID {
		t.Fatalf("first child = %#v", firstChild)
	}
	secondChild := fixtureFrame(t, store, fixture.SecondChildFrameID)
	if secondChild.Status != "awaiting_user_response" || secondChild.MessageCount != 2 ||
		secondChild.ParentFrameID != fixture.ParentFrameID {
		t.Fatalf("second child = %#v", secondChild)
	}
	grandchild := fixtureFrame(t, store, fixture.GrandchildFrameID)
	if grandchild.Status != "failed" || grandchild.MessageCount != 2 ||
		grandchild.ParentFrameID != fixture.ChildFrameID || grandchild.RootFrameID != fixture.ParentFrameID {
		t.Fatalf("grandchild = %#v", grandchild)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, frameID := range []string{fixture.ParentFrameID, fixture.ChildFrameID} {
		if _, found, err := repository.GetFrameStreamBySession(context.Background(), fixture.UserID, frameID); err != nil {
			t.Fatal(err)
		} else if found {
			t.Fatalf("legacy fixture unexpectedly created transcript stream %q", frameID)
		}
	}
	if _, err := Seed(store); err != nil {
		t.Fatalf("replace fixture: %v", err)
	}
	for _, frameID := range []string{fixture.ParentFrameID, fixture.ChildFrameID} {
		if _, found, err := repository.GetFrameStreamBySession(context.Background(), fixture.UserID, frameID); err != nil {
			t.Fatal(err)
		} else if found {
			t.Fatalf("replacement fixture unexpectedly created transcript stream %q", frameID)
		}
	}
}

func fixtureFrame(t *testing.T, store *workspace.Store, id string) workspace.CompatibilityFrame {
	t.Helper()
	frame, found, err := store.GetCompatibilityFrame(id)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("frame %q not found", id)
	}
	return frame
}
