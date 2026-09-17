package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/persistence/workspace"
)

func TestDispatcherCrashAfterDeliveryBeforeAckRedeliversAndReceiverDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	store := openStoreAt(t, path)
	event := enqueueEvent(t, store, 0, 8)

	var requestCount atomic.Int32
	var sideEffectCount atomic.Int32
	var mu sync.Mutex
	unique := make(map[string]int)
	firstAccepted := make(chan struct{})
	var firstOnce sync.Once
	sink := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var envelope workspace.OutboxEvent
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Header.Get(EventIDHeader) != envelope.ID || request.Header.Get(IdempotencyHeader) != envelope.ID {
			http.Error(writer, "event id contract mismatch", http.StatusBadRequest)
			return
		}
		count := requestCount.Add(1)
		mu.Lock()
		if unique[envelope.ID] == 0 {
			sideEffectCount.Add(1)
		}
		unique[envelope.ID]++
		mu.Unlock()
		if count == 1 {
			firstOnce.Do(func() { close(firstAccepted) })
			<-request.Context().Done()
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sink.Close)

	dispatcher := newHTTPDispatcher(t, store, sink.URL, Options{
		WorkerID: "crashing-worker", BatchSize: 1, Lease: 150 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, ErrorBackoff: 5 * time.Millisecond,
		RetryBase: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond, DeliveryLimit: 120 * time.Millisecond,
	})
	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- dispatcher.Run(firstCtx) }()
	select {
	case <-firstAccepted:
		firstCancel()
	case <-time.After(2 * time.Second):
		t.Fatal("first delivery did not reach real HTTP sink")
	}
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("crashed dispatcher returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("crashed dispatcher did not stop")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first process store: %v", err)
	}

	time.Sleep(180 * time.Millisecond)
	restarted, err := workspace.Open(path)
	if err != nil {
		t.Fatalf("reopen workspace after crash: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartDispatcher := newHTTPDispatcher(t, restarted, sink.URL, Options{
		WorkerID: "restarted-worker", BatchSize: 1, Lease: time.Second,
		PollInterval: 5 * time.Millisecond, ErrorBackoff: 5 * time.Millisecond,
		RetryBase: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond, DeliveryLimit: 500 * time.Millisecond,
	})
	restartCtx, restartCancel := context.WithCancel(context.Background())
	restartDone := make(chan error, 1)
	go func() { restartDone <- restartDispatcher.Run(restartCtx) }()
	waitForOutboxStatus(t, restarted, event.ID, workspace.OutboxStatusDelivered, 3*time.Second)
	restartCancel()
	<-restartDone
	mu.Lock()
	uniqueCount := len(unique)
	receivedForEvent := unique[event.ID]
	mu.Unlock()
	if requestCount.Load() != 2 || uniqueCount != 1 || receivedForEvent != 2 || sideEffectCount.Load() != 1 {
		t.Fatalf("delivery counts: requests=%d unique=%d eventReceipts=%d sideEffects=%d; want 2/1/2/1", requestCount.Load(), uniqueCount, receivedForEvent, sideEffectCount.Load())
	}
	stored, err := restarted.GetOutboxEvent(context.Background(), event.ID)
	if err != nil || stored.AttemptCount != 2 {
		t.Fatalf("crash redelivery attempt state = %+v, %v; want attempt 2", stored, err)
	}
}

