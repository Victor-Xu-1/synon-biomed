package server

import (
	"errors"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	defaultFrameResumeDispatchPollInterval = 250 * time.Millisecond
	frameResumeDispatchClaimLeaseGrace     = time.Minute
	defaultFrameResumeReservationTTL       = 24 * time.Hour
	defaultFrameResumeDispatchWorkerID     = "synon-frame-resume-dispatcher"
	frameResumePauseRecheckInterval        = 30 * time.Second
	// Kernel settlement and approval transitions wake the dispatcher through the
	// workspace retention signal. This bounded backstop is only for a lost
	// signal; it must not make a healthy long-running task wait many minutes.
	frameResumeKernelRecoveryRecheckInterval = 30 * time.Second
	frameResumeDispatchRenewMaxAttempts      = 3
	frameResumeDispatchRenewRetryDelay       = 50 * time.Millisecond
	// Recovery is event-driven for terminal runner/frame transitions. This
	// bounded backstop handles a lost notification without repeatedly scanning
	// the full transcript tables while the system is idle.
	autoResumeScanInterval               = time.Minute
	autoResumeNoProgressBackoffBase      = 2 * time.Second
	autoResumeNoProgressBackoffMax       = 20 * time.Second
	autoResumeNoProgressBackoffJitterMax = 2 * time.Second
	// A provider transport that ended before its first semantic event follows
	// the same retry envelope as the reference Harness: 5s exponential delay,
	// capped at 60s, with up to 50% jitter. This is a scheduling delay between
	// durable execution segments, never a logical-task lifetime or retry cap.
	autoResumeProviderTransportBackoffBase = 5 * time.Second
	autoResumeProviderTransportBackoffMax  = 60 * time.Second
	autoResumeCandidateLimit               = 20
)

var (
	errFrameResumeRunnerStillActive    = errors.New("resume runner is still active; recovery is deferred")
	errFrameResumeToolOutcomeUncertain = errors.New("resume runner has an unresolved side-effecting tool outcome")
	errFrameResumeDispatchClaimLost    = errors.New("resume dispatch claim changed before runner execution")
)

type FrameResumeDispatchOptions struct {
	WorkerID       string
	PollInterval   time.Duration
	ClaimTTL       time.Duration
	ReservationTTL time.Duration
	Chat           SessionRunnerChatOptions
}

type FrameResumeDispatchResult struct {
	Claimed       bool
	ResumeEventID string
	FrameID       string
	Attempt       int
	Recovered     bool
	Status        string
	Runner        SessionRunnerCycleResult
	TerminalEvent *workspace.FrameEvent
}
