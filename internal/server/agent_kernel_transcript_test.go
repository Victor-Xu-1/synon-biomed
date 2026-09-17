package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestTranscriptFrameRunnerExecutesPythonWithoutLegacySession(t *testing.T) {
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
		Python:                   python,
		ManagedPythonEnvironment: "python",
		AssetRoot:                assetRoot,
		ManifestPath:             filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:               filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		ExecutionTimeout:         5 * time.Second,
		InterruptGrace:           2 * time.Second,
		ShutdownTimeout:          2 * time.Second,
	})

	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-kernel", "frame-kernel")
	if _, err := db.Exec(`UPDATE frames SET agent_name='OPERON' WHERE id='frame-kernel'`); err != nil {
		t.Fatal(err)
	}
	projectPath := ""
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestNumber == 1 {
			pythonAvailable, readFileAvailable, editFileAvailable, saveArtifactsAvailable := false, false, false, false
			for _, tool := range request.Tools {
				switch tool.Function.Name {
				case "python":
					pythonAvailable = true
				case "read_file":
					readFileAvailable = true
				case "edit_file":
					editFileAvailable = true
				case "save_artifacts":
					saveArtifactsAvailable = true
				case "Agent", "Task", "TaskRun", "agent_run", "task_run":
					t.Fatalf("OPERON runner exposed excluded delegation tool %q", tool.Function.Name)
				case "Read", "ReadBatch", "file_read", "file_read_batch", "file_list", "file_info", "file_search",
					"Glob", "glob", "Grep", "grep", "Write", "file_write", "Edit", "Patch", "artifact_register", "artifact_list", "artifact_get":
					t.Fatalf("OPERON runner exposed global file or artifact authority %q", tool.Function.Name)
				}
			}
			if !pythonAvailable || !readFileAvailable || !editFileAvailable || !saveArtifactsAvailable {
				t.Fatalf("canonical Transcript frame runner tools omit python/read_file/edit_file/save_artifacts: %#v", request.Tools)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"python-call","type":"function","function":{"name":"python","arguments":"{\"code\":\"import os; print(os.getcwd()); open('kernel-output.txt', 'w').write('kernel transcript parity')\",\"environment\":\"python\",\"human_description\":\"Running kernel parity check\"}"}}]}}]}`))
			return
		}
		if requestNumber == 2 {
			foundResult := false
			for _, message := range request.Messages {
				if message.Role == "tool" && message.ToolCallID == "python-call" &&
					strings.Contains(message.Content, projectPath) {
					foundResult = true
					break
				}
			}
			if !foundResult {
				t.Fatalf("provider did not receive the real Python result: %#v", request.Messages)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"save-call","type":"function","function":{"name":"save_artifacts","arguments":"{\"files\":[\"kernel-output.txt\"],\"language\":\"python\",\"environment\":\"python\",\"human_description\":\"Saving kernel output\"}"}}]}}]}`))
			return
		}
		foundArtifact := false
		for _, message := range request.Messages {
			if message.Role == "tool" && message.ToolCallID == "save-call" &&
				strings.Contains(message.Content, `"artifact_id"`) && strings.Contains(message.Content, `"version_id"`) {
				foundArtifact = true
				break
			}
		}
		if !foundArtifact {
			t.Fatalf("provider did not receive the canonical save_artifacts result: %#v", request.Messages)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Python execution completed"}}]}`))
	}))
	defer provider.Close()

	server := newV11TestServer(t, Options{
		Workspace: store, Transcript: repo, KernelManager: manager, FileRoot: t.TempDir(),
	})
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), "frame-kernel")
	if err != nil || !found {
		t.Fatalf("resolve frame access found=%v err=%v", found, err)
	}
	projectPath, err = server.defaultAgentKernelTaskWorkspace(
		access.Frame.ProjectID, access.Frame.RootFrameID, access.RootFrameIncarnationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	now := time.Now().UTC()
	legacyWorkdir := t.TempDir()
	if err := server.sessionStore.Upsert(sessionstore.Session{
		ID: "frame-kernel", CreatedAt: now, UpdatedAt: now,
		WorkDir:       legacyWorkdir,
		Orchestration: map[string]any{"frame_id": "stale-legacy-frame"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.sessionStore.Upsert(sessionstore.Session{
		ID: "legacy-kernel-alias", CreatedAt: now, UpdatedAt: now,
		WorkDir:       legacyWorkdir,
		Orchestration: map[string]any{"frame_id": "frame-kernel"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-kernel", MessageUUID: "message-kernel", ClientMessageID: "client-kernel",
		Text: "Run Python through the canonical Transcript frame runner.",
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, server, "local", "frame-kernel")
	aliasContext := server.resolveAgentKernelContext(context.Background(), "legacy-kernel-alias")
	if aliasContext == nil || aliasContext.access.Frame.ID != "frame-kernel" ||
		aliasContext.access.UserID != "local" || aliasContext.workspaceDir != projectPath {
		t.Fatalf("legacy alias context=%#v", aliasContext)
	}
	schemas := server.agentKernelToolSchemas(aliasContext, map[string]struct{}{"python": {}, "r": {}, "repl": {}, "save_artifacts": {}})
	schemaNames := map[string]bool{}
	for _, schema := range schemas {
		schemaNames[schema.Name] = true
	}
	if !schemaNames["python"] || !schemaNames["repl"] || !schemaNames["save_artifacts"] || !schemaNames["wait_for_notification"] || schemaNames["r"] {
		t.Fatalf("unready R tool publication=%#v", schemaNames)
	}
	for _, schema := range schemas {
		if schema.Name == "python" {
			required, _ := schema.Parameters["required"].([]string)
			properties, _ := schema.Parameters["properties"].(map[string]any)
			if schema.Description != agentKernelPythonDescription ||
				!sameStringSet(required, []string{"code", "environment", "human_description"}) || properties["working_dir"] == nil ||
				properties["background"] == nil || !kernelSchemaPropertiesDescribed(properties) {
				t.Fatalf("python schema=%#v description=%q", schema.Parameters, schema.Description)
			}
		}
		if schema.Name == "repl" {
			properties, _ := schema.Parameters["properties"].(map[string]any)
			if schema.Description != agentKernelReplDescription || properties["environment"] != nil ||
				properties["working_dir"] == nil || properties["fresh"] == nil || properties["background"] == nil ||
				!kernelSchemaPropertiesDescribed(properties) {
				t.Fatalf("repl schema=%#v description=%q", schema.Parameters, schema.Description)
			}
		}
		if schema.Name == "wait_for_notification" {
			properties, _ := schema.Parameters["properties"].(map[string]any)
			timeout, _ := properties["timeout_seconds"].(map[string]any)
			required, _ := schema.Parameters["required"].([]string)
			if timeout["type"] != "number" || timeout["default"] != 30 ||
				timeout["maximum"] != 1800 || len(required) != 0 || schema.Parameters["additionalProperties"] != nil {
				t.Fatalf("wait_for_notification schema=%#v", schema.Parameters)
			}
		}
	}
	result, err := runTranscriptRunnerWithLocalExecApproval(t, server, "local", SessionRunnerChatOptions{
		SessionID: "frame-kernel", RunnerID: "runner-kernel", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, ReplayLimit: 100,
		OutputLimitBytes: 1 << 20, DisableSkillDiscovery: true,
	}, func() {
		if _, statErr := os.Stat(filepath.Join(projectPath, "kernel-output.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("unapproved transcript runner wrote project output: %v", statErr)
		}
	})
	if err != nil || !result.Claimed || result.Status != "completed" || requests.Load() != 3 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	assertKernelApprovalEventCounts(t, store, "frame-kernel", 1, 1, 1)
	waitForKernelExecutionCount(t, manager, 0)
	confirmations, err := server.webConversationPendingConfirmations("frame-kernel")
	if err != nil || len(confirmations) != 0 {
		t.Fatalf("terminal approval confirmations=%#v err=%v", confirmations, err)
	}
	output, err := os.ReadFile(filepath.Join(projectPath, "kernel-output.txt"))
	if err != nil || string(output) != "kernel transcript parity" {
		t.Fatalf("project output=%q err=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(legacyWorkdir, "kernel-output.txt")); !os.IsNotExist(err) {
		t.Fatalf("legacy workdir received canonical output: %v", err)
	}
	records, err := store.ListExecutionLog("frame-kernel", "")
	if err != nil || len(records) != 1 || records[0].Origin != "agent" ||
		records[0].ExitStatus != "ok" || !strings.Contains(records[0].Stdout, projectPath) ||
		!strings.Contains(records[0].Source, "kernel-output.txt") || records[0].FilesWritten == nil {
		t.Fatalf("execution records=%#v err=%v", records, err)
	}
	filesWritten, ok := records[0].FilesWritten.([]any)
	if !ok || len(filesWritten) != 1 {
		t.Fatalf("files written=%#v", records[0].FilesWritten)
	}
	fileWritten, ok := filesWritten[0].(map[string]any)
	if !ok || stringValue(fileWritten["path"]) != "kernel-output.txt" {
		t.Fatalf("files written=%#v", records[0].FilesWritten)
	}
	artifacts, err := store.ListArtifacts("project-kernel", 10, 0)
	if err != nil || len(artifacts) != 1 || artifacts[0].Name != "kernel-output.txt" || artifacts[0].CurrentVersionNumber != 1 {
		t.Fatalf("saved artifacts=%#v err=%v", artifacts, err)
	}
	var executionLinks int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM artifact_version_execution_links link
		JOIN artifact_versions version ON version.id=link.version_id
		WHERE version.artifact_id=?`, artifacts[0].ID).Scan(&executionLinks); err != nil || executionLinks != 1 {
		t.Fatalf("artifact execution links=%d err=%v", executionLinks, err)
	}
}

func TestAgentKernelProtectedPathsAreReadOnlyUntilExecutionEnsuresManagedDirectories(t *testing.T) {
	fileRoot := t.TempDir()
	runtimeAssets := t.TempDir()
	condaHome := filepath.Join(fileRoot, "conda")
	condaEnvs := filepath.Join(condaHome, "envs")
	app := &Server{
		fileRoot: fileRoot, runtimeAssetsDir: runtimeAssets,
		condaHome: condaHome, condaEnvsPath: condaEnvs,
	}

	paths, err := app.agentKernelProtectedPaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{condaHome, condaEnvs} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("read-only protected-path resolution created %q: %v", path, statErr)
		}
		target, targetErr := canonicalAgentWorkspaceDirectoryTarget(path)
		if targetErr != nil || !stringSliceContains(paths, target) {
			t.Fatalf("protected paths=%#v missing %q err=%v", paths, target, targetErr)
		}
	}
	if err := app.ensureAgentKernelManagedDirectories(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{condaHome, condaEnvs} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("execution ensure did not create %q info=%#v err=%v", path, info, statErr)
		}
	}
}

func TestAgentKernelProtectedPathsDoNotCreateMissingExternalCondaDirectory(t *testing.T) {
	fileRoot := t.TempDir()
	externalRoot := t.TempDir()
	missingExternal := filepath.Join(externalRoot, "missing-conda")
	app := &Server{fileRoot: fileRoot, condaHome: missingExternal}

	if _, err := app.agentKernelProtectedPaths(); err == nil || err.Error() != "kernel protected paths could not be verified" {
		t.Fatalf("external missing protected path err=%v", err)
	}
	if _, err := os.Stat(missingExternal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external protected path was created: %v", err)
	}
}

func TestAgentKernelProtectedPathsDoNotCreateThroughSymlinkedManagedParent(t *testing.T) {
	fileRoot := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(fileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	linkedParent := filepath.Join(fileRoot, "managed")
	if err := os.Symlink(external, linkedParent); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	managed := filepath.Join(linkedParent, "conda")
	app := &Server{fileRoot: fileRoot, condaHome: managed}
	if _, err := app.agentKernelProtectedPaths(); err == nil || err.Error() != "kernel protected paths could not be verified" {
		t.Fatalf("symlinked managed path err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(external, "conda")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external directory must not be created: %v", err)
	}
}

func TestAgentKernelExecutesRealReferenceRRuntimeAndRestartsCleanly(t *testing.T) {
	environmentRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_CLAUDE_R_ENVS"))
	if environmentRoot == "" {
		t.Skip("SYNON_TEST_CLAUDE_R_ENVS is not configured")
	}
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
	newManager := func() *kernelruntime.Manager {
		return kernelruntime.NewManager(kernelruntime.Config{
			Python: python, AssetRoot: assetRoot,
			ManifestPath: filepath.Join(assetRoot, "kernel-compute.manifest.json"),
			WorkerPath:   filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
			RWorkerPath:  filepath.Join(assetRoot, "kernels", "kernel_worker.R"),
			CondaHome:    filepath.Join(t.TempDir(), "conda"), CondaEnvsPath: environmentRoot, DefaultREnv: "r",
			ExecutionTimeout: 5 * time.Second, InterruptGrace: 2 * time.Second, ShutdownTimeout: 2 * time.Second,
		})
	}
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createKernelAPIProjectAndFrame(t, store, "project-real-r", "owner-real-r", "frame-real-r", "OPERON", "")
	access, found, err := store.GetKernelFrameAccess("frame-real-r")
	if err != nil || !found {
		t.Fatalf("R frame access found=%t err=%v", found, err)
	}
	identity := &agentKernelContext{access: access, workspaceDir: filepath.Join(filepath.Dir(databasePath), "r-workspace")}
	manager := newManager()
	app := New(Options{FileRoot: filepath.Dir(databasePath), Workspace: store, KernelManager: manager})
	var rSchema agentruntime.ToolSchema
	for _, schema := range app.agentKernelToolSchemas(identity, nil) {
		if schema.Name == "r" {
			rSchema = schema
		}
	}
	if rSchema.Name == "" {
		t.Fatal("real ready R runtime was not advertised to the agent")
	}
	properties, _ := rSchema.Parameters["properties"].(map[string]any)
	required, _ := rSchema.Parameters["required"].([]string)
	if rSchema.Description != agentKernelRDescription || rSchema.Parameters["type"] != "object" || rSchema.Parameters["additionalProperties"] != false ||
		!sameStringSet(required, []string{"code", "environment", "human_description"}) || len(properties) != 5 ||
		properties["code"] == nil || properties["environment"] == nil || properties["working_dir"] == nil ||
		properties["background"] == nil || properties["human_description"] == nil || !kernelSchemaPropertiesDescribed(properties) {
		t.Fatalf("R schema=%#v description=%q", rSchema.Parameters, rSchema.Description)
	}
	for _, invalid := range []map[string]any{
		{"environment": "r", "code": `cat("wrong")`, "fresh": false},
		{"environment": "r", "code": `cat("wrong")`, "unknown": true},
		{"environment": 1, "code": `cat("wrong")`},
		{"environment": "r", "code": 1},
	} {
		if _, err := app.executeAgentKernelTool(context.Background(), identity, "r", invalid); err == nil {
			t.Fatalf("invalid R input was accepted: %#v", invalid)
		}
	}
	if kernels := manager.ListSessionKernels("frame-real-r"); len(kernels) != 0 {
		t.Fatalf("invalid R input started kernels: %#v", kernels)
	}
	result, err := app.executeAgentKernelTool(context.Background(), identity, "r", map[string]any{
		"environment": "r", "code": `state_value <- 40; cat(state_value + 2, "\n")`,
	})
	if err != nil || len(result) != 3 || strings.TrimSpace(stringValue(result["stdout"])) != "42" ||
		stringValue(result["stderr"]) != "" || result["exit_code"] != 0 {
		t.Fatalf("real R tool result=%#v err=%v", result, err)
	}
	records, err := store.ListExecutionLog("frame-real-r", "")
	if err != nil || len(records) != 1 || records[0].Language != "r" || strings.TrimSpace(records[0].Stdout) != "42" {
		t.Fatalf("real R execution log=%#v err=%v", records, err)
	}
	errorResult, err := app.executeAgentKernelTool(context.Background(), identity, "r", map[string]any{
		"environment": "r", "code": `stop("visible-error")`,
	})
	if err != nil || len(errorResult) != 3 || errorResult["exit_code"] != 1 ||
		!strings.Contains(stringValue(errorResult["stderr"]), "visible-error") || errorResult["cancelled"] != nil {
		t.Fatalf("real R error result=%#v err=%v", errorResult, err)
	}
	type executionResult struct {
		result map[string]any
		err    error
	}
	cancelledResult := make(chan executionResult, 1)
	go func() {
		result, executeErr := app.executeAgentKernelTool(context.Background(), identity, "r", map[string]any{
			"environment": "r", "code": `writeLines("ready", "cancel-started"); repeat {}`,
		})
		cancelledResult <- executionResult{result: result, err: executeErr}
	}()
	waitForKernelAPIFile(t, filepath.Join(identity.workspaceDir, "cancel-started"))
	var active []kernelruntime.ExecStream
	deadline := time.Now().Add(3 * time.Second)
	for len(active) == 0 && time.Now().Before(deadline) {
		active = manager.ListExecStreams("frame-real-r")
		runtime.Gosched()
	}
	if len(active) != 1 {
		t.Fatalf("active R executions=%#v", active)
	}
	if interrupt := manager.Interrupt("frame-real-r", active[0].ExecID); !interrupt.Interrupted {
		t.Fatalf("R interrupt=%#v", interrupt)
	}
	select {
	case cancelled := <-cancelledResult:
		if cancelled.err != nil || len(cancelled.result) != 4 || cancelled.result["exit_code"] != 1 ||
			cancelled.result["cancelled"] != true || !strings.Contains(stringValue(cancelled.result["stderr"]), "Interrupted") {
			t.Fatalf("real R cancelled result=%#v err=%v", cancelled.result, cancelled.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("real R cancellation did not complete")
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 3*time.Second)
	if _, err := manager.CloseAll(closeCtx); err != nil {
		cancelClose()
		t.Fatal(err)
	}
	cancelClose()

	restartedManager := newManager()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = restartedManager.CloseAll(ctx)
	})
	restarted := New(Options{FileRoot: filepath.Dir(databasePath), Workspace: store, KernelManager: restartedManager})
	result, err = restarted.executeAgentKernelTool(context.Background(), identity, "r", map[string]any{
		"environment": "r", "code": `cat(exists("state_value"), "\n")`,
	})
	if err != nil || len(result) != 3 || strings.TrimSpace(stringValue(result["stdout"])) != "FALSE" ||
		stringValue(result["stderr"]) != "" || result["exit_code"] != 0 {
		t.Fatalf("real R restart result=%#v err=%v", result, err)
	}
}

func TestTranscriptRunnerBackgroundKernelWaitsForDurableNotification(t *testing.T) {
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
		ManagedPythonEnvironment: "python",
		ManifestPath:             filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:               filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		ExecutionTimeout:         5 * time.Second, InterruptGrace: time.Second, ShutdownTimeout: time.Second,
	})
	store, repo, _ := newTranscriptWebFixture(t)
	stopSettlement := startKernelSettlementTestDispatcher(t, store)
	defer stopSettlement()
	seedTranscriptWebFrame(t, store, "local", "project-background", "frame-background")
	projectPath := filepath.Join(t.TempDir(), "background-project")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProject("project-background", workspace.UpdateProjectInput{Path: &projectPath}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestNumber {
		case 1:
			available := map[string]bool{}
			for _, tool := range request.Tools {
				available[tool.Function.Name] = true
			}
			if !available["python"] || !available["wait_for_notification"] {
				t.Errorf("background tools unavailable: %#v", available)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"background-python","type":"function","function":{"name":"python","arguments":"{\"code\":\"import time; time.sleep(0.2); print('BACKGROUND-READY')\",\"environment\":\"python\",\"background\":true,\"human_description\":\"Running background kernel check\"}"}}]}}]}`))
		case 2:
			if !chatRequestHasToolResult(request, "background-python", "running") {
				t.Errorf("provider did not receive running background receipt: %#v", request.Messages)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"wait-background","type":"function","function":{"name":"wait_for_notification","arguments":"{\"timeout_seconds\":3,\"human_description\":\"Waiting for background kernel result\"}"}}]}}]}`))
		case 3:
			if !chatRequestHasToolResult(request, "wait-background", "BACKGROUND-READY") {
				t.Errorf("provider did not receive durable cell result: %#v", request.Messages)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Background result collected"}}]}`))
		default:
			t.Errorf("unexpected provider request %d", requestNumber)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer provider.Close()
	app := newV11TestServer(t, Options{Workspace: store, Transcript: repo, KernelManager: manager, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := app.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	if _, _, err := app.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-background", MessageUUID: "message-background", ClientMessageID: "client-background",
		Text: "Run Python in the background and wait for its durable result.",
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, app, "local", "frame-background")
	result, err := runTranscriptRunnerWithLocalExecApproval(t, app, "local", SessionRunnerChatOptions{
		SessionID: "frame-background", RunnerID: "runner-background", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, ReplayLimit: 100,
		OutputLimitBytes: 1 << 20, DisableSkillDiscovery: true, AllowedTools: []string{"python"},
	}, nil)
	if err != nil || !result.Claimed || result.Status != "completed" || requests.Load() != 3 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	assertKernelApprovalEventCounts(t, store, "frame-background", 1, 1, 1)
	waitForKernelExecutionCount(t, manager, 0)
	confirmations, err := app.webConversationPendingConfirmations("frame-background")
	if err != nil || len(confirmations) != 0 {
		t.Fatalf("terminal approval confirmations=%#v err=%v", confirmations, err)
	}
	access, found, err := store.GetKernelFrameAccess("frame-background")
	if err != nil || !found {
		t.Fatalf("access found=%t err=%v", found, err)
	}
	if count, err := store.CountUnreadNotifications(context.Background(), access.Frame.ID, access.Frame.RootFrameID, access.UserID); err != nil || count != 0 {
		t.Fatalf("unread after tool checkpoint=%d err=%v", count, err)
	}
}

