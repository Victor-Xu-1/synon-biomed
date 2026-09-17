package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type BindAgentToolApprovalInput struct {
	FrameID      string
	ApprovalID   string
	ToolCallID   string
	ToolName     string
	Description  string
	Target       string
	Rememberable bool
}

type BindAgentToolApprovalResult struct {
	Events         []FrameEvent
	AlreadyPending bool
}

// BindAgentToolApprovalTransaction projects a server-owned deferred tool
// approval into the Frame's single pending-input authority in the same
// transaction that parks its Transcript runner.
func (s *Store) BindAgentToolApprovalTransaction(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	input BindAgentToolApprovalInput,
) (BindAgentToolApprovalResult, error) {
	if s == nil || tx == nil {
		return BindAgentToolApprovalResult{}, errors.New("agent tool approval transaction is required")
	}
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.ApprovalID = strings.TrimSpace(input.ApprovalID)
	input.ToolCallID = strings.TrimSpace(input.ToolCallID)
	input.ToolName = strings.TrimSpace(input.ToolName)
	input.Description = strings.TrimSpace(input.Description)
	input.Target = strings.TrimSpace(input.Target)
	if input.FrameID == "" || input.ApprovalID == "" || input.ToolCallID == "" || input.ToolName == "" {
		return BindAgentToolApprovalResult{}, errors.New("complete agent tool approval identity is required")
	}
	if len(input.ApprovalID) > 512 || len(input.ToolCallID) > 512 || len(input.ToolName) > 256 ||
		len(input.Description) > 512 || len(input.Target) > 512 {
		return BindAgentToolApprovalResult{}, errors.New("agent tool approval identity exceeds the durable limit")
	}

	ownerID, err := frameOwnerInTransaction(ctx, tx, input.FrameID)
	if err != nil {
		return BindAgentToolApprovalResult{}, err
	}
	var status, rawContext string
	if err := tx.QueryRowContext(ctx, `
		SELECT frame.status,COALESCE(metadata.context_data,'{}')
		FROM frames frame LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id=frame.id
		WHERE frame.id=?`, input.FrameID,
	).Scan(&status, &rawContext); err != nil {
		return BindAgentToolApprovalResult{}, err
	}
	if status == "completed" || status == "failed" || status == "cancelled" || status == "canceled" {
		return BindAgentToolApprovalResult{}, errors.New("terminal frame cannot accept an agent tool approval")
	}
	contextData := map[string]any{}
	if json.Unmarshal([]byte(rawContext), &contextData) != nil {
		return BindAgentToolApprovalResult{}, errors.New("agent tool approval frame context is invalid")
	}
	request := map[string]any{
		"tool_id": input.ApprovalID, "requestId": input.ApprovalID,
		"approval_id": input.ApprovalID, "kind": "agent_tool",
		"tool": input.ToolName, "tool_name": input.ToolName,
		"tool_call_id": input.ToolCallID, "description": input.Description,
		"target":       input.Target,
		"rememberable": input.Rememberable,
	}
	pending := compatibilityPendingInputRequests(contextData)
	for _, item := range pending {
		if compatibilityPendingInputID(item) != input.ApprovalID {
			continue
		}
		stored, storedErr := json.Marshal(item)
		expected, expectedErr := json.Marshal(request)
		if storedErr != nil || expectedErr != nil || !bytes.Equal(stored, expected) {
			return BindAgentToolApprovalResult{}, errors.New("agent tool approval conflicts with durable state")
		}
		return BindAgentToolApprovalResult{AlreadyPending: true}, nil
	}
	pending = append(pending, request)
	contextData["_pending_input_requests"] = compatibilityMapsToAny(pending)
	updatedContext, err := json.Marshal(contextData)
	if err != nil {
		return BindAgentToolApprovalResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata(frame_id,context_data)
		VALUES(?,?) ON CONFLICT(frame_id) DO UPDATE SET context_data=excluded.context_data`,
		input.FrameID, string(updatedContext),
	); err != nil {
		return BindAgentToolApprovalResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET status='awaiting_user_response',updated_at=? WHERE id=?`,
		s.now().UTC(), input.FrameID,
	); err != nil {
		return BindAgentToolApprovalResult{}, err
	}
	assistantEvent, err := appendFrameLifecycleEvent(ctx, tx, input.FrameID, "assistant_message", map[string]any{
		"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": input.ApprovalID, "name": input.ToolName,
			"input": map[string]any{"approval_id": input.ApprovalID, "tool_call_id": input.ToolCallID},
		}},
	}, s.now().UTC())
	if err != nil {
		return BindAgentToolApprovalResult{}, err
	}
	resultEvent, err := appendFrameLifecycleEvent(ctx, tx, input.FrameID, "user_message", map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": input.ApprovalID,
			"content": `{"status":"awaiting_approval"}`, "is_error": true,
		}},
	}, s.now().UTC())
	if err != nil {
		return BindAgentToolApprovalResult{}, err
	}
	pausedEvent, err := appendFrameLifecycleEvent(ctx, tx, input.FrameID, "frame_paused", map[string]any{
		"status": "awaiting_user_response", "reason": "agent_tool_approval",
		"approval_id": input.ApprovalID, "tool_call_id": input.ToolCallID, "tool_name": input.ToolName,
	}, s.now().UTC())
	if err != nil {
		return BindAgentToolApprovalResult{}, err
	}
	frame, err := frameForRealtimeInTransaction(ctx, tx, input.FrameID)
	if err != nil {
		return BindAgentToolApprovalResult{}, err
	}
	events := []FrameEvent{assistantEvent, resultEvent, pausedEvent}
	for _, event := range events {
		if _, err := s.enqueueRealtimeOutboxTransaction(ctx, tx,
			FrameRealtimeEventInput("frame-event:"+event.ID, ownerID, frame, event), event.ID, "",
		); err != nil {
			return BindAgentToolApprovalResult{}, err
		}
	}
	return BindAgentToolApprovalResult{Events: events}, nil
}
