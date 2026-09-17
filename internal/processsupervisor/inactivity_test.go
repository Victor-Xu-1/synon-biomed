package processsupervisor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInactivityWatchdogTerminatesAStalledProcess(t *testing.T) {
	done := make(chan error, 1)
	watchdog := NewInactivityWatchdog(40 * time.Millisecond)
	terminated := false
	err := watchdog.Wait(context.Background(), 0, done, func() error {
		terminated = true
		done <- errors.New("terminated fixture")
		return nil
	})
	var inactivity *InactivityError
	if !terminated || !errors.As(err, &inactivity) || inactivity.Duration != 40*time.Millisecond {
		t.Fatalf("terminated=%v err=%v inactivity=%#v", terminated, err, inactivity)
	}
}

func TestInactivityWatchdogExtendsOnlyOnObservedActivity(t *testing.T) {
	done := make(chan error, 1)
	watchdog := NewInactivityWatchdog(80 * time.Millisecond)
	go func() {
		for range 4 {
			time.Sleep(25 * time.Millisecond)
			watchdog.MarkActivity()
		}
		time.Sleep(20 * time.Millisecond)
		done <- nil
	}()
	if err := watchdog.Wait(context.Background(), 0, done, func() error {
		return errors.New("active fixture was terminated")
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessActivityRequiresARealCounterOrLifecycleChange(t *testing.T) {
	identity := processActivityIdentity{PID: 42, StartTicks: 7}
	baseline := processActivitySnapshot{Available: true, Processes: map[processActivityIdentity]processActivityCounters{
		identity: {CPU: 10, IO: 20},
	}}
	unchanged := processActivitySnapshot{Available: true, Processes: map[processActivityIdentity]processActivityCounters{
		identity: {CPU: 10, IO: 20},
	}}
	advanced := processActivitySnapshot{Available: true, Processes: map[processActivityIdentity]processActivityCounters{
		identity: {CPU: 11, IO: 20},
	}}
	if processActivityAdvanced(baseline, unchanged) || !processActivityAdvanced(baseline, advanced) ||
		!processActivityAdvanced(baseline, processActivitySnapshot{}) {
		t.Fatalf("activity comparison baseline=%#v unchanged=%#v advanced=%#v", baseline, unchanged, advanced)
	}
}
