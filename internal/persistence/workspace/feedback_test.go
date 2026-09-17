package workspace

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestFeedbackIsDurableAndUserScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.SaveFeedback(FeedbackInput{
		UserID: "user-1", Kind: "general", Payload: map[string]any{"rating": 5, "message": "solid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveFeedback(FeedbackInput{
		UserID: "user-2", Kind: "safety", RootFrameID: "frame-x", Payload: map[string]any{"reason": "test"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	records, err := reopened.ListFeedbackForUser("user-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != first.ID || records[0].Payload["message"] != "solid" {
		t.Fatalf("records=%#v", records)
	}
}

func TestSafetyFeedbackIsConcurrentAndIdempotent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const writers = 16
	records := make(chan FeedbackRecord, writers)
	errors := make(chan error, writers)
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record, _, saveErr := store.SaveSafetyFeedback(FeedbackInput{
				UserID: "user-1", RootFrameID: "root-1", Payload: map[string]any{"reason": "same report"},
			})
			if saveErr != nil {
				errors <- saveErr
				return
			}
			records <- record
		}()
	}
	wait.Wait()
	close(records)
	close(errors)
	for saveErr := range errors {
		t.Fatalf("concurrent safety feedback: %v", saveErr)
	}
	firstID := ""
	for record := range records {
		if firstID == "" {
			firstID = record.ID
		}
		if record.ID != firstID {
			t.Fatalf("idempotent records used different IDs: %q and %q", firstID, record.ID)
		}
	}
	stored, err := store.ListFeedbackForUser("user-1", 10)
	if err != nil || len(stored) != 1 || stored[0].ID != firstID {
		t.Fatalf("stored safety feedback=%#v err=%v", stored, err)
	}
}
