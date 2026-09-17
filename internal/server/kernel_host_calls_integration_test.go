package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func TestKernelHostRoutineClaimScopedDoneSurvivesRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)

	configured := runKernelHostCell(t, app, identity, `
import host
configured = host.routine.configure(5, on_tick="check the tracked work", label="Tracker")
status = host.routine.status()
print(configured["configured"], status["every_minutes"], status["label"])
`)
	if configured["ok"] != true || strings.TrimSpace(configured["stdout"].(string)) != "True 5 Tracker" {
		t.Fatalf("configure/status result = %#v", configured)
	}
	operon := runKernelHostCellNamed(t, app, identity, "Operon", `
import host
print(host.routine.status()["on_tick"])
`)
	if operon["ok"] != true || strings.TrimSpace(operon["stdout"].(string)) != "check the tracked work" {
		t.Fatalf("Operon host import/status = %#v", operon)
	}
	routine, found, err := store.GetRoutineByRoot(context.Background(), identity.access.Frame.RootFrameID)
	if err != nil || !found || !routine.Enabled || routine.EveryMinutes != 5 || routine.OnTick != "check the tracked work" || routine.Label != "Tracker" {
		t.Fatalf("configured routine = %#v found=%t err=%v", routine, found, err)
	}

	// A normal owner kernel has configure/status access, but done is invalid
	// until the scheduler has established a durable claim generation.
	unclaimed := runKernelHostCell(t, app, identity, `
import host
host.routine.done(False, "must be rejected")
`)
	if unclaimed["ok"] != false || !strings.Contains(unclaimed["stderr"].(string), "not claimed by the scheduler") {
		t.Fatalf("unclaimed done result = %#v", unclaimed)
	}

	claimAt := routine.NextDue.Add(time.Second)
	claimed, ok, err := store.ClaimNextDueRoutineRealtime(context.Background(), claimAt, time.Minute, "user-routine", "host-test-claim-1")
	if err != nil || !ok || claimed.LockedAt == nil {
		t.Fatalf("first claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	identity.access, _, err = store.GetKernelFrameAccess(identity.access.Frame.ID)
	if err != nil {
		t.Fatal(err)
	}
	doneFalse := runKernelHostCell(t, app, identity, `
import host
result = host.routine.done(False, "no new work")
print(result["had_work"], result["summary"])
`)
	if doneFalse["ok"] != true || strings.TrimSpace(doneFalse["stdout"].(string)) != "False no new work" {
		t.Fatalf("done(false) = %#v", doneFalse)
	}

	// Simulate a process crash after done persisted but before scheduler
	// completion. The marker must survive closing both worker and SQLite.
	closeKernelHostTestRuntime(t, app, manager, store)
	store, manager, app, identity = newKernelHostTestRuntime(t, databasePath, false)
	afterRestart, err := store.CompleteRoutineTickRealtime(context.Background(), claimed, claimAt.Add(time.Second), true, "tick completed")
	if err != nil {
		t.Fatal(err)
	}
	if afterRestart.IdleStreak != 1 {
		t.Fatalf("done(false) after restart = %#v err=%v", afterRestart, err)
	}

	secondClaimAt := afterRestart.NextDue.Add(time.Second)
	second, ok, err := store.ClaimNextDueRoutineRealtime(context.Background(), secondClaimAt, time.Minute, "user-routine", "host-test-claim-2")
	if err != nil || !ok || second.LockedAt == nil {
		t.Fatalf("second claim = %#v ok=%t err=%v", second, ok, err)
	}
	if _, err := store.RecordRoutineHostDone(context.Background(), claimed.RootFrameID, claimed.OwnerUserID, claimed, false, "stale first tick"); err == nil || !strings.Contains(err.Error(), "execution generation") {
		t.Fatalf("stale first-generation done error = %v", err)
	}
	// Repeated done in one generation is explicit last-write-wins. This also
	// proves a false marker can be safely overwritten by true.
	overwrite := runKernelHostCell(t, app, identity, `
import host
host.routine.done(False, "first report")
host.routine.done(True, "work arrived")
print(host.routine.status()["configured"])
`)
	if overwrite["ok"] != true || strings.TrimSpace(overwrite["stdout"].(string)) != "True" {
		t.Fatalf("repeated done = %#v", overwrite)
	}
	afterWork, err := store.CompleteRoutineTickRealtime(context.Background(), second, secondClaimAt.Add(time.Second), true, "second tick")
	if err != nil {
		t.Fatal(err)
	}
	if afterWork.IdleStreak != 0 {
		t.Fatalf("done(true) completion = %#v err=%v", afterWork, err)
	}

	// Without host.done, retain the existing successful/failed policy.
	thirdClaimAt := afterWork.NextDue.Add(time.Second)
	third, ok, err := store.ClaimNextDueRoutineRealtime(context.Background(), thirdClaimAt, time.Minute, "user-routine", "host-test-claim-3")
	if err != nil || !ok || third.LockedAt == nil {
		t.Fatalf("third claim = %#v ok=%t err=%v", third, ok, err)
	}
	afterFailure, err := store.CompleteRoutineTickRealtime(context.Background(), third, thirdClaimAt.Add(time.Second), false, "failed without done")
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.IdleStreak != 1 {
		t.Fatalf("fallback failure policy = %#v err=%v", afterFailure, err)
	}
	fourthClaimAt := afterFailure.NextDue.Add(time.Second)
	fourth, ok, err := store.ClaimNextDueRoutineRealtime(context.Background(), fourthClaimAt, time.Minute, "user-routine", "host-test-claim-4")
	if err != nil || !ok {
		t.Fatalf("fourth claim ok=%t err=%v", ok, err)
	}
	afterSuccess, err := store.CompleteRoutineTickRealtime(context.Background(), fourth, fourthClaimAt.Add(time.Second), true, "success without done")
	if err != nil {
		t.Fatal(err)
	}
	if afterSuccess.IdleStreak != 0 {
		t.Fatalf("fallback success policy = %#v err=%v", afterSuccess, err)
	}

	closeKernelHostTestRuntime(t, app, manager, store)
}

func TestKernelHostRoutineInvalidArgumentsAndForgedOwnerDoNotWrite(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	invalid := runKernelHostCell(t, app, identity, `
import host
try:
    host.routine.configure(True, on_tick="must not persist")
except Exception as exc:
    print(type(exc).__name__, str(exc))
`)
	if invalid["ok"] != true || !strings.Contains(invalid["stdout"].(string), "TypeError host.routine.configure") {
		t.Fatalf("invalid argument result = %#v", invalid)
	}
	if routine, found, err := store.GetRoutineByRoot(context.Background(), identity.access.Frame.RootFrameID); err != nil || found {
		t.Fatalf("invalid configure wrote routine=%#v found=%t err=%v", routine, found, err)
	}

	forged := *identity
	forged.access.UserID = "forged-owner"
	_, deniedErr := app.executeAgentKernelTool(context.Background(), &forged, "Operon", map[string]any{"code": `
import host
host.routine.configure(5, on_tick="must be denied")
`})
	if deniedErr == nil || !strings.Contains(deniedErr.Error(), "kernel frame ownership changed or is invalid") {
		t.Fatalf("forged owner error=%v", deniedErr)
	}
	if routine, found, err := store.GetRoutineByRoot(context.Background(), identity.access.Frame.RootFrameID); err != nil || found {
		t.Fatalf("forged owner wrote routine=%#v found=%t err=%v", routine, found, err)
	}
}

func TestAgentPythonKernelExposesOnlyReferenceLocalHostFunctions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	denied := runKernelHostCellNamed(t, app, identity, "Python", `
import host
print(hasattr(host, "view_image"))
host.routine.configure(5, on_tick="must not persist")
`)
	if denied["ok"] != false || denied["code"] != "python_execution_failed" || denied["preflight"] != nil ||
		!strings.Contains(stringValue(denied["stdout"]), "True") ||
		!strings.Contains(stringValue(denied["stderr"]), "host method is not allowed") {
		t.Fatalf("Python host bridge result=%#v", denied)
	}
	if routine, found, err := store.GetRoutineByRoot(context.Background(), identity.access.Frame.RootFrameID); err != nil || found {
		t.Fatalf("Python host bridge wrote routine=%#v found=%t err=%v", routine, found, err)
	}
}

