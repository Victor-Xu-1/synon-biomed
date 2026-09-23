//go:build linux && amd64

package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func TestScientificCoreHealthReadyAfterServiceRestartReusesInstalledGenerations(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(root, "assets", "optional")
	state := t.TempDir()
	installer := filepath.Join(state, "micromamba")
	countPath := filepath.Join(state, "install-count")
	// Only the first manager may invoke this installer. A restart must validate
	// both published generations without creating a second Conda environment.
	script := `#!/bin/sh
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
count_file="$script_dir/install-count"
count=0
if [ -f "$count_file" ]; then count=$(cat "$count_file"); fi
printf '%s\n' "$((count + 1))" > "$count_file"
prefix=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-p" ]; then shift; prefix="$1"; break; fi
  shift
done
test -n "$prefix"
mkdir -p "$prefix/bin"
case "$prefix" in
  */synon-biomed-r/*)
    printf '%s\n' '#!/bin/sh' "printf '%s\\n' 'SYNON_R_VERSION=4.5.3'" > "$prefix/bin/Rscript"
    chmod 755 "$prefix/bin/Rscript"
    ;;
  */synon-biomed-python/*)
    printf '%s\n' '#!/bin/sh' "printf '%s\\n' '{\"ok\":true,\"rdkit\":\"2024.03.5\"}'" > "$prefix/bin/python"
    chmod 755 "$prefix/bin/python"
    ;;
  *) exit 90 ;;
esac
`
	if err := os.WriteFile(installer, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	condaHome := filepath.Join(state, "conda")
	config := kernelruntime.Config{
		CondaHome:           condaHome,
		CondaEnvsPath:       filepath.Join(condaHome, "envs"),
		CondaRuntimeCatalog: filepath.Join(assets, "conda-runtimes", "manifest.json"),
		ManifestPath:        filepath.Join(assets, "kernel-compute.manifest.json"),
		AssetRoot:           assets,
		WorkerPath:          filepath.Join(assets, "kernels", "kernel_worker.py"),
		PythonHelperPath:    filepath.Join(assets, "kernels", "cheminfo_render_helpers.py"),
		RWorkerPath:         filepath.Join(assets, "kernels", "kernel_worker.R"),
		Micromamba:          installer,
	}
	first := kernelruntime.NewManager(config)
	if err := first.ProvisionManagedPythonEnvironment(t.Context()); err != nil {
		t.Fatalf("first Python provision: %v", err)
	}
	if err := first.ProvisionManagedREnvironment(t.Context()); err != nil {
		t.Fatalf("first R provision: %v", err)
	}
	firstCount, err := os.ReadFile(countPath)
	if err != nil || strings.TrimSpace(string(firstCount)) != "2" {
		t.Fatalf("first installer count=%q err=%v", firstCount, err)
	}

	store, err := workspace.Open(filepath.Join(state, "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted := &Server{
		kernelManager:  kernelruntime.NewManager(config),
		workspaceStore: store,
		compatEvents:   newCompatEventHub(),
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- restarted.RunManagedScientificRuntimeProvisionerWithReady(ctx, func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("supervisor returned before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("reused runtimes never reached supervisor ready")
	}
	response := httptest.NewRecorder()
	restarted.handleHealth(response, httptest.NewRequest("GET", "/health", nil))
	var health map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	var core struct {
		Ready  bool `json:"ready"`
		Python struct {
			Status string `json:"status"`
		} `json:"python"`
		R struct {
			Status string `json:"status"`
		} `json:"r"`
	}
	if err := json.Unmarshal(health["scientific_runtime_core"], &core); err != nil {
		t.Fatal(err)
	}
	if !core.Ready || core.Python.Status != "ready" || core.R.Status != "ready" {
		t.Fatalf("supervisor ready but health core=%+v", core)
	}
	var runtimeReady bool
	if err := json.Unmarshal(health["scientific_runtime_ready"], &runtimeReady); err != nil || !runtimeReady {
		t.Fatalf("scientific_runtime_ready=%q err=%v", health["scientific_runtime_ready"], err)
	}
	afterCount, err := os.ReadFile(countPath)
	if err != nil || strings.TrimSpace(string(afterCount)) != "2" {
		t.Fatalf("restart reinstalled runtime: count=%q err=%v", afterCount, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("supervisor shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop")
	}
}
