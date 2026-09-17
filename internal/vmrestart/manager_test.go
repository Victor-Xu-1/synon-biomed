package vmrestart

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestManagerPrepareWritesSecureServiceWithoutLeakingSecrets(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "synon-go")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	workingDirectory := filepath.Join(root, "work dir")
	if err := os.MkdirAll(workingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	var commands [][]string
	manager, err := New(Options{
		HomeDir: root, Executable: executable, WorkingDirectory: workingDirectory,
		UnitDirectory: filepath.Join(root, "user-units"),
		Distro:        "Ubuntu", User: "victor_1", ServiceName: "synon-go.service",
		Environment: map[string]string{
			"SYNON_HOME": root, "WECHAT_BOT_TOKEN": "secret-token",
		},
		RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			commands = append(commands, append([]string{name}, args...))
			return nil, nil
		},
		LaunchHost: func(context.Context, HostRestartRequest) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(info.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unit), "secret-token") || !strings.Contains(string(unit), "EnvironmentFile=") ||
		!strings.Contains(string(unit), "Restart=always") ||
		!strings.Contains(string(unit), "Description=Synon Biomed managed agent runtime") ||
		strings.Contains(string(unit), "Description=Synon Go") {
		t.Fatalf("unit = %s", unit)
	}
	wantWorkingDirectory := "WorkingDirectory=" + strings.ReplaceAll(workingDirectory, " ", "\\x20")
	if !strings.Contains(string(unit), wantWorkingDirectory) || strings.Contains(string(unit), `WorkingDirectory="`) {
		t.Fatalf("unit has invalid systemd path escaping: %s", unit)
	}
	environment, err := os.ReadFile(info.EnvironmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(environment), `WECHAT_BOT_TOKEN="secret-token"`) {
		t.Fatalf("environment = %s", environment)
	}
	for _, path := range []string{info.UnitPath, info.EnvironmentPath} {
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", path, stat.Mode().Perm())
		}
	}
	if len(commands) != 2 || strings.Join(commands[0], " ") != "systemctl --user daemon-reload" ||
		strings.Join(commands[1], " ") != "systemctl --user enable synon-go.service" {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestManagerRestartPersistsPendingAndRuntimeCompletion(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "synon-go")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	var launched HostRestartRequest
	options := Options{
		HomeDir: root, Executable: executable, WorkingDirectory: root,
		UnitDirectory: filepath.Join(root, "user-units"),
		Distro:        "Ubuntu", User: "victor_1", ServiceName: "synon-go.service",
		Environment: map[string]string{"SYNON_HOME": root},
		RunCommand:  func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		LaunchHost: func(_ context.Context, request HostRestartRequest) error {
			launched = request
			return nil
		},
	}
	manager, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Restart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StatePending || !manager.IsRestarting() || launched.Distro != "Ubuntu" ||
		launched.ServiceName != "synon-go.service" {
		t.Fatalf("status=%#v launched=%#v", status, launched)
	}
	restarted, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.IsRestarting() {
		t.Fatal("pending restart did not survive manager recreation")
	}
	if err := restarted.MarkRuntimeStarted(); err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Status()
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != StateCompleted || restarted.IsRestarting() || completed.CompletedAt.IsZero() {
		t.Fatalf("completed = %#v", completed)
	}
}

func TestPowerShellRestartScriptUsesArgumentsAndRetriesServiceStart(t *testing.T) {
	script, err := PowerShellRestartScript(HostRestartRequest{
		Distro: "Ubuntu", User: "victor_1", ServiceName: "synon-go.service",
		ShutdownDelaySeconds: 2, StartAttempts: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"wsl.exe", "--shutdown", "--distribution", "'Ubuntu'", "--user", "'victor_1'",
		"systemctl", "--user", "start", "'synon-go.service'", "Start-Sleep",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("script missing %q: %s", expected, script)
		}
	}
	if strings.Contains(script, "secret") {
		t.Fatalf("script unexpectedly contains secret material: %s", script)
	}
}

