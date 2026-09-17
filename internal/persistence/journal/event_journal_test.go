package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestEventJournalPersistsVisibleEventsAndReplaysAfterCursor(t *testing.T) {
	journal := NewEventJournal(t.TempDir())

	first, err := journal.Append("session-1", Message{"type": "status", "state": "thinking", "verb": "Thinking"}, Metadata{
		RunID:           "run-1",
		ClientMessageID: "client-1",
	})
	if err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	second, err := journal.Append("session-1", Message{"type": "content_delta", "text": "hello"}, Metadata{
		RunID:           "run-1",
		ClientMessageID: "client-1",
	})
	if err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}

	if first == nil || first.EventID != 1 {
		t.Fatalf("first = %#v", first)
	}
	if second == nil || second.EventID != 2 {
		t.Fatalf("second = %#v", second)
	}
	if second.Message["runId"] != "run-1" || second.Message["clientMessageId"] != "client-1" {
		t.Fatalf("second message metadata = %#v", second.Message)
	}

	freshJournal := NewEventJournal(journal.Root())
	replayed, err := freshJournal.ReadAfter("session-1", 1, 100)
	if err != nil {
		t.Fatalf("ReadAfter() error = %v", err)
	}
	if len(replayed) != 1 || replayed[0].EventID != 2 || replayed[0].Message["text"] != "hello" {
		t.Fatalf("replayed = %#v", replayed)
	}
	has, err := freshJournal.HasClientMessage("session-1", "client-1")
	if err != nil {
		t.Fatalf("HasClientMessage(client-1) error = %v", err)
	}
	if !has {
		t.Fatal("expected client-1 to exist")
	}
	missing, err := freshJournal.HasClientMessage("session-1", "missing")
	if err != nil {
		t.Fatalf("HasClientMessage(missing) error = %v", err)
	}
	if missing {
		t.Fatal("missing client message should not exist")
	}
}

