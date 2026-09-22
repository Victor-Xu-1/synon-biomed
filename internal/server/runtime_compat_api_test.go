package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRuntimeSystemAndFeedbackCompatibilityAPI(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Owned"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-2", UserID: "user-2", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	for _, frame := range []workspace.CreateFrameInput{
		{ID: "frame-1", ProjectID: "project-1", AgentName: "synon", Status: "failed", ConversationType: "task"},
		{ID: "frame-2", ProjectID: "project-2", AgentName: "synon", Status: "failed", ConversationType: "task"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	for _, frameID := range []string{"frame-1", "frame-2"} {
		if err := store.SetFrameOutputData(frameID, map[string]any{"stop_reason": "refusal"}); err != nil {
			t.Fatal(err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: "frame-1", Type: "message", Payload: map[string]any{
		"role": "user", "content": "email owner@example.test token=top-secret " + filepath.Join(home, "private.txt"),
		"_credential": "must-not-survive",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: "frame-1", Type: "message", Payload: map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "thinking", "text": "private reasoning"}},
	}}); err != nil {
		t.Fatal(err)
	}
	runtimeServer := New(Options{FileRoot: root, Workspace: store, StartBackgroundServices: true})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := runtimeServer.Close(ctx); err != nil {
			t.Errorf("close runtime compatibility server: %v", err)
		}
	})
	app := runtimeServer.Handler()

	unauthenticated := httptest.NewRecorder()
	app.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /me status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	me := runtimeCompatJSON(t, app, http.MethodGet, "/api/me", "user-1", nil, http.StatusOK)
	if me["user_id"] != "user-1" || me["provider"] != "local" || me["auth_mode"] != "local_header" {
		t.Fatalf("me=%#v", me)
	}
	if email, _ := me["email"].(string); !strings.Contains(email, "user-1") {
		t.Fatalf("me email=%#v", me["email"])
	}
	invalidEmail := httptest.NewRecorder()
	invalidEmailRequest := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	invalidEmailRequest.Header.Set("X-Synon-User-Id", "user-1")
	invalidEmailRequest.Header["X-Synon-User-Email"] = []string{"victim@example.test\r\nX-Injected: true"}
	app.ServeHTTP(invalidEmail, invalidEmailRequest)
	if invalidEmail.Code != http.StatusBadRequest || strings.Contains(invalidEmail.Header().Get("X-Injected"), "true") {
		t.Fatalf("invalid email = %d headers=%#v body=%s", invalidEmail.Code, invalidEmail.Header(), invalidEmail.Body.String())
	}

	environments := runtimeCompatJSON(t, app, http.MethodGet, "/api/environments/status", "user-1", nil, http.StatusOK)
	items, ok := environments["environments"].([]any)
	if !ok || len(items) == 0 || items[0].(map[string]any)["env_name"] != "synon-go-runtime" || items[0].(map[string]any)["status"] != "ready" {
		t.Fatalf("environment status=%#v", environments)
	}
	kernelStatus := items[len(items)-1].(map[string]any)
	if kernelStatus["env_name"] != "python-kernel-sidecar" || kernelStatus["status"] != "failed" || kernelStatus["error"] == "" {
		t.Fatalf("failed kernel discovery must be explicit: %#v", environments)
	}
	if environments["conda_disabled_reason"] != kernelStatus["error"] {
		t.Fatalf("unconfigured conda reason=%#v", environments)
	}
	retry := runtimeCompatJSON(t, app, http.MethodPost, "/api/environments/retry", "user-1", map[string]any{}, http.StatusOK)
	if retry["disabled_reason"] != kernelStatus["error"] || len(retry["retried"].([]any)) != 0 {
		t.Fatalf("environment retry=%#v", retry)
	}
	assertEnvironmentRetryRepairsFailedAssets(t, root)

	safety := runtimeCompatJSON(t, app, http.MethodPost, "/api/feedback/safety", "user-1", map[string]any{
		"root_frame_id": "frame-1", "model": "model-a", "reason": "unsafe suggestion", "share_transcript": true,
	}, http.StatusCreated)
	if safety["id"] == "" || safety["root_frame_id"] != "frame-1" || safety["wire_delivered"] != false {
		t.Fatalf("safety feedback=%#v", safety)
	}
	runtimeCompatJSON(t, app, http.MethodPost, "/api/feedback/safety", "user-1", map[string]any{
		"root_frame_id": 123,
	}, http.StatusBadRequest)
	duplicate := runtimeCompatJSON(t, app, http.MethodPost, "/api/feedback/safety", "user-1", map[string]any{
		"root_frame_id": "frame-1", "reason": "duplicate must not overwrite",
	}, http.StatusCreated)
	if duplicate["id"] != safety["id"] || duplicate["already_submitted"] != true {
		t.Fatalf("idempotent safety feedback=%#v first=%#v", duplicate, safety)
	}
	runtimeCompatJSON(t, app, http.MethodPost, "/api/feedback/safety", "user-1", map[string]any{
		"root_frame_id": "frame-2", "reason": "foreign",
	}, http.StatusNotFound)
	availability := runtimeCompatJSON(t, app, http.MethodGet, "/api/feedback/available", "user-1", nil, http.StatusOK)
	if len(availability) != 2 || availability["available"] != false || availability["safety_available"] != false {
		t.Fatalf("feedback availability=%#v", availability)
	}
	records, err := store.ListFeedbackForUser("user-1", 10)
	if err != nil || len(records) != 1 || records[0].UserID != "user-1" {
		t.Fatalf("durable feedback records=%#v err=%v", records, err)
	}
	for _, record := range records {
		if record.Kind != "safety" {
			continue
		}
		raw := mustJSON(t, record.Payload)
		for _, forbidden := range []string{"owner@example.test", "top-secret", "must-not-survive", "private reasoning", home} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("safety transcript leaked %q: %s", forbidden, raw)
			}
		}
	}

	logoutResponse := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", bytes.NewReader([]byte(`{}`)))
	logoutRequest.Header.Set("Content-Type", "application/json")
	logoutRequest.Header.Set("X-Synon-User-Id", "user-1")
	app.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK || !strings.Contains(logoutResponse.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout status=%d cookie=%q body=%s", logoutResponse.Code, logoutResponse.Header().Get("Set-Cookie"), logoutResponse.Body.String())
	}
	logout := map[string]any{}
	if err := json.Unmarshal(logoutResponse.Body.Bytes(), &logout); err != nil || logout["status"] != "local_session" || logout["tokens_deleted"] != false || logout["restart_pending"] != false {
		t.Fatalf("logout contract=%#v err=%v", logout, err)
	}
	realtime := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?limit=100", "user-1", nil, http.StatusOK)
	counts := map[string]int{}
	for _, raw := range realtime["events"].([]any) {
		event := raw.(map[string]any)
		counts[event["type"].(string)]++
		if len(event["invalidations"].([]any)) == 0 {
			t.Fatalf("system event has no invalidations: %#v", event)
		}
	}
	if counts["environment_status"] != 1 || counts["auth_status_changed"] != 1 {
		t.Fatalf("system realtime event counts=%#v", counts)
	}
}

