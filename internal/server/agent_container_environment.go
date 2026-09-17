package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/google/uuid"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/software/localcontainer"
)

func (s *Server) localContainerEnvironmentManager() (*localcontainer.Manager, error) {
	if s == nil || s.kernelManager == nil || strings.TrimSpace(s.condaHome) == "" {
		return nil, errors.New("local container environment runtime is unavailable")
	}
	return localcontainer.New(s.kernelManager, s.condaHome)
}

func (s *Server) executeAgentContainerEnvironmentTool(
	ctx context.Context,
	identity *agentKernelContext,
	access workspace.KernelFrameAccess,
	call agentruntime.ToolCall,
	request manageEnvironmentsInput,
) (any, error) {
	manager, err := s.localContainerEnvironmentManager()
	if err != nil {
		return map[string]any{
			"tool": manageEnvironmentsToolName, "provider": localcontainer.ProviderID,
			"ok": true, "executed": false, "status": "container_runtime_unavailable",
			"message":  boundedManagedEnvironmentError(err),
			"recovery": "Keep the selected scientific implementation. Restore the local Docker daemon and managed command host, then retry this same container proposal; do not switch software merely because the runtime is temporarily unavailable.",
		}, nil
	}
	if request.Mode == "list" {
		environments, err := manager.List(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"tool": manageEnvironmentsToolName, "mode": "list", "provider": localcontainer.ProviderID,
			"environments": environments, "count": len(environments),
			"machine": s.managedEnvironmentMachineSnapshot(ctx, identity),
		}, nil
	}
	if request.Mode != "preflight" && request.Mode != "create" {
		return nil, errors.New("local-container supports list, preflight, and create")
	}
	if strings.TrimSpace(request.Image) == "" {
		return nil, errors.New("local-container requires image")
	}
	if decision, required := s.managedEnvironmentImplementationDecision(ctx, request.Implementation, request.Mode == "create"); required {
		return decision, nil
	}
	requirements := defaultManagedEnvironmentCreateRequirements(request.ResourceRequirements, 1)
	requirements, requiredCapabilities := s.applyLoadedSkillResourceRequirements(ctx, requirements)
	if _, err := normalizeManagedEnvironmentResourceRequirements(requirements); err != nil {
		return nil, err
	}
	accelerator := "none"
	if requirements.Accelerator == "required" {
		accelerator = "required"
	}
	spec := localcontainer.Spec{Image: request.Image, Accelerator: accelerator, Network: request.Network}
	preflight, err := manager.Preflight(ctx, spec)
	if err != nil {
		return map[string]any{
			"tool": manageEnvironmentsToolName, "provider": localcontainer.ProviderID,
			"ok": true, "mode": "preflight", "executed": false, "feasible": false,
			"status": "container_preflight_failed", "implementation": request.Implementation,
			"requirements": requirements, "required_capabilities": requiredCapabilities,
			"machine":    s.managedEnvironmentMachineSnapshot(ctx, identity),
			"diagnostic": boundedManagedEnvironmentError(err),
			"recovery":   "Keep the selected implementation and image authority. Repair the Docker daemon, NVIDIA runtime, image reference, registry access, or host capacity identified by the diagnostic, then repeat this exact preflight. Do not substitute another scientific implementation without a new user decision.",
		}, nil
	}
	preflightResult := map[string]any{
		"tool": manageEnvironmentsToolName, "provider": localcontainer.ProviderID,
		"ok": true, "mode": "preflight", "executed": false, "feasible": preflight.Ready,
		"status": "container_preflight_ready", "implementation": request.Implementation,
		"requirements": requirements, "required_capabilities": requiredCapabilities,
		"machine":           s.managedEnvironmentMachineSnapshot(ctx, identity),
		"container_runtime": preflight.Resources, "image": preflight.Spec.Image,
		"accelerator": preflight.Spec.Accelerator, "network": preflight.Spec.Network,
		"resource_requirements_verified": false,
		"resource_requirements_note":     "CPU, memory, disk, and accelerator-memory minima remain advisory unless supplied by a reviewed Skill or current official source; the observed Docker and NVIDIA runtime checks are real preflight evidence.",
	}
	if request.Mode == "preflight" {
		return preflightResult, nil
	}
	if requirement, evidenceErr := s.managedEnvironmentSetupEvidenceRequirement(ctx, request.Implementation); evidenceErr != nil {
		return nil, evidenceErr
	} else if requirement != nil {
		requirement["provider"] = localcontainer.ProviderID
		requirement["container_preflight"] = preflightResult
		return requirement, nil
	}
	operation := func(operationCtx context.Context) map[string]any {
		environment, observed, prepareErr := manager.Prepare(operationCtx, spec, call.ID)
		if prepareErr != nil {
			return map[string]any{
				"tool": manageEnvironmentsToolName, "provider": localcontainer.ProviderID,
				"ok": false, "executed": true, "status": "container_prepare_failed",
				"implementation": request.Implementation, "diagnostic": boundedManagedEnvironmentError(prepareErr),
				"container_preflight": preflightResult,
				"recovery":            "Keep the selected implementation and the successfully downloaded layers. Inspect the Docker diagnostic, repair registry, storage, accelerator, or command-host state, and resume this same image preparation. Do not discard a healthy pull cache or change scientific software after a routine setup failure.",
			}
		}
		return map[string]any{
			"tool": manageEnvironmentsToolName, "provider": localcontainer.ProviderID,
			"ok": true, "executed": true, "status": "completed", "mode": environment.Disposition,
			"implementation": request.Implementation, "environment": environment,
			"container_preflight": observed,
			"next":                "Use the returned immutable environment name for governed execution. Reuse it for later steps instead of pulling or configuring the selected image again.",
		}
	}
	if !request.Background {
		return operation(ctx), nil
	}
	if s.workspaceStore == nil {
		return nil, errors.New("container environment background notifications are unavailable")
	}
	digest := sha256.Sum256([]byte(call.ID + "\x00" + localcontainer.ProviderID))
	operationID := "container-environment-" + hex.EncodeToString(digest[:12])
	notificationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("synon-container-environment:"+access.Frame.ID+":"+call.ID)).String()
	go func() {
		payload := operation(context.Background())
		payload["operation_id"] = operationID
		_, _, _ = s.workspaceStore.CreateNotification(context.Background(), workspace.CreateNotificationInput{
			ID: notificationID, SenderFrameID: access.Frame.ID, RecipientFrameID: access.Frame.ID,
			RootFrameID: access.Frame.RootFrameID, OwnerUserID: access.UserID,
			NotificationType: "cell_result", Payload: payload,
		})
	}()
	return map[string]any{
		"status": "running", "tool": manageEnvironmentsToolName, "provider": localcontainer.ProviderID,
		"operation_id": operationID, "notification_id": notificationID,
		"recovery": "Use wait_for_notification for the durable result. Image preparation has no implicit wall-clock deadline.",
	}, nil
}
