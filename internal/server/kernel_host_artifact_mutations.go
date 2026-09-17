package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const kernelArtifactDeleteApprovalSource = "kernel-host-artifact-delete"

type kernelArtifactApprovalUserContextKey struct{}

func kernelArtifactApprovalUserAuthorized(ctx context.Context) bool {
	approved, _ := ctx.Value(kernelArtifactApprovalUserContextKey{}).(bool)
	return approved
}

func isKernelArtifactMutationHostMethod(method string) bool {
	return method == "host.artifacts.rename" || method == "host.artifacts.delete"
}

func (s *Server) handleKernelArtifactMutationHostCall(
	ctx context.Context,
	bound kernelHostExecutionIdentity,
	access workspace.KernelFrameAccess,
	call kernelruntime.HostCall,
	args []any,
	kwargs map[string]any,
) (any, error) {
	if len(kwargs) != 0 {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", call.Method+": keyword arguments are not accepted on the wire")
	}
	switch call.Method {
	case "host.artifacts.rename":
		if len(args) != 2 {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts.rename requires artifact id and filename")
		}
		artifactID, idOK := args[0].(string)
		filename, filenameOK := args[1].(string)
		if !idOK || strings.TrimSpace(artifactID) == "" || !filenameOK {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts.rename requires non-empty string arguments")
		}
		filename = workspace.SanitizeCompatibilityArtifactFilename(filename)
		if filename == "" {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts.rename filename is empty after validation")
		}
		result, err := s.workspaceStore.RenameKernelArtifact(ctx, access.UserID, access.Frame.ProjectID, artifactID, filename)
		if err != nil {
			return nil, kernelArtifactMutationError("host.artifacts.rename", err)
		}
		return result, nil
	case "host.artifacts.delete":
		artifactIDs, reason, err := parseKernelArtifactDeleteWire(args)
		if err != nil {
			return nil, err
		}
		approved, totalBytes, err := s.workspaceStore.InspectKernelArtifactsForDelete(ctx, access.UserID, access.Frame.ProjectID, artifactIDs)
		if err != nil {
			return nil, kernelArtifactMutationError("host.artifacts.delete", err)
		}
		input := map[string]any{"artifact_ids": artifactIDs}
		if reason != "" {
			input["reason"] = reason
		}
		items := make([]any, 0, len(approved))
		for _, item := range approved {
			items = append(items, map[string]any{
				"artifact_id": item.ArtifactID, "filename": item.Filename,
				"size_bytes": item.SizeBytes, "version_count": item.VersionCount,
				"is_user_upload": item.IsUserUpload, "is_branch_mint": item.IsBranchMint,
				"is_ref": item.IsReference, "created_at": item.CreatedAt,
			})
		}
		mode, err := s.webSessionApprovalMode(access.Frame.ID)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("approval_unavailable", "conversation permission policy is unavailable")
		}
		decision, err := webPermissionDecision(mode, true)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("policy_blocked", "conversation permission policy is invalid")
		}
		if decision == "deny" {
			return nil, kernelruntime.NewHostCallError("permission_denied", "artifact deletion is denied by the conversation permission policy")
		}
		if decision == "ask" {
			defaults := s.agentRuntimeApprovalDefaults()
			defaults.Mode = "ask"
			defaults.RequireReason = false
			defaults.RememberDecisions = false
			metadata := map[string]any{
				"approvalType": "artifact_delete", "frameId": access.Frame.ID,
				"projectId": access.Frame.ProjectID, "artifact_ids": artifactIDs,
				"items": items, "total_bytes": totalBytes,
			}
			if reason != "" {
				metadata["reason"] = reason
			}
			approvalCall := agentruntime.ToolCall{ID: call.ID, Name: call.Method}
			approvalID := agentRuntimeApprovalID(call.Method, approvalCall, input)
			filenames := make([]string, 0, len(approved))
			for _, item := range approved {
				label := item.Filename + " — " + fmt.Sprintf("%d version(s), %d bytes", item.VersionCount, item.SizeBytes)
				if item.IsReference {
					label += " (reference; source file retained)"
				}
				filenames = append(filenames, label)
			}
			filenames = append(filenames, fmt.Sprintf("Total physical bytes: %d", totalBytes))
			if reason != "" {
				filenames = append(filenames, "Reason: "+reason)
			}
			pendingRequest := map[string]any{
				"requestId": approvalID, "kind": "artifact_delete", "tool": call.Method,
				"title":       "Delete artifacts permanently",
				"description": "Permanently delete the selected artifacts and every stored version.",
				"code":        strings.Join(filenames, "\n"), "mode": "rw", "rememberable": false,
				"items": items, "total_bytes": totalBytes,
			}
			if reason != "" {
				pendingRequest["reason"] = reason
			}
			if err := s.workspaceStore.AddKernelArtifactApprovalRequest(
				ctx, access.UserID, access.Frame.ProjectID, access.Frame.ID, pendingRequest,
			); err != nil {
				return nil, kernelruntime.NewHostCallError("approval_unavailable", "host.artifacts.delete approval could not be presented")
			}
			if frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(access.Frame.ID); err != nil || !found {
				_ = s.workspaceStore.RemoveKernelArtifactApprovalRequest(context.Background(), access.UserID, access.Frame.ProjectID, access.Frame.ID, approvalID)
				return nil, kernelruntime.NewHostCallError("approval_unavailable", "host.artifacts.delete approval could not be presented")
			} else if err := s.publishWebConfirmationProjection(frameContext, workspace.FrameEvent{}); err != nil {
				_ = s.workspaceStore.RemoveKernelArtifactApprovalRequest(context.Background(), access.UserID, access.Frame.ProjectID, access.Frame.ID, approvalID)
				return nil, kernelruntime.NewHostCallError("approval_unavailable", "host.artifacts.delete approval could not be presented")
			}
			permission := s.queueAgentRuntimeApprovalForSourceWithMetadata(
				call.Method,
				approvalCall,
				input,
				"ask",
				defaults,
				kernelArtifactDeleteApprovalSource,
				metadata,
			)
			if strings.TrimSpace(stringValue(permission["approvalId"])) != approvalID {
				_ = s.workspaceStore.RemoveKernelArtifactApprovalRequest(context.Background(), access.UserID, access.Frame.ProjectID, access.Frame.ID, approvalID)
				return nil, kernelruntime.NewHostCallError("approval_unavailable", "host.artifacts.delete approval could not be persisted")
			}
			defer s.clearKernelArtifactDeleteApproval(access, approvalID)
			if err := s.waitForKernelArtifactDeleteApproval(ctx, approvalID); err != nil {
				return nil, err
			}
		}
		if _, err := s.validateKernelHostIdentity(ctx, bound.access); err != nil {
			return nil, err
		}
		results := s.workspaceStore.DeleteKernelArtifactsAfterApproval(ctx, access.UserID, access.Frame.ProjectID, approved)
		deleted := 0
		for index := range results {
			if results[index].Status != "deleted" {
				continue
			}
			deleted++
			if err := s.workspaceStore.RemoveArtifactBlobs(results[index].BlobPaths); err != nil {
				log.Printf("kernel artifact blob cleanup deferred until workspace restart reconciliation")
			}
		}
		return map[string]any{"requested": len(artifactIDs), "deleted": deleted, "results": results}, nil
	default:
		return nil, kernelruntime.NewHostCallError("method_not_allowed", "host method is not allowed")
	}
}

