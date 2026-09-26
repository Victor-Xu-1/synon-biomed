package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"synon-go/internal/httpreliability"
	"synon-go/internal/tools/securefetch"
)

type agentPublicScientificTransferInterrupted struct {
	Cause           error
	BytesRetained   int64
	Resumable       bool
	Attempts        int
	StalledAttempts int
	RetryNotBefore  time.Time
}

func (failure *agentPublicScientificTransferInterrupted) Error() string {
	if failure.StalledAttempts == 0 {
		return "public scientific file transfer is waiting for its recorded retry time; retained bytes are preserved"
	}
	return fmt.Sprintf("public scientific file transfer stopped after %d consecutive attempts without retained progress; %d bytes preserved", failure.StalledAttempts, failure.BytesRetained)
}
func (failure *agentPublicScientificTransferInterrupted) Unwrap() error { return failure.Cause }

func agentPublicScientificTransferErrorCode(err error) string {
	if status, ok := securefetch.HTTPStatus(err); ok {
		return fmt.Sprintf("http_%d", status)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "response_truncated"
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "network_timeout"
	}
	return "transport_failed"
}

func isRetryableAgentPublicScientificDownloadError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || securefetch.IsCode(err, securefetch.CodeTransport) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	status, ok := securefetch.HTTPStatus(err)
	return ok && httpreliability.TransientStatus(status)
}
