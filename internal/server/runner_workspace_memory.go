package server

import (
	"context"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/memoryprompt"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

var (
	workspaceMemoryRecallBlockPattern = regexp.MustCompile(`(?s)<memory_recall\b[^>]*>(.*?)</memory_recall>`)
	workspaceMemoryRecallIDPattern    = regexp.MustCompile(`(?i)\[(mem_[a-z0-9]+)\b`)
)

const workspaceMemoryServedRuntimeNamespace = "synon-memory-served"

type workspaceMemoryRecallScope struct {
	ownerUserID   string
	projectID     string
	frameID       string
	rootFrameID   string
	parentFrameID string
}

func (s *Server) workspaceMemoryContext(ctx context.Context, session sessionstore.Session, messages []chatCompletionMessage, taskQueries ...string) (string, error) {
	scope, enabled := s.resolveWorkspaceMemoryRecallScope(ctx, session)
	if !enabled {
		return "", nil
	}

	parts := make([]string, 0, 2)
	taskQuery := ""
	if len(taskQueries) > 0 {
		taskQuery = strings.TrimSpace(taskQueries[0])
	}
	if taskQuery == "" {
		taskQuery = promptContextFromMessages(messages)
	}
	systemParts := []string{memoryprompt.SystemRules()}
	if s.memoryConfig.ContextInSystemPrompt {
		if facts := s.workspaceMemoryFacts(ctx, scope.ownerUserID); facts != "" {
			systemParts = append(systemParts, facts)
		}
	}
	parts = append(parts, strings.Join(systemParts, "\n\n"))
	if recall := s.workspaceMemoryRecallSignal(
		ctx, scope.ownerUserID, scope.projectID, scope.frameID, scope.rootFrameID,
		scope.parentFrameID, taskQuery, "user_message", messages,
	); recall != "" {
		parts = append(parts, recall)
	}
	return strings.Join(parts, "\n\n"), nil
}

func (s *Server) resolveWorkspaceMemoryRecallScope(ctx context.Context, session sessionstore.Session) (workspaceMemoryRecallScope, bool) {
	scope := workspaceMemoryRecallScope{}
	if s == nil || s.workspaceStore == nil || session.Project == nil {
		return scope, false
	}
	scope.projectID = strings.TrimSpace(session.Project.ID)
	if scope.projectID == "" {
		return scope, false
	}
	ownerUserID, found, err := s.workspaceStore.ProjectOwnerIDContext(ctx, scope.projectID)
	if err != nil {
		log.Printf("load Synon memory owner project_id=%q: %v", scope.projectID, err)
		return scope, false
	}
	if !found {
		return scope, false
	}
	scope.ownerUserID = ownerUserID
	scope.frameID, scope.rootFrameID, found = s.workspaceMemoryRecallFrameScope(ctx, session, scope.ownerUserID, scope.projectID)
	if !found {
		return scope, false
	}
	metadata, metadataFound, err := s.workspaceStore.GetFrameRuntimeMetadata(scope.frameID)
	if err != nil {
		log.Printf("load Synon memory frame metadata frame_id=%q: %v", scope.frameID, err)
		return scope, false
	}
	if !sessionWorkspaceMemoryEnabledWithFrame(session, metadata, metadataFound) {
		return scope, false
	}
	enabled, err := s.workspaceStore.MemoryEnabledWithDefault(ctx, scope.ownerUserID, s.memoryConfig.Enabled)
	if err != nil {
		log.Printf("load Synon global memory setting project_id=%q: %v", scope.projectID, err)
		return scope, false
	}
	if !enabled {
		return scope, false
	}
	projectEnabled, err := s.workspaceStore.ProjectMemoryEnabled(ctx, scope.projectID, scope.ownerUserID)
	if err != nil {
		log.Printf("load Synon project memory setting project_id=%q: %v", scope.projectID, err)
		return scope, false
	}
	if !projectEnabled {
		return scope, false
	}
	scope.parentFrameID = sessionWorkspaceMemoryParentID(session)
	return scope, true
}

func (s *Server) workspaceMemoryRecallForSession(
	ctx context.Context,
	session sessionstore.Session,
	messages []chatCompletionMessage,
	query, kind string,
) string {
	scope, enabled := s.resolveWorkspaceMemoryRecallScope(ctx, session)
	if !enabled {
		return ""
	}
	return s.workspaceMemoryRecallSignal(
		ctx, scope.ownerUserID, scope.projectID, scope.frameID, scope.rootFrameID,
		scope.parentFrameID, query, kind, messages,
	)
}

func (s *Server) workspaceMemoryRecallFrameScope(
	ctx context.Context,
	session sessionstore.Session,
	ownerUserID, projectID string,
) (frameID, rootFrameID string, trusted bool) {
	frameID = strings.TrimSpace(stringValue(session.Orchestration["frame_id"]))
	if frameID == "" {
		frameID = strings.TrimSpace(stringValue(session.Orchestration["frameId"]))
	}
	if frameID == "" {
		frameID = strings.TrimSpace(session.ID)
	}
	if frameID == "" {
		return "", "", false
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, frameID)
	if err != nil {
		log.Printf("resolve Synon memory frame frame_id=%q: %v", frameID, err)
		return "", "", false
	}
	if !found {
		return "", "", false
	}
	if access.UserID != ownerUserID || access.Frame.ProjectID != projectID {
		log.Printf("reject Synon memory frame scope frame_id=%q", frameID)
		return "", "", false
	}
	rootFrameID = strings.TrimSpace(access.Frame.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = access.Frame.ID
	}
	return access.Frame.ID, rootFrameID, true
}

func (s *Server) workspaceMemoryFacts(ctx context.Context, ownerUserID string) string {
	profile, err := s.workspaceStore.ListMemorySystemProfileRows(ctx, ownerUserID)
	if err != nil {
		log.Printf("load Synon memory profile user_id=%q: %v", ownerUserID, err)
		return ""
	}
	categories, err := s.workspaceStore.ListMemoryCategories(ctx, ownerUserID)
	if err != nil {
		log.Printf("load Synon memory categories user_id=%q: %v", ownerUserID, err)
		return ""
	}
	return memoryprompt.RenderFactsWithConfig(profile, categories, time.Now().UTC(), s.memoryConfig)
}

func (s *Server) workspaceMemoryRecallSignal(
	ctx context.Context,
	ownerUserID, projectID, frameID, rootFrameID, parentFrameID, query, kind string,
	messages []chatCompletionMessage,
) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "user_message"
	}
	servedIDs := s.workspaceMemoryServedIDs(frameID, parentFrameID, messages)
	recalled, err := s.workspaceStore.RecallMemories(ctx, workspace.MemoryRecallOptions{
		UserID: ownerUserID, ProjectID: projectID, FrameID: rootFrameID, Query: query,
		Limit: s.memoryConfig.RecallInjectMax, CrossProject: true, Kind: kind,
		ServedIDs: servedIDs, Config: &s.memoryConfig,
	})
	if err != nil {
		log.Printf("recall Synon memory project_id=%q: %v", projectID, err)
		return ""
	}
	if len(recalled) == 0 {
		return ""
	}
	staleness, err := s.workspaceStore.MemoryStalenessFor(ctx, recalled)
	if err != nil {
		log.Printf("load Synon memory staleness project_id=%q: %v", projectID, err)
		return ""
	}
	counts, err := s.workspaceStore.CountActiveMemoryEntities(ctx, ownerUserID, rootFrameID)
	if err != nil {
		log.Printf("count Synon memory entities project_id=%q: %v", projectID, err)
		return ""
	}
	now := time.Now().UTC()
	rendered := memoryprompt.RenderRecall(recalled, staleness, counts, kind, now)
	if rendered == "" {
		return ""
	}
	if err := s.workspaceStore.MarkMemoryRowsSurfaced(ctx, ownerUserID, recalled, now); err != nil {
		log.Printf("mark Synon memory surfaced project_id=%q: %v", projectID, err)
	}
	for _, memory := range recalled {
		servedIDs[strings.ToLower(memory.ID)] = struct{}{}
	}
	s.persistWorkspaceMemoryServedIDs(frameID, servedIDs)
	return rendered
}

