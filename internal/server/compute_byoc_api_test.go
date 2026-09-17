package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	compute "synon-go/internal/compute"
	kernelruntime "synon-go/internal/kernel"
	secretstore "synon-go/internal/persistence/secrets"
	workspace "synon-go/internal/persistence/workspace"
)

type recordingBYOCCleanupRunner struct {
	mu       sync.Mutex
	runtimes []kernelruntime.ProviderRuntimeSpec
	result   *compute.BYOCCleanupResult
	err      error
	started  chan struct{}
	release  <-chan struct{}
}

func (l *recordingBYOCCleanupRunner) RunProviderOperation(_ context.Context, input kernelruntime.ProviderOperationInput) (map[string]any, error) {
	l.mu.Lock()
	result, err := valueOrZero(l.result), l.err
	started, release := l.started, l.release
	if input.Operation == "reconcile" {
		l.runtimes = append(l.runtimes, input.Runtime)
	}
	l.mu.Unlock()
	if input.Operation == "reconcile" && started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if input.Operation == "reconcile" && release != nil {
		<-release
	}
	if err != nil {
		return nil, err
	}
	switch input.Operation {
	case "reconcile":
		sandboxes := []any{map[string]any{"sandbox_id": "sandbox-fixture"}}
		if l.result != nil {
			sandboxes = make([]any, 0, result.Owned)
			for index := 0; index < result.Owned; index++ {
				name := "sandbox-a"
				if index > 0 {
					name = "sandbox-b"
				}
				sandboxes = append(sandboxes, map[string]any{"sandbox_id": name})
			}
		}
		return map[string]any{"ok": true, "sandboxes": sandboxes}, nil
	case "terminate":
		sandboxID := stringValue(input.Request["sandbox_id"])
		for _, failed := range result.Failed {
			if sandboxID == failed {
				return nil, &kernelruntime.ProviderOperationError{Kind: "transient", Message: "fixture cleanup failure"}
			}
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, errors.New("unexpected BYOC cleanup operation")
	}
}

func valueOrZero(value *compute.BYOCCleanupResult) compute.BYOCCleanupResult {
	if value == nil {
		return compute.BYOCCleanupResult{}
	}
	return *value
}

func (l *recordingBYOCCleanupRunner) snapshot() []kernelruntime.ProviderRuntimeSpec {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]kernelruntime.ProviderRuntimeSpec(nil), l.runtimes...)
}

func newBYOCControlTestServer(t *testing.T, root string, store *workspace.Store, runner kernelruntime.ProviderOperationRunner, modalConfigPath string) *Server {
	t.Helper()
	repositoryRoot := repositoryRootForServerTest(t)
	optional := filepath.Join(repositoryRoot, "assets", "optional")
	environments := filepath.Join(root, "conda", "envs")
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
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: python, CondaHome: filepath.Join(root, "conda"), CondaEnvsPath: environments,
		AssetRoot: optional, ManifestPath: filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath: filepath.Join(optional, "kernels", "kernel_worker.py"),
	})
	return New(Options{
		Workspace: store, FileRoot: root, RuntimeAssetsDir: optional,
		SkillDirectories: []string{filepath.Join(repositoryRoot, "skills", "synonbiomed")},
		KernelManager:    manager, ProviderOperationRunner: runner, ModalConfigPath: modalConfigPath,
	})
}

