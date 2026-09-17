package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"synon-go/internal/memorytools"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type workspaceMemoryScopeResolution struct {
	Session        sessionstore.Session
	Scope          memorytools.Scope
	Enabled        bool
	DisabledReason workspaceMemoryDisabledReason
}

var errWorkspaceMemoryDisabled = errors.New("memory tools are disabled for this workspace; enable memory in workspace settings before using them")

func (s *Server) executeWorkspaceMemoryTool(ctx context.Context, sessionID, name string, input map[string]any) (any, bool, error) {
	switch name {
	case "read_memory", "write_memory", "search_memory":
	default:
		return nil, false, nil
	}
	if name == "write_memory" {
		run, ok := transcriptArtifactRunFromContext(ctx)
		if !ok || run.Authority == nil || s.workspaceStore == nil {
			return map[string]any{"error": "write_memory requires an active transcript runner"}, true, nil
		}
		stream := run.Authority.Stream
		rootFrameID := strings.TrimSpace(stream.RootFrameID)
		if rootFrameID == "" {
			rootFrameID = strings.TrimSpace(stream.FrameID)
		}
		scope := memorytools.Scope{
			UserID: stream.OwnerID, ProjectID: stream.ProjectID,
			FrameID: rootFrameID, SourceFrameID: stream.FrameID,
		}
		service := memorytools.NewWithConfig(s.workspaceStore, s.newMemoryClassifier(scope), s.memoryConfig)
		rawInput, err := json.Marshal(input)
		if err != nil {
			return map[string]any{"error": "write_memory input is invalid"}, true, nil
		}
		inputDigest := sha256.Sum256(rawInput)
		result, err := service.WriteClaimed(ctx, memorytools.WriteAuthority{
			Claim: run.Authority.Claim, SourceEventID: run.SourceEventID,
			InputSHA256: hex.EncodeToString(inputDigest[:]),
			FreshWriteAllowed: func(freshCtx context.Context) (bool, error) {
				return s.claimedMemoryWriteEnabled(freshCtx, stream)
			},
		})
		if err != nil {
			return map[string]any{"error": claimedMemoryToolError(err)}, true, nil
		}
		return map[string]any{
			"output": result.Output, "appended": result.Appended, "replaced": result.Replaced, "removed": result.Removed,
		}, true, nil
	}
	resolved, err := s.resolveWorkspaceMemoryScope(ctx, sessionID)
	if err != nil {
		return map[string]any{"error": boundedMemoryScopeError(name, err)}, true, nil
	}
	if !resolved.Enabled {
		if resolved.DisabledReason == workspaceMemoryDisabledReasonSession {
			return map[string]any{"error": memorytools.ErrMemoryUnavailable.Error()}, true, nil
		}
		return map[string]any{"error": errWorkspaceMemoryDisabled.Error()}, true, nil
	}
	scope := resolved.Scope
	service := memorytools.NewWithConfig(
		s.workspaceStore,
		s.newMemoryClassifierForModel(scope, sessionWorkspaceModel(resolved.Session)),
		s.memoryConfig,
	)
	switch name {
	case "read_memory":
		var request memorytools.ReadInput
		if err := decodeMemoryToolInput(input, &request); err != nil {
			return map[string]any{"error": "read_memory input is invalid"}, true, nil
		}
		result, err := service.Read(ctx, scope, request)
		if err != nil {
			return map[string]any{"error": boundedMemoryOperationError(name, err)}, true, nil
		}
		return map[string]any{"output": result.Output}, true, nil
	default:
		var request memorytools.SearchInput
		if err := decodeMemoryToolInput(input, &request); err != nil {
			return map[string]any{"error": "search_memory input is invalid"}, true, nil
		}
		result, err := service.Search(ctx, scope, request)
		if err != nil {
			return map[string]any{"error": boundedMemoryOperationError(name, err)}, true, nil
		}
		return map[string]any{"output": result.Output, "results_returned": result.ResultsReturned}, true, nil
	}
}

func boundedMemoryScopeError(toolName string, err error) string {
	message := strings.TrimSpace(err.Error())
	switch message {
	case "unavailable", "memory tools require a trusted agent session",
		"memory session does not match the active transcript",
		"memory session project does not match the trusted frame":
		return message
	default:
		log.Printf("memory tool scope failed: tool=%s code=scope_unavailable", toolName)
		return "unavailable"
	}
}

func boundedMemoryOperationError(toolName string, err error) string {
	if errors.Is(err, memorytools.ErrMemoryUnavailable) {
		return memorytools.ErrMemoryUnavailable.Error()
	}
	message := strings.TrimSpace(err.Error())
	for _, prefix := range []string{
		"Missing 'query' argument",
		"Unknown entity ",
		"Unknown category ",
		"'frame:<id>' for another session is not permitted",
	} {
		if strings.HasPrefix(message, prefix) {
			return message
		}
	}
	log.Printf("memory tool operation failed: tool=%s code=operation_failed", toolName)
	return toolName + " failed"
}

