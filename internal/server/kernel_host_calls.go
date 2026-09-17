package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

var kernelHostMethods = []string{
	"mcp",
	"mcp.catalog",
	"host.routine.configure",
	"host.routine.status",
	"host.routine.done",
	"host.current_model",
	"host.list_models",
	"host.llm",
	"host.llm_batch",
	"host.delegate",
	"host.collect",
	"host.children",
	"host.delegation_stats",
	"host.stop_child",
	"host.send_message",
	"host.artifacts",
	"host.artifacts.rename",
	"host.artifacts.delete",
	"host.artifact_path",
	"host.lineage",
	"host.lineage_graph",
	"host.frames",
	"host.mcp.list",
	"host.mcp.install",
	"host.mcp.authorize",
	"host.mcp.remove",
	"host.app_tool",
	"host.app_tools_list",
	"host.skills.list",
	"host.skills.read",
	"host.skills.edit",
	"host.skills.install",
	"host.skills.publish",
	"host.skills.delete",
	"host.agents.list",
	"host.agents.create",
	"host.agents.update",
	"host.agents.delete",
	"host.agents.attach_skill",
	"host.agents.detach_skill",
	"host.agents.attach_connector",
	"host.agents.detach_connector",
	"host.agents.list_connectors",
	"host.agents.switch",
	"host.archive.search",
	"host.archive.page",
	"host.capabilities",
	"host.credentials.list",
	"host.credentials.get",
	"host.credentials.request",
	"host.exec_peek",
	"host.exec_interrupt",
	"host.findings",
	"host.findings.mark_addressed",
	"host.get_local_compute_stats",
	"host.get_user_email",
	"host.query",
	"host.query.schema",
	"host.model_endpoints.free_port",
	"host.model_endpoints.register",
	"host.reasoning_model",
	"host.submit_output",
	"host.compute.create",
	"host.compute.ledger",
	"host.compute.status",
	"host.compute.config_get",
	"host.compute.set_concurrency_limit",
	"host.compute.call_command",
	"host.compute.submit_job",
	"host.compute.attach_job",
	"host.compute.job_result",
	"host.compute.job_cancel",
	"host.compute.close",
}

func (s *Server) kernelHostCallPolicy(ctx context.Context, access workspace.KernelFrameAccess, workspaceDirs ...string) *kernelruntime.HostCallPolicy {
	workspaceDir := ""
	if len(workspaceDirs) > 0 {
		workspaceDir = workspaceDirs[0]
	}
	return s.agentKernelHostCallPolicy(ctx, access, workspaceDir, nil, false)
}

func (s *Server) agentKernelHostCallPolicy(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	workspaceDir string,
	allowedTools []string,
	fresh bool,
	executionBindings ...kernelTranscriptExecutionBinding,
) *kernelruntime.HostCallPolicy {
	bound := kernelHostExecutionIdentity{access: access, allowedTools: chatRunnerAllowedToolSet(allowedTools), fresh: fresh}
	if len(executionBindings) > 0 {
		bound.transcriptExecution = executionBindings[0]
	}
	if s != nil && s.workspaceStore != nil {
		if project, found, err := s.workspaceStore.GetProject(access.Frame.ProjectID); err == nil && found {
			bound.workspaceDir = strings.TrimSpace(project.Path)
		}
		if routine, found, err := s.workspaceStore.GetRoutineByRoot(ctx, access.Frame.RootFrameID); err == nil && found && routine.LockedAt != nil {
			claim := routine
			bound.routineClaim = &claim
		}
	}
	if strings.TrimSpace(workspaceDir) != "" {
		bound.workspaceDir = strings.TrimSpace(workspaceDir)
	}
	if bound.workspaceDir != "" {
		bound.workspaceDir = filepath.Clean(bound.workspaceDir)
	}
	messageBudgets := s.kernelMessageBudgetsForFrame(access.Frame.ID)
	handler := s.handleKernelHostCall(bound, messageBudgets)
	if ctx != nil {
		baseHandler := handler
		handler = func(callCtx context.Context, call kernelruntime.HostCall) (any, error) {
			combined, cancel := context.WithCancel(callCtx)
			stop := context.AfterFunc(ctx, cancel)
			defer stop()
			defer cancel()
			return baseHandler(combined, call)
		}
	}
	allowedMethods := append([]string(nil), kernelHostMethods...)
	if sessionReviewerEvidenceScopeFromContext(ctx) != nil {
		allowedMethods = []string{"host.archive.search", "host.archive.page", "host.artifact_path"}
	}
	if fresh {
		filtered := allowedMethods[:0]
		for _, method := range allowedMethods {
			if method != "host.delegate" {
				filtered = append(filtered, method)
			}
		}
		allowedMethods = filtered
	}
	return &kernelruntime.HostCallPolicy{
		Handler:        handler,
		AllowedMethods: allowedMethods,
		MaxCalls:       1024,
		CallTimeout:    24 * time.Hour,
	}
}