func runTranscriptRunnerWithLocalExecApproval(
	t *testing.T,
	app *Server,
	ownerUserID string,
	options SessionRunnerChatOptions,
	beforeResolve func(),
) (SessionRunnerCycleResult, error) {
	t.Helper()
	runnerCtx, cancelRunner := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRunner()
	paused, err := app.RunSessionRunnerChatOnce(runnerCtx, options)
	if err != nil || !paused.Claimed || paused.Status != "awaiting_approval" {
		t.Fatalf("approval pause result=%#v err=%v", paused, err)
	}
	requestID := waitForKernelLocalExecConfirmation(t, app, options.SessionID)
	if beforeResolve != nil {
		beforeResolve()
	}
	compatJSONRequest(t, app.Handler(), http.MethodPost,
		"/api/frames/"+options.SessionID+"/resolve-input", ownerUserID,
		map[string]any{"responses": []any{map[string]any{"requestId": requestID, "action": "allow_once"}}},
		http.StatusOK)
	return app.RunSessionRunnerChatOnce(runnerCtx, options)
}

func chatRequestHasToolResult(request chatCompletionRequest, callID, contains string) bool {
	for _, message := range request.Messages {
		if message.Role == "tool" && message.ToolCallID == callID && strings.Contains(message.Content, contains) {
			return true
		}
	}
	return false
}

