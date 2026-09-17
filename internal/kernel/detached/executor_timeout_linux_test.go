//go:build linux

package detached

import (
	"testing"
	"time"
)

func TestDetachedExecutionTimeoutDefaultsAndPreservesDurableBudget(t *testing.T) {
	if got := detachedExecutionTimeout(0); got != 0 {
		t.Fatalf("unbounded timeout=%s, want zero", got)
	}
	if got := detachedExecutionTimeout(75000); got != 75*time.Second {
		t.Fatalf("durable timeout=%s, want 75s", got)
	}
}
