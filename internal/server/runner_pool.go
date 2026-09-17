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
			if result.Claimed {
				// One conversation is an isolation boundary. A task-local failure
				// must never cancel the shared worker cohort and consequently abort
				// unrelated long-running conversations. RunSessionRunnerChatOnce
				// owns settlement/recovery for the claimed task; the worker remains
				// available for the next durable claim.
				log.Printf("isolated claimed session runner cycle failure session=%q runner=%q error_type=%T",
					result.SessionID, result.RunnerID, err)
				idlePollInterval = options.PollInterval
				continue
			}
			return err
		}
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
	if workers <= 0 {
		return errors.New("session runner chat worker count must be positive")
	}
	if workers == 1 {
		return s.RunSessionRunnerChatLoop(ctx, options)
	}

	options = normalizeSessionRunnerChatOptions(options)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

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
			errCh <- s.RunSessionRunnerChatLoop(runCtx, workerOptions)
		}(workerOptions, initialDelay)
	}

	var result error
	select {
	case <-ctx.Done():
	case result = <-errCh:
	}
	cancel()
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