func TestAgentKernelCellResultPayloadStatusesAndTruncation(t *testing.T) {
	started := kernelruntime.ExecutionStarted{ExecID: "exec-1", ToolUseID: "tool-1", KernelKind: "analysis"}
	tests := []struct {
		name       string
		exitStatus string
		wantStatus string
	}{
		{name: "completed", exitStatus: "ok", wantStatus: "completed"},
		{name: "errored", exitStatus: "error", wantStatus: "errored"},
		{name: "interrupted", exitStatus: "cancelled", wantStatus: "interrupted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := agentKernelCellResultPayload(started, map[string]any{
				"exit_status": test.exitStatus, "stdout": strings.Repeat("界", 200),
			}, 96)
			if payload["status"] != test.wantStatus || payload["exec_id"] != "exec-1" || payload["tool_id"] != "tool-1" {
				t.Fatalf("payload=%#v", payload)
			}
			output := stringValue(payload["output"])
			if len(output) > 96 || !utf8.ValidString(output) || numberValue(payload["output_truncated_from_chars"]) <= int64(utf8.RuneCountInString(output)) {
				t.Fatalf("truncated payload=%#v output_bytes=%d", payload, len(output))
			}
		})
	}
}

func TestAgentKernelRestartProjectsReferenceLostCellResult(t *testing.T) {
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	execution := workspace.BackgroundKernelExecution{
		ExecID: "lost-exec", ToolID: "lost-tool", ToolName: "python",
		FrameID: identity.access.Frame.ID, RootFrameID: identity.access.Frame.RootFrameID,
		FrameIncarnationID:     identity.access.Frame.IncarnationID,
		RootFrameIncarnationID: identity.access.RootFrameIncarnationID,
		StartedAt:              time.Now().UTC().Add(-time.Minute),
	}
	if _, err := store.RecordBackgroundKernelExecutionStarted(context.Background(), identity.access, execution); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "lost-wait", map[string]any{"timeout_seconds": 0})
		notifications, _ := result["notifications"].([]any)
		if err != nil || result["status"] != "received" || result["num_notifications"] != 1 || len(notifications) != 1 {
			t.Fatalf("lost wait %d result=%#v err=%v", attempt, result, err)
		}
		notification, _ := notifications[0].(map[string]any)
		payload, _ := notification["payload"].(map[string]any)
		completed, _ := result["cells_completed"].([]any)
		if notification["notification_type"] != "cell_result" || payload["status"] != "interrupted" ||
			payload["output"] != "[CANCELLED] This python cell was running when the session restarted; kernel state was lost." ||
			len(completed) != 1 || completed[0] != execution.ExecID {
			t.Fatalf("lost notification=%#v completed=%#v", notification, completed)
		}
	}
	if count, err := store.CountUnreadNotifications(context.Background(), identity.access.Frame.ID, identity.access.Frame.RootFrameID, identity.access.UserID); err != nil || count != 1 {
		t.Fatalf("lost execution notifications=%d err=%v", count, err)
	}
	if err := app.ackAgentKernelNotificationClaim(context.Background(), identity.access.Frame.ID, "lost-wait", 1); err != nil {
		t.Fatal(err)
	}
	noWork, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "lost-empty", map[string]any{"timeout_seconds": 0})
	if err != nil || noWork["status"] != "completed" || noWork["num_notifications"] != 0 ||
		len(noWork["notifications"].([]any)) != 0 || noWork["error"] != nil ||
		noWork["system_hint"] != "All delegations have completed and their notifications have been consumed." {
		t.Fatalf("post-lost notification work=%#v err=%v", noWork, err)
	}
	events, err := store.ListFrameEvents(identity.access.Frame.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var lostEvents int
	for _, event := range events {
		if event.Type == "kernel_execution_background_lost" {
			lostEvents++
		}
	}
	if lostEvents != 1 {
		t.Fatalf("lost events=%d events=%#v", lostEvents, events)
	}
}

