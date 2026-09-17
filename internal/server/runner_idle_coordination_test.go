package server

import (
	"testing"
	"time"
)

func TestNextRunnerIdlePollIntervalBacksOffAndCaps(t *testing.T) {
	base := 250 * time.Millisecond
	cases := []struct {
		current time.Duration
		want    time.Duration
	}{
		{current: 0, want: base},
		{current: base, want: 500 * time.Millisecond},
		{current: 16 * time.Second, want: maxRunnerIdlePollInterval},
		{current: maxRunnerIdlePollInterval, want: maxRunnerIdlePollInterval},
	}
	for _, testCase := range cases {
		if got := nextRunnerIdlePollInterval(testCase.current, base); got != testCase.want {
			t.Fatalf("nextRunnerIdlePollInterval(%s, %s)=%s want %s", testCase.current, base, got, testCase.want)
		}
	}
}

func TestFrameResumeDispatchWakeOnlySignalsRunnableEvents(t *testing.T) {
	server := &Server{}
	ignored := server.frameResumeDispatchWakeChannel()
	server.signalFrameResumeDispatchForEvent("assistant_delta")
	select {
	case <-ignored:
		t.Fatal("unrelated workspace event woke frame resume dispatchers")
	default:
	}

	server.signalFrameResumeDispatchForEvent("frame_resumed")
	select {
	case <-ignored:
	default:
		t.Fatal("frame_resumed did not wake frame resume dispatchers")
	}

	next := server.frameResumeDispatchWakeChannel()
	if next == ignored {
		t.Fatal("frame resume wake generation was not replaced")
	}
	server.signalFrameResumeDispatchForEvent("frame_resume_dispatch_woken")
	select {
	case <-next:
	default:
		t.Fatal("frame_resume_dispatch_woken did not wake frame resume dispatchers")
	}

	finished := server.frameResumeDispatchWakeChannel()
	server.signalFrameResumeDispatchForEvent("runner_finished")
	select {
	case <-finished:
	default:
		t.Fatal("runner_finished did not wake frame resume recovery")
	}
}
