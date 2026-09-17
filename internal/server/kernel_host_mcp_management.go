package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

var kernelHostMCPManagementMethods = []string{
	"host.mcp.list",
	"host.mcp.install",
	"host.mcp.authorize",
	"host.mcp.remove",
}

func isKernelMCPManagementHostMethod(method string) bool {
	for _, candidate := range kernelHostMCPManagementMethods {
		if method == candidate {
			return true
		}
	}
	return false
}

func (s *Server) handleKernelMCPManagementHostCall(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	callID string,
	method string,
	args []any,
	kwargs map[string]any,
) (any, error) {
	if s == nil || s.workspaceStore == nil {
		return nil, kernelruntime.NewHostCallError("unavailable", "MCP registry is not configured")
	}
	userID := strings.TrimSpace(access.UserID)
	switch method {
	case "host.mcp.list":
		if err := kernelHostExpectNoArguments(args, kwargs, method); err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		if s.mcpDirectory == nil {
			return nil, kernelruntime.NewHostCallError("unavailable", "MCP directory is not configured")
		}
		connectors, err := s.mcpDirectory.ListUnifiedConnectors(ctx, userID)
		if err != nil {
			return nil, kernelHostRegistryError(err)
		}
		items := make([]map[string]any, 0, len(connectors))
		for _, connector := range connectors {
			items = append(items, map[string]any{
				"name": connector.ID, "displayName": connector.DisplayName, "description": connector.Description,
				"source": connector.Source, "authState": connector.AuthState, "enabled": connector.Enabled,
				"connectionStatus": connector.ConnectionStatus, "attachedAgents": connector.AttachedAgents,
			})
		}
		return items, nil
	case "host.mcp.install":
		input, err := kernelHostMCPInstallInput(args, kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		if input.ConnectorID != "" {
			if s.mcpDirectory == nil {
				return nil, kernelruntime.NewHostCallError("unavailable", "MCP directory is not configured")
			}
			connector, found, err := s.mcpDirectory.ResolveRuntimeConnector(ctx, userID, input.ConnectorID)
			if err != nil || !found {
				if err == nil {
					err = fmt.Errorf("connector %q was not found", input.ConnectorID)
				}
				return nil, kernelHostRegistryError(err)
			}
			updated, err := s.mcpDirectory.SetUnifiedEnabled(ctx, userID, input.ConnectorID, true)
			if err != nil {
				return nil, kernelHostRegistryError(err)
			}
			return map[string]any{
				"installed": true, "id": updated.ID, "name": updated.Name,
				"source": connector.Source, "authState": updated.AuthState,
				"connectionStatus": updated.ConnectionStatus,
			}, nil
		}
		if input.Name == "" || input.Transport.Type == "" {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "name and transport are required")
		}
		if len(input.Transport.Headers) > 0 || len(input.Transport.Env) > 0 {
			return nil, kernelruntime.NewHostCallError(
				"permission_denied", "MCP headers and environment credentials must be configured through the credentialed settings flow",
			)
		}
		serverID := uuid.NewString()
		transport := input.Transport
		prepared, err := prepareWebMCPRecord(ctx, userID, serverID, webMCPMutationRequest{
			Name: &input.Name, Description: &input.Description, Transport: &transport,
		})
		if err != nil {
			return nil, kernelHostRegistryError(err)
		}
		approvalInput := map[string]any{
			"name": input.Name, "description": input.Description, "transport": transport.Type,
		}
		if transport.URL != "" {
			approvalInput["url"] = transport.URL
		}
		if transport.Command != "" {
			approvalInput["command"] = transport.Command
		}
		if len(transport.Args) > 0 {
			approvalInput["args"] = append([]string(nil), transport.Args...)
		}
		metadata := map[string]any{
			"title":        "Install MCP connector for this task",
			"description":  "Register the connector, verify it, then continue the current task.",
			"mcpName":      input.Name,
			"mcpTransport": transport.Type,
			"mcpURL":       transport.URL,
			"mcpCommand":   transport.Command,
		}
		if err := s.requireKernelCapabilityInstallApproval(
			ctx, access, callID, method, "mcp", approvalInput, metadata,
		); err != nil {
			return nil, err
		}
		if err := s.createWebMCPConfigSecret(userID, prepared.SecretRef, prepared.Config); err != nil {
			return nil, kernelHostRegistryError(err)
		}
		server, err := s.workspaceStore.CreateMCPServer(prepared.ServerInput)
		if err != nil {
			_ = s.deleteWebMCPConfigSecret(userID, prepared.SecretRef)
			return nil, kernelHostRegistryError(err)
		}
		if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": server.ID, "action": "created"}); err != nil {
			return nil, kernelHostRegistryError(err)
		}
		return map[string]any{
			"installed": true, "id": server.ID, "name": server.Name,
			"transport": server.Transport, "url": server.URL, "authState": "not-required",
		}, nil
	case "host.mcp.authorize":
		connectorID, err := kernelHostSingleNameInput(args, kwargs, method)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		if s.mcpDirectory == nil || uuid.Validate(connectorID) != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "authorize requires a directory connector UUID")
		}
		result, err := s.mcpDirectory.Authorize(ctx, userID, connectorID)
		if err != nil {
			return nil, kernelHostRegistryError(err)
		}
		return result, nil
	case "host.mcp.remove":
		connectorID, err := kernelHostSingleNameInput(args, kwargs, method)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		return s.kernelHostRemoveCustomMCP(userID, connectorID)
	default:
		return nil, kernelruntime.NewHostCallError("method_not_allowed", "host MCP management method is not allowed")
	}
}