func TestAgentKernelQuestionNotificationUsesReferenceProjection(t *testing.T) {
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	if _, _, err := store.CreateNotification(context.Background(), workspace.CreateNotificationInput{
		ID: "question-1", SenderFrameID: identity.access.Frame.ID, RecipientFrameID: identity.access.Frame.ID,
		RootFrameID: identity.access.Frame.RootFrameID, OwnerUserID: identity.access.UserID,
		NotificationType: "child_message",
		Payload:          map[string]any{"kind": "question", "name": "reviewer", "sender_frame_id": "frame-123", "text": "Which cohort?"},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "question-wait", map[string]any{"timeout_seconds": 0})
	if err != nil || result["status"] != "received" || result["num_notifications"] != 1 || result["cells_completed"] != nil ||
		result["system_hint"] != "1 agent question(s) — answer each with host.send_message('<frame_id>', '<answer>'): reviewer (frame-123)." {
		t.Fatalf("question result=%#v err=%v", result, err)
	}
	running, ok := result["running_children"].([]any)
	if !ok || len(running) != 0 {
		t.Fatalf("question running children=%#v", result["running_children"])
	}
}

func TestAgentKernelWaitReturnsReferenceUncollectedLandingUntilCollect(t *testing.T) {
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	children, err := store.CreateKernelSupervisedChildren(context.Background(), workspace.CreateKernelDelegatesInput{
		ParentFrameID: identity.access.Frame.ID, OwnerUserID: identity.access.UserID, ToolUseID: "landing-delegate",
		Requests: []workspace.KernelDelegateRequest{{Task: "finish without blocking", Name: "reviewer"}},
	})
	if err != nil || len(children) != 1 {
		t.Fatalf("children=%#v err=%v", children, err)
	}
	child, err := store.CompleteKernelSupervisedChild(context.Background(), children[0].FrameID, identity.access.UserID, "completed", map[string]any{"response": "done"}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "landing-wait", map[string]any{"timeout_seconds": 0})
	if err != nil || result["status"] != "uncollected_results" || result["num_notifications"] != 0 {
		t.Fatalf("uncollected result=%#v err=%v", result, err)
	}
	if notifications, ok := result["notifications"].([]any); !ok || len(notifications) != 0 {
		t.Fatalf("uncollected notifications=%#v", result["notifications"])
	}
	uncollected, ok := result["uncollected"].([]any)
	if !ok || len(uncollected) != 1 {
		t.Fatalf("uncollected=%#v", result["uncollected"])
	}
	landing, _ := uncollected[0].(map[string]any)
	if landing["frame_id"] != child.FrameID || landing["agent_name"] != child.AgentName || landing["status"] != "completed" ||
		!strings.Contains(stringValue(result["system_hint"]), "host.collect(['"+child.FrameID+"'])") {
		t.Fatalf("landing=%#v result=%#v", landing, result)
	}
	collected := app.collectKernelChildren(context.Background(), identity.access, []string{child.FrameID}, []workspace.KernelSupervisedChild{child}, []bool{true}, 0)
	if len(collected) != 1 {
		t.Fatalf("collected=%#v", collected)
	}
	if _, err := store.CompleteKernelSupervisedChild(context.Background(), child.FrameID, identity.access.UserID, "completed", map[string]any{"response": "done"}, ""); err != nil {
		t.Fatalf("terminal retry after collection: %v", err)
	}
	noWork, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "landing-empty", map[string]any{"timeout_seconds": 0})
	if err != nil || noWork["status"] != "completed" || noWork["num_notifications"] != 0 ||
		len(noWork["notifications"].([]any)) != 0 || noWork["error"] != nil ||
		noWork["system_hint"] != "All delegations have completed and their notifications have been consumed." {
		t.Fatalf("post-collect result=%#v err=%v", noWork, err)
	}
}