func (s *Server) claimedMemoryWriteEnabled(ctx context.Context, stream transcriptstore.Stream) (bool, error) {
	if s == nil || s.workspaceStore == nil {
		return false, errors.New("memory runtime is unavailable")
	}
	mode := ""
	modeExplicit := false
	if s.transcriptStore != nil {
		config, found, err := s.transcriptStore.LatestFrameRuntimeConfig(ctx, stream.UID, stream.OwnerID)
		if err != nil {
			return false, err
		}
		if found {
			mode, modeExplicit = workspaceMemoryModeValue(config)
		}
	}
	metadata, metadataFound, err := s.workspaceStore.GetFrameRuntimeMetadata(stream.FrameID)
	if err != nil {
		return false, err
	}
	var session sessionstore.Session
	if s.sessionStore != nil {
		sessionID := strings.TrimSpace(stream.SessionID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(stream.FrameID)
		}
		if sessionID != "" {
			var found bool
			session, found, err = s.sessionStore.Get(sessionID)
			if err != nil {
				return false, err
			}
			if !found {
				session = sessionstore.Session{}
			}
		}
	}
	if !modeExplicit {
		mode, modeExplicit = sessionWorkspaceMemoryModeWithFrame(session, metadata, metadataFound)
	}
	enabled, err, disabledReason := s.workspaceMemoryAccessEnabledReason(ctx, stream.OwnerID, stream.ProjectID, mode, modeExplicit)
	if err != nil {
		return false, err
	}
	if !enabled {
		if disabledReason == workspaceMemoryDisabledReasonSession {
			return false, memorytools.ErrMemoryUnavailable
		}
		return false, errWorkspaceMemoryDisabled
	}
	return true, nil
}

func claimedMemoryToolError(err error) string {
	switch {
	case errors.Is(err, memorytools.ErrMemoryClassifierUnavailable):
		return memorytools.ErrMemoryClassifierUnavailable.Error()
	case errors.Is(err, memorytools.ErrMemoryWriteRejected):
		return err.Error()
	case errors.Is(err, errWorkspaceMemoryDisabled):
		return errWorkspaceMemoryDisabled.Error()
	case errors.Is(err, memorytools.ErrMemoryUnavailable):
		return memorytools.ErrMemoryUnavailable.Error()
	default:
		return "write_memory failed"
	}
}

func (s *Server) resolveWorkspaceMemoryToolScope(ctx context.Context, sessionID string) (memorytools.Scope, error) {
	resolved, err := s.resolveWorkspaceMemoryScope(ctx, sessionID)
	if err != nil {
		return memorytools.Scope{}, err
	}
	if !resolved.Enabled {
		return memorytools.Scope{}, errors.New("unavailable")
	}
	return resolved.Scope, nil
}

