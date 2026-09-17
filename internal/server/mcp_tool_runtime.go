package server

import (
	"context"
	"fmt"
	"strings"
	"synon-go/internal/tools/mcpstdio"
)

func (s *Server) executeMCPTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	ctx = mcpstdio.WithHTTPClient(ctx, s.httpClient)
	switch {
	case toolName == "ListMcpTools":
		if err := s.validateRegisteredTool(toolName, input); err != nil {
			return nil, err
		}
		return mcpstdio.ListTools(ctx, s.fileRoot, strings.TrimSpace(stringValue(input["server"])))
	case toolName == "ListMcpResourcesTool":
		if err := s.validateRegisteredTool(toolName, input); err != nil {
			return nil, err
		}
		return mcpstdio.ListResources(ctx, s.fileRoot, strings.TrimSpace(stringValue(input["server"])))
	case toolName == "ReadMcpResourceTool":
		if err := s.validateRegisteredTool(toolName, input); err != nil {
			return nil, err
		}
		return mcpstdio.ReadResource(ctx, s.fileRoot, strings.TrimSpace(stringValue(input["server"])), strings.TrimSpace(stringValue(input["uri"])))
	case toolName == "MCPTool":
		if err := s.validateRegisteredTool(toolName, input); err != nil {
			return nil, err
		}
		serverName := strings.TrimSpace(stringValue(input["server"]))
		mcpToolName := strings.TrimSpace(stringValue(input["toolName"]))
		if mcpToolName == "authenticate" {
			return mcpstdio.Authenticate(ctx, s.fileRoot, serverName)
		}
		arguments := objectMapValue(input["input"])
		if len(arguments) == 0 {
			arguments = objectMapValue(input["arguments"])
		}
		output, callErr := mcpstdio.CallTool(ctx, s.fileRoot, serverName, mcpToolName, arguments)
		s.recordLegacyMCPInvocation(ctx, serverName, mcpToolName, callErr)
		return output, callErr
	case strings.HasPrefix(toolName, "mcp__"):
		serverName, mcpToolName, err := mcpstdio.ResolveDynamicTool(ctx, s.fileRoot, toolName)
		if err != nil {
			return nil, err
		}
		if mcpToolName == "authenticate" {
			return mcpstdio.Authenticate(ctx, s.fileRoot, serverName)
		}
		output, callErr := mcpstdio.CallTool(ctx, s.fileRoot, serverName, mcpToolName, input)
		s.recordLegacyMCPInvocation(ctx, serverName, mcpToolName, callErr)
		return output, callErr
	default:
		return nil, fmt.Errorf("unknown MCP tool: %s", toolName)
	}
}
