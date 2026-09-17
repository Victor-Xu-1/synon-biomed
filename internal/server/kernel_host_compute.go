package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const kernelComputeConcurrencyNamespace = "compute-session-concurrency"

func isKernelComputeHostMethod(method string) bool {
	switch method {
	case "host.compute.create", "host.compute.ledger", "host.compute.status", "host.compute.config_get", "host.compute.set_concurrency_limit",
		"host.compute.call_command", "host.compute.submit_job", "host.compute.attach_job", "host.compute.job_result",
		"host.compute.job_cancel", "host.compute.close":
		return true
	default:
		return false
	}
}

func (s *Server) handleKernelComputeHostCall(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	workspaceDir string,
	callID string,
	method string,
	args []any,
	kwargs map[string]any,
) (any, error) {
	if s == nil || s.workspaceStore == nil {
		return nil, kernelruntime.NewHostCallError("unavailable", "compute runtime is unavailable")
	}
	switch method {
	case "host.compute.create":
		target, providerParams, err := kernelComputeCreateInput(args, kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		provider, found, err := s.kernelComputeProvider(access, target)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, kernelruntime.NewHostCallError("not_found", "compute provider is not enabled for this session")
		}
		publicTarget := publicComputeProviderName(provider.Name)
		if provider.Family == "byoc" {
			providerID := strings.TrimPrefix(provider.Name, "byoc:")
			if _, authorityErr := s.agentComputeProviderAuthority(access, providerID, false); authorityErr != nil {
				return nil, kernelruntime.NewHostCallError("not_found", authorityErr.Error())
			}
			settings, settingsFound, settingsErr := s.workspaceStore.GetBYOCSettings(providerID, access.UserID)
			if settingsErr != nil || !settingsFound {
				return nil, kernelruntime.NewHostCallError("not_found", "BYOC provider settings are unavailable")
			}
			providerParams, err = canonicalModalProviderParams(providerParams)
			if err != nil {
				return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
			}
			if _, validateErr := s.normalizeModalJobSpec(map[string]any{"provider_params": providerParams}, settings); validateErr != nil {
				return nil, kernelruntime.NewHostCallError("invalid_arguments", validateErr.Error())
			}
			handleID, handleErr := s.createAgentComputeHandle(access, callID, publicTarget, providerParams)
			if handleErr != nil {
				return nil, kernelruntime.NewHostCallError("storage_error", handleErr.Error())
			}
			return map[string]any{"target": publicTarget, "family": "byoc", "provider_params": providerParams, "handle_id": handleID}, nil
		}
		if provider.Family != "byoc" && len(providerParams) > 0 {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "provider_params are supported only for byoc providers")
		}
		return map[string]any{"target": publicTarget, "family": provider.Family, "provider_params": providerParams}, nil
	case "host.compute.ledger":
		if err := kernelHostExpectNoArguments(args, kwargs, method); err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		return s.kernelComputeLedger(access)
	case "host.compute.status":
		if err := kernelHostExpectNoArguments(args, kwargs, method); err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		return s.kernelComputeStatus(access)
	case "host.compute.config_get":
		providerName := strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 0, "provider")))
		if providerName == "" {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.compute.config_get requires provider")
		}
		projection, err := s.agentComputeProviderConfigProjection(access, providerName)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("not_found", err.Error())
		}
		return projection, nil
	case "host.compute.set_concurrency_limit":
		limit, err := kernelComputeConcurrencyInput(args, kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		return s.kernelSetComputeConcurrency(access, limit)
	case "host.compute.call_command":
		input, err := kernelComputeCommandInput(args, kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		provider, found, err := s.kernelComputeProvider(access, input.Provider)
		if err != nil {
			return nil, err
		}
		if !found || provider.Family != "ssh" {
			return nil, kernelruntime.NewHostCallError("not_found", "SSH compute provider is not enabled for this session")
		}
		return runKernelComputeSSHCommand(ctx, provider, input)
	case "host.compute.attach_job", "host.compute.job_result":
		jobID, err := kernelComputeJobIDInput(args, kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		job, found, err := s.workspaceStore.GetComputeJob(access.UserID, jobID)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("storage_error", "compute job could not be read")
		}
		if !found || job.RootFrameID == nil || *job.RootFrameID != access.Frame.RootFrameID {
			return nil, kernelruntime.NewHostCallError("not_found", "compute job was not found in this task")
		}
		if method == "host.compute.job_result" {
			return s.kernelComputeJobDetailedProjection(access.UserID, job), nil
		}
		return kernelComputeJobProjection(job), nil
	case "host.compute.submit_job":
		if len(args) != 1 || len(kwargs) != 0 {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.compute.submit_job requires one request object")
		}
		input, ok := args[0].(map[string]any)
		if !ok {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.compute.submit_job request must be an object")
		}
		raw, _ := json.Marshal(input)
		digest := sha256.Sum256(raw)
		result, submitErr := s.submitAgentComputeJob(ctx, access, agentruntime.ToolCall{ID: "host-compute-" + hex.EncodeToString(digest[:12])}, input, workspaceDir)
		if submitErr != nil {
			return nil, kernelComputeHostCallFailure(submitErr)
		}
		return result, nil
	case "host.compute.job_cancel":
		jobID, err := kernelComputeJobIDInput(args, kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		job, found, err := s.workspaceStore.GetComputeJob(access.UserID, jobID)
		if err != nil || !found {
			return nil, kernelruntime.NewHostCallError("not_found", "compute job was not found in this task")
		}
		result, cancelErr := s.cancelAgentComputeJob(ctx, access, map[string]any{"job_id": jobID, "provider": job.Provider})
		if cancelErr != nil {
			return nil, kernelComputeHostCallFailure(cancelErr)
		}
		return result, nil
	case "host.compute.close":
		providerName := strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 0, "provider")))
		if providerName == "" {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.compute.close requires provider")
		}
		handleID := strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 1, "handle_id")))
		if handleID != "" {
			return s.closeAgentComputeHandle(ctx, access, providerName, handleID)
		}
		jobs, err := s.workspaceStore.ListComputeJobs(access.UserID, access.Frame.ProjectID)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("storage_error", "compute jobs could not be read")
		}
		cancelled := []string{}
		removedWorkdirs := 0
		for _, job := range jobs {
			if !computeJobMatchesProvider(job, providerName) || job.RootFrameID == nil || *job.RootFrameID != access.Frame.RootFrameID {
				continue
			}
			if isTerminalAgentComputeJobState(job.State) {
				if job.ProviderFamily == "ssh" && boolValue(mapValue(job.HardwareDetails)["managed_ssh"], false) {
					remoteWorkdir := strings.TrimSpace(stringValue(mapValue(job.HardwareDetails)["remote_workdir"]))
					provider, found, getErr := s.workspaceStore.GetComputeProvider(job.Provider, access.UserID)
					if getErr == nil && found && validAgentSSHWorkdir(remoteWorkdir, job.JobID) {
						quoted := shellSingleQuote(remoteWorkdir)
						if result, removeErr := runKernelComputeSSHCommand(ctx, provider, kernelComputeCommandRequest{
							Command: "if [ -d " + quoted + " ]; then rm -rf -- " + quoted + "; printf removed; fi", Intent: "Remove the completed remote job directory", Timeout: 30 * time.Second,
						}); removeErr == nil && strings.TrimSpace(stringValue(mapValue(result)["stdout"])) == "removed" {
							removedWorkdirs++
						}
					}
				}
				continue
			}
			result, cancelErr := s.cancelAgentComputeJob(ctx, access, map[string]any{"job_id": job.JobID, "provider": providerName})
			if cancelErr == nil && boolValue(mapValue(result)["cancelled"], false) {
				cancelled = append(cancelled, job.JobID)
			}
		}
		return map[string]any{"closed": true, "provider": providerName, "cancelled": cancelled, "removed_workdirs": removedWorkdirs}, nil
	default:
		return nil, kernelruntime.NewHostCallError("method_not_allowed", "compute host method is not allowed")
	}
}

