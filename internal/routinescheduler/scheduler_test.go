package routinescheduler

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/persistence/workspace"
)

type executorFunc func(context.Context, Tick) (Result, error)

func (f executorFunc) ExecuteRoutineTick(ctx context.Context, tick Tick) (Result, error) {
	return f(ctx, tick)
}

func TestTwoSchedulersDoNotExecuteTheSameRoutine(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	first := openStore(t, databasePath)
	defer first.Close()
	second := openStore(t, databasePath)
	defer second.Close()
	now := time.Now().UTC()
	createDueRoutine(t, first, "shared", now)

	started := make(chan Tick, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	executor := executorFunc(func(ctx context.Context, tick Tick) (Result, error) {
		calls.Add(1)
		started <- tick
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-release:
			return Result{Summary: "completed once"}, nil
		}
	})
	one := newTestScheduler(t, first, executor, nil)
	two := newTestScheduler(t, second, executor, nil)
	startScheduler(t, one)
	startScheduler(t, two)
	tick := receive(t, started)
	if tick.RootFrameID != "frame-shared" || tick.Instruction != "instruction-shared" || tick.Attempt != 1 {
		t.Fatalf("tick contract = %#v", tick)
	}
	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("executor calls while lease held = %d, want 1", got)
	}
	close(release)
	waitFor(t, time.Second, func() bool {
		routine, err := first.GetRoutine("routine-shared")
		return err == nil && routine.TickCount == 1 && routine.LastResults == "completed once"
	})
	stopScheduler(t, one)
	stopScheduler(t, two)
}

func TestUnboundedTickRenewsClaimPastOriginalLease(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	first := openStore(t, databasePath)
	defer first.Close()
	second := openStore(t, databasePath)
	defer second.Close()
	createDueRoutine(t, first, "unbounded", time.Now().UTC())

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	executor := executorFunc(func(ctx context.Context, tick Tick) (Result, error) {
		if _, hasDeadline := ctx.Deadline(); hasDeadline || !tick.Deadline.IsZero() {
			t.Errorf("unbounded tick unexpectedly has a deadline: %#v", tick)
		}
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-release:
			return Result{Summary: "unbounded completed"}, nil
		}
	})
	newScheduler := func(repository Repository) *Scheduler {
		scheduler, err := New(Options{
			Repository: repository, Executor: executor,
			LockTTL: 60 * time.Millisecond, TickTimeout: 0, UnboundedTicks: true,
			ErrorBackoff: 5 * time.Millisecond,
			MinimumDelay: time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		return scheduler
	}
	one, two := newScheduler(first), newScheduler(second)
	startScheduler(t, one)
	startScheduler(t, two)
	receiveSignal(t, started)
	time.Sleep(180 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("executor calls after original lease elapsed = %d, want 1", got)
	}
	close(release)
	waitFor(t, time.Second, func() bool {
		routine, err := first.GetRoutine("routine-unbounded")
		return err == nil && routine.TickCount == 1 && routine.LockedAt == nil && routine.LastResults == "unbounded completed"
	})
	stopScheduler(t, one)
	stopScheduler(t, two)
}

func TestDeferredTickKeepsClaimWithoutRecordingFailureOrCompletion(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "workspace.db"))
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	createDueRoutine(t, store, "deferred", now)
	claim, claimed, err := store.ClaimNextDueRoutine(now, time.Second)
	if err != nil || !claimed {
		t.Fatalf("claim deferred routine: claimed=%v err=%v", claimed, err)
	}
	scheduler := newTestScheduler(t, store, executorFunc(func(_ context.Context, tick Tick) (Result, error) {
		if tick.Attempt != 1 {
			t.Fatalf("deferred tick attempt = %d", tick.Attempt)
		}
		return Result{Summary: "awaiting user response", Deferred: true}, nil
	}), nil)

	scheduler.execute(context.Background(), claim, now)
	routine, err := store.GetRoutine("routine-deferred")
	if err != nil {
		t.Fatal(err)
	}
	if routine.TickCount != 0 || routine.LockedAt == nil || routine.LastResults != "" || routine.LastOKAt != nil || routine.IdleStreak != 0 {
		t.Fatalf("deferred routine was incorrectly completed: %#v", routine)
	}
}

