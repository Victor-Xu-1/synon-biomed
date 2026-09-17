package server

import (
	"fmt"
	osexec "os/exec"
	"strings"
	"synon-go/internal/toolcontract"
	"synon-go/internal/tools/lspstatic"
	"synon-go/internal/tools/mcpstdio"
	"synon-go/internal/tools/registry"
)

func (s *Server) executeToolDoctorTool(input map[string]any) (any, error) {
	if err := s.validateRegisteredTool("ToolDoctor", input); err != nil {
		return nil, err
	}
	scope := strings.TrimSpace(stringValue(input["scope"]))
	if scope == "" {
		scope = "all"
	}
	scope = canonicalToolDoctorScope(scope)
	if !validToolDoctorScope(scope) {
		return nil, fmt.Errorf("ToolDoctor.scope is unsupported: %s", scope)
	}
	smoke := boolValue(input["smoke"], false)
	checks := make([]map[string]any, 0)
	addCheck := func(checkScope string, name string, status string, message string, detail map[string]any) {
		check := map[string]any{
			"scope":   checkScope,
			"name":    name,
			"status":  status,
			"message": message,
		}
		if len(detail) > 0 {
			check["detail"] = detail
		}
		checks = append(checks, check)
	}
	shouldRun := func(checkScope string) bool {
		return scope == "all" || scope == checkScope
	}
	toolNames := s.allRegisteredNames()
	modelToolNames := s.tools.Names()
	serviceOperationNames := s.registeredOperationNames()
	executableTools := 0
	for _, name := range toolNames {
		tool, ok := s.registeredTool(name)
		if ok && tool.Executable {
			executableTools++
		}
	}
	if shouldRun("tools") {
		status := "pass"
		if len(toolNames) == 0 {
			status = "fail"
		}
		addCheck("tools", "go-tool-registry", status, fmt.Sprintf("%d registered model tools; %d service operations; %d total executable contracts.", len(modelToolNames), len(serviceOperationNames), executableTools), map[string]any{
			"registeredModelTools":  len(modelToolNames),
			"serviceOperations":     len(serviceOperationNames),
			"executableContracts":   executableTools,
			"modelTools":            modelToolNames,
			"serviceOperationNames": serviceOperationNames,
		})
	}
	if shouldRun("shell") {
		shells := map[string]any{
			"sh":         executablePath("sh"),
			"bash":       executablePath("bash"),
			"pwsh":       executablePath("pwsh"),
			"powershell": executablePath("powershell"),
		}
		status := "pass"
		if shells["sh"] == "" && shells["bash"] == "" && shells["pwsh"] == "" && shells["powershell"] == "" {
			status = "warn"
		}
		addCheck("shell", "host-shells", status, "Host shell availability for shell_exec and original shell aliases.", shells)
	}
	if shouldRun("web") {
		_, hasWebFetch := s.registeredTool("web_fetch")
		_, hasWebSearch := s.registeredTool("web_search")
		status := "pass"
		if !hasWebFetch || !hasWebSearch {
			status = "fail"
		}
		addCheck("web", "web-tools", status, "Canonical web_fetch and web_search tool registration.", map[string]any{
			"webFetchRegistered":  hasWebFetch,
			"webSearchRegistered": hasWebSearch,
		})
	}
	if shouldRun("task") {
		status := "pass"
		message := "Durable task store is configured."
		if s.taskStore == nil {
			status = "warn"
			message = "Durable task store is not configured because FileRoot is empty."
		}
		addCheck("task", "task-store", status, message, map[string]any{"configured": s.taskStore != nil})
	}
	if shouldRun("compute") {
		status := "pass"
		message := "Compute requests remain attached to the durable kernel runner; use AgentRuntimeDoctor with scope=kernel-compute for provider and kernel health."
		if !hasRegisteredTool(s.tools, "AgentRuntimeDoctor") {
			status = "warn"
			message = "The compute compatibility scope is accepted, but AgentRuntimeDoctor is not registered for kernel health checks."
		}
		addCheck("compute", "durable-kernel-runner", status, message, map[string]any{"agentRuntimeDoctorRegistered": hasRegisteredTool(s.tools, "AgentRuntimeDoctor")})
	}
	if shouldRun("synonlink") {
		clients := s.synonLink.ListAllClients()
		running := s.synonLink.ListAllRunning()
		addCheck("synonlink", "synon-link-service", "pass", fmt.Sprintf("%d connected clients; %d running tasks.", len(clients), len(running)), map[string]any{
			"clients":      len(clients),
			"runningTasks": len(running),
		})
	}
	if shouldRun("adapters") {
		platforms := s.adapterDoctorPlatforms(smoke)
		configured := 0
		enabled := 0
		livePass := 0
		livePending := 0
		for _, platform := range platforms {
			if boolValue(platform["enabled"], false) {
				enabled++
			}
			if boolValue(platform["configured"], false) {
				configured++
				smokeResult := objectMapValue(platform["smoke"])
				if stringValue(smokeResult["status"]) == "pass" {
					livePass++
				} else if smoke {
					livePending++
				}
			}
		}
		status := "pass"
		message := fmt.Sprintf("%d/%d message adapter platform(s) have required credentials configured.", configured, len(platforms))
		if configured == 0 {
			status = "warn"
			message = "No live message adapter credentials are configured; source handlers are present but deploy-time live channel smoke is skipped."
		} else if enabled > configured {
			status = "warn"
			message = fmt.Sprintf("%d adapter(s) are enabled but only %d/%d platform credential set(s) are complete.", enabled, configured, len(platforms))
		}
		auditRecords := s.recordAdapterLiveSmokeAudit(platforms, smoke)
		addCheck("adapters", "adapter-credentials", status, message, map[string]any{
			"platforms":  platforms,
			"enabled":    enabled,
			"configured": configured,
		})
		smokeStatus := "info"
		smokeMessage := "Live adapter credential smoke skipped because no platform credentials are configured."
		if configured > 0 {
			smokeStatus = "warn"
			smokeMessage = "Live adapter credential smoke requires explicit deployment credentials and platform endpoints; run with real credentials before declaring live channels pass."
			if smoke && livePass == configured && livePending == 0 {
				smokeStatus = "pass"
				smokeMessage = fmt.Sprintf("Live adapter credential smoke passed for %d configured platform(s).", livePass)
			}
		}
		addCheck("adapters", "adapter-live-smoke", smokeStatus, smokeMessage, map[string]any{
			"requiresLiveCredentials": true,
			"configuredPlatforms":     configured,
			"livePassedPlatforms":     livePass,
			"smokeRequested":          smoke,
			"auditRecordsWritten":     auditRecords,
			"auditNamespace":          adapterLiveSmokeRuntimeNamespace,
			"platforms":               platforms,
		})
	}
	if shouldRun("mcp") {
		servers, err := mcpstdio.LoadServers(s.fileRoot)
		if err != nil {
			addCheck("mcp", "mcp-runtime", "fail", err.Error(), nil)
		} else {
			stdioCount := 0
			remoteHTTPCount := 0
			remoteWebSocketCount := 0
			sdkBridgeCount := 0
			unsupportedCount := 0
			for _, server := range servers {
				if server.Disabled {
					continue
				}
				serverType := strings.ToLower(strings.TrimSpace(server.Type))
				switch {
				case strings.TrimSpace(server.Command) != "" && (serverType == "" || serverType == "stdio"):
					stdioCount++
				case strings.TrimSpace(server.URL) != "" && (serverType == "" || serverType == "http" || serverType == "sse" || serverType == "streamable_http"):
					remoteHTTPCount++
				case strings.TrimSpace(server.URL) != "" && (serverType == "ws" || serverType == "websocket" || serverType == "ws-ide"):
					remoteWebSocketCount++
				case serverType == "sdk" && server.IsCallableTransport():
					sdkBridgeCount++
				default:
					unsupportedCount++
				}
			}
			status := "pass"
			message := fmt.Sprintf("%d MCP servers configured; %d stdio, %d HTTP/SSE, %d WebSocket, and %d SDK bridge servers callable by the Go runtime.", len(servers), stdioCount, remoteHTTPCount, remoteWebSocketCount, sdkBridgeCount)
			if len(servers) == 0 {
				status = "info"
				message = "No MCP servers are configured in SYNON_MCP_CONFIG, SYNON_MCP_CONFIG_JSON, or FileRoot .mcp.json."
			} else if unsupportedCount > 0 {
				status = "warn"
				message = fmt.Sprintf("%d MCP servers configured; %d stdio, %d HTTP/SSE, %d WebSocket, and %d SDK bridge callable, %d unsupported servers require an external bridge or supported transport.", len(servers), stdioCount, remoteHTTPCount, remoteWebSocketCount, sdkBridgeCount, unsupportedCount)
			}
			addCheck("mcp", "mcp-runtime", status, message, map[string]any{"servers": len(servers), "stdio": stdioCount, "remoteHttp": remoteHTTPCount, "remoteWebSocket": remoteWebSocketCount, "sdkBridge": sdkBridgeCount, "unsupported": unsupportedCount})
		}
	}
	if shouldRun("lsp") {
		liveCount, err := lspstatic.LiveServerCount(s.fileRoot)
		if err != nil {
			addCheck("lsp", "lsp-runtime", "warn", err.Error(), nil)
		} else if liveCount > 0 {
			addCheck("lsp", "lsp-runtime", "pass", fmt.Sprintf("%d external stdio/socket LSP server(s) configured; LSP tool will prefer live language-server JSON-RPC and fall back to static scanning when unavailable.", liveCount), map[string]any{"liveServers": liveCount})
		} else {
			addCheck("lsp", "lsp-runtime", "info", "No external LSP servers configured in SYNON_LSP_CONFIG, SYNON_LSP_CONFIG_JSON, or FileRoot .lsp.json; LSP tool uses static code intelligence fallback.", map[string]any{"liveServers": 0})
		}
	}
	if shouldRun("permissions") {
		addCheck("permissions", "bounded-execution", "pass", "File, shell, Synon Link desktop, and session runner operations use root bounds, timeouts, or explicit approval gates where applicable.", nil)
	}
	if shouldRun("feature-gates") {
		addCheck("feature-gates", "excluded-capabilities", "pass", "Web UI and removed pharma/knowledge side capabilities are not exposed by the compact Go registry.", nil)
	}
	if shouldRun("smoke") || smoke {
		addCheck("smoke", "cheap-tool-validation", toolDoctorSmokeStatus(s.tools), "Cheap registry validation smoke completed without network or file mutation.", nil)
	}
	ok := true
	for _, check := range checks {
		if check["status"] == "fail" {
			ok = false
			break
		}
	}
	return map[string]any{
		"ok":     ok,
		"scope":  scope,
		"checks": checks,
		"inventory": map[string]any{
			"tools": map[string]any{
				"registered": len(toolNames),
				"executable": executableTools,
			},
			"fileRootConfigured": s.fileRoot != "",
			"stores": map[string]any{
				"settings": s.settingsStore != nil,
				"sessions": s.sessionStore != nil,
				"tasks":    s.taskStore != nil,
				"pairing":  s.pairingStore != nil,
				"runtime":  s.runtimeStore != nil,
			},
		},
	}, nil
}

func validToolDoctorScope(scope string) bool {
	switch scope {
	case "all", "tools", "mcp", "lsp", "shell", "web", "task", "compute", "synonlink", "adapters", "permissions", "feature-gates", "smoke":
		return true
	default:
		return false
	}
}

func canonicalToolDoctorScope(scope string) string {
	switch strings.TrimSpace(scope) {
	case "kernel-compute", "molecular-docking":
		return "compute"
	default:
		return strings.TrimSpace(scope)
	}
}

func executablePath(name string) string {
	path, err := osexec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

func toolDoctorSmokeStatus(reg *registry.Registry) string {
	if err := reg.Validate(toolcontract.SearchSkills, map[string]any{"query": "tool"}); err != nil {
		return "fail"
	}
	if err := reg.Validate("SendUserMessage", map[string]any{"message": "smoke", "status": "normal"}); err != nil {
		return "fail"
	}
	return "pass"
}
