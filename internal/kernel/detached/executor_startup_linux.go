//go:build linux

package detached

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

var errStartupStillOwned = errors.New("detached kernel startup still has live or unknown process ownership")

// ReconcileStartups runs in the existing execution recovery supervisor, not a
// second launch loop. It settles early process death even if the task has been
// cancelled and will never call EnsureSession again.
func (b *Backend) ReconcileStartups(ctx context.Context) error {
	if b == nil || b.Store == nil || b.Launcher == nil {
		return errors.New("startup recovery authority unavailable")
	}
	b.startupRecoveryMu.Lock()
	defer b.startupRecoveryMu.Unlock()
	page, err := b.Store.ListStartingKernelBackends(ctx, b.startupRecoveryCursor, 16)
	if err != nil {
		return err
	}
	if len(page) == 0 {
		b.startupRecoveryCursor = ""
		return nil
	}
	window, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var failures []error
	for _, backend := range page {
		b.startupRecoveryCursor = backend.BackendID
		b.mu.Lock()
		timeout := b.StartTimeout
		b.mu.Unlock()
		if timeout <= 0 {
			timeout = defaultBackendStartTimeout
		}
		if time.Since(backend.UpdatedAt) < timeout {
			continue
		}
		if err := b.settleExitedStartup(window, backend); err != nil && !errors.Is(err, errStartupStillOwned) && !errors.Is(err, workspace.ErrKernelExecutionBackendStale) {
			failures = append(failures, err)
		}
		if window.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}

// ClaimStartup must precede runtime discovery as well as worker creation.
// The process identity is a liveness witness, not a readiness claim.
func (e *Executor) ClaimStartup(ctx context.Context) error {
	ticks, err := kernelruntime.CurrentProcessStartTicks()
	if err != nil {
		return err
	}
	return e.Store.ClaimKernelExecutorStartup(ctx, workspace.ActivateKernelExecutionBackendInput{
		BackendID: e.BackendID, BackendGeneration: e.BackendGeneration, ExecutorInstanceID: e.ExecutorInstanceID,
		ExecutorPID: int64(os.Getpid()), ExecutorPIDStartTicks: ticks,
	})
}

func (e *Executor) RecordStartupFailure(stage string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := e.Store.FailKernelExecutorStartup(ctx, workspace.KernelStartupFailure{
		BackendID: e.BackendID, BackendGeneration: e.BackendGeneration, ExecutorInstanceID: e.ExecutorInstanceID, Stage: stage,
	})
	return errors.Join(cause, err)
}

type StartupFailureError struct {
	Receipt workspace.KernelStartupFailure
}

func (b *Backend) refreshStartingPredecessor(ctx context.Context, current workspace.KernelExecutionBackend) (workspace.KernelExecutionBackend, bool, error) {
	if current.State == workspace.KernelExecutionBackendStateStarting && time.Since(current.UpdatedAt) >= b.StartTimeout {
		// The observer supplies the proof; age only avoids querying a just-created
		// supervisor unit before its launch request has been dispatched.
		if err := b.settleExitedStartup(ctx, current); err == nil {
			return b.Store.GetKernelExecutionBackend(ctx, current.BackendID)
		} else if !errors.Is(err, errStartupStillOwned) {
			return current, true, err
		}
	}
	return current, true, nil
}

func (e *StartupFailureError) Error() string {
	return fmt.Sprintf("detached kernel startup failed at %s (backend %s generation %d)", e.Receipt.Stage, e.Receipt.BackendID, e.Receipt.BackendGeneration)
}

// settleExitedStartup is only called after an actual process identity or the
// configured supervisor proves this generation has exited. No elapsed-time
// heuristic is allowed to create a competing executor.
func (b *Backend) settleExitedStartup(ctx context.Context, backend workspace.KernelExecutionBackend) error {
	if backend.ExecutorPID > 0 {
		alive, err := kernelruntime.ProcessIdentityAlive(backend.ExecutorPID, backend.ExecutorPIDStartTicks)
		if err != nil {
			return err
		}
		if alive {
			return errStartupStillOwned
		}
	} else {
		state, err := b.Launcher.Observe(ctx, backend.BackendID, backend.BackendGeneration)
		if err != nil {
			return err
		}
		if state != ExecutorLaunchExited {
			return errStartupStillOwned
		}
	}
	return b.Store.FailKernelExecutorStartup(ctx, workspace.KernelStartupFailure{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID, Stage: "process_exit",
	})
}