func TestExpiredRoutineLeaseIsRecoveredAfterCrash(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	crashed := openStore(t, databasePath)
	defer crashed.Close()
	recovered := openStore(t, databasePath)
	defer recovered.Close()
	claimedAt := time.Now().UTC().Truncate(time.Millisecond)
	createDueRoutine(t, crashed, "recover", claimedAt)
	if _, claimed, err := crashed.ClaimNextDueRoutine(claimedAt, time.Second); err != nil || !claimed {
		t.Fatalf("simulate crashed claim: claimed=%v err=%v", claimed, err)
	}

	clock := newManualClock(claimedAt.Add(time.Second + time.Millisecond))
	executed := make(chan Tick, 1)
	scheduler := newTestScheduler(t, recovered, executorFunc(func(_ context.Context, tick Tick) (Result, error) {
		executed <- tick
		return Result{Summary: "recovered"}, nil
	}), clock)
	startScheduler(t, scheduler)
	if tick := receive(t, executed); tick.RoutineID != "routine-recover" {
		t.Fatalf("recovered tick = %#v", tick)
	}
	waitFor(t, time.Second, func() bool {
		routine, err := recovered.GetRoutine("routine-recover")
		return err == nil && routine.TickCount == 1 && routine.LockedAt == nil && routine.LastResults == "recovered"
	})
	stopScheduler(t, scheduler)
}

func TestFailedTickPersistsAndRunsAtNextSchedule(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store := openStore(t, databasePath)
	defer store.Close()
	initial := time.Now().UTC().Truncate(time.Millisecond)
	createDueRoutine(t, store, "retry", initial)
	clock := newManualClock(initial)

	var calls atomic.Int32
	executed := make(chan Tick, 2)
	scheduler := newTestScheduler(t, store, executorFunc(func(_ context.Context, tick Tick) (Result, error) {
		executed <- tick
		if calls.Add(1) == 1 {
			return Result{Summary: "first attempt"}, errors.New("controlled failure")
		}
		return Result{Summary: "second attempt"}, nil
	}), clock)
	startScheduler(t, scheduler)
	firstTick := receive(t, executed)
	if firstTick.Attempt != 1 || firstTick.Deadline.IsZero() {
		t.Fatalf("first tick = %#v", firstTick)
	}
	waitFor(t, time.Second, func() bool {
		routine, err := store.GetRoutine("routine-retry")
		return err == nil && routine.TickCount == 1 && routine.IdleStreak == 1 && routine.LastOKAt == nil && routine.LastResults == "first attempt\nerror: controlled failure"
	})
	clock.Advance(time.Minute)
	scheduler.Wake()
	secondTick := receive(t, executed)
	if secondTick.Attempt != 2 {
		t.Fatalf("second tick = %#v", secondTick)
	}
	waitFor(t, time.Second, func() bool {
		routine, err := store.GetRoutine("routine-retry")
		return err == nil && routine.TickCount == 2 && routine.IdleStreak == 0 && routine.LastOKAt != nil && routine.LastResults == "second attempt"
	})
	stopScheduler(t, scheduler)
}