func TestKernelDiscoveryFailureIsVisibleAndRedacted(t *testing.T) {
	for _, fixture := range []struct {
		name       string
		manifest   string
		wantReason string
	}{
		{name: "corrupt manifest", manifest: `{`, wantReason: "kernel_assets_invalid: kernel asset verification failed"},
		{name: "missing worker", manifest: `{"schemaVersion":1,"entrypoint":"kernels/kernel_worker.py","files":[{"path":"kernels/kernel_worker.py","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","bytes":1}]}`, wantReason: "kernel_assets_invalid: kernel asset verification failed"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			assetRoot := filepath.Join(t.TempDir(), "private-kernel-assets")
			if err := os.MkdirAll(assetRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(assetRoot, "kernel-compute.manifest.json"), []byte(fixture.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("SYNON_KERNEL_ASSET_ROOT", assetRoot)
			t.Setenv("SYNON_KERNEL_PYTHON", executable)
			t.Setenv("SYNON_MICROMAMBA", executable)

			store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			runtimeServer := New(Options{FileRoot: filepath.Join(t.TempDir(), "runtime"), Workspace: store, StartBackgroundServices: true})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := runtimeServer.Close(ctx); err != nil {
					t.Errorf("close kernel discovery server: %v", err)
				}
			})
			app := runtimeServer
			status := runtimeCompatJSON(t, app.Handler(), http.MethodGet, "/api/environments/status", "user-1", nil, http.StatusOK)
			environments := status["environments"].([]any)
			kernelStatus := environments[len(environments)-1].(map[string]any)
			if kernelStatus["env_name"] != "python-kernel-sidecar" || kernelStatus["status"] != "failed" || kernelStatus["error"] != fixture.wantReason {
				t.Fatalf("kernel status=%#v", kernelStatus)
			}
			if status["conda_disabled_reason"] != fixture.wantReason {
				t.Fatalf("disabled reason=%#v", status["conda_disabled_reason"])
			}
			retry := runtimeCompatJSON(t, app.Handler(), http.MethodPost, "/api/environments/retry", "user-1", map[string]any{}, http.StatusOK)
			if len(retry["retried"].([]any)) != 0 || retry["disabled_reason"] != fixture.wantReason {
				t.Fatalf("discovery retry=%#v", retry)
			}
			diagnostics := runtimeCompatJSON(t, app.Handler(), http.MethodGet, "/api/go/diagnostics/runtime", "user-1", nil, http.StatusOK)
			errorsByArea := diagnostics["errors"].(map[string]any)
			if errorsByArea["kernel"] != fixture.wantReason {
				t.Fatalf("runtime diagnostics=%#v", diagnostics)
			}
			serialized := mustJSON(t, map[string]any{"status": status, "retry": retry, "diagnostics": diagnostics})
			if strings.Contains(serialized, assetRoot) || strings.Contains(serialized, executable) {
				t.Fatalf("kernel discovery response leaked a local path: %s", serialized)
			}
			for _, schema := range app.agentRuntimeToolSchemas([]string{"python", "r", "repl"}, "frame-missing") {
				if schema.Name == "python" || schema.Name == "r" || schema.Name == "repl" {
					t.Fatalf("failed kernel runtime published tool %#v", schema)
				}
			}
		})
	}
}

