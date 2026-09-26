//go:build windows

package processsupervisor

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsSilentFileIOKeepsInstallerWatchdogAlive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.bin")
	command := exec.Command(os.Args[0], "-test.run=^TestWindowsSilentFileIOHelper$")
	command.Env = append(os.Environ(), "SYNON_SILENT_IO_TEST_PATH="+path)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	started := time.Now()
	watchdog := NewInactivityWatchdog(180 * time.Millisecond)
	if err := watchdog.Wait(context.Background(), command.Process.Pid, done, command.Process.Kill); err != nil {
		t.Fatalf("silent file I/O did not extend the activity window: %v", err)
	}
	if time.Since(started) < 450*time.Millisecond {
		t.Fatal("helper exited before the inactivity window was exercised")
	}
}

func TestWindowsSilentFileIOHelper(t *testing.T) {
	path := os.Getenv("SYNON_SILENT_IO_TEST_PATH")
	if path == "" {
		t.Skip("launched only by the process-activity integration test")
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data := make([]byte, 4096)
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
