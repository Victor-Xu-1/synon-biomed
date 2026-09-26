package server

import (
	"errors"
	"strings"
)

// Choose a safe machine identity before redacting the public explanation.
// Typed host errors are authoritative; compatibility text classification is
// limited to the existing known reasons. Never publish an arbitrary error.
func sessionRunnerErrorReasonCode(err error) string {
	if err == nil {
		return ""
	}
	var correction sessionRunnerBoundedCorrection
	if errors.As(err, &correction) {
		if reason := correction.runnerCorrection().ReasonCode; validRunnerFailureReason(reason) {
			return reason
		}
	}
	if reason := sessionRunnerFailureReasonCode(err.Error()); reason != "" {
		return reason
	}
	return "runner_execution_failed"
}

func validRunnerFailureReason(reason string) bool {
	if reason == "" || len(reason) > 64 || strings.TrimSpace(reason) != reason {
		return false
	}
	for _, value := range reason {
		if !(value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_') {
			return false
		}
	}
	return true
}
