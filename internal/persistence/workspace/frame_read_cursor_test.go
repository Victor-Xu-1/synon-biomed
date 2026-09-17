package workspace

import (
	"context"
	"path/filepath"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestStableReadCursorIsMonotonicAndRepairRequiresObservedWinner(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.PutStableReadCursor("frame", "message-5", 5, "", false)
	if err != nil || first.MessageIndex != 5 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	winner, err := store.PutStableReadCursor("frame", "message-3", 3, "", false)
	if err != nil || winner.MessageUUID != "message-5" || winner.MessageIndex != 5 {
		t.Fatalf("monotonic winner=%#v err=%v", winner, err)
	}
	if _, err := store.PutStableReadCursor("frame", "conflict", 5, "", false); err == nil {
		t.Fatal("same-index identity conflict was accepted")
	}
	if _, err := store.PutStableReadCursor("frame", "message-2", 2, "stale-observation", true); err == nil {
		t.Fatal("stale repair authority was accepted")
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *transcriptstore.ImmediateTransaction) error {
		_, err := store.PutStableReadCursorImmediate(
			context.Background(), tx, "frame", "message-1", 1, "message-5", 4, true,
		)
		return err
	}); err == nil {
		t.Fatal("same-identity different-index repair authority was accepted")
	}
	repaired, err := store.PutStableReadCursor("frame", "message-2", 2, "message-5", true)
	if err != nil || repaired.MessageUUID != "message-2" || repaired.MessageIndex != 2 {
		t.Fatalf("repaired=%#v err=%v", repaired, err)
	}
	stored, found, err := store.GetReadCursor("frame")
	if err != nil || !found || stored.MessageUUID != repaired.MessageUUID || stored.MessageIndex != repaired.MessageIndex {
		t.Fatalf("stored=%#v found=%t err=%v", stored, found, err)
	}
}
