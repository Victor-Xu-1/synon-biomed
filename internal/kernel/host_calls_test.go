package kernel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecodeHostCallRejectsIdentityForgeryAndInvalidRequestIDs(t *testing.T) {
	valid := []byte(`{"type":"host_call","id":"hc-0123456789abcdef0123456789abcdef","cell_id":"cell-1","method":"host.routine.status","args":[],"kwargs":{}}`)
	call, err := decodeHostCall(valid)
	if err != nil || call.ID == "" || call.CellID != "cell-1" {
		t.Fatalf("valid host call = %#v err=%v", call, err)
	}
	invalid := [][]byte{
		[]byte(`{"type":"host_call","id":"caller-selected","cell_id":"cell-1","method":"host.routine.status","args":[],"kwargs":{}}`),
		[]byte(`{"type":"host_call","id":"hc-0123456789abcdef0123456789abcdef","cell_id":"cell-1","method":"host.routine.status","args":[],"kwargs":{},"frame_id":"forged"}`),
		[]byte(`{"type":"host_call","id":"hc-0123456789abcdef0123456789abcdef","cell_id":"cell-1","method":" host.routine.status","args":[],"kwargs":{}}`),
	}
	for index, payload := range invalid {
		if _, err := decodeHostCall(payload); err == nil {
			t.Fatalf("invalid host call %d was accepted", index)
		}
	}
}

func TestHostCallPolicyAllowsExplicitLongRunningHandlers(t *testing.T) {
	policy := normalizeHostCallPolicy(&HostCallPolicy{CallTimeout: 24 * time.Hour})
	if policy.timeout != 24*time.Hour {
		t.Fatalf("24h host-call timeout was reduced to %s", policy.timeout)
	}
	overLimit := normalizeHostCallPolicy(&HostCallPolicy{CallTimeout: 24*time.Hour + time.Second})
	if overLimit.timeout != defaultHostCallTimeout {
		t.Fatalf("over-limit timeout = %s", overLimit.timeout)
	}
}

