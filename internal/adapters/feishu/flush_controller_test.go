package feishu

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlushControllerReadyGateAndComplete(t *testing.T) {
	var count atomic.Int32
	controller := NewFlushController(func() error {
		count.Add(1)
		return nil
	})

	controller.Flush()
	controller.ThrottledUpdate(10 * time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if count.Load() != 0 {
		t.Fatalf("flush count before ready = %d", count.Load())
	}

	controller.SetCardMessageReady(true)
	controller.Flush()
	if count.Load() != 1 {
		t.Fatalf("flush count after ready = %d", count.Load())
	}
	controller.Complete()
	controller.Flush()
	if count.Load() != 1 {
		t.Fatalf("flush count after complete = %d", count.Load())
	}
}

func TestFlushControllerThrottledUpdate(t *testing.T) {
	var count atomic.Int32
	controller := NewFlushController(func() error {
		count.Add(1)
		return nil
	})
	controller.SetCardMessageReady(true)

	controller.ThrottledUpdate(60 * time.Millisecond)
	if count.Load() != 0 {
		t.Fatalf("immediate count = %d", count.Load())
	}
	time.Sleep(90 * time.Millisecond)
	if count.Load() != 1 {
		t.Fatalf("delayed count = %d", count.Load())
	}

	controller.ThrottledUpdate(10 * time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if count.Load() != 2 {
		t.Fatalf("second count = %d", count.Load())
	}
}

func TestFlushControllerReusesPendingTimer(t *testing.T) {
	var count atomic.Int32
	controller := NewFlushController(func() error {
		count.Add(1)
		return nil
	})
	controller.SetCardMessageReady(true)

	for range 4 {
		controller.ThrottledUpdate(80 * time.Millisecond)
	}
	time.Sleep(120 * time.Millisecond)
	if count.Load() != 1 {
		t.Fatalf("flush count after repeated throttled updates = %d", count.Load())
	}
}

func TestFlushControllerLongGapBatchesFirstFlush(t *testing.T) {
	var count atomic.Int32
	start := time.Now()
	flushedAfter := make(chan time.Duration, 1)
	controller := NewFlushController(func() error {
		count.Add(1)
		flushedAfter <- time.Since(start)
		return nil
	})
	controller.SetCardMessageReady(true)

	time.Sleep(LongGapThreshold + 50*time.Millisecond)
	calledAfter := time.Since(start)
	controller.ThrottledUpdate(CardKitThrottle)
	if count.Load() != 0 {
		t.Fatalf("flush count during long-gap batch delay = %d", count.Load())
	}

	var delay time.Duration
	select {
	case flushedAt := <-flushedAfter:
		delay = flushedAt - calledAfter
	case <-time.After(BatchAfterGapDuration + 500*time.Millisecond):
		t.Fatal("timed out waiting for long-gap flush")
	}
	if count.Load() != 1 {
		t.Fatalf("flush count after long-gap batch delay = %d", count.Load())
	}
	if delay < BatchAfterGapDuration-40*time.Millisecond {
		t.Fatalf("long-gap flush delay = %s, want at least %s", delay, BatchAfterGapDuration-40*time.Millisecond)
	}
}

func TestFlushControllerCompleteCancelsPendingFlush(t *testing.T) {
	var count atomic.Int32
	controller := NewFlushController(func() error {
		count.Add(1)
		return nil
	})
	controller.SetCardMessageReady(true)

	controller.ThrottledUpdate(80 * time.Millisecond)
	controller.Complete()
	time.Sleep(120 * time.Millisecond)
	if count.Load() != 0 {
		t.Fatalf("flush count after complete canceled pending timer = %d", count.Load())
	}
}

func TestFlushControllerReflushesAfterConcurrentRequest(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var count atomic.Int32
	controller := NewFlushController(func() error {
		count.Add(1)
		started <- struct{}{}
		<-release
		return nil
	})
	controller.SetCardMessageReady(true)

	go controller.Flush()
	<-started
	controller.Flush()
	close(release)
	controller.WaitForFlush()

	deadline := time.Now().Add(500 * time.Millisecond)
	for count.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count.Load() != 2 {
		t.Fatalf("flush count = %d", count.Load())
	}
}

func TestFlushControllerReturnsFlushError(t *testing.T) {
	want := errors.New("cardkit rate limited")
	controller := NewFlushController(func() error {
		return want
	})
	controller.SetCardMessageReady(true)

	if err := controller.Flush(); !errors.Is(err, want) {
		t.Fatalf("Flush error = %v, want %v", err, want)
	}
}

func TestFlushControllerWaitForFlushReturnsAfterActiveFlush(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	controller := NewFlushController(func() error {
		started <- struct{}{}
		<-release
		return nil
	})
	controller.SetCardMessageReady(true)

	go controller.Flush()
	<-started
	done := make(chan struct{})
	go func() {
		controller.WaitForFlush()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("wait returned before active flush finished")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("wait did not return after active flush finished")
	}
}

func TestFlushControllerThrottleConstants(t *testing.T) {
	if CardKitThrottle != 500*time.Millisecond {
		t.Fatalf("CardKitThrottle = %s", CardKitThrottle)
	}
	if PatchThrottle != 1500*time.Millisecond {
		t.Fatalf("PatchThrottle = %s", PatchThrottle)
	}
	if LongGapThreshold != 2*time.Second {
		t.Fatalf("LongGapThreshold = %s", LongGapThreshold)
	}
	if BatchAfterGapDuration != 300*time.Millisecond {
		t.Fatalf("BatchAfterGapDuration = %s", BatchAfterGapDuration)
	}
}
