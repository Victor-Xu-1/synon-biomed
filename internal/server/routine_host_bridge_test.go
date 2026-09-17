package server

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRoutineHostConfigureExactV11Boundaries(t *testing.T) {
	label := "tracking"
	parsed, err := ParseRoutineHostConfigure([]any{5}, map[string]any{"on_tick": "continue", "label": label})
	if err != nil || parsed.EveryMinutes != 5 || parsed.OnTick != "continue" || parsed.Label == nil || *parsed.Label != label {
		t.Fatalf("minimum configure = %#v err=%v", parsed, err)
	}
	if _, err := ParseRoutineHostConfigure([]any{1440}, map[string]any{"on_tick": strings.Repeat("界", 2000), "label": nil}); err != nil {
		t.Fatalf("maximum configure: %v", err)
	}
	if parsed, err := ParseRoutineHostConfigure(nil, map[string]any{"every_minutes": 5, "on_tick": "keyword"}); err != nil || parsed.EveryMinutes != 5 {
		t.Fatalf("keyword every_minutes = %#v err=%v", parsed, err)
	}
	invalid := []struct {
		name   string
		args   []any
		kwargs map[string]any
	}{
		{"bool interval", []any{true}, map[string]any{"on_tick": "x"}},
		{"float interval", []any{5.0}, map[string]any{"on_tick": "x"}},
		{"below interval", []any{4}, map[string]any{"on_tick": "x"}},
		{"above interval", []any{1441}, map[string]any{"on_tick": "x"}},
		{"on_tick positional", []any{5, "x"}, nil},
		{"missing on_tick", []any{5}, nil},
		{"empty on_tick", []any{5}, map[string]any{"on_tick": " \t"}},
		{"non-string on_tick", []any{5}, map[string]any{"on_tick": 1}},
		{"long on_tick", []any{5}, map[string]any{"on_tick": strings.Repeat("界", 2001)}},
		{"non-string label", []any{5}, map[string]any{"on_tick": "x", "label": false}},
		{"long label", []any{5}, map[string]any{"on_tick": "x", "label": strings.Repeat("界", 121)}},
		{"unknown keyword", []any{5}, map[string]any{"on_tick": "x", "extra": true}},
		{"duplicate interval", []any{5}, map[string]any{"every_minutes": 5, "on_tick": "x"}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseRoutineHostConfigure(test.args, test.kwargs); err == nil {
				t.Fatal("expected strict configure validation error")
			}
		})
	}
}

func TestRoutineHostStatusAndDoneExactV11Boundaries(t *testing.T) {
	if err := ParseRoutineHostStatus(nil, nil); err != nil {
		t.Fatalf("status(): %v", err)
	}
	if err := ParseRoutineHostStatus([]any{1}, nil); err == nil {
		t.Fatal("status accepted positional input")
	}
	if err := ParseRoutineHostStatus(nil, map[string]any{"extra": true}); err == nil {
		t.Fatal("status accepted keyword input")
	}
	parsed, err := ParseRoutineHostDone([]any{true}, nil)
	if err != nil || !parsed.HadWork || parsed.Summary != "" {
		t.Fatalf("done default = %#v err=%v", parsed, err)
	}
	parsed, err = ParseRoutineHostDone([]any{false}, map[string]any{"summary": strings.Repeat("结", 500)})
	if err != nil || parsed.HadWork || len([]rune(parsed.Summary)) != 500 {
		t.Fatalf("done max = %#v err=%v", parsed, err)
	}
	parsed, err = ParseRoutineHostDone(nil, map[string]any{"had_work": true, "summary": "keyword"})
	if err != nil || !parsed.HadWork || parsed.Summary != "keyword" {
		t.Fatalf("done keywords = %#v err=%v", parsed, err)
	}
	invalid := []struct {
		args   []any
		kwargs map[string]any
	}{
		{[]any{1}, nil},
		{[]any{true, false}, nil},
		{[]any{true}, map[string]any{"summary": strings.Repeat("结", 501)}},
		{[]any{true, "x"}, map[string]any{"summary": "duplicate"}},
		{nil, nil},
		{[]any{true}, map[string]any{"extra": "x"}},
		{[]any{true}, map[string]any{"had_work": true}},
	}
	for index, test := range invalid {
		if _, err := ParseRoutineHostDone(test.args, test.kwargs); err == nil {
			t.Fatalf("invalid done case %d was accepted", index)
		}
	}
}

func TestRoutineHostErrorOnlyResultBecomesTypedRuntimeError(t *testing.T) {
	_, err := DecodeRoutineHostResult("status", map[string]any{"error": "not configured"})
	var runtimeErr *RoutineHostRuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Method != "status" || runtimeErr.Message != "not configured" {
		t.Fatalf("runtime error = %#v", err)
	}
	result, err := DecodeRoutineHostResult("status", map[string]any{"error": "warning", "configured": false})
	if err != nil || result["configured"] != false {
		t.Fatalf("non-error-only result = %#v err=%v", result, err)
	}
	_, err = UnavailableRoutineHostBridge{}.Status(context.Background())
	if !errors.Is(err, ErrRoutineKernelHostTransportUnavailable) {
		t.Fatalf("unavailable bridge error = %v", err)
	}
}
