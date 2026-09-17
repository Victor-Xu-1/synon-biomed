package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrCompatibilityAsideParentNotRoot           = errors.New("aside parent must be a root frame")
	ErrCompatibilityAsideParentTranscriptMissing = errors.New("aside parent transcript stream is missing")
)

type CompatibilityAsideInput struct {
	ID                 string
	ParentRootFrameID  string
	Request            string
	Model              *string
	IntentID           string
	AsSession          bool
	RuntimeConfig      map[string]any
	canonicalSeed      bool
	canonicalSeedCount int
}

type CompatibilityAsideResult struct {
	Frame        Frame
	SeedMessages []map[string]any
	PrefixLen    int
	InputData    map[string]any
	ContextData  map[string]any
	Model        any
	Effort       any
	Event        FrameEvent
}

var compatibilityAsideScrubKeys = append([]string{
	"_running_children", "_tool_id_to_frame_id", "_delegated_agents",
	"_streaming_buffer", "_pending_user_message", "_seen_viewport_versions",
	"_pending_input_requests", "_ask_user_payload", "_pending_access_request",
	"_checkpoint_inflight_start", "_checkpoint_rewinds", "_n_subagent_spawns",
	"_n_reviewer_rounds",
	"_branch_meta", "_messages", "_session_started_emitted", "_sdk_child_spawn_tid",
	"_system",
	"_original_input", "_last_checkpoint_idx", "_last_bookmark_idx",
	"_exec_log_watermark", "_goal_iterations", "_goal_consecutive_denials",
	"_goal_last_denial_msg_idx", "_goal_last_reason", "_goal_started_at",
	"_goal_armed_condition", "_goal_tokens_at_start", "_goal_park_tool_id",
	"_goal_result", "_goal_predicate", "_goal_artifact_id", "_goal_version_id",
	"_goal_artifact_filename", "_goal_budget", "_delegate_spawns_this_task",
	"_routine_spawn_floor",
}, compatibilityPlanContextKeys...)

var compatibilityAsideSessionResetKeys = []string{
	"_input_tokens", "_output_tokens", "_total_cost", "_cache_read_tokens",
	"_cache_write_tokens", "_aux_input_tokens", "_aux_output_tokens", "_aux_cost",
	"_aux_cache_read_tokens", "_aux_cache_write_tokens", "_token_class_usage",
	"_rc_fork_log",
}

var compatibilityAsideSessionKnobs = []string{
	"ultra_mode", "verifier_mode", "memory_mode", "auto_mode", "reviewer_model",
	"rc_context_ceiling", "python_version", "kernel_idle_timeout", "async_local_exec_wallclock_cap_s", "gpu_mode",
}

func (s *Store) CreateCompatibilityAside(input CompatibilityAsideInput) (CompatibilityAsideResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityAsideResult{}, errors.New("workspace store is closed")
	}
	input, err := normalizeCompatibilityAsideInput(input)
	if err != nil {
		return CompatibilityAsideResult{}, err
	}

	s.branchMu.Lock()
	defer s.branchMu.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompatibilityAsideResult{}, fmt.Errorf("begin compatibility aside transaction: %w", err)
	}
	defer tx.Rollback()
	result, err := s.createCompatibilityAsideInTransaction(ctx, tx, input)
	if err != nil {
		return CompatibilityAsideResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CompatibilityAsideResult{}, fmt.Errorf("commit compatibility aside transaction: %w", err)
	}
	return result, nil
}

func normalizeCompatibilityAsideInput(input CompatibilityAsideInput) (CompatibilityAsideInput, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.ParentRootFrameID = strings.TrimSpace(input.ParentRootFrameID)
	input.IntentID = strings.TrimSpace(input.IntentID)
	if input.ID == "" || input.ParentRootFrameID == "" {
		return CompatibilityAsideInput{}, errors.New("aside frame id and parent root frame id are required")
	}
	if input.Request == "" {
		return CompatibilityAsideInput{}, errors.New("aside request is required")
	}
	return input, nil
}

