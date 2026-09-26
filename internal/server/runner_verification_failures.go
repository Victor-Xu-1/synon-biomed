package server

import (
	"context"
	"errors"

	"synon-go/internal/agentruntime"
	"synon-go/internal/providers"
)

// Only reviewer protocol, provider availability and bounded generation limits
// are advisory. Unknown failures (especially persistence and authority errors)
// remain blocking even when the main agent already produced a candidate.
func sessionReviewerFailureIsAdvisory(err error) bool {
	if err == nil {
		return false
	}
	var stage *sessionRunnerReviewStageError
	if errors.As(err, &stage) {
		return false
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			causes := joined.Unwrap()
			if len(causes) == 0 {
				return false
			}
			for _, cause := range causes {
				if !sessionReviewerFailureIsAdvisory(cause) {
					return false
				}
			}
			return true
		}
	}
	if _, ok := err.(*sessionRunnerReviewerEvidenceUnavailableError); ok {
		return true
	}
	if _, ok := providers.HTTPStatus(err); ok {
		return true
	}
	if providers.IsRetryableModelProtocolError(err) || providers.IsProviderEmptyResponse(err) ||
		providers.IsRecoverableStreamInterruption(err) || providers.IsRetryableProviderTransportFailure(err) ||
		providers.IsContinuationSafeStreamFailure(err) || providers.IsProviderOutputTokenLimit(err) {
		return true
	}
	switch err.(type) {
	case *sessionReviewerSubmissionProtocolError, *agentruntime.ToolCallBatchValidationError,
		*agentruntime.ToolRoundLimitError, *agentruntime.ToolRoundNoProgressError,
		*agentruntime.ToolCallBatchLimitError:
		return true
	case *sessionRunnerModelCallError:
		// A request deadline is different from cancellation of the root task,
		// which is checked before considering an advisory disposition.
		if errors.Is(err, context.DeadlineExceeded) {
			return true
		}
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return sessionReviewerFailureIsAdvisory(wrapped.Unwrap())
	}
	return false
}
