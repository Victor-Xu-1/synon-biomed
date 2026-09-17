//go:build linux

package shellops

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLinuxSandboxEnforcesFilesystemNetworkAndEnvironment(t *testing.T) {
	status := SandboxStatus()
	if !status.Available || status.Network != "denied" || !strings.Contains(status.Mode, "landlock") {
		t.Fatalf("sandbox status = %#v", status)
	}
	root := t.TempDir()
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("must-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}

	write, err := Execute(context.Background(), root, "/bin/sh", []string{"-c", "printf ignored > /dev/null; printf allowed > written.txt"}, ".", 5)
	if err != nil || write.ExitCode != 0 {
		t.Fatalf("workspace write result=%#v err=%v", write, err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "written.txt")); err != nil || string(raw) != "allowed" {
		t.Fatalf("workspace write=%q err=%v", raw, err)
	}

	read, err := Execute(context.Background(), root, "/bin/cat", []string{secretPath}, ".", 5)
	if err == nil || read.ExitCode == 0 || strings.Contains(read.Stdout, "must-not-read") {
		t.Fatalf("outside read result=%#v err=%v", read, err)
	}

	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	environment, err := ExecuteWithInputEnv(context.Background(), root, os.Args[0], []string{"-test.run=TestLinuxSandboxEnvironmentHelper"}, ".", 5, "", map[string]string{"GO_SHELL_ENV_SANDBOX_HELPER": "1"})
	if err != nil || environment.Stdout != "clean" || strings.Contains(environment.Stdout, "must-not-leak") {
		t.Fatalf("sanitized environment result=%#v err=%v", environment, err)
	}
	if _, err := ExecuteWithInputEnv(context.Background(), root, "/usr/bin/env", nil, ".", 5, "", map[string]string{"EXTRA_API_TOKEN": "denied"}); err == nil {
		t.Fatal("sensitive explicit environment variable was accepted")
	}

	network, err := ExecuteWithInputEnv(context.Background(), root, os.Args[0], []string{"-test.run=TestLinuxSandboxNetworkHelper"}, ".", 5, "", map[string]string{"GO_SHELL_NETWORK_SANDBOX_HELPER": "1"})
	if err != nil || strings.TrimSpace(network.Stdout) != "network-denied" {
		t.Fatalf("network isolation result=%#v err=%v", network, err)
	}
}

func TestLinuxSandboxKillStopsProcessTree(t *testing.T) {
	root := t.TempDir()
	running, err := StartShellCommand(context.Background(), root, "Bash", "sleep 60 & echo $! > child.pid; wait", ".")
	if err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(root, "child.pid")
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid <= 0 {
		_ = running.Kill()
		_, _ = running.Wait()
		t.Fatal("background child pid was not recorded")
	}
	if err := running.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = running.Wait()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("sandbox child process %d survived process-tree kill", pid)
}

func TestLinuxSandboxEnvironmentHelper(t *testing.T) {
	if os.Getenv("GO_SHELL_ENV_SANDBOX_HELPER") != "1" {
		return
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		_, _ = os.Stdout.WriteString("leaked")
		os.Exit(3)
	}
	_, _ = os.Stdout.WriteString("clean")
	os.Exit(0)
}

func TestLinuxSandboxNetworkHelper(t *testing.T) {
	if os.Getenv("GO_SHELL_NETWORK_SANDBOX_HELPER") != "1" {
		return
	}
	connection, err := net.DialTimeout("tcp", "127.0.0.1:9", 100*time.Millisecond)
	if connection != nil {
		_ = connection.Close()
	}
	if err != nil && (errors.Is(err, syscall.EPERM) || os.IsPermission(err)) {
		_, _ = os.Stdout.WriteString("network-denied")
		os.Exit(0)
	}
	_, _ = os.Stdout.WriteString("network-not-isolated")
	os.Exit(4)
}
