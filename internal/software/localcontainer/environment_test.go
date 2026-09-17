package localcontainer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/processsupervisor"
	"synon-go/internal/software"
)

const testImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeHostEnvironmentManager struct {
	environments map[string]kernelruntime.ManagedEnvironment
	created      int
}

func (m *fakeHostEnvironmentManager) ManagedPythonEnvironmentName() string { return "synon-python" }
func (m *fakeHostEnvironmentManager) EnsureManagedPythonEnvironment(context.Context) error {
	return nil
}
func (m *fakeHostEnvironmentManager) InspectManagedEnvironment(_ context.Context, name string) (kernelruntime.ManagedEnvironment, bool, error) {
	environment, found := m.environments[name]
	return environment, found, nil
}
func (m *fakeHostEnvironmentManager) CreateManagedEnvironment(_ context.Context, input kernelruntime.CreateManagedEnvironmentInput) (kernelruntime.ManagedEnvironment, error) {
	if input.SourceEnvironment != "synon-python" || input.Language != "python" {
		return kernelruntime.ManagedEnvironment{}, errors.New("invalid command host request")
	}
	m.created++
	environment := kernelruntime.ManagedEnvironment{Name: input.Name, Language: "python", Status: "ready", Generation: "host-generation"}
	m.environments[input.Name] = environment
	return environment, nil
}

type fakeDockerRunner struct {
	imagePresent bool
	pullLines    []string
	pullCount    int
	nvidia       bool
}

