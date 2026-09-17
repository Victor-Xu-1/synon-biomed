package server

import (
	"context"
	"errors"
	"strings"
)

const (
	sessionRunnerSupervisorInterruptedReasonCode      = "runner_supervisor_interrupted"
	sessionRunnerResumeDispatchInterruptedReasonCode  = "resume_dispatch_lease_interrupted"
	sessionRunnerModelProviderUnavailableReasonCode   = "model_provider_unavailable"
	sessionRunnerModelProviderTemporaryReasonCode     = "model_provider_temporarily_unavailable"
	sessionRunnerProviderTransportTemporaryReasonCode = "provider_transport_temporarily_unavailable"
)

// sessionRunnerInfrastructureInterruption distinguishes an internal ownership
// or persistence interruption from an explicit user stop. It is deliberately a
// value error so errors.As can recover the durable reason code from a context
// cancellation cause without depending on an internal pointer identity.
type sessionRunnerInfrastructureInterruption struct {
	ReasonCode string
	Detail     string
	cause      error
}

func (interruption sessionRunnerInfrastructureInterruption) Error() string {
	if detail := strings.TrimSpace(interruption.Detail); detail != "" {
		return detail
	}
	if interruption.cause != nil {
		return interruption.cause.Error()
	}
	if reason := strings.TrimSpace(interruption.ReasonCode); reason != "" {
		return reason
	}
	return "session runner infrastructure interrupted"
}

func (interruption sessionRunnerInfrastructureInterruption) Unwrap() error {
	return interruption.cause
}

func newSessionRunnerInfrastructureInterruption(reasonCode string, cause error) sessionRunnerInfrastructureInterruption {
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	return sessionRunnerInfrastructureInterruption{
		ReasonCode: strings.TrimSpace(reasonCode),
		Detail:     detail,
		cause:      cause,
	}
}

func sessionRunnerInfrastructureInterruptionFromContext(ctx context.Context) (sessionRunnerInfrastructureInterruption, bool) {
	if ctx == nil {
		return sessionRunnerInfrastructureInterruption{}, false
	}
	var interruption sessionRunnerInfrastructureInterruption
	if !errors.As(context.Cause(ctx), &interruption) || strings.TrimSpace(interruption.ReasonCode) == "" {
		return sessionRunnerInfrastructureInterruption{}, false
	}
	return interruption, true
}
