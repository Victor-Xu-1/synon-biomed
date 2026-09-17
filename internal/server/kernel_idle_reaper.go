package server

import (
	"context"
	"errors"
	"time"
)

const kernelIdleCloseTimeout = 5 * time.Second

// RunKernelIdleReaper applies the persisted root-session policy to immutable
// Manager candidates. It has no polling interval: committed Workspace state,
// kernel activity, or the nearest exact deadline wakes the next evaluation.
func (s *Server) RunKernelIdleReaper(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.kernelManager == nil || s.workspaceStore == nil {
		<-ctx.Done()
		return nil
	}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	for {
		if cause := context.Cause(ctx); cause != nil {
			return nil
		}
		candidates := s.kernelManager.SnapshotIdleCandidates()
		roots := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			roots = append(roots, candidate.RootFrameID)
		}
		states, err := s.workspaceStore.ListKernelRetentionStates(ctx, roots)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		stateByRoot := make(map[string]kernelRetentionPolicy, len(states))
		for _, state := range states {
			stateByRoot[state.RootFrameID] = kernelRetentionPolicy{
				exists: state.Exists, protected: state.Protected,
				idleTimeout: time.Duration(state.IdleTimeoutSeconds) * time.Second,
			}
		}
		now := time.Now().UTC()
		if s.kernelIdleNow != nil {
			now = s.kernelIdleNow().UTC()
		}
		var nearest time.Time
		closedAny := false
		for _, candidate := range candidates {
			policy, found := stateByRoot[candidate.RootFrameID]
			if !found || !policy.exists || policy.protected || policy.idleTimeout <= 0 {
				continue
			}
			deadline := candidate.LastUsed.Add(policy.idleTimeout)
			if deadline.After(now) {
				if nearest.IsZero() || deadline.Before(nearest) {
					nearest = deadline
				}
				continue
			}
			closeCtx, cancel := context.WithTimeout(ctx, kernelIdleCloseTimeout)
			closed, closeErr := s.kernelManager.CloseIdleCandidate(closeCtx, candidate)
			cancel()
			if closeErr != nil {
				return closeErr
			}
			closedAny = closedAny || closed
		}
		if closedAny {
			continue
		}
		var deadline <-chan time.Time
		if !nearest.IsZero() {
			delay := nearest.Sub(now)
			if delay < 0 {
				delay = 0
			}
			timer.Reset(delay)
			deadline = timer.C
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.kernelManager.IdleWake():
		case <-s.workspaceStore.OutboxWake():
		case <-s.workspaceStore.KernelRetentionWake():
		case <-deadline:
		}
		if deadline != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

type kernelRetentionPolicy struct {
	exists      bool
	protected   bool
	idleTimeout time.Duration
}

func (s *Server) KernelIdleReaperEnabled() bool {
	return s != nil && s.kernelManager != nil && s.workspaceStore != nil
}
