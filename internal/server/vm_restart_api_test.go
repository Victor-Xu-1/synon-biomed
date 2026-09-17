package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/vmresources"
	"synon-go/internal/vmrestart"
)

func TestVMRestartAPIReportsPendingAndCompletedLifecycle(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "synon-go")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	launched := false
	manager, err := vmrestart.New(vmrestart.Options{
		HomeDir: root, Executable: executable, WorkingDirectory: root,
		UnitDirectory: filepath.Join(root, "units"),
		Distro:        "Ubuntu", User: "victor_1", ServiceName: "synon-go.service",
		Environment: map[string]string{"SYNON_HOME": root},
		RunCommand:  func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		LaunchHost: func(context.Context, vmrestart.HostRestartRequest) error {
			launched = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	controller := vmresources.New(filepath.Join(root, ".wslconfig"), vmresources.Limits{
		MaxMemoryGB: 16, HostCPUCount: 8, LaunchedMemoryGB: 4, LaunchedCPUCount: 2,
	})
	app := New(Options{VMResources: controller, VMRestart: manager}).Handler()

	restart := httptest.NewRecorder()
	app.ServeHTTP(restart, newLoopbackTestRequest(http.MethodPost, "/api/preferences/vm-resources/restart", nil))
	if restart.Code != http.StatusAccepted || !launched || !strings.Contains(restart.Body.String(), "\"state\":\"pending\"") ||
		strings.Contains(restart.Body.String(), "completedAt") ||
		!strings.Contains(restart.Body.String(), "managed Synon Biomed service") ||
		strings.Contains(restart.Body.String(), "managed Synon Go service") {
		t.Fatalf("restart = %d launched=%v body=%s", restart.Code, launched, restart.Body.String())
	}
	resources := httptest.NewRecorder()
	app.ServeHTTP(resources, newLoopbackTestRequest(http.MethodGet, "/api/preferences/vm-resources", nil))
	if resources.Code != http.StatusOK || !strings.Contains(resources.Body.String(), "\"isRestarting\":true") {
		t.Fatalf("pending resources = %d: %s", resources.Code, resources.Body.String())
	}
	status := httptest.NewRecorder()
	app.ServeHTTP(status, newLoopbackTestRequest(http.MethodGet, "/api/go/preferences/vm-resources/restart", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), "\"state\":\"pending\"") {
		t.Fatalf("pending status = %d: %s", status.Code, status.Body.String())
	}
	if err := manager.MarkRuntimeStarted(); err != nil {
		t.Fatal(err)
	}
	completed := httptest.NewRecorder()
	app.ServeHTTP(completed, newLoopbackTestRequest(http.MethodGet, "/api/go/preferences/vm-resources", nil))
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), "\"isRestarting\":false") {
		t.Fatalf("completed resources = %d: %s", completed.Code, completed.Body.String())
	}
}
