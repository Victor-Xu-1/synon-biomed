package server

import (
	"context"
	"errors"
	"time"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

// The approval adapter supplies authorization only. Execution, progress and
// terminal receipt publication remain inside the claimed runner's Engine.
func (s *Server) approvedRunnerToolGateway(run *sessionRunnerChatRun, item workspace.ToolCallBatchItem, approvalID string) agentruntime.ToolGateway {
	return agentruntime.FuncToolGateway(func(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		if call.ID != item.ToolCallID || call.Name != item.ToolName {
			return agentruntime.ToolResult{}, errors.New("approved tool identity changed")
		}
		s.agentToolApprovalMu.Lock()
		entry, found, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
		if err != nil {
			s.agentToolApprovalMu.Unlock()
			return agentruntime.ToolResult{}, err
		}
		value := mapValue(entry.Value)
		if !found || value["status"] != "approved" || value["sessionId"] != run.SessionID || value["toolCallId"] != call.ID || value["tool"] != call.Name {
			s.agentToolApprovalMu.Unlock()
			return agentruntime.ToolResult{}, errors.New("approved tool authority changed")
		}
		input := mapValue(value["input"])
		// Integrity-check the persisted approved input only. The lookup identity
		// is the waiting checkpoint reference, never the raw model arguments.
		if agentRuntimeApprovalID(call.Name, call, input) != approvalID {
			s.agentToolApprovalMu.Unlock()
			return agentruntime.ToolResult{}, errors.New("approved tool arguments changed")
		}
		value["status"] = "executing"
		value["executionOwner"] = "session-runner"
		value["executionAttempt"] = run.Attempt
		_, err = s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value)
		s.agentToolApprovalMu.Unlock()
		if err != nil {
			return agentruntime.ToolResult{}, err
		}
		receipt := s.executeExactToolGateway(ctx, "agent-runtime", run.SessionID, call.ID, call.Name, input, exactServerToolGatewayOptions{ResumeAfterApproval: true, AuditExtra: map[string]any{"approvalId": approvalID}})
		value["status"] = agentRuntimeApprovalStatus(receipt.Status, receipt.Value)
		value["result"] = receipt.Value
		value["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		if receipt.Err != nil {
			value["status"] = "failed"
			value["error"] = receipt.Err.Error()
		}
		s.agentToolApprovalMu.Lock()
		_, persistErr := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value)
		s.agentToolApprovalMu.Unlock()
		if persistErr != nil {
			return agentruntime.ToolResult{}, persistErr
		}
		return serverAgentRuntimeGatewayResult(receipt)
	})
}

func (s *Server) bindAgentToolApprovalFrame(ctx context.Context, approvalID, frameID string) (map[string]any, error) {
	s.agentToolApprovalMu.Lock()
	defer s.agentToolApprovalMu.Unlock()
	entry, found, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("approval authority disappeared")
	}
	value := mapValue(entry.Value)
	if stringValue(value["sessionId"]) != "" {
		return value, nil
	}
	owned, err := s.workspaceStore.HasFrameToolCallBatch(ctx, frameID, stringValue(value["toolCallId"]), stringValue(value["tool"]))
	if err != nil {
		return nil, err
	}
	if owned {
		value["sessionId"] = frameID
		_, err = s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value)
	}
	return value, err
}