func TestComputeBYOCModalFullControlLifecycle(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := &recordingBYOCCleanupRunner{}
	modalConfigPath := filepath.Join(root, ".modal.toml")
	server := newBYOCControlTestServer(t, root, store, runner, modalConfigPath)
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "modal-stored", UserID: "owner-a", Provider: "modal", Name: "Research workspace",
		Credentials: map[string]string{"token_id": "ak-fixture-id", "token_secret": "as-fixture-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	app := server.Handler()

	initial := requestCompute(t, app, http.MethodGet, "/api/compute/byoc/modal", nil, "owner-a")
	if initial.Code != http.StatusOK {
		t.Fatalf("initial status=%d body=%s", initial.Code, initial.Body.String())
	}
	var status map[string]any
	if err := json.NewDecoder(initial.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status["provider"] != "modal" || status["enabled"] != false || status["hasStoredCredential"] != true ||
		status["tomlMissing"] != true || status["credsError"] != nil ||
		len(status["profiles"].([]any)) != 1 || status["profiles"].([]any)[0].(map[string]any)["name"] != "Synon Biomed (stored)" ||
		strings.Contains(initial.Body.String(), "as-fixture-secret") {
		t.Fatalf("initial=%#v", status)
	}
	if value, exists := status["egressPolicy"]; !exists || value != nil {
		t.Fatalf("initial egress policy=%#v exists=%v", value, exists)
	}

	missing := requestCompute(t, app, http.MethodGet, "/api/compute/byoc/modal", nil, "owner-b")
	if missing.Code != http.StatusOK {
		t.Fatalf("missing credential status=%d body=%s", missing.Code, missing.Body.String())
	}
	var missingStatus map[string]any
	if err := json.NewDecoder(missing.Body).Decode(&missingStatus); err != nil {
		t.Fatal(err)
	}
	if missingStatus["credsError"] != "~/.modal.toml not found \u2014 run `modal token new` to create one." || missingStatus["egressPolicy"] != nil {
		t.Fatalf("missing credential status=%#v", missingStatus)
	}

	enable := requestCompute(t, app, http.MethodPut, "/api/compute/byoc/modal/enabled", map[string]any{"enabled": true}, "owner-a")
	if enable.Code != http.StatusNoContent {
		t.Fatalf("enable status=%d body=%s", enable.Code, enable.Body.String())
	}
	inputs := runner.snapshot()
	if len(inputs) != 0 {
		t.Fatalf("enable lifecycle=%#v", inputs)
	}

	patch := requestCompute(t, app, http.MethodPatch, "/api/compute/providers/byoc:modal", map[string]any{
		"detailsMd": "Modal operator notes", "maxConcurrentJobs": 4, "maxTimeoutSec": 3600,
		"appName": "research-modal", "environmentName": "research-1",
		"egressPolicy": map[string]any{
			"mode": "allowlist", "mirror": true,
			"additional": []string{"api.example.com", "*.data.example.com"},
		},
	}, "owner-a")
	if patch.Code != http.StatusNoContent {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	detail := requestCompute(t, app, http.MethodGet, "/api/compute/providers/byoc:modal", nil, "owner-a")
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var provider map[string]any
	if err := json.NewDecoder(detail.Body).Decode(&provider); err != nil {
		t.Fatal(err)
	}
	if provider["appName"] != "research-modal" || provider["environmentName"] != "research-1" ||
		provider["maxConcurrentJobs"] != float64(4) ||
		provider["egressPolicy"].(map[string]any)["mode"] != "allowlist" {
		t.Fatalf("provider=%#v", provider)
	}
	invalid := requestCompute(t, app, http.MethodPatch, "/api/compute/providers/byoc:modal", map[string]any{
		"egressPolicy": map[string]any{"mode": "allowlist", "mirror": false, "additional": []string{"https://bad.example"}},
	}, "owner-a")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	foreign := requestCompute(t, app, http.MethodGet, "/api/compute/providers/byoc:modal", nil, "owner-b")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}

	disable := requestCompute(t, app, http.MethodPut, "/api/compute/byoc/modal/enabled", map[string]any{"enabled": false}, "owner-a")
	if disable.Code != http.StatusNoContent {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	inputs = runner.snapshot()
	if len(inputs) != 1 || inputs[0].AppName != "research-modal" || inputs[0].ModalEnvironment != "research-1" ||
		inputs[0].InstallID == "" ||
		len(inputs[0].PriorAppNames) != 1 || inputs[0].PriorAppNames[0] != workspace.DefaultModalAppName ||
		inputs[0].Credentials["token_secret"] != "as-fixture-secret" {
		t.Fatalf("disable lifecycle=%#v", inputs)
	}
	final := requestCompute(t, app, http.MethodGet, "/api/compute/byoc/modal", nil, "owner-a")
	var finalStatus map[string]any
	if err := json.NewDecoder(final.Body).Decode(&finalStatus); err != nil {
		t.Fatal(err)
	}
	if finalStatus["enabled"] != false || finalStatus["appName"] != "research-modal" {
		t.Fatalf("final=%#v", finalStatus)
	}
	audits, err := server.runtimeStore.List(byocLifecycleAuditNamespace)
	if err != nil || len(audits) != 1 {
		t.Fatalf("audits=%#v err=%v", audits, err)
	}
	audit := audits[0].Value.(map[string]any)
	if audit["status"] != "completed" || audit["provider"] != "modal" ||
		audit["enabled"] != false || audit["installId"] == "" {
		t.Fatalf("audit=%#v", audit)
	}
}

func TestComputeBYOCModalTOMLProfilesOverrideStoredCredentialAndExposeParseErrors(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := &recordingBYOCCleanupRunner{}
	modalConfigPath := filepath.Join(root, ".modal.toml")
	server := newBYOCControlTestServer(t, root, store, runner, modalConfigPath)
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "modal-stored", UserID: "owner-a", Provider: "modal", Name: "stored",
		Credentials: map[string]string{"token_id": "ak-stored-id", "token_secret": "as-stored-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"[default]",
		"token_id = \"ak-default-123456\"",
		"token_secret = \"as-default-secret\"",
		"workspace = \"workspace-a\"",
		"",
		"[research]",
		"token_id = \"ak-active-123456\"",
		"token_secret = \"as-active-secret\"",
		"active = true",
	}, "\n")
	if err := os.WriteFile(modalConfigPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	app := server.Handler()
	response := requestCompute(t, app, http.MethodGet, "/api/compute/byoc/modal", nil, "owner-a")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var status map[string]any
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	profiles := status["profiles"].([]any)
	_, hasTomlMissing := status["tomlMissing"]
	if hasTomlMissing || status["credsError"] != nil || status["hasStoredCredential"] != true ||
		len(profiles) != 2 || profiles[0].(map[string]any)["workspace"] != "workspace-a" ||
		profiles[1].(map[string]any)["active"] != true {
		t.Fatalf("status=%#v", status)
	}
	serialized, _ := json.Marshal(status)
	if strings.Contains(string(serialized), "as-active-secret") || strings.Contains(string(serialized), "as-stored-secret") {
		t.Fatalf("credential leaked: %s", serialized)
	}
	enable := requestCompute(t, app, http.MethodPut, "/api/compute/byoc/modal/enabled", map[string]any{"enabled": true}, "owner-a")
	if enable.Code != http.StatusNoContent {
		t.Fatalf("enable status=%d body=%s", enable.Code, enable.Body.String())
	}
	disable := requestCompute(t, app, http.MethodPut, "/api/compute/byoc/modal/enabled", map[string]any{"enabled": false}, "owner-a")
	if disable.Code != http.StatusNoContent {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	inputs := runner.snapshot()
	if len(inputs) != 1 || inputs[0].Credentials["token_id"] != "ak-active-123456" ||
		inputs[0].Credentials["token_secret"] != "as-active-secret" {
		t.Fatalf("lifecycle=%#v", inputs)
	}

	if err := os.WriteFile(modalConfigPath, []byte("[broken\nsecret = \"must-not-leak\""), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := requestCompute(t, app, http.MethodGet, "/api/compute/byoc/modal", nil, "owner-a")
	var brokenStatus map[string]any
	if err := json.NewDecoder(broken.Body).Decode(&brokenStatus); err != nil {
		t.Fatal(err)
	}
	_, brokenHasTomlMissing := brokenStatus["tomlMissing"]
	if brokenHasTomlMissing || brokenStatus["credsError"] == nil ||
		len(brokenStatus["profiles"].([]any)) != 0 || strings.Contains(broken.Body.String(), "must-not-leak") {
		t.Fatalf("broken=%#v", brokenStatus)
	}
}

func TestComputeBYOCDisablePersistsIncompleteCleanupAudit(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := &recordingBYOCCleanupRunner{result: &compute.BYOCCleanupResult{
		Owned: 2, Terminated: []string{"sandbox-a"}, Failed: []string{"sandbox-b"},
	}}
	server := newBYOCControlTestServer(t, root, store, runner, filepath.Join(root, ".modal.toml"))
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "modal-stored", UserID: "owner-a", Provider: "modal",
		Credentials: map[string]string{"token_id": "ak-fixture-id", "token_secret": "as-fixture-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	app := server.Handler()
	if response := requestCompute(t, app, http.MethodPut, "/api/compute/byoc/modal/enabled", map[string]any{"enabled": true}, "owner-a"); response.Code != http.StatusNoContent {
		t.Fatalf("enable status=%d", response.Code)
	}
	if response := requestCompute(t, app, http.MethodPut, "/api/compute/byoc/modal/enabled", map[string]any{"enabled": false}, "owner-a"); response.Code != http.StatusNoContent {
		t.Fatalf("disable status=%d", response.Code)
	}
	audits, err := server.runtimeStore.List(byocLifecycleAuditNamespace)
	if err != nil || len(audits) != 1 {
		t.Fatalf("audits=%#v err=%v", audits, err)
	}
	audit := audits[0].Value.(map[string]any)
	failed := audit["failed"].([]any)
	if audit["status"] != "incomplete" || audit["owned"] != float64(2) ||
		len(failed) != 1 || failed[0] != "sandbox-b" {
		t.Fatalf("audit=%#v", audit)
	}
}

func TestComputeBYOCToggleLeaseRejectsEnableDuringDisableCleanup(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	runner := &recordingBYOCCleanupRunner{started: started, release: release}
	server := newBYOCControlTestServer(t, root, store, runner, filepath.Join(root, ".modal.toml"))
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "modal-stored", UserID: "owner-a", Provider: "modal",
		Credentials: map[string]string{"token_id": "ak-fixture-id", "token_secret": "as-fixture-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	app := server.Handler()
	if response := requestCompute(t, app, http.MethodPut, "/api/compute/byoc/modal/enabled", map[string]any{"enabled": true}, "owner-a"); response.Code != http.StatusNoContent {
		t.Fatalf("initial enable status=%d", response.Code)
	}
	disableDone := make(chan int, 1)
	go func() {
		response := requestCompute(t, app, http.MethodPut, "/api/compute/byoc/modal/enabled", map[string]any{"enabled": false}, "owner-a")
		disableDone <- response.Code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("disable cleanup did not start")
	}
	concurrentEnable := requestCompute(t, app, http.MethodPut, "/api/compute/byoc/modal/enabled", map[string]any{"enabled": true}, "owner-a")
	if concurrentEnable.Code != http.StatusConflict {
		t.Fatalf("concurrent enable status=%d body=%s", concurrentEnable.Code, concurrentEnable.Body.String())
	}
	close(release)
	select {
	case status := <-disableDone:
		if status != http.StatusNoContent {
			t.Fatalf("disable status=%d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disable did not finish")
	}
	final := requestCompute(t, app, http.MethodGet, "/api/compute/byoc/modal", nil, "owner-a")
	var status map[string]any
	if err := json.NewDecoder(final.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status["enabled"] != false {
		t.Fatalf("final status=%#v", status)
	}
}
