// Package routinescheduler runs durable, lease-backed routine ticks.
package routinescheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"synon-go/internal/persistence/workspace"
)

var ErrAlreadyRunning = errors.New("routine scheduler is already running")

const (
	defaultLockTTL       = 5 * time.Minute
	defaultTickTimeout   = 4 * time.Minute
	defaultErrorBackoff  = time.Second
	defaultMinimumDelay  = 10 * time.Millisecond
	defaultMaxResultSize = 64 << 10
)

// Repository is the durable scheduling surface supplied by workspace.Store.
type Repository interface {
	ClaimNextDueRoutine(now time.Time, lockTTL time.Duration) (workspace.Routine, bool, error)
	CompleteRoutineTick(id string, at time.Time, successful bool, result string) error
	NextRoutineWakeAt(lockTTL time.Duration) (time.Time, bool, error)
}

// FencedCompletionRepository receives the exact claim returned by claim. It
// prevents a delayed worker from completing a later routine generation.
type FencedCompletionRepository interface {
	CompleteClaimedRoutineTick(workspace.Routine, time.Time, bool, string) error
}

// ClaimRenewalRepository keeps an unbounded Agent tick exclusively leased.
// Implementations fence renewals by the claim generation and opaque token.
type ClaimRenewalRepository interface {
	RenewClaimedRoutineTick(context.Context, workspace.Routine, time.Time) error
}

// Tick is the stable input needed by an Agent-backed routine executor.
type Tick struct {
	RoutineID    string
	RootFrameID  string
	OwnerUserID  string
	Instruction  string
	ScheduledFor time.Time
	ClaimedAt    time.Time
	Deadline     time.Time
	Attempt      int
}

type Result struct {
	Summary  string
	Deferred bool
}

// Executor must honor ctx cancellation and its deadline before returning.
type Executor interface {
	ExecuteRoutineTick(ctx context.Context, tick Tick) (Result, error)
}

