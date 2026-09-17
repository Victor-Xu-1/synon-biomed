package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/compute"
	kernelruntime "synon-go/internal/kernel"
	secretstore "synon-go/internal/persistence/secrets"
	workspace "synon-go/internal/persistence/workspace"
)

type recordingProviderOperationRunner struct {
	mu              sync.Mutex
	operations      []string
	archiveFiles    map[string]string
	archiveSHA256   string
	findSandboxIDs  []string
	terminateCalled bool
}

func (r *recordingProviderOperationRunner) RunProviderOperation(_ context.Context, input kernelruntime.ProviderOperationInput) (map[string]any, error) {
	r.mu.Lock()
	r.operations = append(r.operations, input.Operation)
	r.mu.Unlock()
	switch input.Operation {
	case "create":
		return map[string]any{"ok": true, "sandbox_id": "sandbox-recording"}, nil
	case "find_owned_submission":
		r.mu.Lock()
		ids := append([]string(nil), r.findSandboxIDs...)
		r.mu.Unlock()
		return map[string]any{"ok": true, "sandbox_ids": ids}, nil
	case "submit":
		stage, err := os.MkdirTemp("/tmp", "synon-provider-recording-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stage)
		if input.Prepare != nil {
			if err := input.Prepare(stage); err != nil {
				return nil, err
			}
		}
		r.archiveSHA256 = stringValue(input.Request["archive_sha256"])
		archive, err := os.Open(filepath.Join(stage, "in.tar.gz"))
		if err != nil {
			return nil, err
		}
		defer archive.Close()
		compressed, err := gzip.NewReader(archive)
		if err != nil {
			return nil, err
		}
		defer compressed.Close()
		reader := tar.NewReader(compressed)
		files := map[string]string{}
		for {
			header, nextErr := reader.Next()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				return nil, nextErr
			}
			body, readErr := io.ReadAll(reader)
			if readErr != nil {
				return nil, readErr
			}
			files[header.Name] = string(body)
		}
		r.mu.Lock()
		r.archiveFiles = files
		r.mu.Unlock()
		return map[string]any{"ok": true, "wrapper_deadline_aware": true}, nil
	case "terminate":
		r.mu.Lock()
		r.terminateCalled = true
		r.mu.Unlock()
		return map[string]any{"ok": true}, nil
	case "wait":
		if boolValue(input.Request["probe_only"], false) {
			return map[string]any{
				"ok": true, "ready": true, "job_exit_code": 0, "job_wall_s": 2,
				"stdout_tail": "remote stdout", "stderr_tail": "", "deadline_fired": false,
			}, nil
		}
		stage, err := os.MkdirTemp("/tmp", "synon-provider-harvest-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stage)
		archive, err := os.OpenFile(filepath.Join(stage, "out.tar.gz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		compressed := gzip.NewWriter(archive)
		writer := tar.NewWriter(compressed)
		for name, body := range map[string]string{"out/result.txt": "harvested-result", "stdout.log": "remote stdout", "stderr.log": ""} {
			if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				return nil, err
			}
			if _, err := io.WriteString(writer, body); err != nil {
				return nil, err
			}
		}
		if err := errors.Join(writer.Close(), compressed.Close(), archive.Close()); err != nil {
			return nil, err
		}
		response := map[string]any{"ok": true, "ready": true, "job_exit_code": 0, "bytes_written": 1}
		if input.Collect != nil {
			if err := input.Collect(stage, response); err != nil {
				return nil, err
			}
		}
		return response, nil
	case "list_volumes":
		return map[string]any{"ok": true, "volumes": []any{map[string]any{"name": "weights", "created_at": 1234}}}, nil
	case "list_dir":
		return map[string]any{"ok": true, "entries": []any{
			map[string]any{"name": "model.bin", "type": "file", "size": 12, "mtime": 1235},
			map[string]any{"name": "subdir", "type": "dir", "size": 0, "mtime": 1236},
		}}, nil
	case "read_file":
		stage, err := os.MkdirTemp("/tmp", "synon-provider-file-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stage)
		body := []byte("remote-volume-file")
		if err := os.WriteFile(filepath.Join(stage, "out.bin"), body, 0o600); err != nil {
			return nil, err
		}
		response := map[string]any{"ok": true, "size": len(body)}
		if input.Collect != nil {
			if err := input.Collect(stage, response); err != nil {
				return nil, err
			}
		}
		return response, nil
	case "reconcile":
		return map[string]any{"ok": true, "sandboxes": []any{
			map[string]any{"sandbox_id": "sandbox-old-a"}, map[string]any{"sandbox_id": "sandbox-old-b"},
		}}, nil
	default:
		return nil, fmt.Errorf("unexpected provider operation %q", input.Operation)
	}
}

func TestComputeProviderApprovalRunsOneConfinedPersistentKernelAndRecordsExecution(t *testing.T) {
	repositoryRoot := repositoryRootForServerTest(t)
	optional := filepath.Join(repositoryRoot, "assets", "optional")
	state := t.TempDir()
	environments := filepath.Join(state, "conda", "envs")
	prefix := filepath.Join(environments, "compute-provider-modal")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	python, err = filepath.EvalSymlinks(python)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(python, filepath.Join(prefix, "bin", "python")); err != nil {
		t.Fatal(err)
	}

	skillRoot := filepath.Join(t.TempDir(), "remote-compute-modal")
	if err := os.MkdirAll(filepath.Join(skillRoot, "envs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte(`---
name: remote-compute-modal
description: Test provider runtime.
tools:
  - compute_provider
---
Use the admitted provider kernel only.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "provider.json"), []byte(`{
  "id":"modal",
  "helperEnv":{"name":"compute-provider-modal","packages":["python=3.11","pip"],"pip":[]},
  "egress":{"control":{"host":"api.provider.test","port":443},"worker":[],"blob":[]}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	providerSource := `
import builtins
import contextlib
import hashlib
import re
import sys
import types

modal = types.ModuleType("modal")
class App:
    @staticmethod
    def lookup(name, create_if_missing=False):
        return {"name": name, "create": create_if_missing}
modal.App = App
modal.enable_output = contextlib.nullcontext
sys.modules["modal"] = modal

class TestProvider:
    secret_env_prefixes = ("MODAL_",)
    token_scrub_regex = re.compile(r"(?:ak|as)-[A-Za-z0-9-]+")

    def __init__(self, repl=False):
        self.repl = repl

    def apply_auth(self, credentials):
        self.credentials = credentials

    def import_and_patch(self):
        builtins.PROVIDER_AUTH_OK = (
            self.credentials["token_id"] == "ak-server-id"
            and self.credentials["token_secret"] == "as-server-secret"
        )

    def install_unauth_hook(self, callback):
        self.unauth_callback = callback

PROVIDER = TestProvider
`
	if err := os.WriteFile(filepath.Join(skillRoot, "provider.py"), []byte(providerSource), 0o600); err != nil {
		t.Fatal(err)
	}
	modalConfig := filepath.Join(state, "modal.toml")
	if err := os.WriteFile(modalConfig, []byte("[default]\nactive = true\ntoken_id = \"ak-server-id\"\ntoken_secret = \"as-server-secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: python, CondaHome: filepath.Join(state, "conda"), CondaEnvsPath: environments,
		AssetRoot: optional, ManifestPath: filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath:       filepath.Join(optional, "kernels", "kernel_worker.py"),
		ExecutionTimeout: 15 * time.Second, ShutdownTimeout: 2 * time.Second, InterruptGrace: time.Second,
	})
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-provider", "project-provider", "frame-provider")
	workspaceDir := t.TempDir()
	if _, err := store.UpdateProject("project-provider", workspace.UpdateProjectInput{Path: &workspaceDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetBYOCEnabled("modal", "owner-provider", true); err != nil {
		t.Fatal(err)
	}
	operationRunner := &recordingProviderOperationRunner{}
	server := New(Options{
		Workspace: store, Transcript: repo, FileRoot: state, RuntimeAssetsDir: optional,
		SkillDirectories: []string{filepath.Dir(skillRoot)}, KernelManager: manager, ModalConfigPath: modalConfig,
		ProviderOperationRunner: operationRunner,
		ComputeProviderDial: func(_ context.Context, host string, port int, _ bool) (net.Conn, error) {
			if host != "api.provider.test" || port != 443 {
				return nil, fmt.Errorf("unexpected provider target %s:%d", host, port)
			}
			client, remote := net.Pipe()
			go func() {
				defer remote.Close()
				_, _ = io.Copy(remote, remote)
			}()
			return client, nil
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, err := server.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "ask"}); err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), "frame-provider")
	if err != nil || !found {
		t.Fatalf("provider frame found=%t err=%v", found, err)
	}
	identity := &agentKernelContext{access: access, workspaceDir: workspaceDir}
	if !agentRuntimeToolSchemaNamed(server.agentKernelToolSchemas(identity, nil), computeProviderToolName) {
		t.Fatal("ready compute_provider schema was not exposed")
	}
	skillAuthority := map[string]struct{}{computeProviderToolName: {}}
	server.addComputeProviderSkillAuthorities(identity, skillAuthority)
	discovered := server.agentRuntimeDiscoverableSkillSet(server.skillCatalog.Skills(), skillAuthority)
	if _, found := discovered["remote-compute-modal"]; !found {
		t.Fatalf("provider Skill was not discoverable after its runtime became ready: %#v", discovered)
	}

	code := `
import socket
print("provider-auth", PROVIDER_AUTH_OK)
print("provider-config", compute_provider_config()["provider"])
print("provider-envs", list_envs()["_envs_dir"].endswith("/envs"))
state_counter = globals().get("state_counter", 0) + 1
print("state-counter", state_counter)
s = socket.create_connection(("127.0.0.1", 1080), timeout=2)
host = b"api.provider.test"
s.sendall(b"\x05\x01\x00")
assert s.recv(2) == b"\x05\x00"
s.sendall(b"\x05\x01\x00\x03" + bytes([len(host)]) + host + b"\x01\xbb")
assert len(s.recv(10)) == 10
s.sendall(b"server-provider-ok")
print(s.recv(18).decode("ascii"))
s.close()
`
	call := agentruntime.ToolCall{ID: "provider-call-1", Name: computeProviderToolName}
	input := map[string]any{"provider": "modal", "code": code}
	pending := server.agentRuntimePermissionResultForSessionWithContext(context.Background(), "frame-provider", computeProviderToolName, call, input)
	if pending == nil || pending["decision"] != "pending_approval" {
		t.Fatalf("provider permission=%#v", pending)
	}
	approvalID := stringValue(pending["approvalId"])
	resolved, err := server.resolveAgentRuntimeApprovalMessage(context.Background(), "agent-runtime", map[string]any{
		"type": "agent_runtime_approval_response", "approvalId": approvalID, "approve": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(resolved)
	visible := mapValue(result["result"])
	stdout := stringValue(visible["stdout"])
	if result["status"] != "completed" || visible["ok"] != true ||
		!strings.Contains(stdout, "provider-auth True") || !strings.Contains(stdout, "state-counter 1") ||
		!strings.Contains(stdout, "provider-config modal") || !strings.Contains(stdout, "provider-envs True") ||
		!strings.Contains(stdout, "server-provider-ok") || strings.Contains(stdout, "as-server-secret") {
		t.Fatalf("provider approval result=%#v", result)
	}

	secondInput := map[string]any{"provider": "modal", "code": `state_counter += 1; print("state-counter", state_counter)`}
	if permission := server.agentRuntimePermissionResultForSessionWithContext(
		context.Background(), "frame-provider", computeProviderToolName,
		agentruntime.ToolCall{ID: "provider-call-2", Name: computeProviderToolName}, secondInput,
	); permission != nil {
		t.Fatalf("active provider kernel requested a duplicate approval: %#v", permission)
	}
	second, err := server.executeAgentComputeProvider(
		context.Background(), identity, access,
		agentruntime.ToolCall{ID: "provider-call-2", Name: computeProviderToolName}, secondInput,
	)
	if err != nil || !strings.Contains(stringValue(mapValue(second)["stdout"]), "state-counter 2") {
		t.Fatalf("provider reuse=%#v err=%v", second, err)
	}
	records, err := store.ListExecutionLog("frame-provider", "")
	if err != nil || len(records) != 2 || records[0].KernelKind != "provider" || records[1].KernelKind != "provider" {
		t.Fatalf("provider execution records=%#v err=%v", records, err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "input.txt"), []byte("input-evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	providerParams := map[string]any{
		"image": "im-fixture", "env": "fixture_env", "gpu": "A10G", "timeout": 600,
	}
	listedCompute, err := server.listAgentComputeProviders(access)
	listedProviders := anySliceValue(mapValue(listedCompute)["providers"])
	if err != nil || len(listedProviders) != 1 || stringValue(mapValue(listedProviders[0])["name"]) != "modal" {
		t.Fatalf("canonical compute targets=%#v err=%v", listedCompute, err)
	}
	createdHandle, err := server.handleKernelComputeHostCall(
		context.Background(), access, workspaceDir, "handle-create-call", "host.compute.create",
		[]any{"modal", providerParams}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	handleID := stringValue(mapValue(createdHandle)["handle_id"])
	if handleID == "" || stringValue(mapValue(createdHandle)["target"]) != "modal" || len(mapValue(mapValue(createdHandle)["provider_params"])) != len(providerParams) {
		t.Fatalf("canonical compute handle=%#v", createdHandle)
	}
	jobInput := map[string]any{
		"provider": "modal", "intent": "Run recorded BYOC job", "command": "cp input.txt out/result.txt",
		"provider_params": providerParams, "handle_id": handleID,
		"inputs":  []any{"input.txt"},
		"outputs": []any{map[string]any{"glob": "out/**/result.txt", "visibility": "featured"}}, "timeout_seconds": 300,
	}
	if generic := server.agentRuntimePermissionResultForSessionWithContext(
		context.Background(), "frame-provider", submitComputeJobToolName,
		agentruntime.ToolCall{ID: "byoc-submit-generic", Name: submitComputeJobToolName}, jobInput,
	); generic != nil {
		t.Fatalf("BYOC submit queued a duplicate generic approval before the tier card: %#v", generic)
	}
	type jobResult struct {
		value any
		err   error
	}
	jobDone := make(chan jobResult, 1)
	go func() {
		value, submitErr := server.submitAgentComputeJob(
			context.Background(), access,
			agentruntime.ToolCall{ID: "byoc-submit-call", Name: submitComputeJobToolName}, jobInput, workspaceDir,
		)
		jobDone <- jobResult{value: value, err: submitErr}
	}()
	approvalID = ""
	deadline := time.Now().Add(3 * time.Second)
	for approvalID == "" && time.Now().Before(deadline) {
		entries, listErr := server.runtimeStore.List(agentRuntimeApprovalNamespace)
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, entry := range entries {
			value := mapValue(entry.Value)
			if stringValue(value["approvalSource"]) == kernelCapabilityInstallApprovalSource &&
				stringValue(value["tool"]) == "host.compute.submit_job" && stringValue(value["status"]) == "pending" {
				approvalID = entry.Key
				break
			}
		}
		if approvalID == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if approvalID == "" {
		t.Fatal("BYOC tier approval was not queued")
	}
	if _, err := server.resolveAgentRuntimeApprovalMessage(context.Background(), "", map[string]any{
		"approvalId": approvalID, "approve": true,
	}); err != nil {
		t.Fatal(err)
	}
	var submitted jobResult
	select {
	case submitted = <-jobDone:
	case <-time.After(5 * time.Second):
		t.Fatal("BYOC submit did not resume after approval")
	}
	if submitted.err != nil || stringValue(mapValue(submitted.value)["status"]) != "running" {
		t.Fatalf("BYOC submit=%#v err=%v", submitted.value, submitted.err)
	}
	operationRunner.mu.Lock()
	operations := append([]string(nil), operationRunner.operations...)
	archiveFiles := map[string]string{}
	for key, value := range operationRunner.archiveFiles {
		archiveFiles[key] = value
	}
	archiveSHA := operationRunner.archiveSHA256
	operationRunner.mu.Unlock()
	if strings.Join(operations, ",") != "create,submit" || archiveSHA == "" ||
		!strings.Contains(archiveFiles["run.sh"], "cp input.txt out/result.txt") ||
		archiveFiles["input.txt"] != "input-evidence" || archiveFiles["_operon_wrapper.sh"] == "" {
		t.Fatalf("BYOC operations=%v archiveSHA=%q files=%#v", operations, archiveSHA, archiveFiles)
	}
	server.reconcileActiveComputeProviderJobs(context.Background())
	jobID := stringValue(mapValue(submitted.value)["job_id"])
	var completed workspace.ComputeJob
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, found, getErr := store.GetComputeJob("owner-provider", jobID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if found && isTerminalAgentComputeJobState(job.State) {
			completed = job
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if completed.State != workspace.ComputeJobDone {
		t.Fatalf("BYOC job did not complete: %#v", completed)
	}
	detailed := server.kernelComputeJobDetailedProjection("owner-provider", completed)
	if numberValue(detailed["exit_code"]) != 0 || len(anySliceValue(detailed["featured_files"])) != 1 ||
		!strings.Contains(stringValue(detailed["stdout_tail"]), "remote stdout") {
		t.Fatalf("BYOC detailed result=%#v", detailed)
	}
	harvested, err := os.ReadFile(filepath.Join(workspaceDir, "hpc", jobID, "out", "result.txt"))
	if err != nil || string(harvested) != "harvested-result" {
		t.Fatalf("BYOC harvest=%q err=%v", harvested, err)
	}
	unread, err := store.CountUnreadNotifications(context.Background(), "frame-provider", "frame-provider", "owner-provider")
	if err != nil || unread != 1 {
		t.Fatalf("BYOC notification count=%d err=%v", unread, err)
	}
	operationRunner.mu.Lock()
	operations = append([]string(nil), operationRunner.operations...)
	terminated := operationRunner.terminateCalled
	operationRunner.mu.Unlock()
	if strings.Join(operations, ",") != "create,submit,wait,wait" || terminated {
		t.Fatalf("BYOC terminal operations=%v terminated=%t", operations, terminated)
	}
	secondJobInput := copyMapAny(jobInput)
	secondJobInput["intent"] = "Reuse warm BYOC sandbox"
	secondJobInput["command"] = "printf warm > out/warm.txt"
	secondSubmitted, err := server.submitAgentComputeJob(
		context.Background(), access,
		agentruntime.ToolCall{ID: "byoc-submit-call-2", Name: submitComputeJobToolName}, secondJobInput, workspaceDir,
	)
	if err != nil || stringValue(mapValue(secondSubmitted)["status"]) != "running" {
		t.Fatalf("warm BYOC submit=%#v err=%v", secondSubmitted, err)
	}
	server.reconcileActiveComputeProviderJobs(context.Background())
	secondJobID := stringValue(mapValue(secondSubmitted)["job_id"])
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, found, getErr := store.GetComputeJob("owner-provider", secondJobID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if found && isTerminalAgentComputeJobState(job.State) {
			completed = job
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if completed.JobID != secondJobID || completed.State != workspace.ComputeJobDone {
		t.Fatalf("warm BYOC job did not complete: %#v", completed)
	}
	closed, err := server.closeAgentComputeHandle(context.Background(), access, "modal", handleID)
	if err != nil || closed["closed"] != true {
		t.Fatalf("close compute handle=%#v err=%v", closed, err)
	}
	operationRunner.mu.Lock()
	operations = append([]string(nil), operationRunner.operations...)
	terminated = operationRunner.terminateCalled
	operationRunner.mu.Unlock()
	if strings.Join(operations, ",") != "create,submit,wait,wait,submit,wait,wait,terminate" || !terminated {
		t.Fatalf("warm BYOC operations=%v terminated=%t", operations, terminated)
	}
	landing, err := server.listBYOCRemoteDirectory(context.Background(), "owner-provider", "modal", "/")
	if err != nil || len(landing.Entries) != 1 || landing.Entries[0].Name != "weights" || !landing.Entries[0].IsDirectory {
		t.Fatalf("BYOC volume landing=%#v err=%v", landing, err)
	}
	directory, err := server.listBYOCRemoteDirectory(context.Background(), "owner-provider", "modal", "/weights/")
	if err != nil || len(directory.Entries) != 2 || directory.ResolvedPath != "/weights" {
		t.Fatalf("BYOC volume directory=%#v err=%v", directory, err)
	}
	download, err := server.fetchBYOCRemoteFile(context.Background(), "owner-provider", "modal", "/weights/model.bin", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	remoteBody, err := os.ReadFile(download.Path)
	_ = download.Cleanup()
	if err != nil || string(remoteBody) != "remote-volume-file" || download.Filename != "model.bin" {
		t.Fatalf("BYOC remote file=%q download=%#v err=%v", remoteBody, download, err)
	}
	cleanupAuthority, err := server.agentComputeProviderAuthority(access, "modal", true)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := server.cleanupAgentBYOCProvider(context.Background(), cleanupAuthority.Definition)
	if err != nil || cleanup.Owned != 2 || len(cleanup.Terminated) != 2 || len(cleanup.Failed) != 0 {
		t.Fatalf("secure BYOC cleanup=%#v err=%v", cleanup, err)
	}
	probeFrame, probeRoot, probeOrigin := "frame-provider", "frame-provider", "probe-origin"
	probeJob, err := store.CreateComputeJob("owner-provider", workspace.ComputeJob{
		JobID: "job-probe-budget", ProjectID: "project-provider", Provider: "byoc:modal",
		Environment: "fixture_env", TierType: "remote", FrameID: &probeFrame, RootFrameID: &probeRoot,
		OriginToolUseID: &probeOrigin, ProviderFamily: "byoc", ProviderLabel: "byoc:modal",
	})
	if err != nil {
		t.Fatal(err)
	}
	probeJob, err = store.BindComputeJobExternal("owner-provider", probeJob.JobID, "sandbox-probe-budget", "")
	if err != nil {
		t.Fatal(err)
	}
	ownedProbe := workspace.OwnedComputeJob{OwnerUserID: "owner-provider", Job: probeJob}
	probeErr := &kernelruntime.ProviderOperationError{Kind: "transient", Message: "temporary provider error"}
	for attempt := 1; attempt <= computeProviderJobFailureLimit; attempt++ {
		server.handleComputeProviderProbeFailure(ownedProbe, probeErr)
		current, found, getErr := store.GetComputeJob("owner-provider", probeJob.JobID)
		if getErr != nil || !found {
			t.Fatalf("probe job attempt=%d found=%t err=%v", attempt, found, getErr)
		}
		if attempt < computeProviderJobFailureLimit && current.State != workspace.ComputeJobRunning {
			t.Fatalf("probe job stopped before budget at attempt=%d state=%s", attempt, current.State)
		}
		if attempt == computeProviderJobFailureLimit && current.State != workspace.ComputeJobOrphaned {
			t.Fatalf("probe job did not stop at bounded budget: state=%s", current.State)
		}
	}
	createRecoveryJob := func(jobID, submissionID string) (workspace.OwnedComputeJob, map[string]any, string) {
		t.Helper()
		stage, archive, stageErr := server.stageAgentBYOCJob(jobID, workspaceDir, map[string]any{"command": "true"})
		if stageErr != nil {
			t.Fatal(stageErr)
		}
		providerSpec := agentBYOCProviderSandboxSpec(byocModalJobSpec{Image: "im-fixture", Timeout: 10 * time.Minute})
		providerSpecSHA256, digestErr := agentBYOCProviderSpecSHA256(providerSpec)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		hardware := map[string]any{
			"provider_params": map[string]any{"image": "im-fixture", "timeout": 600},
			"outputs":         []any{}, "workspace_dir": workspaceDir,
			"install_id": cleanupAuthority.Definition.InstallID, "submission_id": submissionID,
			"sandbox_deadline_epoch": time.Now().Add(20 * time.Minute).Unix(), "job_timeout_seconds": 300,
			"harvest_margin_seconds": int(byocHarvestMargin / time.Second), "termination_grace_seconds": int(byocTerminationGrace / time.Second),
			"handle_id": "", "sandbox_hint": "",
			"staging_dir": stage, "archive_sha256": archive.SHA256, "archive_bytes": archive.Bytes,
			"provider_spec": providerSpec, "provider_spec_sha256": providerSpecSHA256,
			"provider_config_sha256": cleanupAuthority.Definition.ExtraEnvironment["SYNON_PROVIDER_BOUND_CONFIG_HASH"],
		}
		origin := "recovery-origin-" + jobID
		job, createErr := store.CreateComputeJob("owner-provider", workspace.ComputeJob{
			JobID: jobID, ProjectID: "project-provider", Provider: "byoc:modal",
			Environment: "fixture_env", TierType: "remote", FrameID: &probeFrame, RootFrameID: &probeRoot,
			OriginToolUseID: &origin, ProviderFamily: "byoc", ProviderLabel: "byoc:modal", HardwareDetails: hardware,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return workspace.OwnedComputeJob{OwnerUserID: "owner-provider", Job: job}, hardware, stage
	}

	recoverable, recoverHardware, recoverStage := createRecoveryJob("job-recovery-adopt", "submission-111111111111111111111111")
	operationRunner.mu.Lock()
	operationRunner.findSandboxIDs = []string{"sandbox-recovered"}
	beforeRecovery := len(operationRunner.operations)
	operationRunner.mu.Unlock()
	recovered, ok := server.recoverAgentBYOCSubmission(context.Background(), recoverable, access, cleanupAuthority, recoverHardware)
	if !ok || recovered.State != workspace.ComputeJobRunning || recovered.ExternalID == nil || *recovered.ExternalID != "sandbox-recovered" {
		t.Fatalf("recovered BYOC job=%#v ok=%t", recovered, ok)
	}
	operationRunner.mu.Lock()
	recoveryOperations := append([]string(nil), operationRunner.operations[beforeRecovery:]...)
	operationRunner.mu.Unlock()
	if strings.Join(recoveryOperations, ",") != "find_owned_submission,submit" {
		t.Fatalf("recovery operations=%v", recoveryOperations)
	}
	if _, statErr := os.Stat(recoverStage); !os.IsNotExist(statErr) {
		t.Fatalf("recovery staging directory was not removed after commit: %v", statErr)
	}

	ambiguous, ambiguousHardware, ambiguousStage := createRecoveryJob("job-recovery-ambiguous", "submission-222222222222222222222222")
	operationRunner.mu.Lock()
	operationRunner.findSandboxIDs = []string{"sandbox-duplicate-a", "sandbox-duplicate-b"}
	operationRunner.mu.Unlock()
	if _, ok := server.recoverAgentBYOCSubmission(context.Background(), ambiguous, access, cleanupAuthority, ambiguousHardware); ok {
		t.Fatal("ambiguous BYOC recovery unexpectedly continued")
	}
	ambiguousJob, found, err := store.GetComputeJob("owner-provider", ambiguous.Job.JobID)
	if err != nil || !found || ambiguousJob.State != workspace.ComputeJobOrphaned || ambiguousJob.ErrorKind == nil || *ambiguousJob.ErrorKind != "ambiguous_remote_identity" {
		t.Fatalf("ambiguous recovery job=%#v found=%t err=%v", ambiguousJob, found, err)
	}
	if _, statErr := os.Stat(ambiguousStage); !os.IsNotExist(statErr) {
		t.Fatalf("ambiguous recovery staging directory was not removed: %v", statErr)
	}
}

func repositoryRootForServerTest(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(cwd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	return root
}

func TestManagedInferenceComputeProviderUsesExactLoopbackProxyAndBaseURL(t *testing.T) {
	repositoryRoot := repositoryRootForServerTest(t)
	optional := filepath.Join(repositoryRoot, "assets", "optional")
	state := t.TempDir()
	environments := filepath.Join(state, "conda", "envs")
	prefix := filepath.Join(environments, "compute-provider-http")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	python, err = filepath.EvalSymlinks(python)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(python, filepath.Join(prefix, "bin", "python")); err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/health" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if request.URL.Path != "/v1/infer" {
			http.NotFound(w, request)
			return
		}
		_, _ = w.Write([]byte("inference-ok"))
	}))
	defer endpoint.Close()

	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: python, CondaHome: filepath.Join(state, "conda"), CondaEnvsPath: environments,
		AssetRoot: optional, ManifestPath: filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath:       filepath.Join(optional, "kernels", "kernel_worker.py"),
		ExecutionTimeout: 15 * time.Second, ShutdownTimeout: 2 * time.Second,
	})
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-inference", "project-inference", "frame-inference")
	workspaceDir := t.TempDir()
	if _, err := store.UpdateProject("project-inference", workspace.UpdateProjectInput{Path: &workspaceDir}); err != nil {
		t.Fatal(err)
	}
	registration := compute.ManagedEndpointRegistration{
		Name: "fixture-endpoint", URL: endpoint.URL + "/v1", Port: endpoint.Listener.Addr().(*net.TCPAddr).Port,
		SkillName: "using-model-endpoint", LivePath: "/health", StartScript: "true", StopScript: "true",
	}
	if err := store.UpsertManagedEndpoint(workspace.ManagedEndpoint{
		Name: registration.Name, URL: registration.URL, Port: registration.Port,
		State: "stopped", Location: "local", SkillName: registration.SkillName,
		LivePath: registration.LivePath, StartScript: registration.StartScript, StopScript: registration.StopScript,
		ApprovedScriptHash: compute.ApprovedManagedEndpointHash(registration), RegisteredBy: "owner-inference",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{
		Workspace: store, Transcript: repo, FileRoot: state, RuntimeAssetsDir: optional,
		SkillDirectories: []string{filepath.Join(repositoryRoot, "skills", "synonbiomed")}, KernelManager: manager,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), "frame-inference")
	if err != nil || !found {
		t.Fatalf("inference frame found=%t err=%v", found, err)
	}
	identity := &agentKernelContext{access: access, workspaceDir: workspaceDir}
	if !agentRuntimeToolSchemaNamed(server.agentKernelToolSchemas(identity, nil), computeProviderToolName) {
		t.Fatal("managed inference did not expose compute_provider")
	}
	skillAuthority := map[string]struct{}{computeProviderToolName: {}}
	server.addComputeProviderSkillAuthorities(identity, skillAuthority)
	if _, found := skillAuthority["compute_provider_inference"]; !found {
		t.Fatal("managed inference capability authority is missing")
	}
	if _, found := skillAuthority["compute_provider_modal"]; found {
		t.Fatal("disabled Modal capability leaked through an inference endpoint")
	}
	discoverable := server.agentRuntimeDiscoverableSkillSet(server.skillCatalog.Skills(), skillAuthority)
	if _, found := discoverable["using-model-endpoint"]; !found {
		t.Fatal("using-model-endpoint was hidden despite a ready inference endpoint")
	}
	if _, found := discoverable["remote-compute-modal"]; found {
		t.Fatal("remote-compute-modal was discoverable without a ready Modal provider")
	}
	listed, err := server.listAgentComputeProviders(access)
	providers := anySliceValue(mapValue(listed)["providers"])
	if err != nil || len(providers) != 1 || stringValue(mapValue(providers[0])["name"]) != "fixture-endpoint" {
		t.Fatalf("managed inference list_compute=%#v err=%v", listed, err)
	}
	result, err := server.executeAgentComputeProvider(
		context.Background(), identity, access,
		agentruntime.ToolCall{ID: "inference-provider-call", Name: computeProviderToolName},
		map[string]any{"provider": "fixture-endpoint", "code": `import urllib.request; print(BASE_URL); print(urllib.request.urlopen(BASE_URL + "/infer").read().decode())`},
	)
	stdout := stringValue(mapValue(result)["stdout"])
	if err != nil || !strings.Contains(stdout, endpoint.URL+"/v1") || !strings.Contains(stdout, "inference-ok") {
		t.Fatalf("managed inference result=%#v err=%v", result, err)
	}
	endpoints, err := store.ListManagedEndpoints("owner-inference", "fixture-endpoint", false)
	if err != nil || len(endpoints) != 1 || endpoints[0].State != "live" {
		t.Fatalf("managed endpoint state=%#v err=%v", endpoints, err)
	}
}

func TestHostedInferenceProviderInjectsCredentialByFDAndTunnelsExactTLSHost(t *testing.T) {
	repositoryRoot := repositoryRootForServerTest(t)
	optional := filepath.Join(repositoryRoot, "assets", "optional")
	state := t.TempDir()
	environments := filepath.Join(state, "conda", "envs")
	prefix := filepath.Join(environments, "compute-provider-http")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	python, err = filepath.EvalSymlinks(python)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(python, filepath.Join(prefix, "bin", "python")); err != nil {
		t.Fatal(err)
	}
	const secretValue = "hosted-inference-secret"
	hosted := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/infer" || request.Header.Get("Authorization") != "Bearer "+secretValue {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("hosted-inference-ok"))
	}))
	defer hosted.Close()

	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: python, CondaHome: filepath.Join(state, "conda"), CondaEnvsPath: environments,
		AssetRoot: optional, ManifestPath: filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath:       filepath.Join(optional, "kernels", "kernel_worker.py"),
		ExecutionTimeout: 15 * time.Second, ShutdownTimeout: 2 * time.Second,
	})
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-hosted", "project-hosted", "frame-hosted")
	workspaceDir := t.TempDir()
	if _, err := store.UpdateProject("project-hosted", workspace.UpdateProjectInput{Path: &workspaceDir}); err != nil {
		t.Fatal(err)
	}
	credentialName := "NVIDIA_API_KEY"
	if err := store.UpsertManagedEndpoint(workspace.ManagedEndpoint{
		Name: "hosted-endpoint", URL: "https://api.inference.test/v1", State: "live", Location: "remote",
		SkillName: "using-model-endpoint", CredentialName: &credentialName,
		ApprovedScriptHash: "hosted", RegisteredBy: "owner-hosted",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{
		Workspace: store, Transcript: repo, FileRoot: state, RuntimeAssetsDir: optional,
		SkillDirectories: []string{filepath.Join(repositoryRoot, "skills", "synonbiomed")}, KernelManager: manager,
		ComputeProviderDial: func(_ context.Context, host string, port int, allowPrivate bool) (net.Conn, error) {
			if host != "api.inference.test" || port != 443 || allowPrivate {
				return nil, fmt.Errorf("unexpected hosted target %s:%d private=%t", host, port, allowPrivate)
			}
			return net.Dial("tcp", hosted.Listener.Addr().String())
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "hosted-api-key", UserID: "owner-hosted", Provider: credentialName,
		Name: credentialName, Value: secretValue,
	}); err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), "frame-hosted")
	if err != nil || !found {
		t.Fatalf("hosted frame found=%t err=%v", found, err)
	}
	identity := &agentKernelContext{access: access, workspaceDir: workspaceDir}
	code := `
import os, ssl, urllib.request
request = urllib.request.Request(BASE_URL + "/infer", headers={"Authorization": "Bearer " + os.environ["INFER_API_KEY"]})
print(urllib.request.urlopen(request, context=ssl._create_unverified_context()).read().decode())
print("credential-alias", os.environ["NVIDIA_API_KEY"] == os.environ["INFER_API_KEY"])
print("hosted-inference-secret")
`
	result, err := server.executeAgentComputeProvider(
		context.Background(), identity, access,
		agentruntime.ToolCall{ID: "hosted-provider-call", Name: computeProviderToolName},
		map[string]any{"provider": "hosted-endpoint", "code": code},
	)
	stdout := stringValue(mapValue(result)["stdout"])
	if err != nil || !strings.Contains(stdout, "hosted-inference-ok") || !strings.Contains(stdout, "credential-alias True") ||
		!strings.Contains(stdout, "***") || strings.Contains(stdout, secretValue) {
		t.Fatalf("hosted inference result=%#v err=%v", result, err)
	}
}
