//go:build linux

package kernel

import (
	"os"
	"testing"
)

func TestCurrentProcessStartTicksFencesPIDReuse(t *testing.T) {
	first, err := CurrentProcessStartTicks()
	if err != nil || first <= 0 {
		t.Fatalf("current process start ticks=%d err=%v", first, err)
	}
	second, err := linuxProcessStartTicks(os.Getpid())
	if err != nil || second != first {
		t.Fatalf("re-read process start ticks=%d want=%d err=%v", second, first, err)
	}
	if _, err := linuxProcessStartTicks(-1); err == nil {
		t.Fatal("negative process id was accepted")
	}
}

func TestProcessIdentityAliveRejectsMissingAndReusedProcesses(t *testing.T) {
	startTicks, err := CurrentProcessStartTicks()
	if err != nil {
		t.Fatal(err)
	}
	alive, err := ProcessIdentityAlive(int64(os.Getpid()), startTicks)
	if err != nil || !alive {
		t.Fatalf("current process alive=%t err=%v", alive, err)
	}
	alive, err = ProcessIdentityAlive(int64(os.Getpid()), startTicks+1)
	if err != nil || alive {
		t.Fatalf("reused process identity alive=%t err=%v", alive, err)
	}
	alive, err = ProcessIdentityAlive(1<<30, 1)
	if err != nil || alive {
		t.Fatalf("missing process identity alive=%t err=%v", alive, err)
	}
}