func kernelComputeHostCallFailure(err error) error {
	if err == nil {
		return nil
	}
	var hostErr *kernelruntime.HostCallError
	if errors.As(err, &hostErr) {
		return hostErr
	}
	message := strings.TrimSpace(err.Error())
	lower := strings.ToLower(message)
	code := "provider_error"
	switch {
	case strings.Contains(lower, "provider concurrency limit is full"):
		code = "provider_concurrency_full"
	case strings.Contains(lower, "concurrency limit is full"):
		code = "session_concurrency_full"
	case strings.Contains(lower, "not enabled") || strings.Contains(lower, "not found") || strings.Contains(lower, "unavailable in this task"):
		code = "not_found"
	case strings.Contains(lower, "unsupported") || strings.Contains(lower, "currently accepts scheduler"):
		code = "not_supported"
	case strings.Contains(lower, "invalid") || strings.Contains(lower, "requires") || strings.Contains(lower, "exceeds") || strings.Contains(lower, "must be"):
		code = "invalid_resource"
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout"):
		code = "timeout_ceiling"
	}
	return kernelruntime.NewHostCallError(code, message)
}

func (s *Server) kernelComputeProvider(access workspace.KernelFrameAccess, target string) (workspace.ComputeProvider, bool, error) {
	target = strings.TrimSpace(target)
	candidates := []string{target}
	if !strings.Contains(target, ":") {
		candidates = append(candidates, "ssh:"+target, "byoc:"+target)
	}
	var provider workspace.ComputeProvider
	found := false
	for _, candidate := range candidates {
		item, exists, err := s.workspaceStore.GetComputeProvider(candidate, access.UserID)
		if err != nil {
			return workspace.ComputeProvider{}, false, err
		}
		if !exists {
			continue
		}
		if found && item.Name != provider.Name {
			return workspace.ComputeProvider{}, false, errors.New("compute target name is ambiguous across provider families")
		}
		provider, found = item, true
	}
	if !found {
		return workspace.ComputeProvider{}, false, nil
	}
	if !agentComputeProviderExecutable(provider) {
		if provider.Family != "byoc" {
			return workspace.ComputeProvider{}, false, nil
		}
		if _, authorityErr := s.agentComputeProviderAuthority(access, strings.TrimPrefix(provider.Name, "byoc:"), false); authorityErr != nil {
			return workspace.ComputeProvider{}, false, nil
		}
	}
	selected, configured, err := s.workspaceStore.SessionComputeProviderSelection(access.UserID, access.Frame.RootFrameID)
	if err != nil {
		return workspace.ComputeProvider{}, false, err
	}
	if !configured {
		if provider.Family == "byoc" {
			settings, settingsFound, settingsErr := s.workspaceStore.GetBYOCSettings(strings.TrimPrefix(provider.Name, "byoc:"), access.UserID)
			if settingsErr != nil || !settingsFound || !settings.Enabled {
				return workspace.ComputeProvider{}, false, settingsErr
			}
		}
		return provider, true, nil
	}
	for _, name := range selected {
		if name == provider.Name {
			return provider, true, nil
		}
	}
	return workspace.ComputeProvider{}, false, nil
}

