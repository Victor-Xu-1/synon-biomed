package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/kernelcontract"
	"synon-go/internal/networkpolicy"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) agentKernelEgressPolicy(publicName, frameID string) ([]string, []string, string, string, error) {
	if s == nil || publicName == "repl" {
		return nil, nil, "", "", nil
	}
	allowed := append([]string(nil), s.configAllowedDomains...)
	if strings.TrimSpace(frameID) != "" {
		mode, err := s.webSessionApprovalMode(frameID)
		if err != nil {
			return nil, nil, "", "", fmt.Errorf("load conversation network mode: %w", err)
		}
		if mode == "allow" {
			allowed = append(allowed, networkpolicy.PublicWildcard)
		}
	}
	if s.settingsStore != nil {
		userAllowed, err := s.loadAllowedDomains()
		if err != nil {
			return nil, nil, "", "", fmt.Errorf("load kernel network grants: %w", err)
		}
		allowed = append(allowed, userAllowed...)
	}
	allowed, err := networkpolicy.NormalizePatterns(allowed, maximumUserAllowedDomains+65)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("normalize kernel network grants: %w", err)
	}
	if len(allowed) == 0 {
		return nil, nil, "", "", nil
	}
	denied := networkpolicy.EffectiveDeniedPatterns(allowed, s.configDeniedDomains)
	caBundle := ""
	proxy := s.configNetworkProxy
	if s.mcpX509Posture != nil {
		posture := s.mcpX509Posture()
		caBundle = strings.TrimSpace(posture.CABundle)
		if proxy == "" {
			proxy = strings.TrimSpace(posture.ProxyURL)
		}
	}
	return allowed, denied, caBundle, proxy, nil
}

// awaitAgentKernelExecutionStart closes the ordering gap between the worker's
// start and terminal channels. managedExecution.finish closes Started before
// publishing Done when a worker exits before startup. A non-blocking Done read
// in that window loses the exact terminal outcome and strands the durable
// operation in started, so a closed start channel must join the terminal handoff.
func awaitAgentKernelExecutionStart(
	ctx context.Context,
	started <-chan kernelruntime.ExecutionStarted,
	done <-chan kernelruntime.ExecutionOutcome,
) (kernelruntime.ExecutionStarted, *kernelruntime.ExecutionOutcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if started == nil || done == nil {
		return kernelruntime.ExecutionStarted{}, nil, errors.New("kernel execution lifecycle channels are unavailable")
	}
	select {
	case execution, ok := <-started:
		if ok && execution.ExecID != "" {
			return execution, nil, nil
		}
		select {
		case outcome, doneOK := <-done:
			if !doneOK {
				return kernelruntime.ExecutionStarted{}, nil, errors.New("kernel execution closed without an outcome")
			}
			return kernelruntime.ExecutionStarted{}, &outcome, nil
		case <-ctx.Done():
			return kernelruntime.ExecutionStarted{}, nil, agentKernelContextCause(ctx)
		}
	case outcome, ok := <-done:
		if !ok {
			return kernelruntime.ExecutionStarted{}, nil, errors.New("kernel execution closed without an outcome")
		}
		var execution kernelruntime.ExecutionStarted
		select {
		case observed, startedOK := <-started:
			if startedOK && observed.ExecID != "" {
				execution = observed
			}
		default:
		}
		return execution, &outcome, nil
	case <-ctx.Done():
		return kernelruntime.ExecutionStarted{}, nil, agentKernelContextCause(ctx)
	}
}

func agentKernelSyntheticExecutionStart(
	request kernelruntime.SubmitRequest,
	outcome kernelruntime.ExecutionOutcome,
) kernelruntime.ExecutionStarted {
	startedAt := outcome.StartedAt.UTC()
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return kernelruntime.ExecutionStarted{
		ExecID: request.ExecID, ToolUseID: request.ToolUseID,
		KernelID: request.KernelID, FrameID: request.FrameID,
		Language: request.Language, Environment: request.Environment,
		KernelKind: request.KernelKind, Code: request.Code,
		Origin: request.Origin, StartedAt: startedAt,
	}
}

func agentKernelContextCause(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

func agentKernelCallerRequiresExecutionStop(ctx context.Context) bool {
	cause := agentKernelContextCause(ctx)
	return errors.Is(cause, ErrGenerationStopped) || errors.Is(cause, ErrRuntimeDraining)
}

func agentKernelSystemPythonEnvironment(environment string) bool {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "python", "system", "repl", "operon":
		return true
	default:
		return false
	}
}

