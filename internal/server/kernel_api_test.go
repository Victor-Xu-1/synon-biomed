package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func TestKernelSessionInventoryEnforcesOwnershipAndMatchesV11Shape(t *testing.T) {
	store, manager, app := newKernelAPITestRuntime(t)
	createKernelAPIProjectAndFrame(t, store, "project-a", "user-a", "root-a", "OPERON", "")
	createKernelAPIProjectAndFrame(t, store, "project-b", "user-b", "root-b", "OPERON", "")
	access, found, err := store.GetKernelFrameAccess("root-a")
	if err != nil || !found {
		t.Fatalf("kernel access found=%t err=%v", found, err)
	}
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "kernel-a", OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: "root-a", FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: "root-a", RootFrameIncarnationID: access.RootFrameIncarnationID, AgentName: "OPERON",
		Language: "python", Environment: "chem", WorkspaceDir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutionLog(workspace.SaveExecutionLogInput{Record: workspace.ExecutionLogRecord{
		ID: "cell-a", FrameID: "root-a", CellIndex: 1, KernelID: "kernel-a", KernelKind: "analysis",
		CondaEnv: "chem", Language: "python", Source: "print(1)", ExitStatus: "ok", Origin: "agent",
	}}); err != nil {
		t.Fatal(err)
	}

	denied := kernelAPIRequest(t, app, http.MethodGet, "/api/frames/root-a/kernels", "user-b", nil, http.StatusNotFound)
	if denied["detail"] != "Frame root-a not found" {
		t.Fatalf("denied response = %#v", denied)
	}
	response := kernelAPIRequest(t, app, http.MethodGet, "/api/frames/root-a/kernels", "user-a", nil, http.StatusOK)
	if response["has_history"] != true {
		t.Fatalf("inventory response = %#v", response)
	}
	if _, found := response["machine"]; found {
		t.Fatalf("frame inventory must not duplicate global machine metrics: %#v", response)
	}
	kernels, ok := response["kernels"].([]any)
	if !ok || len(kernels) != 1 {
		t.Fatalf("kernels = %#v", response["kernels"])
	}
	kernel := kernels[0].(map[string]any)
	wantKeys := []string{"agent_name", "busy", "cell_count", "cpu_pct", "current_cell", "current_cell_tag", "delegate_name", "environment", "execution_count", "execution_observation", "frame_id", "kernel_id", "kind", "language", "last_cell", "last_description", "last_used", "pid_visible", "project_id", "project_name", "root_frame_id", "rss_bytes", "starting"}
	gotKeys := make([]string, 0, len(kernel))
	for key := range kernel {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("kernel keys = %v", gotKeys)
	}
	for index := range wantKeys {
		if gotKeys[index] != wantKeys[index] {
			t.Fatalf("kernel keys = %v", gotKeys)
		}
	}
	if kernel["frame_id"] != "root-a" || kernel["agent_name"] != "OPERON" || kernel["environment"] != "chem" ||
		kernel["language"] != "python" || kernel["kernel_id"] != "kernel-a" || kernel["busy"] != false ||
		kernel["starting"] != false || kernel["current_cell_tag"] != nil || kernel["current_cell"] != nil ||
		kernel["execution_count"] != float64(0) || kernel["cell_count"] != float64(1) || kernel["delegate_name"] != nil ||
		kernel["project_id"] != "project-a" || kernel["root_frame_id"] != "root-a" || kernel["kind"] != "analysis" || kernel["pid_visible"] != true ||
		kernel["last_description"] != "print(1)" || kernel["last_cell"] == nil {
		t.Fatalf("kernel shape = %#v", kernel)
	}
	if _, err := time.Parse(time.RFC3339Nano, kernel["last_used"].(string)); err != nil {
		t.Fatalf("last_used = %#v: %v", kernel["last_used"], err)
	}

}

