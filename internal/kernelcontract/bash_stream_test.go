package kernelcontract

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashWrapperDeliversSmallOutputBeforeProcessExit(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required")
	}
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash is required")
	}
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	// The child cannot finish until the test observes its first output.
	command := "printf '检查进度\\n'; while [ ! -f '" + release + "' ]; do sleep 0.02; done; printf 'finished\\n'"
	source, err := BashPythonWrapper(command)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, python, "-u", "-c", source)
	stdout, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(release, nil, 0600)
		_ = process.Wait()
	}()
	lines := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	select {
	case line := <-lines:
		if line != "检查进度" {
			t.Fatalf("UTF-8 output=%q", line)
		}
	case <-ctx.Done():
		// Release the shell before failure so no child is left behind.
		_ = os.WriteFile(release, nil, 0600)
		t.Fatal("small output was withheld until process exit")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-lines:
		if !strings.Contains(line, "finished") {
			t.Fatalf("trailing output=%q", line)
		}
	case <-ctx.Done():
		t.Fatal("missing trailing output")
	}
}

func TestBashWrapperPreservesSplitUTF8AndExitStatus(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required")
	}
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash is required")
	}
	// Each three-byte character crosses separate pipe reads on both streams.
	command := "printf '\\344'; printf '\\345' >&2; sleep 0.03; printf '\\270\\255'; printf '\\217\\226' >&2; /bin/sh -c 'exit 7'"
	source, err := BashPythonWrapper(command)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, python, "-u", "-c", source)
	var stderr strings.Builder
	process.Stderr = &stderr
	stdout, err := process.Output()
	if err != nil || string(stdout) != "中" || stderr.String() != "取\n"+BashExitPrefix+"7\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr.String(), err)
	}
}
