//go:build windows

package kernel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// This opt-in probe exercises a real installed interpreter and current worker
// assets without site startup. It does not establish managed-environment or
// production (-I) readiness, which also needs site-packages and temp paths.
func windowsInstalledPythonProbe(t *testing.T) (string, string) {
	t.Helper()
	python := os.Getenv("SYNON_TEST_WINDOWS_PYTHON_EXE")
	assets := os.Getenv("SYNON_TEST_WINDOWS_KERNEL_ASSETS")
	if python == "" || assets == "" {
		t.Skip("set SYNON_TEST_WINDOWS_PYTHON_EXE and SYNON_TEST_WINDOWS_KERNEL_ASSETS for the native interpreter probe")
	}
	for _, path := range []string{python, filepath.Join(assets, "kernel_worker.py")} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("native interpreter probe asset %q is unavailable: %v", path, err)
		}
	}
	return python, assets
}

func windowsInstalledPythonCommand(t *testing.T, python, assets, workspace, code string) *exec.Cmd {
	t.Helper()
	request, err := json.Marshal(map[string]any{
		"id": "native-probe", "code": code, "workspace_dir": workspace, "host_enabled": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err := newWindowsConfinedCommand(workspace, python,
		[]string{"-I", "-S", "-u", filepath.Join(assets, "kernel_worker.py")},
		[]string{"PYTHONNOUSERSITE=1", "TMPDIR=" + workspace},
		[]string{filepath.Dir(python), assets})
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = strings.NewReader(string(request) + "\n")
	return command
}

func TestWindowsAppContainerInstalledPythonProtocolNoSite(t *testing.T) {
	python, assets := windowsInstalledPythonProbe(t)
	t.Setenv("SYNON_HOME", t.TempDir())
	workspace := t.TempDir()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	code := fmt.Sprintf("import socket\nprint(123)\ntry:\n    socket.create_connection(('127.0.0.1', %d), timeout=1)\n    print('connect-allowed')\nexcept OSError:\n    print('connect-denied')\n", listener.Addr().(*net.TCPAddr).Port)
	command := windowsInstalledPythonCommand(t, python, assets, workspace, code)
	request, err := windowsConfinedRequestFromCommand(command)
	if err != nil || request == nil {
		t.Fatalf("decode native interpreter request: %v", err)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	process, err := startWorkerProcess(command)
	if err != nil {
		t.Fatal(err)
	}
	waitErr := command.Wait()
	process.close()
	if waitErr != nil || process.cleanupErr != nil {
		t.Fatalf("real Python worker failed: wait=%v cleanup=%v stderr=%q", waitErr, process.cleanupErr, stderr.String())
	}
	started, terminal := false, Response{}
	for _, line := range bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n")) {
		var frame map[string]json.RawMessage
		if err := json.Unmarshal(bytes.TrimSpace(line), &frame); err != nil {
			t.Fatalf("invalid worker protocol frame: %v output=%q", err, stdout.String())
		}
		if string(frame["type"]) == `"execution_started"` {
			started = true
		}
		if frame["stdout"] != nil {
			if err := json.Unmarshal(line, &terminal); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !started || terminal.ID != "native-probe" || terminal.Error != "" ||
		!strings.Contains(terminal.Stdout, "123") || !strings.Contains(terminal.Stdout, "connect-denied") ||
		strings.Contains(terminal.Stdout, "connect-allowed") {
		t.Fatalf("native worker protocol/network contract failed: started=%v terminal=%+v stderr=%q", started, terminal, stderr.String())
	}
	sid, err := deriveWindowsAppContainerSID(request.ProfileName)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.FreeSid(sid)
	for _, path := range []string{
		workspace, filepath.Dir(python), python,
		filepath.Join(filepath.Dir(python), "Lib", "encodings", "__init__.py"),
		assets, filepath.Join(assets, "kernel_worker.py"),
	} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if windowsTestPathHasSIDGrant(t, path, sid) {
			t.Fatalf("native interpreter grant remained on %q", path)
		}
	}
}

func TestWindowsAppContainerInstalledPythonCancellationNoSite(t *testing.T) {
	python, assets := windowsInstalledPythonProbe(t)
	t.Setenv("SYNON_HOME", t.TempDir())
	workspace := t.TempDir()
	command := windowsInstalledPythonCommand(t, python, assets, workspace, "import time\ntime.sleep(30)\n")
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	command.Stdout = writer
	var stderr bytes.Buffer
	command.Stderr = &stderr
	process, err := startWorkerProcess(command)
	if err != nil {
		t.Fatal(err)
	}
	settled := false
	defer func() {
		if !settled {
			_ = process.kill()
			_ = command.Wait()
			process.close()
		}
	}()
	started := make(chan struct{}, 1)
	readDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			var frame struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(scanner.Bytes(), &frame) == nil && frame.Type == "execution_started" {
				select {
				case started <- struct{}{}:
				default:
				}
			}
		}
		readDone <- scanner.Err()
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		_ = process.kill()
		_ = command.Wait()
		process.close()
		settled = true
		t.Fatalf("Python worker did not acknowledge active execution: %q", stderr.String())
	}
	if err := process.kill(); err != nil {
		t.Fatalf("terminate active Python worker: %v", err)
	}
	_ = command.Wait()
	process.close()
	settled = true
	_ = writer.Close()
	if err := <-readDone; err != nil || process.cleanupErr != nil {
		t.Fatalf("Python cancellation did not settle: read=%v cleanup=%v stderr=%q", err, process.cleanupErr, stderr.String())
	}
}