func (s *Server) kernelComputeLedger(access workspace.KernelFrameAccess) (map[string]any, error) {
	jobs, err := s.workspaceStore.ListComputeJobs(access.UserID, access.Frame.ProjectID)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("storage_error", "compute ledger could not be read")
	}
	items := make([]any, 0, len(jobs))
	for _, job := range jobs {
		if job.RootFrameID == nil || *job.RootFrameID != access.Frame.RootFrameID {
			continue
		}
		items = append(items, kernelComputeJobProjection(job))
	}
	return map[string]any{"jobs": items}, nil
}

func (s *Server) kernelComputeStatus(access workspace.KernelFrameAccess) (map[string]any, error) {
	ledger, err := s.kernelComputeLedger(access)
	if err != nil {
		return nil, err
	}
	live := 0
	for _, raw := range anySliceValue(ledger["jobs"]) {
		state := strings.TrimSpace(stringValue(mapValue(raw)["state"]))
		if state == workspace.ComputeJobPending || state == workspace.ComputeJobStaging || state == workspace.ComputeJobQueued ||
			state == workspace.ComputeJobRunning || state == workspace.ComputeJobHarvesting {
			live++
		}
	}
	limit := any(nil)
	if s.runtimeStore != nil {
		if entry, found, getErr := s.runtimeStore.Get(kernelComputeConcurrencyNamespace, access.Frame.RootFrameID); getErr == nil && found {
			limit = int(numberValue(mapValue(entry.Value)["max_concurrent"]))
		}
	}
	providers, _ := s.workspaceStore.ListComputeProviders(access.UserID)
	providerCaps := map[string]any{}
	for _, provider := range providers {
		if provider.MaxConcurrentJobs != nil {
			providerCaps[publicComputeProviderName(provider.Name)] = *provider.MaxConcurrentJobs
		}
	}
	return map[string]any{"live": live, "limit": limit, "provider_caps": providerCaps}, nil
}

