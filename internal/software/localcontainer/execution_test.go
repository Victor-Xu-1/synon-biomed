package localcontainer

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
)

type executionDockerRunner struct {
	mu            sync.Mutex
	created       bool
	started       bool
	inspectCount  int
	createArgs    []string
	executionID   string
	environment   string
	imageID       string
	remainRunning bool
	stopCount     int
}

func (r *executionDockerRunner) Run(_ context.Context, arguments ...string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(arguments) == 0 {
		return "", "missing command", errors.New("missing command")
	}
	switch arguments[0] {
	case "container":
		if len(arguments) != 3 || arguments[1] != "inspect" {
			return "", "unexpected inspect", errors.New("unexpected inspect")
		}
		if !r.created {
			return "", "Error: No such container", errors.New("exit status 1")
		}
		r.inspectCount++
		status := "created"
		running := false
		exitCode := 0
		if r.started {
			status, running = "running", true
			if !r.remainRunning && r.inspectCount >= 3 {
				status, running, exitCode = "exited", false, 0
			}
		}
		container := dockerContainer{ID: "container-id", Name: "/managed", Image: r.imageID}
		container.Config.Labels = map[string]string{
			"com.synonbiomed.managed": "true", "com.synonbiomed.execution": r.executionID,
			"com.synonbiomed.environment": r.environment,
		}
		container.State.Status, container.State.Running, container.State.ExitCode = status, running, exitCode
		container.State.StartedAt = "2026-09-03T00:00:00Z"
		container.State.FinishedAt = "2026-09-03T00:01:00Z"
		encoded, _ := json.Marshal([]dockerContainer{container})
		return string(encoded), "", nil
	case "create":
		r.created = true
		r.createArgs = append([]string(nil), arguments...)
		return "container-id\n", "", nil
	case "start":
		r.started = true
		return "managed\n", "", nil
	case "logs":
		return "scientific output\n", "diagnostic output\n", nil
	case "stop":
		r.stopCount++
		r.started = false
		return "managed\n", "", nil
	default:
		return "", "unexpected command", errors.New("unexpected command")
	}
}

func (r *executionDockerRunner) RunStreaming(context.Context, []string, func(string)) (string, string, error) {
	return "", "", errors.New("unexpected streaming command")
}

func testContainerEnvironment(t *testing.T, docker *executionDockerRunner) (*Manager, Environment) {
	t.Helper()
	host := &fakeHostEnvironmentManager{environments: map[string]kernelruntime.ManagedEnvironment{}}
	manager, err := newManager(host, filepath.Join(t.TempDir(), "catalog"), docker)
	if err != nil {
		t.Fatal(err)
	}
	manager.pollInterval = time.Millisecond
	spec := Spec{Image: "science/tool:1", Accelerator: "required", Network: "egress"}
	name, err := environmentName(testImageID, spec)
	if err != nil {
		t.Fatal(err)
	}
	environment := Environment{
		Name: name, ProviderID: ProviderID, RequestedImage: spec.Image, ResolvedImage: testImageID,
		ImageID: testImageID, Accelerator: spec.Accelerator, Network: spec.Network,
		HostEnvironment: name, HostGeneration: "host-generation", Status: "ready",
	}
	docker.executionID, docker.environment, docker.imageID = "execution-1", name, testImageID
	return manager, environment
}

func TestRunExecutionCreatesPinnedPersistentContainerAndCollectsLogs(t *testing.T) {
	docker := &executionDockerRunner{}
	manager, environment := testContainerEnvironment(t, docker)
	workspace := t.TempDir()
	result, err := manager.RunExecution(context.Background(), make(chan struct{}), environment, ExecutionRequest{
		ExecutionID: "execution-1", ToolName: "python", Code: "print(42)",
		Workspace: workspace, WorkingDir: workspace,
	})
	if err != nil || result.ExitCode != 0 || result.Stdout != "scientific output\n" || result.Resumed || result.ImageID != testImageID {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	joined := strings.Join(docker.createArgs, "\x00")
	for _, required := range []string{"--gpus\x00all", "--network\x00bridge", "--entrypoint\x00python", testImageID + "\x00-c\x00print(42)"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("create args missing %q: %#v", required, docker.createArgs)
		}
	}
	if strings.Contains(joined, "--rm") {
		t.Fatalf("managed container was made ephemeral: %#v", docker.createArgs)
	}
}

func TestNormalizeExecutionDefaultsEmptyWorkingDirectoryToTaskWorkspace(t *testing.T) {
	docker := &executionDockerRunner{}
	_, environment := testContainerEnvironment(t, docker)
	workspace := t.TempDir()
	request, containerWorkingDir, err := normalizeExecutionRequest(environment, ExecutionRequest{
		ExecutionID: "execution-1", ToolName: "bash", Code: "pwd", Workspace: workspace,
	})
	if err != nil || request.WorkingDir != workspace || containerWorkingDir != "/workspace" {
		t.Fatalf("request=%#v containerWorkingDir=%q err=%v", request, containerWorkingDir, err)
	}
}

func TestRunExecutionResumesExistingContainerWithoutCreatingAnother(t *testing.T) {
	docker := &executionDockerRunner{created: true, started: true, inspectCount: 1}
	manager, environment := testContainerEnvironment(t, docker)
	workspace := t.TempDir()
	result, err := manager.RunExecution(context.Background(), make(chan struct{}), environment, ExecutionRequest{
		ExecutionID: "execution-1", ToolName: "bash", Code: "echo ready",
		Workspace: workspace, WorkingDir: workspace,
	})
	if err != nil || !result.Resumed || docker.createArgs != nil {
		t.Fatalf("result=%#v create=%#v err=%v", result, docker.createArgs, err)
	}
}

func TestRunExecutionDetachPreservesLiveContainer(t *testing.T) {
	docker := &executionDockerRunner{remainRunning: true}
	manager, environment := testContainerEnvironment(t, docker)
	workspace := t.TempDir()
	detach := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := manager.RunExecution(context.Background(), detach, environment, ExecutionRequest{
			ExecutionID: "execution-1", ToolName: "python", Code: "while True: pass",
			Workspace: workspace, WorkingDir: workspace,
		})
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	close(detach)
	select {
	case err := <-done:
		if !errors.Is(err, ErrExecutorDetached) || docker.stopCount != 0 {
			t.Fatalf("detach err=%v stopCount=%d", err, docker.stopCount)
		}
	case <-time.After(time.Second):
		t.Fatal("container execution did not detach")
	}
}