func (s *Store) createCompatibilityAsideInTransaction(
	ctx context.Context,
	tx workspaceTransaction,
	input CompatibilityAsideInput,
) (CompatibilityAsideResult, error) {
	var projectID, agentName string
	var parentID sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT project_id, parent_frame_id, agent_name
		FROM frames WHERE id = ?`, input.ParentRootFrameID).Scan(&projectID, &parentID, &agentName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompatibilityAsideResult{}, fmt.Errorf("frame %s not found", input.ParentRootFrameID)
		}
		return CompatibilityAsideResult{}, fmt.Errorf("load compatibility aside parent: %w", err)
	}
	if parentID.Valid {
		return CompatibilityAsideResult{}, ErrCompatibilityAsideParentNotRoot
	}
	parentContext, parentInput, err := compatibilityAsideRuntimeMetadata(ctx, tx, input.ParentRootFrameID)
	if err != nil {
		return CompatibilityAsideResult{}, err
	}
	var parentMessages []map[string]any
	if !input.canonicalSeed {
		parentMessages, err = compatibilityRootMessages(ctx, tx, input.ParentRootFrameID)
		if err != nil {
			return CompatibilityAsideResult{}, err
		}
	}
	trimmed := parentMessages
	if !input.canonicalSeed {
		pending := compatibilityAsidePendingToolIDs(parentContext)
		trimmed = trimCompatibilityAsideMessages(parentMessages, pending)
	}
	asideContext := cloneCompatibilityMap(parentContext)
	for _, key := range compatibilityAsideScrubKeys {
		delete(asideContext, key)
	}
	if input.AsSession {
		for _, key := range compatibilityAsideSessionResetKeys {
			delete(asideContext, key)
		}
	}

	parentModel := parentContext["_model"]
	asideEffort := parentContext["_effort"]
	if input.RuntimeConfig != nil {
		parentModel = input.RuntimeConfig["model"]
		asideEffort = input.RuntimeConfig["effort"]
	}
	asideModel := parentModel
	if input.Model != nil {
		asideModel = *input.Model
	}
	asideContext["_model"] = asideModel
	if asideEffort != nil {
		asideContext["_effort"] = asideEffort
	} else {
		delete(asideContext, "_effort")
	}
	seedMessages := cloneCompatibilityMessages(trimmed)
	if !compatibilityAsideEqualScalar(asideModel, parentModel) {
		seedMessages = stripCompatibilityThinkingBlocks(seedMessages)
	}
	prefixLen := len(seedMessages)
	if input.canonicalSeed {
		prefixLen = input.canonicalSeedCount
	}
	if input.AsSession {
		if input.canonicalSeed {
			asideContext["_last_checkpoint_idx"] = prefixLen + 1
			asideContext["_last_bookmark_idx"] = prefixLen + 1
		} else {
			seedMessages = append(seedMessages, map[string]any{
				"role":            "user",
				"content":         "[System] This session was forked from another session at this point. Background kernels, in-memory variables, and running jobs from the original session are not available here.",
				"_harness_notice": true,
			})
			asideContext["_last_checkpoint_idx"] = len(seedMessages)
			asideContext["_last_bookmark_idx"] = len(seedMessages)
		}
	}

	inherited := map[string]any{}
	if input.RuntimeConfig != nil {
		keys := []string{"gpu_mode"}
		if input.AsSession {
			keys = compatibilityAsideSessionKnobs
		}
		for _, key := range keys {
			if value, found := input.RuntimeConfig[key]; found {
				inherited[key] = value
			}
		}
	} else {
		var err error
		inherited, err = compatibilityAsideInheritedKnobs(ctx, tx, input.ParentRootFrameID, projectID, parentInput, parentContext, input.AsSession)
		if err != nil {
			return CompatibilityAsideResult{}, err
		}
	}
	inputData := map[string]any{"request": input.Request}
	for key, value := range inherited {
		inputData[key] = value
	}
	if !input.AsSession {
		inputData["_aside_parent"] = map[string]any{
			"root_frame_id": input.ParentRootFrameID,
			"prefix_len":    prefixLen,
		}
	}
	if input.IntentID != "" {
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT frame_id FROM queued_user_messages WHERE intent_id = ?`, input.IntentID).Scan(&existing)
		if err == nil {
			return CompatibilityAsideResult{}, ErrCompatibilityIntentUsed
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return CompatibilityAsideResult{}, fmt.Errorf("look up aside intent: %w", err)
		}
		inputData["_intent_id"] = input.IntentID
	}

	now := s.now().UTC()
	name := "Aside \u00b7 " + truncateCompatibilityAsideText(input.Request, 60)
	hidden := true
	taskSummary := ""
	if input.AsSession {
		name = truncateCompatibilityAsideText(input.Request, 60)
		hidden = false
		taskSummary = truncateCompatibilityAsideText(input.Request, 200)
	}
	frame := Frame{
		ID: input.ID, IncarnationID: uuid.NewString(), ProjectID: projectID, RootFrameID: input.ID,
		AgentName: agentName, Status: "processing", ConversationType: "agent",
		Name: name, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frames (
			id, incarnation_id, project_id, parent_frame_id, root_frame_id, root_sequence,
			agent_name, status, conversation_type, name, created_at, updated_at
		) VALUES (?, ?, ?, NULL, ?, 0, ?, 'processing', 'agent', ?, ?, ?)`,
		frame.ID, frame.IncarnationID, frame.ProjectID, frame.RootFrameID, frame.AgentName,
		frame.Name, frame.CreatedAt, frame.UpdatedAt); err != nil {
		return CompatibilityAsideResult{}, fmt.Errorf("insert compatibility aside frame: %w", err)
	}
	rawInput, err := json.Marshal(inputData)
	if err != nil {
		return CompatibilityAsideResult{}, fmt.Errorf("encode compatibility aside input: %w", err)
	}
	rawContext, err := json.Marshal(asideContext)
	if err != nil {
		return CompatibilityAsideResult{}, fmt.Errorf("encode compatibility aside context: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (
			frame_id, input_data, context_data, task_summary, is_hidden
		) VALUES (?, ?, ?, ?, ?)`,
		frame.ID, string(rawInput), string(rawContext), nullableString(taskSummary), hidden); err != nil {
		return CompatibilityAsideResult{}, fmt.Errorf("insert compatibility aside metadata: %w", err)
	}
	if !input.canonicalSeed {
		if err := replaceCompatibilityRootMessages(ctx, tx, frame.ID, seedMessages, now); err != nil {
			return CompatibilityAsideResult{}, err
		}
	}
	event, err := appendCompatibilityBranchEvent(ctx, tx, frame.ID, "frame_created", map[string]any{
		"projectId": projectID, "agentName": agentName, "status": "processing",
		"conversationType": "agent", "isHidden": hidden,
	}, now.Add(time.Duration(len(seedMessages))*time.Nanosecond))
	if err != nil {
		return CompatibilityAsideResult{}, err
	}
	if input.IntentID != "" {
		dedupe := map[string]any{
			"request": input.Request, "model": asideModel, "as_session": input.AsSession,
		}
		rawEnvelope, err := marshalCompatibilityQueueEnvelope(dedupe, map[string]any{
			"frame_id": frame.ID, "request": input.Request,
		}, "")
		if err != nil {
			return CompatibilityAsideResult{}, err
		}
		var sequence int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM queued_user_messages`).Scan(&sequence); err != nil {
			return CompatibilityAsideResult{}, fmt.Errorf("allocate aside intent sequence: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO queued_user_messages (
				sequence, frame_id, payload, intent_id, state, resolved_at, created_at
			) VALUES (?, ?, ?, ?, 'drained', ?, ?)`,
			sequence, frame.ID, rawEnvelope, input.IntentID, now, now); err != nil {
			return CompatibilityAsideResult{}, fmt.Errorf("record aside intent: %w", err)
		}
	}
	return CompatibilityAsideResult{
		Frame: frame, SeedMessages: seedMessages, PrefixLen: prefixLen,
		InputData: inputData, ContextData: asideContext,
		Model: asideModel, Effort: asideEffort, Event: event,
	}, nil
}