func (s *Server) kernelSetComputeConcurrency(access workspace.KernelFrameAccess, limit int) (map[string]any, error) {
	if s.runtimeStore == nil {
		return nil, kernelruntime.NewHostCallError("unavailable", "compute concurrency store is unavailable")
	}
	current := 0
	if entry, found, err := s.runtimeStore.Get(kernelComputeConcurrencyNamespace, access.Frame.RootFrameID); err == nil && found {
		current = int(numberValue(mapValue(entry.Value)["max_concurrent"]))
	}
	if access.Frame.ID != access.Frame.RootFrameID && current > 0 && limit > current {
		return nil, kernelruntime.NewHostCallError("permission_denied", "subframes may only lower the session compute limit")
	}
	if _, err := s.runtimeStore.Set(kernelComputeConcurrencyNamespace, access.Frame.RootFrameID, map[string]any{
		"max_concurrent": limit, "updated_at": time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return nil, kernelruntime.NewHostCallError("storage_error", "compute concurrency limit could not be persisted")
	}
	status, err := s.kernelComputeStatus(access)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "concurrency": status}, nil
}

type kernelComputeCommandRequest struct {
	Provider   string
	Command    string
	Intent     string
	Timeout    time.Duration
	LoginShell bool
}

func kernelComputeCreateInput(args []any, kwargs map[string]any) (string, map[string]any, error) {
	target := strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 0, "target")))
	if target == "" {
		return "", nil, errors.New("host.compute.create requires a target")
	}
	params := map[string]any{}
	if raw := kernelHostArgumentValue(args, kwargs, 1, "provider_params"); raw != nil {
		var ok bool
		params, ok = raw.(map[string]any)
		if !ok {
			return "", nil, errors.New("host.compute.create provider_params must be an object")
		}
	}
	return target, copyMapAny(params), nil
}

func kernelComputeConcurrencyInput(args []any, kwargs map[string]any) (int, error) {
	value := kernelHostArgumentValue(args, kwargs, 0, "max_concurrent")
	rawLimit := numberValue(value)
	limit := int(rawLimit)
	if limit < 1 || limit > 128 || int64(limit) != rawLimit {
		return 0, errors.New("host.compute.set_concurrency_limit requires an integer from 1 to 128")
	}
	return limit, nil
}

func kernelComputeCommandInput(args []any, kwargs map[string]any) (kernelComputeCommandRequest, error) {
	request := kernelComputeCommandRequest{
		Provider:   strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 0, "provider"))),
		Command:    strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 1, "command"))),
		Intent:     strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 2, "intent"))),
		LoginShell: boolValue(kernelHostArgumentValue(args, kwargs, 4, "login_shell"), false),
	}
	timeoutSeconds := numberValue(kernelHostArgumentValue(args, kwargs, 3, "timeout_seconds"))
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	if request.Provider == "" || request.Command == "" || request.Intent == "" || timeoutSeconds > 60 {
		return kernelComputeCommandRequest{}, errors.New("host.compute.call_command requires provider, command, intent, and timeout_seconds <= 60")
	}
	request.Timeout = time.Duration(timeoutSeconds) * time.Second
	return request, nil
}

