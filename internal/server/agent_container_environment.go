package server

import (
	"context"
	"errors"
	"strings"

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
	requirements, requiredCapabilities, capabilitySkill := s.applyImplementationResourceRequirements(request.Implementation, requirements)
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
			"capability_contract_skill": capabilitySkill,
			"machine":                   s.managedEnvironmentMachineSnapshot(ctx, identity),
			"diagnostic":                boundedManagedEnvironmentError(err),
			"recovery":                  "Keep the selected implementation and image authority. Repair the Docker daemon, NVIDIA runtime, image reference, registry access, or host capacity identified by the diagnostic, then repeat this exact preflight. Do not substitute another scientific implementation without a new user decision.",
		}, nil
	}
	preflightResult := map[string]any{
		"tool": manageEnvironmentsToolName, "provider": localcontainer.ProviderID,
		"ok": true, "mode": "preflight", "executed": false, "feasible": preflight.Ready,
		"status": "container_preflight_ready", "implementation": request.Implementation,
		"requirements": requirements, "required_capabilities": requiredCapabilities,
		"capability_contract_skill": capabilitySkill,
		"machine":                   s.managedEnvironmentMachineSnapshot(ctx, identity),
		"container_runtime":         preflight.Resources, "image": preflight.Spec.Image,
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
	operationID, err := stableServerOperationID(
		"container-environment",
		access.UserID+"\x00"+access.Frame.RootFrameID+"\x00"+manageEnvironmentsToolName,
		stableAuthorityInput(map[string]any{
			"tool": manageEnvironmentsToolName, "provider": localcontainer.ProviderID, "mode": request.Mode,
			"implementation": request.Implementation, "image": spec.Image, "accelerator": spec.Accelerator,
			"network": spec.Network, "requirements": requirements,
		}),
	)
	if err != nil {
		return nil, err
	}
	return s.executeManagedEnvironmentOperation(ctx, access, call, manageEnvironmentsToolName, request.Background, map[string]any{
		"operation_id": operationID, "provider": localcontainer.ProviderID, "implementation": request.Implementation, "container_preflight": preflightResult,
	}, managedOperationRequest{Kind: "container", Container: &spec}, s.kernelManager)
}
