package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"synon-go/internal/agentruntime"
	"synon-go/internal/memoryextract"
	"synon-go/internal/memorytools"
	sessionstore "synon-go/internal/persistence/sessions"
)

type memoryExtractionExtractorFactory func(memorytools.Scope, string, *agentruntime.ModelRequest) memoryextract.Extractor

type memoryExtractorFunc func(context.Context, memoryextract.ExtractRequest) (map[string]any, error)

func (extract memoryExtractorFunc) ExtractMemories(ctx context.Context, request memoryextract.ExtractRequest) (map[string]any, error) {
	return extract(ctx, request)
}

type memoryExtractionSessionState struct {
	runMu         sync.Mutex
	latestEventID int64
	request       *agentruntime.ModelRequest
}

// memoryExtractionRuntime is the sole post-completion memory authority. It
// owns cadence, request snapshots, event de-duplication and detached task
// draining; durable facts remain owned by workspace persistence.
type memoryExtractionRuntime struct {
	server  *Server
	tasks   *memoryextract.PostCompletionTasks
	cadence *memoryextract.Cadence
	factory memoryExtractionExtractorFactory

	mu       sync.Mutex
	sessions map[string]*memoryExtractionSessionState
}

func newMemoryExtractionRuntime(server *Server) *memoryExtractionRuntime {
	return newMemoryExtractionRuntimeWithFactory(server, func(scope memorytools.Scope, mode string, request *agentruntime.ModelRequest) memoryextract.Extractor {
		switch mode {
		case "compact":
			return server.newMemoryCompactExtractor(scope)
		case "forked":
			return server.newMemoryForkedExtractor(scope, request)
		default:
			return memoryExtractorFunc(func(context.Context, memoryextract.ExtractRequest) (map[string]any, error) {
				return nil, fmt.Errorf("unsupported memory extraction mode %q", mode)
			})
		}
	})
}

func newMemoryExtractionRuntimeWithFactory(server *Server, factory memoryExtractionExtractorFactory) *memoryExtractionRuntime {
	return &memoryExtractionRuntime{
		server: server, tasks: memoryextract.NewPostCompletionTasks(), cadence: memoryextract.NewCadence(),
		factory: factory, sessions: make(map[string]*memoryExtractionSessionState),
	}
}

func (runtime *memoryExtractionRuntime) rememberLastRequest(sessionID string, request agentruntime.ModelRequest) {
	if runtime == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	copyRequest := request
	copyRequest.Messages = append([]agentruntime.Message(nil), request.Messages...)
	runtime.mu.Lock()
	state := runtime.sessions[sessionID]
	if state == nil {
		state = &memoryExtractionSessionState{}
		runtime.sessions[sessionID] = state
	}
	state.request = &copyRequest
	runtime.mu.Unlock()
}

func (runtime *memoryExtractionRuntime) ScheduleCompletedRootEvent(sessionID string, eventID int64) <-chan error {
	if runtime == nil || runtime.tasks == nil || runtime.server == nil {
		return completedMemoryExtractionResult(errors.New("memory extraction runtime is unavailable"))
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || eventID <= 0 {
		return completedMemoryExtractionResult(errors.New("completed memory extraction identity is required"))
	}
	runtime.mu.Lock()
	state := runtime.sessions[sessionID]
	if state == nil {
		state = &memoryExtractionSessionState{}
		runtime.sessions[sessionID] = state
	}
	if state.latestEventID >= eventID {
		runtime.mu.Unlock()
		return completedMemoryExtractionResult(nil)
	}
	state.latestEventID = eventID
	runtime.mu.Unlock()
	return runtime.tasks.Go(func() error {
		state.runMu.Lock()
		defer state.runMu.Unlock()
		defer runtime.releaseSessionState(sessionID, state, eventID)
		result, err := runtime.RunCompletedRoot(context.Background(), sessionID)
		if err != nil {
			log.Printf("extract completed root memory session_id=%q event_id=%d: %v", sessionID, eventID, err)
		} else {
			log.Printf(
				"extract completed root memory session_id=%q event_id=%d gate=%q cursor_before=%d cursor_after=%d cursor_advanced=%t appended=%d replaced=%d removed=%d apply_failures=%d extractor_error=%q cursor_error=%q",
				sessionID, eventID, result.Gate, result.CursorBefore, result.CursorAfter, result.CursorAdvanced,
				len(result.Applied.Appended), len(result.Applied.Replaced), len(result.Applied.Removed), len(result.Applied.Failures),
				result.ExtractorError, result.CursorError,
			)
		}
		return err
	})
}

func (runtime *memoryExtractionRuntime) releaseSessionState(sessionID string, state *memoryExtractionSessionState, eventID int64) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.sessions[sessionID] == state && state.latestEventID == eventID {
		delete(runtime.sessions, sessionID)
	}
}