func TestSessionKernelHistoryIsScopedAndSurvivesWithoutLiveKernels(t *testing.T) {
	store, _, app := newKernelAPITestRuntime(t)
	createKernelAPIProjectAndFrame(t, store, "project-a", "user-a", "root-a", "OPERON", "")
	createKernelAPIProjectAndFrame(t, store, "project-b", "user-a", "root-b", "OPERON", "")
	if _, err := store.SaveExecutionLog(workspace.SaveExecutionLogInput{Record: workspace.ExecutionLogRecord{
		ID: "cell-history", FrameID: "root-a", CellIndex: 1, KernelID: "ended-kernel",
		KernelKind: "analysis", CondaEnv: "python", Language: "python", Source: "print(1)",
		ExitStatus: "ok", Origin: "agent",
	}}); err != nil {
		t.Fatal(err)
	}
	withHistory := kernelAPIRequest(t, app, http.MethodGet, "/api/frames/root-a/kernels", "user-a", nil, http.StatusOK)
	withoutHistory := kernelAPIRequest(t, app, http.MethodGet, "/api/frames/root-b/kernels", "user-a", nil, http.StatusOK)
	if withHistory["has_history"] != true || len(withHistory["kernels"].([]any)) != 0 {
		t.Fatalf("ended session history = %#v", withHistory)
	}
	if withoutHistory["has_history"] != false || len(withoutHistory["kernels"].([]any)) != 0 {
		t.Fatalf("unrelated session history = %#v", withoutHistory)
	}
}

func TestGlobalKernelInventoryEmptyMachineShapeMatchesWorkspace(t *testing.T) {
	_, _, app := newKernelAPITestRuntime(t)
	response := kernelAPIRequest(t, app, http.MethodGet, "/api/kernels", "user-a", nil, http.StatusOK)
	if len(response) != 2 || response["has_history"] != nil {
		t.Fatalf("global inventory envelope = %#v", response)
	}
	kernels, ok := response["kernels"].([]any)
	if !ok || len(kernels) != 0 {
		t.Fatalf("global kernels = %#v", response["kernels"])
	}
	machine, ok := response["machine"].(map[string]any)
	if !ok {
		t.Fatalf("global machine = %#v", response["machine"])
	}
	wantKeys := []string{
		"avail_mem_bytes", "busy_count", "cgroup_cpu_pct", "cores", "disk_avail_bytes",
		"disk_total_bytes", "host_cores", "kernel_count", "kernel_cpu_pct", "kernel_rss_bytes",
		"sampled_at", "total_cpu_pct", "total_mem_bytes",
	}
	gotKeys := make([]string, 0, len(machine))
	for key := range machine {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("global machine keys = %v", gotKeys)
	}
	if machine["kernel_count"] != float64(0) || machine["busy_count"] != float64(0) ||
		machine["kernel_rss_bytes"] != float64(0) || machine["kernel_cpu_pct"] != float64(0) {
		t.Fatalf("empty global machine = %#v", machine)
	}
}

