package server

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestStructureInteractionResultCoordinatorDeduplicatesAndCachesSuccess(t *testing.T) {
	coordinator := newStructureInteractionResultCoordinator(4, 4096, time.Minute, 2, 4, 4096)
	var calls atomic.Int32
	release := make(chan struct{})
	work := func(context.Context) structureInteractionHTTPResult {
		calls.Add(1)
		<-release
		return structureInteractionHTTPResult{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}
	}

	const waiters = 8
	results := make(chan structureInteractionHTTPResult, waiters)
	var ready sync.WaitGroup
	ready.Add(waiters)
	for range waiters {
		go func() {
			ready.Done()
			result, ok := coordinator.Do(context.Background(), "same", 32, time.Second, work)
			if !ok {
				t.Errorf("shared result waiter was cancelled")
				return
			}
			results <- result
		}()
	}
	ready.Wait()
	close(release)
	for range waiters {
		if result := <-results; result.Status != http.StatusOK {
			t.Fatalf("status = %d", result.Status)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("work calls = %d, want 1", calls.Load())
	}

	if _, ok := coordinator.Do(context.Background(), "same", 32, time.Second, work); !ok {
		t.Fatal("cached result unavailable")
	}
	if calls.Load() != 1 {
		t.Fatalf("cache miss triggered work call %d", calls.Load())
	}
}

func TestStructureInteractionResultCoordinatorDoesNotCacheFailure(t *testing.T) {
	coordinator := newStructureInteractionResultCoordinator(4, 4096, time.Minute, 1, 4, 4096)
	var calls atomic.Int32
	work := func(context.Context) structureInteractionHTTPResult {
		calls.Add(1)
		return structureInteractionHTTPResult{Status: http.StatusUnprocessableEntity, Body: []byte(`{"ok":false}`)}
	}
	for range 2 {
		result, ok := coordinator.Do(context.Background(), "failure", 32, time.Second, work)
		if !ok || result.Status != http.StatusUnprocessableEntity {
			t.Fatalf("unexpected failure result: ok=%v status=%d", ok, result.Status)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("failed result was cached; calls = %d", calls.Load())
	}
}

func TestStructureInteractionResultCoordinatorKeepsSharedWorkAfterWaiterCancellation(t *testing.T) {
	coordinator := newStructureInteractionResultCoordinator(4, 4096, time.Minute, 1, 4, 4096)
	started := make(chan struct{})
	release := make(chan struct{})
	work := func(context.Context) structureInteractionHTTPResult {
		close(started)
		<-release
		return structureInteractionHTTPResult{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}
	}
	waitContext, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		_, ok := coordinator.Do(waitContext, "detached", 32, time.Second, work)
		done <- ok
	}()
	<-started
	cancel()
	if <-done {
		t.Fatal("cancelled waiter unexpectedly received a result")
	}
	close(release)

	result, ok := coordinator.Do(context.Background(), "detached", 32, time.Second, work)
	if !ok || result.Status != http.StatusOK {
		t.Fatalf("shared work was not reusable: ok=%v status=%d", ok, result.Status)
	}
}

func TestStructureInteractionResultCoordinatorEvictsLeastRecentlyUsedEntry(t *testing.T) {
	coordinator := newStructureInteractionResultCoordinator(2, 4096, time.Minute, 1, 4, 4096)
	var calls atomic.Int32
	work := func(context.Context) structureInteractionHTTPResult {
		calls.Add(1)
		return structureInteractionHTTPResult{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}
	}
	for _, key := range []string{"a", "b", "a", "c", "b"} {
		if _, ok := coordinator.Do(context.Background(), key, 32, time.Second, work); !ok {
			t.Fatalf("key %q unavailable", key)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("work calls = %d, want 4 after LRU eviction", calls.Load())
	}
}

func TestStructureInteractionResultCoordinatorBoundsQueuedWorkAndRecoversCapacity(t *testing.T) {
	coordinator := newStructureInteractionResultCoordinator(4, 4096, time.Minute, 1, 2, 64)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	work := func(context.Context) structureInteractionHTTPResult {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return structureInteractionHTTPResult{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}
	}
	results := make(chan structureInteractionHTTPResult, 3)
	go func() {
		result, _ := coordinator.Do(context.Background(), "running", 32, time.Second, work)
		results <- result
	}()
	<-started
	go func() {
		result, _ := coordinator.Do(context.Background(), "queued", 32, time.Second, work)
		results <- result
	}()
	deadline := time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		inflight := len(coordinator.inflight)
		coordinator.mu.Unlock()
		if inflight == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued work was not admitted")
		}
		time.Sleep(time.Millisecond)
	}

	overloaded, ok := coordinator.Do(context.Background(), "rejected", 1, time.Second, work)
	if !ok || overloaded.Status != http.StatusTooManyRequests {
		t.Fatalf("capacity result: ok=%v status=%d", ok, overloaded.Status)
	}
	if calls.Load() != 1 {
		t.Fatalf("rejected work executed; calls=%d", calls.Load())
	}
	close(release)
	for range 2 {
		if result := <-results; result.Status != http.StatusOK {
			t.Fatalf("accepted work status=%d", result.Status)
		}
	}

	recovered, ok := coordinator.Do(context.Background(), "after-release", 64, time.Second, func(context.Context) structureInteractionHTTPResult {
		return structureInteractionHTTPResult{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}
	})
	if !ok || recovered.Status != http.StatusOK {
		t.Fatalf("capacity did not recover: ok=%v status=%d", ok, recovered.Status)
	}
}

