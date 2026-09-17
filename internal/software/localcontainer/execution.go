package localcontainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrExecutorDetached = errors.New("container executor detached while the workload remains externally owned")

// ErrExecutionOutcomeUnconfirmed retains the durable operation for recovery.
// A control-plane error is not evidence that the externally owned workload ended.
var ErrExecutionOutcomeUnconfirmed = errors.New("container execution terminal outcome is unconfirmed")

type ExecutionRequest struct {
	ExecutionID string
	ToolName    string
	Code        string
	Workspace   string
	WorkingDir  string
}

type ExecutionResult struct {
	ContainerID   string
	ContainerName string
	ImageID       string
	Stdout        string
	Stderr        string
	ExitCode      int
	StartedAt     time.Time
	FinishedAt    time.Time
	Resumed       bool
	Interrupted   bool
}

type dockerContainer struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
}

func (m *Manager) RunExecution(
	ctx context.Context,
	detach <-chan struct{},
	environment Environment,
	input ExecutionRequest,
) (ExecutionResult, error) {
	if m == nil || m.docker == nil {
		return ExecutionResult{}, errors.New("container execution runtime is unavailable")
	}
	request, containerWorkingDir, err := normalizeExecutionRequest(environment, input)
	if err != nil {
		return ExecutionResult{}, err
	}
	name := containerName(request.ExecutionID)
	container, found, err := m.inspectContainer(ctx, name)
	if err != nil {
		return ExecutionResult{}, err
	}
	resumed := found
	if found {
		if err := validateExistingContainer(container, environment, request.ExecutionID); err != nil {
			return ExecutionResult{}, err
		}
	} else {
		arguments := []string{
			"create", "--name", name,
			"--label", "com.synonbiomed.managed=true",
			"--label", "com.synonbiomed.execution=" + request.ExecutionID,
			"--label", "com.synonbiomed.environment=" + environment.Name,
			"--network", dockerNetwork(environment.Network),
			"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
			"--mount", "type=bind,src=" + request.Workspace + ",dst=/workspace",
			"--workdir", containerWorkingDir,
		}
		if environment.Accelerator == "required" {
			arguments = append(arguments, "--gpus", "all")
		}
		switch request.ToolName {
		case "python":
			arguments = append(arguments, "--entrypoint", "python", environment.ImageID, "-c", request.Code)
		case "bash":
			arguments = append(arguments, "--entrypoint", "bash", environment.ImageID, "-lc", request.Code)
		}
		stdout, stderr, createErr := m.docker.Run(ctx, arguments...)
		if createErr != nil {
			return ExecutionResult{}, fmt.Errorf("create managed container: %s", boundedDiagnostic(stderr+"\n"+stdout, createErr))
		}
		container, found, err = m.inspectContainer(ctx, name)
		if err != nil || !found {
			if err == nil {
				err = errors.New("Docker did not publish the managed container")
			}
			return ExecutionResult{}, err
		}
		if err := validateExistingContainer(container, environment, request.ExecutionID); err != nil {
			return ExecutionResult{}, err
		}
	}
	if !container.State.Running && container.State.Status != "exited" && container.State.Status != "dead" {
		stdout, stderr, startErr := m.docker.Run(ctx, "start", name)
		if startErr != nil {
			return ExecutionResult{}, fmt.Errorf("start managed container: %s", boundedDiagnostic(stderr+"\n"+stdout, startErr))
		}
	}

	consecutiveInspectFailures := 0
	pollInterval := m.pollInterval
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	for {
		// Task cancellation takes precedence when shutdown and cancellation are
		// both ready. Shutdown alone must still leave the durable container alive.
		if ctx.Err() != nil {
			return m.finishCancelledExecution(container, name, environment, request.ExecutionID, resumed)
		}
		select {
		case <-detach:
			if ctx.Err() != nil {
				return m.finishCancelledExecution(container, name, environment, request.ExecutionID, resumed)
			}
			return ExecutionResult{ContainerID: container.ID, ContainerName: name, ImageID: environment.ImageID, Resumed: resumed}, ErrExecutorDetached
		case <-ctx.Done():
			return m.finishCancelledExecution(container, name, environment, request.ExecutionID, resumed)
		case <-time.After(pollInterval):
		}
		observed, exists, inspectErr := m.inspectContainer(context.Background(), name)
		if inspectErr != nil {
			consecutiveInspectFailures++
			if consecutiveInspectFailures >= 24 {
				return ExecutionResult{}, fmt.Errorf("observe managed container after repeated Docker failures: %w", inspectErr)
			}
			continue
		}
		consecutiveInspectFailures = 0
		if !exists {
			return ExecutionResult{}, errors.New("managed container disappeared before a terminal receipt")
		}
		container = observed
		if container.State.Running || container.State.Status == "created" || container.State.Status == "restarting" {
			continue
		}
		stdout, stderr := m.containerLogs(context.Background(), name)
		result := executionResult(container, name, environment.ImageID, stdout, stderr, resumed, false)
		if container.State.Status == "dead" {
			return result, errors.New("managed container entered the dead state")
		}
		return result, nil
	}
}

