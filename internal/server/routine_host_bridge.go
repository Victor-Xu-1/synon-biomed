package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

var ErrRoutineKernelHostTransportUnavailable = errors.New("Kernel host.routine transport is not connected")

type RoutineHostConfigure struct {
	EveryMinutes int
	OnTick       string
	Label        *string
}

type RoutineHostDone struct {
	HadWork bool
	Summary string
}

type RoutineHostStatus struct {
	Configured   bool
	Enabled      bool
	EveryMinutes int
	OnTick       string
	Label        string
	TickCount    int
	IdleStreak   int
}

// RoutineHostBridge is the typed host.routine contract awaiting attachment to
// the Kernel host-call transport. It intentionally has no fallback behavior.
type RoutineHostBridge interface {
	Configure(context.Context, RoutineHostConfigure) (map[string]any, error)
	Status(context.Context) (RoutineHostStatus, error)
	Done(context.Context, RoutineHostDone) (map[string]any, error)
}

type UnavailableRoutineHostBridge struct{}

func (UnavailableRoutineHostBridge) Configure(context.Context, RoutineHostConfigure) (map[string]any, error) {
	return nil, ErrRoutineKernelHostTransportUnavailable
}

func (UnavailableRoutineHostBridge) Status(context.Context) (RoutineHostStatus, error) {
	return RoutineHostStatus{}, ErrRoutineKernelHostTransportUnavailable
}

func (UnavailableRoutineHostBridge) Done(context.Context, RoutineHostDone) (map[string]any, error) {
	return nil, ErrRoutineKernelHostTransportUnavailable
}

// ParseRoutineHostConfigure enforces the v1.1 Python signature
// configure(every_minutes, *, on_tick, label=None).
func ParseRoutineHostConfigure(args []any, kwargs map[string]any) (RoutineHostConfigure, error) {
	if len(args) > 1 {
		return RoutineHostConfigure{}, fmt.Errorf("host.routine.configure: expected at most one positional argument every_minutes, got %d", len(args))
	}
	if err := rejectUnknownRoutineKeywords(kwargs, "every_minutes", "on_tick", "label"); err != nil {
		return RoutineHostConfigure{}, fmt.Errorf("host.routine.configure: %w", err)
	}
	var everyMinutesValue any
	if len(args) == 1 {
		if _, duplicated := kwargs["every_minutes"]; duplicated {
			return RoutineHostConfigure{}, errors.New("host.routine.configure: every_minutes was provided more than once")
		}
		everyMinutesValue = args[0]
	} else {
		var exists bool
		everyMinutesValue, exists = kwargs["every_minutes"]
		if !exists {
			return RoutineHostConfigure{}, errors.New("host.routine.configure: every_minutes is required")
		}
	}
	everyMinutes, ok := strictHostInteger(everyMinutesValue)
	if !ok {
		return RoutineHostConfigure{}, fmt.Errorf("host.routine.configure: every_minutes must be an int (minutes, 5-1440), got %T", everyMinutesValue)
	}
	if everyMinutes < 5 || everyMinutes > 1440 {
		return RoutineHostConfigure{}, fmt.Errorf("host.routine.configure: every_minutes must be between 5 and 1440 (inclusive), got %d", everyMinutes)
	}
	onTickValue, exists := kwargs["on_tick"]
	if !exists {
		return RoutineHostConfigure{}, errors.New("host.routine.configure: on_tick is required and keyword-only")
	}
	onTick, ok := onTickValue.(string)
	if !ok || strings.TrimSpace(onTick) == "" {
		return RoutineHostConfigure{}, fmt.Errorf("host.routine.configure: on_tick must be a non-empty str, got %T", onTickValue)
	}
	if length := utf8.RuneCountInString(onTick); length > 2000 {
		return RoutineHostConfigure{}, fmt.Errorf("host.routine.configure: on_tick must be <= 2000 chars, got %d", length)
	}
	var label *string
	if labelValue, exists := kwargs["label"]; exists && labelValue != nil {
		text, ok := labelValue.(string)
		if !ok {
			return RoutineHostConfigure{}, fmt.Errorf("host.routine.configure: label must be a str or None, got %T", labelValue)
		}
		if length := utf8.RuneCountInString(text); length > 120 {
			return RoutineHostConfigure{}, fmt.Errorf("host.routine.configure: label must be <= 120 chars, got %d", length)
		}
		label = &text
	}
	return RoutineHostConfigure{EveryMinutes: everyMinutes, OnTick: onTick, Label: label}, nil
}