func (s *Server) kernelMessageBudgetsForFrame(frameID string) *kernelMessageBudgets {
	budgets := &kernelMessageBudgets{}
	if s == nil {
		return budgets
	}
	s.sessionRunsMu.Lock()
	if run := s.sessionRuns[strings.TrimSpace(frameID)]; run != nil {
		budgets = &run.kernelPeer
	}
	s.sessionRunsMu.Unlock()
	return budgets
}

type kernelHostExecutionIdentity struct {
	access              workspace.KernelFrameAccess
	routineClaim        *workspace.Routine
	workspaceDir        string
	allowedTools        map[string]struct{}
	fresh               bool
	transcriptExecution kernelTranscriptExecutionBinding
}

func (s *Server) handleKernelHostCall(bound kernelHostExecutionIdentity, messageBudgets *kernelMessageBudgets) kernelruntime.HostCallHandler {
	return func(ctx context.Context, call kernelruntime.HostCall) (any, error) {
		if ctx == nil {
			return nil, kernelruntime.NewHostCallError("cancelled", "host call context is unavailable")
		}
		current, err := s.validateKernelHostIdentity(ctx, bound.access)
		if err != nil {
			return nil, err
		}
		args, kwargs, err := normalizeKernelHostArguments(call.Args, call.Kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		bridge := &kernelRoutineHostBridge{
			store: s.workspaceStore, rootFrameID: current.Frame.RootFrameID,
			ownerUserID: current.UserID, claim: bound.routineClaim, callID: call.ID,
		}
		if isKernelLLMHostMethod(call.Method) {
			return s.handleKernelLLMHostCall(ctx, bound, current, call.Method, args, kwargs)
		}
		if isKernelSupervisionHostMethod(call.Method) {
			return s.handleKernelSupervisionHostCall(ctx, bound, current, call.Method, args, kwargs, call.ID, messageBudgets)
		}
		if isKernelInspectionHostMethod(call.Method) {
			return s.handleKernelInspectionHostCall(ctx, bound, current, call.Method, args, kwargs)
		}
		if isKernelArtifactMutationHostMethod(call.Method) {
			return s.handleKernelArtifactMutationHostCall(ctx, bound, current, call, args, kwargs)
		}
		if isKernelRegistryHostMethod(call.Method) {
			return s.handleKernelRegistryHostCall(ctx, current, call.ID, call.Method, args, kwargs)
		}
		if isKernelMCPManagementHostMethod(call.Method) {
			return s.handleKernelMCPManagementHostCall(ctx, current, call.ID, call.Method, args, kwargs)
		}
		if isKernelArchiveHostMethod(call.Method) {
			return s.handleKernelArchiveHostCall(ctx, current, call.Method, args, kwargs)
		}
		if isKernelComputeHostMethod(call.Method) {
			return s.handleKernelComputeHostCall(ctx, current, bound.workspaceDir, call.ID, call.Method, args, kwargs)
		}
		if isKernelMiscHostMethod(call.Method) {
			return s.handleKernelMiscHostCall(ctx, current, call.Method, args, kwargs)
		}
		if isKernelAppHostMethod(call.Method) {
			return s.handleKernelAppHostCall(ctx, bound, current, call.Method, args, kwargs, call.ID)
		}
		switch call.Method {
		case "mcp":
			return s.handleKernelMCPHostCall(ctx, bound, current, call, args, kwargs)
		case "mcp.catalog":
			return s.handleKernelMCPCatalogHostCall(ctx, bound, current, args, kwargs)
		case "host.routine.configure":
			input, err := ParseRoutineHostConfigure(args, kwargs)
			if err != nil {
				return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
			}
			result, err := bridge.Configure(ctx, input)
			return result, classifyKernelHostError(err)
		case "host.routine.status":
			if err := ParseRoutineHostStatus(args, kwargs); err != nil {
				return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
			}
			status, err := bridge.Status(ctx)
			if err != nil {
				return nil, classifyKernelHostError(err)
			}
			return routineHostStatusResult(status), nil
		case "host.routine.done":
			input, err := ParseRoutineHostDone(args, kwargs)
			if err != nil {
				return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
			}
			result, err := bridge.Done(ctx, input)
			return result, classifyKernelHostError(err)
		default:
			return nil, kernelruntime.NewHostCallError("method_not_allowed", "host method is not allowed")
		}
	}
}

func (s *Server) validateKernelHostIdentity(ctx context.Context, bound workspace.KernelFrameAccess) (workspace.KernelFrameAccess, error) {
	if s == nil || s.workspaceStore == nil {
		return workspace.KernelFrameAccess{}, kernelruntime.NewHostCallError("unavailable", "workspace runtime is not configured")
	}
	if strings.TrimSpace(bound.Frame.ID) == "" || strings.TrimSpace(bound.Frame.IncarnationID) == "" ||
		strings.TrimSpace(bound.Frame.RootFrameID) == "" || strings.TrimSpace(bound.RootFrameIncarnationID) == "" ||
		strings.TrimSpace(bound.UserID) == "" {
		return workspace.KernelFrameAccess{}, kernelruntime.NewHostCallError("permission_denied", "kernel execution identity is incomplete")
	}
	select {
	case <-ctx.Done():
		return workspace.KernelFrameAccess{}, ctx.Err()
	default:
	}
	current, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, bound.Frame.ID)
	if err != nil {
		return workspace.KernelFrameAccess{}, kernelruntime.NewHostCallError("storage_error", "could not verify kernel frame ownership")
	}
	if !found || current.UserID != bound.UserID || current.Frame.ProjectID != bound.Frame.ProjectID ||
		current.Frame.RootFrameID != bound.Frame.RootFrameID || current.Frame.IncarnationID != bound.Frame.IncarnationID ||
		current.RootFrameIncarnationID != bound.RootFrameIncarnationID || current.ProjectPath != bound.ProjectPath {
		return workspace.KernelFrameAccess{}, kernelruntime.NewHostCallError("permission_denied", "kernel frame ownership changed or is invalid")
	}
	root, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, current.Frame.RootFrameID)
	if err != nil {
		return workspace.KernelFrameAccess{}, kernelruntime.NewHostCallError("storage_error", "could not verify kernel root ownership")
	}
	if !found || root.Frame.ID != root.Frame.RootFrameID || root.Frame.ProjectID != current.Frame.ProjectID ||
		root.Frame.IncarnationID != bound.RootFrameIncarnationID || root.UserID != current.UserID {
		return workspace.KernelFrameAccess{}, kernelruntime.NewHostCallError("permission_denied", "kernel root does not belong to the execution project owner")
	}
	owner, found, err := s.workspaceStore.ProjectOwnerIDContext(ctx, current.Frame.ProjectID)
	if err != nil {
		return workspace.KernelFrameAccess{}, kernelruntime.NewHostCallError("storage_error", "could not verify project owner")
	}
	if !found || owner != current.UserID {
		return workspace.KernelFrameAccess{}, kernelruntime.NewHostCallError("permission_denied", "kernel execution user no longer owns the project")
	}
	return current, nil
}