func (s *Server) clearKernelArtifactDeleteApproval(access workspace.KernelFrameAccess, approvalID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.workspaceStore.RemoveKernelArtifactApprovalRequest(
		ctx, access.UserID, access.Frame.ProjectID, access.Frame.ID, approvalID,
	); err != nil {
		return
	}
	_ = s.publishWebConfirmationRemovals(access.Frame.ID, []string{approvalID}, access.Frame.Status)
}

func parseKernelArtifactDeleteWire(args []any) ([]string, string, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, "", kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts.delete requires artifact ids and optional reason")
	}
	var values []any
	switch typed := args[0].(type) {
	case string:
		values = []any{typed}
	case []any:
		values = typed
	default:
		return nil, "", kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts.delete artifact ids must be a string or array")
	}
	if len(values) == 0 {
		return nil, "", kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts.delete artifact ids must not be empty")
	}
	seen := make(map[string]struct{}, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id, ok := value.(string)
		if !ok || strings.TrimSpace(id) == "" {
			return nil, "", kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts.delete artifact ids must be non-empty strings")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) > workspace.MaxKernelArtifactDeleteBatch {
		return nil, "", kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts.delete accepts at most 200 unique artifact ids")
	}
	reason := ""
	if len(args) == 2 && args[1] != nil {
		value, ok := args[1].(string)
		if !ok {
			return nil, "", kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts.delete reason must be a string or null")
		}
		reason = strings.TrimSpace(value)
	}
	return ids, reason, nil
}

