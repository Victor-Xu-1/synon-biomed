package server

import (
	"net/http"

	"synon-go/internal/mcpdirectory"
)

type bundledMCPRuntimeReadiness interface {
	MCPPythonEnvironmentName() string
	RuntimeReady(language, environment string) bool
}

func bundledMCPRuntimeIsReady(runtime bundledMCPRuntimeReadiness) bool {
	return runtime != nil && runtime.RuntimeReady("python", runtime.MCPPythonEnvironmentName())
}

func (s *Server) listMCPDirectoryAndCustomConnectors(r *http.Request, userID string) ([]mcpdirectory.Connector, error) {
	if s.backgroundServicesStarted && bundledMCPRuntimeIsReady(s.kernelManager) {
		s.mcpDirectory.ScheduleMissingBundledProbe(userID)
		s.mcpDirectory.ScheduleBundledToolCatalogWarmup(userID)
	}
	return s.mcpDirectory.ListUnifiedConnectors(r.Context(), userID)
}

func mcpRuntimeSummary(connectors []mcpdirectory.Connector) map[string]any {
	bySource := map[string]int{}
	byStatus := map[string]int{}
	enabled := 0
	totalTools := 0
	for _, connector := range connectors {
		bySource[connector.Source]++
		status := connector.ConnectionStatus
		if status == "" {
			status = "unknown"
		}
		byStatus[status]++
		if connector.Enabled {
			enabled++
		}
		totalTools += connector.ToolCount
	}
	return map[string]any{
		"totalConnectors": len(connectors), "enabledConnectors": enabled,
		"advertisedMethods": totalTools, "bySource": bySource, "byConnectionStatus": byStatus,
		"modelExecutionSurface": "repl/host.mcp", "flattenedModelTools": false,
		"customServerCollection":  "/api/mcp-servers",
		"unifiedConnectorCatalog": "/api/mcp-servers/connectors",
	}
}
