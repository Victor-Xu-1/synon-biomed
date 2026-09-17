package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/mcpdirectory"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

type workspaceMCPRuntimeConnector struct {
	ID      string
	Name    string
	Source  string
	Enabled bool
	Custom  *workspace.MCPServer
}

// Keep one task-level watchdog around the whole MCP call (schema inspection,
// initialize, and tools/call). The transport has its own configurable budget,
// but a connector that does not terminate cleanly must still release the
// runner before the model turn becomes an unbounded background job.
const workspaceMCPToolExecutionTimeout = 90 * time.Second

type workspaceMCPRuntimeContext struct {
	UserID    string
	AgentName string
	// Servers remains the custom connector projection used by compatibility
	// tests and APIs. Connectors is the authoritative multi-source runtime list.
	Servers             []workspace.MCPServer
	Connectors          []workspaceMCPRuntimeConnector
	Excluded            map[string]struct{}
	ExcludedByConnector map[string]map[string]struct{}
}

type agentRuntimeMCPSchemaDiscovery struct {
	Schemas     []agentruntime.ToolSchema
	Unavailable bool
}

type workspaceMCPConnectorProbeResult struct {
	Tools []mcpstdio.ToolProjection
	Err   error
}

const workspaceMCPSourceEvidenceSchemaV1 = "synon.workspace_mcp_evidence.v1"

type workspaceMCPSourceEvidenceAttestation struct {
	Schema            string
	EvidenceClass     string
	ConnectorID       string
	ConnectorSource   string
	InputSchemaSHA256 string
	ReadOnlyHint      bool
}

type workspaceMCPSourceEvidenceCollector struct {
	mu      sync.Mutex
	records map[string]workspaceMCPSourceEvidenceAttestation
}

type workspaceMCPSourceEvidenceCollectorContextKey struct{}

func withWorkspaceMCPSourceEvidenceCollector(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, workspaceMCPSourceEvidenceCollectorContextKey{}, &workspaceMCPSourceEvidenceCollector{
		records: map[string]workspaceMCPSourceEvidenceAttestation{},
	})
}

