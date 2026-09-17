package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	maxHostCallPayloadBytes = 48 * 1024 * 1024
	maxHostResultBytes      = 4 * 1024 * 1024
	defaultHostCallLimit    = 32
	defaultHostCallTimeout  = 15 * time.Second
	maxHostCallTimeout      = 24 * time.Hour
)

var hostCallIDPattern = regexp.MustCompile(`^hc-[0-9a-f]{32}$`)

// HostCall is emitted by a live kernel while a cell is executing. Identity is
// deliberately absent: frame, root, project, and user are bound by the Go
// execution context and can never be supplied by kernel code.
type HostCall struct {
	ID     string
	CellID string
	Method string
	Args   []any
	Kwargs map[string]any
}

type HostCallHandler func(context.Context, HostCall) (any, error)

// HostCallPolicy is attached to one Submit request. No policy means no host
// methods are available to that cell.
type HostCallPolicy struct {
	Handler        HostCallHandler
	AllowedMethods []string
	MaxCalls       int
	CallTimeout    time.Duration
}

type HostCallError struct {
	Code    string
	Message string
}

func (e *HostCallError) Error() string {
	if e == nil {
		return "host call failed"
	}
	return e.Message
}

func NewHostCallError(code, message string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "internal"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "host call failed"
	}
	return &HostCallError{Code: code, Message: message}
}

type hostCallWire struct {
	Type   string         `json:"type"`
	ID     string         `json:"id"`
	CellID string         `json:"cell_id"`
	Method string         `json:"method"`
	Args   []any          `json:"args"`
	Kwargs map[string]any `json:"kwargs"`
}

type hostResultWire struct {
	Type   string           `json:"type"`
	ID     string           `json:"id"`
	CellID string           `json:"cell_id"`
	OK     bool             `json:"ok"`
	Result any              `json:"result,omitempty"`
	Error  *hostResultError `json:"error,omitempty"`
}

type hostResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func decodeHostCall(line []byte) (HostCall, error) {
	if len(line) == 0 || len(line) > maxHostCallPayloadBytes {
		return HostCall{}, NewHostCallError("payload_too_large", fmt.Sprintf("host call payload exceeds %d bytes", maxHostCallPayloadBytes))
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var wire hostCallWire
	if err := decoder.Decode(&wire); err != nil {
		return HostCall{}, NewHostCallError("invalid_request", "invalid host_call frame: "+err.Error())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return HostCall{}, NewHostCallError("invalid_request", "host_call must contain one JSON object")
	}
	if wire.Type != "host_call" {
		return HostCall{}, NewHostCallError("invalid_request", "protocol frame is not a host_call")
	}
	if !hostCallIDPattern.MatchString(wire.ID) {
		return HostCall{}, NewHostCallError("invalid_request", "host call id must match hc- followed by 32 lowercase hex characters")
	}
	if strings.TrimSpace(wire.CellID) == "" || len(wire.CellID) > 128 || strings.ContainsAny(wire.CellID, "\x00\r\n") {
		return HostCall{}, NewHostCallError("invalid_request", "host call cell id is invalid")
	}
	if wire.Method != strings.TrimSpace(wire.Method) || wire.Method == "" || len(wire.Method) > 128 || strings.ContainsAny(wire.Method, "\x00\r\n") {
		return HostCall{}, NewHostCallError("invalid_request", "host call method is invalid")
	}
	if wire.Args == nil {
		wire.Args = []any{}
	}
	if wire.Kwargs == nil {
		wire.Kwargs = map[string]any{}
	}
	for key := range wire.Kwargs {
		if key == "" || len(key) > 128 || strings.ContainsAny(key, "\x00\r\n") {
			return HostCall{}, NewHostCallError("invalid_request", "host call keyword is invalid")
		}
	}
	return HostCall{ID: wire.ID, CellID: wire.CellID, Method: wire.Method, Args: wire.Args, Kwargs: wire.Kwargs}, nil
}

func normalizeHostCallPolicy(input *HostCallPolicy) normalizedHostCallPolicy {
	result := normalizedHostCallPolicy{allowed: map[string]struct{}{}, maxCalls: defaultHostCallLimit, timeout: defaultHostCallTimeout}
	if input == nil {
		return result
	}
	result.handler = input.Handler
	if input.MaxCalls > 0 && input.MaxCalls <= 1024 {
		result.maxCalls = input.MaxCalls
	}
	if input.CallTimeout > 0 && input.CallTimeout <= maxHostCallTimeout {
		result.timeout = input.CallTimeout
	}
	for _, method := range input.AllowedMethods {
		method = strings.TrimSpace(method)
		if method != "" && len(method) <= 128 && !strings.ContainsAny(method, "\x00\r\n") {
			result.allowed[method] = struct{}{}
		}
	}
	return result
}

type normalizedHostCallPolicy struct {
	handler  HostCallHandler
	allowed  map[string]struct{}
	maxCalls int
	timeout  time.Duration
}

type hostCallCompletion struct {
	call   HostCall
	result any
	err    error
}

func executeHostCall(parent context.Context, policy normalizedHostCallPolicy, call HostCall, completed chan<- hostCallCompletion) {
	ctx, cancel := context.WithTimeout(parent, policy.timeout)
	defer cancel()
	completion := hostCallCompletion{call: call}
	defer func() {
		if recovered := recover(); recovered != nil {
			completion.result = nil
			completion.err = NewHostCallError("internal", "host call handler panicked")
		}
		// Deliver the completion even when the host context was cancelled.
		// A host-call-only cancel (no SIGINT) must still unblock the worker so
		// the cell can observe the cancellation and exit cleanly. The channel
		// is buffered, so a stale completion after the loop exits is harmless.
		select {
		case completed <- completion:
		default:
		}
	}()
	completion.result, completion.err = policy.handler(ctx, call)
}

func hostCallFailure(err error) *hostResultError {
	if err == nil {
		return nil
	}
	var typed *HostCallError
	if errors.As(err, &typed) {
		return &hostResultError{Code: typed.Code, Message: typed.Message}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &hostResultError{Code: "deadline_exceeded", Message: "host call deadline exceeded"}
	}
	if errors.Is(err, context.Canceled) {
		return &hostResultError{Code: "cancelled", Message: "host call was cancelled"}
	}
	return &hostResultError{Code: "internal", Message: "host call failed"}
}
