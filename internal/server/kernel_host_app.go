package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func isKernelAppHostMethod(method string) bool {
	return method == "host.app_tool" || method == "host.app_tools_list"
}

func (s *Server) handleKernelAppHostCall(
	ctx context.Context,
	bound kernelHostExecutionIdentity,
	access workspace.KernelFrameAccess,
	method string,
	args []any,
	kwargs map[string]any,
	callID string,
) (result any, resultErr error) {
	serverName, toolName, artifactID, input, err := parseKernelAppHostCall(method, args, kwargs)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
	}
	connector, runtimeContext, found, err := s.workspaceMCPRuntimeTargetWithContext(ctx, access.Frame.ID, serverName)
	if err != nil {
		return nil, classifyKernelAppHostError(err)
	}
	if !found {
		return nil, kernelruntime.NewHostCallError("not_found", fmt.Sprintf("host.app: unknown or unattached server %q", serverName))
	}
	if runtimeContext.UserID != access.UserID {
		return nil, kernelruntime.NewHostCallError("permission_denied", "MCP app owner does not match the kernel Frame owner")
	}
	if s == nil || s.mcpApps == nil {
		return nil, kernelruntime.NewHostCallError("unavailable", "host.app is not available in this context")
	}
	if method == "host.app_tools_list" {
		tools, err := s.mcpApps.ListTools(access.UserID, access.Frame.RootFrameID, connector.ID)
		if err != nil {
			return nil, classifyKernelAppHostError(err)
		}
		return tools, nil
	}

	started := time.Now().UTC()
	auditInput := map[string]any{
		"server": serverName, "tool": toolName,
		"artifact_id": artifactID, "input": input,
	}
	defer func() {
		status := "completed"
		errorMessage := ""
		if resultErr != nil {
			status = agentRuntimeContextStatus(resultErr)
			errorMessage = resultErr.Error()
		}
		s.recordToolGatewayAudit(
			"kernel-host", "host.app", callID, auditInput, auditInput,
			status, result, errorMessage, started,
			map[string]any{
				"frameId": access.Frame.ID, "mcpServer": serverName,
				"mcpAppTool": toolName, "artifactId": artifactID,
			},
		)
	}()
	response, err := s.mcpApps.Call(
		ctx, access.UserID, access.Frame.RootFrameID, connector.ID,
		artifactID, toolName, input,
	)
	if err != nil {
		return nil, classifyKernelAppHostError(err)
	}
	text := mcpAppResultText(response.Content)
	if response.IsError {
		if text == "" {
			text = "app tool error"
		}
		return nil, kernelruntime.NewHostCallError("app_error", text)
	}
	if response.StructuredContent != nil {
		return response.StructuredContent, nil
	}
	return text, nil
}

func parseKernelAppHostCall(method string, args []any, kwargs map[string]any) (string, string, string, map[string]any, error) {
	if len(kwargs) != 0 || len(args) != 1 {
		return "", "", "", nil, errors.New("host.app accepts one positional request object")
	}
	request, ok := args[0].(map[string]any)
	if !ok {
		return "", "", "", nil, errors.New("host.app request must be an object")
	}
	serverName, ok := request["server"].(string)
	if !ok || !validMCPAppToolName(serverName) {
		return "", "", "", nil, errors.New("host.app server must be a non-empty bounded string")
	}
	if method == "host.app_tools_list" {
		if len(request) != 1 {
			return "", "", "", nil, errors.New("host.app tools request contains unknown fields")
		}
		return serverName, "", "", nil, nil
	}
	toolName, ok := request["tool"].(string)
	if !ok || !validMCPAppToolName(toolName) {
		return "", "", "", nil, errors.New("host.app tool must be a non-empty bounded string")
	}
	artifactID := ""
	if value, found := request["artifact_id"]; found && value != nil {
		artifactID, ok = value.(string)
		if !ok || artifactID == "" || artifactID != strings.TrimSpace(artifactID) || len([]byte(artifactID)) > maxMCPAppArtifactIDBytes {
			return "", "", "", nil, errors.New("host.app artifact_id must be a non-empty bounded string or null")
		}
	}
	input, ok := request["args"].(map[string]any)
	if !ok {
		return "", "", "", nil, errors.New("host.app args must be an object")
	}
	for key := range request {
		if key != "server" && key != "tool" && key != "artifact_id" && key != "args" {
			return "", "", "", nil, fmt.Errorf("host.app request contains unknown field %q", key)
		}
	}
	return serverName, toolName, artifactID, input, nil
}

func mcpAppResultText(content []mcpAppContent) string {
	parts := make([]string, 0, len(content))
	for _, item := range content {
		if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func classifyKernelAppHostError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var typed *kernelruntime.HostCallError
	if errors.As(err, &typed) {
		return typed
	}
	message := strings.TrimSpace(err.Error())
	switch {
	case strings.Contains(message, "unexpected argument"):
		return kernelruntime.NewHostCallError("invalid_arguments", message)
	case strings.Contains(message, "unknown tool"), strings.Contains(message, "not found"):
		return kernelruntime.NewHostCallError("not_found", message)
	case strings.Contains(message, "timed out"):
		return kernelruntime.NewHostCallError("timeout", message)
	case strings.Contains(message, "disconnected"), strings.Contains(message, "not consuming"):
		return kernelruntime.NewHostCallError("unavailable", message)
	default:
		return kernelruntime.NewHostCallError("app_error", message)
	}
}
