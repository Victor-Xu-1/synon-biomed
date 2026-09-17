package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	runnermachine "synon-go/internal/sessionrunner"
)

var (
	ErrGenerationStopped       = errors.New("generation stopped")
	ErrRuntimeDraining         = errors.New("runtime draining")
	ErrSessionRunAlreadyActive = errors.New("session run is already active")
)

type activeSessionRun struct {
	runnerID     string
	cancel       context.CancelCauseFunc
	done         chan struct{}
	settlement   sync.Mutex
	settled      bool
	draining     atomic.Bool
	transcript   *transcriptRunnerAuthority
	phaseMachine *runnermachine.Machine
	kernelPeer   kernelMessageBudgets
	chatRun      *sessionRunnerChatRun
}

func (s *Server) activeSessionChatRun(sessionID string) *sessionRunnerChatRun {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	s.sessionRunsMu.Lock()
	defer s.sessionRunsMu.Unlock()
	active := s.sessionRuns[strings.TrimSpace(sessionID)]
	if active == nil {
		return nil
	}
	active.settlement.Lock()
	defer active.settlement.Unlock()
	return active.chatRun
}

func (s *Server) registerActiveSessionRun(parent context.Context, sessionID, runnerID string) (context.Context, *activeSessionRun, func(), error) {
	if parent == nil {
		return nil, nil, nil, errors.New("active session run context is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	runnerID = strings.TrimSpace(runnerID)
	if s == nil || sessionID == "" || runnerID == "" {
		return nil, nil, nil, errors.New("active session run requires a server, session, and runner")
	}
	ctx, cancel := context.WithCancelCause(parent)
	run := &activeSessionRun{runnerID: runnerID, cancel: cancel, done: make(chan struct{})}
	s.sessionRunsMu.Lock()
	if s.sessionRunsDraining {
		s.sessionRunsMu.Unlock()
		cancel(ErrRuntimeDraining)
		return nil, nil, nil, ErrRuntimeDraining
	}
	if existing := s.sessionRuns[sessionID]; existing != nil {
		s.sessionRunsMu.Unlock()
		cancel(errors.New("duplicate active session run"))
		return nil, nil, nil, fmt.Errorf("%w: session %q is already running under %s", ErrSessionRunAlreadyActive, sessionID, existing.runnerID)
	}
	s.sessionRuns[sessionID] = run
	s.sessionRunsMu.Unlock()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			s.sessionRunsMu.Lock()
			if s.sessionRuns[sessionID] == run {
				delete(s.sessionRuns, sessionID)
			}
			s.sessionRunsMu.Unlock()
			cancel(nil)
			close(run.done)
		})
	}
	return ctx, run, cleanup, nil
}

func (s *Server) hasActiveSessionRun(sessionID string) bool {
	if s == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	s.sessionRunsMu.Lock()
	defer s.sessionRunsMu.Unlock()
	return s.sessionRuns[sessionID] != nil
}

func (s *Server) isDraining() bool {
	if s == nil {
		return false
	}
	s.sessionRunsMu.Lock()
	defer s.sessionRunsMu.Unlock()
	return s.sessionRunsDraining
}

func (s *Server) sessionRunActivity() (active int, draining bool) {
	if s == nil {
		return 0, false
	}
	s.sessionRunsMu.Lock()
	defer s.sessionRunsMu.Unlock()
	return len(s.sessionRuns), s.sessionRunsDraining
}