func TestKernelConfinementFailureControlsStatusRetryAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	manager := verifiedKernelManagerForStatusTest(t, root)
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{FileRoot: filepath.Join(root, "runtime"), Workspace: store, KernelManager: manager})
	available := false
	retryCalls := 0
	app.kernelConfinement = func(retry bool) kernelruntime.ConfinementEvidence {
		if retry {
			retryCalls++
			available = true
		}
		if available {
			return kernelruntime.ConfinementEvidence{Available: true, Mode: "verified-test-boundary"}
		}
		return kernelruntime.ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "private probe failure at " + root}
	}

	status := runtimeCompatJSON(t, app.Handler(), http.MethodGet, "/api/environments/status", "user-1", nil, http.StatusOK)
	for _, raw := range status["environments"].([]any)[1:] {
		environment := raw.(map[string]any)
		if environment["env_name"] == "synon-biomed-python" {
			if environment["status"] != "failed" || environment["failure_source"] != "runtime" ||
				environment["error"] != "managed Python scientific runtime is unavailable" {
				t.Fatalf("managed python environment=%#v", environment)
			}
			continue
		}
		if environment["status"] != "failed" || environment["failure_source"] != "confinement" ||
			environment["error"] != "kernel_confinement_probe_failed: kernel process confinement verification failed" {
			t.Fatalf("confinement environment=%#v", environment)
		}
	}
	diagnostics := runtimeCompatJSON(t, app.Handler(), http.MethodGet, "/api/go/diagnostics/runtime", "user-1", nil, http.StatusOK)
	if diagnostics["errors"].(map[string]any)["kernel"] != "kernel_confinement_probe_failed: kernel process confinement verification failed" || strings.Contains(mustJSON(t, diagnostics), root) {
		t.Fatalf("confinement diagnostics=%#v", diagnostics)
	}

	retry := runtimeCompatJSON(t, app.Handler(), http.MethodPost, "/api/environments/retry", "user-1", map[string]any{}, http.StatusOK)
	if retryCalls != 1 || len(retry["retried"].([]any)) != 3 || retry["disabled_reason"] != nil ||
		len(retry["repair_errors"].(map[string]any)) != 1 ||
		stringValue(retry["repair_errors"].(map[string]any)["synon-biomed-python"]) != "managed Python scientific runtime repair failed" {
		t.Fatalf("confinement retry=%#v calls=%d", retry, retryCalls)
	}
	after := runtimeCompatJSON(t, app.Handler(), http.MethodGet, "/api/environments/status", "user-1", nil, http.StatusOK)
	for _, raw := range after["environments"].([]any)[1:] {
		environment := raw.(map[string]any)
		if environment["env_name"] == "synon-biomed-python" {
			if environment["status"] != "failed" || environment["failure_source"] != "runtime" {
				t.Fatalf("managed python environment after retry=%#v", environment)
			}
			continue
		}
		if environment["status"] != "ready" || environment["error"] != nil || environment["failure_source"] != nil {
			t.Fatalf("recovered confinement environment=%#v", environment)
		}
	}
}

