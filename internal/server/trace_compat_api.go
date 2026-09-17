package server

import (
	"net/http"
	"strconv"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

const maxTraceMessagesPerFrame = 1000

func (s *Server) handleFrameTraceShallow(w http.ResponseWriter, r *http.Request, store *workspace.Store, requested workspace.Frame) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	rootID := requested.RootFrameID
	var afterRevision *int64
	if rawCursor, present := r.URL.Query()["cursor"]; present {
		if len(rawCursor) != 1 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": "trace cursor must be singular"})
			return
		}
		cursor, err := strconv.ParseInt(strings.TrimSpace(rawCursor[0]), 10, 64)
		if err != nil || cursor < 0 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": "trace cursor must be a non-negative integer"})
			return
		}
		afterRevision = &cursor
		if strings.TrimSpace(r.URL.Query().Get("focus_frame_id")) != "" || strings.TrimSpace(r.URL.Query().Get("since_msg_idx")) != "" {
			writeV11Detail(w, http.StatusBadRequest, "cursor cannot be combined with focus_frame_id/since_msg_idx")
			return
		}
		if requested.ParentFrameID != "" {
			writeV11Detail(w, http.StatusBadRequest, "cursor is only supported on root frames (session conversations)")
			return
		}
	}
	snapshot, err := store.GetFrameTraceSnapshot(rootID, afterRevision)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"error": err.Error()})
		return
	}
	includeMessages := true
	if raw, present := r.URL.Query()["include_messages"]; present && len(raw) > 0 {
		includeMessages = raw[0] != "false"
	}
	sinceMessageSequence := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("since_msg_idx")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": "since_msg_idx must be a non-negative integer"})
			return
		}
		sinceMessageSequence = value
	}
	if afterRevision != nil {
		aggregated := make(map[string]map[string]any, len(snapshot.AllFrames))
		allProjections := make([]map[string]any, 0, len(snapshot.AllFrames))
		for _, frame := range snapshot.AllFrames {
			projection := traceFrameProjection(frame, false)
			aggregated[frame.ID] = projection
			allProjections = append(allProjections, projection)
		}
		if len(allProjections) > 0 {
			_, _ = buildTraceTree(allProjections, rootID)
		}
		changed := make([]map[string]any, 0, len(snapshot.Frames))
		for _, frame := range snapshot.Frames {
			projection := aggregated[frame.ID]
			if projection == nil {
				projection = traceFrameProjection(frame, false)
			}
			projection["children"] = []any{}
			changed = append(changed, projection)
		}
		responseCursor := snapshot.Cursor
		if *afterRevision > responseCursor {
			responseCursor = *afterRevision
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{
			"delta": true, "cursor": responseCursor, "server_max": snapshot.Cursor,
			"frame_count": snapshot.FrameCount, "changed": changed,
		})
		return
	}
	fatIDs := map[string]bool{requested.ID: true}
	if focus := strings.TrimSpace(r.URL.Query().Get("focus_frame_id")); focus != "" {
		for _, frame := range snapshot.Frames {
			if frame.ID == focus || strings.HasPrefix(frame.ID, focus) {
				fatIDs[frame.ID] = true
				break
			}
		}
	}
	projections := make([]map[string]any, 0, len(snapshot.Frames))
	for _, frame := range snapshot.Frames {
		fat := fatIDs[frame.ID]
		projection := traceFrameProjection(frame, fat)
		if fat && includeMessages {
			from := 0
			if frame.ID == requested.ID && sinceMessageSequence > 0 {
				if sinceMessageSequence <= int64(frame.MessageCount) {
					from = int(sinceMessageSequence)
				}
			}
			messages, err := traceFrameMessages(store, frame.ID, from)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"error": err.Error()})
				return
			}
			contextData, _ := projection["context_data"].(map[string]any)
			if contextData == nil {
				contextData = map[string]any{}
			}
			contextData["_messages"] = messages
			if projection["message_count"] == nil {
				projection["message_count"] = len(messages)
			}
			if frame.ID == requested.ID && sinceMessageSequence > 0 {
				contextData["_msg_base_idx"] = from
			}
			traceApplyContextMetrics(projection, contextData)
			projection["context_data"] = traceNullableMap(contextData)
		} else if fat {
			contextData := traceContextWithoutMessages(projection["context_data"])
			projection["context_data"] = contextData
			cleanContext, _ := contextData.(map[string]any)
			traceApplyContextMetrics(projection, cleanContext)
		}
		projections = append(projections, projection)
	}
	root, ok := buildTraceTree(projections, requested.ID)
	if !ok {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"error": "trace root not found"})
		return
	}
	if !includeMessages {
		root["_trace_cursor"] = snapshot.Cursor
	}
	writeWorkspaceJSON(w, http.StatusOK, root)
}