func TestAgentKernelCancellationDequeuesBeforeExecutionStarts(t *testing.T) {
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	spec, err := app.agentKernelSessionSpec(context.Background(), identity.access, identity.workspaceDir, agentKernelSessionRuntime{
		PublicName: "python", Kind: "analysis", Language: "python", Environment: "python",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureSession(spec); err != nil {
		t.Fatal(err)
	}
	blocker, err := manager.Submit(kernelruntime.SubmitRequest{
		FrameID: spec.FrameID, KernelKind: spec.KernelKind, Language: spec.Language, Environment: spec.Environment,
		ExecID: "blocking-cell", ToolUseID: "blocking-tool",
		Code: "import time\nfrom pathlib import Path\nwhile not Path('release-blocking-cell').exists():\n    time.sleep(0.01)", Origin: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocker.Started():
	case <-time.After(3 * time.Second):
		t.Fatal("blocking cell did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := app.executeAgentKernelTool(ctx, identity, "python", map[string]any{
			"code": "open('cancelled-marker.txt', 'w').write('must not execute')", "environment": "python",
		})
		result <- err
	}()
	releaseBlocker := func() {
		_ = os.WriteFile(filepath.Join(identity.workspaceDir, "release-blocking-cell"), []byte("release"), 0o600)
	}
	defer releaseBlocker()
	deadline := time.Now().Add(5 * time.Second)
	for manager.ActiveExecutionCount() < 2 && time.Now().Before(deadline) {
		select {
		case earlyErr := <-result:
			t.Fatalf("agent cell ended before it queued: %v", earlyErr)
		default:
		}
		runtime.Gosched()
	}
	if manager.ActiveExecutionCount() != 2 {
		t.Fatal("agent cell was not queued behind blocking cell")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled execution error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued agent execution did not cancel promptly")
	}
	releaseBlocker()
	select {
	case <-blocker.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("blocking cell did not finish")
	}
	if _, err := os.Stat(filepath.Join(identity.workspaceDir, "cancelled-marker.txt")); !os.IsNotExist(err) {
		t.Fatalf("cancelled queued code executed: %v", err)
	}
	records, err := store.ListExecutionLog(identity.access.Frame.ID, "")
	if err != nil || len(records) != 0 {
		t.Fatalf("cancelled queued execution records=%#v err=%v", records, err)
	}
}

func TestAgentKernelBackgroundStartPersistenceFailureReleasesRegistry(t *testing.T) {
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := manager.CloseAll(ctx); err != nil {
			t.Fatal(err)
		}
	}()
	if _, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "print('completed before persistence failure')", "environment": "python", "background": true,
	}); err == nil {
		t.Fatalf("background persistence error=%v", err)
	}
	deadline := time.Now().Add(time.Second)
	for manager.ActiveExecutionCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.ActiveExecutionCount() != 0 {
		t.Fatalf("background registry entries=%d", manager.ActiveExecutionCount())
	}
}