func TestEventJournalCursorReadsOnlyAppendTailAndRejectsReplacement(t *testing.T) {
	journal := NewEventJournal(t.TempDir())
	const total = 1205
	for index := 0; index < total; index++ {
		if _, err := journal.Append("cursor-session", Message{
			"type": "content_delta", "text": fmt.Sprintf("%04d", index),
		}, Metadata{ClientMessageID: fmt.Sprintf("cursor-%04d", index)}); err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
	}

	var cursor ReadCursor
	seen := make([]Entry, 0, total)
	wantBatches := []int{500, 500, 205}
	for batch, want := range wantBatches {
		entries, next, atEnd, err := journal.ReadFromCursor("cursor-session", cursor, 500)
		if err != nil {
			t.Fatalf("read batch %d: %v", batch, err)
		}
		if len(entries) != want || atEnd != (batch == len(wantBatches)-1) {
			t.Fatalf("batch %d entries=%d end=%t", batch, len(entries), atEnd)
		}
		if next.ByteOffset <= cursor.ByteOffset || next.EntriesSeen != len(seen)+len(entries) {
			t.Fatalf("batch %d cursor=%#v previous=%#v", batch, next, cursor)
		}
		seen = append(seen, entries...)
		cursor = next
	}
	if len(seen) != total || seen[0].EventID != 1 || seen[len(seen)-1].EventID != total {
		t.Fatalf("cursor replay range=%d first=%d last=%d", len(seen), seen[0].EventID, seen[len(seen)-1].EventID)
	}

	if _, err := journal.Append("cursor-session", Message{
		"type": "content_delta", "text": "tail",
	}, Metadata{ClientMessageID: "cursor-tail"}); err != nil {
		t.Fatal(err)
	}
	tail, next, atEnd, err := journal.ReadFromCursor("cursor-session", cursor, 500)
	if err != nil || len(tail) != 1 || tail[0].EventID != total+1 || !atEnd || next.ByteOffset <= cursor.ByteOffset {
		t.Fatalf("tail=%#v next=%#v end=%t err=%v", tail, next, atEnd, err)
	}

	if _, err := journal.ReplaceSession("cursor-session", []Message{{
		"type": "user_message", "role": "user", "text": "replacement",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := journal.ReadFromCursor("cursor-session", next, 500); !errors.Is(err, ErrReadCursorStale) {
		t.Fatalf("replacement cursor error=%v", err)
	}
}

func TestEventJournalNormalizesLegacyRoleMessagesBeforeStrictReplay(t *testing.T) {
	root := t.TempDir()
	journal := NewEventJournal(root)
	path := journal.filePath("legacy-role")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"sessionId":"legacy-role","eventId":1,"createdAt":"2026-07-23T08:00:00Z","clientMessageId":"legacy-client","message":{"role":"user","text":"answer","eventId":1,"createdAt":"2026-07-23T08:00:00Z","clientMessageId":"legacy-client"}}` + "\n")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	strict, err := journal.ReadAllStrict("legacy-role")
	if err != nil || len(strict) != 1 || strict[0].Message["type"] != "message" {
		t.Fatalf("strict=%#v err=%v", strict, err)
	}
	if _, err := journal.Append("legacy-role", Message{"role": "assistant", "text": "continued"}, Metadata{}); err != nil {
		t.Fatalf("append after legacy read: %v", err)
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.HasPrefix(raw, legacy) {
		t.Fatalf("legacy bytes changed=%t err=%v", bytes.HasPrefix(raw, legacy), err)
	}
}

func TestEventJournalStrictReadRejectsUnknownEnvelopeFields(t *testing.T) {
	journal := NewEventJournal(t.TempDir())
	path := journal.filePath("unknown-envelope")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"sessionId":"unknown-envelope","eventId":1,"createdAt":"2026-07-23T08:00:00Z","unexpected":true,"message":{"type":"message","role":"user","eventId":1,"createdAt":"2026-07-23T08:00:00Z"}}` + "\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ReadAllStrict("unknown-envelope"); err == nil || !strings.Contains(err.Error(), "is invalid") {
		t.Fatalf("unknown envelope error=%v", err)
	}
}

func TestEventJournalEntrySizeContractIsSymmetric(t *testing.T) {
	journal := NewEventJournal(t.TempDir())
	within := Message{"type": "im_message", "role": "user", "text": strings.Repeat("x", defaultMaxJournalEntryBytes-4096)}
	if _, err := journal.Append("bounded", within, Metadata{ClientMessageID: "within"}); err != nil {
		t.Fatalf("bounded append error=%v", err)
	}
	if entries, err := journal.ReadAllStrict("bounded"); err != nil || len(entries) != 1 {
		t.Fatalf("bounded strict read entries=%d err=%v", len(entries), err)
	}
	over := Message{"type": "im_message", "role": "user", "text": strings.Repeat("x", defaultMaxJournalEntryBytes)}
	if _, err := journal.Append("bounded", over, Metadata{ClientMessageID: "over"}); !errors.Is(err, ErrEntrySizeLimit) {
		t.Fatalf("oversized append error=%v", err)
	}
	root := t.TempDir()
	legacy := NewEventJournal(root)
	legacyPath := filepath.Join(root, "session-events", url.QueryEscape("legacy-oversized")+".jsonl")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, bytes.Repeat([]byte("x"), defaultMaxJournalEntryBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, read := range map[string]func() error{
		"strict": func() error { _, err := legacy.ReadAllStrict("legacy-oversized"); return err },
		"read":   func() error { _, err := legacy.ReadAll("legacy-oversized"); return err },
		"replay": func() error { _, err := legacy.ReadAfter("legacy-oversized", 0, 10); return err },
	} {
		if err := read(); !errors.Is(err, ErrEntrySizeLimit) {
			t.Fatalf("legacy oversized %s error=%v", name, err)
		}
	}
}

func TestEventJournalStrictReadRejectsValidJSONStructuralCorruption(t *testing.T) {
	createdAt := "2026-07-23T08:00:00Z"
	valid := func(sessionID string, eventID int64) Entry {
		return Entry{SessionID: sessionID, EventID: eventID, CreatedAt: createdAt, ClientMessageID: "client",
			Message: Message{"type": "im_message", "role": "user", "eventId": eventID, "createdAt": createdAt, "clientMessageId": "client"}}
	}
	tests := []struct {
		name    string
		entries []Entry
	}{
		{name: "foreign session", entries: []Entry{valid("other", 1)}},
		{name: "negative id", entries: []Entry{valid("strict", -1)}},
		{name: "duplicate id", entries: []Entry{valid("strict", 1), valid("strict", 1)}},
		{name: "out of order", entries: []Entry{valid("strict", 2), valid("strict", 1)}},
		{name: "invalid timestamp", entries: func() []Entry {
			entry := valid("strict", 1)
			entry.CreatedAt = "invalid"
			entry.Message["createdAt"] = "invalid"
			return []Entry{entry}
		}()},
		{name: "missing type and role", entries: func() []Entry {
			entry := valid("strict", 1)
			delete(entry.Message, "type")
			delete(entry.Message, "role")
			return []Entry{entry}
		}()},
		{name: "metadata mismatch", entries: func() []Entry {
			entry := valid("strict", 1)
			entry.Message["clientMessageId"] = "other"
			return []Entry{entry}
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "session-events", url.QueryEscape("strict")+".jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			var content bytes.Buffer
			for _, entry := range test.entries {
				encoded, err := json.Marshal(entry)
				if err != nil {
					t.Fatal(err)
				}
				content.Write(encoded)
				content.WriteByte('\n')
			}
			if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewEventJournal(root).ReadAllStrict("strict"); err == nil {
				t.Fatal("structural corruption was accepted")
			}
		})
	}
	t.Run("strictly increasing gaps are valid", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "session-events", url.QueryEscape("strict")+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		var content bytes.Buffer
		for _, entry := range []Entry{valid("strict", 1), valid("strict", 3)} {
			encoded, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			content.Write(encoded)
			content.WriteByte('\n')
		}
		if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if entries, err := NewEventJournal(root).ReadAllStrict("strict"); err != nil || len(entries) != 2 {
			t.Fatalf("increasing gap entries=%d err=%v", len(entries), err)
		}
	})
}

func TestEventJournalReadsByClientMessage(t *testing.T) {
	journal := NewEventJournal(t.TempDir())

	_, _ = journal.Append("session-2", Message{"type": "status", "state": "thinking"}, Metadata{RunID: "run-a", ClientMessageID: "client-a"})
	_, _ = journal.Append("session-2", Message{"type": "content_delta", "text": "first"}, Metadata{RunID: "run-a", ClientMessageID: "client-a"})
	_, _ = journal.Append("session-2", Message{"type": "content_delta", "text": "second"}, Metadata{RunID: "run-b", ClientMessageID: "client-b"})

	replayed, err := journal.ReadByClientMessage("session-2", "client-a")
	if err != nil {
		t.Fatalf("ReadByClientMessage() error = %v", err)
	}
	if len(replayed) != 2 {
		t.Fatalf("len(replayed) = %d", len(replayed))
	}
	for _, entry := range replayed {
		if entry.ClientMessageID != "client-a" {
			t.Fatalf("entry = %#v", entry)
		}
	}
}

func TestEventJournalReplaceSessionAtomicallyReindexesActiveBranch(t *testing.T) {
	root := t.TempDir()
	journal := NewEventJournal(root)
	if _, err := journal.Append("branch-session", Message{"type": "user_message", "text": "old"}, Metadata{ClientMessageID: "old"}); err != nil {
		t.Fatal(err)
	}
	replaced, err := journal.ReplaceSession("branch-session", []Message{
		{"type": "user_message", "role": "user", "text": "first", "clientMessageId": "first-id"},
		{"type": "assistant_message", "role": "assistant", "text": "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 2 || replaced[0].EventID != 1 || replaced[1].EventID != 2 || replaced[0].ClientMessageID != "first-id" {
		t.Fatalf("replaced journal = %#v", replaced)
	}
	reopened := NewEventJournal(root)
	entries, err := reopened.ReadAll("branch-session")
	if err != nil || len(entries) != 2 || entries[0].Message["text"] != "first" || entries[1].Message["text"] != "second" {
		t.Fatalf("reopened journal = %#v, err=%v", entries, err)
	}
	appended, err := reopened.Append("branch-session", Message{"type": "user_message", "text": "third"}, Metadata{})
	if err != nil || appended.EventID != 3 {
		t.Fatalf("append after replace = %#v, err=%v", appended, err)
	}
}

func TestEventJournalSkipsConnectionHousekeeping(t *testing.T) {
	journal := NewEventJournal(t.TempDir())

	connected, err := journal.Append("session-3", Message{"type": "connected", "sessionId": "session-3"}, Metadata{})
	if err != nil {
		t.Fatalf("Append(connected) error = %v", err)
	}
	pong, err := journal.Append("session-3", Message{"type": "pong"}, Metadata{})
	if err != nil {
		t.Fatalf("Append(pong) error = %v", err)
	}
	if connected != nil || pong != nil {
		t.Fatalf("connected = %#v pong = %#v", connected, pong)
	}
	replayed, err := journal.ReadAfter("session-3", 0, 100)
	if err != nil {
		t.Fatalf("ReadAfter() error = %v", err)
	}
	if len(replayed) != 0 {
		t.Fatalf("replayed = %#v", replayed)
	}
}

func TestEventJournalSerializesConcurrentAppends(t *testing.T) {
	journal := NewEventJournal(t.TempDir())
	errCh := make(chan error, 50)
	for index := 0; index < 50; index++ {
		index := index
		go func() {
			_, err := journal.Append("session-concurrent", Message{
				"type": "content_delta",
				"text": fmt.Sprintf("chunk-%d", index),
			}, Metadata{})
			errCh <- err
		}()
	}
	for index := 0; index < 50; index++ {
		if err := <-errCh; err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	replayed, err := journal.ReadAfter("session-concurrent", 0, 100)
	if err != nil {
		t.Fatalf("ReadAfter() error = %v", err)
	}
	if len(replayed) != 50 {
		t.Fatalf("len(replayed) = %d", len(replayed))
	}
	eventIDs := make([]int64, 0, len(replayed))
	for _, entry := range replayed {
		eventIDs = append(eventIDs, entry.EventID)
	}
	sort.Slice(eventIDs, func(i, j int) bool { return eventIDs[i] < eventIDs[j] })
	for index, eventID := range eventIDs {
		if eventID != int64(index+1) {
			t.Fatalf("event ids = %#v", eventIDs)
		}
	}
}

func TestEventJournalAppendIdempotentHasOneConcurrentWinner(t *testing.T) {
	root := t.TempDir()
	journal := NewEventJournal(root)
	message := Message{
		"type": "runner_finished", "runnerId": "runner-a", "runnerAttempt": 2,
		"status": "completed", "text": "done", "eventId": int64(999), "createdAt": "caller-value",
	}
	metadata := Metadata{RunID: "run-a", ClientMessageID: "finish-a"}
	start := make(chan struct{})
	entries := make(chan *Entry, 64)
	created := make(chan bool, 64)
	errs := make(chan error, 64)
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			entry, wasCreated, err := journal.AppendIdempotent("session-idempotent", message, metadata)
			entries <- entry
			created <- wasCreated
			errs <- err
		}()
	}
	close(start)
	workers.Wait()
	close(entries)
	close(created)
	close(errs)

	createdCount := 0
	for err := range errs {
		if err != nil {
			t.Fatalf("AppendIdempotent() error = %v", err)
		}
	}
	for wasCreated := range created {
		if wasCreated {
			createdCount++
		}
	}
	for entry := range entries {
		if entry == nil || entry.EventID != 1 {
			t.Fatalf("entry = %#v", entry)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}

	reopened := NewEventJournal(root)
	entry, wasCreated, err := reopened.AppendIdempotent("session-idempotent", message, metadata)
	if err != nil || wasCreated || entry == nil || entry.EventID != 1 {
		t.Fatalf("reopened AppendIdempotent() entry=%#v created=%t err=%v", entry, wasCreated, err)
	}
}

func TestEventJournalAppendIdempotentRejectsSemanticConflictsAndCorruption(t *testing.T) {
	journal := NewEventJournal(t.TempDir())
	metadata := Metadata{RunID: "run-a", ClientMessageID: "finish-a"}
	base := Message{
		"type": "runner_finished", "runnerId": "runner-a", "runnerAttempt": 2,
		"status": "completed", "text": "done", "afterEventId": int64(7),
	}
	if _, _, err := journal.AppendIdempotent("conflict", base, metadata); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(Message, *Metadata){
		"status":  func(message Message, _ *Metadata) { message["status"] = "failed" },
		"text":    func(message Message, _ *Metadata) { message["text"] = "different" },
		"attempt": func(message Message, _ *Metadata) { message["runnerAttempt"] = 3 },
		"run id":  func(_ Message, metadata *Metadata) { metadata.RunID = "run-b" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneMessage(base)
			candidateMetadata := metadata
			mutate(candidate, &candidateMetadata)
			if _, created, err := journal.AppendIdempotent("conflict", candidate, candidateMetadata); !errors.Is(err, ErrIdempotencyConflict) || created {
				t.Fatalf("conflict created=%t err=%v", created, err)
			}
		})
	}

	duplicateMetadata := Metadata{ClientMessageID: "duplicate"}
	for range 2 {
		if _, err := journal.Append("corrupt", base, duplicateMetadata); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := journal.AppendIdempotent("corrupt", base, duplicateMetadata); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("duplicate corruption error = %v", err)
	}
}

func TestEventJournalAppendIdempotentFailsClosedOnMalformedDurableLine(t *testing.T) {
	root := t.TempDir()
	journal := NewEventJournal(root)
	message := Message{"type": "runner_finished", "runnerId": "runner-a", "status": "completed"}
	if _, _, err := journal.AppendIdempotent("malformed", message, Metadata{ClientMessageID: "finish-a"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(journal.filePath("malformed"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(journal.filePath("malformed"))
	if err != nil {
		t.Fatal(err)
	}
	reopened := NewEventJournal(root)
	if _, created, err := reopened.AppendIdempotent("malformed", message, Metadata{ClientMessageID: "finish-b"}); err == nil || created {
		t.Fatalf("malformed journal append created=%t err=%v", created, err)
	}
	after, err := os.ReadFile(journal.filePath("malformed"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("malformed durable journal changed after rejected append")
	}
}

func TestEventJournalAppendIdempotentWithoutClientIDAppendsNormally(t *testing.T) {
	journal := NewEventJournal(t.TempDir())
	first, firstCreated, err := journal.AppendIdempotent("no-id", Message{"type": "content_delta", "text": "a"}, Metadata{})
	if err != nil || !firstCreated {
		t.Fatalf("first append entry=%#v created=%t err=%v", first, firstCreated, err)
	}
	second, secondCreated, err := journal.AppendIdempotent("no-id", Message{"type": "content_delta", "text": "a"}, Metadata{})
	if err != nil || !secondCreated || second.EventID != first.EventID+1 {
		t.Fatalf("second append entry=%#v created=%t err=%v", second, secondCreated, err)
	}
}

func TestEventJournalPreflightUsesExactAppendEnvelopeAndCumulativeLimits(t *testing.T) {
	journal := NewEventJournalWithLimits(t.TempDir(), Limits{MaxEntryBytes: 512, MaxFileBytes: 2048, MaxEntries: 1})
	metadata := Metadata{ClientMessageID: "boundary-1"}
	lastAccepted := -1
	for size := 0; size < 512; size++ {
		message := Message{"type": "im_message", "role": "user", "text": strings.Repeat("x", size)}
		err := journal.PreflightAppendIdempotent("boundary", message, metadata)
		if errors.Is(err, ErrEntrySizeLimit) {
			break
		}
		if err != nil {
			t.Fatalf("preflight size=%d: %v", size, err)
		}
		lastAccepted = size
	}
	if lastAccepted <= 0 {
		t.Fatalf("last accepted size=%d", lastAccepted)
	}
	accepted := Message{"type": "im_message", "role": "user", "text": strings.Repeat("x", lastAccepted)}
	if _, err := journal.Append("boundary", accepted, metadata); err != nil {
		t.Fatalf("append exact preflight boundary: %v", err)
	}
	over := Message{"type": "im_message", "role": "user", "text": strings.Repeat("x", lastAccepted+1)}
	if err := journal.PreflightAppendIdempotent("other", over, Metadata{ClientMessageID: "boundary-over"}); !errors.Is(err, ErrEntrySizeLimit) {
		t.Fatalf("over-boundary preflight error=%v", err)
	}
	if err := journal.PreflightAppendIdempotent("boundary", accepted, metadata); err != nil {
		t.Fatalf("idempotent existing projection should remain valid: %v", err)
	}
	if err := journal.PreflightAppendIdempotent("boundary", Message{
		"type": "im_message", "role": "user", "text": "next",
	}, Metadata{ClientMessageID: "boundary-2"}); !errors.Is(err, ErrEntryCountLimit) {
		t.Fatalf("entry-count preflight error=%v", err)
	}
}

func TestEventJournalLegacyLimitViolationsAreReadOnlyAndTyped(t *testing.T) {
	root := t.TempDir()
	writer := NewEventJournal(root)
	for index := 1; index <= 2; index++ {
		if _, err := writer.Append("legacy-limit", Message{
			"type": "im_message", "role": "user", "text": fmt.Sprintf("entry-%d", index),
		}, Metadata{ClientMessageID: fmt.Sprintf("legacy-%d", index)}); err != nil {
			t.Fatal(err)
		}
	}
	path := writer.filePath("legacy-limit")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fileLimited := NewEventJournalWithLimits(root, Limits{
		MaxEntryBytes: 512, MaxFileBytes: int64(len(before) - 1), MaxEntries: 10,
	})
	for name, read := range map[string]func() error{
		"strict":  func() error { _, err := fileLimited.ReadAllStrict("legacy-limit"); return err },
		"lenient": func() error { _, err := fileLimited.ReadAll("legacy-limit"); return err },
		"replay":  func() error { _, err := fileLimited.ReadAfter("legacy-limit", 0, 10); return err },
	} {
		if err := read(); !errors.Is(err, ErrFileSizeLimit) {
			t.Fatalf("%s file-limit error=%v", name, err)
		}
	}
	countLimited := NewEventJournalWithLimits(root, Limits{
		MaxEntryBytes: 512, MaxFileBytes: int64(len(before) + 1), MaxEntries: 1,
	})
	for name, read := range map[string]func() error{
		"strict":  func() error { _, err := countLimited.ReadAllStrict("legacy-limit"); return err },
		"lenient": func() error { _, err := countLimited.ReadAll("legacy-limit"); return err },
		"replay":  func() error { _, err := countLimited.ReadAfter("legacy-limit", 0, 10); return err },
	} {
		if err := read(); !errors.Is(err, ErrEntryCountLimit) {
			t.Fatalf("%s count-limit error=%v", name, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("limit diagnostics changed legacy journal: err=%v", err)
	}
}

func TestEventJournalReadAllStrictRejectsMalformedProjectionInput(t *testing.T) {
	root := t.TempDir()
	journal := NewEventJournal(root)
	if _, err := journal.Append("strict", Message{"type": "im_message", "role": "user"}, Metadata{ClientMessageID: "strict-1"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session-events", url.QueryEscape("strict")+".jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{malformed\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, err := journal.ReadAll("strict"); err != nil || len(entries) != 1 {
		t.Fatalf("lenient entries=%#v err=%v", entries, err)
	}
	if _, err := journal.ReadAllStrict("strict"); err == nil || strings.Contains(err.Error(), "malformed") {
		t.Fatalf("strict error=%v", err)
	}
}

func TestEventJournalEscapesDangerousSessionIDsIntoBoundedFilenames(t *testing.T) {
	root := t.TempDir()
	journal := NewEventJournal(root)
	sessionID := "../outside/session:im?token=secret&x=1"

	entry, err := journal.Append(sessionID, Message{"type": "content_delta", "text": "kept"}, Metadata{})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if entry == nil || entry.SessionID != sessionID {
		t.Fatalf("entry = %#v", entry)
	}

	journalPath := journal.filePath(sessionID)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("Abs(root) error = %v", err)
	}
	journalAbs, err := filepath.Abs(journalPath)
	if err != nil {
		t.Fatalf("Abs(journalPath) error = %v", err)
	}
	rel, err := filepath.Rel(rootAbs, journalAbs)
	if err != nil {
		t.Fatalf("Rel() error = %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("journal path escaped root: %s", rel)
	}
	if filepath.Dir(journalAbs) != filepath.Join(rootAbs, "session-events") {
		t.Fatalf("journal path should stay directly under session-events: %s", journalAbs)
	}
	if filepath.Base(journalAbs) != url.QueryEscape(sessionID)+".jsonl" {
		t.Fatalf("journal filename was not url-escaped: %s", filepath.Base(journalAbs))
	}
	if _, err := os.Stat(journalAbs); err != nil {
		t.Fatalf("Stat(journal) error = %v", err)
	}

	replayed, err := NewEventJournal(root).ReadAll(sessionID)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if len(replayed) != 1 || replayed[0].Message["text"] != "kept" {
		t.Fatalf("replayed = %#v", replayed)
	}

	cloneID := "../clone/session:im?token=secret"
	cloned, err := journal.CloneSession(sessionID, cloneID)
	if err != nil {
		t.Fatalf("CloneSession() error = %v", err)
	}
	if len(cloned) != 1 || cloned[0].SessionID != cloneID {
		t.Fatalf("cloned = %#v", cloned)
	}
	clonePath := journal.filePath(cloneID)
	cloneAbs, err := filepath.Abs(clonePath)
	if err != nil {
		t.Fatalf("Abs(clonePath) error = %v", err)
	}
	cloneRel, err := filepath.Rel(rootAbs, cloneAbs)
	if err != nil {
		t.Fatalf("Rel(clone) error = %v", err)
	}
	if cloneRel == ".." || strings.HasPrefix(cloneRel, ".."+string(filepath.Separator)) {
		t.Fatalf("clone journal path escaped root: %s", cloneRel)
	}

	if err := journal.Remove(sessionID); err != nil {
		t.Fatalf("Remove(source) error = %v", err)
	}
	if err := journal.Remove(cloneID); err != nil {
		t.Fatalf("Remove(clone) error = %v", err)
	}
	if _, err := os.Stat(journalAbs); !os.IsNotExist(err) {
		t.Fatalf("source journal should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(cloneAbs); !os.IsNotExist(err) {
		t.Fatalf("clone journal should be removed, stat err = %v", err)
	}
}