func (m *Manager) finishCancelledExecution(previous dockerContainer, name string, environment Environment, executionID string, resumed bool) (ExecutionResult, error) {
	// This deadline bounds Docker control, not the scientific task's duration.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, stopStderr, stopErr := m.docker.Run(ctx, "stop", "--time", "30", name)
	if stopErr != nil {
		return ExecutionResult{}, errors.Join(ErrExecutionOutcomeUnconfirmed,
			fmt.Errorf("stop cancelled managed container: %s", boundedDiagnostic(stopStderr, stopErr)))
	}
	container, found, err := m.inspectContainer(ctx, name)
	if err != nil || !found || container.ID != previous.ID {
		return ExecutionResult{}, errors.Join(ErrExecutionOutcomeUnconfirmed, err,
			errors.New("cancelled managed container identity could not be verified"))
	}
	if err := validateExistingContainer(container, environment, executionID); err != nil {
		return ExecutionResult{}, errors.Join(ErrExecutionOutcomeUnconfirmed, err)
	}
	if container.State.Running || (container.State.Status != "exited" && container.State.Status != "dead") {
		return ExecutionResult{}, errors.Join(ErrExecutionOutcomeUnconfirmed,
			errors.New("managed container has no verified terminal state after cancellation"))
	}
	stdout, stderr := m.containerLogs(ctx, name)
	return executionResult(container, name, environment.ImageID, stdout, stderr, resumed, true), nil
}

func normalizeExecutionRequest(environment Environment, input ExecutionRequest) (ExecutionRequest, string, error) {
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.ToolName = strings.ToLower(strings.TrimSpace(input.ToolName))
	input.Workspace = filepath.Clean(strings.TrimSpace(input.Workspace))
	workingDir := strings.TrimSpace(input.WorkingDir)
	if workingDir == "" || workingDir == "." {
		workingDir = input.Workspace
	}
	input.WorkingDir = filepath.Clean(workingDir)
	if environment.ProviderID != ProviderID || !IsEnvironmentName(environment.Name) || environment.Status != "ready" ||
		!regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(environment.ImageID) {
		return ExecutionRequest{}, "", errors.New("container environment execution authority is invalid")
	}
	if input.ExecutionID == "" || len(input.ExecutionID) > 512 || strings.ContainsAny(input.ExecutionID, "\x00\r\n") {
		return ExecutionRequest{}, "", errors.New("container execution identity is invalid")
	}
	if input.ToolName != "python" && input.ToolName != "bash" {
		return ExecutionRequest{}, "", errors.New("container execution tool must be python or bash")
	}
	if strings.TrimSpace(input.Code) == "" || len(input.Code) > 100000 || strings.ContainsRune(input.Code, '\x00') {
		return ExecutionRequest{}, "", errors.New("container execution source is invalid")
	}
	if !filepath.IsAbs(input.Workspace) || !filepath.IsAbs(input.WorkingDir) {
		return ExecutionRequest{}, "", errors.New("container workspace authority is invalid")
	}
	relative, err := filepath.Rel(input.Workspace, input.WorkingDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || strings.ContainsAny(input.Workspace, ",\x00\r\n") {
		return ExecutionRequest{}, "", errors.New("container working directory must stay inside the task workspace")
	}
	containerWorkingDir := "/workspace"
	if relative != "." {
		containerWorkingDir += "/" + filepath.ToSlash(relative)
	}
	return input, containerWorkingDir, nil
}

func containerName(executionID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(executionID)))
	return "synon-task-" + hex.EncodeToString(digest[:12])
}

func dockerNetwork(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "none") {
		return "none"
	}
	return "bridge"
}

func (m *Manager) inspectContainer(ctx context.Context, name string) (dockerContainer, bool, error) {
	stdout, stderr, err := m.docker.Run(ctx, "container", "inspect", name)
	if err != nil {
		lower := strings.ToLower(stderr + "\n" + err.Error())
		if strings.Contains(lower, "no such container") || strings.Contains(lower, "not found") {
			return dockerContainer{}, false, nil
		}
		return dockerContainer{}, false, fmt.Errorf("inspect managed container: %s", boundedDiagnostic(stderr, err))
	}
	var containers []dockerContainer
	if err := json.Unmarshal([]byte(stdout), &containers); err != nil || len(containers) != 1 {
		return dockerContainer{}, false, errors.New("Docker container inspection returned an invalid receipt")
	}
	return containers[0], true, nil
}

func validateExistingContainer(container dockerContainer, environment Environment, executionID string) error {
	labels := container.Config.Labels
	if strings.TrimSpace(container.ID) == "" || container.Image != environment.ImageID ||
		labels["com.synonbiomed.managed"] != "true" ||
		labels["com.synonbiomed.execution"] != executionID ||
		labels["com.synonbiomed.environment"] != environment.Name {
		return errors.New("existing managed container conflicts with the durable execution authority")
	}
	return nil
}

func (m *Manager) containerLogs(ctx context.Context, name string) (string, string) {
	stdout, stderr, err := m.docker.Run(ctx, "logs", "--tail", "5000", name)
	if err != nil {
		stderr = strings.TrimSpace(stderr + "\n" + boundedDiagnostic("", err))
	}
	return boundedStream(stdout, 256*1024), boundedStream(stderr, 256*1024)
}

func executionResult(
	container dockerContainer,
	name, imageID, stdout, stderr string,
	resumed, interrupted bool,
) ExecutionResult {
	return ExecutionResult{
		ContainerID: container.ID, ContainerName: name, ImageID: imageID,
		Stdout: stdout, Stderr: stderr, ExitCode: container.State.ExitCode,
		StartedAt: parseDockerTime(container.State.StartedAt), FinishedAt: parseDockerTime(container.State.FinishedAt),
		Resumed: resumed, Interrupted: interrupted,
	}
}

func parseDockerTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func boundedStream(value string, maximum int) string {
	if maximum <= 0 || len(value) <= maximum {
		return value
	}
	return value[len(value)-maximum:]
}
