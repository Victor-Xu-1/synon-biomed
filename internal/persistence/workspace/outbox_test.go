package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOutboxTransactionRollbackAndStableIdempotency(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	input := EnqueueOutboxInput{
		IdempotencyKey: "project-create:p-rollback:1",
		Topic:          "project.events",
		PartitionKey:   "project:p-rollback",
		Type:           "project.created",
		AggregateType:  "project",
		AggregateID:    "p-rollback",
		Payload:        json.RawMessage(`{"name":"Rollback"}`),
		Headers:        map[string]string{"trace-id": "trace-1"},
	}
	rollbackErr := errors.New("force rollback")
	err := store.WithTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects (id, user_id, name, path, created_at, updated_at)
			VALUES ('p-rollback', 'local', 'Rollback', '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
			return err
		}
		if _, err := store.EnqueueOutboxTx(ctx, tx, input); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("WithTransaction error = %v, want rollback sentinel", err)
	}
	if count, err := store.CountOutboxEvents(ctx); err != nil || count != 0 {
		t.Fatalf("outbox count after rollback = %d, %v; want 0", count, err)
	}
	var projectCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = 'p-rollback'`).Scan(&projectCount); err != nil || projectCount != 0 {
		t.Fatalf("project count after rollback = %d, %v; want 0", projectCount, err)
	}

	first, err := store.EnqueueOutbox(ctx, input)
	if err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}
	second, err := store.EnqueueOutbox(ctx, input)
	if err != nil {
		t.Fatalf("idempotent EnqueueOutbox: %v", err)
	}
	wantID := DeriveOutboxEventID(input.Topic, input.IdempotencyKey)
	if first.ID != wantID || second.ID != wantID || first.Sequence != second.Sequence {
		t.Fatalf("stable event mismatch: first=%+v second=%+v wantID=%s", first, second, wantID)
	}
	largeInteger := input
	largeInteger.IdempotencyKey = "large-integer"
	largeInteger.Payload = json.RawMessage(`{"exact":9007199254740993123456789}`)
	preserved, err := store.EnqueueOutbox(ctx, largeInteger)
	if err != nil {
		t.Fatalf("enqueue large integer JSON: %v", err)
	}
	if string(preserved.Payload) != string(largeInteger.Payload) {
		t.Fatalf("large integer payload changed: got %s want %s", preserved.Payload, largeInteger.Payload)
	}
	conflict := input
	conflict.Payload = json.RawMessage(`{"name":"Different"}`)
	if _, err := store.EnqueueOutbox(ctx, conflict); err == nil {
		t.Fatal("reusing event id with different content unexpectedly succeeded")
	}
}

func TestOutboxConcurrentClaimsDoNotOverlapAndExpiredLeaseIsTakenOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open first store: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open second store: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		_, err := first.EnqueueOutbox(ctx, EnqueueOutboxInput{
			IdempotencyKey: fmt.Sprintf("event-%02d", i),
			Topic:          "runtime.events",
			PartitionKey:   fmt.Sprintf("partition-%02d", i),
			Type:           "runtime.changed",
			Payload:        json.RawMessage(fmt.Sprintf(`{"index":%d}`, i)),
		})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	type claimResult struct {
		events []OutboxEvent
		err    error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for index, store := range []*Store{first, second} {
		wg.Add(1)
		go func(index int, store *Store) {
			defer wg.Done()
			<-start
			events, err := store.ClaimOutbox(ctx, ClaimOutboxInput{
				WorkerID: fmt.Sprintf("worker-%d", index), Limit: 6, Lease: 120 * time.Millisecond,
			})
			results <- claimResult{events: events, err: err}
		}(index, store)
	}
	close(start)
	wg.Wait()
	close(results)
	claimed := make(map[string]OutboxEvent, 12)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent claim: %v", result.err)
		}
		for _, event := range result.events {
			if event.AttemptCount != 1 {
				t.Fatalf("first claim attempt count = %d, want 1", event.AttemptCount)
			}
			if _, duplicate := claimed[event.ID]; duplicate {
				t.Fatalf("event %s was claimed by two workers", event.ID)
			}
			claimed[event.ID] = event
		}
	}
	if len(claimed) != 12 {
		t.Fatalf("claimed %d events, want 12", len(claimed))
	}

	var expired OutboxEvent
	for _, event := range claimed {
		expired = event
		break
	}
	time.Sleep(150 * time.Millisecond)
	taken, err := second.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "takeover", Limit: 12, Lease: time.Second})
	if err != nil {
		t.Fatalf("take over expired claims: %v", err)
	}
	var replacement OutboxEvent
	for _, event := range taken {
		if event.ID == expired.ID {
			replacement = event
			break
		}
	}
	if replacement.ID == "" || replacement.ClaimToken == expired.ClaimToken || replacement.ClaimOwner != "takeover" {
		t.Fatalf("expired claim was not fenced and taken over: old=%+v new=%+v", expired, replacement)
	}
	if replacement.AttemptCount != 2 {
		t.Fatalf("takeover attempt count = %d, want 2", replacement.AttemptCount)
	}
	if err := first.AckOutbox(ctx, expired.ID, expired.ClaimToken); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("stale ack error = %v, want ErrOutboxClaimLost", err)
	}
}

func TestOutboxEmptyClaimDoesNotCompeteForSQLiteWriter(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	store.db.SetMaxOpenConns(1)
	if _, err := store.db.ExecContext(ctx, `PRAGMA query_only = ON`); err != nil {
		t.Fatalf("enable query-only mode: %v", err)
	}
	defer func() { _, _ = store.db.ExecContext(context.Background(), `PRAGMA query_only = OFF`) }()

	events, err := store.ClaimOutbox(ctx, ClaimOutboxInput{
		WorkerID: "idle-worker", Limit: 1, Lease: time.Second,
	})
	if err != nil {
		t.Fatalf("empty claim waited for SQLite writer: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("empty claim returned events: %+v", events)
	}
}

func TestOutboxEmptyClaimPlanRejectsDeliveredRowsByReadyIndex(t *testing.T) {
	store := openOutboxTestStore(t)
	query, args := outboxAvailableWorkQuery([]string{"realtime"})
	rows, err := store.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain empty outbox query: %v", err)
	}
	defer rows.Close()
	details := make([]string, 0, 4)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "workspace_outbox_ready_idx") {
		t.Fatalf("empty outbox query did not use ready index:\n%s", plan)
	}
	if strings.Contains(plan, "sqlite_autoindex_workspace_outbox_2") ||
		strings.Contains(plan, "SCAN workspace_outbox") {
		t.Fatalf("empty outbox query may walk delivered topic rows:\n%s", plan)
	}
}

func TestOutboxWakePublishesCommittedDueTimeAndPartitionUnlock(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	wake := store.OutboxWake()
	before := time.Now().UTC()
	event, err := store.EnqueueOutbox(ctx, EnqueueOutboxInput{
		IdempotencyKey: "wake-due-1", Topic: "wake.events", PartitionKey: "wake-partition",
		Type: "wake.changed", Payload: json.RawMessage(`{"value":1}`), AvailableAfter: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("committed enqueue did not publish outbox wake")
	}
	wakeAt, found, err := store.NextOutboxWakeAt(ctx, []string{"wake.events"})
	if err != nil || !found || wakeAt.Before(before.Add(25*time.Millisecond)) || wakeAt.After(before.Add(time.Second)) {
		t.Fatalf("wakeAt=%s found=%t err=%v", wakeAt, found, err)
	}
	if early, err := store.ClaimOutbox(ctx, ClaimOutboxInput{
		WorkerID: "wake-early", Topics: []string{"wake.events"}, Limit: 1, Lease: time.Second,
	}); err != nil || len(early) != 0 {
		t.Fatalf("early claim=%#v err=%v", early, err)
	}
	time.Sleep(time.Until(wakeAt) + 10*time.Millisecond)
	claimed, err := store.ClaimOutbox(ctx, ClaimOutboxInput{
		WorkerID: "wake-ready", Topics: []string{"wake.events"}, Limit: 1, Lease: time.Second,
	})
	if err != nil || len(claimed) != 1 || claimed[0].ID != event.ID {
		t.Fatalf("ready claim=%#v err=%v", claimed, err)
	}
	wake = store.OutboxWake()
	if err := store.AckOutbox(ctx, claimed[0].ID, claimed[0].ClaimToken); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("ack did not publish partition-unlock wake")
	}
}

func TestOutboxRollbackWakeNeverExposesUncommittedEvent(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	wake := store.OutboxWake()
	sentinel := errors.New("rollback fixture")
	err := store.WithTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := store.EnqueueOutboxTx(ctx, tx, EnqueueOutboxInput{
			IdempotencyKey: "wake-rollback-1", Topic: "rollback.events",
			Type: "rollback.changed", Payload: json.RawMessage(`{"value":1}`),
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error=%v", err)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("rollback fixture did not exercise centralized wake")
	}
	events, err := store.ClaimOutbox(ctx, ClaimOutboxInput{
		WorkerID: "rollback-reader", Topics: []string{"rollback.events"}, Limit: 1, Lease: time.Second,
	})
	if err != nil || len(events) != 0 {
		t.Fatalf("rollback exposed events=%#v err=%v", events, err)
	}
}

func TestOutboxRetryUsesDBAvailabilityAndDeadLettersAtMaximum(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	event, err := store.EnqueueOutbox(ctx, EnqueueOutboxInput{
		IdempotencyKey: "failure-1", Topic: "sink.events", Type: "sink.write",
		Payload: json.RawMessage(`{"value":1}`), MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed := claimOneOutbox(t, store, "worker-a", time.Second)
	dead, err := store.RetryOutbox(ctx, event.ID, claimed.ClaimToken, "first failure", 80*time.Millisecond)
	if err != nil || dead {
		t.Fatalf("first retry = dead %v, err %v; want pending", dead, err)
	}
	ready, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "too-early", Limit: 1, Lease: time.Second})
	if err != nil || len(ready) != 0 {
		t.Fatalf("claim during DB backoff = %d, %v; want none", len(ready), err)
	}
	time.Sleep(100 * time.Millisecond)
	claimed = claimOneOutbox(t, store, "worker-b", time.Second)
	dead, err = store.RetryOutbox(ctx, event.ID, claimed.ClaimToken, "second failure", time.Second)
	if err != nil || !dead {
		t.Fatalf("second retry = dead %v, err %v; want dead letter", dead, err)
	}
	stored, err := store.GetOutboxEvent(ctx, event.ID)
	if err != nil {
		t.Fatalf("get dead letter: %v", err)
	}
	if stored.Status != OutboxStatusDeadLetter || stored.AttemptCount != 2 || stored.DeadLetteredAt == nil || stored.LastError != "second failure" {
		t.Fatalf("dead letter state = %+v", stored)
	}
	ready, err = store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "after-dead", Limit: 1, Lease: time.Second})
	if err != nil || len(ready) != 0 {
		t.Fatalf("dead letter was claimable: %d, %v", len(ready), err)
	}
}

func TestOutboxClaimFairlyRotatesPartitionHeads(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	enqueue := func(partition string, index int) OutboxEvent {
		t.Helper()
		event, err := store.EnqueueOutbox(ctx, EnqueueOutboxInput{
			IdempotencyKey: fmt.Sprintf("%s-%d", partition, index), Topic: "fair.events",
			PartitionKey: partition, Type: "fair.changed",
			Payload: json.RawMessage(fmt.Sprintf(`{"partition":%q,"index":%d}`, partition, index)),
		})
		if err != nil {
			t.Fatalf("enqueue %s/%d: %v", partition, index, err)
		}
		return event
	}
	partitionA := []OutboxEvent{enqueue("a", 1), enqueue("a", 2), enqueue("a", 3)}
	partitionB := []OutboxEvent{enqueue("b", 1), enqueue("b", 2), enqueue("b", 3)}
	partitionC := []OutboxEvent{enqueue("c", 1), enqueue("c", 2), enqueue("c", 3)}
	first, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "fair-1", Limit: 2, Lease: time.Second})
	if err != nil {
		t.Fatalf("first fair claim: %v", err)
	}
	if !sameOutboxIDs(first, partitionA[0].ID, partitionB[0].ID) {
		t.Fatalf("first fair claim = %v, want first heads of a and b", outboxIDs(first))
	}
	for _, event := range first {
		if err := store.AckOutbox(ctx, event.ID, event.ClaimToken); err != nil {
			t.Fatalf("ack first fair batch: %v", err)
		}
	}
	second, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "fair-2", Limit: 2, Lease: time.Second})
	if err != nil {
		t.Fatalf("second fair claim: %v", err)
	}
	if !sameOutboxIDs(second, partitionC[0].ID, partitionA[1].ID) {
		t.Fatalf("second fair claim = %v, want untouched c then next a", outboxIDs(second))
	}
}

func TestOutboxExpiredFinalAttemptIsReclaimedForDomainSettlement(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	event, err := store.EnqueueOutbox(ctx, EnqueueOutboxInput{
		IdempotencyKey: "final-crash", Topic: "crash.events", Type: "crash.final",
		Payload: json.RawMessage(`{"final":true}`), MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("enqueue final attempt: %v", err)
	}
	claimed := claimOneOutbox(t, store, "crashing-final-worker", 40*time.Millisecond)
	if claimed.AttemptCount != 1 {
		t.Fatalf("claimed attempt count = %d, want 1", claimed.AttemptCount)
	}
	time.Sleep(60 * time.Millisecond)
	events, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "recovery-worker", Limit: 1, Lease: time.Second})
	if err != nil {
		t.Fatalf("recovery claim: %v", err)
	}
	if len(events) != 1 || events[0].ID != event.ID || events[0].AttemptCount != 2 {
		t.Fatalf("exhausted event was not reclaimed for settlement: %+v", events)
	}
	dead, err := store.RetryOutbox(ctx, events[0].ID, events[0].ClaimToken, "final delivery failure", 0)
	if err != nil || !dead {
		t.Fatalf("settle exhausted event dead=%t err=%v", dead, err)
	}
	stored, err := store.GetOutboxEvent(ctx, event.ID)
	if err != nil || stored.Status != OutboxStatusDeadLetter || stored.AttemptCount != 2 || stored.DeadLetteredAt == nil {
		t.Fatalf("settled final attempt state = %+v err=%v", stored, err)
	}
}

func TestOutboxRejectsOversizedAndControlCharacterInputs(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	base := EnqueueOutboxInput{
		IdempotencyKey: "bounded-1", Topic: "bounded.events", PartitionKey: "bounded",
		Type: "bounded.changed", Payload: json.RawMessage(`{"ok":true}`),
	}
	tests := []struct {
		name   string
		mutate func(*EnqueueOutboxInput)
	}{
		{name: "event id control", mutate: func(input *EnqueueOutboxInput) { input.ID = "bad\nevent" }},
		{name: "event id oversized", mutate: func(input *EnqueueOutboxInput) { input.ID = strings.Repeat("e", outboxMaxEventIDBytes+1) }},
		{name: "topic control", mutate: func(input *EnqueueOutboxInput) { input.Topic = "bad\ttopic" }},
		{name: "topic oversized", mutate: func(input *EnqueueOutboxInput) { input.Topic = strings.Repeat("t", outboxMaxTopicBytes+1) }},
		{name: "partition control", mutate: func(input *EnqueueOutboxInput) { input.PartitionKey = "bad\rpartition" }},
		{name: "partition oversized", mutate: func(input *EnqueueOutboxInput) { input.PartitionKey = strings.Repeat("p", outboxMaxPartitionBytes+1) }},
		{name: "type oversized", mutate: func(input *EnqueueOutboxInput) { input.Type = strings.Repeat("y", outboxMaxTypeBytes+1) }},
		{name: "oversized payload", mutate: func(input *EnqueueOutboxInput) {
			input.Payload = json.RawMessage(`"` + strings.Repeat("x", outboxMaxPayloadBytes) + `"`)
		}},
		{name: "header control", mutate: func(input *EnqueueOutboxInput) { input.Headers = map[string]string{"X-Test": "bad\nvalue"} }},
		{name: "invalid header name", mutate: func(input *EnqueueOutboxInput) { input.Headers = map[string]string{"Bad Header": "value"} }},
		{name: "oversized header", mutate: func(input *EnqueueOutboxInput) {
			input.Headers = map[string]string{"X-Test": strings.Repeat("x", outboxMaxHeaderValueBytes+1)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.IdempotencyKey = "bounded-" + test.name
			test.mutate(&input)
			if _, err := store.EnqueueOutbox(ctx, input); err == nil {
				t.Fatal("invalid outbox input unexpectedly succeeded")
			}
		})
	}
	if _, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "worker", Limit: 1, Lease: outboxMaxLease + time.Millisecond}); err == nil {
		t.Fatal("oversized claim lease unexpectedly succeeded")
	}
	valid, err := store.EnqueueOutbox(ctx, base)
	if err != nil {
		t.Fatalf("enqueue valid event: %v", err)
	}
	claimed := claimOneOutbox(t, store, "worker", time.Second)
	if claimed.ID != valid.ID {
		t.Fatalf("claimed %s, want %s", claimed.ID, valid.ID)
	}
	if _, err := store.RetryOutbox(ctx, valid.ID, claimed.ClaimToken, "failed", outboxMaxBackoff+time.Millisecond); err == nil {
		t.Fatal("oversized retry backoff unexpectedly succeeded")
	}
}

func openOutboxTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func claimOneOutbox(t *testing.T, store *Store, worker string, lease time.Duration) OutboxEvent {
	t.Helper()
	events, err := store.ClaimOutbox(context.Background(), ClaimOutboxInput{WorkerID: worker, Limit: 1, Lease: lease})
	if err != nil {
		t.Fatalf("ClaimOutbox: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ClaimOutbox returned %d events, want 1", len(events))
	}
	return events[0]
}

func outboxIDs(events []OutboxEvent) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}

func sameOutboxIDs(events []OutboxEvent, expected ...string) bool {
	if len(events) != len(expected) {
		return false
	}
	actual := make(map[string]struct{}, len(events))
	for _, event := range events {
		actual[event.ID] = struct{}{}
	}
	for _, id := range expected {
		if _, ok := actual[id]; !ok {
			return false
		}
	}
	return true
}