func (s *Server) waitForKernelArtifactDeleteApproval(ctx context.Context, approvalID string) error {
	err := s.waitForKernelMCPApproval(ctx, approvalID)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return kernelruntime.NewHostCallError("cancelled", "host.artifacts.delete cancelled before approval")
	}
	var typed *kernelruntime.HostCallError
	if errors.As(err, &typed) && typed.Code == "permission_denied" {
		return kernelruntime.NewHostCallError("permission_denied", "User declined the artifact deletion — do not retry or work around it; ask the user or move on.")
	}
	return err
}

func kernelArtifactMutationError(method string, err error) error {
	switch {
	case errors.Is(err, workspace.ErrKernelArtifactNotFound):
		return kernelruntime.NewHostCallError("not_found", method+": artifact was not found; pass the id field from host.artifacts(), not a version id")
	case errors.Is(err, workspace.ErrKernelArtifactNotAgentOwned):
		return kernelruntime.NewHostCallError("permission_denied", method+": artifact is not agent-owned")
	default:
		return kernelruntime.NewHostCallError("storage_error", method+": artifact operation failed")
	}
}

func (s *Server) resolveKernelArtifactApprovalInputs(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
	pendingByID map[string]map[string]any,
	responses []compatibilityInputResponse,
) (compatibilityResolveInputResult, bool, error) {
	artifactResponses := 0
	seen := make(map[string]bool, len(responses))
	for _, response := range responses {
		id := strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if item := pendingByID[id]; strings.EqualFold(strings.TrimSpace(stringValue(item["kind"])), "artifact_delete") {
			artifactResponses++
		}
	}
	if artifactResponses == 0 {
		return compatibilityResolveInputResult{}, false, nil
	}
	if artifactResponses != len(responses) {
		return compatibilityResolveInputResult{}, true, resolveInputRequestError(
			400, "Artifact deletion approvals cannot be mixed with other input responses.",
		)
	}
	ownerID, found, err := s.workspaceStore.ProjectOwnerIDContext(ctx, frame.ProjectID)
	if err != nil || !found {
		return compatibilityResolveInputResult{}, true, errors.New("artifact approval owner is unavailable")
	}
	for _, response := range responses {
		id := strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID))
		scope := strings.ToLower(strings.TrimSpace(response.Scope))
		action := strings.ToLower(strings.TrimSpace(response.Action))
		if scope != "" && scope != "once" || action == "allow_always" || action == "proceed_always" {
			return compatibilityResolveInputResult{}, true, resolveInputRequestError(
				400, "Artifact deletion approval is valid for this invocation only.",
			)
		}
		approved := response.Approved != nil && *response.Approved
		entry, found, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, id)
		if err != nil || !found {
			return compatibilityResolveInputResult{}, true, errors.New("artifact deletion approval is unavailable")
		}
		value := mapValue(entry.Value)
		if stringValue(value["approvalSource"]) != kernelArtifactDeleteApprovalSource ||
			stringValue(value["frameId"]) != frame.ID || stringValue(value["projectId"]) != frame.ProjectID {
			return compatibilityResolveInputResult{}, true, errors.New("artifact deletion approval authority changed")
		}
		userContext := context.WithValue(ctx, kernelArtifactApprovalUserContextKey{}, true)
		if _, err := s.resolveAgentRuntimeApprovalMessage(userContext, "", map[string]any{
			"approvalId": id, "approve": approved,
		}); err != nil {
			return compatibilityResolveInputResult{}, true, err
		}
		if err := s.workspaceStore.RemoveKernelArtifactApprovalRequest(
			ctx, ownerID, frame.ProjectID, frame.ID, id,
		); err != nil {
			return compatibilityResolveInputResult{}, true, err
		}
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
	ids := make([]string, 0, len(responses))
	for _, response := range responses {
		ids = append(ids, strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID)))
	}
	if err := s.publishWebConfirmationRemovals(frame.ID, ids, frame.Status); err != nil {
		return compatibilityResolveInputResult{}, true, err
	}
	return compatibilityResolveInputResult{Frame: frame, Status: frame.Status, RemainingIDs: remaining}, true, nil
}
