package detached

import (
	"testing"
	"time"
)

func TestExecutorShouldReapIdleOnlyWhenNoExecutionIsActive(t *testing.T) {
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	lastActivity := now.Add(-16 * time.Minute)
	timeout := 15 * time.Minute

	if !executorShouldReapIdle(lastActivity, 0, now, timeout) {
		t.Fatal("idle executor should be reaped after the configured timeout")
	}
	if executorShouldReapIdle(lastActivity, 1, now, timeout) {
		t.Fatal("executor with an active execution must not be reaped")
	}
	if executorShouldReapIdle(now.Add(-14*time.Minute), 0, now, timeout) {
		t.Fatal("executor inside the idle grace period must remain alive")
	}
	if executorShouldReapIdle(now.Add(-time.Minute), 0, now, timeout) {
		t.Fatal("a clock that has not reached the timeout must not reap the executor")
	}
}

func TestExecutorShouldReapIdleRejectsInvalidInputs(t *testing.T) {
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	if executorShouldReapIdle(now.Add(-time.Hour), 0, now, 0) {
		t.Fatal("non-positive timeout must disable idle reaping")
	}
	if executorShouldReapIdle(time.Time{}, 0, now, 15*time.Minute) {
		t.Fatal("missing activity timestamp must not trigger idle reaping")
	}
}
