package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHostLoadsExternalPluginManifestAndRunsProcess(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "terminal-tools")
	manifestDir := filepath.Join(pluginDir, ".synon-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readyFile := filepath.Join(root, "terminal-tools.ready")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, manifestDir, fmt.Sprintf(`{
  "id": "terminal-tools",
  "name": "terminal-tools",
  "version": "0.2.0",
  "description": "Small local terminal helper plugin",
  "author": {"name": "Plugin Author"},
  "license": "MIT",
  "keywords": ["terminal", "tools"],
  "server": {
    "api": {"mount": "/api/plugins/terminal-tools", "entry": "./server/api.go"},
    "context": {"entry": "./server/context.go"},
    "process": {
      "command": %q,
      "args": ["-test.run=TestExternalPluginProcessHelper"],
      "env": {
        "SYNON_PLUGIN_PROCESS_HELPER": "1",
        "SYNON_PLUGIN_READY_FILE": %q
      }
    }
  }
}`, executable, readyFile))

	host, err := NewDefaultWithExternalDirectories([]string{pluginDir})
	if err != nil {
		t.Fatalf("NewDefaultWithExternalDirectories() error = %v", err)
	}
	plugin, ok := host.Get("terminal-tools")
	if !ok {
		t.Fatalf("terminal-tools plugin missing from %#v", host.List())
	}
	if plugin.Author != "Plugin Author" {
		t.Fatalf("Author = %q", plugin.Author)
	}
	if plugin.PluginFormat != "synon_agent_external" {
		t.Fatalf("PluginFormat = %q", plugin.PluginFormat)
	}
	if plugin.Server.APIMount != "/api/plugins/terminal-tools" || plugin.Server.ContextEntry != "./server/context.go" {
		t.Fatalf("Server = %#v", plugin.Server)
	}
	if plugin.Runtime.Status != "configured" || plugin.Runtime.Kind != "external_process" {
		t.Fatalf("Runtime before start = %#v", plugin.Runtime)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := host.StartExternalProcesses(ctx); err != nil {
		t.Fatalf("StartExternalProcesses() error = %v", err)
	}
	t.Cleanup(func() {
		cancel()
		host.StopExternalProcesses()
	})
	waitForFile(t, readyFile)

	plugin, ok = host.Get("terminal-tools")
	if !ok {
		t.Fatal("terminal-tools plugin disappeared after start")
	}
	if plugin.Runtime.Status != "running" || plugin.Runtime.PID <= 0 {
		t.Fatalf("Runtime after start = %#v", plugin.Runtime)
	}

	host.StopExternalProcesses()
	plugin, _ = host.Get("terminal-tools")
	if plugin.Runtime.Status != "stopped" {
		t.Fatalf("Runtime after stop = %#v", plugin.Runtime)
	}
}

func TestHostMarksExternalProcessExitedWhenChildEnds(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "short-plugin")
	manifestDir := filepath.Join(pluginDir, ".synon-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readyFile := filepath.Join(root, "short-plugin.ready")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, manifestDir, fmt.Sprintf(`{
  "id": "short-plugin",
  "name": "short-plugin",
  "version": "0.1.0",
  "server": {
    "api": {"mount": "/api/plugins/short-plugin"},
    "process": {
      "command": %q,
      "args": ["-test.run=TestExternalPluginProcessHelper"],
      "env": {
        "SYNON_PLUGIN_PROCESS_HELPER": "1",
        "SYNON_PLUGIN_EXIT_AFTER_READY": "1",
        "SYNON_PLUGIN_READY_FILE": %q
      }
    }
  }
}`, executable, readyFile))

	host, err := NewDefaultWithExternalDirectories([]string{pluginDir})
	if err != nil {
		t.Fatalf("NewDefaultWithExternalDirectories() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := host.StartExternalProcesses(ctx); err != nil {
		t.Fatalf("StartExternalProcesses() error = %v", err)
	}
	t.Cleanup(host.StopExternalProcesses)
	waitForFile(t, readyFile)
	waitForRuntimeStatus(t, host, "short-plugin", "exited")
}

func TestHostRejectsExcludedSidePluginManifest(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "admet", ".synon-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, manifestDir, `{
  "id": "admet",
  "name": "ADMET",
  "version": "9.9.9",
  "server": {"api": {"mount": "/api/plugins/admet", "entry": "./server/api.go"}}
}`)

	_, err := NewDefaultWithExternalDirectories([]string{filepath.Join(root, "admet")})
	if err == nil {
		t.Fatal("NewDefaultWithExternalDirectories() succeeded for excluded ADMET plugin")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "excluded") {
		t.Fatalf("error = %v", err)
	}
}

func TestHostLoadsExternalPluginHooksConfig(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "terminal-tools")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".synon-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pluginDir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, filepath.Join(pluginDir, ".synon-plugin"), `{
  "id": "terminal-tools",
  "name": "terminal-tools",
  "version": "0.2.0",
  "server": {"api": {"mount": "/api/plugins/terminal-tools"}}
}`)
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks", "hooks.json"), []byte(`{
  "description": "plugin hooks",
  "hooks": {
    "PreToolUse": [{
      "matcher": "task_create",
      "hooks": [{"type": "command", "shell": "Bash", "command": "echo ok"}]
    }]
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	host, err := NewDefaultWithExternalDirectories([]string{pluginDir})
	if err != nil {
		t.Fatalf("NewDefaultWithExternalDirectories() error = %v", err)
	}
	plugin, ok := host.Get("terminal-tools")
	if !ok {
		t.Fatalf("plugin missing from %#v", host.List())
	}
	matchers, ok := plugin.HooksConfig["PreToolUse"].([]any)
	if !ok || len(matchers) != 1 {
		t.Fatalf("HooksConfig = %#v", plugin.HooksConfig)
	}
	if plugin.Root() != pluginDir {
		t.Fatalf("Root() = %q, want %q", plugin.Root(), pluginDir)
	}
}

func TestHostRejectsMissingManifestHooksFile(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "terminal-tools")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".synon-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, filepath.Join(pluginDir, ".synon-plugin"), `{
  "id": "terminal-tools",
  "name": "terminal-tools",
  "version": "0.2.0",
  "hooks": "hooks/missing.json",
  "server": {"api": {"mount": "/api/plugins/terminal-tools"}}
}`)

	_, err := NewDefaultWithExternalDirectories([]string{pluginDir})
	if err == nil {
		t.Fatal("NewDefaultWithExternalDirectories() succeeded with missing manifest hooks file")
	}
	if !strings.Contains(err.Error(), "missing.json") {
		t.Fatalf("error = %v", err)
	}
}

func TestHostKeepsPathCommandNamesAndResolvesRelativeExecutables(t *testing.T) {
	root := t.TempDir()
	pathCommandDir := filepath.Join(root, "path-command")
	if err := os.MkdirAll(filepath.Join(pathCommandDir, ".synon-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, filepath.Join(pathCommandDir, ".synon-plugin"), `{
  "id": "path-command",
  "name": "path-command",
  "version": "0.1.0",
  "server": {
    "api": {"mount": "/api/plugins/path-command"},
    "process": {"command": "plugin-helper", "args": ["--serve"]}
  }
}`)
	relativeCommandDir := filepath.Join(root, "relative-command")
	if err := os.MkdirAll(filepath.Join(relativeCommandDir, ".synon-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(relativeCommandDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relativeCommandDir, "bin", "plugin-helper"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, filepath.Join(relativeCommandDir, ".synon-plugin"), `{
  "id": "relative-command",
  "name": "relative-command",
  "version": "0.1.0",
  "server": {
    "api": {"mount": "/api/plugins/relative-command"},
    "process": {"command": "./bin/plugin-helper"}
  }
}`)

	host, err := NewDefaultWithExternalDirectories([]string{pathCommandDir, relativeCommandDir})
	if err != nil {
		t.Fatalf("NewDefaultWithExternalDirectories() error = %v", err)
	}
	pathCommand, _ := host.Get("path-command")
	if pathCommand.Server.Process.Command != "plugin-helper" {
		t.Fatalf("path command resolved unexpectedly: %q", pathCommand.Server.Process.Command)
	}
	relativeCommand, _ := host.Get("relative-command")
	wantRelative := filepath.Join(relativeCommandDir, "bin", "plugin-helper")
	if relativeCommand.Server.Process.Command != wantRelative {
		t.Fatalf("relative command = %q, want %q", relativeCommand.Server.Process.Command, wantRelative)
	}
}

func TestHostPluginProcessStripsAmbientSecretsAndAllowsExplicitEnvironment(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "environment-plugin")
	manifestDir := filepath.Join(pluginDir, ".synon-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	readyFile := filepath.Join(root, "environment.ready")
	environmentFile := filepath.Join(root, "environment.txt")
	t.Setenv("SYNON_AMBIENT_PLUGIN_SECRET", "must-not-reach-plugin")
	writePluginManifest(t, manifestDir, fmt.Sprintf(`{
  "id": "environment-plugin",
  "name": "environment-plugin",
  "version": "0.1.0",
  "server": {
    "api": {"mount": "/api/plugins/environment-plugin"},
    "process": {
      "command": %q,
      "args": ["-test.run=TestExternalPluginProcessHelper"],
      "env": {
        "SYNON_PLUGIN_PROCESS_HELPER": "1",
        "SYNON_PLUGIN_EXIT_AFTER_READY": "1",
        "SYNON_PLUGIN_EXPLICIT": "configured-value",
        "SYNON_PLUGIN_ENV_FILE": %q,
        "SYNON_PLUGIN_READY_FILE": %q
      }
    }
  }
}`, executable, environmentFile, readyFile))

	host, err := NewDefaultWithExternalDirectories([]string{pluginDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.StartExternalProcesses(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.StopExternalProcesses)
	waitForFile(t, readyFile)
	raw, err := os.ReadFile(environmentFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if text != "explicit=configured-value\nambient=\n" {
		t.Fatalf("plugin environment evidence = %q", text)
	}
}

func TestHostRejectsProcessPathEscapeAndInvalidEnvironment(t *testing.T) {
	root := t.TempDir()
	escapeDir := filepath.Join(root, "escape-plugin")
	if err := os.MkdirAll(filepath.Join(escapeDir, ".synon-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-helper")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, filepath.Join(escapeDir, ".synon-plugin"), `{
  "id": "escape-plugin",
  "name": "escape-plugin",
  "server": {
    "api": {"mount": "/api/plugins/escape-plugin"},
    "process": {"command": "../outside-helper"}
  }
}`)
	if _, err := NewDefaultWithExternalDirectories([]string{escapeDir}); err == nil || !strings.Contains(err.Error(), "must stay under") {
		t.Fatalf("escaped plugin command error = %v", err)
	}

	invalidDir := filepath.Join(root, "invalid-environment")
	if err := os.MkdirAll(filepath.Join(invalidDir, ".synon-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, filepath.Join(invalidDir, ".synon-plugin"), `{
  "id": "invalid-environment",
  "name": "invalid-environment",
  "server": {
    "api": {"mount": "/api/plugins/invalid-environment"},
    "process": {"command": "plugin-helper", "env": {"BAD=KEY": "value"}}
  }
}`)
	host, err := NewDefaultWithExternalDirectories([]string{invalidDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.StartExternalProcesses(context.Background()); err == nil || !strings.Contains(err.Error(), "environment key") {
		t.Fatalf("invalid plugin environment start error = %v", err)
	}
}

func TestHostStopKillsPluginProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix liveness assertion; Windows Job Object coverage is cross-compiled")
	}
	root := t.TempDir()
	pluginDir := filepath.Join(root, "tree-plugin")
	manifestDir := filepath.Join(pluginDir, ".synon-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	readyFile := filepath.Join(root, "tree.ready")
	childPIDFile := filepath.Join(root, "child.pid")
	writePluginManifest(t, manifestDir, fmt.Sprintf(`{
  "id": "tree-plugin",
  "name": "tree-plugin",
  "server": {
    "api": {"mount": "/api/plugins/tree-plugin"},
    "process": {
      "command": %q,
      "args": ["-test.run=TestExternalPluginProcessHelper"],
      "env": {
        "SYNON_PLUGIN_PROCESS_HELPER": "1",
        "SYNON_PLUGIN_CHILD_PID_FILE": %q,
        "SYNON_PLUGIN_READY_FILE": %q
      }
    }
  }
}`, executable, childPIDFile, readyFile))
	host, err := NewDefaultWithExternalDirectories([]string{pluginDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.StartExternalProcesses(context.Background()); err != nil {
		t.Fatal(err)
	}
	childPID := waitForPIDFile(t, childPIDFile)
	host.StopExternalProcesses()
	waitForPIDExit(t, childPID)
}

func TestExternalPluginProcessHelper(t *testing.T) {
	if os.Getenv("SYNON_PLUGIN_PROCESS_HELPER") != "1" {
		return
	}
	readyFile := os.Getenv("SYNON_PLUGIN_READY_FILE")
	if readyFile == "" {
		os.Exit(2)
	}
	if environmentFile := os.Getenv("SYNON_PLUGIN_ENV_FILE"); environmentFile != "" {
		content := fmt.Sprintf("explicit=%s\nambient=%s\n", os.Getenv("SYNON_PLUGIN_EXPLICIT"), os.Getenv("SYNON_AMBIENT_PLUGIN_SECRET"))
		if err := os.WriteFile(environmentFile, []byte(content), 0o600); err != nil {
			os.Exit(4)
		}
	}
	if childPIDFile := os.Getenv("SYNON_PLUGIN_CHILD_PID_FILE"); childPIDFile != "" {
		child := exec.Command("sleep", "60")
		if err := child.Start(); err != nil {
			os.Exit(5)
		}
		if err := os.WriteFile(childPIDFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			os.Exit(6)
		}
	}
	if err := os.WriteFile(readyFile, []byte(fmt.Sprintf("pid=%d\n", os.Getpid())), 0o600); err != nil {
		os.Exit(3)
	}
	if os.Getenv("SYNON_PLUGIN_EXIT_AFTER_READY") == "1" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for child pid in %s", path)
	return 0
}

func waitForPIDExit(t *testing.T, pid int) {
	t.Helper()
	defer func() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pluginTestProcessExited(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("plugin child process %d survived host stop", pid)
}

func writePluginManifest(t *testing.T, manifestDir string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitForRuntimeStatus(t *testing.T, host *Host, id string, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		plugin, ok := host.Get(id)
		if !ok {
			t.Fatalf("plugin %s disappeared", id)
		}
		if plugin.Runtime.Status == status {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	plugin, _ := host.Get(id)
	t.Fatalf("runtime status for %s = %#v, want %q", id, plugin.Runtime, status)
}
