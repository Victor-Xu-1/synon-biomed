package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/persistence/workspace"
	"synon-go/internal/routinescheduler"
)

func TestRoutineOutcomeSurvivesCompletionFailureAndRestartWithoutSecondModelRequest(t *testing.T) {
	var requests atomic.Int32
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		// A real response may legitimately take longer than the old 200ms tick.
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"restart-safe result"}}],"usage":{"total_tokens":2}}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	databasePath := filepath.Join(root, "workspace.db")
	firstStore, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	t.Cleanup(func() { _ = firstStore.Close() })
	firstServer := New(Options{FileRoot: root, Workspace: firstStore})
	t.Cleanup(func() { closeRoutineServer(t, firstServer) })
	configureRoutineProvider(t, firstServer, firstStore, modelAPI.URL)
	createRoutineFixture(t, firstStore, root, "routine-restart", "run once across restart")
	firstExecutor, err := firstServer.NewRoutineExecutor(RoutineExecutorOptions{Chat: SessionRunnerChatOptions{
		RequestTimeout: 5 * time.Second, MaxAttempts: 1, MaxToolRounds: 1,
		LeaseTTL: 10 * time.Second, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
	}})
	if err != nil {
		t.Fatalf("new first executor: %v", err)
	}
	failingRepository := &failFirstRoutineCompletion{Store: firstStore, failed: make(chan struct{})}
	// This is a persistence/idempotence test, not a 200ms end-to-end latency
	// budget. Keep a bounded tick and advance only the scheduling clock across
	// lease expiry after restart instead of making real I/O race a short lease.
	const lockTTL = 40 * time.Second
	const tickTimeout = 30 * time.Second
	firstScheduler, err := routinescheduler.New(routinescheduler.Options{
		Repository: failingRepository, Executor: firstExecutor,
		LockTTL: lockTTL, TickTimeout: tickTimeout,
		ErrorBackoff: 5 * time.Millisecond,
		MinimumDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new first scheduler: %v", err)
	}
	if err := firstScheduler.Start(context.Background()); err != nil {
		t.Fatalf("start first scheduler: %v", err)
	}
	t.Cleanup(func() { stopRoutineIntegrationScheduler(t, firstScheduler) })
	select {
	case <-failingRepository.failed:
	case <-time.After(tickTimeout + 5*time.Second):
		t.Fatal("timed out waiting for the controlled completion failure")
	}
	stopRoutineIntegrationScheduler(t, firstScheduler)
	beforeRestart, err := firstStore.GetRoutine("routine-restart")
	if err != nil || beforeRestart.TickCount != 0 || beforeRestart.LockedAt == nil {
		t.Fatalf("routine before restart = %#v err=%v", beforeRestart, err)
	}
	stableEventID := routineTickStableID("outcome", routinescheduler.Tick{RoutineID: "routine-restart", Attempt: 1})
	stableEvent, found, err := firstStore.GetFrameEventByID(stableEventID)
	if err != nil || !found || stableEvent.Payload["summary"] != "restart-safe result" {
		t.Fatalf("stable outcome before restart = %#v found=%v err=%v", stableEvent, found, err)
	}
	closeRoutineServer(t, firstServer)
	if err := firstStore.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	secondStore, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer secondStore.Close()
	secondServer := New(Options{FileRoot: root, Workspace: secondStore})
	defer closeRoutineServer(t, secondServer)
	secondExecutor, err := secondServer.NewRoutineExecutor(RoutineExecutorOptions{Chat: SessionRunnerChatOptions{
		RequestTimeout: 5 * time.Second, MaxAttempts: 1, MaxToolRounds: 1,
		LeaseTTL: 10 * time.Second, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
	}})
	if err != nil {
		t.Fatalf("new second executor: %v", err)
	}
	secondScheduler, err := routinescheduler.New(routinescheduler.Options{
		Repository: secondStore, Executor: secondExecutor,
		Clock:   routineRestartClock{offset: beforeRestart.LockedAt.Add(lockTTL + time.Second).Sub(time.Now())},
		LockTTL: lockTTL, TickTimeout: tickTimeout,
		ErrorBackoff: 5 * time.Millisecond,
		MinimumDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new second scheduler: %v", err)
	}
	if err := secondScheduler.Start(context.Background()); err != nil {
		t.Fatalf("start second scheduler: %v", err)
	}
	defer stopRoutineIntegrationScheduler(t, secondScheduler)
	routineEventually(t, tickTimeout, func() bool {
		routine, routineErr := secondStore.GetRoutine("routine-restart")
		return routineErr == nil && routine.TickCount == 1 && routine.LastOKAt != nil && routine.LastResults == "restart-safe result"
	})
	if requests.Load() != 1 {
		t.Fatalf("model requests across completion failure and restart = %d, want 1", requests.Load())
	}
}

// The offset is immutable once the restarted scheduler starts. Timer waits
// remain real; only the durable scheduling time crosses the abandoned lease.
type routineRestartClock struct{ offset time.Duration }

func (c routineRestartClock) Now() time.Time { return time.Now().Add(c.offset) }

func (routineRestartClock) NewTimer(delay time.Duration) routinescheduler.Timer {
	return routineRestartTimer{Timer: time.NewTimer(delay)}
}

type routineRestartTimer struct{ *time.Timer }

func (t routineRestartTimer) C() <-chan time.Time { return t.Timer.C }

type failFirstRoutineCompletion struct {
	*workspace.Store
	failed chan struct{}
	used   atomic.Bool
}

func (r *failFirstRoutineCompletion) CompleteRoutineTick(id string, at time.Time, successful bool, result string) error {
	if r.used.CompareAndSwap(false, true) {
		close(r.failed)
		return errors.New("controlled completion failure")
	}
	return r.Store.CompleteRoutineTick(id, at, successful, result)
}
