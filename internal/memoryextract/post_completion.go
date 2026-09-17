package memoryextract

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PostCompletionTasks tracks best-effort extraction work started after a root
// turn completes. Drain snapshots the currently tracked tasks, matching Claude
// Science shutdown behavior: it waits up to the caller's deadline without
// cancelling work that is still finishing.
type PostCompletionTasks struct {
	mu     sync.Mutex
	nextID uint64
	tasks  map[uint64]<-chan struct{}
}

type PostCompletionDrainResult struct {
	Started   int
	Remaining int
	TimedOut  bool
	Elapsed   time.Duration
}

func NewPostCompletionTasks() *PostCompletionTasks {
	return &PostCompletionTasks{tasks: make(map[uint64]<-chan struct{})}
}

func (tasks *PostCompletionTasks) Go(run func() error) <-chan error {
	result := make(chan error, 1)
	done := make(chan struct{})
	if tasks == nil {
		result <- fmt.Errorf("memory post-completion task tracker is unavailable")
		close(result)
		close(done)
		return result
	}
	tasks.mu.Lock()
	if tasks.tasks == nil {
		tasks.tasks = make(map[uint64]<-chan struct{})
	}
	tasks.nextID++
	id := tasks.nextID
	tasks.tasks[id] = done
	tasks.mu.Unlock()

	go func() {
		var taskErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				taskErr = fmt.Errorf("memory post-completion task panicked: %v", recovered)
			}
			tasks.mu.Lock()
			delete(tasks.tasks, id)
			tasks.mu.Unlock()
			close(done)
			if taskErr != nil {
				result <- taskErr
			}
			close(result)
		}()
		if run == nil {
			taskErr = fmt.Errorf("memory post-completion task is nil")
			return
		}
		taskErr = run()
	}()
	return result
}

func (tasks *PostCompletionTasks) Active() int {
	if tasks == nil {
		return 0
	}
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	return len(tasks.tasks)
}

func (tasks *PostCompletionTasks) Drain(ctx context.Context) PostCompletionDrainResult {
	startedAt := time.Now()
	result := PostCompletionDrainResult{}
	if tasks == nil {
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tasks.mu.Lock()
	snapshot := make([]<-chan struct{}, 0, len(tasks.tasks))
	for _, done := range tasks.tasks {
		snapshot = append(snapshot, done)
	}
	tasks.mu.Unlock()
	result.Started = len(snapshot)
	for _, done := range snapshot {
		select {
		case <-done:
		case <-ctx.Done():
			result.Remaining = tasks.Active()
			result.TimedOut = true
			result.Elapsed = time.Since(startedAt)
			return result
		}
	}
	result.Remaining = tasks.Active()
	result.Elapsed = time.Since(startedAt)
	return result
}
