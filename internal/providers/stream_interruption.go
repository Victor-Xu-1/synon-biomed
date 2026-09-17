package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
)

var (
	errProviderSemanticSegmentBoundary  = errors.New("provider semantic segment boundary")
	errProviderContentResponseTruncated = errors.New("provider content response truncated")
	errProviderEmptyResponse            = errors.New("provider response contained no semantic output")
)

type providerEmptyResponseError struct {
	message string
}

func (err *providerEmptyResponseError) Error() string {
	if err == nil || err.message == "" {
		return errProviderEmptyResponse.Error()
	}
	return err.message
}

func (err *providerEmptyResponseError) Is(target error) bool {
	return target == errProviderEmptyResponse
}

func newProviderEmptyResponseError(message string) error {
	return &providerEmptyResponseError{message: message}
}

// IsProviderEmptyResponse reports a successful provider exchange that
// produced neither user-visible text nor an executable tool call. It is a
// typed no-progress boundary: bounded request retries may end, but the durable
// logical task can continue from its last completed tool checkpoint.
func IsProviderEmptyResponse(err error) bool {
	return errors.Is(err, errProviderEmptyResponse)
}

type providerContentResponseTruncatedError struct {
	finishReason string
}

func (err providerContentResponseTruncatedError) Error() string {
	if err.finishReason == "" {
		return errProviderResponseTruncated.Error()
	}
	return fmt.Sprintf("%s: finish reason %s", errProviderResponseTruncated, err.finishReason)
}

func (err providerContentResponseTruncatedError) Is(target error) bool {
	return target == errProviderResponseTruncated || target == errProviderContentResponseTruncated
}

func newProviderContentResponseTruncation(finishReason string) error {
	return providerContentResponseTruncatedError{finishReason: finishReason}
}

type providerSemanticSegmentBoundaryError struct {
	limit int64
}

func (err providerSemanticSegmentBoundaryError) Error() string {
	return fmt.Sprintf("provider response too large: decoded response exceeded %d bytes", err.limit)
}

func (err providerSemanticSegmentBoundaryError) Is(target error) bool {
	return target == errProviderResponseTooLarge || target == errProviderSemanticSegmentBoundary
}

func newProviderSemanticSegmentBoundary(limit int64) error {
	return providerSemanticSegmentBoundaryError{limit: limit}
}

// providerStreamRecoveryCandidate is parser-owned evidence that the stream
// stopped at a safe content-only boundary. Transport normalization still runs
// before the error becomes recoverable so caller cancellation cannot be
// relabelled as a continuation.
type providerStreamRecoveryCandidate struct {
	cause error
}

func (err *providerStreamRecoveryCandidate) Error() string {
	if err == nil || err.cause == nil {
		return "provider stream stopped at a recoverable content boundary"
	}
	return err.cause.Error()
}

func (err *providerStreamRecoveryCandidate) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// RecoverableStreamInterruptionError reports that a provider stream ended
// after semantic output was accepted by the caller. The provider must not
// replay that output; the runner decides how to resume from durable state.
type RecoverableStreamInterruptionError struct {
	cause error
}

func (err *RecoverableStreamInterruptionError) Error() string {
	if err == nil || err.cause == nil {
		return "provider stream interrupted after accepting content"
	}
	return "provider stream interrupted after accepting content: " + err.cause.Error()
}

func (err *RecoverableStreamInterruptionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// IsRecoverableStreamInterruption reports whether an upper layer may resume
// the interrupted generation from its durable checkpoint.
func IsRecoverableStreamInterruption(err error) bool {
	var interruption *RecoverableStreamInterruptionError
	return errors.As(err, &interruption)
}

// IsRetryableProviderTransportFailure recognizes a typed transport failure
// that occurred before a provider response became semantically visible. The
// caller owns the no-progress check; this helper deliberately excludes
// cancellation, configured timeouts, protocol limits, and provider-declared
// response failures so ordinary model or task errors cannot be relabelled by
// matching their text.
func IsRetryableProviderTransportFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errOpenAIChatStreamFirstByteTimeout) ||
		errors.Is(err, errOpenAIChatStreamIdleTimeout) ||
		errors.Is(err, errProviderResponseTooLarge) || errors.Is(err, errProviderResponseTruncated) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

// IsContinuationSafeStreamFailure recognizes stream transport failures that
// may be resumed when the runner has already persisted semantic output. It is
// deliberately narrower than a generic retry predicate and excludes first
// byte failures, oversized responses, and ordinary provider errors.
func IsContinuationSafeStreamFailure(err error) bool {
	if err == nil || IsRecoverableStreamInterruption(err) {
		return false
	}
	if errors.Is(err, errProviderResponseTooLarge) {
		return false
	}
	return errors.Is(err, errProviderStreamIncomplete) ||
		errors.Is(err, errOpenAIChatStreamRead) ||
		errors.Is(err, errOpenAIChatStreamIdleTimeout) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// IsContinuationSafeResponseTruncation reports a provider-declared output
// limit reached after content-only semantic output. The provider parser owns
// this classification because it can distinguish durable text from a partial
// tool call. Ordinary truncation, incomplete streams, and oversized responses
// remain terminal at this boundary.
func IsContinuationSafeResponseTruncation(err error) bool {
	if err == nil || errors.Is(err, errProviderResponseTooLarge) || errors.Is(err, errProviderStreamIncomplete) {
		return false
	}
	return errors.Is(err, errProviderResponseTruncated) &&
		errors.Is(err, errProviderContentResponseTruncated)
}

// IsProviderOutputTokenLimit reports a provider-declared output-token limit,
// including a response that ended while constructing a tool call. Such a
// partial tool call is never executable or persisted, but the runner may start
// a fresh bounded generation from the last completed tool checkpoint.
func IsProviderOutputTokenLimit(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errProviderResponseTooLarge) || errors.Is(err, errProviderStreamIncomplete) {
		return false
	}
	return errors.Is(err, errProviderResponseTruncated)
}

func newRecoverableStreamInterruption(cause error) error {
	return &RecoverableStreamInterruptionError{cause: cause}
}

func newProviderStreamRecoveryCandidate(cause error) error {
	return &providerStreamRecoveryCandidate{cause: cause}
}

func classifyProviderStreamInterruption(parentCtx, requestCtx context.Context, emitted bool, err error) error {
	var candidate *providerStreamRecoveryCandidate
	if errors.As(err, &candidate) {
		err = candidate.cause
	}
	err = openAIChatStreamRequestError(parentCtx, requestCtx, err)
	if candidate != nil && emitted && isRecoverableProviderStreamFailure(err) {
		return newRecoverableStreamInterruption(err)
	}
	return err
}

func isRecoverableProviderStreamFailure(err error) bool {
	if errors.Is(err, errProviderSemanticSegmentBoundary) {
		return true
	}
	if errors.Is(err, errProviderResponseTooLarge) ||
		(errors.Is(err, errProviderResponseTruncated) && !errors.Is(err, errProviderStreamIncomplete)) {
		return false
	}
	if isRecoverableOpenAIChatStreamFailure(err) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

type providerStreamIdleReadCloser struct {
	reader *openAIChatStreamIdleReader
	closer io.Closer
}

func (body *providerStreamIdleReadCloser) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

func (body *providerStreamIdleReadCloser) Close() error {
	return body.closer.Close()
}

func (body *providerStreamIdleReadCloser) recordProgress() {
	body.reader.reset()
}
