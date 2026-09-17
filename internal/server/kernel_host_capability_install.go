package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agentruntime "synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const kernelCapabilityInstallApprovalSource = "kernel-host-capability-install"

func (s *Server) requireKernelCapabilityInstallApproval(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	callID, method, capabilityKind string,
	input, metadata map[string]any,
) error {
	mode, err := s.webSessionApprovalMode(access.Frame.ID)
	if err != nil {
		return kernelruntime.NewHostCallError("approval_unavailable", "conversation permission policy is unavailable")
	}
	decision, err := webPermissionDecision(mode, true)
	if err != nil {
		return kernelruntime.NewHostCallError("policy_blocked", "conversation permission policy is invalid")
	}
	if decision == "allow" {
		return nil
	}
	if decision == "deny" {
		return kernelruntime.NewHostCallError("permission_denied", "capability installation is denied by the conversation permission policy")
	}
	defaults := s.agentRuntimeApprovalDefaults()
	defaults.Mode = "ask"
	defaults.RequireReason = false
	defaults.RememberDecisions = false
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["approvalType"] = "capability_install"
	metadata["capabilityKind"] = strings.TrimSpace(capabilityKind)
	metadata["frameId"] = access.Frame.ID
	metadata["projectId"] = access.Frame.ProjectID
	if strings.TrimSpace(callID) == "" {
		callID = method
	}
	permission := s.queueAgentRuntimeApprovalForSourceWithMetadata(
		method,
		agentruntime.ToolCall{ID: callID, Name: method},
		copyMapAny(input),
		"ask",
		defaults,
		kernelCapabilityInstallApprovalSource,
		metadata,
	)
	approvalID := strings.TrimSpace(stringValue(permission["approvalId"]))
	if approvalID == "" {
		return kernelruntime.NewHostCallError("approval_unavailable", "capability installation approval could not be persisted")
	}
	pendingRequest := map[string]any{
		"requestId": approvalID,
		"kind":      "capability_install",
		"tool":      method,
		"title": firstNonEmpty(
			strings.TrimSpace(stringValue(metadata["title"])),
			"Install capability for this task",
		),
		"description": firstNonEmpty(
			strings.TrimSpace(stringValue(metadata["description"])),
			"Install the reviewed capability, verify it, then continue the current task.",
		),
		"code":            kernelCapabilityInstallApprovalCode(capabilityKind, input),
		"mode":            "rw",
		"rememberable":    false,
		"capability_kind": strings.TrimSpace(capabilityKind),
	}
	if err := s.workspaceStore.AddKernelArtifactApprovalRequest(
		ctx, access.UserID, access.Frame.ProjectID, access.Frame.ID, pendingRequest,
	); err != nil {
		return kernelruntime.NewHostCallError("approval_unavailable", "capability installation approval could not be presented")
	}
	if frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(access.Frame.ID); err != nil || !found {
		_ = s.workspaceStore.RemoveKernelArtifactApprovalRequest(
			context.Background(), access.UserID, access.Frame.ProjectID, access.Frame.ID, approvalID,
		)
		return kernelruntime.NewHostCallError("approval_unavailable", "capability installation approval could not be presented")
	} else if err := s.publishWebConfirmationProjection(frameContext, workspace.FrameEvent{}); err != nil {
		_ = s.workspaceStore.RemoveKernelArtifactApprovalRequest(
			context.Background(), access.UserID, access.Frame.ProjectID, access.Frame.ID, approvalID,
		)
		return kernelruntime.NewHostCallError("approval_unavailable", "capability installation approval could not be presented")
	}
	defer s.clearKernelCapabilityInstallApproval(access, approvalID)
	if err := s.waitForKernelHostApproval(ctx, approvalID, "capability installation"); err != nil {
		return err
	}
	_, err = s.validateKernelHostIdentity(ctx, access)
	return err
}

func kernelCapabilityInstallApprovalCode(capabilityKind string, input map[string]any) string {
	payload := map[string]any{"kind": strings.TrimSpace(capabilityKind)}
	for _, key := range []string{"repo", "sha", "skills", "name", "transport", "url", "command", "args", "start", "stop", "live", "port", "credential", "skill"} {
		if value, found := input[key]; found {
			payload[key] = value
		}
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return strings.TrimSpace(capabilityKind)
	}
	return string(encoded)
}

func (s *Server) clearKernelCapabilityInstallApproval(access workspace.KernelFrameAccess, approvalID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.workspaceStore.RemoveKernelArtifactApprovalRequest(
		ctx, access.UserID, access.Frame.ProjectID, access.Frame.ID, approvalID,
	); err != nil {
		return
	}
	_ = s.publishWebConfirmationRemovals(access.Frame.ID, []string{approvalID}, access.Frame.Status)
}