// resolveWorkspaceMemoryScope derives memory ownership exclusively from the
// persisted session and Frame. Callers may inspect the session memory mode,
// but may not supply user/project/frame identity themselves.
func (s *Server) resolveWorkspaceMemoryScope(ctx context.Context, sessionID string) (workspaceMemoryScopeResolution, error) {
	if s == nil || s.workspaceStore == nil {
		return workspaceMemoryScopeResolution{}, errors.New("unavailable")
	}
	if ctx == nil {
		return workspaceMemoryScopeResolution{}, errors.New("unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return workspaceMemoryScopeResolution{}, errors.New("memory tools require a trusted agent session")
	}
	var transcriptAuthority *transcriptRunnerAuthority
	if run, ok := transcriptArtifactRunFromContext(ctx); ok {
		transcriptAuthority = run.Authority
	} else if run, ok := transcriptRunnerChatRunFromContext(ctx); ok {
		transcriptAuthority = run.Transcript
	}
	if transcriptAuthority != nil {
		stream := transcriptAuthority.Stream
		trustedSessionID := strings.TrimSpace(stream.SessionID)
		if trustedSessionID == "" {
			trustedSessionID = strings.TrimSpace(stream.FrameID)
		}
		if stream.Kind != transcriptstore.StreamKindFrameRef || trustedSessionID == "" || sessionID != trustedSessionID {
			return workspaceMemoryScopeResolution{}, errors.New("memory session does not match the active transcript")
		}
		access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, stream.FrameID)
		if err != nil {
			return workspaceMemoryScopeResolution{}, fmt.Errorf("resolve memory frame: %w", err)
		}
		if !found || access.UserID != stream.OwnerID || access.Frame.ProjectID != stream.ProjectID {
			return workspaceMemoryScopeResolution{}, errors.New("unavailable")
		}
		rootFrameID := strings.TrimSpace(stream.RootFrameID)
		if rootFrameID == "" {
			rootFrameID = strings.TrimSpace(stream.FrameID)
		}
		enabled, err := s.claimedMemoryWriteEnabled(ctx, stream)
		if err != nil {
			return workspaceMemoryScopeResolution{}, err
		}
		return workspaceMemoryScopeResolution{
			Scope: memorytools.Scope{
				UserID: stream.OwnerID, ProjectID: stream.ProjectID,
				FrameID: rootFrameID, SourceFrameID: stream.FrameID,
			},
			Enabled: enabled,
		}, nil
	}
	if s.sessionStore == nil {
		return workspaceMemoryScopeResolution{}, errors.New("unavailable")
	}
	session, found, err := s.sessionStore.Get(sessionID)
	if err != nil {
		return workspaceMemoryScopeResolution{}, fmt.Errorf("resolve memory session: %w", err)
	}
	if !found {
		return workspaceMemoryScopeResolution{}, errors.New("unavailable")
	}
	frameID := strings.TrimSpace(stringValue(session.Orchestration["frame_id"]))
	if frameID == "" {
		frameID = strings.TrimSpace(stringValue(session.Orchestration["frameId"]))
	}
	if frameID == "" {
		frameID = session.ID
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, frameID)
	if err != nil {
		return workspaceMemoryScopeResolution{}, fmt.Errorf("resolve memory frame: %w", err)
	}
	if !found {
		return workspaceMemoryScopeResolution{}, errors.New("unavailable")
	}
	if session.Project != nil {
		sessionProjectID := strings.TrimSpace(session.Project.ID)
		if sessionProjectID != "" && sessionProjectID != access.Frame.ProjectID {
			return workspaceMemoryScopeResolution{}, errors.New("memory session project does not match the trusted frame")
		}
	}
	metadata, metadataFound, err := s.workspaceStore.GetFrameRuntimeMetadata(access.Frame.ID)
	if err != nil {
		return workspaceMemoryScopeResolution{}, fmt.Errorf("resolve memory frame metadata: %w", err)
	}
	rootFrameID := strings.TrimSpace(access.Frame.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = access.Frame.ID
	}
	mode, modeExplicit := sessionWorkspaceMemoryModeWithFrame(session, metadata, metadataFound)
	enabled, err, disabledReason := s.workspaceMemoryAccessEnabledReason(ctx, access.UserID, access.Frame.ProjectID, mode, modeExplicit)
	if err != nil {
		return workspaceMemoryScopeResolution{}, fmt.Errorf("resolve memory policy: %w", err)
	}
	return workspaceMemoryScopeResolution{
		Session: session,
		Scope: memorytools.Scope{
			UserID: access.UserID, ProjectID: access.Frame.ProjectID,
			FrameID: rootFrameID, SourceFrameID: access.Frame.ID,
		},
		Enabled: enabled, DisabledReason: disabledReason,
	}, nil
}

func decodeMemoryToolInput(input map[string]any, target any) error {
	if input == nil {
		input = map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode memory tool input: %w", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode memory tool input: %w", err)
	}
	return nil
}

func sessionWorkspaceModel(session sessionstore.Session) string {
	for _, key := range []string{"sessionConfig", "session_config"} {
		configuration, _ := session.Orchestration[key].(map[string]any)
		if model := strings.TrimSpace(stringValue(configuration["model"])); model != "" {
			return model
		}
	}
	return strings.TrimSpace(stringValue(session.Orchestration["model"]))
}

func sessionWorkspaceMemoryEnabledWithFrame(
	session sessionstore.Session,
	metadata workspace.FrameRuntimeMetadata,
	metadataFound bool,
) bool {
	mode, found := sessionWorkspaceMemoryModeWithFrame(session, metadata, metadataFound)
	return !found || mode != "off"
}

func sessionWorkspaceMemoryModeWithFrame(
	session sessionstore.Session,
	metadata workspace.FrameRuntimeMetadata,
	metadataFound bool,
) (string, bool) {
	mode, found := sessionWorkspaceMemoryMode(session)
	if !found && metadataFound {
		mode, found = frameWorkspaceMemoryMode(metadata)
	}
	return mode, found
}

func sessionWorkspaceMemoryMode(session sessionstore.Session) (string, bool) {
	configuration, _ := session.Orchestration["sessionConfig"].(map[string]any)
	if configuration == nil {
		configuration, _ = session.Orchestration["session_config"].(map[string]any)
	}
	return workspaceMemoryModeValue(configuration)
}

func workspaceMemoryModeValue(values map[string]any) (string, bool) {
	if value, exists := values["memory_mode"]; exists {
		mode, _ := value.(string)
		return mode, true
	}
	if value, exists := values["memoryMode"]; exists {
		mode, _ := value.(string)
		return mode, true
	}
	return "", false
}