func TestAgentKernelReferenceToolExecutionContract(t *testing.T) {
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	stopSettlement := startKernelSettlementTestDispatcher(t, store)
	defer stopSettlement()
	workingDir := filepath.Join(t.TempDir(), "explicit-working-directory")
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := app.upsertHostGrant(identity.access.UserID, workingDir, "read_write"); err != nil {
		t.Fatal(err)
	}
	readOnlyDir := filepath.Join(t.TempDir(), "read-only-working-directory")
	if err := os.MkdirAll(readOnlyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readOnlyDir, "input.txt"), []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.upsertHostGrant(identity.access.UserID, readOnlyDir, "read"); err != nil {
		t.Fatal(err)
	}

	changed, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import os; open('external-created.txt','w').write('created'); print(os.getcwd())", "environment": "python", "working_dir": workingDir,
	})
	if err != nil || strings.TrimSpace(stringValue(changed["stdout"])) != workingDir {
		t.Fatalf("working directory result=%#v err=%v", changed, err)
	}
	writes, _ := changed["files_written"].([]kernelruntime.FileWrite)
	if len(writes) != 1 || writes[0].Path != filepath.Join(workingDir, "external-created.txt") || writes[0].Dropped {
		t.Fatalf("external working directory writes=%#v", changed["files_written"])
	}
	persistedWrite, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import os; open('persisted-cwd.txt','w').write('tracked'); print(os.getcwd())", "environment": "python",
	})
	if err != nil || strings.TrimSpace(stringValue(persistedWrite["stdout"])) != workingDir {
		t.Fatalf("persistent writable working directory result=%#v err=%v", persistedWrite, err)
	}
	persistedWrites, _ := persistedWrite["files_written"].([]kernelruntime.FileWrite)
	if len(persistedWrites) != 1 || persistedWrites[0].Path != filepath.Join(workingDir, "persisted-cwd.txt") {
		t.Fatalf("persistent working directory writes=%#v", persistedWrites)
	}
	readOnly, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code":        "from pathlib import Path\nvalue=Path('input.txt').read_text()\ntry:\n Path('denied.txt').write_text('no')\n status='allowed'\nexcept OSError:\n status='denied'\nprint(value+'|'+status)",
		"environment": "python", "working_dir": readOnlyDir,
	})
	if err != nil || strings.TrimSpace(stringValue(readOnly["stdout"])) != "seed|denied" {
		t.Fatalf("read-only working directory result=%#v err=%v", readOnly, err)
	}
	if _, err := os.Stat(filepath.Join(readOnlyDir, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("read-only grant was writable: %v", err)
	}
	persisted, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import os; print(os.getcwd())", "environment": "python",
	})
	if err != nil || strings.TrimSpace(stringValue(persisted["stdout"])) != readOnlyDir {
		t.Fatalf("persistent working directory result=%#v err=%v", persisted, err)
	}
	emptyWorkingDir, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import os; print(os.getcwd())", "environment": "python", "working_dir": "",
	})
	if err != nil || strings.TrimSpace(stringValue(emptyWorkingDir["stdout"])) != readOnlyDir {
		t.Fatalf("empty working directory result=%#v err=%v", emptyWorkingDir, err)
	}
	relativeWorkingDir := filepath.Join(identity.workspaceDir, "relative", "path")
	if err := os.MkdirAll(relativeWorkingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	relativeResult, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import os; print(os.getcwd())", "environment": "python", "working_dir": "relative/path",
	})
	if err != nil || strings.TrimSpace(stringValue(relativeResult["stdout"])) != relativeWorkingDir {
		t.Fatalf("relative working directory result=%#v err=%v", relativeResult, err)
	}
	newRelativeWorkingDir := filepath.Join(identity.workspaceDir, "created", "by", "model")
	newRelativeResult, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import os; print(os.getcwd())", "environment": "python", "working_dir": "created/by/model",
	})
	if err != nil || strings.TrimSpace(stringValue(newRelativeResult["stdout"])) != newRelativeWorkingDir {
		t.Fatalf("new relative working directory result=%#v err=%v", newRelativeResult, err)
	}
	if info, statErr := os.Stat(newRelativeWorkingDir); statErr != nil || !info.IsDir() {
		t.Fatalf("new relative working directory was not created: %v", statErr)
	}
	if _, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "print('no')", "environment": "python", "working_dir": "../../escape",
	}); err == nil || !strings.Contains(err.Error(), "outside the authorized workspace") {
		t.Fatalf("escaping relative working directory error=%v", err)
	}
	duplicateCWD, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import os\nos.chdir('relative/path')\nprint(os.getcwd())", "environment": "python", "working_dir": "relative/path",
	})
	if err != nil || boolValue(duplicateCWD["ok"], true) || duplicateCWD["code"] != "python_file_not_found" ||
		duplicateCWD["preflight"] != nil {
		t.Fatalf("Oracle working directory failure=%#v err=%v", duplicateCWD, err)
	}
	ungrantedDir := filepath.Join(root, "ungranted-working-directory")
	if err := os.MkdirAll(ungrantedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "print('no')", "environment": "python", "working_dir": ungrantedDir,
	}); err == nil || !strings.Contains(err.Error(), "outside the authorized workspace") {
		t.Fatalf("ungranted working directory error=%v", err)
	}
	legacyWorkspaceAlias := filepath.Join(t.TempDir(), "workspace")
	legacyAliasResult, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import os; print(os.getcwd())", "environment": "python", "working_dir": legacyWorkspaceAlias,
	})
	if err != nil || strings.TrimSpace(stringValue(legacyAliasResult["stdout"])) != identity.workspaceDir {
		t.Fatalf("legacy workspace alias result=%#v err=%v", legacyAliasResult, err)
	}
	if _, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "print('no')",
	}); err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("missing environment error=%v", err)
	}

	background, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code":        "import time; time.sleep(0.4); open('background-finished.txt','w').write('done')",
		"environment": "python", "working_dir": workingDir, "background": true,
	})
	if err != nil || background["status"] != "running" || strings.TrimSpace(stringValue(background["exec_id"])) == "" {
		t.Fatalf("background result=%#v err=%v", background, err)
	}
	queuedBackground, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "print('queued-background')", "environment": "python", "background": true,
	})
	if err != nil || queuedBackground["status"] != "running" || strings.TrimSpace(stringValue(queuedBackground["exec_id"])) == "" {
		t.Fatalf("queued background result=%#v err=%v", queuedBackground, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if content, readErr := os.ReadFile(filepath.Join(workingDir, "background-finished.txt")); readErr == nil && string(content) == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background Python cell did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	waited, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "wait-background-1", map[string]any{"timeout_seconds": 3})
	if err != nil || waited["status"] != "received" || numberValue(waited["num_notifications"]) < 1 || numberValue(waited["num_notifications"]) > 2 {
		t.Fatalf("background wait=%#v err=%v", waited, err)
	}
	notifications, _ := waited["notifications"].([]any)
	if len(notifications) < 1 || len(notifications) > 2 {
		t.Fatalf("background notifications=%#v", waited["notifications"])
	}
	completed, _ := waited["cells_completed"].([]any)
	if len(completed) != len(notifications) {
		t.Fatalf("background completed=%#v", completed)
	}
	allNotifications := append([]any(nil), notifications...)
	retryWait, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "wait-background-1", map[string]any{"timeout_seconds": 0})
	if err != nil || retryWait["status"] != "received" || numberValue(retryWait["num_notifications"]) != numberValue(waited["num_notifications"]) {
		t.Fatalf("same claim retry=%#v err=%v", retryWait, err)
	}
	if len(allNotifications) == 1 {
		if err := app.ackAgentKernelNotificationClaim(context.Background(), identity.access.Frame.ID, "wait-background-1", 1); err != nil {
			t.Fatal(err)
		}
		secondWait, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "wait-background-2", map[string]any{"timeout_seconds": 3})
		if err != nil || secondWait["status"] != "received" || secondWait["num_notifications"] != 1 {
			t.Fatalf("queued background wait=%#v err=%v", secondWait, err)
		}
		secondNotifications, _ := secondWait["notifications"].([]any)
		secondCompleted, _ := secondWait["cells_completed"].([]any)
		if len(secondNotifications) != 1 || len(secondCompleted) != 1 {
			t.Fatalf("queued background notifications=%#v completed=%#v", secondWait["notifications"], secondCompleted)
		}
		allNotifications = append(allNotifications, secondNotifications...)
		if err := app.ackAgentKernelNotificationClaim(context.Background(), identity.access.Frame.ID, "wait-background-2", 1); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := app.ackAgentKernelNotificationClaim(context.Background(), identity.access.Frame.ID, "wait-background-1", len(allNotifications)); err != nil {
			t.Fatal(err)
		}
	}
	if len(allNotifications) != 2 {
		t.Fatalf("background notification count=%d", len(allNotifications))
	}
	seenExecutions := map[string]bool{}
	for _, raw := range allNotifications {
		notification, _ := raw.(map[string]any)
		payload, _ := notification["payload"].(map[string]any)
		if notification["notification_type"] != "cell_result" || notification["recipient_frame_id"] != identity.access.Frame.ID ||
			strings.TrimSpace(stringValue(notification["id"])) == "" || strings.TrimSpace(stringValue(notification["created_at"])) == "" ||
			payload["status"] != "completed" || !strings.Contains(stringValue(payload["output"]), `"exit_status":"ok"`) {
			t.Fatalf("background notification=%#v", notification)
		}
		if execID := strings.TrimSpace(stringValue(payload["exec_id"])); execID != "" {
			seenExecutions[execID] = true
		}
	}
	if !seenExecutions[stringValue(background["exec_id"])] || !seenExecutions[stringValue(queuedBackground["exec_id"])] {
		t.Fatalf("background execution notifications=%#v", seenExecutions)
	}
	noWork, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "wait-background-empty", map[string]any{"timeout_seconds": 0})
	if err != nil || noWork["status"] != "completed" || noWork["num_notifications"] != 0 || len(noWork["notifications"].([]any)) != 0 || noWork["error"] != nil ||
		noWork["system_hint"] != "All delegations have completed and their notifications have been consumed." {
		t.Fatalf("no-work wait=%#v err=%v", noWork, err)
	}

	primary, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{"code": "shared_value = 41; print(shared_value)"})
	if err != nil || strings.TrimSpace(stringValue(primary["stdout"])) != "41" {
		t.Fatalf("primary repl=%#v err=%v", primary, err)
	}
	fresh, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code":  "print('shared_value' in globals()); import host;\ntry:\n host.delegate('x')\nexcept Exception as exc:\n print(str(exc))",
		"fresh": true,
	})
	if err != nil || !strings.Contains(stringValue(fresh["stdout"]), "False") ||
		!strings.Contains(stringValue(fresh["stdout"]), "unavailable in a fresh repl kernel") {
		t.Fatalf("fresh repl=%#v err=%v", fresh, err)
	}
	primaryAgain, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{"code": "print(shared_value)"})
	if err != nil || strings.TrimSpace(stringValue(primaryAgain["stdout"])) != "41" {
		t.Fatalf("primary repl after fresh=%#v err=%v", primaryAgain, err)
	}

	for index := 0; index < 2; index++ {
		result, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
			"code": "import time; time.sleep(1.5)", "fresh": true, "background": true,
		})
		if err != nil || result["status"] != "running" {
			t.Fatalf("fresh background %d=%#v err=%v", index, result, err)
		}
	}
	timeoutWait, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "wait-fresh-timeout", map[string]any{"timeout_seconds": 0})
	if err != nil || timeoutWait["status"] != "timeout" || timeoutWait["num_notifications"] != 0 {
		t.Fatalf("fresh timeout wait=%#v err=%v", timeoutWait, err)
	}
	pending, _ := timeoutWait["pending_work"].(map[string]any)
	pendingExecutions, _ := pending["executions"].([]any)
	if len(pendingExecutions) != 2 || pending["active_compute_jobs"] != 0 || pending["undelivered_rows"] != 0 || timeoutWait["system_hint"] == nil {
		t.Fatalf("fresh pending work=%#v", timeoutWait)
	}
	primaryWhileFreshRuns, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{"code": "print(shared_value)"})
	if err != nil || strings.TrimSpace(stringValue(primaryWhileFreshRuns["stdout"])) != "41" {
		t.Fatalf("primary repl while fresh kernels run=%#v err=%v", primaryWhileFreshRuns, err)
	}
	if _, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code": "print('third')", "fresh": true, "background": true,
	}); err == nil || !strings.Contains(err.Error(), "fresh repl kernel cap reached (2 per frame)") {
		t.Fatalf("fresh cap error=%v", err)
	}
}

