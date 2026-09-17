package providers

import "errors"

// retryableModelProtocolError marks a structurally invalid model response.
// The provider transport must not retry it after streaming side effects, but
// callers whose tools are read-only may safely restart the whole generation.
type retryableModelProtocolError struct {
	cause error
}

func (err *retryableModelProtocolError) Error() string {
	if err == nil || err.cause == nil {
		return "model returned an invalid protocol response"
	}
	return err.cause.Error()
}

func (err *retryableModelProtocolError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func newRetryableModelProtocolError(cause error) error {
	if cause == nil {
		return nil
	}
	return &retryableModelProtocolError{cause: cause}
}

// IsRetryableModelProtocolError reports whether a provider accepted a request
// but returned a malformed tool-call or equivalent model protocol payload.
func IsRetryableModelProtocolError(err error) bool {
	var target *retryableModelProtocolError
	return errors.As(err, &target)
}
