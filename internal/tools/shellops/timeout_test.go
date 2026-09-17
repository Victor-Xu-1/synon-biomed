package shellops

import (
	"testing"
	"time"
)

func TestExplicitShellTimeoutIsNotSilentlyClamped(t *testing.T) {
	const week = 7 * 24 * time.Hour
	if got := timeoutFromSeconds(int64(week / time.Second)); got != week {
		t.Fatalf("seconds timeout = %s, want %s", got, week)
	}
	if got := timeoutFromMilliseconds(int64(week / time.Millisecond)); got != week {
		t.Fatalf("milliseconds timeout = %s, want %s", got, week)
	}
}

func TestMissingShellTimeoutRetainsShortExecutionUnitDefault(t *testing.T) {
	if got := timeoutFromSeconds(0); got != defaultTimeout {
		t.Fatalf("seconds default = %s, want %s", got, defaultTimeout)
	}
	if got := timeoutFromMilliseconds(0); got != defaultTimeout {
		t.Fatalf("milliseconds default = %s, want %s", got, defaultTimeout)
	}
}