func TestHostCallAckPrecedesLongHandlerAndStaleResultsAreDiscarded(t *testing.T) {
	manager, worker := newHostCallTestSession(t, map[string]string{"OPERON_SDK_REPLY_DEADLINE_S": "0.05"})
	handlerStarted := make(chan HostCall, 1)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"host.routine.status"}, MaxCalls: 4, CallTimeout: time.Second,
		Handler: func(ctx context.Context, call HostCall) (any, error) {
			handlerStarted <- call
			// Exactly eight legal but stale frames are tolerated. None may be
			// mistaken for the current result.
			for index := 0; index < 8; index++ {
				stale := fmt.Sprintf(`{"type":"host_result","id":"hc-%032x","cell_id":"wrong-cell","ok":true,"result":{"value":"stale-%d"}}`, index, index)
				_ = worker.writeProtocol([]byte(stale))
			}
			select {
			case <-time.After(180 * time.Millisecond): // Longer than the 50 ms ACK deadline.
				return map[string]any{"value": "current"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-ack", "import host\nprint(host.routine.status()['value'])", policy)
	select {
	case call := <-handlerStarted:
		if call.ID == "" || call.CellID != "exec-ack" || call.Method != "host.routine.status" {
			t.Fatalf("host call = %#v", call)
		}
	default:
		t.Fatal("handler was not called")
	}
	if outcome.Err != nil || outcome.Response.Error != "" || strings.TrimSpace(outcome.Response.Stdout) != "current" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestHostCallRejectsNinthStaleResultAndRecovers(t *testing.T) {
	manager, worker := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"host.routine.status"}, CallTimeout: time.Second,
		Handler: func(context.Context, HostCall) (any, error) {
			for index := 0; index < 9; index++ {
				stale := fmt.Sprintf(`{"type":"host_result","id":"hc-%032x","cell_id":"wrong-cell","ok":true,"result":{"value":%d}}`, index, index)
				_ = worker.writeProtocol([]byte(stale))
			}
			return map[string]any{"value": "must-not-be-used"}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-nine-stale", "import host\nhost.routine.status()", policy)
	if outcome.Err != nil || !strings.Contains(outcome.Response.Error, "too many stale or mismatched host results") {
		t.Fatalf("ninth stale result outcome = %#v", outcome)
	}
	normal := executeHostCallCell(t, manager, "exec-after-stale", "print('still-alive')", nil)
	if normal.Err != nil || normal.Response.Error != "" || strings.TrimSpace(normal.Response.Stdout) != "still-alive" {
		t.Fatalf("worker after stale-frame rejection = %#v", normal)
	}
}

func TestHostCallCancellationStopsHandlerAndReadWait(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	started := make(chan struct{})
	exited := make(chan struct{})
	policy := &HostCallPolicy{
		AllowedMethods: []string{"host.routine.status"}, CallTimeout: 24 * time.Hour,
		Handler: func(ctx context.Context, _ HostCall) (any, error) {
			close(started)
			defer close(exited)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	handle, err := manager.Submit(SubmitRequest{
		FrameID: "frame-host", KernelKind: "analysis", Language: "python", Environment: "python",
		ExecID: "exec-cancel-host", ToolUseID: "tool-cancel-host", Code: "import host\nhost.routine.status()",
		Origin: "agent", Timeout: 30 * time.Second, InterruptGrace: 2 * time.Second, HostCalls: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("host handler did not start")
	}
	result := manager.Interrupt("frame-host", "exec-cancel-host")
	if !result.Interrupted {
		t.Fatalf("interrupt = %#v", result)
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled host handler leaked")
	}
	select {
	case outcome := <-handle.Done():
		if !outcome.Response.Interrupted && outcome.Err == nil {
			t.Fatalf("cancel outcome = %#v", outcome)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Python host-result read did not exit after cell cancellation")
	}
}

func TestPythonReadFileCompatibilityModuleUsesArtifactAuthority(t *testing.T) {
	manager, worker := newHostCallTestSession(t, nil)
	artifact := filepath.Join(worker.workspaceDir, "source.json")
	if err := os.WriteFile(artifact, []byte(`{"compound":"itraconazole"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := &HostCallPolicy{
		AllowedMethods: []string{"host.artifact_path"},
		Handler: func(_ context.Context, call HostCall) (any, error) {
			if len(call.Args) != 1 || call.Args[0] != "version-1" {
				t.Fatalf("artifact path call=%#v", call)
			}
			return artifact, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-read-file-module", `
from read_file import read_file
print(read_file(version_id="version-1"))
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" ||
		strings.TrimSpace(outcome.Response.Stdout) != `{"compound":"itraconazole"}` {
		t.Fatalf("read_file compatibility outcome=%#v", outcome)
	}
}

func TestReplHostMCPUsesExactWireContractAndJSONResult(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	var calls atomic.Int64
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp"},
		Handler: func(_ context.Context, call HostCall) (any, error) {
			calls.Add(1)
			if call.Method != "mcp" || len(call.Args) != 3 || len(call.Kwargs) != 0 ||
				call.Args[0] != "pubmed" || call.Args[1] != "search_articles" {
				t.Fatalf("host MCP call=%#v", call)
			}
			input, ok := call.Args[2].(map[string]any)
			if !ok || input["query"] != "NEK7" {
				t.Fatalf("host MCP input=%#v", call.Args[2])
			}
			return `{"count":1,"source":"pubmed"}`, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp", `
import host
result = host.mcp("pubmed", "search_articles", query="NEK7")
print(result["count"], result["source"])
`, policy)
	if outcome.Err != nil || strings.TrimSpace(outcome.Response.Stdout) != "1 pubmed" || calls.Load() != 1 {
		t.Fatalf("outcome=%#v calls=%d", outcome, calls.Load())
	}
	invalid := executeHostCallCell(t, manager, "exec-mcp-invalid", `
import host, math
for invoke in (
    lambda: host.mcp("", "search"),
    lambda: host.mcp("pubmed", "search", value=math.nan),
):
    try:
        invoke()
    except Exception as exc:
        print(type(exc).__name__, str(exc))
`, policy)
	if invalid.Err != nil || !strings.Contains(invalid.Response.Stdout, "server must be a non-empty str") ||
		!strings.Contains(invalid.Response.Stdout, "arguments must be JSON serializable") || calls.Load() != 1 {
		t.Fatalf("invalid=%#v calls=%d", invalid, calls.Load())
	}
}

func TestReplHostExposesComputeNamespaceButEnforcesPerCellMethods(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"host.routine.status"},
		Handler: func(context.Context, HostCall) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-no-retired-compute", `
import host
print(hasattr(host, "compute"))
try:
    host.compute.status()
except Exception as exc:
    print(type(exc).__name__, str(exc))
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" ||
		!strings.Contains(outcome.Response.Stdout, "True") || !strings.Contains(outcome.Response.Stdout, "method is not allowed") {
		t.Fatalf("scoped compute namespace outcome=%#v", outcome)
	}
}

func TestHostCallPanicIsContainedAndWorkerRemainsUsable(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"host.routine.status"},
		Handler:        func(context.Context, HostCall) (any, error) { panic("not exposed") },
	}
	outcome := executeHostCallCell(t, manager, "exec-panic", "import host\nhost.routine.status()", policy)
	if outcome.Err != nil || !strings.Contains(outcome.Response.Error, "RuntimeError: host.routine.status: host call handler panicked") {
		t.Fatalf("panic outcome = %#v", outcome)
	}
	normal := executeHostCallCell(t, manager, "exec-after-panic", "print(6 * 7)", nil)
	if normal.Err != nil || normal.Response.Error != "" || strings.TrimSpace(normal.Response.Stdout) != "42" {
		t.Fatalf("normal execution after panic = %#v", normal)
	}
}

func TestHostCallLimitAndLargeResultArePreserved(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"host.routine.status"}, MaxCalls: 2,
		Handler: func(context.Context, HostCall) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
	limited := executeHostCallCell(t, manager, "exec-call-limit", `
import host
host.routine.status()
host.routine.status()
try:
    host.routine.status()
except RuntimeError as exc:
    print(str(exc))
`, policy)
	if limited.Err != nil || limited.Response.Error != "" || !strings.Contains(limited.Response.Stdout, "cell host-call limit of 2 was exceeded") {
		t.Fatalf("call limit outcome = %#v", limited)
	}
	large := &HostCallPolicy{
		AllowedMethods: []string{"host.routine.status"},
		Handler: func(context.Context, HostCall) (any, error) {
			return map[string]any{"value": strings.Repeat("λ", maxHostResultBytes) + "tail", "count": 9007199254740991}, nil
		},
	}
	bounded := executeHostCallCell(t, manager, "exec-result-bound", `
import host
result = host.routine.status()
assert result['count'] == 9007199254740991
assert result['value'] == 'λ' * (4 * 1024 * 1024) + 'tail'
print('full-result-preserved')
`, large)
	if bounded.Err != nil || bounded.Response.Error != "" || !strings.Contains(bounded.Response.Stdout, "full-result-preserved") {
		t.Fatalf("result bound outcome = %#v", bounded)
	}
	reused := executeHostCallCell(t, manager, "exec-after-large-result", "print(len(result['value']))", nil)
	if reused.Err != nil || reused.Response.Error != "" || strings.TrimSpace(reused.Response.Stdout) != fmt.Sprint(maxHostResultBytes+4) {
		t.Fatalf("successful result delivery discarded live kernel state: %#v", reused)
	}
}

func TestHostCallsRunConcurrentlyAcrossIndependentKernelProcesses(t *testing.T) {
	manager, first := newHostCallTestSession(t, nil)
	second, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-host-2", FrameID: "frame-host-2", RootFrameID: "frame-host-2",
		AgentName: "OPERON", KernelKind: "analysis", Language: "python", Environment: "python",
		WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("independent frame reused one kernel process")
	}
	started := make(chan string, 2)
	release := make(chan struct{})
	start := func(frameID, execID string) *ExecutionHandle {
		policy := &HostCallPolicy{
			AllowedMethods: []string{"host.routine.status"}, CallTimeout: 2 * time.Second,
			Handler: func(ctx context.Context, _ HostCall) (any, error) {
				started <- frameID
				select {
				case <-release:
					return map[string]any{"frame": frameID}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		}
		handle, err := manager.Submit(SubmitRequest{
			FrameID: frameID, KernelKind: "analysis", Language: "python", Environment: "python",
			ExecID: execID, ToolUseID: "tool-" + execID,
			Code: "import host\nprint(host.routine.status()['frame'])", Origin: "agent",
			Timeout: 5 * time.Second, HostCalls: policy,
		})
		if err != nil {
			t.Fatal(err)
		}
		return handle
	}
	left := start("frame-host", "exec-concurrent-1")
	right := start("frame-host-2", "exec-concurrent-2")
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case frameID := <-started:
			seen[frameID] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("host calls did not overlap: %#v", seen)
		}
	}
	close(release)
	for _, item := range []struct {
		handle *ExecutionHandle
		want   string
	}{{left, "frame-host"}, {right, "frame-host-2"}} {
		select {
		case outcome := <-item.handle.Done():
			if outcome.Err != nil || outcome.Response.Error != "" || strings.TrimSpace(outcome.Response.Stdout) != item.want {
				t.Fatalf("concurrent outcome = %#v want=%s", outcome, item.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("concurrent execution for %s did not finish", item.want)
		}
	}
}

func newHostCallTestSession(t *testing.T, environment map[string]string) (*Manager, *Worker) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	assetRoot := filepath.Join(root, "assets", "optional")
	manager := NewManager(Config{
		Python: python, AssetRoot: assetRoot,
		ManifestPath: filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:   filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		Environment:  environment, ExecutionTimeout: 5 * time.Second,
		InterruptGrace: 2 * time.Second, ShutdownTimeout: 2 * time.Second,
	})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-host", FrameID: "frame-host", RootFrameID: "frame-host",
		AgentName: "OPERON", KernelKind: "analysis", Language: "python", Environment: "python",
		WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = manager.CloseAll(ctx)
	})
	return manager, worker
}

func executeHostCallCell(t *testing.T, manager *Manager, execID, code string, policy *HostCallPolicy) ExecutionOutcome {
	t.Helper()
	handle, err := manager.Submit(SubmitRequest{
		FrameID: "frame-host", KernelKind: "analysis", Language: "python", Environment: "python",
		ExecID: execID, ToolUseID: "tool-" + execID, Code: code, Origin: "agent",
		Timeout: 5 * time.Second, InterruptGrace: 2 * time.Second, HostCalls: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-handle.Done():
		return outcome
	case <-time.After(8 * time.Second):
		t.Fatal(fmt.Sprintf("execution %s timed out", execID))
		return ExecutionOutcome{}
	}
}