func recordWorkspaceMCPSourceEvidence(
	ctx context.Context,
	toolCallID string,
	attestation workspaceMCPSourceEvidenceAttestation,
) {
	collector, _ := ctx.Value(workspaceMCPSourceEvidenceCollectorContextKey{}).(*workspaceMCPSourceEvidenceCollector)
	toolCallID = strings.TrimSpace(toolCallID)
	if collector == nil || toolCallID == "" || attestation.Schema != workspaceMCPSourceEvidenceSchemaV1 {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.records[toolCallID] = attestation
}

func takeWorkspaceMCPSourceEvidence(ctx context.Context, toolCallID string) (workspaceMCPSourceEvidenceAttestation, bool) {
	collector, _ := ctx.Value(workspaceMCPSourceEvidenceCollectorContextKey{}).(*workspaceMCPSourceEvidenceCollector)
	toolCallID = strings.TrimSpace(toolCallID)
	if collector == nil || toolCallID == "" {
		return workspaceMCPSourceEvidenceAttestation{}, false
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	attestation, found := collector.records[toolCallID]
	delete(collector.records, toolCallID)
	return attestation, found
}

func (s *Server) workspaceMCPRuntimeContext(sessionID string) (workspaceMCPRuntimeContext, bool, error) {
	return s.workspaceMCPRuntimeContextWithContext(context.Background(), sessionID)
}

func (s *Server) workspaceMCPRuntimeContextWithContext(ctx context.Context, sessionID string) (workspaceMCPRuntimeContext, bool, error) {
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(sessionID) == "" {
		return workspaceMCPRuntimeContext{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := agentRuntimeContextError(ctx); err != nil {
		return workspaceMCPRuntimeContext{}, false, err
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(strings.TrimSpace(sessionID))
	if err != nil || !found {
		return workspaceMCPRuntimeContext{}, found, err
	}
	userID := strings.TrimSpace(frameContext.UserID)
	agentName := strings.TrimSpace(frameContext.Frame.AgentName)
	agent, agentFound, err := s.workspaceStore.GetAgent(userID, agentName)
	if err != nil {
		return workspaceMCPRuntimeContext{}, false, err
	}
	connectors, servers, err := s.workspaceMCPRuntimeConnectorsWithContext(ctx, userID, agentName, agent, agentFound)
	if err != nil {
		return workspaceMCPRuntimeContext{}, false, err
	}
	selectedMCPIDs, constrained, err := s.webAssistantRuntimeMCPSelection(userID, sessionID)
	if err != nil {
		return workspaceMCPRuntimeContext{}, false, err
	}
	if constrained {
		selected := make(map[string]struct{}, len(selectedMCPIDs))
		for _, id := range selectedMCPIDs {
			selected[strings.TrimSpace(id)] = struct{}{}
		}
		filtered := connectors[:0]
		for _, connector := range connectors {
			if _, enabled := selected[connector.ID]; enabled {
				filtered = append(filtered, connector)
			}
		}
		connectors = filtered
		servers = runtimeCustomServers(connectors)
	}
	excluded, excludedByConnector, err := s.workspaceMCPRuntimeExclusions(userID, agentName, connectors)
	if err != nil {
		return workspaceMCPRuntimeContext{}, false, err
	}
	return workspaceMCPRuntimeContext{
		UserID: userID, AgentName: agentName, Servers: servers, Connectors: connectors,
		Excluded: excluded, ExcludedByConnector: excludedByConnector,
	}, true, nil
}

func (s *Server) workspaceMCPRuntimeConnectors(userID, agentName string, agent workspace.Agent, agentFound bool) ([]workspaceMCPRuntimeConnector, []workspace.MCPServer, error) {
	return s.workspaceMCPRuntimeConnectorsWithContext(context.Background(), userID, agentName, agent, agentFound)
}

func (s *Server) workspaceMCPRuntimeConnectorsWithContext(ctx context.Context, userID, agentName string, agent workspace.Agent, agentFound bool) ([]workspaceMCPRuntimeConnector, []workspace.MCPServer, error) {
	connectors := make([]workspaceMCPRuntimeConnector, 0)
	if strings.EqualFold(agentName, "OPERON") || (agentFound && agent.Unrestricted) {
		servers, err := s.workspaceStore.ListMCPServers(userID)
		if err != nil {
			return nil, nil, err
		}
		for index := range servers {
			server := servers[index]
			connectors = append(connectors, customWorkspaceMCPRuntimeConnector(server))
		}
		if s.mcpDirectory != nil {
			// Tool catalogs are a cacheable control-plane dependency. Start the
			// deduplicated warmup as soon as a runtime asks for connectors; waiting
			// for the background-service loop makes the first user turn pay every
			// remote connector handshake before the model can start.
			s.mcpDirectory.ScheduleBundledToolCatalogWarmup(userID)
			unified, err := s.mcpDirectory.ListUnifiedConnectors(ctx, userID)
			if err != nil {
				return nil, nil, fmt.Errorf("list unified MCP connectors: %w", err)
			}
			for _, connector := range unified {
				if connector.Source == "custom" {
					continue
				}
				connectors = append(connectors, workspaceMCPRuntimeConnector{
					ID: connector.ID, Name: connector.Name, Source: connector.Source, Enabled: connector.Enabled,
				})
			}
		}
		blocked := stringSetValues(agent.ConnectorTombstones)
		filtered := connectors[:0]
		for _, connector := range connectors {
			if _, excluded := blocked[connector.ID]; !excluded {
				filtered = append(filtered, connector)
			}
		}
		connectors = filtered
	} else {
		blocked := stringSetValues(agent.ConnectorTombstones)
		seen := make(map[string]struct{})
		if s.mcpDirectory != nil {
			for _, serverID := range s.bundledAgentDefaultConnectorIDs(agentName) {
				if _, excluded := blocked[serverID]; excluded {
					continue
				}
				connector, found, err := s.mcpDirectory.ResolveRuntimeConnector(ctx, userID, serverID)
				if err != nil {
					return nil, nil, err
				}
				if !found {
					continue
				}
				connectors = append(connectors, workspaceMCPRuntimeConnector{
					ID: connector.ID, Name: connector.Name, Source: connector.Source, Enabled: !connector.Config.Disabled,
				})
				seen[connector.ID] = struct{}{}
			}
		}
		if agentFound {
			attachments, err := s.workspaceStore.ListAgentConnectorAttachments(userID, agentName)
			if err != nil {
				return nil, nil, err
			}
			for _, attachment := range attachments {
				if _, excluded := blocked[attachment.ServerID]; excluded {
					continue
				}
				connector, found, err := s.workspaceMCPRuntimeConnectorFromAttachmentWithContext(ctx, userID, attachment)
				if err != nil {
					return nil, nil, err
				}
				if found {
					if _, duplicate := seen[connector.ID]; duplicate {
						continue
					}
					connectors = append(connectors, connector)
					seen[connector.ID] = struct{}{}
				}
			}
		}
	}
	if err := validateWorkspaceMCPRuntimeConnectorNames(connectors); err != nil {
		return nil, nil, err
	}
	return connectors, runtimeCustomServers(connectors), nil
}

func (s *Server) workspaceMCPRuntimeConnectorFromAttachment(userID string, attachment workspace.AgentConnectorAttachment) (workspaceMCPRuntimeConnector, bool, error) {
	return s.workspaceMCPRuntimeConnectorFromAttachmentWithContext(context.Background(), userID, attachment)
}

func (s *Server) workspaceMCPRuntimeConnectorFromAttachmentWithContext(ctx context.Context, userID string, attachment workspace.AgentConnectorAttachment) (workspaceMCPRuntimeConnector, bool, error) {
	if attachment.Source == "custom" {
		server, found, err := s.workspaceStore.GetMCPServer(attachment.ServerID, userID)
		if err != nil || !found {
			return workspaceMCPRuntimeConnector{}, found, err
		}
		return customWorkspaceMCPRuntimeConnector(server), true, nil
	}
	if s.mcpDirectory == nil {
		return workspaceMCPRuntimeConnector{}, false, errors.New("MCP directory service is not configured")
	}
	connector, found, err := s.mcpDirectory.ResolveRuntimeConnector(ctx, userID, attachment.ServerID)
	if err != nil || !found {
		return workspaceMCPRuntimeConnector{}, found, err
	}
	if connector.Source != attachment.Source {
		return workspaceMCPRuntimeConnector{}, false, fmt.Errorf(
			"connector %q source changed from %q to %q", attachment.ServerID, attachment.Source, connector.Source,
		)
	}
	return workspaceMCPRuntimeConnector{
		ID: connector.ID, Name: connector.Name, Source: connector.Source, Enabled: !connector.Config.Disabled,
	}, true, nil
}

func customWorkspaceMCPRuntimeConnector(server workspace.MCPServer) workspaceMCPRuntimeConnector {
	copy := server
	return workspaceMCPRuntimeConnector{
		ID: server.ID, Name: server.Name, Source: "custom", Enabled: server.Enabled, Custom: &copy,
	}
}

func runtimeCustomServers(connectors []workspaceMCPRuntimeConnector) []workspace.MCPServer {
	servers := make([]workspace.MCPServer, 0)
	for _, connector := range connectors {
		if connector.Custom != nil {
			servers = append(servers, *connector.Custom)
		}
	}
	return servers
}

func validateWorkspaceMCPRuntimeConnectorNames(connectors []workspaceMCPRuntimeConnector) error {
	seen := make(map[string]string, len(connectors))
	for _, connector := range connectors {
		if strings.TrimSpace(connector.ID) == "" || strings.TrimSpace(connector.Name) == "" {
			return errors.New("MCP runtime connector id and name are required")
		}
		normalized := mcpstdio.NormalizeName(connector.Name)
		if prior, found := seen[normalized]; found && prior != connector.ID {
			return fmt.Errorf("MCP connectors %q and %q normalize to the same runtime name %q", prior, connector.ID, normalized)
		}
		seen[normalized] = connector.ID
	}
	return nil
}

func (s *Server) workspaceMCPRuntimeExclusions(userID, agentName string, connectors []workspaceMCPRuntimeConnector) (map[string]struct{}, map[string]map[string]struct{}, error) {
	aggregated := make(map[string]struct{})
	byConnector := make(map[string]map[string]struct{}, len(connectors))
	for _, connector := range connectors {
		values, err := s.workspaceStore.GetAgentConnectorToolExclusionsForConnector(userID, agentName, connector.ID)
		if err != nil {
			return nil, nil, err
		}
		set := make(map[string]struct{}, len(values))
		for _, name := range values {
			name = strings.TrimSpace(name)
			if name != "" {
				set[name] = struct{}{}
				aggregated[name] = struct{}{}
			}
		}
		byConnector[connector.ID] = set
	}
	return aggregated, byConnector, nil
}

func stringSetValues(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func (s *Server) workspaceMCPRuntimeTarget(sessionID, serverName string) (workspaceMCPRuntimeConnector, workspaceMCPRuntimeContext, bool, error) {
	return s.workspaceMCPRuntimeTargetWithContext(context.Background(), sessionID, serverName)
}

func (s *Server) workspaceMCPRuntimeTargetWithContext(ctx context.Context, sessionID, serverName string) (workspaceMCPRuntimeConnector, workspaceMCPRuntimeContext, bool, error) {
	runtimeContext, found, err := s.workspaceMCPRuntimeContextWithContext(ctx, sessionID)
	if err != nil || !found {
		return workspaceMCPRuntimeConnector{}, runtimeContext, false, err
	}
	requested := strings.TrimSpace(serverName)
	wanted := mcpstdio.NormalizeName(requested)
	for _, connector := range runtimeContext.Connectors {
		if connector.Enabled &&
			(strings.EqualFold(connector.ID, requested) || mcpstdio.NormalizeName(connector.Name) == wanted) {
			return connector, runtimeContext, true, nil
		}
	}
	return workspaceMCPRuntimeConnector{}, runtimeContext, false, nil
}

func (s *Server) workspaceMCPServerPermissionPolicy(sessionID, toolName string, input map[string]any) (mcpServerPermissionPolicy, bool) {
	return s.workspaceMCPServerPermissionPolicyWithContext(context.Background(), sessionID, toolName, input)
}

func (s *Server) workspaceMCPServerPermissionPolicyWithContext(ctx context.Context, sessionID, toolName string, input map[string]any) (mcpServerPermissionPolicy, bool) {
	serverName, mcpToolName, ok := mcpPolicyTarget(toolName, input)
	if !ok {
		return mcpServerPermissionPolicy{}, false
	}
	connector, runtimeContext, found, err := s.workspaceMCPRuntimeTargetWithContext(ctx, sessionID, serverName)
	if err != nil {
		return mcpServerPermissionPolicy{Server: serverName, Tool: mcpToolName, Mode: "deny"}, true
	}
	if !found {
		return mcpServerPermissionPolicy{}, false
	}
	if runtimeContext.workspaceMCPToolExcluded(connector.ID, mcpToolName) {
		return mcpServerPermissionPolicy{Server: connector.Name, Tool: mcpToolName, Mode: "deny"}, true
	}
	mode, explicit, err := s.workspaceMCPRuntimeToolPolicy(runtimeContext.UserID, connector, mcpToolName)
	if err != nil {
		return mcpServerPermissionPolicy{Server: connector.Name, Tool: mcpToolName, Mode: "deny"}, true
	}
	if !explicit {
		return mcpServerPermissionPolicy{}, false
	}
	return mcpServerPermissionPolicy{Server: connector.Name, Tool: mcpToolName, Mode: mode}, true
}

func (runtimeContext workspaceMCPRuntimeContext) workspaceMCPToolExcluded(connectorID, toolName string) bool {
	_, excluded := runtimeContext.ExcludedByConnector[connectorID][strings.TrimSpace(toolName)]
	return excluded
}

func (s *Server) workspaceMCPRuntimeToolPolicy(userID string, connector workspaceMCPRuntimeConnector, toolName string) (string, bool, error) {
	if connector.Source == "custom" {
		grants, err := s.workspaceStore.ListGlobalMCPToolGrants(connector.ID, userID)
		if err != nil {
			return "", false, err
		}
		for _, grant := range grants {
			if grant.ToolName == toolName {
				if grant.Enabled {
					return "allow", true, nil
				}
				return "deny", true, nil
			}
		}
		return "", false, nil
	}
	policies, err := s.workspaceStore.ListMCPConnectorToolPolicies(userID, connector.ID)
	if err != nil {
		return "", false, err
	}
	for _, policy := range policies {
		if policy.ToolName == toolName {
			if policy.Enabled {
				return "allow", true, nil
			}
			return "deny", true, nil
		}
	}
	if connector.Source == "bundled" && s.mcpDirectory != nil {
		resolved, found, err := s.mcpDirectory.ResolveRuntimeConnector(context.Background(), userID, connector.ID)
		if err != nil {
			return "", false, err
		}
		if found {
			if mode, explicit := mcpServerPolicyMode(resolved.Config); explicit {
				return mode, true, nil
			}
		}
	}
	return "", false, nil
}

func (s *Server) executeWorkspaceMCPTool(ctx context.Context, sessionID, toolName string, input map[string]any, toolCallIDs ...string) (map[string]any, bool, error) {
	serverName, mcpToolName, ok := mcpPolicyTarget(toolName, input)
	if !ok {
		return nil, false, nil
	}
	connector, runtimeContext, found, err := s.workspaceMCPRuntimeTargetWithContext(ctx, sessionID, serverName)
	if err != nil {
		return nil, true, err
	}
	if !found {
		return nil, false, nil
	}
	if runtimeContext.workspaceMCPToolExcluded(connector.ID, mcpToolName) {
		return nil, true, fmt.Errorf("MCP tool %q is excluded for agent %q on connector %q", mcpToolName, runtimeContext.AgentName, connector.ID)
	}
	if mode, explicit, err := s.workspaceMCPRuntimeToolPolicy(runtimeContext.UserID, connector, mcpToolName); err != nil {
		return nil, true, err
	} else if explicit && mode == "deny" {
		return nil, true, fmt.Errorf("MCP tool %q is denied for connector %q", mcpToolName, connector.ID)
	}
	arguments := workspaceMCPToolArguments(toolName, input)
	attestation, sourceEvidence := s.workspaceMCPSourceEvidenceAttestation(ctx, runtimeContext.UserID, connector, mcpToolName)
	toolCtx, cancelTool := context.WithTimeout(ctx, workspaceMCPToolExecutionTimeout)
	defer cancelTool()
	var output string
	if connector.Source == "custom" {
		if connector.Custom == nil {
			return nil, true, errors.New("custom MCP runtime connector is missing its configuration")
		}
		config, err := s.runtimeMCPConfig(*connector.Custom)
		if err != nil {
			return nil, true, err
		}
		if strings.TrimSpace(config.URL) != "" {
			client, err := mcpdirectory.SecureHTTPClient(toolCtx, config.URL, s.httpClient)
			if err != nil {
				return nil, true, err
			}
			toolCtx = mcpstdio.WithHTTPClient(toolCtx, client)
		}
		output, err = mcpstdio.CallToolForServer(toolCtx, s.fileRoot, connector.Name, mcpToolName, arguments, config)
		s.recordMCPConnectorInvocation(toolCtx, runtimeContext.UserID, connector.ID, connector.Source, connector.Name, mcpToolName, err)
		if err != nil {
			if value := workspaceMCPToolTimeoutResult(toolCtx, err); value != nil {
				return value, true, nil
			}
			return nil, true, err
		}
	} else {
		if s.mcpDirectory == nil {
			return nil, true, errors.New("MCP directory service is not configured")
		}
		output, err = s.mcpDirectory.CallUnifiedConnectorTool(toolCtx, runtimeContext.UserID, connector.ID, mcpToolName, arguments)
		if err != nil {
			if value := workspaceMCPToolTimeoutResult(toolCtx, err); value != nil {
				return value, true, nil
			}
			return nil, true, err
		}
	}
	decodedOutput := decodeWorkspaceMCPToolOutput(output)
	if sourceEvidence && len(toolCallIDs) > 0 && workspaceMCPSourceResultSucceeded(decodedOutput) {
		recordWorkspaceMCPSourceEvidence(ctx, toolCallIDs[0], attestation)
	}
	// Bundled and custom MCP transports return a JSON document as text. Decode
	// it once at the canonical workspace MCP boundary so the shared runtime
	// outcome classifier can see nested sourceUnavailable/error envelopes. A
	// non-JSON textual result remains text and keeps the same public contract.
	return map[string]any{"ok": true, "result": decodedOutput}, true, nil
}

func workspaceMCPSourceResultSucceeded(value any) bool {
	switch typed := value.(type) {
	case string:
		var decoded any
		if json.Unmarshal([]byte(strings.TrimSpace(typed)), &decoded) == nil {
			value = decoded
		}
	case []byte:
		var decoded any
		if json.Unmarshal(typed, &decoded) == nil {
			value = decoded
		}
	}
	return agentruntime.ClassifyToolResult(value) == agentruntime.ToolResultSucceeded
}

func workspaceMCPToolTimeoutResult(ctx context.Context, err error) map[string]any {
	if err == nil || (ctx != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return nil
	}
	return map[string]any{
		"ok": true, "sourceUnavailable": true, "retryable": true,
		"code":     "mcp_tool_timeout",
		"error":    "MCP connector did not return within the task-level execution budget",
		"recovery": "record_the_connector_timeout_and_continue_with_successful_evidence_or_a_materially_different_authoritative_source",
	}
}

func decodeWorkspaceMCPToolOutput(output string) any {
	var decoded any
	if json.Unmarshal([]byte(output), &decoded) == nil {
		return decoded
	}
	return output
}

// executeWorkspaceListMCPTools keeps agent-session discovery on the same
// owner-scoped connector authority used by dynamic mcp__ execution. The
// process-wide SYNON_MCP_CONFIG catalog remains available only to legacy
// non-session callers; it is never a fallback for a workspace conversation.
func (s *Server) executeWorkspaceListMCPTools(
	ctx context.Context,
	sessionID string,
	input map[string]any,
) (mcpstdio.ToolListResult, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || s.workspaceStore == nil || sessionID == "" {
		return mcpstdio.ToolListResult{}, false, nil
	}
	if s.tools != nil {
		if err := s.validateRegisteredTool("ListMcpTools", input); err != nil {
			return mcpstdio.ToolListResult{}, true, err
		}
	}
	runtimeContext, found, err := s.workspaceMCPRuntimeContextWithContext(ctx, sessionID)
	if err != nil {
		return mcpstdio.ToolListResult{}, true, err
	}
	if !found {
		return mcpstdio.ToolListResult{}, true, fmt.Errorf("workspace MCP runtime context not found for session %q", sessionID)
	}

	serverFilter := strings.TrimSpace(stringValue(input["server"]))
	selected := make([]workspaceMCPRuntimeConnector, 0, len(runtimeContext.Connectors))
	for _, connector := range runtimeContext.Connectors {
		if serverFilter == "" || workspaceMCPConnectorMatches(connector, serverFilter) {
			selected = append(selected, connector)
		}
	}
	result := mcpstdio.ToolListResult{Servers: []mcpstdio.ServerProjection{}, Tools: []mcpstdio.ToolProjection{}}
	if serverFilter != "" && len(selected) == 0 {
		result.Servers = append(result.Servers, mcpstdio.ServerProjection{
			Name: serverFilter, Status: "not_found", Configured: false,
			Error: fmt.Sprintf("workspace MCP connector %q was not found; available connectors: %s", serverFilter, workspaceMCPConnectorNames(runtimeContext.Connectors)),
		})
		return result, true, nil
	}

	enabled := make([]workspaceMCPRuntimeConnector, 0, len(selected))
	for _, connector := range selected {
		if connector.Enabled {
			enabled = append(enabled, connector)
		}
	}
	// The provider start path may consume warm or local catalogs, but it must not
	// block on one slow remote MCP server. Missing catalogs keep warming in the
	// background and become available on the next exact snapshot; no connector
	// or method is removed from the configured directory.
	probeCtx, stopProbe := context.WithTimeout(ctx, agentRuntimeMCPForegroundSnapshotBudget)
	probeResults := s.probeWorkspaceMCPRuntimeConnectors(probeCtx, runtimeContext.UserID, enabled)
	stopProbe()
	probesByID := make(map[string]workspaceMCPConnectorProbeResult, len(enabled))
	for index, connector := range enabled {
		probesByID[connector.ID] = probeResults[index]
	}

	for _, connector := range selected {
		projection := mcpstdio.ServerProjection{
			Name: connector.Name, Status: "disabled", Configured: true, Scope: connector.Source,
		}
		if !connector.Enabled {
			result.Servers = append(result.Servers, projection)
			continue
		}
		probe := probesByID[connector.ID]
		if probe.Err != nil {
			if errors.Is(probe.Err, mcpdirectory.ErrConnectorAuthorizationRequired) {
				// Authorization is an explicit connector state, not a failed task
				// execution. Keep the connector visible for user action without
				// turning every model turn into a recoverable MCP failure.
				projection.Status = "auth_required"
				projection.Error = "connector authorization is required"
			} else {
				projection.Status = "failed"
				projection.Error = probe.Err.Error()
			}
			result.Servers = append(result.Servers, projection)
			continue
		}
		tools, err := s.workspaceMCPRuntimeVisibleTools(runtimeContext, connector, probe.Tools)
		if err != nil {
			projection.Status = "failed"
			projection.Error = err.Error()
			result.Servers = append(result.Servers, projection)
			continue
		}
		projection.Status = "connected"
		projection.ToolCount = len(tools)
		result.Servers = append(result.Servers, projection)
		result.Tools = append(result.Tools, tools...)
	}
	return result, true, nil
}

func workspaceMCPConnectorMatches(connector workspaceMCPRuntimeConnector, serverFilter string) bool {
	serverFilter = strings.TrimSpace(serverFilter)
	return strings.EqualFold(strings.TrimSpace(connector.ID), serverFilter) ||
		mcpstdio.NormalizeName(connector.Name) == mcpstdio.NormalizeName(serverFilter)
}

func workspaceMCPConnectorNames(connectors []workspaceMCPRuntimeConnector) string {
	names := make([]string, 0, len(connectors))
	for _, connector := range connectors {
		if name := strings.TrimSpace(connector.Name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func (s *Server) workspaceMCPRuntimeVisibleTools(
	runtimeContext workspaceMCPRuntimeContext,
	connector workspaceMCPRuntimeConnector,
	tools []mcpstdio.ToolProjection,
) ([]mcpstdio.ToolProjection, error) {
	visible := make([]mcpstdio.ToolProjection, 0, len(tools))
	for _, tool := range tools {
		if runtimeContext.workspaceMCPToolExcluded(connector.ID, tool.ToolName) {
			continue
		}
		mode, explicit, err := s.workspaceMCPRuntimeToolPolicy(runtimeContext.UserID, connector, tool.ToolName)
		if err != nil {
			return nil, fmt.Errorf("read MCP tool policy for connector %q: %w", connector.ID, err)
		}
		if explicit && mode == "deny" {
			continue
		}
		tool.Server = connector.Name
		tool.ServerStatus = "connected"
		visible = append(visible, tool)
	}
	return visible, nil
}

func (s *Server) workspaceMCPSourceEvidenceAttestation(
	ctx context.Context,
	userID string,
	connector workspaceMCPRuntimeConnector,
	toolName string,
) (workspaceMCPSourceEvidenceAttestation, bool) {
	if connector.Source != "bundled" || strings.TrimSpace(connector.ID) == "" {
		return workspaceMCPSourceEvidenceAttestation{}, false
	}
	tools, err := s.workspaceMCPRuntimeConnectorTools(ctx, userID, connector)
	if err != nil {
		return workspaceMCPSourceEvidenceAttestation{}, false
	}
	for _, tool := range tools {
		if tool.ToolName != toolName {
			continue
		}
		return workspaceMCPSourceEvidenceAttestationForTool(connector, tool)
	}
	return workspaceMCPSourceEvidenceAttestation{}, false
}

func workspaceMCPSourceEvidenceAttestationForTool(
	connector workspaceMCPRuntimeConnector,
	tool mcpstdio.ToolProjection,
) (workspaceMCPSourceEvidenceAttestation, bool) {
	if connector.Source != "bundled" || strings.TrimSpace(connector.ID) == "" ||
		!tool.ReadOnlyHint || len(tool.InputSchema) == 0 || strings.TrimSpace(tool.ToolName) == "" {
		return workspaceMCPSourceEvidenceAttestation{}, false
	}
	schemaJSON, err := json.Marshal(tool.InputSchema)
	if err != nil || !json.Valid(schemaJSON) {
		return workspaceMCPSourceEvidenceAttestation{}, false
	}
	return workspaceMCPSourceEvidenceAttestation{
		Schema:        workspaceMCPSourceEvidenceSchemaV1,
		EvidenceClass: workspace.KernelMCPEvidenceClassBundledReadOnly,
		ConnectorID:   connector.ID, ConnectorSource: connector.Source,
		InputSchemaSHA256: kernelMCPEvidenceSHA256(schemaJSON), ReadOnlyHint: true,
	}, true
}

func workspaceMCPToolArguments(toolName string, input map[string]any) map[string]any {
	if strings.TrimSpace(toolName) == "MCPTool" {
		if arguments := objectMapValue(input["input"]); len(arguments) > 0 {
			return arguments
		}
		if arguments := objectMapValue(input["arguments"]); len(arguments) > 0 {
			return arguments
		}
	}
	return input
}

func (s *Server) agentRuntimeWorkspaceMCPToolSchemas(ctx context.Context, sessionID string, allowed map[string]struct{}, existing []agentruntime.ToolSchema) agentRuntimeMCPSchemaDiscovery {
	runtimeContext, found, err := s.workspaceMCPRuntimeContextWithContext(ctx, sessionID)
	if err != nil {
		if agentRuntimeContextError(ctx) == nil {
			log.Printf("workspace MCP tool snapshot unavailable session=%q stage=runtime_context error=%v", strings.TrimSpace(sessionID), err)
		}
		return agentRuntimeMCPSchemaDiscovery{Unavailable: agentRuntimeContextError(ctx) == nil}
	}
	if !found {
		if strings.TrimSpace(sessionID) != "" {
			log.Printf("workspace MCP tool snapshot omitted session=%q stage=runtime_context reason=frame_not_found", strings.TrimSpace(sessionID))
		}
		return agentRuntimeMCPSchemaDiscovery{}
	}
	enabled := make([]workspaceMCPRuntimeConnector, 0, len(runtimeContext.Connectors))
	for _, connector := range runtimeContext.Connectors {
		if connector.Enabled {
			enabled = append(enabled, connector)
		}
	}
	if len(enabled) == 0 && len(runtimeContext.Connectors) > 0 {
		log.Printf("workspace MCP tool snapshot omitted session=%q stage=connector_selection connectors=%d enabled=0",
			strings.TrimSpace(sessionID), len(runtimeContext.Connectors))
	}
	probeResults := s.probeWorkspaceMCPRuntimeConnectors(ctx, runtimeContext.UserID, enabled)
	seen := make(map[string]struct{}, len(existing))
	for _, schema := range existing {
		seen[schema.Name] = struct{}{}
	}
	result := make([]agentruntime.ToolSchema, 0)
	unavailable := false
	for index, connector := range enabled {
		probe := probeResults[index]
		if probe.Err != nil {
			if errors.Is(probe.Err, mcpdirectory.ErrConnectorAuthorizationRequired) {
				// Unauthorised connectors are intentionally omitted from the model
				// schema snapshot. They remain discoverable through the connector
				// status surface and become eligible automatically after auth.
				continue
			}
			log.Printf("MCP connector %s tools unavailable: %v", connector.ID, probe.Err)
			if agentRuntimeContextError(ctx) == nil {
				unavailable = true
			}
			continue
		}
		for _, tool := range probe.Tools {
			if runtimeContext.workspaceMCPToolExcluded(connector.ID, tool.ToolName) {
				continue
			}
			if mode, explicit, err := s.workspaceMCPRuntimeToolPolicy(runtimeContext.UserID, connector, tool.ToolName); err != nil {
				unavailable = true
				continue
			} else if explicit && mode == "deny" {
				continue
			}
			if _, duplicate := seen[tool.Name]; duplicate || !chatRunnerToolAllowed(tool.Name, allowed) {
				continue
			}
			description := strings.TrimSpace(tool.Description)
			if description == "" {
				description = fmt.Sprintf("Call MCP tool %s on server %s.", tool.ToolName, tool.Server)
			}
			parameters := tool.InputSchema
			if len(parameters) == 0 {
				parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			result = append(result, agentruntime.ToolSchema{
				Name: tool.Name, Description: description, Parameters: parameters, OutputSchema: tool.OutputSchema,
			})
			seen[tool.Name] = struct{}{}
		}
	}
	if len(enabled) > 0 && len(result) == 0 {
		log.Printf("workspace MCP tool snapshot omitted session=%q stage=tool_catalog enabled_connectors=%d unavailable=%t",
			strings.TrimSpace(sessionID), len(enabled), unavailable)
	}
	return agentRuntimeMCPSchemaDiscovery{Schemas: result, Unavailable: unavailable}
}

// probeWorkspaceMCPRuntimeConnectors is the single bounded tools/list path for
// both model schema discovery and the model-visible ListMcpTools tool.
func (s *Server) probeWorkspaceMCPRuntimeConnectors(
	ctx context.Context,
	userID string,
	connectors []workspaceMCPRuntimeConnector,
) []workspaceMCPConnectorProbeResult {
	results := make([]workspaceMCPConnectorProbeResult, len(connectors))
	if len(connectors) == 0 {
		return results
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, stopProbes := context.WithTimeout(ctx, agentRuntimeMCPDiscoveryBudget)
	defer stopProbes()
	probeJobs := make(chan int, len(connectors))
	for index := range connectors {
		probeJobs <- index
	}
	close(probeJobs)
	workerCount := min(defaultMCPDiscoveryConcurrency, len(connectors))
	var probeWG sync.WaitGroup
	probeWG.Add(workerCount)
	for range workerCount {
		go func() {
			defer probeWG.Done()
			for index := range probeJobs {
				connectorCtx, connectorStop := context.WithTimeout(probeCtx, agentRuntimeMCPConnectorProbeBudget)
				if err := s.acquireMCPDiscoverySlot(connectorCtx); err != nil {
					connectorStop()
					results[index] = workspaceMCPConnectorProbeResult{Err: err}
					continue
				}
				tools, err := s.workspaceMCPRuntimeConnectorTools(connectorCtx, userID, connectors[index])
				connectorStop()
				s.releaseMCPDiscoverySlot()
				results[index] = workspaceMCPConnectorProbeResult{Tools: tools, Err: err}
			}
		}()
	}
	probeWG.Wait()
	return results
}

const (
	agentRuntimeMCPForegroundSnapshotBudget = time.Second
	agentRuntimeMCPDiscoveryBudget          = 20 * time.Second
	agentRuntimeMCPConnectorProbeBudget     = 18 * time.Second
)

const defaultMCPDiscoveryConcurrency = 8

var processMCPDiscoverySlots = make(chan struct{}, defaultMCPDiscoveryConcurrency)

func (s *Server) acquireMCPDiscoverySlot(ctx context.Context) error {
	if s == nil || s.mcpDiscoverySlots == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case s.mcpDiscoverySlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) releaseMCPDiscoverySlot() {
	if s == nil || s.mcpDiscoverySlots == nil {
		return
	}
	<-s.mcpDiscoverySlots
}

func (s *Server) workspaceMCPRuntimeConnectorTools(ctx context.Context, userID string, connector workspaceMCPRuntimeConnector) ([]mcpstdio.ToolProjection, error) {
	if connector.Source != "custom" {
		if s.mcpDirectory == nil {
			return nil, errors.New("MCP directory service is not configured")
		}
		return s.mcpDirectory.ListUnifiedConnectorTools(ctx, userID, connector.ID)
	}
	if connector.Custom == nil {
		return nil, errors.New("custom MCP runtime connector is missing its configuration")
	}
	config, err := s.runtimeMCPConfig(*connector.Custom)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.URL) != "" {
		client, err := mcpdirectory.SecureHTTPClient(ctx, config.URL, s.httpClient)
		if err != nil {
			return nil, err
		}
		ctx = mcpstdio.WithHTTPClient(ctx, client)
	}
	return mcpstdio.ListToolsForServer(ctx, s.fileRoot, connector.Name, config)
}

func (s *Server) workspaceMCPRuntimeConnectorToolsStable(ctx context.Context, userID string, connector workspaceMCPRuntimeConnector) ([]mcpstdio.ToolProjection, error) {
	if connector.Source != "custom" && s.mcpDirectory != nil {
		return s.mcpDirectory.ListUnifiedConnectorToolsStable(ctx, userID, connector.ID)
	}
	return s.workspaceMCPRuntimeConnectorTools(ctx, userID, connector)
}