func TestAgentKernelForegroundWaitDetachesWithoutCancelling(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	t.Cleanup(func() { closeKernelHostTestRuntime(t, app, manager, store) })
	t.Cleanup(startKernelSettlementTestDispatcher(t, store))
	app.agentKernelForegroundWaitTimeout = 50 * time.Millisecond

	result, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import time; time.sleep(0.25); print('AUTO-BACKGROUND-READY')", "environment": "python",
	})
	if err != nil || result["status"] != "running" || strings.TrimSpace(stringValue(result["exec_id"])) == "" ||
		!strings.Contains(stringValue(result["message"]), "continues in the background") {
		t.Fatalf("auto-background result=%#v err=%v", result, err)
	}
	if manager.ActiveExecutionCount() != 1 {
		t.Fatalf("active executions after foreground detach=%d, want 1", manager.ActiveExecutionCount())
	}

	waited, err := app.executeAgentKernelNotificationWait(context.Background(), identity, "wait-auto-background", map[string]any{
		"timeout_seconds": 3,
	})
	if err != nil || waited["status"] != "received" || waited["num_notifications"] != 1 {
		stats, statsErr := store.OutboxStats(context.Background())
		deadLetters, deadLetterErr := store.ListOutboxDeadLetters(context.Background(), 10)
		t.Fatalf("auto-background wait=%#v err=%v outbox=%#v stats_err=%v dead_letters=%#v dead_letter_err=%v",
			waited, err, stats, statsErr, deadLetters, deadLetterErr)
	}
	notifications, _ := waited["notifications"].([]any)
	if len(notifications) != 1 {
		t.Fatalf("auto-background notifications=%#v", waited["notifications"])
	}
	notification, _ := notifications[0].(map[string]any)
	payload, _ := notification["payload"].(map[string]any)
	if notification["notification_type"] != "cell_result" || payload["status"] != "completed" ||
		!strings.Contains(stringValue(payload["output"]), "AUTO-BACKGROUND-READY") ||
		!strings.Contains(stringValue(payload["output"]), stringValue(result["exec_id"])) {
		t.Fatalf("auto-background notification=%#v", notification)
	}
	waitForKernelExecutionCount(t, manager, 0)
}