func verifiedKernelManagerForStatusTest(t *testing.T, root string) *kernelruntime.Manager {
	t.Helper()
	assetRoot := filepath.Join(root, "kernel-assets")
	workerPath := filepath.Join(assetRoot, "kernels", "kernel_worker.py")
	rWorkerPath := filepath.Join(assetRoot, "kernels", "kernel_worker.R")
	envsPath := filepath.Join(root, "conda", "envs")
	rscriptPath := filepath.Join(envsPath, "r", "bin", "Rscript")
	if runtime.GOOS == "windows" {
		rscriptPath = filepath.Join(envsPath, "r", "Rscript.exe")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		workerPath:  []byte("print('ready')\n"),
		rWorkerPath: []byte("cat('ready\\n')\n"),
		rscriptPath: []byte("#!/bin/sh\nexit 0\n"),
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	entries := make([]map[string]any, 0, 2)
	for _, path := range []string{workerPath, rWorkerPath} {
		body := files[path]
		digest := sha256.Sum256(body)
		relative, err := filepath.Rel(assetRoot, path)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, map[string]any{"path": filepath.ToSlash(relative), "sha256": fmt.Sprintf("%x", digest), "bytes": len(body)})
	}
	manifest, err := json.Marshal(map[string]any{"schemaVersion": 1, "entrypoint": "kernels/kernel_worker.py", "files": entries})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(assetRoot, "kernel-compute.manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: executable, Micromamba: executable, AssetRoot: assetRoot, ManifestPath: manifestPath,
		WorkerPath: workerPath, RWorkerPath: rWorkerPath, CondaHome: filepath.Join(root, "conda"), CondaEnvsPath: envsPath,
		DefaultREnv: "r",
	})
	if err := manager.Verify(); err != nil {
		t.Fatal(err)
	}
	return manager
}

func assertEnvironmentRetryRepairsFailedAssets(t *testing.T, root string) {
	t.Helper()
	assetRoot := filepath.Join(root, "repairable-kernel")
	workerPath := filepath.Join(assetRoot, "kernels", "kernel_worker.py")
	manifestPath := filepath.Join(assetRoot, "kernel-compute.manifest.json")
	rWorkerPath := filepath.Join(assetRoot, "kernels", "kernel_worker.R")
	condaHome := filepath.Join(root, "repair-conda")
	condaEnvsPath := filepath.Join(condaHome, "envs")
	micromambaPath := filepath.Join(root, "repair-micromamba")
	if err := os.MkdirAll(filepath.Dir(workerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		rWorkerPath: "# R worker\n",
		filepath.Join(condaEnvsPath, "r", "bin", "Rscript"):         "#!/bin/sh\nexit 0\n",
		filepath.Join(condaEnvsPath, "r", "Rscript.exe"):            "fixture executable\n",
		filepath.Join(condaEnvsPath, "r", "Scripts", "Rscript.exe"): "fixture executable\n",
		micromambaPath: "#!/bin/sh\nexit 0\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: "python3", AssetRoot: assetRoot, ManifestPath: manifestPath, WorkerPath: workerPath,
		RWorkerPath: rWorkerPath, CondaHome: condaHome, CondaEnvsPath: condaEnvsPath,
		DefaultREnv: "r", Micromamba: micromambaPath,
	})
	if err := manager.Verify(); err == nil {
		t.Fatal("incomplete kernel assets unexpectedly verified")
	}
	worker := []byte("print('ready')\n")
	if err := os.WriteFile(workerPath, worker, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(worker)
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "entrypoint": "kernels/kernel_worker.py",
		"files": []any{map[string]any{"path": "kernels/kernel_worker.py", "sha256": fmt.Sprintf("%x", digest), "bytes": len(worker)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	repairStore, err := workspace.Open(filepath.Join(root, "repair-environment.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repairStore.Close() })
	repairServer := New(Options{FileRoot: filepath.Join(root, "repair-runtime"), Workspace: repairStore, KernelManager: manager})
	repairServer.kernelConfinement = func(bool) kernelruntime.ConfinementEvidence {
		return kernelruntime.ConfinementEvidence{Available: true, Mode: "verified-test-boundary"}
	}
	repairApp := repairServer.Handler()
	retry := runtimeCompatJSON(t, repairApp, http.MethodPost, "/api/environments/retry", "user-1", map[string]any{}, http.StatusOK)
	retried := retry["retried"].([]any)
	if len(retried) != 2 || retried[0] != "python-kernel-sidecar" || retried[1] != "synon-biomed-python" ||
		retry["disabled_reason"] != nil ||
		stringValue(retry["repair_errors"].(map[string]any)["synon-biomed-python"]) != "managed Python scientific runtime repair failed" {
		t.Fatalf("repair retry=%#v", retry)
	}
	status := runtimeCompatJSON(t, repairApp, http.MethodGet, "/api/environments/status", "user-1", nil, http.StatusOK)
	environments := status["environments"].([]any)
	if environments[len(environments)-1].(map[string]any)["status"] != "ready" {
		t.Fatalf("repaired environment status=%#v", status)
	}
}

func runtimeCompatJSON(t *testing.T, app http.Handler, method, target, userID string, body any, wantStatus int) map[string]any {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewReader(payload))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if userID != "" {
		request.Header.Set("X-Synon-User-Id", userID)
	}
	app.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status=%d body=%s", method, target, response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s %s: %v body=%s", method, target, err, response.Body.String())
	}
	return decoded
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
