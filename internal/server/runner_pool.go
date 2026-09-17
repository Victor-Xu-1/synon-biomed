package server

import (
	"context"

	"errors"
	"fmt"
	"log"

	"sync"
	"time"
)

func (s *Server) RunSessionRunnerChatLoop(ctx context.Context, options SessionRunnerChatOptions) error {
	options = normalizeSessionRunnerChatOptions(options)
	return s.runSessionRunnerChatLoop(ctx, options, s.RunSessionRunnerChatOnce)
}

type sessionRunnerChatCycle func(context.Context, SessionRunnerChatOptions) (SessionRunnerCycleResult, error)

func (s *Server) runSessionRunnerChatLoop(
	ctx context.Context,
	options SessionRunnerChatOptions,
	runCycle sessionRunnerChatCycle,
) error {
	idlePollInterval := options.PollInterval
	contentionDelay := time.Duration(0)

	for {
		if s.isDraining() {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		var committedWork <-chan struct{}
		if s.workspaceStore != nil {
			// Capture the generation before claiming so a commit racing with the
			// empty claim closes this exact channel instead of becoming a lost wake.
			committedWork = s.workspaceStore.OutboxWake()
		}
		kernelWork := s.kernelRuntimeWake()
		result, err := runCycle(ctx, options)
		if err != nil {
			if context.Cause(ctx) != nil {
				return nil
			}
			if result.Claimed || isTransientSQLiteContention(err) {
				// Admission contention and task-owned failures stay local to this
				// worker. Re-enter the durable scheduler, never replay a side effect;
				// use bounded, cancellable backoff while sibling tasks continue.
				contentionDelay = nextRunnerIdlePollInterval(contentionDelay, max(options.PollInterval, 50*time.Millisecond))
				log.Printf("runner_cycle_retry runner=%q claimed=%t error_type=%T retry_after=%s", options.RunnerID, result.Claimed, err, contentionDelay)
				timer := time.NewTimer(contentionDelay)
				select {
				case <-ctx.Done():
					stopRunnerIdleTimer(timer)
					return nil
				case <-timer.C:
				}
				continue
			}
			return err
		}
		contentionDelay = 0
		if result.Claimed {
			idlePollInterval = options.PollInterval
			continue
		}
		timer := time.NewTimer(idlePollInterval)
		select {
		case <-ctx.Done():
			stopRunnerIdleTimer(timer)
			return nil
		case <-committedWork:
			stopRunnerIdleTimer(timer)
			idlePollInterval = options.PollInterval
		case <-kernelWork:
			stopRunnerIdleTimer(timer)
			idlePollInterval = options.PollInterval
		case <-timer.C:
			idlePollInterval = nextRunnerIdlePollInterval(idlePollInterval, options.PollInterval)
		}
	}
}

// RunSessionRunnerChatPool lets independent runnable conversations progress in
// parallel while the repository's transactional claim remains the single
// scheduling authority. Each worker has a distinct durable runner identity;
// one worker failure cancels and restarts the cohort through the process
// supervisor instead of leaving a partially degraded pool behind.
func (s *Server) RunSessionRunnerChatPool(ctx context.Context, options SessionRunnerChatOptions, workers int) error {
	return s.runSessionRunnerChatPool(ctx, options, workers, s.RunSessionRunnerChatOnce)
}

func (s *Server) runSessionRunnerChatPool(ctx context.Context, options SessionRunnerChatOptions, workers int, cycle sessionRunnerChatCycle) error {
	if workers <= 0 {
		return errors.New("session runner chat worker count must be positive")
	}
	if workers == 1 {
		return s.runSessionRunnerChatLoop(ctx, normalizeSessionRunnerChatOptions(options), cycle)
	}

	options = normalizeSessionRunnerChatOptions(options)
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	errCh := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for workerIndex := 0; workerIndex < workers; workerIndex++ {
		workerOptions, initialDelay := sessionRunnerChatPoolWorkerTiming(options, workers, workerIndex)
		go func(workerOptions SessionRunnerChatOptions, initialDelay time.Duration) {
			defer group.Done()
			if initialDelay > 0 {
				timer := time.NewTimer(initialDelay)
				defer timer.Stop()
				select {
				case <-runCtx.Done():
					errCh <- nil
					return
				case <-timer.C:
				}
			}
			errCh <- s.runSessionRunnerChatLoop(runCtx, workerOptions, cycle)
		}(workerOptions, initialDelay)
	}

	var result error
	select {
	case <-ctx.Done():
	case result = <-errCh:
	}
	if result != nil {
		cancel(newSessionRunnerInfrastructureInterruption(sessionRunnerSupervisorInterruptedReasonCode, result))
	} else {
		cancel(context.Cause(ctx))
	}
	group.Wait()
	close(errCh)
	if result == nil {
		for workerErr := range errCh {
			if workerErr != nil {
				result = workerErr
				break
			}
		}
	}
	return result
}

// sessionRunnerChatPoolWorkerTiming keeps each worker responsive at the base
// cadence. Startup staggering is bounded to one base interval and must not
// turn a larger pool into a slower scheduler for newly committed work.
func sessionRunnerChatPoolWorkerTiming(options SessionRunnerChatOptions, workers, workerIndex int) (SessionRunnerChatOptions, time.Duration) {
	workerOptions := options
	workerOptions.RunnerID = fmt.Sprintf("%s/chat-%d", options.RunnerID, workerIndex+1)
	workerOptions.PollInterval = options.PollInterval
	initialDelay := options.PollInterval * time.Duration(workerIndex) / time.Duration(workers)
	return workerOptions, initialDelay
}
