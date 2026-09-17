package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"synon-go/internal/vmresources"
)

func TestVMResourcesHTTPReadsAndWritesRealWSLConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".wslconfig")
	if err := os.WriteFile(path, []byte("[wsl2]\nmemory=4GB\nprocessors=2\nlocalhostForwarding=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	controller := vmresources.New(path, vmresources.Limits{
		MaxMemoryGB: 16, HostCPUCount: 8, LaunchedMemoryGB: 4, LaunchedCPUCount: 2,
	})
	app := New(Options{FileRoot: t.TempDir(), VMResources: controller}).Handler()
	current := getProjectControlJSON(t, app, "/api/preferences/vm-resources", http.StatusOK)
	if current["memoryGB"] != float64(4) || current["cpuCount"] != float64(2) ||
		current["maxMemoryGB"] != float64(16) || current["hostCpuCount"] != float64(8) ||
		current["launchedMemoryGB"] != float64(4) || current["launchedCpuCount"] != float64(2) ||
		current["isRestarting"] != false {
		t.Fatalf("VM resources = %#v", current)
	}
	updated := postProjectControlJSON(t, app, http.MethodPut, "/api/preferences/vm-resources", map[string]any{
		"memoryGB": 6, "cpuCount": 4,
	}, http.StatusOK)
	if updated["memoryGB"] != float64(6) || updated["cpuCount"] != float64(4) ||
		updated["launchedMemoryGB"] != float64(4) || updated["launchedCpuCount"] != float64(2) {
		t.Fatalf("updated VM resources = %#v", updated)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "memory=6GB") || !strings.Contains(string(raw), "processors=4") ||
		!strings.Contains(string(raw), "localhostForwarding=true") {
		t.Fatalf("updated .wslconfig = %q", raw)
	}
	postProjectControlJSON(t, app, http.MethodPut, "/api/preferences/vm-resources", map[string]any{
		"memoryGB": 17, "cpuCount": 4,
	}, http.StatusBadRequest)
	alias := getProjectControlJSON(t, app, "/api/go/preferences/vm-resources", http.StatusOK)
	if alias["memoryGB"] != float64(6) || alias["cpuCount"] != float64(4) {
		t.Fatalf("Go VM resource alias = %#v", alias)
	}
	restarted := New(Options{FileRoot: t.TempDir(), VMResources: vmresources.New(path, vmresources.Limits{
		MaxMemoryGB: 16, HostCPUCount: 8, LaunchedMemoryGB: 4, LaunchedCPUCount: 2,
	})}).Handler()
	afterRestart := getProjectControlJSON(t, restarted, "/api/preferences/vm-resources", http.StatusOK)
	if afterRestart["memoryGB"] != float64(6) || afterRestart["cpuCount"] != float64(4) {
		t.Fatalf("VM resources after restart = %#v", afterRestart)
	}
}

func TestVMResourcesHTTPRejectsInvalidAndSerializesConcurrentPairs(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".wslconfig")
	original := "[wsl2]\nmemory=4GB\nprocessors=2\nlocalhostForwarding=true\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	controller := vmresources.New(path, vmresources.Limits{
		MaxMemoryGB: 16, HostCPUCount: 8, LaunchedMemoryGB: 4, LaunchedCPUCount: 2,
	})
	app := New(Options{FileRoot: t.TempDir(), VMResources: controller}).Handler()
	invalid := postProjectControlJSON(t, app, http.MethodPut, "/api/preferences/vm-resources", map[string]any{
		"memoryGB": 4, "cpuCount": 2, "unexpected": true,
	}, http.StatusBadRequest)
	if !strings.Contains(invalid["error"].(string), "unknown field") {
		t.Fatalf("unknown field error = %#v", invalid)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != original {
		t.Fatalf("invalid request mutated config: raw=%q err=%v", raw, err)
	}

	pairs := []setVMResourcesInput{{MemoryGB: 5, CPUCount: 3}, {MemoryGB: 7, CPUCount: 4}}
	requests := make([]*http.Request, 0, len(pairs))
	for _, pair := range pairs {
		payload, err := json.Marshal(pair)
		if err != nil {
			t.Fatal(err)
		}
		request := newLoopbackTestRequest(http.MethodPut, "/api/preferences/vm-resources", bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		requests = append(requests, request)
	}
	start := make(chan struct{})
	statuses := make(chan int, len(requests))
	var wait sync.WaitGroup
	for _, request := range requests {
		wait.Add(1)
		go func(request *http.Request) {
			defer wait.Done()
			<-start
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			statuses <- response.Code
		}(request)
	}
	close(start)
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("concurrent VM resource status = %d", status)
		}
	}
	current := getProjectControlJSON(t, app, "/api/preferences/vm-resources", http.StatusOK)
	validPair := (current["memoryGB"] == float64(5) && current["cpuCount"] == float64(3)) ||
		(current["memoryGB"] == float64(7) && current["cpuCount"] == float64(4))
	if !validPair {
		t.Fatalf("concurrent VM resource pair was torn = %#v", current)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "localhostForwarding=true") {
		t.Fatalf("concurrent update lost unrelated config = %q", raw)
	}
}