func completedMemoryExtractionResult(err error) <-chan error {
	result := make(chan error, 1)
	if err != nil {
		result <- err
	}
	close(result)
	return result
}

func (runtime *memoryExtractionRuntime) RunCompletedRoot(ctx context.Context, sessionID string) (memoryextract.RunResult, error) {
	result := memoryextract.RunResult{Gate: "start"}
	if runtime == nil || runtime.server == nil || runtime.server.workspaceStore == nil {
		return result, errors.New("memory extraction runtime is unavailable")
	}
	resolved, err := runtime.resolveCompletedRootMemoryScope(ctx, sessionID)
	if err != nil {
		return result, err
	}
	if !resolved.Enabled {
		result.Gate = "disabled"
		return result, nil
	}
	if resolved.Scope.SourceFrameID != resolved.Scope.FrameID {
		result.Gate = "not-root"
		return result, nil
	}
	metadata, metadataFound, err := runtime.server.workspaceStore.GetFrameRuntimeMetadata(resolved.Scope.SourceFrameID)
	if err != nil {
		return result, err
	}
	asideParent := metadataFound && (metadata.InputData["_aside_parent"] != nil || metadata.ContextData["_aside_parent"] != nil)
	if asideParent {
		result.Gate = "disabled-aside"
		return result, nil
	}
	projectEnabled, err := runtime.server.workspaceStore.ProjectMemoryEnabledSetting(ctx, resolved.Scope.ProjectID, resolved.Scope.UserID)
	if err != nil {
		return result, err
	}
	if projectEnabled != nil && !*projectEnabled {
		result.Gate = "disabled"
		return result, nil
	}
	config := memoryextract.ConfigFromMemoryConfig(runtime.server.memoryConfig)
	if !config.ExtractEnabled {
		result.Gate = "disabled"
		return result, nil
	}
	autoEnabled, err := runtime.server.memoryAutoExtractionEnabled(resolved.Scope.UserID)
	if err != nil {
		return result, err
	}
	if !autoEnabled {
		result.Gate = "disabled-auto"
		return result, nil
	}
	snapshot, err := runtime.readMemoryExtractionSnapshot(ctx, resolved.Scope.UserID, sessionID)
	if err != nil {
		return result, err
	}
	if snapshot.TerminalStatus != "completed" {
		result.Gate = "not-completed"
		return result, nil
	}
	runtime.mu.Lock()
	state := runtime.sessions[sessionID]
	var request *agentruntime.ModelRequest
	if state != nil && state.request != nil {
		copyRequest := *state.request
		copyRequest.Messages = append([]agentruntime.Message(nil), state.request.Messages...)
		request = &copyRequest
	}
	runtime.mu.Unlock()
	if config.ExtractMode == "forked" && request == nil {
		result.Gate = "no-last-request-view"
		return result, nil
	}
	if runtime.factory == nil {
		return result, errors.New("memory extraction model factory is unavailable")
	}
	extractor := runtime.factory(resolved.Scope, config.ExtractMode, request)
	service := memoryextract.NewService(
		runtime.server.workspaceStore,
		runtime.server.newMemoryClassifierForModel(resolved.Scope, sessionWorkspaceModel(resolved.Session)),
		runtime.server.newMemoryLiteralRepairer(resolved.Scope),
	)
	runner := memoryextract.NewRunnerWithCadence(runtime.server.workspaceStore, service, extractor, runtime.cadence)
	mode, _ := sessionWorkspaceMemoryMode(resolved.Session)
	recalledBodies := []string(nil)
	if config.ExtractMode == "forked" && request != nil {
		recalledBodies, err = runtime.recalledBodies(ctx, resolved.Scope, memoryExtractionRequestViewMessages(request.Messages))
		if err != nil {
			return result, err
		}
	}
	return runner.Run(ctx, memoryextract.RunInput{
		Scope: memoryextract.ApplyScope{
			UserID: resolved.Scope.UserID, ProjectID: resolved.Scope.ProjectID, SourceFrameID: resolved.Scope.SourceFrameID,
		},
		SessionMemoryMode: mode,
		Model:             sessionWorkspaceModel(resolved.Session),
		Messages:          snapshot.Messages,
		FinalResponseText: snapshot.FinalResponseText,
		RecalledBodies:    recalledBodies,
		Config:            config,
	})
}

