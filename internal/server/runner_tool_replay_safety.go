package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"synon-go/internal/kernelcontract"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const sessionRunnerToolReplaySafetyReasonCode = "tool_replay_safety_pending"

func legacyReplayReadOnlyToolNames() []string {
	current := append([]string(nil), readOnlyAgentAllowedTools(nil)...)
	return append(current,
		"Read", "file_read", "ReadBatch", "file_read_batch",
		"Glob", "Grep", "file_search", "file_list", "file_info",
		"code_index", "code_references", "LSP", "web_search", "binding_mode_analysis",
		// These names were accepted by older provider transcripts. They remain
		// replay-only aliases; new model turns use canonical lower-case schemas
		// and never advertise the retired spellings.
		"WebSearch", "WebFetch", "WebResearch",
		"ToolDoctor", "AgentRuntimeDoctor", "TaskList", "task_list", "TaskGet", "TaskOutput",
		"settings_get", "settings_list", "runtime_get", "runtime_list",
		"ListMcpTools", "ListMcpResourcesTool", "ReadMcpResourceTool",
	)
}

type sessionRunnerToolReplaySafetyPending struct {
	toolName string
	cause    error
}

func (e sessionRunnerToolReplaySafetyPending) Error() string {
	if e.cause == nil {
		return "tool replay safety authority is temporarily unavailable"
	}
	return e.cause.Error()
}

func (e sessionRunnerToolReplaySafetyPending) Unwrap() error {
	return e.cause
}

func (e sessionRunnerToolReplaySafetyPending) runnerCorrection() transcriptstore.RunnerInterruptionCause {
	return newRunnerTextCorrection(sessionRunnerToolReplaySafetyReasonCode, fmt.Sprintf(
		"read-only replay authority for interrupted tool %s is temporarily unavailable; preserve the original durable call and retry its safety resolution before executing or declaring an unknown outcome",
		strings.TrimSpace(e.toolName),
	))
}

// runnerToolCallReplaySafeAfterInterruption decides whether an already-started
// durable call may be executed again after the prior process disappeared. The
// decision is capability-based: registered read-only tools and MCP tools whose
// authoritative connector annotation says readOnlyHint=true are replayable;
// mutating and unknown tools remain fail-closed. Kernel calls are excluded
// because their separate durable operation protocol owns exact-once recovery.
func (s *Server) runnerToolCallReplaySafeAfterInterruption(
	ctx context.Context,
	options SessionRunnerChatOptions,
	item workspace.ToolCallBatchItem,
) (bool, error) {
	if kernelcontract.Admitted(item.ToolName, item.ArgumentsJSON) {
		return false, nil
	}
	name := strings.TrimSpace(item.ToolName)
	if name == "" {
		return false, nil
	}
	if s != nil && s.tools != nil {
		if tool, found := s.registeredTool(name); found {
			for _, capability := range tool.Capabilities {
				if strings.EqualFold(strings.TrimSpace(capability), "read-only") {
					return true, nil
				}
			}
		}
	}
	for _, readOnlyName := range legacyReplayReadOnlyToolNames() {
		if strings.EqualFold(strings.TrimSpace(readOnlyName), name) {
			return true, nil
		}
	}

	input := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(string(item.ArgumentsJSON)))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil || input == nil || decoder.Decode(&struct{}{}) == nil {
		return false, nil
	}
	serverName, mcpToolName, isMCP := mcpPolicyTarget(name, input)
	if !isMCP {
		return false, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, agentRuntimeMCPConnectorProbeBudget)
	defer cancel()
	connector, runtimeContext, found, err := s.workspaceMCPRuntimeTargetWithContext(
		probeCtx, options.SessionID, serverName,
	)
	if err != nil {
		return false, sessionRunnerToolReplaySafetyPending{toolName: name, cause: err}
	}
	if !found || runtimeContext.workspaceMCPToolExcluded(connector.ID, mcpToolName) {
		return false, nil
	}
	if mode, explicit, policyErr := s.workspaceMCPRuntimeToolPolicy(
		runtimeContext.UserID, connector, mcpToolName,
	); policyErr != nil {
		return false, sessionRunnerToolReplaySafetyPending{toolName: name, cause: policyErr}
	} else if explicit && mode == "deny" {
		return false, nil
	}
	tools, err := s.workspaceMCPRuntimeConnectorTools(probeCtx, runtimeContext.UserID, connector)
	if err != nil {
		return false, sessionRunnerToolReplaySafetyPending{toolName: name, cause: err}
	}
	for _, tool := range tools {
		if tool.ToolName == mcpToolName {
			return tool.ReadOnlyHint, nil
		}
	}
	return false, nil
}