func frameWorkspaceMemoryMode(metadata workspace.FrameRuntimeMetadata) (string, bool) {
	originalInput, _ := metadata.ContextData["_original_input"].(map[string]any)
	if mode, found := workspaceMemoryModeValue(originalInput); found {
		return mode, true
	}
	return workspaceMemoryModeValue(metadata.InputData)
}

func sessionWorkspaceMemoryParentID(session sessionstore.Session) string {
	for _, key := range []string{"parentSessionId", "parent_session_id"} {
		if value := strings.TrimSpace(stringValue(session.Orchestration[key])); value != "" && value != strings.TrimSpace(session.ID) {
			return value
		}
	}
	return ""
}

func workspaceMemoryServedIDs(messages []chatCompletionMessage) map[string]struct{} {
	served := make(map[string]struct{})
	for _, message := range messages {
		for _, block := range workspaceMemoryRecallBlockPattern.FindAllStringSubmatch(message.Content, -1) {
			if len(block) != 2 {
				continue
			}
			for _, match := range workspaceMemoryRecallIDPattern.FindAllStringSubmatch(block[1], -1) {
				if len(match) == 2 {
					served[strings.ToLower(match[1])] = struct{}{}
				}
			}
		}
	}
	return served
}

func (s *Server) workspaceMemoryServedIDs(frameID, parentFrameID string, messages []chatCompletionMessage) map[string]struct{} {
	served := workspaceMemoryServedIDs(messages)
	if s == nil || s.runtimeStore == nil || strings.TrimSpace(frameID) == "" {
		return served
	}
	s.mergeWorkspaceMemoryServedIDs(frameID, served)
	parentFrameID = strings.TrimSpace(parentFrameID)
	if parentFrameID != "" && parentFrameID != strings.TrimSpace(frameID) {
		before := len(served)
		s.mergeWorkspaceMemoryServedIDs(parentFrameID, served)
		if len(served) != before {
			s.persistWorkspaceMemoryServedIDs(frameID, served)
		}
	}
	return served
}