func traceFrameProjection(frame workspace.Frame, fat bool) map[string]any {
	contextData := copyMapAny(frame.ContextData)
	compactionCount := traceCompactionCount(contextData)
	inputData := traceNullableMap(frame.InputData)
	outputData := traceNullableMap(frame.OutputData)
	if !fat {
		inputData = nil
		outputData = traceLeanOutput(frame.OutputData)
		contextData = traceLeanContext(contextData, frame.Status)
	}
	var messageCount any
	if value, found := traceContextInteger(contextData, "_message_count"); found {
		messageCount = value
	} else {
		messageCount = frame.MessageCount
	}
	var name, conversationType any
	if frame.ParentFrameID == "" {
		name = traceNullableString(frame.Name)
		conversationType = traceNullableString(frame.ConversationType)
	}
	projection := map[string]any{
		"id": frame.ID, "root_frame_id": frame.RootFrameID,
		"parent_frame_id": traceNullableString(frame.ParentFrameID),
		"agent_name":      frame.AgentName, "delegate_name": traceNullableString(frame.DelegateName),
		"status": frame.Status, "input_data": inputData,
		"output_data": outputData, "context_data": traceNullableMap(contextData),
		"created_at": frame.CreatedAt.UTC(), "completed_at": frame.CompletedAt, "updated_at": frame.UpdatedAt.UTC(),
		"model": traceNullableString(frame.Model), "effort": traceNullableString(frame.Effort),
		"input_tokens":       traceTokenTotal(frame.InputTokens, frame.AuxInputTokens),
		"output_tokens":      traceTokenTotal(frame.OutputTokens, frame.AuxOutputTokens),
		"cache_read_tokens":  traceTokenTotal(frame.CacheReadTokens, frame.AuxCacheReadTokens),
		"cache_write_tokens": traceTokenTotal(frame.CacheWriteTokens, frame.AuxCacheWriteTokens),
		"total_cost":         traceCostTotal(frame.TotalCost, frame.AuxCost), "aux_cost": frame.AuxCost,
		"context_limit": v11DefaultContextLimit, "context_used": nil, "context_usage_percent": nil,
		"compaction_count": compactionCount, "children": []any{},
		"project_id": frame.ProjectID, "name": name, "conversation_type": conversationType,
		"message_count": messageCount, "task_summary": traceNullableString(frame.TaskSummary),
		"status_description": traceNullableString(frame.StatusDescription),
		"mentioned_files":    traceRootMentionedFiles(frame),
		"specialists_used":   nil, "is_hidden": frame.IsHidden,
		"activity_counts": nil,
	}
	traceApplyContextMetrics(projection, contextData)
	return projection
}

func traceFrameMessages(store *workspace.Store, frameID string, from int) ([]any, error) {
	messages := make([]any, 0)
	for len(messages) < maxTraceMessagesPerFrame {
		page, err := store.CompatibilityFrameMessages(frameID, from+len(messages), 500)
		if err != nil {
			return nil, err
		}
		for _, message := range page.Messages {
			messages = append(messages, message)
		}
		if len(page.Messages) == 0 || from+len(messages) >= page.Total {
			break
		}
	}
	return messages, nil
}

func buildTraceTree(frames []map[string]any, rootID string) (map[string]any, bool) {
	byID := make(map[string]map[string]any, len(frames))
	for _, frame := range frames {
		id, _ := frame["id"].(string)
		if id != "" {
			byID[id] = frame
		}
	}
	root, found := byID[rootID]
	if !found {
		return nil, false
	}
	for _, frame := range frames {
		id, _ := frame["id"].(string)
		if id == rootID {
			continue
		}
		parentID, _ := frame["parent_frame_id"].(string)
		parent := byID[parentID]
		if parent == nil {
			continue
		}
		children, _ := parent["children"].([]any)
		parent["children"] = append(children, frame)
	}
	aggregateTraceUsage(root)
	return root, true
}

func traceNullableMap(value map[string]any) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func traceNullableStrings(values []string) any {
	if len(values) == 0 {
		return nil
	}
	return values
}

func traceCompactionCount(contextData map[string]any) int {
	if value, ok := contextData["_compaction_count"].(float64); ok && value > 0 {
		return int(value)
	}
	if value, ok := contextData["_compaction_count"].(int); ok && value > 0 {
		return value
	}
	return 0
}

func traceTokenTotal(primary, auxiliary *int64) any {
	if primary == nil && auxiliary == nil {
		return nil
	}
	var total int64
	if primary != nil {
		total += *primary
	}
	if auxiliary != nil {
		total += *auxiliary
	}
	return total
}

func traceCostTotal(primary, auxiliary *float64) any {
	if primary == nil && auxiliary == nil {
		return nil
	}
	var total float64
	if primary != nil {
		total += *primary
	}
	if auxiliary != nil {
		total += *auxiliary
	}
	return total
}

func aggregateTraceUsage(frame map[string]any) [6]float64 {
	keys := [...]string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "total_cost", "aux_cost"}
	var totals [6]float64
	for index, key := range keys {
		totals[index] = traceNumericValue(frame[key])
	}
	children, _ := frame["children"].([]any)
	for _, raw := range children {
		child, _ := raw.(map[string]any)
		childTotals := aggregateTraceUsage(child)
		for index := range totals {
			totals[index] += childTotals[index]
		}
	}
	for index, key := range keys {
		if totals[index] > 0 {
			frame[key] = totals[index]
		} else {
			frame[key] = nil
		}
	}
	return totals
}

func traceNumericValue(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	default:
		return 0
	}
}

func traceNullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