// ParseRoutineHostStatus enforces status() with no arguments.
func ParseRoutineHostStatus(args []any, kwargs map[string]any) error {
	if len(args) != 0 || len(kwargs) != 0 {
		return errors.New("host.routine.status: expected no arguments")
	}
	return nil
}

// ParseRoutineHostDone enforces done(had_work, summary) with an empty default summary.
func ParseRoutineHostDone(args []any, kwargs map[string]any) (RoutineHostDone, error) {
	if len(args) > 2 {
		return RoutineHostDone{}, fmt.Errorf("host.routine.done: expected had_work and optional summary, got %d positional arguments", len(args))
	}
	if err := rejectUnknownRoutineKeywords(kwargs, "had_work", "summary"); err != nil {
		return RoutineHostDone{}, fmt.Errorf("host.routine.done: %w", err)
	}
	var hadWorkValue any
	if len(args) >= 1 {
		if _, duplicated := kwargs["had_work"]; duplicated {
			return RoutineHostDone{}, errors.New("host.routine.done: had_work was provided more than once")
		}
		hadWorkValue = args[0]
	} else {
		var exists bool
		hadWorkValue, exists = kwargs["had_work"]
		if !exists {
			return RoutineHostDone{}, errors.New("host.routine.done: had_work is required")
		}
	}
	hadWork, ok := hadWorkValue.(bool)
	if !ok {
		return RoutineHostDone{}, fmt.Errorf("host.routine.done: had_work must be a bool, got %T", hadWorkValue)
	}
	if len(args) == 2 {
		if _, duplicated := kwargs["summary"]; duplicated {
			return RoutineHostDone{}, errors.New("host.routine.done: summary was provided more than once")
		}
	}
	summaryValue := any("")
	if len(args) == 2 {
		summaryValue = args[1]
	} else if value, exists := kwargs["summary"]; exists {
		summaryValue = value
	}
	summary, ok := summaryValue.(string)
	if !ok {
		return RoutineHostDone{}, fmt.Errorf("host.routine.done: summary must be a str, got %T", summaryValue)
	}
	if length := utf8.RuneCountInString(summary); length > 500 {
		return RoutineHostDone{}, fmt.Errorf("host.routine.done: summary must be <= 500 chars, got %d", length)
	}
	return RoutineHostDone{HadWork: hadWork, Summary: summary}, nil
}

type RoutineHostRuntimeError struct {
	Method  string
	Message string
}

func (e *RoutineHostRuntimeError) Error() string {
	return fmt.Sprintf("host.routine.%s: %s", e.Method, e.Message)
}

// DecodeRoutineHostResult mirrors v1.1: an error-only dictionary raises a
// RuntimeError, while dictionaries containing additional fields remain data.
func DecodeRoutineHostResult(method string, result map[string]any) (map[string]any, error) {
	if len(result) == 1 {
		if value, exists := result["error"]; exists {
			return nil, &RoutineHostRuntimeError{Method: strings.TrimSpace(method), Message: fmt.Sprint(value)}
		}
	}
	return result, nil
}

func strictHostInteger(value any) (int, bool) {
	var number int64
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		number = int64(typed)
	case int16:
		number = int64(typed)
	case int32:
		number = int64(typed)
	case int64:
		number = typed
	case uint:
		if uint64(typed) > math.MaxInt {
			return 0, false
		}
		return int(typed), true
	case uint8:
		number = int64(typed)
	case uint16:
		number = int64(typed)
	case uint32:
		number = int64(typed)
	case uint64:
		if typed > math.MaxInt {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
	if int64(int(number)) != number {
		return 0, false
	}
	return int(number), true
}

func rejectUnknownRoutineKeywords(kwargs map[string]any, allowed ...string) error {
	accepted := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		accepted[key] = struct{}{}
	}
	for key := range kwargs {
		if _, ok := accepted[key]; !ok {
			return fmt.Errorf("unexpected keyword argument %q", key)
		}
	}
	return nil
}

var _ RoutineHostBridge = UnavailableRoutineHostBridge{}