func TestGlobalKernelInventoryIncludesLiveDetachedExecutorAndRejectsStalePID(t *testing.T) {
	store, _, app := newKernelAPITestRuntime(t)
	createKernelAPIProjectAndFrame(t, store, "project-a", "user-a", "root-a", "OPERON", "")
	access, found, err := store.GetKernelFrameAccess("root-a")
	if err != nil || !found {
		t.Fatalf("kernel access found=%t err=%v", found, err)
	}
	startTicks, err := kernelruntime.CurrentProcessStartTicks()
	if err != nil {
		t.Fatal(err)
	}
	createBackend := func(backendID, kernelID string, pid, ticks int64) {
		backend, err := store.CreateKernelExecutionBackend(context.Background(), workspace.CreateKernelExecutionBackendInput{
			BackendID: backendID, OwnerUserID: access.UserID, ProjectID: access.Frame.ProjectID,
			RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
			FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
			KernelID: kernelID, KernelGeneration: 1, ExecutorInstanceID: "executor-" + kernelID,
			MachineBootID: "machine-boot-api", SocketPath: "/run/user/1000/" + backendID + ".sock",
			BackendGeneration: 1,
			SessionSpec: workspace.KernelExecutionSessionSpecV1{
				Version: 1, KernelID: kernelID, OwnerUserID: access.UserID, ProjectID: access.Frame.ProjectID,
				RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
				FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID, AgentName: "OPERON",
				KernelKind: "analysis", Language: "python", Environment: "software-runtime",
				WorkspaceDir: t.TempDir(), Mounts: []workspace.KernelExecutionMountSpecV1{}, ProtectedPaths: []string{},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ActivateKernelExecutionBackend(context.Background(), workspace.ActivateKernelExecutionBackendInput{
			BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
			ExecutorInstanceID: backend.ExecutorInstanceID, ExecutorPID: pid, ExecutorPIDStartTicks: ticks,
			WorkerPID: pid, WorkerPIDStartTicks: ticks, WorkerPGID: pid,
			CgroupPath: "/synon-test.scope", HeartbeatSequence: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	createBackend("backend-api-live", "kernel-api-live", int64(os.Getpid()), startTicks)
	createBackend("backend-api-stale", "kernel-api-stale", int64(os.Getpid()), startTicks+1)

	response := kernelAPIRequest(t, app, http.MethodGet, "/api/kernels", "user-a", nil, http.StatusOK)
	kernels := response["kernels"].([]any)
	if len(kernels) != 1 {
		t.Fatalf("detached kernels = %#v", kernels)
	}
	kernel := kernels[0].(map[string]any)
	if kernel["kernel_id"] != "kernel-api-live" || kernel["environment"] != "software-runtime" ||
		kernel["pid_visible"] != true || kernel["rss_bytes"] == nil || kernel["starting"] != false {
		t.Fatalf("detached kernel = %#v", kernel)
	}
	machine := response["machine"].(map[string]any)
	if machine["kernel_count"] != float64(1) || machine["kernel_rss_bytes"].(float64) <= 0 {
		t.Fatalf("detached machine = %#v", machine)
	}
}

func TestDetachedKernelSessionProjectionExposesBusyCellAndHumanDescription(t *testing.T) {
	now := time.Now().UTC()
	entry := workspace.DetachedKernelInventoryEntry{
		Backend: workspace.KernelExecutionBackend{
			KernelID: "kernel-detached", State: workspace.KernelExecutionBackendStateReady, UpdatedAt: now,
		},
		SessionSpec: workspace.KernelExecutionSessionSpecV1{
			KernelID: "kernel-detached", OwnerUserID: "owner", ProjectID: "project",
			RootFrameID: "root", RootFrameIncarnationID: "root-inc", FrameID: "frame",
			FrameIncarnationID: "frame-inc", AgentName: "OPERON", KernelKind: "analysis",
			Language: "python", Environment: "software-runtime", RuntimeGeneration: "generation",
		},
		Operation: &workspace.KernelLocalOperation{
			Tool: "software_runtime", InputJSON: []byte(`{"capability":"molecular-diversity","executable":"python","args":["generate_ligands.py"]}`),
		},
		Execution: &workspace.DetachedKernelExecution{
			ExecutionID: "execution", State: workspace.DetachedKernelExecutionStateStarted,
			AcceptedAt: now.Add(-time.Minute), WorkerStartedAt: &now, UpdatedAt: now,
		},
		Request: &workspace.KernelDetachedExecutionRequestV1{
			ExecutionID: "execution", ToolCallID: "tool-call", ToolName: "software_runtime",
			Code: "print('running')", Origin: "agent",
		},
		ExecutionCount: 4,
	}
	projection := detachedKernelSessionProjection(entry)
	if !projection.Busy || projection.Starting || projection.ExecutionCount != 4 || projection.CurrentCell == nil ||
		projection.CurrentCellTag == nil || *projection.CurrentCellTag != "execution" ||
		projection.CurrentCell.HumanDescription != "molecular diversity · python generate_ligands.py" ||
		projection.CurrentCell.Source != projection.CurrentCell.HumanDescription || projection.CurrentCell.Truncated ||
		projection.LastDescription != projection.CurrentCell.HumanDescription {
		t.Fatalf("projection = %#v", projection)
	}
}

func TestStopDetachedKernelUsesDurableCancelAndSessionClose(t *testing.T) {
	backend := &recordingKernelExecutionBackend{}
	app := &Server{kernelExecutionBackend: backend}
	entry := workspace.DetachedKernelInventoryEntry{
		Backend: workspace.KernelExecutionBackend{
			BackendID: "backend", BackendGeneration: 2, KernelID: "kernel", KernelGeneration: 3,
			ExecutorInstanceID: "executor", SocketPath: "/run/kernel.sock",
		},
		Execution: &workspace.DetachedKernelExecution{
			ExecutionID: "execution", OperationID: "operation", BackendID: "backend", BackendGeneration: 2,
			RequestSHA256: strings.Repeat("a", 64), ConfinementSHA256: strings.Repeat("b", 64),
		},
	}
	interrupted, err := app.stopDetachedKernel(context.Background(), entry, "interrupt", "use a cheaper path")
	if err != nil {
		t.Fatal(err)
	}
	if interrupted["interrupted"] != true || len(interrupted) != 3 || backend.cancelCalls != 1 || backend.closeCalls != 0 ||
		backend.cancelRequest.ExpectedVersion != 7 || backend.cancelRequest.Reason != "use a cheaper path" ||
		!strings.HasPrefix(backend.cancelRequest.CancelRequestID, "kernel-ui-stop-") {
		t.Fatalf("interrupt=%#v backend=%#v", interrupted, backend)
	}
	killed, err := app.stopDetachedKernel(context.Background(), entry, "clear", "release resources")
	if err != nil {
		t.Fatal(err)
	}
	if killed["interrupted"] != true || len(killed) != 3 ||
		backend.cancelCalls != 2 || backend.closeCalls != 0 || backend.cancelRequest.Reason != "release resources" {
		t.Fatalf("clear=%#v backend=%#v", killed, backend)
	}
	idle := entry
	idle.Execution = nil
	idle.Request = nil
	if _, err := app.stopDetachedKernel(context.Background(), idle, "clear", ""); err != nil {
		t.Fatal(err)
	}
	if backend.closeCalls != 1 {
		t.Fatalf("idle close backend=%#v", backend)
	}
}

type recordingKernelExecutionBackend struct {
	cancelCalls   int
	closeCalls    int
	cancelRequest kernelruntime.BackendCancelRequest
}

func (*recordingKernelExecutionBackend) EnsureSession(context.Context, kernelruntime.SessionSpec) (kernelruntime.BackendSessionRef, error) {
	return kernelruntime.BackendSessionRef{}, nil
}
func (*recordingKernelExecutionBackend) AcquireSessionControl(context.Context, kernelruntime.BackendSessionRef, time.Duration) (kernelruntime.BackendControlLease, error) {
	return kernelruntime.BackendControlLease{Epoch: 1, Token: "token"}, nil
}
func (*recordingKernelExecutionBackend) Start(context.Context, kernelruntime.BackendExecutionRef, kernelruntime.BackendStartFence) (kernelruntime.BackendDispatchReceipt, error) {
	return kernelruntime.BackendDispatchReceipt{}, nil
}
func (*recordingKernelExecutionBackend) AcquireControl(context.Context, kernelruntime.BackendExecutionRef, time.Duration) (kernelruntime.BackendControlLease, kernelruntime.BackendExecutionSnapshot, error) {
	return kernelruntime.BackendControlLease{Epoch: 1, Token: "token"}, kernelruntime.BackendExecutionSnapshot{StateVersion: 7}, nil
}
func (*recordingKernelExecutionBackend) RenewControl(context.Context, kernelruntime.BackendControlLease, time.Duration) (kernelruntime.BackendControlLease, error) {
	return kernelruntime.BackendControlLease{}, nil
}
func (*recordingKernelExecutionBackend) Probe(context.Context, kernelruntime.BackendExecutionRef, kernelruntime.BackendControlLease) (kernelruntime.BackendExecutionSnapshot, error) {
	return kernelruntime.BackendExecutionSnapshot{}, nil
}
func (*recordingKernelExecutionBackend) Watch(context.Context, kernelruntime.BackendExecutionRef, kernelruntime.BackendControlLease, int64) (kernelruntime.ExecutionEventStream, error) {
	return nil, nil
}
func (backend *recordingKernelExecutionBackend) Cancel(_ context.Context, _ kernelruntime.BackendExecutionRef, _ kernelruntime.BackendControlLease, request kernelruntime.BackendCancelRequest) (kernelruntime.BackendCancelReceipt, error) {
	backend.cancelCalls++
	backend.cancelRequest = request
	return kernelruntime.BackendCancelReceipt{Signal: "sigint", Acknowledged: true}, nil
}
func (backend *recordingKernelExecutionBackend) CloseSession(context.Context, kernelruntime.BackendSessionRef, kernelruntime.BackendControlLease) error {
	backend.closeCalls++
	return nil
}

func TestGlobalKernelInventoryIsOwnerScopedAndStopMatchesWorkspace(t *testing.T) {
	store, manager, app := newKernelAPITestRuntime(t)
	createKernelAPIProjectAndFrame(t, store, "project-a", "user-a", "root-a", "OPERON", "")
	createKernelAPIProjectAndFrame(t, store, "project-b", "user-b", "root-b", "OPERON", "")
	for _, item := range []struct {
		frameID, kernelID string
	}{
		{frameID: "root-a", kernelID: "kernel-a"},
		{frameID: "root-b", kernelID: "kernel-b"},
	} {
		access, found, err := store.GetKernelFrameAccess(item.frameID)
		if err != nil || !found {
			t.Fatalf("access %s found=%t err=%v", item.frameID, found, err)
		}
		if _, err := manager.StartSession(kernelruntime.SessionSpec{
			KernelID: item.kernelID, OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
			FrameID: item.frameID, FrameIncarnationID: access.Frame.IncarnationID,
			RootFrameID: item.frameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
			AgentName: "OPERON", Language: "python", Environment: "chem", WorkspaceDir: t.TempDir(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, user := range []struct {
		id, wantKernel string
	}{
		{id: "user-a", wantKernel: "kernel-a"},
		{id: "user-b", wantKernel: "kernel-b"},
	} {
		response := kernelAPIRequest(t, app, http.MethodGet, "/api/kernels", user.id, nil, http.StatusOK)
		kernels := response["kernels"].([]any)
		if len(kernels) != 1 || kernels[0].(map[string]any)["kernel_id"] != user.wantKernel {
			t.Fatalf("%s global kernels = %#v", user.id, kernels)
		}
		machine := response["machine"].(map[string]any)
		if machine["kernel_count"] != float64(1) || machine["cores"].(float64) < 1 ||
			machine["sampled_at"] == "" {
			t.Fatalf("%s machine = %#v", user.id, machine)
		}
		if runtime.GOOS == "linux" && (machine["total_mem_bytes"] == nil || machine["disk_avail_bytes"] == nil) {
			t.Fatalf("%s Linux machine metrics = %#v", user.id, machine)
		}
	}

	kernelAPIRequest(t, app, http.MethodPost, "/api/frames/root-a/kernels/kernel-a/stop", "user-b",
		map[string]any{"mode": "clear"}, http.StatusNotFound)
	stopped := kernelAPIRequest(t, app, http.MethodPost, "/api/frames/root-a/kernels/kernel-a/stop", "user-a",
		map[string]any{"mode": "clear"}, http.StatusOK)
	if stopped["ok"] != true || stopped["interrupted"] != false || stopped["mode"] != "clear" || len(stopped) != 3 {
		t.Fatalf("stop response = %#v", stopped)
	}
	if kernels := manager.ListSessionKernels("root-a"); len(kernels) != 0 {
		t.Fatalf("root-a kernels after stop = %#v", kernels)
	}
	if kernels := manager.ListSessionKernels("root-b"); len(kernels) != 1 {
		t.Fatalf("root-b kernels after foreign stop = %#v", kernels)
	}
}

func TestKernelStopReasonForceAndUnicodeContractMatchesWorkspace(t *testing.T) {
	store, manager, app := newKernelAPITestRuntime(t)
	createKernelAPIProjectAndFrame(t, store, "project-a", "user-a", "root-a", "OPERON", "")
	access, found, err := store.GetKernelFrameAccess("root-a")
	if err != nil || !found {
		t.Fatalf("kernel access found=%t err=%v", found, err)
	}
	workspaceDir := t.TempDir()
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "kernel-a", OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: "root-a", FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: "root-a", RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}

	tooLong := kernelAPIRequest(t, app, http.MethodPost, "/api/frames/root-a/kernels/kernel-a/stop", "user-a",
		map[string]any{"mode": "interrupt", "reason": strings.Repeat("药", 501)}, http.StatusBadRequest)
	if tooLong["detail"] != "reason must be at most 500 characters" {
		t.Fatalf("long reason response = %#v", tooLong)
	}

	handle, err := manager.Submit(kernelruntime.SubmitRequest{
		KernelID: "kernel-a", OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: "root-a", FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameIncarnationID: access.RootFrameIncarnationID,
		Language:               "python", Environment: "python", ExecID: "exec-stop-reason", ToolUseID: "tool-stop-reason",
		Origin: "agent", WorkingDir: workspaceDir,
		Code: "from pathlib import Path\nPath('stop-reason-started').write_text('ready')\nwhile True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForKernelAPIFile(t, filepath.Join(workspaceDir, "stop-reason-started"))
	reason := "this is too expensive — use a cheaper path"
	interrupted := kernelAPIRequest(t, app, http.MethodPost, "/api/frames/root-a/kernels/kernel-a/stop", "user-a",
		map[string]any{"mode": "interrupt", "reason": reason}, http.StatusOK)
	if interrupted["ok"] != true || interrupted["mode"] != "interrupt" || interrupted["interrupted"] != true ||
		len(interrupted) != 3 {
		t.Fatalf("interrupt response = %#v", interrupted)
	}
	select {
	case outcome := <-handle.Done():
		if !strings.Contains(outcome.Response.Stderr, reason) &&
			(outcome.Err == nil || !strings.Contains(outcome.Err.Error(), reason)) {
			t.Fatalf("stop reason was not delivered to execution: response=%#v err=%v", outcome.Response, outcome.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted execution did not settle")
	}

	attachedHandle, err := manager.Submit(kernelruntime.SubmitRequest{
		KernelID: "kernel-a", OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: "root-a", FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameIncarnationID: access.RootFrameIncarnationID,
		Language:               "python", Environment: "python", ExecID: "exec-attach-reason", ToolUseID: "tool-attach-reason",
		Origin: "agent", WorkingDir: workspaceDir,
		Code: "from pathlib import Path\nPath('attach-reason-started').write_text('ready')\nwhile True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForKernelAPIFile(t, filepath.Join(workspaceDir, "attach-reason-started"))
	attached := kernelAPIRequest(t, app, http.MethodPost, "/api/frames/root-a/kernels/kernel-a/stop", "user-a",
		map[string]any{"mode": "interrupt", "reason": "释放资源", "force": true, "attach_only": true}, http.StatusOK)
	if attached["ok"] != true || attached["mode"] != "interrupt" || attached["interrupted"] != false || len(attached) != 3 {
		t.Fatalf("attach-only response = %#v", attached)
	}
	if kernels := manager.ListSessionKernels("root-a"); len(kernels) != 1 {
		t.Fatalf("attach_only stopped kernel: %#v", kernels)
	}
	select {
	case outcome := <-attachedHandle.Done():
		t.Fatalf("attach_only settled active execution: %#v", outcome)
	case <-time.After(200 * time.Millisecond):
	}
	forced := kernelAPIRequest(t, app, http.MethodPost, "/api/frames/root-a/kernels/kernel-a/stop", "user-a",
		map[string]any{"mode": "interrupt", "reason": "释放资源", "force": true}, http.StatusOK)
	if forced["ok"] != true || forced["mode"] != "interrupt" || forced["interrupted"] != true || len(forced) != 3 {
		t.Fatalf("force response = %#v", forced)
	}
	select {
	case outcome := <-attachedHandle.Done():
		if !strings.Contains(outcome.Response.Stderr, "释放资源") &&
			(outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "释放资源")) {
			t.Fatalf("attached stop reason was not delivered: response=%#v err=%v", outcome.Response, outcome.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("force-stopped attached execution did not settle")
	}
	if kernels := manager.ListSessionKernels("root-a"); len(kernels) != 0 {
		t.Fatalf("kernels after force stop = %#v", kernels)
	}
}

func TestKernelTerminalInterruptPublishesV11StartAndDoneEvents(t *testing.T) {
	store, manager, app := newKernelAPITestRuntime(t)
	createKernelAPIProjectAndFrame(t, store, "project-a", "user-a", "root-a", "OPERON", "")
	access, found, err := store.GetKernelFrameAccess("root-a")
	if err != nil || !found {
		t.Fatalf("kernel access found=%t err=%v", found, err)
	}
	workspaceDir := t.TempDir()
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "kernel-a", OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: "root-a", FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: "root-a", RootFrameIncarnationID: access.RootFrameIncarnationID, AgentName: "OPERON",
		Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}
	execute := kernelAPIRequest(t, app, http.MethodPost, "/api/frames/root-a/kernel-exec", "user-a", map[string]any{
		"language": "python", "environment": "python",
		"code": "from pathlib import Path\nPath('api-started').write_text('ready')\nwhile True:\n    pass",
	}, http.StatusOK)
	execID, _ := execute["exec_id"].(string)
	toolUseID, _ := execute["tool_use_id"].(string)
	if execID == "" || toolUseID != "user-"+execID {
		t.Fatalf("execute response = %#v", execute)
	}
	waitForKernelAPIFile(t, filepath.Join(workspaceDir, "api-started"))
	interrupt := kernelAPIRequest(t, app, http.MethodPost, "/api/frames/root-a/kernel-exec/"+execID+"/interrupt", "user-a", map[string]any{}, http.StatusOK)
	if len(interrupt) != 2 || interrupt["interrupted"] != true || interrupt["via"] != "sigint" {
		t.Fatalf("interrupt response = %#v", interrupt)
	}

	events := waitForKernelAPIEvents(t, store, "user-a", "root-a", 2)
	start, done := events[0].Payload, events[1].Payload
	if start["phase"] != "start" || start["origin"] != "user" || start["language"] != "python" ||
		start["environment"] != "python" || start["kernel_target"] != "analysis" ||
		start["source"] == "" || start["started_at"] == "" {
		t.Fatalf("start event = %#v", start)
	}
	if done["phase"] != "done" || done["origin"] != "user" || done["language"] != "python" ||
		done["environment"] != "python" || done["kernel_target"] != "analysis" ||
		done["cell_id"] != execID || done["cancelled"] != true || done["exit_code"] != nil ||
		done["duration_ms"].(float64) < 0 {
		t.Fatalf("done event = %#v", done)
	}
	if events[0].FrameID != "root-a" || events[0].RootFrameID != "root-a" || events[0].ProjectID != "project-a" {
		t.Fatalf("event scope = %#v", events[0])
	}
	records, err := store.ListExecutionLog("root-a", "")
	if err != nil || len(records) != 1 || records[0].ExitStatus != "cancelled" || records[0].ID != execID {
		t.Fatalf("execution records = %#v, err=%v", records, err)
	}
}

func TestKernelExecutionPublishesLiveAndReplayEvents(t *testing.T) {
	store, manager, _ := newKernelAPITestRuntime(t)
	createKernelAPIProjectAndFrame(t, store, "project-a", "user-a", "root-a", "OPERON", "")
	access, found, err := store.GetKernelFrameAccess("root-a")
	if err != nil || !found {
		t.Fatalf("kernel access found=%t err=%v", found, err)
	}
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "kernel-a", OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: "root-a", FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: "root-a", RootFrameIncarnationID: access.RootFrameIncarnationID, AgentName: "OPERON",
		Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	serverApp := New(Options{Workspace: store, KernelManager: manager})
	startServerRealtimeOutbox(t, store, serverApp)
	httpServer := httptest.NewServer(serverApp.Handler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	websocketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/ws?user_id=user-a&after_sequence=0"
	liveConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if connected := readCompatWebSocketTest(t, ctx, liveConnection); connected["type"] != "connected" {
		t.Fatalf("live websocket handshake=%#v", connected)
	}
	if err := writeWebSocketJSON(ctx, liveConnection, map[string]any{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
	if pong := readCompatWebSocketTest(t, ctx, liveConnection); pong["type"] != "pong" {
		t.Fatalf("pong=%#v", pong)
	}
	if err := writeWebSocketJSON(ctx, liveConnection, map[string]any{
		"type": "kernel_user_exec", "request_id": "request-a", "frame_id": "root-a",
		"language": "python", "environment": "python", "code": "print('kernel-event')",
	}); err != nil {
		t.Fatal(err)
	}
	ack := readCompatWebSocketTest(t, ctx, liveConnection)
	if ack["type"] != "kernel_terminal_ack" || ack["request_id"] != "request-a" || ack["ok"] != true ||
		ack["exec_id"] == "" || ack["tool_use_id"] != "user-"+ack["exec_id"].(string) {
		t.Fatalf("kernel terminal acknowledgement=%#v", ack)
	}
	waitForKernelAPIEvents(t, store, "user-a", "root-a", 2)
	want := map[string]int{"execution_cell_update": 2}
	liveEvents, err := readDomainWebSocketEvents(ctx, liveConnection, want, 2)
	if err != nil {
		t.Fatalf("live kernel events=%v err=%v", domainEventTypes(liveEvents), err)
	}
	if err := liveConnection.Close(websocket.StatusNormalClosure, "reconnect for replay"); err != nil {
		t.Fatal(err)
	}
	replayConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer replayConnection.Close(websocket.StatusNormalClosure, "test complete")
	if connected := readCompatWebSocketTest(t, ctx, replayConnection); connected["type"] != "connected" {
		t.Fatalf("replay websocket handshake=%#v", connected)
	}
	replayed, err := readDomainWebSocketEvents(ctx, replayConnection, want, 2)
	if err != nil {
		t.Fatalf("replayed kernel events=%v err=%v", domainEventTypes(replayed), err)
	}
	if !reflect.DeepEqual(liveEvents, replayed) {
		t.Fatalf("live and replayed kernel events differ:\nlive=%#v\nreplayed=%#v", liveEvents, replayed)
	}
	foreignURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/ws?user_id=user-b&after_sequence=0"
	foreignConnection, _, err := websocket.Dial(ctx, foreignURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer foreignConnection.Close(websocket.StatusNormalClosure, "test complete")
	if connected := readCompatWebSocketTest(t, ctx, foreignConnection); connected["type"] != "connected" {
		t.Fatalf("foreign websocket handshake=%#v", connected)
	}
	if err := writeWebSocketJSON(ctx, foreignConnection, map[string]any{
		"type": "kernel_user_exec", "request_id": "request-foreign", "frame_id": "root-a",
		"language": "python", "environment": "python", "code": "print('denied')",
	}); err != nil {
		t.Fatal(err)
	}
	denied := readCompatWebSocketTest(t, ctx, foreignConnection)
	if denied["type"] != "kernel_terminal_ack" || denied["request_id"] != "request-foreign" ||
		denied["ok"] != false || denied["error"] != "Frame root-a not found" {
		t.Fatalf("foreign kernel acknowledgement=%#v", denied)
	}
}

func TestRefreshKernelsUsesExactV11ResponseAndCleansRunningExecution(t *testing.T) {
	_, manager, app := newKernelAPITestRuntime(t)
	workspaceDir := t.TempDir()
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "kernel-refresh", FrameID: "frame-refresh", RootFrameID: "frame-refresh", AgentName: "OPERON",
		Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(kernelruntime.SubmitRequest{
		FrameID: "frame-refresh", Language: "python", Environment: "python",
		ExecID: "exec-refresh", ToolUseID: "user-exec-refresh", Origin: "user",
		Code: "from pathlib import Path\nPath('refresh-started').write_text('ready')\nwhile True:\n    pass",
	}); err != nil {
		t.Fatal(err)
	}
	waitForKernelAPIFile(t, filepath.Join(workspaceDir, "refresh-started"))
	response := kernelAPIRequest(t, app, http.MethodPost, "/api/system/refresh-kernels", "local", map[string]any{}, http.StatusOK)
	if len(response) != 1 || response["closed_count"] != float64(1) {
		t.Fatalf("refresh response = %#v", response)
	}
	if manager.ActiveCount() != 0 || manager.ActiveExecutionCount() != 0 {
		t.Fatalf("post-refresh active kernels=%d executions=%d", manager.ActiveCount(), manager.ActiveExecutionCount())
	}
}

func newKernelAPITestRuntime(t *testing.T) (*workspace.Store, *kernelruntime.Manager, http.Handler) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	assetRoot := filepath.Join(repositoryRoot, "assets", "optional")
	managedEnvs := filepath.Join(t.TempDir(), "envs")
	managedPython := filepath.Join(managedEnvs, "chem", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(managedPython), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(python, managedPython); err != nil {
		t.Fatal(err)
	}
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: python, AssetRoot: assetRoot, CondaEnvsPath: managedEnvs,
		ManifestPath:     filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:       filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		ExecutionTimeout: 5 * time.Second, InterruptGrace: 2 * time.Second, ShutdownTimeout: 2 * time.Second,
	})
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, KernelManager: manager})
	mux := http.NewServeMux()
	server.registerKernelRoutes(mux)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = manager.CloseAll(ctx)
		_ = store.Close()
	})
	return store, manager, mux
}

func createKernelAPIProjectAndFrame(t *testing.T, store *workspace.Store, projectID, userID, frameID, agentName, parentID string) {
	t.Helper()
	if _, found, err := store.GetProject(projectID); err != nil {
		t.Fatal(err)
	} else if !found {
		if _, err := store.CreateProject(workspace.CreateProjectInput{ID: projectID, UserID: userID, Name: projectID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: projectID, ParentFrameID: parentID, AgentName: agentName,
		Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
}

func kernelAPIRequest(t *testing.T, app http.Handler, method, target, userID string, body map[string]any, wantStatus int) map[string]any {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", userID)
	app.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status=%d body=%s", method, target, recorder.Code, recorder.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s %s: %v (%s)", method, target, err, recorder.Body.String())
	}
	return decoded
}

func waitForKernelAPIFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("kernel marker %s was not created", path)
}

func waitForKernelAPIEvents(t *testing.T, store *workspace.Store, userID, rootFrameID string, count int) []workspace.RealtimeEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
			UserID: userID, RootFrameID: rootFrameID, Type: "execution_cell_update", Limit: 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) >= count {
			return events
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not observe %d execution_cell_update events", count)
	return nil
}