func compatibilityAsideRuntimeMetadata(ctx context.Context, tx workspaceTransaction, frameID string) (map[string]any, map[string]any, error) {
	var rawContext, rawInput sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT context_data, input_data FROM frame_runtime_metadata WHERE frame_id = ?`, frameID).Scan(&rawContext, &rawInput)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("load compatibility aside metadata: %w", err)
	}
	contextData := map[string]any{}
	inputData := map[string]any{}
	if rawContext.Valid && strings.TrimSpace(rawContext.String) != "" {
		if err := json.Unmarshal([]byte(rawContext.String), &contextData); err != nil {
			return nil, nil, fmt.Errorf("decode compatibility aside context: %w", err)
		}
	}
	if rawInput.Valid && strings.TrimSpace(rawInput.String) != "" {
		if err := json.Unmarshal([]byte(rawInput.String), &inputData); err != nil {
			return nil, nil, fmt.Errorf("decode compatibility aside input: %w", err)
		}
	}
	if contextData == nil {
		contextData = map[string]any{}
	}
	if inputData == nil {
		inputData = map[string]any{}
	}
	return contextData, inputData, nil
}

func compatibilityAsideInheritedKnobs(
	ctx context.Context,
	tx workspaceTransaction,
	parentID string,
	projectID string,
	parentInput map[string]any,
	parentContext map[string]any,
	asSession bool,
) (map[string]any, error) {
	inputData := parentInput
	contextData := parentContext
	seen := map[string]bool{parentID: true}
	for depth := 0; depth < 8; depth++ {
		asideParent, _ := inputData["_aside_parent"].(map[string]any)
		nextID := strings.TrimSpace(compatibilityStringValue(asideParent["root_frame_id"]))
		if nextID == "" || seen[nextID] {
			break
		}
		var nextProject string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM frames WHERE id = ?`, nextID).Scan(&nextProject); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			return nil, fmt.Errorf("load compatibility aside ancestor: %w", err)
		}
		if nextProject != projectID {
			break
		}
		nextContext, nextInput, err := compatibilityAsideRuntimeMetadata(ctx, tx, nextID)
		if err != nil {
			return nil, err
		}
		seen[nextID] = true
		contextData, inputData = nextContext, nextInput
	}
	original, _ := contextData["_original_input"].(map[string]any)
	keys := []string{"gpu_mode"}
	if asSession {
		keys = compatibilityAsideSessionKnobs
	}
	result := map[string]any{}
	for _, key := range keys {
		if value, found := original[key]; found {
			result[key] = value
			continue
		}
		if value, found := inputData[key]; found {
			result[key] = value
		}
	}
	return result, nil
}