func TestStopCancelsTickAndDoesNotLeakSchedulerGoroutine(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store := openStore(t, databasePath)
	defer store.Close()
	createDueRoutine(t, store, "cancel", time.Now().UTC())
	started := make(chan struct{})
	exited := make(chan struct{})
	scheduler := newTestScheduler(t, store, executorFunc(func(ctx context.Context, tick Tick) (Result, error) {
		if _, ok := ctx.Deadline(); !ok || tick.Deadline.IsZero() {
			t.Error("executor context has no deadline")
		}
		close(started)
		<-ctx.Done()
		close(exited)
		return Result{}, ctx.Err()
	}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	if err := scheduler.Start(ctx); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second start error = %v", err)
	}
	receiveSignal(t, started)
	cancel()
	stopScheduler(t, scheduler)
	receiveSignal(t, exited)
	waitFor(t, time.Second, func() bool {
		routine, err := store.GetRoutine("routine-cancel")
		return err == nil && routine.TickCount == 1 && routine.IdleStreak == 1 && routine.LockedAt == nil
	})

	// The lifecycle state is cleared after parent cancellation, so restart does
	// not retain the previous goroutine or cancellation function.
	restartCtx, restartCancel := context.WithCancel(context.Background())
	if err := scheduler.Start(restartCtx); err != nil {
		t.Fatalf("restart scheduler: %v", err)
	}
	restartCancel()
	stopScheduler(t, scheduler)
}

func TestTickDeadlinePersistsTimeoutFailure(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store := openStore(t, databasePath)
	defer store.Close()
	createDueRoutine(t, store, "deadline", time.Now().UTC())
	exited := make(chan struct{})
	scheduler, err := New(Options{
		Repository: store,
		Executor: executorFunc(func(ctx context.Context, tick Tick) (Result, error) {
			deadline, ok := ctx.Deadline()
			if !ok || !deadline.Equal(tick.Deadline) {
				t.Errorf("deadline contract: context=%v tick=%v ok=%v", deadline, tick.Deadline, ok)
			}
			<-ctx.Done()
			close(exited)
			return Result{Summary: "deadline reached"}, ctx.Err()
		}),
		LockTTL: 500 * time.Millisecond, TickTimeout: 20 * time.Millisecond,
		ErrorBackoff: 5 * time.Millisecond,
		MinimumDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	startScheduler(t, scheduler)
	receiveSignal(t, exited)
	waitFor(t, time.Second, func() bool {
		routine, err := store.GetRoutine("routine-deadline")
		return err == nil && routine.TickCount == 1 && routine.IdleStreak == 1 && routine.LockedAt == nil && routine.LastResults == "deadline reached\nerror: context deadline exceeded"
	})
	stopScheduler(t, scheduler)
}

func TestFutureRoutineWaitsWithoutBusyPolling(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store := openStore(t, databasePath)
	defer store.Close()
	createRoutineAt(t, store, "future", time.Now().UTC().Add(time.Hour))
	repository := &countingRepository{Store: store, claimed: make(chan struct{}, 1)}
	scheduler, err := New(Options{
		Repository: repository,
		Executor: executorFunc(func(context.Context, Tick) (Result, error) {
			t.Fatal("future routine must not execute")
			return Result{}, nil
		}),
		LockTTL: time.Second, TickTimeout: 500 * time.Millisecond,
		ErrorBackoff: 5 * time.Millisecond,
		MinimumDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	startScheduler(t, scheduler)
	receiveSignal(t, repository.claimed)
	// This window exceeds the former idle interval. A future durable wake time
	// must be honored directly instead of being capped by a polling interval.
	time.Sleep(350 * time.Millisecond)
	if calls := repository.calls.Load(); calls != 1 {
		t.Fatalf("claim calls in idle window = %d, want 1", calls)
	}
	stopScheduler(t, scheduler)
}

func newTestScheduler(t *testing.T, repository Repository, executor Executor, clock Clock) *Scheduler {
	t.Helper()
	options := Options{
		Repository: repository, Executor: executor, Clock: clock,
		LockTTL: time.Second, TickTimeout: 500 * time.Millisecond,
		ErrorBackoff: 5 * time.Millisecond,
		MinimumDelay: time.Millisecond,
	}
	scheduler, err := New(options)
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	return scheduler
}

func openStore(t *testing.T, path string) *workspace.Store {
	t.Helper()
	store, err := workspace.Open(path)
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	return store
}

func createDueRoutine(t *testing.T, store *workspace.Store, suffix string, now time.Time) {
	createRoutineAt(t, store, suffix, now.Add(-time.Second))
}

func createRoutineAt(t *testing.T, store *workspace.Store, suffix string, nextDue time.Time) {
	t.Helper()
	project, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-" + suffix, UserID: "user-1", Name: "Routine " + suffix})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	root, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-" + suffix, ProjectID: project.ID, AgentName: "planner", Status: "running", ConversationType: "task"})
	if err != nil {
		t.Fatalf("create frame: %v", err)
	}
	_, err = store.CreateRoutine(workspace.CreateRoutineInput{
		ID: "routine-" + suffix, RootFrameID: root.ID, OwnerUserID: "user-1",
		OnTick: "instruction-" + suffix, EveryMinutes: 1, Enabled: true,
		NextDue: nextDue,
	})
	if err != nil {
		t.Fatalf("create routine: %v", err)
	}
}

type countingRepository struct {
	*workspace.Store
	calls   atomic.Int32
	claimed chan struct{}
}

func (r *countingRepository) ClaimNextDueRoutine(now time.Time, lockTTL time.Duration) (workspace.Routine, bool, error) {
	r.calls.Add(1)
	select {
	case r.claimed <- struct{}{}:
	default:
	}
	return r.Store.ClaimNextDueRoutine(now, lockTTL)
}

func startScheduler(t *testing.T, scheduler *Scheduler) {
	t.Helper()
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
}

func stopScheduler(t *testing.T, scheduler *Scheduler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.Stop(ctx); err != nil {
		t.Fatalf("stop scheduler: %v", err)
	}
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel value")
		var zero T
		return zero
	}
}

func receiveSignal(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	receive(t, channel)
}

func waitFor(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*manualTimer]time.Time
}

func newManualClock(now time.Time) *manualClock {
	return &manualClock{now: now, timers: make(map[*manualTimer]time.Time)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) NewTimer(delay time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{clock: c, channel: make(chan time.Time, 1)}
	c.timers[timer] = c.now.Add(delay)
	return timer
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
	for timer, due := range c.timers {
		if !due.After(c.now) {
			timer.channel <- c.now
			delete(c.timers, timer)
		}
	}
}

type manualTimer struct {
	clock   *manualClock
	channel chan time.Time
}

func (t *manualTimer) C() <-chan time.Time { return t.channel }

func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if _, exists := t.clock.timers[t]; !exists {
		return false
	}
	delete(t.clock.timers, t)
	return true
}