type Clock interface {
	Now() time.Time
	NewTimer(delay time.Duration) Timer
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Options struct {
	Repository     Repository
	Executor       Executor
	Clock          Clock
	LockTTL        time.Duration
	TickTimeout    time.Duration
	UnboundedTicks bool
	ErrorBackoff   time.Duration
	MinimumDelay   time.Duration
	MaxResultBytes int
	OnError        func(error)
}

type Scheduler struct {
	repository     Repository
	executor       Executor
	clock          Clock
	lockTTL        time.Duration
	tickTimeout    time.Duration
	errorBackoff   time.Duration
	minimumDelay   time.Duration
	maxResultBytes int
	onError        func(error)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
}

func New(options Options) (*Scheduler, error) {
	if options.Repository == nil {
		return nil, errors.New("routine scheduler repository is required")
	}
	if options.Executor == nil {
		return nil, errors.New("routine scheduler executor is required")
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	setDurationDefault(&options.LockTTL, defaultLockTTL)
	if options.UnboundedTicks {
		options.TickTimeout = 0
	} else {
		setDurationDefault(&options.TickTimeout, defaultTickTimeout)
	}
	setDurationDefault(&options.ErrorBackoff, defaultErrorBackoff)
	setDurationDefault(&options.MinimumDelay, defaultMinimumDelay)
	if options.MaxResultBytes == 0 {
		options.MaxResultBytes = defaultMaxResultSize
	}
	if options.LockTTL <= 0 || options.TickTimeout < 0 || options.ErrorBackoff <= 0 || options.MinimumDelay <= 0 {
		return nil, errors.New("routine scheduler durations must be non-negative, with positive lease and retry durations")
	}
	if options.TickTimeout > 0 && options.TickTimeout >= options.LockTTL {
		return nil, errors.New("routine tick timeout must be shorter than lock ttl")
	}
	if options.TickTimeout == 0 {
		if _, ok := options.Repository.(ClaimRenewalRepository); !ok {
			return nil, errors.New("unbounded routine ticks require a claim renewal repository")
		}
	}
	if options.MaxResultBytes < 1 {
		return nil, errors.New("routine scheduler result limit must be positive")
	}
	return &Scheduler{
		repository: options.Repository, executor: options.Executor, clock: options.Clock,
		lockTTL: options.LockTTL, tickTimeout: options.TickTimeout,
		errorBackoff: options.ErrorBackoff,
		minimumDelay: options.MinimumDelay, maxResultBytes: options.MaxResultBytes,
		onError: options.OnError, wake: make(chan struct{}, 1),
	}, nil
}

// Start launches one scheduling loop. Calling Start while it is active is an
// error; after Stop completes the same Scheduler may be started again.
func (s *Scheduler) Start(parent context.Context) error {
	if parent == nil {
		return errors.New("routine scheduler context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	go func() {
		defer func() {
			s.mu.Lock()
			if s.done == done {
				s.cancel = nil
				s.done = nil
			}
			s.mu.Unlock()
			close(done)
		}()
		s.run(ctx)
	}()
	return nil
}

// Stop cancels an active tick, waits for persistence cleanup, and is idempotent.
func (s *Scheduler) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("routine scheduler stop context is required")
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wake interrupts an idle wait after a routine is created or reconfigured.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) run(ctx context.Context) {
	for ctx.Err() == nil {
		now := s.clock.Now().UTC()
		routine, claimed, err := s.repository.ClaimNextDueRoutine(now, s.lockTTL)
		if err != nil {
			s.report(fmt.Errorf("claim due routine: %w", err))
			if !s.wait(ctx, s.errorBackoff) {
				return
			}
			continue
		}
		if claimed {
			s.execute(ctx, routine, now)
			continue
		}

		wakeAt, found, err := s.repository.NextRoutineWakeAt(s.lockTTL)
		if err != nil {
			s.report(fmt.Errorf("find next routine wake time: %w", err))
			if !s.wait(ctx, s.errorBackoff) {
				return
			}
			continue
		}
		if !found {
			if !s.waitForWake(ctx) {
				return
			}
			continue
		}
		delay := wakeAt.Sub(s.clock.Now().UTC())
		if delay < s.minimumDelay {
			delay = s.minimumDelay
		}
		if !s.wait(ctx, delay) {
			return
		}
	}
}

// waitForWake blocks when there is no durable routine or expired lease to
// schedule. All supported routine mutations call Wake after their transaction
// commits, so an empty database causes no periodic SQLite reads.
func (s *Scheduler) waitForWake(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	}
}

func (s *Scheduler) execute(parent context.Context, routine workspace.Routine, claimedAt time.Time) {
	ctx := parent
	cancel := func() {}
	if s.tickTimeout > 0 {
		ctx, cancel = context.WithTimeout(parent, s.tickTimeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	deadline, _ := ctx.Deadline()
	tick := Tick{
		RoutineID: routine.ID, RootFrameID: routine.RootFrameID,
		OwnerUserID: routine.OwnerUserID, Instruction: routine.OnTick,
		ScheduledFor: routine.NextDue, ClaimedAt: claimedAt,
		Deadline: deadline, Attempt: routine.TickCount + 1,
	}
	heartbeatDone, heartbeatErr := s.startClaimHeartbeat(ctx, routine, cancel)
	result, executeErr := s.executor.ExecuteRoutineTick(ctx, tick)
	contextErr := ctx.Err()
	cancel()
	if heartbeatDone != nil {
		<-heartbeatDone
	}
	if heartbeatErr != nil {
		select {
		case err := <-heartbeatErr:
			if err != nil {
				s.report(fmt.Errorf("renew routine %q claim: %w", routine.ID, err))
				return
			}
		default:
		}
	}
	if executeErr == nil && contextErr != nil {
		executeErr = contextErr
	}
	// A deferred tick is still the same logical attempt. Keep its fenced claim
	// leased until it expires so the scheduler neither records a false failure
	// nor spins while an external input (for example AskUser) is outstanding.
	if result.Deferred && executeErr == nil {
		return
	}

	successful := executeErr == nil
	summary := strings.TrimSpace(result.Summary)
	if executeErr != nil {
		if summary != "" {
			summary += "\n"
		}
		summary += "error: " + executeErr.Error()
	}
	summary = truncateUTF8(summary, s.maxResultBytes)
	var completionErr error
	if fenced, ok := s.repository.(FencedCompletionRepository); ok {
		completionErr = fenced.CompleteClaimedRoutineTick(routine, s.clock.Now().UTC(), successful, summary)
	} else {
		completionErr = s.repository.CompleteRoutineTick(routine.ID, s.clock.Now().UTC(), successful, summary)
	}
	if completionErr != nil {
		s.report(fmt.Errorf("complete routine %q: %w", routine.ID, completionErr))
		return
	}
	if executeErr != nil && !errors.Is(executeErr, context.Canceled) {
		s.report(fmt.Errorf("execute routine %q: %w", routine.ID, executeErr))
	}
}

func (s *Scheduler) startClaimHeartbeat(ctx context.Context, routine workspace.Routine, cancel context.CancelFunc) (<-chan struct{}, <-chan error) {
	renewer, ok := s.repository.(ClaimRenewalRepository)
	if !ok {
		return nil, nil
	}
	done := make(chan struct{})
	errorsOut := make(chan error, 1)
	interval := s.lockTTL / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	go func() {
		defer close(done)
		for {
			timer := s.clock.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C():
				timer.Stop()
				if err := renewer.RenewClaimedRoutineTick(ctx, routine, s.clock.Now().UTC()); err != nil {
					select {
					case errorsOut <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	return done, errorsOut
}

func (s *Scheduler) wait(ctx context.Context, delay time.Duration) bool {
	timer := s.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timer.C():
		return true
	}
}

func (s *Scheduler) report(err error) {
	if s.onError != nil {
		s.onError(err)
	}
}

func setDurationDefault(value *time.Duration, fallback time.Duration) {
	if *value == 0 {
		*value = fallback
	}
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

type systemClock struct{}

func (systemClock) Now() time.Time                     { return time.Now() }
func (systemClock) NewTimer(delay time.Duration) Timer { return systemTimer{time.NewTimer(delay)} }

type systemTimer struct{ timer *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.timer.C }
func (t systemTimer) Stop() bool          { return t.timer.Stop() }
