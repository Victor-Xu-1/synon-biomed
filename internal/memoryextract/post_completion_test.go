package memoryextract

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkspacePostCompletionTasksDrainAndRetainTimedOutWork(t *testing.T) {
	tasks := NewPostCompletionTasks()
	release := make(chan struct{})
	blockedResult := tasks.Go(func() error {
		<-release
		return nil
	})
	quickResult := tasks.Go(func() error { return nil })
	if err := <-quickResult; err != nil {
		t.Fatal(err)
	}

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	timedOut := tasks.Drain(timeoutCtx)
	if timedOut.Started != 1 || !timedOut.TimedOut || timedOut.Remaining != 1 || tasks.Active() != 1 {
		t.Fatalf("timed-out drain = %#v active=%d", timedOut, tasks.Active())
	}
	close(release)
	if err := <-blockedResult; err != nil {
		t.Fatal(err)
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
	defer drainCancel()
	drained := tasks.Drain(drainCtx)
	if drained.TimedOut || drained.Remaining != 0 || tasks.Active() != 0 {
		t.Fatalf("completed drain = %#v active=%d", drained, tasks.Active())
	}
}

func TestWorkspacePostCompletionTasksReportErrorsAndPanics(t *testing.T) {
	tasks := NewPostCompletionTasks()
	want := errors.New("extract failed")
	if got := <-tasks.Go(func() error { return want }); !errors.Is(got, want) {
		t.Fatalf("task error = %v", got)
	}
	if got := <-tasks.Go(func() error { panic("boom") }); got == nil || !strings.Contains(got.Error(), "panicked: boom") {
		t.Fatalf("panic error = %v", got)
	}
	if tasks.Active() != 0 {
		t.Fatalf("active tasks = %d", tasks.Active())
	}
}
