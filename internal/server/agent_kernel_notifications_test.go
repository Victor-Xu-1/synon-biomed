package server

import (
	"math"
	"testing"
	"time"
)

func TestAgentKernelNotificationTimeoutUsesBoundedWaitContract(t *testing.T) {
	tests := []struct {
		name      string
		input     map[string]any
		want      time.Duration
		wantError bool
	}{
		{name: "default", input: map[string]any{}, want: 30 * time.Second},
		{name: "peek", input: map[string]any{"timeout_seconds": float64(0)}, want: 0},
		{name: "negative peek", input: map[string]any{"timeout_seconds": float64(-1)}, want: 0},
		{name: "fractional", input: map[string]any{"timeout_seconds": float64(12.5)}, want: 12500 * time.Millisecond},
		{name: "exact maximum", input: map[string]any{"timeout_seconds": float64(1800)}, want: 30 * time.Minute},
		{name: "over maximum", input: map[string]any{"timeout_seconds": float64(1801)}, want: 30 * time.Minute},
		{name: "positive infinity", input: map[string]any{"timeout_seconds": math.Inf(1)}, want: 30 * time.Minute},
		{name: "wrong type", input: map[string]any{"timeout_seconds": "30"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := agentKernelNotificationTimeoutDuration(test.input)
			if (err != nil) != test.wantError {
				t.Fatalf("timeout=%s err=%v", got, err)
			}
			if err == nil && got != test.want {
				t.Fatalf("timeout=%s want=%s", got, test.want)
			}
		})
	}
}
