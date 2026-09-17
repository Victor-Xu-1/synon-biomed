package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/providers"
)

const webConversationProviderSelectionPrefix = "provider:"

type sessionConversationModelSnapshot struct {
	Selection   string
	OwnerUserID string
	Revision    int64
}

func webConversationModelSelectionFromContext(contextData map[string]any) string {
	assistant, _ := contextData["web_assistant"].(map[string]any)
	overrides, _ := assistant["conversation_overrides"].(map[string]any)
	return strings.TrimSpace(webString(overrides["model"]))
}

func webConversationModelRevisionFromContext(contextData map[string]any) int64 {
	assistant, _ := contextData["web_assistant"].(map[string]any)
	overrides, _ := assistant["conversation_overrides"].(map[string]any)
	switch value := overrides["model_revision"].(type) {
	case float64:
		return int64(value)
	case float32:
		return int64(value)
	case int:
		return int64(value)
	case int64:
		return value
	case json.Number:
		revision, _ := value.Int64()
		return revision
	default:
		return 0
	}
}

func (s *Server) webConversationRootModelSnapshot(rootFrameID string) (sessionConversationModelSnapshot, error) {
	if s == nil || s.workspaceStore == nil {
		return sessionConversationModelSnapshot{}, nil
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(strings.TrimSpace(rootFrameID))
	if err != nil || !found {
		return sessionConversationModelSnapshot{}, err
	}
	return sessionConversationModelSnapshot{
		Selection: webConversationModelSelectionFromContext(metadata.ContextData),
		Revision:  webConversationModelRevisionFromContext(metadata.ContextData),
	}, nil
}

func (s *Server) webConversationRootModelSelection(rootFrameID string) (string, error) {
	snapshot, err := s.webConversationRootModelSnapshot(rootFrameID)
	return snapshot.Selection, err
}

func (s *Server) webConversationFrameModelSelection(frame workspace.CompatibilityFrame) (string, error) {
	rootFrameID := strings.TrimSpace(frame.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = strings.TrimSpace(frame.ID)
	}
	return s.webConversationRootModelSelection(rootFrameID)
}

func (s *Server) sessionConversationModelSelection(sessionID string) (string, string, error) {
	snapshot, err := s.sessionConversationModelSnapshot(sessionID)
	return snapshot.Selection, snapshot.OwnerUserID, err
}

func (s *Server) sessionConversationModelSnapshot(sessionID string) (sessionConversationModelSnapshot, error) {
	return s.sessionConversationModelSnapshotWithContext(context.Background(), sessionID)
}

func (s *Server) sessionConversationModelSnapshotWithContext(ctx context.Context, sessionID string) (sessionConversationModelSnapshot, error) {
	if s == nil || s.workspaceStore == nil {
		return sessionConversationModelSnapshot{}, nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContextWithContext(ctx, strings.TrimSpace(sessionID))
	if err != nil || !found {
		return sessionConversationModelSnapshot{}, err
	}
	rootFrameID := strings.TrimSpace(frameContext.Frame.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = strings.TrimSpace(frameContext.Frame.ID)
	}
	snapshot, err := s.webConversationRootModelSnapshotWithContext(ctx, rootFrameID)
	snapshot.OwnerUserID = strings.TrimSpace(frameContext.UserID)
	return snapshot, err
}

func (s *Server) webConversationRootModelSnapshotWithContext(ctx context.Context, rootFrameID string) (sessionConversationModelSnapshot, error) {
	if s == nil || s.workspaceStore == nil {
		return sessionConversationModelSnapshot{}, nil
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadataWithContext(ctx, strings.TrimSpace(rootFrameID))
	if err != nil || !found {
		return sessionConversationModelSnapshot{}, err
	}
	return sessionConversationModelSnapshot{
		Selection: webConversationModelSelectionFromContext(metadata.ContextData),
		Revision:  webConversationModelRevisionFromContext(metadata.ContextData),
	}, nil
}

func (s *Server) resolveUserModelSelectionProfile(
	userID string,
	selection string,
	input providers.ResolutionInput,
) (providers.ModelProfile, error) {
	if s == nil {
		return providers.ModelProfile{}, errors.New("model selection runtime is unavailable")
	}
	selection = strings.TrimSpace(selection)
	if strings.HasPrefix(selection, webConversationProviderSelectionPrefix) {
		return providers.ResolveUserProviderProfile(
			s.workspaceStore,
			s.secretStore,
			userID,
			strings.TrimSpace(strings.TrimPrefix(selection, webConversationProviderSelectionPrefix)),
			input,
		)
	}
	return providers.ResolveUserModelProfile(
		s.settingsStore, s.workspaceStore, s.secretStore, userID, selection, input,
	)
}

func (s *Server) resolveUserModelSelectionProfileWithContext(
	ctx context.Context, userID string, selection string, input providers.ResolutionInput,
) (providers.ModelProfile, error) {
	input.Context = ctx
	if s == nil {
		return providers.ModelProfile{}, errors.New("model selection runtime is unavailable")
	}
	selection = strings.TrimSpace(selection)
	if strings.HasPrefix(selection, webConversationProviderSelectionPrefix) {
		return providers.ResolveUserProviderProfile(
			s.workspaceStore, s.secretStore, userID,
			strings.TrimSpace(strings.TrimPrefix(selection, webConversationProviderSelectionPrefix)), input,
		)
	}
	return providers.ResolveUserModelProfile(s.settingsStore, s.workspaceStore, s.secretStore, userID, selection, input)
}