func (r *fakeDockerRunner) Run(_ context.Context, arguments ...string) (string, string, error) {
	joined := strings.Join(arguments, " ")
	switch {
	case joined == "version --format {{.Server.Version}}":
		return "29.4.3\n", "", nil
	case joined == "info --format {{json .Runtimes}}":
		if r.nvidia {
			return `{"runc":{},"nvidia":{}}`, "", nil
		}
		return `{"runc":{}}`, "", nil
	case strings.HasPrefix(joined, "image inspect "):
		if !r.imagePresent {
			return "", "Error: No such image", errors.New("exit status 1")
		}
		encoded, _ := json.Marshal([]dockerImage{{
			ID: testImageID, RepoDigests: []string{"registry.example/science/tool@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, Size: 4096,
		}})
		return string(encoded), "", nil
	default:
		return "", "unexpected command", errors.New("unexpected command")
	}
}

func (r *fakeDockerRunner) RunStreaming(_ context.Context, arguments []string, onLine func(string)) (string, string, error) {
	if !reflect.DeepEqual(arguments, []string{"pull", "registry.example/science/tool:1.0"}) {
		return "", "unexpected pull", errors.New("unexpected pull")
	}
	r.pullCount++
	for _, line := range r.pullLines {
		onLine(line)
	}
	r.imagePresent = true
	return strings.Join(r.pullLines, "\n"), "", nil
}

func TestNormalizeSpecIsProviderGenericAndCredentialFree(t *testing.T) {
	normalized, err := NormalizeSpec(Spec{Image: " registry.example/science/tool:1.0 ", Accelerator: "REQUIRED"})
	if err != nil || normalized.Image != "registry.example/science/tool:1.0" || normalized.Accelerator != "required" || normalized.Network != "egress" {
		t.Fatalf("normalized=%#v err=%v", normalized, err)
	}
	for _, invalid := range []string{"", "https://registry.example/tool", "user:secret@registry.example/tool:1", "--privileged"} {
		if _, err := NormalizeSpec(Spec{Image: invalid}); err == nil {
			t.Fatalf("invalid image %q was admitted", invalid)
		}
	}
}

func TestPreparePullsOncePublishesImmutableHostAndReusesIt(t *testing.T) {
	host := &fakeHostEnvironmentManager{environments: map[string]kernelruntime.ManagedEnvironment{}}
	docker := &fakeDockerRunner{nvidia: true, pullLines: []string{"layer: Downloading 1.5MB/3MB", "layer: Pull complete"}}
	manager, err := newManager(host, filepath.Join(t.TempDir(), "catalog"), docker)
	if err != nil {
		t.Fatal(err)
	}
	first, preflight, err := manager.Prepare(context.Background(), Spec{
		Image: "registry.example/science/tool:1.0", Accelerator: "required",
	}, "operation-1")
	if err != nil || !preflight.Ready || first.Status != "ready" || first.ProviderID != ProviderID ||
		first.ImageID != testImageID || first.HostGeneration != "host-generation" ||
		first.Disposition != software.ProvisionDispositionInstalled || docker.pullCount != 1 || host.created != 1 {
		t.Fatalf("first=%#v preflight=%#v pulls=%d creates=%d err=%v", first, preflight, docker.pullCount, host.created, err)
	}
	second, _, err := manager.Prepare(context.Background(), Spec{
		Image: "registry.example/science/tool:1.0", Accelerator: "required",
	}, "operation-2")
	if err != nil || second.Name != first.Name || second.Disposition != software.ProvisionDispositionReused || docker.pullCount != 1 || host.created != 1 {
		t.Fatalf("second=%#v pulls=%d creates=%d err=%v", second, docker.pullCount, host.created, err)
	}
	resolved, found, err := manager.Resolve(context.Background(), first.Name)
	if err != nil || !found || resolved.Status != "ready" || resolved.ImageID != testImageID {
		t.Fatalf("resolved=%#v found=%v err=%v", resolved, found, err)
	}
}

func TestPreflightRequiresRealNVIDIARuntimeWhenSelected(t *testing.T) {
	host := &fakeHostEnvironmentManager{environments: map[string]kernelruntime.ManagedEnvironment{}}
	manager, err := newManager(host, filepath.Join(t.TempDir(), "catalog"), &fakeDockerRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Preflight(context.Background(), Spec{Image: "science/tool:1", Accelerator: "required"}); err == nil || !strings.Contains(err.Error(), "NVIDIA") {
		t.Fatalf("GPU preflight error=%v", err)
	}
}

func TestDockerProgressUsesObservedBytesOnly(t *testing.T) {
	completed, total := dockerProgressBytes("layer: Downloading [====>] 12.5MB/50MB")
	if completed != 13107200 || total != 52428800 {
		t.Fatalf("progress=%d/%d", completed, total)
	}
	if completed, total := dockerProgressBytes("layer: Waiting"); completed != 0 || total != 0 {
		t.Fatalf("invented progress=%d/%d", completed, total)
	}
}

func TestDockerProgressSplitsCarriageReturnAndNewlineFrames(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(
		"layer: Downloading 1MB/4MB\rlayer: Downloading 2MB/4MB\r\nlayer: Pull complete\n",
	))
	scanner.Split(splitDockerProgressFrames)
	frames := make([]string, 0, 3)
	for scanner.Scan() {
		if frame := strings.TrimSpace(scanner.Text()); frame != "" {
			frames = append(frames, frame)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"layer: Downloading 1MB/4MB",
		"layer: Downloading 2MB/4MB",
		"layer: Pull complete",
	}
	if !reflect.DeepEqual(frames, want) {
		t.Fatalf("frames=%#v want=%#v", frames, want)
	}
}

func TestExecDockerRunnerUsesObservableActivityWatchdog(t *testing.T) {
	switch os.Getenv("SYNON_DOCKER_RUNNER_HELPER") {
	case "stalled":
		time.Sleep(10 * time.Second)
		return
	case "streaming":
		for index := range 4 {
			fmt.Printf("layer: Downloading %dMB/4MB\r", index+1)
			time.Sleep(100 * time.Millisecond)
		}
		return
	}
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	runner := execDockerRunner{executable: os.Args[0], inactivityTimeout: 300 * time.Millisecond}
	t.Setenv("SYNON_DOCKER_RUNNER_HELPER", "stalled")
	_, _, err := runner.Run(context.Background(), "-test.run=^TestExecDockerRunnerUsesObservableActivityWatchdog$")
	var inactivity *processsupervisor.InactivityError
	if !errors.As(err, &inactivity) {
		t.Fatalf("stalled command error=%v", err)
	}

	t.Setenv("SYNON_DOCKER_RUNNER_HELPER", "streaming")
	var lines []string
	_, _, err = runner.RunStreaming(
		context.Background(), []string{"-test.run=^TestExecDockerRunnerUsesObservableActivityWatchdog$"},
		func(line string) { lines = append(lines, line) },
	)
	if err != nil || len(lines) < 4 {
		t.Fatalf("streaming command lines=%#v err=%v", lines, err)
	}
}

func TestDockerCommandOutputIsBoundedWithoutSilentTruncation(t *testing.T) {
	var output boundedDockerOutput
	if n, err := output.Write([]byte("complete-json")); err != nil || n != 13 {
		t.Fatalf("write=%d %v", n, err)
	}
	if _, err := output.Write(make([]byte, 8<<20)); err == nil {
		t.Fatal("unbounded Docker output accepted")
	}
	if output.String() != "complete-json" {
		t.Fatal("failed append corrupted previous output")
	}
}
