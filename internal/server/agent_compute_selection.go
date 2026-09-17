package server

import (
	"context"
	"strings"

	kernelruntime "synon-go/internal/kernel"
)

const localComputeProviderID = "local"

// resolveSelectedComputeProvider is the sole authority bridge from the
// conversation selector to an execution provider. An untouched selector and
// an explicit local choice both mean the local machine. Explicit remote
// choices are preserved and can never be silently rerouted to local.
func (s *Server) resolveSelectedComputeProvider(ctx context.Context, userID, rootFrameID string) (string, error) {
	if s == nil || s.workspaceStore == nil {
		return "", kernelruntime.NewHostCallError("unavailable", "compute provider selection authority is unavailable")
	}
	providers, _, err := s.workspaceStore.SessionComputeProviderSelection(userID, rootFrameID)
	if err != nil {
		return "", kernelruntime.NewHostCallError("unavailable", "compute provider selection is unavailable")
	}
	if len(providers) == 0 {
		return localComputeProviderID, nil
	}
	if len(providers) != 1 {
		return "", kernelruntime.NewHostCallError("invalid_arguments", "select exactly one compute provider before execution")
	}
	provider := strings.TrimSpace(providers[0])
	if provider == "" {
		return "", kernelruntime.NewHostCallError("invalid_arguments", "compute provider selection is invalid")
	}
	if _, found, err := s.workspaceStore.GetComputeProvider(provider, userID); err != nil || !found {
		return "", kernelruntime.NewHostCallError("unavailable", "selected compute provider is unavailable")
	}
	return provider, nil
}
