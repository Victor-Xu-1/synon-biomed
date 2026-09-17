package server

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
)

// This opt-in check uses the production API, SQLite store, confined kernel and
// renderer. Only authentication is supplied by the existing local API fixture;
// it never attaches to an installed service, account or browser profile.
func TestKernelExecutionObservationRealBrowser(t *testing.T) {
	if os.Getenv("SYNON_TEST_EXECUTION_OBSERVATION_BROWSER") != "1" {
		t.Skip("opt-in real browser check")
	}
	store, manager, app := newKernelAPITestRuntime(t)
	createKernelAPIProjectAndFrame(t, store, "observation-project", "observation-owner", "observation-frame", "OPERON", "")
	access, found, err := store.GetKernelFrameAccess("observation-frame")
	if err != nil || !found {
		t.Fatalf("frame access: %t %v", found, err)
	}
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "observation-kernel", OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.ID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: "OPERON", Language: "python", Environment: "chem", WorkspaceDir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app)
	defer server.Close()
	_, file, _, _ := runtime.Caller(0)
	frontend := filepath.Join(filepath.Dir(file), "..", "..", "frontend")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "tests/web-e2e/computeExecutionObservation.browser.mjs")
	command.Dir = frontend
	command.Env = append(os.Environ(), "SYNON_OBSERVATION_TEST_API="+server.URL)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("real browser check: %v\n%s", err, output)
	}
	t.Log(string(output))
}
