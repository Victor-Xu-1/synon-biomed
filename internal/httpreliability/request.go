// Package httpreliability owns HTTP attempt lifetimes and retry advice only.
// Destination authorization, proxy selection, body-size limits, persistence
// and the decision to retry remain with their existing caller authorities.
package httpreliability

import (
	"context"
	"io"
	"sync"
	"time"
)

const DefaultHeaderTimeout = 60 * time.Second
const DefaultReadIdleTimeout = 30 * time.Second
const DefaultTransferIdleTimeout = 5 * time.Minute
const DefaultSearchTimeout = 90 * time.Second

type TimeoutError struct{ Phase string }

func (err *TimeoutError) Error() string { return "HTTP " + err.Phase + " timed out" }
func (*TimeoutError) Timeout() bool     { return true }
func (*TimeoutError) Temporary() bool   { return true }

var ErrResponseHeadersTimeout = &TimeoutError{Phase: "response headers"}
var ErrBodyIdleTimeout = &TimeoutError{Phase: "response body idle"}

type Attempt struct {
	Context   context.Context
	cancel    context.CancelCauseFunc
	timer     *time.Timer
	timerDone chan struct{}
	stopOnce  sync.Once
}

// Begin includes destination lookup, connection and response headers in one
// attempt budget. It never extends the caller's deadline or cancellation.
func Begin(parent context.Context, headerTimeout time.Duration) *Attempt {
	if headerTimeout <= 0 {
		headerTimeout = DefaultHeaderTimeout
	}
	ctx, cancel := context.WithCancelCause(parent)
	attempt := &Attempt{Context: ctx, cancel: cancel, timerDone: make(chan struct{})}
	attempt.timer = time.AfterFunc(headerTimeout, func() {
		cancel(ErrResponseHeadersTimeout)
		close(attempt.timerDone)
	})
	return attempt
}

func (attempt *Attempt) stopHeaders() {
	attempt.stopOnce.Do(func() {
		if !attempt.timer.Stop() {
			<-attempt.timerDone
		}
	})
}

// Body changes from a header budget to a per-read idle budget. A progressing
// response can outlive the header budget without any wall-clock reset loop.
func (attempt *Attempt) Body(body io.ReadCloser, idle time.Duration) (io.ReadCloser, error) {
	attempt.stopHeaders()
	if err := context.Cause(attempt.Context); err != nil {
		_ = body.Close()
		return nil, err
	}
	return &attemptBody{ReadCloser: IdleBody(body, idle), attempt: attempt}, nil
}

func (attempt *Attempt) Error(err error) error {
	if cause := context.Cause(attempt.Context); cause != nil {
		return cause
	}
	return err
}

func (attempt *Attempt) Close() { attempt.stopHeaders(); attempt.cancel(context.Canceled) }

type attemptBody struct {
	io.ReadCloser
	attempt *Attempt
}

func (body *attemptBody) Close() error {
	body.attempt.Close()
	return body.ReadCloser.Close()
}

// IdleBody is shared by bounded inspection and complete file acquisition.
// It owns no routing or retry policy, and does not buffer the response.
func IdleBody(body io.ReadCloser, timeout time.Duration) io.ReadCloser {
	if timeout <= 0 {
		timeout = DefaultReadIdleTimeout
	}
	return &idleBody{body: body, timeout: timeout}
}

type idleBody struct {
	body    io.ReadCloser
	timeout time.Duration
}

func (body *idleBody) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	timedOut := make(chan struct{})
	timer := time.AfterFunc(body.timeout, func() {
		_ = body.body.Close()
		close(timedOut)
	})
	count, err := body.body.Read(target)
	if !timer.Stop() {
		// Do not allow a late callback from this read to close the next one.
		<-timedOut
		return count, ErrBodyIdleTimeout
	}
	return count, err
}
func (body *idleBody) Close() error { return body.body.Close() }