func compatibilityAsidePendingToolIDs(contextData map[string]any) map[string]bool {
	result := map[string]bool{}
	if requests, ok := contextData["_pending_input_requests"].([]any); ok {
		for _, raw := range requests {
			request, _ := raw.(map[string]any)
			if id := strings.TrimSpace(compatibilityStringValue(request["tool_id"])); id != "" {
				result[id] = true
			}
		}
	}
	if len(result) == 0 {
		for _, key := range []string{"_ask_user_payload", "_pending_access_request"} {
			request, _ := contextData[key].(map[string]any)
			if id := strings.TrimSpace(compatibilityStringValue(request["tool_id"])); id != "" {
				result[id] = true
			}
		}
	}
	return result
}

func trimCompatibilityAsideMessages(messages []map[string]any, pending map[string]bool) []map[string]any {
	keep := len(messages)
	if len(pending) > 0 && keep > 0 {
		last := messages[keep-1]
		if last["role"] == "user" {
			blocks, ok := last["content"].([]any)
			if ok && len(blocks) > 0 {
				allPending := true
				for _, raw := range blocks {
					block, _ := raw.(map[string]any)
					id := strings.TrimSpace(compatibilityStringValue(block["tool_use_id"]))
					if block["type"] != "tool_result" || id == "" || !pending[id] ||
						block["content"] != `{"status":"awaiting_user_response"}` {
						allPending = false
						break
					}
				}
				if allPending {
					keep--
				}
			}
		}
	}
	for keep > 0 {
		last := messages[keep-1]
		if last["role"] != "assistant" {
			break
		}
		blocks, ok := last["content"].([]any)
		if !ok {
			break
		}
		hasToolUse := false
		for _, raw := range blocks {
			block, _ := raw.(map[string]any)
			if block["type"] == "tool_use" {
				hasToolUse = true
				break
			}
		}
		if !hasToolUse {
			break
		}
		keep--
	}
	return cloneCompatibilityMessages(messages[:keep])
}

func stripCompatibilityThinkingBlocks(messages []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(messages))
	for _, message := range cloneCompatibilityMessages(messages) {
		if refused, _ := message["_refusal"].(bool); refused {
			result = append(result, message)
			continue
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			result = append(result, message)
			continue
		}
		filtered := make([]any, 0, len(blocks))
		for _, raw := range blocks {
			block, _ := raw.(map[string]any)
			if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
				continue
			}
			filtered = append(filtered, raw)
		}
		if len(filtered) == 0 {
			continue
		}
		message["content"] = filtered
		result = append(result, message)
	}
	return result
}

func cloneCompatibilityMap(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	result := map[string]any{}
	_ = json.Unmarshal(raw, &result)
	return result
}

func compatibilityAsideEqualScalar(left, right any) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return string(leftRaw) == string(rightRaw)
}

func truncateCompatibilityAsideText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
