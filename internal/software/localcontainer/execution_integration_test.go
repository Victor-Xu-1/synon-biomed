package localcontainer

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type cancelAtContainerObservation struct {
	commandRunner
	once   sync.Once
	cancel context.CancelFunc
	detach chan struct{}
}

func (r *cancelAtContainerObservation) Run(ctx context.Context, args ...string) (string, string, error) {
	out, diagnostic, err := r.commandRunner.Run(ctx, args...)
	if len(args) > 1 && args[0] == "container" && args[1] == "inspect" && err == nil {
		r.once.Do(func() { r.cancel(); close(r.detach) })
	}
	return out, diagnostic, err
}

// The caller supplies an already-local immutable image with /bin/sh and sleep.
// No image pull, shared container, network, user directory or persistent volume
// is used. Only the uniquely created test container is removed afterwards.
func TestContainerCancellationRealDaemonConfirmsPhysicalExit(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("SYNON_TEST_CONTAINER_IMAGE"))
	if image == "" {
		t.Skip("an explicit local immutable test image is required")
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatal("test image must be an immutable local image ID")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	runner := execDockerRunner{executable: docker, inactivityTimeout: time.Minute}
	ctx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	executionID := "cancellation-acceptance-" + time.Now().UTC().Format("20060102T150405.000000000")
	name := containerName(executionID)
	environmentID, err := environmentName(image, Spec{Image: image, Accelerator: "none", Network: "none"})
	if err != nil {
		t.Fatal(err)
	}
	environment := Environment{Name: environmentID, ProviderID: ProviderID, Status: "ready", ImageID: image}
	out, diagnostic, err := runner.Run(ctx, "create", "--name", name, "--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--label", "com.synonbiomed.managed=true",
		"--label", "com.synonbiomed.execution="+executionID, "--label", "com.synonbiomed.environment="+environment.Name,
		"--entrypoint", "/bin/sh", image, "-c", "exec sleep 60")
	if err != nil {
		t.Fatalf("create isolated test container: %v %s", err, diagnostic)
	}
	id := strings.TrimSpace(out)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) {
		t.Fatal("Docker returned an invalid created-container identity")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, detail, err := runner.Run(cleanup, "rm", "-f", id); err != nil {
			t.Errorf("remove owned test container: %v %s", err, detail)
		}
	})
	if _, diagnostic, err := runner.Run(ctx, "start", id); err != nil {
		t.Fatalf("start test container: %v %s", err, diagnostic)
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	detach := make(chan struct{})
	boundary := &cancelAtContainerObservation{commandRunner: runner, cancel: cancel, detach: detach}
	manager := &Manager{docker: boundary, pollInterval: time.Millisecond}
	result, err := manager.RunExecution(workCtx, detach, environment, ExecutionRequest{
		ExecutionID: executionID, ToolName: "bash", Code: "exec sleep 60", Workspace: t.TempDir(),
	})
	if err != nil || !result.Interrupted || !result.Resumed || result.ContainerID != id || result.FinishedAt.IsZero() {
		t.Fatalf("cancellation receipt=%#v err=%v", result, err)
	}
	observer := &Manager{docker: runner}
	container, found, err := observer.inspectContainer(ctx, name)
	if err != nil || !found || container.ID != id || container.State.Running || container.State.Status != "exited" {
		t.Fatalf("container not physically stopped: found=%t running=%t status=%s err=%v", found, container.State.Running, container.State.Status, err)
	}
}