// resolveCompletedRootMemoryScope keeps post-completion extraction on durable
// workspace authority. Web conversations do not have to remain in the legacy
// session index after their runner settles, so a missing compatibility session
// must not discard an otherwise trusted completed frame.
func (runtime *memoryExtractionRuntime) resolveCompletedRootMemoryScope(
	ctx context.Context,
	sessionID string,
) (workspaceMemoryScopeResolution, error) {
	resolved, err := runtime.server.resolveWorkspaceMemoryScope(ctx, sessionID)
	if err == nil || strings.TrimSpace(err.Error()) != "unavailable" {
		return resolved, err
	}
	access, found, accessErr := runtime.server.workspaceStore.GetKernelFrameAccessContext(ctx, sessionID)
	if accessErr != nil {
		return workspaceMemoryScopeResolution{}, accessErr
	}
	if !found || strings.TrimSpace(access.UserID) == "" || strings.TrimSpace(access.Frame.ProjectID) == "" {
		return workspaceMemoryScopeResolution{}, err
	}
	metadata, metadataFound, metadataErr := runtime.server.workspaceStore.GetFrameRuntimeMetadata(access.Frame.ID)
	if metadataErr != nil {
		return workspaceMemoryScopeResolution{}, metadataErr
	}
	session := sessionstore.Session{
		ID:            access.Frame.ID,
		Project:       &sessionstore.Project{ID: access.Frame.ProjectID},
		Orchestration: map[string]any{"frame_id": access.Frame.ID},
	}
	mode, modeExplicit := sessionWorkspaceMemoryModeWithFrame(session, metadata, metadataFound)
	enabled, policyErr, disabledReason := runtime.server.workspaceMemoryAccessEnabledReason(
		ctx, access.UserID, access.Frame.ProjectID, mode, modeExplicit,
	)
	if policyErr != nil {
		return workspaceMemoryScopeResolution{}, policyErr
	}
	rootFrameID := strings.TrimSpace(access.Frame.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = access.Frame.ID
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

func memoryExtractionRequestViewMessages(messages []agentruntime.Message) []memoryextract.Message {
	result := make([]memoryextract.Message, 0, len(messages))
	for _, message := range messages {
		blocks := make([]memoryextract.Block, 0, len(message.Parts)+1)
		if strings.TrimSpace(message.Content) != "" {
			blocks = append(blocks, memoryextract.Block{Type: memoryextract.BlockText, Text: message.Content})
		}
		for _, part := range message.Parts {
			if part.Type == agentruntime.ContentPartText && strings.TrimSpace(part.Text) != "" {
				blocks = append(blocks, memoryextract.Block{Type: memoryextract.BlockText, Text: part.Text})
			}
		}
		if len(blocks) > 0 {
			result = append(result, memoryextract.Message{Role: message.Role, Content: blocks})
		}
	}
	return result
}

func (runtime *memoryExtractionRuntime) recalledBodies(ctx context.Context, scope memorytools.Scope, messages []memoryextract.Message) ([]string, error) {
	manifest, err := runtime.server.workspaceStore.ListMemoryExtractionRows(ctx, scope.UserID, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	mutable := make(map[string]struct{}, len(manifest))
	for _, memory := range manifest {
		if memory.SubjectProjectID != "" || memory.SubjectArtifactID != "" || memory.SubjectVersionID != "" {
			mutable[strings.ToLower(memory.ID)] = struct{}{}
		}
	}
	ids := make(map[string]struct{})
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type != memoryextract.BlockText {
				continue
			}
			for _, recall := range workspaceMemoryRecallBlockPattern.FindAllStringSubmatch(block.Text, -1) {
				if len(recall) != 2 {
					continue
				}
				for _, match := range workspaceMemoryRecallIDPattern.FindAllStringSubmatch(recall[1], -1) {
					if len(match) == 2 {
						ids[strings.ToLower(match[1])] = struct{}{}
					}
				}
			}
		}
	}
	bodies := make([]string, 0, len(ids))
	for id := range ids {
		if _, currentManifestRow := mutable[id]; currentManifestRow {
			continue
		}
		memory, found, err := runtime.server.workspaceStore.GetMemoryOwned(ctx, scope.UserID, id)
		if err != nil {
			return nil, err
		}
		if found && strings.TrimSpace(memory.Body) != "" {
			bodies = append(bodies, memory.Body)
		}
	}
	return bodies, nil
}

func (runtime *memoryExtractionRuntime) Drain(ctx context.Context) memoryextract.PostCompletionDrainResult {
	if runtime == nil || runtime.tasks == nil {
		return memoryextract.PostCompletionDrainResult{}
	}
	return runtime.tasks.Drain(ctx)
}

type memoryExtractionRecordingModelClient struct {
	delegate  agentruntime.ModelClient
	runtime   *memoryExtractionRuntime
	sessionID string
}

func (client *memoryExtractionRecordingModelClient) Complete(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	client.runtime.rememberLastRequest(client.sessionID, request)
	return client.delegate.Complete(ctx, request)
}

func (client *memoryExtractionRecordingModelClient) CompleteStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	client.runtime.rememberLastRequest(client.sessionID, request)
	if streaming, ok := client.delegate.(agentruntime.StreamingModelClient); ok {
		return streaming.CompleteStream(ctx, request, emit)
	}
	return client.delegate.Complete(ctx, request)
}
