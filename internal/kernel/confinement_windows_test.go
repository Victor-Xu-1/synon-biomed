//go:build windows

package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
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

func TestWindowsFrozenMountsUseExactPathIdentity(t *testing.T) {
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
			if err != nil {
				t.Fatalf("frozen mount path should validate: %v", err)
			}
		})
	}
	other, err := os.CreateTemp(t.TempDir(), "other-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := validateWindowsConfinementRequest(workspace, executable, nil, nil,
		[]WorkerMount{{Path: operationLog.Name(), Writable: true, regular: true, trusted: true, frozen: other}}, nil, nil); err == nil {
		t.Fatal("swapped frozen operation log identity was accepted")
	}
}

func TestWindowsWorkerAuthorityDoesNotGrantArbitraryArguments(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "python.exe")
	assets := filepath.Join(t.TempDir(), "kernels")
	secret := filepath.Join(t.TempDir(), "private.txt")
	shared := t.TempDir()
	oplog := filepath.Join(t.TempDir(), "operation.jsonl")
	roots, files, writable := windowsWorkerAuthority(executable,
		[]string{filepath.Join(assets, "kernel_worker.py"), secret},
		[]string{"CONDA_PREFIX=" + root}, []WorkerMount{
			{Path: shared, trusted: true},
			{Path: oplog, Writable: true, regular: true, trusted: true},
		})
	if len(roots) != 3 || len(files) != 0 || len(writable) != 1 ||
		writable[0] != oplog {
		t.Fatalf("unexpected worker authority: roots=%q files=%q writable=%q", roots, files, writable)
	}
	for _, path := range append(append([]string(nil), roots...), append(files, writable...)...) {
		if path == secret {
			t.Fatal("untrusted argument was granted filesystem authority")
		}
	}
	outside := t.TempDir()
	roots, _, _ = windowsWorkerAuthority(executable, nil, []string{"CONDA_PREFIX=" + outside}, nil)
	if len(roots) != 1 || roots[0] != filepath.Dir(executable) {
		t.Fatalf("unrelated CONDA_PREFIX gained authority: %q", roots)
	}
}

func TestWindowsPythonWorkerAlwaysStartsIsolated(t *testing.T) {
	worker := `C:\Synon\kernels\kernel_worker.py`
	python := `C:\Runtime\python.exe`
	arguments := windowsIsolatedWorkerArguments(python, []string{worker})
	if len(arguments) != 2 || arguments[0] != "-I" || arguments[1] != worker {
		t.Fatalf("Python startup was not isolated: %q", arguments)
	}
	arguments = windowsIsolatedWorkerArguments(python, arguments)
	if len(arguments) != 2 {
		t.Fatalf("Python isolation flag was duplicated: %q", arguments)
	}
	arguments = windowsIsolatedWorkerArguments(`C:\Runtime\Rscript.exe`, []string{worker})
	if len(arguments) != 1 || arguments[0] != worker {
		t.Fatalf("non-Python arguments changed: %q", arguments)
	}
}

func TestWindowsFrozenAuthorityDetectsChangedFileIdentity(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "mount-*.dat")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	identity, err := windowsFrozenPath(file.Name(), file)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := openWindowsFrozenAuthorityPath(identity)
	if err != nil {
		t.Fatalf("unchanged frozen file was rejected: %v", err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	identity.IndexLow++
	if _, err := openWindowsFrozenAuthorityPath(identity); err == nil {
		t.Fatal("changed frozen file identity was accepted")
	}
}

func TestWindowsROperationLogAndSharedLibraryUseNativeFiles(t *testing.T) {
	prefix := t.TempDir()
	path, frozen, err := secureROperationLog(prefix)
	if err != nil {
		t.Fatalf("create R operation log: %v", err)
	}
	defer frozen.Close()
	if filepath.Dir(path) != prefix {
		t.Fatalf("R operation log escaped its runtime: %q", path)
	}
	identity, err := windowsFrozenPath(path, frozen)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := openWindowsFrozenAuthorityPath(identity)
	if err != nil {
		t.Fatal(err)
	}
	_ = windows.CloseHandle(handle)
	entry := []byte("{\"operation\":\"install\"}\n")
	if err := os.WriteFile(path, entry, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, found, err := readBoundedKernelMetadataFile(prefix, filepath.Base(path), len(entry))
	if err != nil || !found || string(raw) != string(entry) {
		t.Fatalf("read R operation metadata: found=%v content=%q err=%v", found, raw, err)
	}
	if _, _, err := readBoundedKernelMetadataFile(prefix, filepath.Base(path), len(entry)-1); err == nil {
		t.Fatal("oversized R operation log was accepted")
	}
	if _, _, err := readBoundedKernelMetadataFile(prefix, `..\private.txt`, 1024); err == nil {
		t.Fatal("traversal metadata name was accepted")
	}
	shared, err := freezeInternalKernelDirectory(prefix)
	if err != nil {
		t.Fatalf("freeze shared R library: %v", err)
	}
	_ = shared.Close()
}
