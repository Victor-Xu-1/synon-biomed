package main

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"reflect"
	"sync"
	"time"
)

const (
	defaultRuntimeSupervisorInitialBackoff = 250 * time.Millisecond
	defaultRuntimeSupervisorMaxBackoff     = 30 * time.Second
	defaultRuntimeSupervisorStableWindow   = time.Minute
)

var errRuntimeComponentStarting = errors.New("runtime component starting")

type runtimeSupervisorPolicy struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	StableWindow   time.Duration
	Jitter         func(time.Duration) time.Duration
}

func defaultRuntimeSupervisorPolicy() runtimeSupervisorPolicy {
	return runtimeSupervisorPolicy{
		InitialBackoff: defaultRuntimeSupervisorInitialBackoff,
		MaxBackoff:     defaultRuntimeSupervisorMaxBackoff,
		StableWindow:   defaultRuntimeSupervisorStableWindow,
		Jitter: func(delay time.Duration) time.Duration {
			if delay <= 0 {
				return 0
			}
			spread := delay / 4
			if spread <= 0 {
				return delay
			}
			return delay - spread + time.Duration(rand.Int63n(int64(spread*2)+1))
		},
	}
}

func runRuntimeSupervisor(
	ctx context.Context,
	name string,
	run func(context.Context, func()) error,
	report func(string, error),
	stopping func() bool,
	policy runtimeSupervisorPolicy,
) {
	if ctx == nil || run == nil {
		return
	}
	if policy.InitialBackoff <= 0 {
		policy.InitialBackoff = defaultRuntimeSupervisorInitialBackoff
	}
	if policy.MaxBackoff < policy.InitialBackoff {
		policy.MaxBackoff = policy.InitialBackoff
	}
	if policy.StableWindow <= 0 {
		policy.StableWindow = defaultRuntimeSupervisorStableWindow
	}
	if policy.Jitter == nil {
		policy.Jitter = func(delay time.Duration) time.Duration { return delay }
	}
	backoff := policy.InitialBackoff
	for {
		if ctx.Err() != nil || (stopping != nil && stopping()) {
			return
		}
		if report != nil {
			report(name, errRuntimeComponentStarting)
		}
		startedAt := time.Now()
		var readyOnce sync.Once
		ready := func() {
			readyOnce.Do(func() {
				if ctx.Err() == nil && (stopping == nil || !stopping()) && report != nil {
					report(name, nil)
				}
			})
		}
		err := run(ctx, ready)
		if ctx.Err() != nil {
			return
		}
		if stopping != nil && stopping() {
			return
		}
		if err == nil {
			err = errors.New("runtime component stopped unexpectedly")
		}
		if report != nil {
			report(name, err)
		}
		delay := policy.Jitter(backoff)
		if delay < 0 {
			delay = 0
		}
		log.Printf("runtime supervisor component=%q failure_type=%q failure=%q retry_after=%s",
			name, reflect.TypeOf(err).String(), err.Error(), delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		if time.Since(startedAt) >= policy.StableWindow {
			backoff = policy.InitialBackoff
		} else if backoff < policy.MaxBackoff {
			backoff *= 2
			if backoff > policy.MaxBackoff {
				backoff = policy.MaxBackoff
			}
		}
	}
}