func TestDispatchersClaimEachEventImmediatelyBeforeDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	first := openStoreAt(t, path)
	second, err := workspace.Open(path)
	if err != nil {
		t.Fatalf("open competing Store: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	events := []workspace.OutboxEvent{
		enqueueEvent(t, first, 0, 4),
		enqueueEvent(t, first, 1, 4),
		enqueueEvent(t, first, 2, 4),
	}

	var mu sync.Mutex
	receipts := make(map[string]int)
	active := make(map[string]int)
	maxActive := make(map[string]int)
	sink := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		id := request.Header.Get(EventIDHeader)
		mu.Lock()
		receipts[id]++
		attempt := receipts[id]
		active[id]++
		if active[id] > maxActive[id] {
			maxActive[id] = active[id]
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			active[id]--
			mu.Unlock()
		}()
		if attempt == 1 && (id == events[0].ID || id == events[1].ID) {
			// Keep the downstream request active longer than the caller's delivery
			// deadline to recreate a slow/uncertain network outcome.
			time.Sleep(130 * time.Millisecond)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sink.Close)
	options := func(worker string) Options {
		return Options{
			WorkerID: worker, BatchSize: 3, Lease: 180 * time.Millisecond,
			PollInterval: 2 * time.Millisecond, ErrorBackoff: 3 * time.Millisecond,
			RetryBase: 10 * time.Millisecond, RetryMax: 20 * time.Millisecond, DeliveryLimit: 100 * time.Millisecond,
		}
	}
	d1 := newHTTPDispatcher(t, first, sink.URL, options("near-delivery-one"))
	d2 := newHTTPDispatcher(t, second, sink.URL, options("near-delivery-two"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- d1.Run(ctx) }()
	go func() { done <- d2.Run(ctx) }()
	for _, event := range events {
		waitForOutboxStatus(t, first, event.ID, workspace.OutboxStatusDelivered, 4*time.Second)
	}
	// Leave both dispatchers alive beyond the original batch lease. A dispatcher
	// holding stale preclaims would now deliver an already completed later item.
	time.Sleep(220 * time.Millisecond)
	cancel()
	<-done
	<-done
	mu.Lock()
	defer mu.Unlock()
	if receipts[events[2].ID] != 1 {
		t.Fatalf("unblocked trailing event delivered %d times, want exactly once", receipts[events[2].ID])
	}
	if maxActive[events[2].ID] > 1 {
		t.Fatalf("trailing event %s had %d concurrent deliveries after lease expiry", events[2].ID, maxActive[events[2].ID])
	}
}

func TestTwoDispatchersCompeteWithoutDuplicateClaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	first := openStoreAt(t, path)
	second, err := workspace.Open(path)
	if err != nil {
		t.Fatalf("open second Store: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	events := make([]workspace.OutboxEvent, 0, 24)
	for index := 0; index < 24; index++ {
		events = append(events, enqueueEvent(t, first, index, 5))
	}
	var mu sync.Mutex
	receipts := make(map[string]int, len(events))
	sink := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		id := request.Header.Get(EventIDHeader)
		mu.Lock()
		receipts[id]++
		mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sink.Close)
	options := func(worker string) Options {
		return Options{WorkerID: worker, BatchSize: 3, Lease: time.Second, PollInterval: 3 * time.Millisecond,
			ErrorBackoff: 5 * time.Millisecond, RetryBase: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond,
			DeliveryLimit: 500 * time.Millisecond}
	}
	d1 := newHTTPDispatcher(t, first, sink.URL, options("worker-one"))
	d2 := newHTTPDispatcher(t, second, sink.URL, options("worker-two"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- d1.Run(ctx) }()
	go func() { done <- d2.Run(ctx) }()
	for _, event := range events {
		waitForOutboxStatus(t, first, event.ID, workspace.OutboxStatusDelivered, 4*time.Second)
	}
	cancel()
	<-done
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(receipts) != len(events) {
		t.Fatalf("sink received %d unique events, want %d", len(receipts), len(events))
	}
	for id, count := range receipts {
		if count != 1 {
			t.Fatalf("event %s delivered %d times without a crash", id, count)
		}
	}
}

func TestDispatcherHTTPFailuresBackOffAndDeadLetter(t *testing.T) {
	store := openStoreAt(t, filepath.Join(t.TempDir(), "workspace.sqlite"))
	event := enqueueEvent(t, store, 0, 3)
	var mu sync.Mutex
	requestTimes := make([]time.Time, 0, 3)
	var reportedErrors atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requestTimes = append(requestTimes, time.Now())
		mu.Unlock()
		http.Error(writer, "temporary downstream failure", http.StatusServiceUnavailable)
	}))
	t.Cleanup(sink.Close)
	dispatcher := newHTTPDispatcher(t, store, sink.URL, Options{
		WorkerID: "failure-worker", BatchSize: 1, Lease: time.Second,
		PollInterval: 2 * time.Millisecond, ErrorBackoff: 5 * time.Millisecond,
		RetryBase: 40 * time.Millisecond, RetryMax: 80 * time.Millisecond, DeliveryLimit: 500 * time.Millisecond,
		OnError: func(error) { reportedErrors.Add(1) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	waitForOutboxStatus(t, store, event.ID, workspace.OutboxStatusDeadLetter, 4*time.Second)
	cancel()
	<-done
	stored, err := store.GetOutboxEvent(context.Background(), event.ID)
	if err != nil {
		t.Fatalf("GetOutboxEvent: %v", err)
	}
	if stored.AttemptCount != 3 || stored.DeadLetteredAt == nil || stored.LastError == "" {
		t.Fatalf("dead-letter state = %+v", stored)
	}
	mu.Lock()
	times := append([]time.Time(nil), requestTimes...)
	mu.Unlock()
	if len(times) != 3 {
		t.Fatalf("HTTP attempts = %d, want 3", len(times))
	}
	if reportedErrors.Load() < 3 {
		t.Fatalf("reported delivery errors = %d, want at least 3", reportedErrors.Load())
	}
	if firstDelay := times[1].Sub(times[0]); firstDelay < 30*time.Millisecond {
		t.Fatalf("first retry delay = %s, expected DB backoff near 40ms", firstDelay)
	}
	if secondDelay := times[2].Sub(times[1]); secondDelay < 65*time.Millisecond {
		t.Fatalf("second retry delay = %s, expected exponential DB backoff near 80ms", secondDelay)
	}
}

func TestDispatcherUsesOneIdleClaimCoordinatorForAllDeliveryWorkers(t *testing.T) {
	repository := &countingEmptyOutboxRepository{}
	dispatcher, err := NewDispatcher(repository, discardOutboxDeliverer{}, Options{
		WorkerID: "idle-coordinator", BatchSize: 4, Lease: time.Second,
		PollInterval: 10 * time.Millisecond, ErrorBackoff: 10 * time.Millisecond,
		RetryBase: 10 * time.Millisecond, RetryMax: 20 * time.Millisecond, DeliveryLimit: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Millisecond)
	defer cancel()
	if err := dispatcher.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	claims := repository.claims.Load()
	if claims < 3 || claims > 8 {
		t.Fatalf("idle claims=%d, want one coordinator near one claim per poll interval", claims)
	}
}

func TestDispatcherEventDrivenRepositoryDoesNotPollWhileIdle(t *testing.T) {
	repository := newWakeEmptyOutboxRepository()
	dispatcher, err := NewDispatcher(repository, discardOutboxDeliverer{}, Options{
		WorkerID: "event-driven", BatchSize: 4, Lease: time.Second,
		PollInterval: 5 * time.Millisecond, ErrorBackoff: 5 * time.Millisecond,
		RetryBase: 5 * time.Millisecond, RetryMax: 10 * time.Millisecond, DeliveryLimit: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	select {
	case <-repository.waiting:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not enter durable idle wait")
	}
	time.Sleep(30 * time.Millisecond)
	if claims := repository.claims.Load(); claims != 1 {
		t.Fatalf("idle event-driven claims=%d want=1", claims)
	}
	repository.Signal()
	deadline := time.Now().Add(time.Second)
	for repository.claims.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if claims := repository.claims.Load(); claims != 2 {
		t.Fatalf("claims after durable wake=%d want=2", claims)
	}
	time.Sleep(30 * time.Millisecond)
	if claims := repository.claims.Load(); claims != 2 {
		t.Fatalf("event-driven dispatcher resumed polling: claims=%d", claims)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop")
	}
}

type wakeEmptyOutboxRepository struct {
	countingEmptyOutboxRepository
	mu      sync.Mutex
	wake    chan struct{}
	waiting chan struct{}
}

func newWakeEmptyOutboxRepository() *wakeEmptyOutboxRepository {
	return &wakeEmptyOutboxRepository{wake: make(chan struct{}), waiting: make(chan struct{}, 1)}
}

func (r *wakeEmptyOutboxRepository) OutboxWake() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wake
}

func (r *wakeEmptyOutboxRepository) NextOutboxWakeAt(
	context.Context,
	[]string,
) (time.Time, bool, error) {
	select {
	case r.waiting <- struct{}{}:
	default:
	}
	return time.Time{}, false, nil
}

func (r *wakeEmptyOutboxRepository) Signal() {
	r.mu.Lock()
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
}

type countingEmptyOutboxRepository struct{ claims atomic.Int32 }

func (r *countingEmptyOutboxRepository) ClaimOutbox(
	context.Context,
	workspace.ClaimOutboxInput,
) ([]workspace.OutboxEvent, error) {
	r.claims.Add(1)
	return nil, nil
}

func (*countingEmptyOutboxRepository) AckOutbox(context.Context, string, string) error { return nil }
func (*countingEmptyOutboxRepository) RetryOutbox(
	context.Context,
	string,
	string,
	string,
	time.Duration,
) (bool, error) {
	return false, nil
}

type discardOutboxDeliverer struct{}

func (discardOutboxDeliverer) Deliver(context.Context, workspace.OutboxEvent) error { return nil }

func newHTTPDispatcher(t *testing.T, repository Repository, endpoint string, options Options) *Dispatcher {
	t.Helper()
	dispatcher, err := NewDispatcher(repository, HTTPDeliverer{Endpoint: endpoint}, options)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return dispatcher
}

func openStoreAt(t *testing.T, path string) *workspace.Store {
	t.Helper()
	store, err := workspace.Open(path)
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func enqueueEvent(t *testing.T, store *workspace.Store, index, maxAttempts int) workspace.OutboxEvent {
	t.Helper()
	event, err := store.EnqueueOutbox(context.Background(), workspace.EnqueueOutboxInput{
		IdempotencyKey: fmt.Sprintf("dispatch-%02d", index), Topic: "runtime.events",
		PartitionKey: fmt.Sprintf("frame-%d", index%4), Type: "frame.updated",
		AggregateType: "frame", AggregateID: fmt.Sprintf("frame-%d", index),
		Payload: json.RawMessage(fmt.Sprintf(`{"index":%d}`, index)), MaxAttempts: maxAttempts,
	})
	if err != nil {
		t.Fatalf("EnqueueOutbox(%d): %v", index, err)
	}
	return event
}

func waitForOutboxStatus(t *testing.T, store *workspace.Store, eventID, status string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		event, err := store.GetOutboxEvent(context.Background(), eventID)
		if err == nil && event.Status == status {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	event, err := store.GetOutboxEvent(context.Background(), eventID)
	t.Fatalf("event %s did not reach %s: event=%+v err=%v", eventID, status, event, err)
}