func (s *Server) activeKernelExecutionCount() int {
	if s == nil {
		return 0
	}
	inMemory := 0
	if s.kernelManager != nil {
		inMemory = s.kernelManager.ActiveExecutionCount()
	}
	if s.workspaceStore == nil {
		return inMemory
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	durable, err := s.workspaceStore.CountActiveDetachedKernelExecutions(ctx)
	if err != nil || durable <= inMemory {
		return inMemory
	}
	// The same execution normally exists in both projections. max(), rather
	// than sum(), avoids double counting while still preserving restart and
	// pending-recovery liveness that has no current in-memory handle.
	return durable
}

func (s *Server) activeDurableRunnerAttemptCount() int {
	if s == nil || s.transcriptStore == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	count, err := s.transcriptStore.CountActiveRunnerAttempts(ctx)
	if err != nil {
		// A failed liveness read is not proof that deployment is safe.
		return 1
	}
	return count
}

func (s *Server) activeKernelOperationCount() int {
	if s == nil || s.workspaceStore == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	count, err := s.workspaceStore.CountActiveKernelLocalOperations(ctx)
	if err != nil {
		// Preserve the running backend when durable work cannot be classified.
		return 1
	}
	return count
}

// TryDrainIdle atomically enters draining mode only when no runner is active.
// It is used by source/deployment supervisors to activate a verified binary
// without racing a new task admission or interrupting an in-flight task.
func (s *Server) TryDrainIdle() bool {
	if s == nil {
		return false
	}
	s.sessionRunsMu.Lock()
	defer s.sessionRunsMu.Unlock()
	if s.sessionRunsDraining {
		return true
	}
	// A durable local operation can outlive the runner cycle that submitted it.
	// Treat that execution as active runtime work as well; otherwise a verified
	// source update can replace the backend while the kernel is still settling,
	// leaving replay with a start checkpoint but no live kernel to complete it.
	if len(s.sessionRuns) != 0 || s.activeDurableRunnerAttemptCount() != 0 ||
		s.activeKernelExecutionCount() != 0 || s.activeKernelOperationCount() != 0 {
		return false
	}
	s.sessionRunsDraining = true
	return true
}

// IsDraining reports whether the server has stopped accepting runtime work.
// Runtime supervisors use this to distinguish intentional shutdown from a
// component failure without cancelling active runner contexts prematurely.
func (s *Server) IsDraining() bool {
	return s.isDraining()
}

func (s *Server) stopActiveSessionRun(sessionID, reason string) (bool, string) {
	if s == nil {
		return false, ""
	}
	sessionID = strings.TrimSpace(sessionID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "stopped by user"
	}
	if len(reason) > 500 {
		reason = reason[:500]
	}
	s.sessionRunsMu.Lock()
	run := s.sessionRuns[sessionID]
	s.sessionRunsMu.Unlock()
	if run == nil {
		return false, ""
	}
	run.settlement.Lock()
	defer run.settlement.Unlock()
	if run.settled {
		return false, run.runnerID
	}
	run.cancel(fmt.Errorf("%w: %s", ErrGenerationStopped, reason))
	return true, run.runnerID
}

func (s *Server) commitSessionRunCancellation(sessionIDs []string, commit func(map[string]*transcriptRunnerAuthority) error) error {
	if s == nil || commit == nil {
		return errors.New("session cancellation commit is required")
	}
	ids := normalizedSessionRunIDs(sessionIDs)
	s.sessionRunsMu.Lock()
	runs := make(map[string]*activeSessionRun, len(ids))
	for _, sessionID := range ids {
		if run := s.sessionRuns[sessionID]; run != nil {
			run.settlement.Lock()
			runs[sessionID] = run
		}
	}
	authorities := make(map[string]*transcriptRunnerAuthority, len(runs))
	for sessionID, run := range runs {
		authorities[sessionID] = run.transcript
	}
	err := commit(authorities)
	if err == nil {
		for _, run := range runs {
			if !run.settled {
				run.cancel(fmt.Errorf("%w: cancelled by durable frame authority", ErrGenerationStopped))
			}
		}
	}
	for index := len(ids) - 1; index >= 0; index-- {
		if run := runs[ids[index]]; run != nil {
			run.settlement.Unlock()
		}
	}
	s.sessionRunsMu.Unlock()
	return err
}

func normalizedSessionRunIDs(input []string) []string {
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, value := range input {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (s *Server) drainAllActiveSessionRuns(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.sessionRunsMu.Lock()
	s.sessionRunsDraining = true
	runs := make([]*activeSessionRun, 0, len(s.sessionRuns))
	for _, run := range s.sessionRuns {
		runs = append(runs, run)
	}
	s.sessionRunsMu.Unlock()
	for _, run := range runs {
		// Publish drain intent before competing for settlement. Terminal writers
		// that reach the mutex first must still observe that drain won globally.
		if run != nil {
			run.draining.Store(true)
		}
	}
	pending := append([]*activeSessionRun(nil), runs...)
	for len(pending) > 0 {
		next := make([]*activeSessionRun, 0, len(pending))
		for _, run := range pending {
			if run == nil {
				continue
			}
			// Settlement is the linearization boundary between a durable terminal
			// result and a resumable infrastructure interruption. Taking the same
			// lock as terminal writers guarantees that either the terminal commit
			// won first or the runner observes ErrRuntimeDraining before committing.
			if !run.settlement.TryLock() {
				next = append(next, run)
				continue
			}
			if !run.settled && run.cancel != nil {
				run.cancel(ErrRuntimeDraining)
			}
			run.settlement.Unlock()
		}
		pending = next
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	for _, run := range runs {
		if run == nil || run.done == nil {
			continue
		}
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Drain stops accepting work in active in-process runs and waits until each
// one has persisted a resumable interruption. It is intentionally distinct
// from user cancellation, which is a terminal business action.
func (s *Server) Drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("server drain context is required")
	}
	return s.drainAllActiveSessionRuns(ctx)
}
