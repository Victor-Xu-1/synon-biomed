package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	runtimekv "synon-go/internal/persistence/runtimekv"
)

const compatibilityRuntimeCleanupTimeout = 10 * time.Second

// compatibilityRuntimeCleanupContext gives runtime shutdown a bounded window
// while allowing cleanup to finish after a client disconnects or cancels its
// HTTP request. The durable frame-tree transaction remains owned by the
// workspace store; these helpers only handle volatile/session side effects.
func compatibilityRuntimeCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), compatibilityRuntimeCleanupTimeout)
}

// stopCompatibilityFrameRuntime closes active transports before the durable
// frame deletion. It is safe to call again after the transaction as a bounded
// retry when a runtime races with deletion.
func (s *Server) stopCompatibilityFrameRuntime(ctx context.Context, frameID string) []error {
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return []error{fmt.Errorf("frame runtime id is empty")}
	}
	warnings := make([]error, 0, 1)
	if s != nil && s.sessionSockets != nil {
		s.sessionSockets.CloseSession(frameID)
	}
	if s != nil && s.kernelManager != nil {
		if _, err := s.kernelManager.CloseSession(ctx, frameID); err != nil {
			warnings = append(warnings, fmt.Errorf("stop runtime %s: %w", frameID, err))
		}
	}
	return warnings
}

// removeCompatibilityFrameRuntime removes durable runner/session projections
// only after the workspace frame transaction has committed. A failure here is
// a cleanup warning, never a reason to report a committed user deletion as a
// failed delete.
func (s *Server) removeCompatibilityFrameRuntime(frameID string) []error {
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return []error{fmt.Errorf("frame runtime id is empty")}
	}
	warnings := make([]error, 0, 3)
	if s != nil && s.sessionStore != nil {
		if _, err := s.sessionStore.Delete(frameID); err != nil {
			warnings = append(warnings, fmt.Errorf("remove session %s: %w", frameID, err))
		}
	}
	if s != nil && s.eventJournal != nil {
		if err := s.eventJournal.Remove(frameID); err != nil {
			warnings = append(warnings, fmt.Errorf("remove session journal %s: %w", frameID, err))
		}
	}
	if s != nil && s.runtimeStore != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), compatibilityRuntimeCleanupTimeout)
		defer cancel()
		if _, err := s.runtimeStore.DeleteScope(cleanupCtx, runtimekv.Scope{
			RootFrameID: frameID, FrameID: frameID,
		}); err != nil {
			warnings = append(warnings, fmt.Errorf("remove runtime state %s: %w", frameID, err))
		}
	}
	return warnings
}

func (s *Server) removeCompatibilityProjectRuntime(projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project runtime id is empty")
	}
	if s == nil || s.runtimeStore == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), compatibilityRuntimeCleanupTimeout)
	defer cancel()
	_, err := s.runtimeStore.DeleteScope(cleanupCtx, runtimekv.Scope{ProjectID: projectID})
	return err
}
