package httpreliability

import (
	"context"
	"errors"
	"net"
)

// Receipt records the actual HTTP attempt, never a generated explanation.
// URLs, proxy values, headers and response bodies are intentionally absent.
type Receipt struct {
	Attempt           int     `json:"attempt"`
	Outcome           string  `json:"outcome"`
	StatusCode        int     `json:"status_code,omitempty"`
	DurationMS        int64   `json:"duration_ms"`
	RetryAfterSeconds float64 `json:"retry_after_seconds,omitempty"`
}

func ErrorKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrResponseHeadersTimeout):
		return "response_headers_timeout"
	case errors.Is(err, ErrBodyIdleTimeout):
		return "body_idle_timeout"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation_deadline"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns_error"
	}
	var operation *net.OpError
	if errors.As(err, &operation) && operation.Op == "dial" {
		return "connect_error"
	}
	return "transport_error"
}
