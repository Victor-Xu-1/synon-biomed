//go:build windows

package kernel

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestWorkerProcessContextCancellationDuringWindowsLaunch(t *testing.T) {
	if os.Getenv("SYNON_KERNEL_CANCEL_LAUNCH_HELPER") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	for attempt := 0; attempt < 16; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		command := newWorkerProcessCommand(ctx, os.Args[0], "-test.run=^TestWorkerProcessContextCancellationDuringWindowsLaunch$")
		command.Env = append(os.Environ(), "SYNON_KERNEL_CANCEL_LAUNCH_HELPER=1")
		cancelled := make(chan struct{})
		if attempt%2 == 0 {
			go func() { runtime.Gosched(); cancel(); close(cancelled) }()
		}
		process, err := startWorkerProcess(command)
		if attempt%2 != 0 {
			cancel()
			close(cancelled)
			if err != nil {
				t.Fatalf("uncancelled start failed: %v", err)
			}
		}
		<-cancelled
		if err != nil {
			continue
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = process.kill()
			process.close()
			t.Fatal("Windows launch cancellation left a running child")
		}
		process.close()
	}
}
