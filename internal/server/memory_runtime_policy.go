package server

import (
	"context"
	"errors"
	"strings"
)

// workspaceMemoryDisabledReason distinguishes why memory tools are disabled.
// A workspace/user-level preference warrants actionable settings guidance,
// while a session/frame-level opt-out is reported as unavailable so the model
// does not receive guidance that contradicts the user's explicit session mode.
type workspaceMemoryDisabledReason uint8

const (
	workspaceMemoryDisabledReasonUser workspaceMemoryDisabledReason = iota
	workspaceMemoryDisabledReasonSession
)

// workspaceMemoryAccessEnabled resolves explicit memory-tool availability.
// Runtime configuration supplies the default for users without a preference;
// a user preference is authoritative, and the active session may opt out.
func (s *Server) workspaceMemoryAccessEnabled(
	ctx context.Context,
	userID string,
	projectID string,
	sessionMode string,
	sessionModeExplicit bool,
) (bool, error) {
	enabled, err, _ := s.workspaceMemoryAccessEnabledReason(ctx, userID, projectID, sessionMode, sessionModeExplicit)
	return enabled, err
}

// workspaceMemoryAccessEnabledReason is the reason-aware variant used by
// memory tool execution so callers can keep actionable disable guidance
// separate from silent session opt-outs.
func (s *Server) workspaceMemoryAccessEnabledReason(
	ctx context.Context,
	userID string,
	projectID string,
	sessionMode string,
	sessionModeExplicit bool,
) (bool, error, workspaceMemoryDisabledReason) {
	if s == nil || s.workspaceStore == nil {
		return false, nil, workspaceMemoryDisabledReasonUser
	}
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	if userID == "" {
		return false, errors.New("memory user id is required"), workspaceMemoryDisabledReasonUser
	}
	userEnabled, err := s.workspaceStore.MemoryEnabledWithDefault(ctx, userID, s.memoryConfig.Enabled)
	if err != nil {
		return false, err, workspaceMemoryDisabledReasonUser
	}
	if !userEnabled {
		return false, nil, workspaceMemoryDisabledReasonUser
	}
	if sessionModeExplicit && sessionMode == "off" {
		return false, nil, workspaceMemoryDisabledReasonSession
	}
	return true, nil, workspaceMemoryDisabledReasonUser
}