func (s *Server) resolveKernelCapabilityInstallApprovalInputs(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
	pendingByID map[string]map[string]any,
	responses []compatibilityInputResponse,
) (compatibilityResolveInputResult, bool, error) {
	capabilityResponses := 0
	seen := make(map[string]bool, len(responses))
	for _, response := range responses {
		id := strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if item := pendingByID[id]; strings.EqualFold(strings.TrimSpace(stringValue(item["kind"])), "capability_install") {
			capabilityResponses++
		}
	}
	if capabilityResponses == 0 {
		return compatibilityResolveInputResult{}, false, nil
	}
	if capabilityResponses != len(responses) {
		return compatibilityResolveInputResult{}, true, resolveInputRequestError(
			400, "Capability installation approvals cannot be mixed with other input responses.",
		)
	}
	ownerID, found, err := s.workspaceStore.ProjectOwnerIDContext(ctx, frame.ProjectID)
	if err != nil || !found {
		return compatibilityResolveInputResult{}, true, errors.New("capability installation approval owner is unavailable")
	}
	resolvedIDs := make([]string, 0, len(responses))
	for _, response := range responses {
		id := strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID))
		scope := strings.ToLower(strings.TrimSpace(response.Scope))
		action := strings.ToLower(strings.TrimSpace(response.Action))
		if scope != "" && scope != "once" || action == "allow_always" || action == "proceed_always" {
			return compatibilityResolveInputResult{}, true, resolveInputRequestError(
				400, "Capability installation approval is valid for this invocation only.",
			)
		}
		approved := response.Approved != nil && *response.Approved
		entry, found, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, id)
		if err != nil || !found {
			return compatibilityResolveInputResult{}, true, errors.New("capability installation approval is unavailable")
		}
		value := mapValue(entry.Value)
		if stringValue(value["approvalSource"]) != kernelCapabilityInstallApprovalSource ||
			stringValue(value["frameId"]) != frame.ID || stringValue(value["projectId"]) != frame.ProjectID {
			return compatibilityResolveInputResult{}, true, errors.New("capability installation approval authority changed")
		}
		if _, err := s.resolveAgentRuntimeApprovalMessage(ctx, "", map[string]any{
			"approvalId": id, "approve": approved,
		}); err != nil {
			return compatibilityResolveInputResult{}, true, err
		}
		if err := s.workspaceStore.RemoveKernelArtifactApprovalRequest(
			ctx, ownerID, frame.ProjectID, frame.ID, id,
		); err != nil {
			return compatibilityResolveInputResult{}, true, err
		}
		resolvedIDs = append(resolvedIDs, id)
	}
	metadata, _, err := s.workspaceStore.GetFrameRuntimeMetadata(frame.ID)
	if err != nil {
		return compatibilityResolveInputResult{}, true, err
	}
	remaining := make([]string, 0)
	for _, item := range compatibilityServerPendingInputs(metadata.ContextData) {
		if id := compatibilityServerPendingInputID(item); id != "" {
			remaining = append(remaining, id)
		}
	}
	if err := s.publishWebConfirmationRemovals(frame.ID, resolvedIDs, frame.Status); err != nil {
		return compatibilityResolveInputResult{}, true, err
	}
	return compatibilityResolveInputResult{Frame: frame, Status: frame.Status, RemainingIDs: remaining}, true, nil
}

func isKernelHostWaitOnlyApprovalSource(source string) bool {
	source = strings.TrimSpace(source)
	return source == "kernel-host-mcp" || source == kernelCapabilityInstallApprovalSource || source == kernelArtifactDeleteApprovalSource
}

func (s *Server) waitForKernelHostApproval(ctx context.Context, approvalID, subject string) error {
	if s == nil || s.runtimeStore == nil {
		return kernelruntime.NewHostCallError("approval_unavailable", subject+" approval store is unavailable")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		entry, found, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
		if err != nil || !found {
			return kernelruntime.NewHostCallError("approval_unavailable", subject+" approval request is unavailable")
		}
		value := mapValue(entry.Value)
		switch strings.TrimSpace(stringValue(value["status"])) {
		case "approved":
			return nil
		case "denied":
			return kernelruntime.NewHostCallError("permission_denied", subject+" approval was denied")
		case "blocked", "failed":
			return kernelruntime.NewHostCallError("policy_blocked", subject+" approval could not be completed")
		case "pending":
		default:
			return kernelruntime.NewHostCallError("approval_unavailable", subject+" approval state is invalid")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