type kernelRoutineHostBridge struct {
	store       *workspace.Store
	rootFrameID string
	ownerUserID string
	claim       *workspace.Routine
	callID      string
}

func (b *kernelRoutineHostBridge) Configure(ctx context.Context, input RoutineHostConfigure) (map[string]any, error) {
	if b == nil || b.store == nil {
		return nil, ErrRoutineKernelHostTransportUnavailable
	}
	routineID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("synon-routine:"+b.rootFrameID)).String()
	routine, err := b.store.ConfigureRoutineHost(ctx, workspace.ConfigureRoutineHostInput{
		ID: routineID, RootFrameID: b.rootFrameID, OwnerUserID: b.ownerUserID,
		Label: input.Label, OnTick: input.OnTick, EveryMinutes: input.EveryMinutes,
		MutationID: b.callID,
	})
	if err != nil {
		return nil, err
	}
	result := routineHostStatusResult(routineStatus(routine, true))
	result["ok"] = true
	result["routine_id"] = routine.ID
	return result, nil
}

func (b *kernelRoutineHostBridge) Status(ctx context.Context) (RoutineHostStatus, error) {
	if b == nil || b.store == nil {
		return RoutineHostStatus{}, ErrRoutineKernelHostTransportUnavailable
	}
	routine, found, err := b.store.GetRoutineByRoot(ctx, b.rootFrameID)
	if err != nil {
		return RoutineHostStatus{}, err
	}
	if !found {
		return RoutineHostStatus{Configured: false}, nil
	}
	if routine.OwnerUserID != b.ownerUserID {
		return RoutineHostStatus{}, errors.New("routine belongs to a different project owner")
	}
	return routineStatus(routine, true), nil
}

func (b *kernelRoutineHostBridge) Done(ctx context.Context, input RoutineHostDone) (map[string]any, error) {
	if b == nil || b.store == nil {
		return nil, ErrRoutineKernelHostTransportUnavailable
	}
	if b.claim == nil || b.claim.LockedAt == nil || b.claim.ClaimGeneration < 1 || strings.TrimSpace(b.claim.ClaimToken) == "" {
		return nil, errors.New("routine is not claimed by the scheduler for this kernel execution")
	}
	routine, err := b.store.RecordRoutineHostDone(ctx, b.rootFrameID, b.ownerUserID, *b.claim, input.HadWork, input.Summary)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "routine_id": routine.ID, "had_work": input.HadWork,
		"summary": input.Summary, "idle_streak": routine.IdleStreak,
	}, nil
}