func agentKernelUsesDetachedExecution(publicName string, background bool) bool {
	if background {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(publicName)) {
	case "python", "r", "bash", softwareRuntimeToolName:
		return true
	default:
		return false
	}
}

func agentKernelConfinementSHA256(spec kernelruntime.SessionSpec) (string, error) {
	payload, err := json.Marshal(struct {
		WorkspaceDir   string                      `json:"workspace_dir"`
		Mounts         []kernelruntime.WorkerMount `json:"mounts"`
		ProtectedPaths []string                    `json:"protected_paths"`
	}{WorkspaceDir: spec.WorkspaceDir, Mounts: spec.Mounts, ProtectedPaths: spec.ProtectedPaths})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Server) replayAgentKernelOperationResult(
	operation workspace.KernelLocalOperation,
) (map[string]any, error) {
	if s == nil || s.workspaceStore == nil || operation.ExecutionLogID == "" {
		return nil, errors.New("kernel local operation result authority is unavailable")
	}
	record, found, err := s.workspaceStore.GetExecutionLog(operation.FrameID, operation.ExecutionLogID)
	if err != nil {
		return nil, err
	}
	if !found || record.ID != operation.ExecutionID || record.FrameID != operation.FrameID ||
		record.KernelID != operation.KernelID || record.CondaEnv != operation.Environment {
		return nil, errors.New("kernel local operation execution log conflicts with durable state")
	}
	if operation.Tool == softwareRuntimeToolName {
		rawResult := map[string]any{
			"stdout": record.Stdout, "stderr": record.Stderr, "exit_status": record.ExitStatus,
			"exec_id": record.ID, "kernel_id": record.KernelID, "cell_index": record.CellIndex,
		}
		if record.FilesRead != nil {
			rawResult["input_artifacts"] = record.FilesRead
		}
		visible, err := softwareRuntimeVisibleResult(operation.InputJSON, operation.Environment, "", record.Source, rawResult)
		if err != nil {
			return nil, err
		}
		if operation.State == workspace.KernelLocalOperationStateCancelled {
			normalizeCancelledAgentKernelTerminalResult(visible, operation.Tool)
		}
		return visible, nil
	}
	if operation.Tool == "r" {
		visible := map[string]any{"stdout": record.Stdout, "stderr": record.Stderr, "exit_code": 0}
		if record.ExitStatus != "ok" {
			visible["exit_code"] = 1
		}
		if record.ExitStatus == "cancelled" {
			visible["cancelled"] = true
		}
		return visible, nil
	}
	result := map[string]any{
		"ok": record.ExitStatus == "ok", "exec_id": record.ID, "tool_use_id": operation.ToolCallID,
		"kernel_id": record.KernelID, "kernel_kind": record.KernelKind, "reused": true,
		"stdout": record.Stdout, "stderr": record.Stderr, "exit_status": record.ExitStatus,
		"cell_index": record.CellIndex, "files_written": record.FilesWritten,
		"environment": record.CondaEnv,
	}
	if record.FilesRead != nil {
		result["input_artifacts"] = record.FilesRead
	}
	if code := agentKernelEnvironmentFailureCode(record.ExitStatus, record.Stderr); code != "" {
		applyAgentKernelFailureRecovery(result, code)
	} else if code := agentKernelExecutionFailureCode("python", record.ExitStatus, record.Stderr); code != "" {
		applyAgentKernelFailureRecovery(result, code)
	}
	return result, nil
}

func (s *Server) agentKernelForegroundWaitBudget(rootFrameID string) time.Duration {
	budget := defaultAgentKernelForegroundWaitTimeout
	if s != nil && s.agentKernelForegroundWaitTimeout > 0 {
		budget = s.agentKernelForegroundWaitTimeout
	}
	if s == nil || s.sessionStore == nil {
		return budget
	}
	session, found, err := s.sessionStore.Get(strings.TrimSpace(rootFrameID))
	if err != nil || !found {
		return budget
	}
	config, _ := session.Orchestration["sessionConfig"].(map[string]any)
	raw, present := config["async_local_exec_wallclock_cap_s"]
	if !present || raw == nil {
		return budget
	}
	seconds, valid := agentKernelNonnegativeInteger(raw)
	if !valid || seconds > maxAgentKernelForegroundWaitSeconds {
		return budget
	}
	return time.Duration(seconds) * time.Second
}

func agentKernelNonnegativeInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), typed >= 0
	case int64:
		return typed, typed >= 0
	case int32:
		return int64(typed), typed >= 0
	case float64:
		if typed < 0 || typed > float64(maxAgentKernelForegroundWaitSeconds) || typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	default:
		return 0, false
	}
}

func (s *Server) authorizeAgentKernelWorkingDir(userID, workspaceDir, requested string) (string, error) {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" || !filepath.IsAbs(workspaceDir) {
		return "", errors.New("kernel workspace authority is unavailable")
	}
	workspaceDir, err := canonicalOrCreateAgentWorkspaceDirectory(workspaceDir, 0o700)
	if err != nil {
		return "", errors.New("kernel workspace authority is unavailable")
	}
	requested = strings.TrimSpace(requested)
	if agentKernelLegacyWorkspaceAlias(requested) {
		if _, statErr := os.Stat(strings.TrimSpace(requested)); errors.Is(statErr, os.ErrNotExist) {
			requested = workspaceDir
		}
	}
	if !filepath.IsAbs(requested) {
		cleaned := filepath.Clean(requested)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return "", errors.New("working_dir is outside the authorized workspace")
		}
		requested = filepath.Join(workspaceDir, cleaned)
	}
	if hostPathWithin(workspaceDir, requested) {
		created, createErr := canonicalOrCreateAgentWorkspaceDirectory(requested, 0o700)
		if createErr == nil && hostPathWithin(workspaceDir, created) {
			return created, nil
		}
	}
	requested, err = canonicalHostDirectory(requested)
	if err != nil {
		return "", errors.New("working_dir must be an existing authorized directory")
	}
	if hostPathWithin(workspaceDir, requested) {
		return requested, nil
	}
	if s == nil || s.settingsStore == nil {
		return "", errors.New("working_dir is outside the authorized workspace")
	}
	grants, err := s.loadHostGrants(userID)
	if err != nil {
		return "", errors.New("working_dir authorization could not be verified")
	}
	for _, grant := range grants {
		root, resolveErr := canonicalHostDirectory(grant.Path)
		if resolveErr == nil && hostPathWithin(root, requested) {
			return requested, nil
		}
	}
	return "", errors.New("working_dir is outside the authorized workspace")
}

func agentKernelLegacyWorkspaceAlias(requested string) bool {
	return kernelcontract.LegacyWorkspaceAlias(requested)
}