func kernelComputeJobIDInput(args []any, kwargs map[string]any) (string, error) {
	jobID := strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 0, "job_id")))
	if !computeJobIDPattern.MatchString(jobID) || jobID == "." || jobID == ".." {
		return "", errors.New("compute job id is invalid")
	}
	return jobID, nil
}

func kernelHostArgumentValue(args []any, kwargs map[string]any, index int, key string) any {
	if index >= 0 && index < len(args) {
		return args[index]
	}
	return kwargs[key]
}

func runKernelComputeSSHCommand(ctx context.Context, provider workspace.ComputeProvider, input kernelComputeCommandRequest) (map[string]any, error) {
	alias := strings.TrimPrefix(provider.Name, "ssh:")
	if alias == "" || strings.HasPrefix(alias, "-") || strings.ContainsAny(alias, "\x00\r\n") {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "SSH provider alias is invalid")
	}
	runCtx, cancel := context.WithTimeout(ctx, input.Timeout)
	defer cancel()
	arguments := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}
	if user := strings.TrimSpace(stringValue(provider.SSHOverrides["user"])); user != "" {
		arguments = append(arguments, "-l", user)
	}
	if port := int(numberValue(provider.SSHOverrides["port"])); port > 0 {
		arguments = append(arguments, "-p", strconv.Itoa(port))
	}
	if identity := strings.TrimSpace(stringValue(provider.SSHOverrides["identityFile"])); identity != "" {
		arguments = append(arguments, "-i", identity)
	}
	remoteCommand := input.Command
	if input.LoginShell {
		remoteCommand = "bash -lc " + shellSingleQuote(input.Command)
	}
	arguments = append(arguments, alias, remoteCommand)
	command := exec.CommandContext(runCtx, "ssh", arguments...)
	stdout, stderr := &boundedComputeBuffer{limit: 64 << 10}, &boundedComputeBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	started := time.Now()
	err := command.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, kernelruntime.NewHostCallError("timeout", "SSH command timed out")
		} else {
			return nil, kernelruntime.NewHostCallError("provider_error", "SSH command could not be started")
		}
	}
	return map[string]any{
		"stdout": stdout.String(), "stderr": stderr.String(), "exit_code": exitCode,
		"stdout_truncated": stdout.truncated, "stderr_truncated": stderr.truncated,
		"wall_s": time.Since(started).Seconds(), "intent": input.Intent,
	}, nil
}

type boundedComputeBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedComputeBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || original > 0
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(value)
	return original, nil
}

func (b *boundedComputeBuffer) String() string { return b.buffer.String() }

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func kernelComputeJobProjection(job workspace.ComputeJob) map[string]any {
	result := map[string]any{
		"job_id": job.JobID, "target": publicComputeProviderName(job.Provider), "state": job.State, "status": job.State,
		"started_at": job.StartedAt.UTC().Format(time.RFC3339Nano),
	}
	if job.EndedAtISO != nil {
		result["ended_at"] = *job.EndedAtISO
	}
	if job.ExternalID != nil {
		result["external_id"] = *job.ExternalID
	}
	if job.ErrorKind != nil {
		result["error_kind"] = *job.ErrorKind
	}
	return result
}

func (s *Server) kernelComputeJobDetailedProjection(userID string, job workspace.ComputeJob) map[string]any {
	projection := kernelComputeJobProjection(job)
	if s == nil || s.workspaceStore == nil {
		return projection
	}
	if result, found, err := s.workspaceStore.GetComputeJobResult(userID, job.JobID); err == nil && found {
		for key, value := range result {
			projection[key] = value
		}
	}
	for _, stream := range []string{"stdout", "stderr"} {
		if log, found, err := s.workspaceStore.GetComputeJobLog(userID, job.JobID, stream, 64<<10); err == nil && found {
			projection[stream+"_tail"] = log.Text
		}
	}
	return projection
}