func TestAgentKernelGenerationStopInterruptsForegroundExecution(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	t.Cleanup(func() { closeKernelHostTestRuntime(t, app, manager, store) })
	startedPath := filepath.Join(identity.workspaceDir, "generation-stop-started.txt")
	finishedPath := filepath.Join(identity.workspaceDir, "generation-stop-finished.txt")
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := app.executeAgentKernelTool(ctx, identity, "repl", map[string]any{
			"code":  "import time\nfrom pathlib import Path\nPath('generation-stop-started.txt').write_text('started', encoding='utf-8')\ntime.sleep(30)\nPath('generation-stop-finished.txt').write_text('finished', encoding='utf-8')",
			"fresh": true,
		})
		done <- err
	}()
	waitForKernelAPIFile(t, startedPath)
	cancel(fmt.Errorf("%w: test user stop", ErrGenerationStopped))
	select {
	case err := <-done:
		if !errors.Is(err, ErrGenerationStopped) {
			t.Fatalf("generation stop error=%v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("generation stop did not interrupt the foreground execution")
	}
	waitForKernelExecutionCount(t, manager, 0)
	if _, err := os.Stat(finishedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled foreground execution reached its terminal write: %v", err)
	}
}

func TestAgentKernelForegroundWaitBudgetUsesReferenceSessionContract(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	t.Cleanup(func() { closeKernelHostTestRuntime(t, app, manager, store) })
	if got := app.agentKernelForegroundWaitBudget(identity.access.Frame.RootFrameID); got != 10*time.Minute {
		t.Fatalf("default foreground wait=%s, want 10m", got)
	}
	if err := app.mergeRuntimeSessionConfig(identity.access.Frame.RootFrameID, map[string]any{
		"async_local_exec_wallclock_cap_s": 0,
	}); err != nil {
		t.Fatal(err)
	}
	if got := app.agentKernelForegroundWaitBudget(identity.access.Frame.RootFrameID); got != 0 {
		t.Fatalf("disabled foreground wait=%s, want 0", got)
	}
	if err := app.mergeRuntimeSessionConfig(identity.access.Frame.RootFrameID, map[string]any{
		"async_local_exec_wallclock_cap_s": 2000000,
	}); err != nil {
		t.Fatal(err)
	}
	if got := app.agentKernelForegroundWaitBudget(identity.access.Frame.RootFrameID); got != 2000000*time.Second {
		t.Fatalf("maximum foreground wait=%s, want 2000000s", got)
	}
}

func TestHostGrantMutationStopsOwnerKernelBeforeReturn(t *testing.T) {
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	external := filepath.Join(t.TempDir(), "revoked-grant")
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := app.upsertHostGrant(identity.access.UserID, external, "read_write"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(external, "late-write.txt")
	result, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": fmt.Sprintf(`
import subprocess
subprocess.Popen(["setsid", "sh", "-c", %q])
print("spawned")
`, "sleep 1; printf escaped > "+marker),
		"environment": "python", "working_dir": external,
	})
	if err != nil || strings.TrimSpace(stringValue(result["stdout"])) != "spawned" || manager.ActiveCount() != 1 {
		t.Fatalf("spawned result=%#v active=%d err=%v", result, manager.ActiveCount(), err)
	}
	removed, err := app.revokeHostGrant(identity.access.UserID, external)
	if err != nil || !removed || manager.ActiveCount() != 0 {
		t.Fatalf("revoke removed=%t active=%d err=%v", removed, manager.ActiveCount(), err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("revoked namespace wrote after mutation returned: %v", err)
	}
	if _, err := app.upsertHostGrant(identity.access.UserID, external, "read"); err != nil {
		t.Fatal(err)
	}
	readOnly, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": `
try:
    open("after-downgrade.txt", "w").write("forbidden")
    print("writable")
except OSError:
    print("read-only")
`, "environment": "python", "working_dir": external,
	})
	if err != nil || strings.TrimSpace(stringValue(readOnly["stdout"])) != "read-only" {
		t.Fatalf("read-only replacement result=%#v err=%v", readOnly, err)
	}
}

func kernelSchemaPropertiesDescribed(properties map[string]any) bool {
	if len(properties) == 0 {
		return false
	}
	for _, value := range properties {
		property, ok := value.(map[string]any)
		if !ok || strings.TrimSpace(stringValue(property["description"])) == "" {
			return false
		}
	}
	return true
}
