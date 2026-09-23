//go:build windows

package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsKernelConfinementIsNotAdvertisedBeforeFullBoundary(t *testing.T) {
	t.Setenv("SYNON_HOME", t.TempDir())
	workspace := t.TempDir()
	executable := windowsConfinementTestExecutable(t)
	command, err := newConfinedWorkerCommand(workspace, executable, nil, nil, nil, nil)
	if command != nil || !errors.Is(err, ErrConfinementUnavailable) {
		t.Fatalf("unverified kernel worker must remain closed: command=%v error=%v", command, err)
	}
	if evidence := probePlatformConfinement(); evidence.Available || evidence.PolicySHA256 != "" {
		t.Fatalf("Job Object supervision cannot prove Windows confinement: %+v", evidence)
	}
}

func TestWindowsRequestedKernelEgressFailsWithoutBroker(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		allowed  []string
		upstream string
	}{
		{name: "approved domain", allowed: []string{"example.com"}},
		{name: "upstream proxy", upstream: "https://proxy.example.com"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			proxy, err := startKernelEgressProxy(t.TempDir(), "windows-egress", fixture.allowed, nil, fixture.upstream)
			if proxy != nil || !errors.Is(err, ErrConfinementUnavailable) {
				t.Fatalf("requested egress must fail without a broker: proxy=%v err=%v", proxy, err)
			}
		})
	}
}

func TestWindowsConfinementRequestRejectsUnsafeAuthority(t *testing.T) {
	workspace := t.TempDir()
	executable := windowsConfinementTestExecutable(t)
	outside := t.TempDir()
	child := filepath.Join(workspace, "overlay")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		workspace   string
		executable  string
		environment []string
		mounts      []WorkerMount
		protected   []string
		auxiliary   []*os.File
	}{
		{name: "relative workspace", workspace: "relative", executable: executable},
		{name: "network workspace", workspace: `\\server\share\workspace`, executable: executable},
		{name: "device workspace", workspace: `\\?\C:\workspace`, executable: executable},
		{name: "alternate stream", workspace: workspace + ":stream", executable: executable},
		{name: "duplicate environment", workspace: workspace, executable: executable, environment: []string{"PATH=a", "Path=b"}},
		{name: "invalid environment", workspace: workspace, executable: executable, environment: []string{"PATH=a\x00b"}},
		{name: "workspace overlay", workspace: workspace, executable: executable, mounts: []WorkerMount{{Path: child}}},
		{name: "protected mount", workspace: workspace, executable: executable, mounts: []WorkerMount{{Path: outside}}, protected: []string{outside}},
		{name: "trusted writable mount", workspace: workspace, executable: executable, mounts: []WorkerMount{{Path: outside, Writable: true, trusted: true}}},
		{name: "auxiliary handle", workspace: workspace, executable: executable, auxiliary: []*os.File{os.Stdin}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateWindowsConfinementRequest(
				test.workspace, test.executable, nil, test.environment,
				test.mounts, test.protected, test.auxiliary,
			)
			if err == nil {
				t.Fatal("unsafe Windows worker authority was accepted")
			}
		})
	}
}

func windowsConfinementTestExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("local Windows test executable %q is unavailable: %v", path, err)
	}
	return path
}

func TestWindowsConfinementRejectsReparsePoint(t *testing.T) {
	workspace := t.TempDir()
	parent := t.TempDir()
	link := filepath.Join(parent, "link")
	if err := os.Symlink(workspace, link); err != nil {
		t.Skipf("creating a Windows directory symlink is unavailable: %v", err)
	}
	if err := validateWindowsKernelPath(link, true, false); err == nil || !strings.Contains(err.Error(), "reparse") {
		t.Fatalf("reparse workspace error=%v", err)
	}
}

func TestWindowsConfinementOverlapUsesCaseInsensitiveBoundaries(t *testing.T) {
	if !windowsKernelPathsOverlap(`C:\Workspace\data`, `c:\workspace`) {
		t.Fatal("case-insensitive child overlap was missed")
	}
	if windowsKernelPathsOverlap(`C:\Workspace-other`, `c:\workspace`) {
		t.Fatal("sibling path was classified as an overlap")
	}
}

func TestWindowsConfinementGrantsDoNotWidenExecutableParent(t *testing.T) {
	executable := `C:\Program Files\Synon\synon.exe`
	grants := windowsConfinementGrants(executable, `C:\SynonWork\task`, nil)
	if len(grants) != 2 || grants[0].path != executable || grants[1].path != `C:\SynonWork\task` {
		t.Fatalf("unexpected AppContainer grants: %+v", grants)
	}
}

func TestWindowsFrozenMountsFailClosed(t *testing.T) {
	workspace := t.TempDir()
	executable := windowsConfinementTestExecutable(t)
	operationLog, err := os.CreateTemp(t.TempDir(), "operation-log-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer operationLog.Close()
	sharedLibrary := t.TempDir()
	sharedHandle, err := os.Open(sharedLibrary)
	if err != nil {
		t.Fatal(err)
	}
	defer sharedHandle.Close()
	for _, fixture := range []struct {
		name  string
		mount WorkerMount
	}{
		{"writable operation log", WorkerMount{Path: operationLog.Name(), Writable: true, regular: true, trusted: true, frozen: operationLog}},
		{"read-only shared library", WorkerMount{Path: sharedLibrary, trusted: true, frozen: sharedHandle}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			err := validateWindowsConfinementRequest(workspace, executable, nil, nil, []WorkerMount{fixture.mount}, nil, nil)
			if !errors.Is(err, ErrConfinementUnavailable) {
				t.Fatalf("unrepresented frozen mount must fail closed: %v", err)
			}
		})
	}
}