func (s *Server) mergeWorkspaceMemoryServedIDs(frameID string, served map[string]struct{}) {
	entry, found, err := s.runtimeStore.Get(workspaceMemoryServedRuntimeNamespace, runtimeKeyFromSessionID(frameID))
	if err != nil {
		log.Printf("load Synon served memory ids frame_id=%q: %v", frameID, err)
		return
	}
	if !found {
		return
	}
	switch values := entry.Value.(type) {
	case []any:
		for _, value := range values {
			if id := strings.ToLower(strings.TrimSpace(stringValue(value))); id != "" {
				served[id] = struct{}{}
			}
		}
	case []string:
		for _, value := range values {
			if id := strings.ToLower(strings.TrimSpace(value)); id != "" {
				served[id] = struct{}{}
			}
		}
	}
}

func (s *Server) persistWorkspaceMemoryServedIDs(frameID string, served map[string]struct{}) {
	if s == nil || s.runtimeStore == nil || strings.TrimSpace(frameID) == "" || len(served) == 0 {
		return
	}
	ids := make([]string, 0, len(served))
	for id := range served {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if _, err := s.runtimeStore.Set(workspaceMemoryServedRuntimeNamespace, runtimeKeyFromSessionID(frameID), ids); err != nil {
		log.Printf("persist Synon served memory ids frame_id=%q: %v", frameID, err)
	}
}

func appendRuntimeWorkspaceMemoryContextMessage(messages []chatCompletionMessage, contextValue string) []chatCompletionMessage {
	contextValue = strings.TrimSpace(contextValue)
	if contextValue == "" {
		return messages
	}
	facts, recall := splitWorkspaceMemoryContext(contextValue)
	out := append([]chatCompletionMessage(nil), messages...)
	if facts != "" {
		insertAt := 0
		for insertAt < len(out) && out[insertAt].Role == "system" {
			insertAt++
		}
		content := facts
		if !strings.HasPrefix(content, memoryprompt.SystemHeader) {
			content = memoryprompt.SystemHeader + content
		}
		insert := chatCompletionMessage{Role: "system", Content: content}
		out = append(out, chatCompletionMessage{})
		copy(out[insertAt+1:], out[insertAt:])
		out[insertAt] = insert
	}
	if recall != "" {
		insertAt := -1
		for index := len(out) - 1; index >= 0; index-- {
			if out[index].Role == "user" {
				insertAt = index
				break
			}
		}
		if insertAt >= 0 {
			insert := chatCompletionMessage{Role: "user", Content: recall}
			out = append(out, chatCompletionMessage{})
			copy(out[insertAt+1:], out[insertAt:])
			out[insertAt] = insert
		}
	}
	return out
}

// attachRuntimeWorkspaceMemoryRecallMessages restores harness-owned recall
// messages after durable journal replay replaces ordinary conversation turns.
func attachRuntimeWorkspaceMemoryRecallMessages(runtimeMessages []agentruntime.Message, source []chatCompletionMessage) []agentruntime.Message {
	recalls := make([]agentruntime.Message, 0, 2)
	seen := make(map[string]struct{})
	for _, message := range runtimeMessages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			seen[strings.TrimSpace(message.Content)] = struct{}{}
		}
	}
	for _, message := range source {
		content := strings.TrimSpace(message.Content)
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") ||
			!strings.HasPrefix(content, memoryprompt.RecallPrefix) {
			continue
		}
		if _, duplicate := seen[content]; duplicate {
			continue
		}
		seen[content] = struct{}{}
		recalls = append(recalls, agentruntime.Message{Role: "user", Content: content})
	}
	if len(recalls) == 0 {
		return runtimeMessages
	}
	insertAt := len(runtimeMessages)
	for index := len(runtimeMessages) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(runtimeMessages[index].Role), "user") {
			insertAt = index
			break
		}
	}
	result := make([]agentruntime.Message, 0, len(runtimeMessages)+len(recalls))
	result = append(result, runtimeMessages[:insertAt]...)
	result = append(result, recalls...)
	result = append(result, runtimeMessages[insertAt:]...)
	return result
}

func splitWorkspaceMemoryContext(value string) (facts, recall string) {
	value = strings.TrimSpace(value)
	recallPrefix := memoryprompt.RecallPrefix + ` signal="`
	if strings.HasPrefix(value, recallPrefix) {
		return "", value
	}
	boundary := "\n\n" + recallPrefix
	index := strings.Index(value, boundary)
	if index >= 0 {
		return strings.TrimSpace(value[:index]), strings.TrimSpace(value[index+2:])
	}
	if strings.HasPrefix(value, memoryprompt.SystemHeader) || strings.HasPrefix(value, "<memory_facts>") {
		return value, ""
	}
	return "", value
}