func (s *Server) agentKernelConfinementMounts(userID, workspaceDir string, protectedPaths []string) ([]kernelruntime.WorkerMount, error) {
	workspaceDir, err := canonicalOrCreateAgentWorkspaceDirectory(strings.TrimSpace(workspaceDir), 0o700)
	if err != nil {
		return nil, errors.New("kernel workspace authority is unavailable")
	}
	for _, protected := range protectedPaths {
		if hostPathWithin(protected, workspaceDir) || hostPathWithin(workspaceDir, protected) {
			return nil, errors.New("kernel workspace overlaps protected application data")
		}
	}
	mounts := []kernelruntime.WorkerMount{}
	trustedSkillRoots := []string{}
	if s != nil {
		for _, candidate := range s.skillDirectories {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			if info, statErr := os.Stat(candidate); errors.Is(statErr, os.ErrNotExist) {
				continue
			} else if statErr != nil || !info.IsDir() {
				return nil, errors.New("trusted Skill runtime path could not be verified")
			}
			root, resolveErr := canonicalHostDirectory(candidate)
			if resolveErr != nil {
				return nil, errors.New("trusted Skill runtime path could not be verified")
			}
			if hostPathWithin(workspaceDir, root) || hostPathWithin(root, workspaceDir) {
				continue
			}
			trustedSkillRoots = append(trustedSkillRoots, root)
		}
	}
	sort.Slice(trustedSkillRoots, func(left, right int) bool {
		if len(trustedSkillRoots[left]) != len(trustedSkillRoots[right]) {
			return len(trustedSkillRoots[left]) < len(trustedSkillRoots[right])
		}
		return trustedSkillRoots[left] < trustedSkillRoots[right]
	})
	for _, root := range trustedSkillRoots {
		covered := false
		for _, mount := range mounts {
			if hostPathWithin(mount.Path, root) {
				covered = true
				break
			}
		}
		if !covered {
			mounts = append(mounts, kernelruntime.TrustedReadOnlyDirectoryMount(root))
		}
	}
	if s == nil || s.settingsStore == nil {
		return mounts, nil
	}
	grants, err := s.loadHostGrants(userID)
	if err != nil {
		return nil, errors.New("working_dir authorization could not be verified")
	}
	for _, grant := range grants {
		root, resolveErr := canonicalHostDirectory(grant.Path)
		if resolveErr != nil {
			return nil, errors.New("working_dir authorization could not be verified")
		}
		if err := kernelruntime.ValidateHostMountPath(root); err != nil {
			return nil, err
		}
		if hostPathWithin(workspaceDir, root) || hostPathWithin(root, workspaceDir) {
			continue
		}
		for _, protected := range protectedPaths {
			if hostPathWithin(protected, root) || hostPathWithin(root, protected) {
				return nil, errors.New("host grant overlaps protected application data")
			}
		}
		for _, skillRoot := range trustedSkillRoots {
			if hostPathWithin(skillRoot, root) || hostPathWithin(root, skillRoot) {
				return nil, errors.New("host grant overlaps trusted Skill runtime data")
			}
		}
		mount := kernelruntime.WorkerMount{Path: root}
		switch grant.Mode {
		case "read":
		case "read_write":
			mount.Writable = true
		default:
			return nil, errors.New("working_dir authorization could not be verified")
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

func (s *Server) agentKernelProtectedPaths() ([]string, error) {
	if s == nil {
		return nil, errors.New("kernel protected paths are unavailable")
	}
	fileRoot := strings.TrimSpace(s.fileRoot)
	canonicalFileRoot := ""
	if fileRoot != "" {
		var err error
		canonicalFileRoot, err = canonicalHostDirectory(fileRoot)
		if err != nil {
			return nil, errors.New("kernel protected paths could not be verified")
		}
	}
	managedTargets := map[string]string{}
	for _, managed := range []string{s.condaHome, s.condaEnvsPath} {
		managed = strings.TrimSpace(managed)
		if managed == "" {
			continue
		}
		if _, err := os.Stat(managed); err == nil {
			resolved, resolveErr := canonicalHostDirectory(managed)
			if resolveErr != nil {
				return nil, errors.New("kernel protected paths could not be verified")
			}
			managedTargets[managed] = resolved
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("kernel protected paths could not be verified")
		}
		absolute, err := filepath.Abs(managed)
		if err != nil || canonicalFileRoot == "" {
			return nil, errors.New("kernel protected paths could not be verified")
		}
		target, err := canonicalAgentWorkspaceDirectoryTarget(absolute)
		if err != nil || !hostPathWithin(canonicalFileRoot, target) {
			return nil, errors.New("kernel protected paths could not be verified")
		}
		managedTargets[managed] = target
	}
	seen := map[string]bool{}
	result := []string{}
	for _, candidate := range []string{s.fileRoot, s.runtimeAssetsDir, s.condaHome, s.condaEnvsPath} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		resolved := managedTargets[candidate]
		if resolved == "" {
			var err error
			resolved, err = canonicalHostDirectory(candidate)
			if err != nil {
				return nil, errors.New("kernel protected paths could not be verified")
			}
		}
		if !seen[resolved] {
			seen[resolved] = true
			result = append(result, resolved)
		}
	}
	sort.Strings(result)
	return result, nil
}

func (s *Server) ensureAgentKernelManagedDirectories() error {
	if s == nil {
		return errors.New("kernel managed directories are unavailable")
	}
	fileRoot := strings.TrimSpace(s.fileRoot)
	canonicalFileRoot, err := canonicalHostDirectory(fileRoot)
	if err != nil {
		return errors.New("kernel managed directories could not be verified")
	}
	for _, managed := range []string{s.condaHome, s.condaEnvsPath} {
		managed = strings.TrimSpace(managed)
		if managed == "" {
			continue
		}
		target, targetErr := canonicalAgentWorkspaceDirectoryTarget(managed)
		if targetErr != nil || !hostPathWithin(canonicalFileRoot, target) {
			return errors.New("kernel managed directories could not be verified")
		}
		resolved, ensureErr := secureEnsureAgentWorkspaceDirectory(target, 0o700)
		if ensureErr != nil || filepath.Clean(resolved) != filepath.Clean(target) || !hostPathWithin(canonicalFileRoot, resolved) {
			return errors.New("kernel managed directories could not be verified")
		}
	}
	return nil
}

func optionalKernelBoolean(input map[string]any, key string) (bool, error) {
	value, found := input[key]
	if !found {
		return false, nil
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return result, nil
}