func TestStructureInteractionResultCoordinatorLetsSameKeyJoinAtCapacity(t *testing.T) {
	coordinator := newStructureInteractionResultCoordinator(4, 4096, time.Minute, 1, 1, 32)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	work := func(context.Context) structureInteractionHTTPResult {
		calls.Add(1)
		close(started)
		<-release
		return structureInteractionHTTPResult{Status: http.StatusOK}
	}
	results := make(chan structureInteractionHTTPResult, 2)
	for range 2 {
		go func() {
			result, _ := coordinator.Do(context.Background(), "same", 32, time.Second, work)
			results <- result
		}()
	}
	<-started
	close(release)
	for range 2 {
		if result := <-results; result.Status != http.StatusOK {
			t.Fatalf("joined result status=%d", result.Status)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("same key work calls=%d, want 1", calls.Load())
	}
}

func TestStructureInteractionResultCoordinatorReleasesCapacityAfterQueueTimeoutAndPanic(t *testing.T) {
	coordinator := newStructureInteractionResultCoordinator(4, 4096, time.Minute, 1, 2, 64)
	started := make(chan struct{})
	release := make(chan struct{})
	first := make(chan structureInteractionHTTPResult, 1)
	go func() {
		result, _ := coordinator.Do(context.Background(), "running", 32, time.Second, func(context.Context) structureInteractionHTTPResult {
			close(started)
			<-release
			return structureInteractionHTTPResult{Status: http.StatusOK}
		})
		first <- result
	}()
	<-started
	timedOut, ok := coordinator.Do(context.Background(), "queued-timeout", 32, 10*time.Millisecond, func(context.Context) structureInteractionHTTPResult {
		return structureInteractionHTTPResult{Status: http.StatusOK}
	})
	if !ok || timedOut.Status != http.StatusGatewayTimeout {
		t.Fatalf("queued timeout: ok=%v status=%d", ok, timedOut.Status)
	}
	close(release)
	if result := <-first; result.Status != http.StatusOK {
		t.Fatalf("running result status=%d", result.Status)
	}

	panicked, ok := coordinator.Do(context.Background(), "panic", 64, time.Second, func(context.Context) structureInteractionHTTPResult {
		panic("test panic")
	})
	if !ok || panicked.Status != http.StatusInternalServerError {
		t.Fatalf("panic result: ok=%v status=%d", ok, panicked.Status)
	}
	afterPanic, ok := coordinator.Do(context.Background(), "after-panic", 64, time.Second, func(context.Context) structureInteractionHTTPResult {
		return structureInteractionHTTPResult{Status: http.StatusOK}
	})
	if !ok || afterPanic.Status != http.StatusOK {
		t.Fatalf("panic capacity did not recover: ok=%v status=%d", ok, afterPanic.Status)
	}
}

func TestStructureInteractionDiagramCacheKeyIsolatesUserFrameAndIncarnation(t *testing.T) {
	request := structureInteractionDiagramRequest{
		Content:           "ATOM\n",
		Filename:          "complex.pdb",
		LigandResidueName: "D01",
		Smiles:            "CO",
	}
	access := workspace.KernelFrameAccess{
		UserID: "user-a",
		Frame: workspace.Frame{
			ID:            "frame-a",
			IncarnationID: "incarnation-a",
			ProjectID:     "project-a",
		},
	}
	want := structureInteractionDiagramCacheKey(access, request)
	if got := structureInteractionDiagramCacheKey(access, request); got != want {
		t.Fatalf("stable cache key = %q, want %q", got, want)
	}
	mutations := []func(*workspace.KernelFrameAccess){
		func(value *workspace.KernelFrameAccess) { value.UserID = "user-b" },
		func(value *workspace.KernelFrameAccess) { value.Frame.ID = "frame-b" },
		func(value *workspace.KernelFrameAccess) { value.Frame.IncarnationID = "incarnation-b" },
	}
	for index, mutate := range mutations {
		changed := access
		mutate(&changed)
		if got := structureInteractionDiagramCacheKey(changed, request); got == want {
			t.Fatalf("mutation %d reused a cross-authority cache key", index)
		}
	}
}
