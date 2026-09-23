//go:build windows

package kernel

import (
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
	"unsafe"

	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

const windowsConfinementTestChildEnv = "SYNON_WINDOWS_CONFINEMENT_TEST_CHILD"

type windowsConfinementTestResult struct {
	AppContainer      bool `json:"app_container"`
	StdinDelivered    bool `json:"stdin_delivered"`
	WorkspaceWrite    bool `json:"workspace_write"`
	OutsideRead       bool `json:"outside_read"`
	DirectNetwork     bool `json:"direct_network"`
	HostSecretVisible bool `json:"host_secret_visible"`
	RuntimeRead       bool `json:"runtime_read"`
	RuntimeWrite      bool `json:"runtime_write"`
}

func TestWindowsKernelAppContainerRealBoundary(t *testing.T) {
	if os.Getenv(windowsConfinementTestChildEnv) == "1" {
		runWindowsConfinementTestChild(t)
		return
	}
	workspace := t.TempDir()
	t.Setenv("SYNON_HOME", t.TempDir())
	t.Setenv("SYNON_TEST_HOST_SECRET_SENTINEL", "not-for-worker")
	outside := t.TempDir()
	runtimeRoot := t.TempDir()
	runtimeFile := filepath.Join(runtimeRoot, "library.dat")
	if err := os.WriteFile(runtimeFile, []byte("verified-library"), 0o600); err != nil {
		t.Fatal(err)
	}
	privateFile := filepath.Join(outside, "private.txt")
	if err := os.WriteFile(privateFile, []byte("parent-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	command, err := newWindowsConfinedCommand(
		workspace, os.Args[0],
		[]string{"-test.run=TestWindowsKernelAppContainerRealBoundary"},
		[]string{
			windowsConfinementTestChildEnv + "=1",
			"SYNON_WINDOWS_CONFINEMENT_TEST_WORKSPACE=" + workspace,
			"SYNON_WINDOWS_CONFINEMENT_TEST_OUTSIDE=" + privateFile,
			"SYNON_WINDOWS_CONFINEMENT_TEST_ADDRESS=" + listener.Addr().String(),
			"SYNON_WINDOWS_CONFINEMENT_TEST_RUNTIME=" + runtimeRoot,
		},
		[]string{runtimeRoot},
	)
	if err != nil {
		t.Fatalf("construct confined worker command: %v", err)
	}
	request, err := windowsConfinedRequestFromCommand(command)
	if err != nil || request == nil {
		t.Fatalf("read confined worker request: %v", err)
	}
	command.Stdin = strings.NewReader("cell-input")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	process, err := startWorkerProcess(command)
	if err != nil {
		t.Fatalf("start confined worker: %v", err)
	}
	defer process.close()
	if err := command.Wait(); err != nil {
		t.Fatalf("wait for confined worker: %v, stderr=%s", err, stderr.String())
	}
	var result windowsConfinementTestResult
	firstLine, _, _ := bytes.Cut(stdout.Bytes(), []byte("\n"))
	if err := json.Unmarshal(bytes.TrimSpace(firstLine), &result); err != nil {
		t.Fatalf("decode confined worker result: %v, stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if !result.AppContainer || !result.StdinDelivered || !result.WorkspaceWrite ||
		!result.RuntimeRead || result.RuntimeWrite || result.OutsideRead ||
		result.DirectNetwork || result.HostSecretVisible {
		t.Fatalf("Windows worker boundary failed: %+v stderr=%q", result, stderr.String())
	}
	if !strings.Contains(stderr.String(), "confined-stderr-ok") {
		t.Fatalf("confined stderr was not delivered: %q", stderr.String())
	}
	process.close()
	if process.cleanupErr != nil {
		t.Fatalf("AppContainer cleanup failed: %v", process.cleanupErr)
	}
	if sid, err := deriveWindowsAppContainerSID(request.ProfileName); err != nil {
		t.Fatal(err)
	} else {
		defer windows.FreeSid(sid)
		for _, path := range []string{workspace, runtimeRoot, runtimeFile, os.Args[0], filepath.Dir(os.Args[0])} {
			if windowsTestPathHasSIDGrant(t, path, sid) {
				t.Fatalf("AppContainer grant remained on %q", path)
			}
		}
	}
	if sid, err := createWindowsAppContainerProfile(request.ProfileName); err != nil {
		t.Fatalf("AppContainer profile remained after worker exit: %v", err)
	} else {
		defer windows.FreeSid(sid)
		if err := deleteWindowsAppContainerProfile(request.ProfileName); err != nil {
			t.Fatalf("remove profile created by cleanup verification: %v", err)
		}
	}
}

func windowsTestPathHasSIDGrant(t *testing.T, path string, sid *windows.SID) bool {
	t.Helper()
	security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := security.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("read test path DACL: %v", err)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE &&
			(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(sid) {
			return true
		}
	}
	return false
}

func runWindowsConfinementTestChild(t *testing.T) {
	var tokenValue, size uint32
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29,
		(*byte)(unsafe.Pointer(&tokenValue)), uint32(unsafe.Sizeof(tokenValue)), &size)
	if err != nil || size != uint32(unsafe.Sizeof(tokenValue)) {
		t.Fatalf("query AppContainer token: size=%d err=%v", size, err)
	}
	stdin, err := io.ReadAll(io.LimitReader(os.Stdin, 128))
	if err != nil {
		t.Fatal(err)
	}
	workspace := os.Getenv("SYNON_WINDOWS_CONFINEMENT_TEST_WORKSPACE")
	outside := os.Getenv("SYNON_WINDOWS_CONFINEMENT_TEST_OUTSIDE")
	address := os.Getenv("SYNON_WINDOWS_CONFINEMENT_TEST_ADDRESS")
	runtimeRoot := os.Getenv("SYNON_WINDOWS_CONFINEMENT_TEST_RUNTIME")
	runtimeFile := filepath.Join(runtimeRoot, "library.dat")
	runtimeRaw, runtimeReadErr := os.ReadFile(runtimeFile)
	runtimeWriteErr := os.WriteFile(runtimeFile, []byte("changed"), 0o600)
	runtimeCreateErr := os.WriteFile(filepath.Join(runtimeRoot, "unapproved.txt"), []byte("new"), 0o600)
	writeErr := os.WriteFile(filepath.Join(workspace, "guest-output.txt"), []byte("guest-output"), 0o600)
	_, readErr := os.ReadFile(outside)
	connection, dialErr := net.DialTimeout("tcp4", address, time.Second)
	if dialErr == nil {
		_ = connection.Close()
	}
	result := windowsConfinementTestResult{
		AppContainer: tokenValue != 0, StdinDelivered: string(stdin) == "cell-input",
		WorkspaceWrite: writeErr == nil, OutsideRead: readErr == nil, DirectNetwork: dialErr == nil,
		HostSecretVisible: os.Getenv("SYNON_TEST_HOST_SECRET_SENTINEL") != "",
		RuntimeRead:       runtimeReadErr == nil && string(runtimeRaw) == "verified-library",
		RuntimeWrite:      runtimeWriteErr == nil || runtimeCreateErr == nil,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stdout, string(raw))
	_, _ = fmt.Fprintln(os.Stderr, "confined-stderr-ok")
}

func TestWindowsKernelAppContainerCancellationKillsDescendants(t *testing.T) {
	switch os.Getenv("SYNON_WINDOWS_CONFINEMENT_CANCEL_MODE") {
	case "root":
		workspace := os.Getenv("SYNON_WINDOWS_CONFINEMENT_CANCEL_WORKSPACE")
		childEnv := make([]string, 0, len(os.Environ())+1)
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.EqualFold(key, "SYNON_WINDOWS_CONFINEMENT_CANCEL_MODE") {
				childEnv = append(childEnv, entry)
			}
		}
		childEnv = append(childEnv, "SYNON_WINDOWS_CONFINEMENT_CANCEL_MODE=descendant")
		child, err := os.StartProcess(os.Args[0],
			[]string{os.Args[0], "-test.run=TestWindowsKernelAppContainerCancellationKillsDescendants"},
			&os.ProcAttr{Dir: workspace, Env: childEnv, Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
		if err != nil {
			t.Fatal(err)
		}
		_ = child.Release()
		if err := os.WriteFile(filepath.Join(workspace, "cancel-ready.txt"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		return
	case "descendant":
		workspace := os.Getenv("SYNON_WINDOWS_CONFINEMENT_CANCEL_WORKSPACE")
		var tokenValue, size uint32
		err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29,
			(*byte)(unsafe.Pointer(&tokenValue)), uint32(unsafe.Sizeof(tokenValue)), &size)
		if err != nil || tokenValue == 0 {
			t.Fatalf("descendant lost AppContainer token: token=%d err=%v", tokenValue, err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "cancel-descendant.txt"), []byte("appcontainer"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(workspace, "cancel-escaped.txt"), []byte("survived"), 0o600)
		return
	}
	workspace := t.TempDir()
	t.Setenv("SYNON_HOME", t.TempDir())
	command, err := newWindowsConfinedCommand(
		workspace, os.Args[0],
		[]string{"-test.run=TestWindowsKernelAppContainerCancellationKillsDescendants"},
		[]string{
			"SYNON_WINDOWS_CONFINEMENT_CANCEL_MODE=root",
			"SYNON_WINDOWS_CONFINEMENT_CANCEL_WORKSPACE=" + workspace,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := windowsConfinedRequestFromCommand(command)
	if err != nil || request == nil {
		t.Fatalf("decode confinement request: %v", err)
	}
	command.Stdin = strings.NewReader("")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	process, err := startWorkerProcess(command)
	if err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	waited := false
	defer func() {
		_ = process.kill()
		if !waited {
			<-wait
		}
		process.close()
	}()
	for _, name := range []string{"cancel-ready.txt", "cancel-descendant.txt"} {
		path := filepath.Join(workspace, name)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(path); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("confined process did not reach %s: %v output=%q", name, err, output.String())
		}
	}
	if err := process.kill(); err != nil {
		t.Fatalf("terminate AppContainer Job: %v", err)
	}
	<-wait
	waited = true
	process.close()
	if process.cleanupErr != nil {
		t.Fatalf("AppContainer cleanup after cancellation failed: %v", process.cleanupErr)
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(filepath.Join(workspace, "cancel-escaped.txt")); err == nil {
		t.Fatal("AppContainer descendant survived Job termination")
	}
	sid, err := deriveWindowsAppContainerSID(request.ProfileName)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.FreeSid(sid)
	if windowsTestPathHasSIDGrant(t, workspace, sid) {
		t.Fatal("AppContainer workspace grant remained after cancellation")
	}
}

func TestWindowsKernelAppContainerRecoversAbandonedLease(t *testing.T) {
	t.Setenv("SYNON_HOME", t.TempDir())
	workspace := t.TempDir()
	runtimeRoot := t.TempDir()
	runtimeFile := filepath.Join(runtimeRoot, "existing.dat")
	if err := os.WriteFile(runtimeFile, []byte("immutable"), 0o600); err != nil {
		t.Fatal(err)
	}
	command, err := newWindowsConfinedCommand(workspace, os.Args[0], nil, nil, []string{runtimeRoot})
	if err != nil {
		t.Fatal(err)
	}
	request, err := windowsConfinedRequestFromCommand(command)
	if err != nil || request == nil {
		t.Fatalf("decode confinement request: %v", err)
	}
	lease, err := prepareWindowsConfinedRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = lease.journal.closeLock()
		_ = recoverWindowsConfinementLeases()
		_ = windows.FreeSid(lease.sid)
	})
	if !windowsTestPathHasSIDGrant(t, workspace, lease.sid) {
		t.Fatal("prepared AppContainer lease did not grant its own workspace")
	}
	if !windowsTestPathHasSIDGrant(t, runtimeRoot, lease.sid) {
		t.Fatal("prepared AppContainer lease did not grant its read-only runtime")
	}
	if err := recoverWindowsConfinementLeases(); err != nil {
		t.Fatalf("active lease should be skipped: %v", err)
	}
	if !windowsTestPathHasSIDGrant(t, workspace, lease.sid) {
		t.Fatal("recovery removed a grant from an active worker")
	}
	// Closing the exclusive lock simulates process death: Windows releases all
	// process handles even when a host does not run deferred cleanup.
	if err := lease.journal.closeLock(); err != nil {
		t.Fatal(err)
	}
	if err := recoverWindowsConfinementLeases(); err != nil {
		t.Fatalf("recover abandoned AppContainer lease: %v", err)
	}
	if windowsTestPathHasSIDGrant(t, workspace, lease.sid) {
		t.Fatal("recovery left an AppContainer workspace grant")
	}
	for _, path := range []string{runtimeRoot, runtimeFile} {
		if windowsTestPathHasSIDGrant(t, path, lease.sid) {
			t.Fatalf("recovery left an AppContainer runtime grant on %q", path)
		}
	}
	if _, err := os.Stat(lease.journal.receiptPath); !os.IsNotExist(err) {
		t.Fatalf("recovered lease receipt remains: %v", err)
	}
	if sid, err := createWindowsAppContainerProfile(request.ProfileName); err != nil {
		t.Fatalf("recovered AppContainer profile remains: %v", err)
	} else {
		_ = windows.FreeSid(sid)
		if err := deleteWindowsAppContainerProfile(request.ProfileName); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWindowsKernelAppContainerRecoversCrashedHost(t *testing.T) {
	if os.Getenv("SYNON_WINDOWS_CONFINEMENT_CRASH_HOST") == "1" {
		request := &windowsConfinedRequest{
			ProfileName: "SynonWorker." + os.Getenv("SYNON_WINDOWS_CONFINEMENT_CRASH_ID"),
			Workspace:   os.Getenv("SYNON_WINDOWS_CONFINEMENT_CRASH_WORKSPACE"),
			Executable:  os.Args[0],
		}
		if _, err := prepareWindowsConfinedRequest(request); err != nil {
			t.Fatal(err)
		}
		// Simulate a host crash: deliberately bypass all Go defers and Close.
		os.Exit(91)
	}
	state := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("SYNON_HOME", state)
	identifier := uuid.NewString()
	profileName := "SynonWorker." + identifier
	command := exec.Command(os.Args[0], "-test.run=^TestWindowsKernelAppContainerRecoversCrashedHost$")
	command.Env = append(os.Environ(),
		"SYNON_WINDOWS_CONFINEMENT_CRASH_HOST=1",
		"SYNON_WINDOWS_CONFINEMENT_CRASH_ID="+identifier,
		"SYNON_WINDOWS_CONFINEMENT_CRASH_WORKSPACE="+workspace,
	)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(err.Error(), "exit status 91") {
		t.Fatalf("crash helper did not exit at the injected point: err=%v output=%q", err, output)
	}
	sid, err := deriveWindowsAppContainerSID(profileName)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.FreeSid(sid)
	if !windowsTestPathHasSIDGrant(t, workspace, sid) {
		t.Fatal("crash injection did not leave an AppContainer grant to recover")
	}
	if err := recoverWindowsConfinementLeases(); err != nil {
		t.Fatalf("recover after host process exit: %v", err)
	}
	if windowsTestPathHasSIDGrant(t, workspace, sid) {
		t.Fatal("recovery left a workspace grant after host crash")
	}
	if _, err := os.Stat(filepath.Join(state, windowsConfinementLeaseDirectory, profileName+".json")); !os.IsNotExist(err) {
		t.Fatalf("recovery left a lease receipt: %v", err)
	}
	if recreated, err := createWindowsAppContainerProfile(profileName); err != nil {
		t.Fatalf("recovery left AppContainer profile after host crash: %v", err)
	} else {
		_ = windows.FreeSid(recreated)
		if err := deleteWindowsAppContainerProfile(profileName); err != nil {
			t.Fatal(err)
		}
	}
}