func TestNewRejectsUnsafeServiceAndSymlinkStateRoot(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "synon-go")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{
		HomeDir: root, Executable: executable, WorkingDirectory: root,
		Distro: "Ubuntu", User: "victor_1", ServiceName: "../bad.service",
	}); err == nil {
		t.Fatal("unsafe service name was accepted")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "systemd")); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Options{
		HomeDir: root, Executable: executable, WorkingDirectory: root,
		UnitDirectory: filepath.Join(root, "user-units"),
		Distro:        "Ubuntu", User: "victor_1", ServiceName: "synon-go.service",
		RunCommand: func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(context.Background()); err == nil {
		t.Fatal("symlink systemd state root was accepted")
	}
}

func TestManagerRestartUsesRealCommandProcessesAndPersistsLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real fixture process test requires Linux")
	}
	root := t.TempDir()
	fixtureDir := filepath.Join(root, "bin")
	if err := os.Mkdir(fixtureDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutableFixture(t, filepath.Join(fixtureDir, "systemctl"), `#!/bin/sh
set -eu
: "${SYNON_VM_RESTART_SYSTEMCTL_LOG:?}"
printf '%s\n' "$*" >> "$SYNON_VM_RESTART_SYSTEMCTL_LOG"
`)
	writeExecutableFixture(t, filepath.Join(fixtureDir, "powershell.exe"), `#!/bin/sh
set -eu
: "${SYNON_VM_RESTART_POWERSHELL_LOG:?}"
printf '%s\n' "$*" > "$SYNON_VM_RESTART_POWERSHELL_LOG"
`)
	systemctlLog := filepath.Join(root, "systemctl.log")
	powerShellLog := filepath.Join(root, "powershell.log")
	t.Setenv("PATH", fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SYNON_VM_RESTART_SYSTEMCTL_LOG", systemctlLog)
	t.Setenv("SYNON_VM_RESTART_POWERSHELL_LOG", powerShellLog)
	executable := filepath.Join(root, "synon-go")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	options := Options{
		HomeDir: root, Executable: executable, WorkingDirectory: root,
		UnitDirectory: filepath.Join(root, "user-units"),
		Distro:        "Ubuntu", User: "victor_1", ServiceName: "synon-go.service",
		Environment: map[string]string{"SYNON_HOME": root},
	}
	manager, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Restart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StatePending || !manager.IsRestarting() {
		t.Fatalf("pending status = %#v", status)
	}
	systemctl, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(systemctl) != "--user daemon-reload\n--user enable synon-go.service\n" {
		t.Fatalf("systemctl commands = %q", systemctl)
	}
	powerShell := waitForFixtureLog(t, powerShellLog)
	arguments := strings.Fields(string(powerShell))
	if len(arguments) < 2 || arguments[len(arguments)-2] != "-EncodedCommand" {
		t.Fatalf("PowerShell arguments = %q", powerShell)
	}
	script := decodePowerShellFixture(t, arguments[len(arguments)-1])
	for _, expected := range []string{"wsl.exe", "--shutdown", "--distribution 'Ubuntu'", "systemctl --user start 'synon-go.service'"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("PowerShell script missing %q: %s", expected, script)
		}
	}
	unitPath := filepath.Join(options.UnitDirectory, options.ServiceName)
	if analyzer, lookupErr := exec.LookPath("systemd-analyze"); lookupErr == nil {
		command := exec.Command(analyzer, "verify", unitPath)
		if output, verifyErr := command.CombinedOutput(); verifyErr != nil {
			t.Fatalf("systemd-analyze verify: %v: %s", verifyErr, output)
		}
	}
	recreated, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if !recreated.IsRestarting() {
		t.Fatal("pending state did not survive manager recreation")
	}
	if err := recreated.MarkRuntimeStarted(); err != nil {
		t.Fatal(err)
	}
	completed, err := recreated.Status()
	if err != nil || completed.State != StateCompleted || completed.CompletedAt.IsZero() {
		t.Fatalf("completed status = %#v err=%v", completed, err)
	}
}

func writeExecutableFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func waitForFixtureLog(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		content, err := os.ReadFile(path)
		if err == nil && len(content) > 0 {
			return content
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture process did not write %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func decodePowerShellFixture(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("decode PowerShell payload: len=%d err=%v", len(raw), err)
	}
	units := make([]uint16, len(raw)/2)
	for index := range units {
		units[index] = uint16(raw[index*2]) | uint16(raw[index*2+1])<<8
	}
	return string(utf16.Decode(units))
}