func routineStatus(routine workspace.Routine, configured bool) RoutineHostStatus {
	return RoutineHostStatus{
		Configured: configured, Enabled: routine.Enabled, EveryMinutes: routine.EveryMinutes,
		OnTick: routine.OnTick, Label: routine.Label, TickCount: routine.TickCount,
		IdleStreak: routine.IdleStreak,
	}
}

func routineHostStatusResult(status RoutineHostStatus) map[string]any {
	return map[string]any{
		"configured":    status.Configured,
		"enabled":       status.Enabled,
		"every_minutes": status.EveryMinutes,
		"on_tick":       status.OnTick,
		"label":         status.Label,
		"tick_count":    status.TickCount,
		"idle_streak":   status.IdleStreak,
	}
}

const (
	maxKernelHostJSONDepth       = 12
	maxKernelHostJSONNodes       = 4096
	maxKernelHostJSONStringBytes = 44 * 1024 * 1024
	maxKernelHostJSONCollection  = 1024
)

type kernelHostJSONBudget struct{ nodes int }

func normalizeKernelHostArguments(args []any, kwargs map[string]any) ([]any, map[string]any, error) {
	budget := &kernelHostJSONBudget{}
	normalizedArgs := make([]any, len(args))
	for index, value := range args {
		normalized, err := normalizeKernelHostValue(value, 0, budget)
		if err != nil {
			return nil, nil, fmt.Errorf("argument %d: %w", index, err)
		}
		normalizedArgs[index] = normalized
	}
	normalizedKwargs := make(map[string]any, len(kwargs))
	for key, value := range kwargs {
		normalized, err := normalizeKernelHostValue(value, 0, budget)
		if err != nil {
			return nil, nil, fmt.Errorf("keyword %s: %w", key, err)
		}
		normalizedKwargs[key] = normalized
	}
	return normalizedArgs, normalizedKwargs, nil
}

func normalizeKernelHostValue(value any, depth int, budget *kernelHostJSONBudget) (any, error) {
	if depth > maxKernelHostJSONDepth {
		return nil, fmt.Errorf("JSON nesting exceeds depth %d", maxKernelHostJSONDepth)
	}
	budget.nodes++
	if budget.nodes > maxKernelHostJSONNodes {
		return nil, fmt.Errorf("JSON value exceeds %d nodes", maxKernelHostJSONNodes)
	}
	switch typed := value.(type) {
	case json.Number:
		if integer, err := strconv.ParseInt(typed.String(), 10, 64); err == nil {
			converted := int(integer)
			if int64(converted) != integer {
				return nil, errors.New("integer is outside the platform range")
			}
			return converted, nil
		}
		decimal, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil || math.IsNaN(decimal) || math.IsInf(decimal, 0) {
			return nil, errors.New("numeric value is not finite")
		}
		return decimal, nil
	case string:
		if len([]byte(typed)) > maxKernelHostJSONStringBytes {
			return nil, fmt.Errorf("string exceeds %d UTF-8 bytes", maxKernelHostJSONStringBytes)
		}
		return typed, nil
	case nil, bool:
		return typed, nil
	case []any:
		if len(typed) > maxKernelHostJSONCollection {
			return nil, fmt.Errorf("array exceeds %d elements", maxKernelHostJSONCollection)
		}
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeKernelHostValue(item, depth+1, budget)
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", index, err)
			}
			result[index] = normalized
		}
		return result, nil
	case map[string]any:
		if len(typed) > maxKernelHostJSONCollection {
			return nil, fmt.Errorf("object exceeds %d fields", maxKernelHostJSONCollection)
		}
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "" || len([]byte(key)) > 128 || strings.ContainsAny(key, "\x00\r\n") {
				return nil, errors.New("object key is invalid")
			}
			normalized, err := normalizeKernelHostValue(item, depth+1, budget)
			if err != nil {
				return nil, fmt.Errorf("object field %s: %w", key, err)
			}
			result[key] = normalized
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value type %T", value)
	}
}

func classifyKernelHostError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var typed *kernelruntime.HostCallError
	if errors.As(err, &typed) {
		return typed
	}
	message := strings.TrimSpace(err.Error())
	if strings.Contains(message, "not configured") || strings.Contains(message, "does not exist") {
		return kernelruntime.NewHostCallError("not_found", message)
	}
	if strings.Contains(message, "owner") || strings.Contains(message, "owned") || strings.Contains(message, "belongs") {
		return kernelruntime.NewHostCallError("permission_denied", message)
	}
	return kernelruntime.NewHostCallError("storage_error", message)
}

var _ RoutineHostBridge = (*kernelRoutineHostBridge)(nil)