type kernelHostMCPInstallRequest struct {
	ConnectorID string
	Name        string
	Description string
	Transport   webMCPTransport
}

func kernelHostMCPInstallInput(args []any, kwargs map[string]any) (kernelHostMCPInstallRequest, error) {
	if len(args) > 1 {
		return kernelHostMCPInstallRequest{}, errors.New("host.mcp.install accepts one configuration object")
	}
	config := map[string]any{}
	if len(args) == 1 {
		value, ok := args[0].(map[string]any)
		if !ok {
			return kernelHostMCPInstallRequest{}, errors.New("MCP install configuration must be an object")
		}
		config = value
		if len(kwargs) != 0 {
			return kernelHostMCPInstallRequest{}, errors.New("MCP install configuration cannot mix positional and keyword fields")
		}
	} else {
		config = kwargs
	}
	allowed := map[string]bool{
		"connector_id": true, "name": true, "description": true, "transport": true,
		"url": true, "command": true, "args": true, "headers": true, "env": true,
	}
	for key := range config {
		if !allowed[key] {
			return kernelHostMCPInstallRequest{}, fmt.Errorf("unknown MCP install field %q", key)
		}
	}
	result := kernelHostMCPInstallRequest{
		ConnectorID: stringValue(config["connector_id"]), Name: strings.TrimSpace(stringValue(config["name"])),
		Description: strings.TrimSpace(stringValue(config["description"])),
		Transport:   webMCPTransport{Type: strings.TrimSpace(stringValue(config["transport"])), URL: strings.TrimSpace(stringValue(config["url"])), Command: strings.TrimSpace(stringValue(config["command"]))},
	}
	if result.ConnectorID != "" {
		if len(config) != 1 {
			return kernelHostMCPInstallRequest{}, errors.New("connector_id cannot be combined with a new MCP configuration")
		}
		return result, nil
	}
	if result.Transport.Type == "" {
		result.Transport.Type = "streamable_http"
	}
	if rawArgs, found := config["args"]; found {
		args, err := kernelHostStringSlice(rawArgs, "args")
		if err != nil {
			return kernelHostMCPInstallRequest{}, err
		}
		result.Transport.Args = args
	}
	if rawHeaders, found := config["headers"]; found && rawHeaders != nil {
		if _, ok := rawHeaders.(map[string]any); !ok {
			return kernelHostMCPInstallRequest{}, errors.New("headers must be an object")
		}
		result.Transport.Headers = map[string]string{"__blocked__": "credentials must use the settings flow"}
	}
	if rawEnv, found := config["env"]; found && rawEnv != nil {
		if _, ok := rawEnv.(map[string]any); !ok {
			return kernelHostMCPInstallRequest{}, errors.New("env must be an object")
		}
		result.Transport.Env = map[string]string{"__blocked__": "credentials must use the settings flow"}
	}
	return result, nil
}

func (s *Server) kernelHostRemoveCustomMCP(userID, serverID string) (any, error) {
	server, found, err := s.workspaceStore.GetMCPServer(serverID, userID)
	if err != nil {
		return nil, kernelHostRegistryError(err)
	}
	if !found {
		return nil, kernelruntime.NewHostCallError("not_found", fmt.Sprintf("MCP server %q was not found", serverID))
	}
	if server.Builtin {
		return nil, kernelruntime.NewHostCallError("permission_denied", "built-in MCP servers cannot be removed")
	}
	_, ref, err := s.loadWebMCPStoredConfig(server)
	if err != nil {
		return nil, kernelHostRegistryError(err)
	}
	if ref != "" {
		if err := s.deleteWebMCPConfigSecret(userID, ref); err != nil {
			return nil, kernelHostRegistryError(err)
		}
	}
	if err := s.workspaceStore.DeleteMCPServer(server.ID, userID); err != nil {
		return nil, kernelHostRegistryError(err)
	}
	if s.fileRoot != "" {
		_ = mcpstdio.DisconnectOAuth(s.fileRoot, server.Name)
	}
	if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": server.ID, "action": "deleted"}); err != nil {
		return nil, kernelHostRegistryError(err)
	}
	return map[string]any{"deleted": server.ID, "name": server.Name}, nil
}
