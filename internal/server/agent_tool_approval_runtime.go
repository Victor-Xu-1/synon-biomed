package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

const agentToolApprovalKind = "agent_tool"

func transcriptAgentToolApprovalPause(
	ctx context.Context,
	sessionID, toolName string,
	call agentruntime.ToolCall,
	input, permission map[string]any,
) *agentruntime.PauseError {
	if strings.TrimSpace(stringValue(permission["decision"])) != "pending_approval" {
		return nil
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil || strings.TrimSpace(sessionID) == "" ||
		strings.TrimSpace(sessionID) != run.Transcript.Stream.FrameID {
		return nil
	}
	approvalID := strings.TrimSpace(stringValue(permission["approvalId"]))
	toolCallID := strings.TrimSpace(call.ID)
	toolName = strings.TrimSpace(toolName)
	if approvalID == "" || toolCallID == "" || toolName == "" {
		return nil
	}
	description := strings.TrimSpace(firstNonEmpty(
		stringValue(input["human_description"]), stringValue(input["reason"]),
		"Approve "+toolName+" for this task.",
	))
	target := agentToolApprovalTarget(toolName, input)
	return &agentruntime.PauseError{
		Status:  "awaiting_approval",
		Message: "waiting for the user to approve the requested tool operation",
		Data: map[string]any{
			"approval_kind": agentToolApprovalKind,
			"approval_id":   approvalID, "request_id": approvalID,
			"session_id": sessionID, "frame_id": sessionID,
			"tool_call_id": toolCallID, "tool_name": toolName,
			"description": description, "target": target,
			"rememberable": boolValue(permission["rememberable"], false),
		},
	}
}

// agentToolApprovalTarget exposes only the bounded resource identity needed
// to make an informed approval decision. It deliberately excludes commands,
// credentials, headers, and arbitrary request payloads.
func agentToolApprovalTarget(toolName string, input map[string]any) string {
	var target string
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "edit_file":
		target = stringValue(input["file_path"])
	case requestNetworkAccessToolName:
		target = stringValue(input["domain"])
	case requestHostAccessToolName, deleteHostFilesToolName:
		target = firstNonEmpty(stringValue(input["host_path"]), stringValue(input["path"]))
	case sshComputeToolName, scpComputeToolName, submitComputeJobToolName:
		target = stringValue(input["provider"])
	}
	target = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(target))
	if len(target) > 512 {
		target = strings.TrimSpace(target[:512])
	}
	return target
}

func (s *Server) agentToolApprovalBatchResult(
	ctx context.Context,
	item workspace.ToolCallBatchItem,
) (result any, state, approvalID string, found bool, err error) {
	if s == nil || s.runtimeStore == nil || s.workspaceStore == nil {
		return nil, "", "", false, nil
	}
	waitingResult, frameID, err := s.workspaceStore.ReadToolCallBatchWaitingResult(ctx, item)
	if err != nil {
		return nil, "", "", false, err
	}
	if len(waitingResult) == 0 || string(waitingResult) == "null" {
		return nil, "", "", false, nil
	}
	pause := map[string]any{}
	if err := decodeOneJSONObject(waitingResult, &pause); err != nil {
		return nil, "", "", false, err
	}
	if pause["approval_kind"] != agentToolApprovalKind {
		return nil, "", "", false, nil
	}
	approvalID = strings.TrimSpace(stringValue(pause["approval_id"]))
	if approvalID == "" || pause["request_id"] != approvalID || pause["tool_call_id"] != item.ToolCallID || pause["tool_name"] != item.ToolName || pause["frame_id"] != frameID {
		return nil, "", approvalID, true, errors.New("agent tool approval reference conflicts with the waiting checkpoint")
	}
	entry, found, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil {
		return nil, "", approvalID, true, err
	}
	if !found {
		return nil, "", approvalID, true, errors.New("persisted agent tool approval authority is unavailable")
	}
	value := mapValue(entry.Value)
	if strings.TrimSpace(stringValue(value["toolCallId"])) != item.ToolCallID ||
		strings.TrimSpace(stringValue(value["tool"])) != strings.TrimSpace(item.ToolName) ||
		(stringValue(value["sessionId"]) != "" && stringValue(value["sessionId"]) != frameID) {
		return nil, "", approvalID, true, errors.New("agent tool approval identity conflicts with the durable batch")
	}
	state = agentRuntimeApprovalStatus(stringValue(value["status"]), value["result"])
	switch state {
	case "completed":
		result, ok := value["result"]
		if !ok {
			return nil, state, approvalID, true, errors.New("completed agent tool approval has no result")
		}
		return result, state, approvalID, true, nil
	case "denied":
		return map[string]any{
			"ok": false, "decision": "denied", "approvalId": approvalID,
			"error": "permission denied by the user for tool " + item.ToolName,
		}, state, approvalID, true, nil
	case "failed", "blocked":
		if result, ok := value["result"]; ok {
			return result, state, approvalID, true, nil
		}
		message := strings.TrimSpace(stringValue(value["error"]))
		if message == "" {
			message = fmt.Sprintf("approved tool operation ended with status %s", state)
		}
		return map[string]any{"ok": false, "code": "approved_tool_" + state, "error": message}, state, approvalID, true, nil
	case "pending", "executing", "approved":
		if state == "executing" && value["executionOwner"] == "session-runner" {
			// A reclaimed runner must not replay a mutation with no durable
			// receipt. The original runner settles this record before advancing.
			return map[string]any{"ok": false, "code": "tool_outcome_unknown", "error": "Approved tool execution was interrupted before its durable result was recorded."}, "failed", approvalID, true, nil
		}
		return nil, state, approvalID, true, nil
	default:
		return nil, state, approvalID, true, errors.New("agent tool approval has an unsupported state")
	}
}

func decodeOneJSONObject(raw []byte, target *map[string]any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil || *target == nil {
		return errors.New("agent tool approval input is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("agent tool approval input is invalid")
	}
	return nil
}
