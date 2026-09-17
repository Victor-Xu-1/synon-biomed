package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeSupervisorRestartsTransientFailureAndKeepsProcessAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	var mu sync.Mutex
	reports := make([]error, 0, 8)
	healthy := make(chan struct{})
	var healthyOnce sync.Once
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRuntimeSupervisor(ctx, "runner-chat", func(runCtx context.Context, ready func()) error {
			switch calls.Add(1) {
			case 1, 2:
				return errors.New("transient")
			default:
				ready()
				<-runCtx.Done()
				return runCtx.Err()
			}
		}, func(_ string, err error) {
			mu.Lock()
			reports = append(reports, err)
			mu.Unlock()
			if err == nil {
				healthyOnce.Do(func() { close(healthy) })
			}
		}, nil, runtimeSupervisorPolicy{
			InitialBackoff: time.Millisecond,
			MaxBackoff:     2 * time.Millisecond,
			StableWindow:   time.Hour,
			Jitter:         func(delay time.Duration) time.Duration { return delay },
		})
	}()
	select {
	case <-healthy:
	case <-time.After(time.Second):
		t.Fatalf("runtime did not become healthy; calls=%d", calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop with parent context")
	}
	mu.Lock()
	defer mu.Unlock()
	failures := 0
	starting := 0
	for _, reportErr := range reports {
		switch {
		case errors.Is(reportErr, errRuntimeComponentStarting):
			starting++
		case reportErr != nil:
			failures++
		}
	}
	if failures != 2 || starting != 3 || reports[len(reports)-1] != nil {
		t.Fatalf("reports=%#v", reports)
	}
}

func TestRuntimeSupervisorRequiresExplicitReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reports := make(chan error, 4)
	releaseReady := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRuntimeSupervisor(ctx, "runner-chat", func(runCtx context.Context, ready func()) error {
			<-releaseReady
			ready()
			<-runCtx.Done()
			return runCtx.Err()
		}, func(_ string, err error) {
			reports <- err
		}, nil, runtimeSupervisorPolicy{
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
			StableWindow:   time.Hour,
			Jitter:         func(delay time.Duration) time.Duration { return delay },
		})
	}()
	if reportErr := <-reports; !errors.Is(reportErr, errRuntimeComponentStarting) {
		cancel()
		t.Fatalf("first report=%v", reportErr)
	}
	select {
	case reportErr := <-reports:
		cancel()
		t.Fatalf("reported healthy without ready signal: %v", reportErr)
	default:
	}
	close(releaseReady)
	select {
	case reportErr := <-reports:
		if reportErr != nil {
			cancel()
			t.Fatalf("readiness report=%v", reportErr)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("readiness report did not arrive")
	}
	cancel()
	<-done
}

func TestRuntimeSupervisorTreatsIntentionalDrainAsStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	var stopping atomic.Bool
	started := make(chan struct{})
	release := make(chan struct{})
	reports := make(chan error, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRuntimeSupervisor(ctx, "runner-chat", func(context.Context, func()) error {
			calls.Add(1)
			close(started)
			<-release
			return nil
		}, func(_ string, err error) {
			reports <- err
		}, stopping.Load, runtimeSupervisorPolicy{
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
			StableWindow:   time.Hour,
			Jitter:         func(delay time.Duration) time.Duration { return delay },
		})
	}()
	<-started
	if reportErr := <-reports; !errors.Is(reportErr, errRuntimeComponentStarting) {
		t.Fatalf("first report=%v", reportErr)
	}
	stopping.Store(true)
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor restarted during intentional drain")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	select {
	case reportErr := <-reports:
		t.Fatalf("intentional drain reported failure: %v", reportErr)
	default:
	}
}
