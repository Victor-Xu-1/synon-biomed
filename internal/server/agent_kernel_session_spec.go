package server

import (
	"context"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

// agentKernelSessionRuntime contains the execution attributes that are part of
// a stable kernel identity plus the public tool identity used to derive its
// mutable authority. Keep authority assembly centralized: callers that
// pre-create or resume a stable session must present the same complete mounts,
// protected paths, and egress policy as the eventual execution path.
type agentKernelSessionRuntime struct {
	PublicName        string
	Kind              string
	Language          string
	Environment       string
	RuntimeGeneration string
	ExcludePackID     string
}

func (s *Server) agentKernelSessionSpec(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	workspaceDir string,
	runtime agentKernelSessionRuntime,
) (kernelruntime.SessionSpec, error) {
	protectedPaths, err := s.agentKernelProtectedPaths()
	if err != nil {
		return kernelruntime.SessionSpec{}, err
	}
	mounts, err := s.agentKernelConfinementMounts(access.UserID, workspaceDir, protectedPaths)
	if err != nil {
		return kernelruntime.SessionSpec{}, err
	}
	immutableMounts, err := s.agentKernelWorkspaceImmutableMounts(
		ctx, access, workspaceDir, runtime.ExcludePackID,
	)
	if err != nil {
		return kernelruntime.SessionSpec{}, err
	}
	mounts = append(mounts, immutableMounts...)
	egressAllowed, egressDenied, caBundle, upstreamProxy, err := s.agentKernelEgressPolicy(
		runtime.PublicName, access.Frame.ID,
	)
	if err != nil {
		return kernelruntime.SessionSpec{}, err
	}
	return kernelruntime.SessionSpec{
		OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: access.Frame.AgentName, DelegateName: access.DelegateName,
		KernelKind: runtime.Kind, Language: runtime.Language,
		Environment: runtime.Environment, RuntimeGeneration: runtime.RuntimeGeneration,
		WorkspaceDir: workspaceDir, Mounts: mounts, ProtectedPaths: protectedPaths,
		EgressAllowedDomains: egressAllowed, EgressDeniedDomains: egressDenied,
		CABundle: caBundle, UpstreamProxy: upstreamProxy,
	}, nil
}
