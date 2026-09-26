package server

import (
	"context"
	"time"
)

const detachedKernelInfrastructureGrace = 2 * time.Minute

// detachedKernelLifecycleContext makes an explicit task stop observable from
// Err and Cause immediately, even though context.AfterFunc closes the detached
// setup channel asynchronously. Ordinary request cancellation remains hidden.
// Value intentionally follows the registered lifecycle parent so context.Cause
// can recover the original ErrGenerationStopped/ErrRuntimeDraining cause.
type detachedKernelLifecycleContext struct {
	context.Context
	lifecycle context.Context
	// values stays on the request-side context. The registered task lifetime
	// is intentionally a different parent used for cancellation, but it does
	// not own runner, reviewer, or approval values attached by the live turn.
	values context.Context
}

func (c detachedKernelLifecycleContext) Err() error {
	if agentKernelCallerRequiresExecutionStop(c.lifecycle) {
		return context.Canceled
	}
	return c.Context.Err()
}

func (c detachedKernelLifecycleContext) Value(key any) any {
	// Keep the lifecycle parent's internal cancellation values authoritative so
	// context.Cause still reports the task stop reason. Request-side values are
	// only a fallback for runner/reviewer/approval objects attached after the
	// lifetime context was registered.
	if c.lifecycle != nil {
		if value := c.lifecycle.Value(key); value != nil {
			return value
		}
	}
	if c.values != nil && c.values != c.lifecycle {
		return c.values.Value(key)
	}
	return nil
}

// detachedKernelSetupContext preserves a request handoff without severing the
// registered task's lifetime. The task parent remains observable even when a
// shorter request has already ended with a different cancellation cause.
func detachedKernelSetupContext(ctx context.Context, executionTimeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	setup, cancelSetup := context.WithCancelCause(context.WithoutCancel(ctx))
	stopRequest := context.AfterFunc(ctx, func() {
		if agentKernelCallerRequiresExecutionStop(ctx) {
			cancelSetup(context.Cause(ctx))
		}
	})
	if agentKernelCallerRequiresExecutionStop(ctx) {
		cancelSetup(context.Cause(ctx))
	}
	stopTask := func() bool { return true }
	lifecycle := ctx
	if task, ok := ctx.Value(sessionRunLifetimeContextKey{}).(context.Context); ok {
		lifecycle = task
		stopTask = context.AfterFunc(task, func() { cancelSetup(context.Cause(task)) })
		if cause := context.Cause(task); cause != nil {
			cancelSetup(cause)
		}
	}
	result := context.Context(setup)
	cancelDeadline := func() {}
	if executionTimeout > 0 {
		result, cancelDeadline = context.WithTimeout(setup, executionTimeout+detachedKernelInfrastructureGrace)
	}
	result = detachedKernelLifecycleContext{Context: result, lifecycle: lifecycle, values: ctx}
	return result, func() {
		stopRequest()
		stopTask()
		cancelDeadline()
		cancelSetup(nil)
	}
}
