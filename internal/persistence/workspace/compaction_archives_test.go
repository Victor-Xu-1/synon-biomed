package workspace

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCompactionArchivesPersistIdempotentlyAndAggregateMessages(t *testing.T) {
	store := newCompactionTestStore(t)
	sourceEventID := int64(7)
	first, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
		FrameID: "frame", MessageCount: 2, Summary: "first summary",
		Messages:      []map[string]any{{"role": "user", "text": "one"}, {"role": "assistant", "text": "two"}},
		SourceEventID: &sourceEventID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.CompactionIndex != 0 || first.MessageCount != 2 || first.TokenCount != nil || first.SourceEventID == nil || *first.SourceEventID != sourceEventID {
		t.Fatalf("first archive = %#v", first)
	}
	replayed, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
		FrameID: "frame", MessageCount: 2, Summary: "first summary",
		Messages:      []map[string]any{{"role": "user", "text": "one"}, {"role": "assistant", "text": "two"}},
		SourceEventID: &sourceEventID,
	})
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("idempotent archive = %#v, err=%v", replayed, err)
	}
	if _, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
		FrameID: "frame", MessageCount: 1, Summary: "different",
		Messages: []map[string]any{{"role": "user", "text": "changed"}}, SourceEventID: &sourceEventID,
	}); err == nil || !strings.Contains(err.Error(), "different payload") {
		t.Fatalf("payload conflict = %v", err)
	}
	tokens := 123
	second, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
		FrameID: "frame", MessageCount: 1, TokenCount: &tokens, Summary: "second summary",
		Messages: []map[string]any{{"role": "user", "text": "three"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.CompactionIndex != 1 || second.TokenCount == nil || *second.TokenCount != tokens {
		t.Fatalf("second archive = %#v", second)
	}
	secondIndex := 1
	secondReplay, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
		FrameID: "frame", CompactionIndex: &secondIndex, MessageCount: 1,
		TokenCount: &tokens, Summary: "second summary",
		Messages: []map[string]any{{"role": "user", "text": "three"}},
	})
	if err != nil || secondReplay.ID != second.ID {
		t.Fatalf("index-idempotent archive = %#v, err=%v", secondReplay, err)
	}
	if _, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
		FrameID: "frame", CompactionIndex: &secondIndex, MessageCount: 1,
		Summary: "index conflict", Messages: []map[string]any{{"role": "user", "text": "different"}},
	}); err == nil || !strings.Contains(err.Error(), "index 1 was reused") {
		t.Fatalf("index payload conflict = %v", err)
	}
	listed, err := store.ListCompactionArchives("frame")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Messages != nil || listed[1].Messages != nil {
		t.Fatalf("archive metadata = %#v", listed)
	}
	aggregated, err := store.ListCompactionMessages("frame", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregated) != 3 || aggregated[2]["text"] != "three" {
		t.Fatalf("aggregated messages = %#v", aggregated)
	}
}

func TestCompactionArchivesAllocateUniqueIndexesConcurrently(t *testing.T) {
	store := newCompactionTestStore(t)
	const workers = 16
	indexes := make(chan int, workers)
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			sourceEventID := int64(worker + 1)
			archive, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
				FrameID: "frame", MessageCount: 1, Summary: "summary",
				Messages: []map[string]any{{"worker": worker}}, SourceEventID: &sourceEventID,
			})
			if err != nil {
				errors <- err
				return
			}
			indexes <- archive.CompactionIndex
		}(worker)
	}
	group.Wait()
	close(errors)
	close(indexes)
	for err := range errors {
		t.Fatal(err)
	}
	got := make([]int, 0, workers)
	for index := range indexes {
		got = append(got, index)
	}
	sort.Ints(got)
	if len(got) != workers {
		t.Fatalf("allocated indexes = %#v", got)
	}
	for index, value := range got {
		if value != index {
			t.Fatalf("allocated indexes = %#v", got)
		}
	}
}

func TestCompactionMessagesMatchV11SettledAggregationSemantics(t *testing.T) {
	store := newCompactionTestStore(t)
	indexZero := 0
	boundaryMessages := []map[string]any{
		{"text": "plain"},
		{"text": "false", "_compact_boundary": false},
		{"text": "zero", "_compact_boundary": 0},
		{"text": "empty-string", "_compact_boundary": ""},
		{"text": "null", "_compact_boundary": nil},
		{"text": "true", "_compact_boundary": true},
		{"text": "string", "_compact_boundary": "yes"},
		{"text": "object", "_compact_boundary": map[string]any{}},
		{"text": "array", "_compact_boundary": []any{}},
	}
	if _, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
		FrameID: "frame", CompactionIndex: &indexZero, MessageCount: len(boundaryMessages),
		Summary: "zero", Messages: boundaryMessages,
	}); err != nil {
		t.Fatal(err)
	}
	indexTwo := 2
	if _, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
		FrameID: "frame", CompactionIndex: &indexTwo, MessageCount: 1,
		Summary: "two", Messages: []map[string]any{{"text": "after-gap"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO compaction_archives
		(id, frame_id, compaction_index, message_count, summary, messages, created_at)
		VALUES ('corrupt', 'frame', 1, 1, 'corrupt', 'not-json', ?)
	`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	assertTexts := func(through int, want ...string) {
		t.Helper()
		messages, err := store.ListCompactionMessages("frame", through)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(messages))
		for _, message := range messages {
			got = append(got, message["text"].(string))
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("through %d messages = %#v, want %#v", through, got, want)
		}
	}
	assertTexts(-1)
	assertTexts(0, "plain", "false", "zero", "empty-string", "null")
	assertTexts(1, "plain", "false", "zero", "empty-string", "null")
	assertTexts(2, "plain", "false", "zero", "empty-string", "null", "after-gap")
	assertTexts(99, "plain", "false", "zero", "empty-string", "null", "after-gap")
}

func TestCompactionArchivesActivatePreviouslyArchivedV11Rows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	createdAtMillis := time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC).UnixMilli()
	if _, err := store.db.Exec(`
		CREATE TABLE legacy_v11_compaction_archives (
			id TEXT PRIMARY KEY, frame_id TEXT NOT NULL, compaction_index INTEGER NOT NULL,
			message_count INTEGER NOT NULL, token_count INTEGER, summary TEXT NOT NULL,
			messages TEXT NOT NULL, created_at INTEGER NOT NULL
		);
		INSERT INTO legacy_v11_compaction_archives VALUES
		('legacy-archive', 'frame', 4, 1, 42, 'legacy summary', '[{"role":"user","text":"legacy"}]', ?)
	`, createdAtMillis); err != nil {
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
	archive, found, err := reopened.GetCompactionArchive("frame", 4)
	if err != nil || !found {
		t.Fatalf("activated archive found=%t archive=%#v err=%v", found, archive, err)
	}
	if archive.ID != "legacy-archive" || archive.Summary != "legacy summary" || archive.TokenCount == nil || *archive.TokenCount != 42 || len(archive.Messages) != 1 {
		t.Fatalf("activated archive = %#v", archive)
	}
	var legacyRows int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM legacy_v11_compaction_archives`).Scan(&legacyRows); err != nil {
		t.Fatal(err)
	}
	if legacyRows != 1 {
		t.Fatalf("legacy archive rows = %d", legacyRows)
	}
}

func newCompactionTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	return store
}