func TestKernelExecutionRejectsChangedProjectPathAuthority(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	changedPath := t.TempDir()
	if _, err := store.UpdateProject(identity.access.Frame.ProjectID, workspace.UpdateProjectInput{Path: &changedPath}); err != nil {
		t.Fatal(err)
	}
	_, err := app.executeAgentKernelTool(context.Background(), identity, "Operon", map[string]any{"code": "print('must not execute')"})
	if err == nil || !strings.Contains(err.Error(), "ownership changed") {
		t.Fatalf("changed project path error=%v", err)
	}
	if records, listErr := store.ListExecutionLog(identity.access.Frame.ID, ""); listErr != nil || len(records) != 0 {
		t.Fatalf("changed project path execution records=%#v err=%v", records, listErr)
	}
}

func TestReplHostMCPUsesOwnerGrantSchemaAndRealStdioConnector(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	marker := filepath.Join(t.TempDir(), "mcp-calls.log")
	command := writeKernelMCPStdioFixture(t, marker)
	enabled := true
	server, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "custom:remote", UserID: identity.access.UserID, Name: "remote",
		URL: command, Transport: "stdio", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMCPToolGrant(workspace.MCPToolGrantInput{
		ID: "grant-remote-echo", MCPServerID: server.ID, UserID: identity.access.UserID,
		AgentName: workspace.GlobalMCPToolGrantAgent, ToolName: "echo", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code": `import host
value = host.mcp("remote", "echo", text="NEK7")
print(value["echo"])
`,
	}, []string{"mcp"})
	if err != nil || result["ok"] != true || strings.TrimSpace(stringValue(result["stdout"])) != "NEK7" {
		t.Fatalf("host MCP result=%#v err=%v", result, err)
	}
	called, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(called), "call\n") != 1 {
		t.Fatalf("MCP calls=%q err=%v", called, err)
	}

	invalid, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code": `import host
host.mcp("remote", "echo")
`,
	}, []string{"mcp__remote__echo"})
	if err != nil || invalid["ok"] != false || !strings.Contains(stringValue(invalid["stderr"]), "MCP input validation failed") ||
		!strings.Contains(stringValue(invalid["stderr"]), "Allowed keys: text") ||
		!strings.Contains(stringValue(invalid["stderr"]), "required keys: text") ||
		strings.Contains(stringValue(invalid["stderr"]), `"required"`) {
		t.Fatalf("invalid MCP result=%#v err=%v", invalid, err)
	}
	called, err = os.ReadFile(marker)
	if err != nil || strings.Count(string(called), "call\n") != 1 {
		t.Fatalf("invalid MCP reached connector calls=%q err=%v", called, err)
	}

	denied, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code": `import host
host.mcp("remote", "echo", text="blocked")
`,
	}, []string{"python"})
	if err != nil || denied["ok"] != false || !strings.Contains(stringValue(denied["stderr"]), "outside the agent tool boundary") {
		t.Fatalf("denied MCP result=%#v err=%v", denied, err)
	}
	called, err = os.ReadFile(marker)
	if err != nil || strings.Count(string(called), "call\n") != 1 {
		t.Fatalf("denied MCP reached connector calls=%q err=%v", called, err)
	}

	if deleted, err := store.DeleteGlobalMCPToolGrant(server.ID, identity.access.UserID, "echo"); err != nil || !deleted {
		t.Fatalf("delete allow grant deleted=%t err=%v", deleted, err)
	}
	if _, err := app.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "ask"}); err != nil {
		t.Fatal(err)
	}
	type hostMCPReturn struct {
		result map[string]any
		err    error
	}
	returned := make(chan hostMCPReturn, 1)
	go func() {
		result, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
			"code": `import host
print(host.mcp("remote", "echo", text="approved")["echo"])
`,
		}, []string{"mcp__remote__echo"})
		returned <- hostMCPReturn{result: result, err: err}
	}()
	approvalID := ""
	deadline := time.Now().Add(5 * time.Second)
	for approvalID == "" && time.Now().Before(deadline) {
		entries, listErr := app.runtimeStore.List(agentRuntimeApprovalNamespace)
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, entry := range entries {
			value := mapValue(entry.Value)
			if value["status"] == "pending" && value["approvalSource"] == "kernel-host-mcp" {
				approvalID = entry.Key
				break
			}
		}
		if approvalID == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if approvalID == "" {
		t.Fatal("kernel MCP approval was not queued")
	}
	mutatedName := "remote-mutated"
	if _, err := store.UpdateMCPServer(server.ID, identity.access.UserID, workspace.UpdateMCPServerInput{Name: &mutatedName}); err != nil {
		t.Fatal(err)
	}
	approval, err := app.resolveAgentRuntimeApprovalMessage(context.Background(), "", map[string]any{
		"approvalId": approvalID, "approve": true,
	})
	if err != nil || mapValue(approval)["status"] != "approved" {
		t.Fatalf("approval=%#v err=%v", approval, err)
	}
	select {
	case completed := <-returned:
		if completed.err != nil || completed.result["ok"] != false || !strings.Contains(stringValue(completed.result["stderr"]), "unknown") {
			t.Fatalf("mutated connector result=%#v err=%v", completed.result, completed.err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("mutated kernel MCP call did not resume")
	}
	called, err = os.ReadFile(marker)
	if err != nil || strings.Count(string(called), "call\n") != 1 {
		t.Fatalf("mutated MCP reached connector calls=%q err=%v", called, err)
	}
	originalName := "remote"
	if _, err := store.UpdateMCPServer(server.ID, identity.access.UserID, workspace.UpdateMCPServerInput{Name: &originalName}); err != nil {
		t.Fatal(err)
	}
	returned = make(chan hostMCPReturn, 1)
	go func() {
		result, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
			"code": `import host
print(host.mcp("remote", "echo", text="approved")["echo"])
`,
		}, []string{"mcp__remote__echo"})
		returned <- hostMCPReturn{result: result, err: err}
	}()
	secondApprovalID := ""
	deadline = time.Now().Add(5 * time.Second)
	for secondApprovalID == "" && time.Now().Before(deadline) {
		entries, listErr := app.runtimeStore.List(agentRuntimeApprovalNamespace)
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, entry := range entries {
			value := mapValue(entry.Value)
			if entry.Key != approvalID && value["status"] == "pending" && value["approvalSource"] == "kernel-host-mcp" {
				secondApprovalID = entry.Key
				break
			}
		}
		if secondApprovalID == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if secondApprovalID == "" {
		t.Fatal("second kernel MCP approval was not queued")
	}
	approval, err = app.resolveAgentRuntimeApprovalMessage(context.Background(), "", map[string]any{
		"approvalId": secondApprovalID, "approve": true,
	})
	if err != nil || mapValue(approval)["status"] != "approved" {
		t.Fatalf("second approval=%#v err=%v", approval, err)
	}
	select {
	case completed := <-returned:
		if completed.err != nil || completed.result["ok"] != true || strings.TrimSpace(stringValue(completed.result["stdout"])) != "approved" {
			t.Fatalf("approved host MCP result=%#v err=%v", completed.result, completed.err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("approved kernel MCP call did not resume")
	}
	called, err = os.ReadFile(marker)
	if err != nil || strings.Count(string(called), "call\n") != 2 {
		t.Fatalf("approved MCP calls=%q err=%v", called, err)
	}
	audits, err := store.ListFrameEvents(identity.access.Frame.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	auditStatuses := map[string]bool{}
	for _, entry := range audits {
		if entry.Type == "kernel_mcp_audit_terminal" {
			auditStatuses[stringValue(entry.Payload["status"])] = true
		}
	}
	if !auditStatuses["completed"] || !auditStatuses["blocked"] {
		t.Fatalf("kernel MCP audit statuses=%#v", auditStatuses)
	}
}

func writeKernelMCPStdioFixture(t *testing.T, marker string) string {
	return writeKernelMCPStdioFixtureWithTool(t, marker, "echo")
}

func writeKernelMCPStdioFixtureWithTool(t *testing.T, marker, toolName string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp-fixture.py")
	script := `#!/usr/bin/env python3
import json
import sys

marker = ` + fmt.Sprintf("%q", marker) + `
tool_name = ` + fmt.Sprintf("%q", toolName) + `
for line in sys.stdin:
    request = json.loads(line)
    method = request.get("method")
    if method == "notifications/initialized":
        continue
    if method == "initialize":
        result = {"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"kernel-mcp","version":"1"}}
    elif method == "tools/list":
        result = {"tools":[{"name":tool_name,"description":"Echo text","inputSchema":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":False},"annotations":{"readOnlyHint":True}}]}
    elif method == "tools/call":
        with open(marker, "a", encoding="utf-8") as output:
            output.write("call\n")
        text = request["params"]["arguments"]["text"]
        result = {"content":[{"type":"text","text":json.dumps({"echo":text}, separators=(",",":"))}]}
    else:
        result = {}
    print(json.dumps({"jsonrpc":"2.0","id":request.get("id"),"result":result}, separators=(",",":")), flush=True)
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func newKernelHostTestRuntime(t *testing.T, databasePath string, seed bool) (*workspace.Store, *kernelruntime.Manager, *Server, *agentKernelContext) {
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
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: python, AssetRoot: assetRoot,
		ManifestPath:     filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:       filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		ExecutionTimeout: 30 * time.Second, InterruptGrace: 2 * time.Second,
		ShutdownTimeout: 2 * time.Second,
	})
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if seed {
		createKernelAPIProjectAndFrame(t, store, "project-routine", "user-routine", "root-routine", "OPERON", "")
	}
	access, found, err := store.GetKernelFrameAccess("root-routine")
	if err != nil || !found {
		t.Fatalf("kernel frame access found=%t err=%v", found, err)
	}
	app := New(Options{FileRoot: filepath.Dir(databasePath), Workspace: store, KernelManager: manager})
	identity := &agentKernelContext{access: access, workspaceDir: t.TempDir()}
	return store, manager, app, identity
}

func TestPythonKernelHostViewImageMatchesReferenceExecutionPath(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	if err := exec.Command(python, "-I", "-c", "import PIL").Run(); err != nil {
		t.Skip("the selected managed Python runtime does not include Pillow")
	}
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	result := runKernelHostCellNamed(t, app, identity, "python", `
from PIL import Image
image = Image.new("RGB", (120, 80), "white")
view = host.view_image(image, crop=(0.25, 0.25, 0.75, 0.75), max_size=32, out="view.png")
print(view)
`)
	if result["ok"] != true || !strings.Contains(stringValue(result["stdout"]), "view.png") {
		t.Fatalf("python host.view_image result = %#v", result)
	}
	raw, err := os.ReadFile(filepath.Join(identity.workspaceDir, "view.png"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 8 || !bytes.Equal(raw[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		t.Fatalf("view.png is not a PNG: %x", raw[:min(len(raw), 8)])
	}
}

func runKernelHostCell(t *testing.T, app *Server, identity *agentKernelContext, code string) map[string]any {
	return runKernelHostCellNamed(t, app, identity, "Operon", code)
}

func runKernelHostCellNamed(t *testing.T, app *Server, identity *agentKernelContext, toolName, code string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	input := map[string]any{"code": code}
	if strings.EqualFold(strings.TrimSpace(toolName), "python") {
		input["environment"] = "python"
	}
	result, err := app.executeAgentKernelTool(ctx, identity, toolName, input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func closeKernelHostTestRuntime(t *testing.T, app *Server, manager *kernelruntime.Manager, store *workspace.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if app != nil {
		if err := app.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.CloseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func startKernelSettlementTestDispatcher(t *testing.T, store *workspace.Store) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		for {
			events, err := store.ClaimOutbox(ctx, workspace.ClaimOutboxInput{
				WorkerID: "server-kernel-settlement-test", Topics: []string{workspace.KernelResultSettlementOutboxTopic},
				Limit: 4, Lease: time.Second,
			})
			if err != nil {
				if ctx.Err() != nil {
					done <- nil
				} else {
					done <- err
				}
				return
			}
			if len(events) == 0 {
				select {
				case <-ctx.Done():
					done <- nil
					return
				case <-time.After(5 * time.Millisecond):
				}
				continue
			}
			for _, event := range events {
				if err := store.DeliverKernelBackgroundSettlement(ctx, event); err != nil {
					envelope, decodeErr := workspace.DecodeKernelBackgroundSettlement(event)
					record, found, logErr := store.GetExecutionLog(envelope.FrameID, envelope.ExecutionID)
					record.AgentName, record.DelegateName = "", ""
					raw, marshalErr := json.Marshal(record)
					digest := sha256.Sum256(raw)
					done <- fmt.Errorf("%w (decode=%v log_found=%t log=%v marshal=%v expected_hash=%s actual_hash=%s)",
						err, decodeErr, found, logErr, marshalErr, envelope.ExecutionLogSHA256,
						hex.EncodeToString(digest[:]))
					return
				}
				if err := store.AckOutbox(ctx, event.ID, event.ClaimToken); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("kernel settlement test dispatcher: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("kernel settlement test dispatcher did not stop")
		}
	}
}
