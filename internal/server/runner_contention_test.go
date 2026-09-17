package server

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerClaimContentionRemainsLocalAndRecovers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var calls atomic.Int64
	err := (&Server{}).runSessionRunnerChatLoop(ctx, normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{PollInterval: time.Millisecond}), func(context.Context, SessionRunnerChatOptions) (SessionRunnerCycleResult, error) {
		if calls.Add(1) <= 2 {
			return SessionRunnerCycleResult{}, errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		cancel()
		return SessionRunnerCycleResult{}, nil
	})
	if err != nil || calls.Load() != 3 {
		t.Fatalf("claim contention escaped scheduler: calls=%d err=%v", calls.Load(), err)
	}
}

func TestRunnerPoolPreservesInfrastructureCauseAcrossCohortRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	active := make(chan struct{})
	cause := make(chan error, 1)
	permanent := errors.New("store unavailable")
	err := (&Server{}).runSessionRunnerChatPool(ctx, SessionRunnerChatOptions{PollInterval: time.Millisecond}, 2,
		func(ctx context.Context, options SessionRunnerChatOptions) (SessionRunnerCycleResult, error) {
			if strings.HasSuffix(options.RunnerID, "/chat-1") {
				select {
				case <-active:
				case <-ctx.Done():
				}
				return SessionRunnerCycleResult{}, permanent
			}
			close(active)
			<-ctx.Done()
			cause <- context.Cause(ctx)
			return SessionRunnerCycleResult{Claimed: true}, ctx.Err()
		})
	var interruption sessionRunnerInfrastructureInterruption
	if !errors.Is(err, permanent) || !errors.As(<-cause, &interruption) || interruption.ReasonCode != sessionRunnerSupervisorInterruptedReasonCode {
		t.Fatalf("lost pool cancellation cause: %v %+v", err, interruption)
	}
	if !runnerInterruptionAutoResume(interruption.ReasonCode) || !runnerInterruptionIsProgressBoundary(interruption.ReasonCode) {
		t.Fatal("infrastructure handoff terminates logical task")
	}
}

func TestRunnerContentionBackoffCanBeCancelledWithoutCancellingSibling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	active := make(chan struct{})
	siblingStopped := make(chan error, 1)
	var claims atomic.Int64
	err := (&Server{}).runSessionRunnerChatPool(ctx, SessionRunnerChatOptions{PollInterval: time.Millisecond}, 2,
		func(ctx context.Context, options SessionRunnerChatOptions) (SessionRunnerCycleResult, error) {
			if strings.HasSuffix(options.RunnerID, "/chat-1") {
				if claims.Add(1) == 3 {
					select {
					case <-active:
					default:
						t.Error("healthy sibling not running")
					}
					if ctx.Err() != nil {
						t.Error("claim contention cancelled sibling")
					}
					cancel()
					return SessionRunnerCycleResult{}, nil
				}
				return SessionRunnerCycleResult{}, errors.New("SQLITE_BUSY")
			}
			close(active)
			<-ctx.Done()
			siblingStopped <- context.Cause(ctx)
			return SessionRunnerCycleResult{Claimed: true}, ctx.Err()
		})
	if err != nil || claims.Load() != 3 || !errors.Is(<-siblingStopped, context.Canceled) {
		t.Fatalf("contention escaped admission: %v calls=%d", err, claims.Load())
	}
}
