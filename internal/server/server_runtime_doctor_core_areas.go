package server

import (
	"strings"
	"synon-go/internal/providers"
	"synon-go/internal/toolcontract"
)

func (s *Server) modelRunnerDoctorArea() (string, string, map[string]any, []string) {
	diagnostics := RunnerDiagnostics{}
	if s != nil {
		diagnostics = s.runnerDiagnostics
	}
	if isZeroRunnerDiagnostics(diagnostics) {
		diagnostics = defaultRunnerDiagnostics()
	}
	hasSecretDiagnosticsRedaction := true
	provider := strings.TrimSpace(diagnostics.Provider)
	if provider == "" {
		provider = "disabled"
	}
	endpointConfigured := strings.TrimSpace(diagnostics.ChatEndpoint) != ""
	modelConfigured := strings.TrimSpace(diagnostics.ChatModel) != ""
	commandConfigured := diagnostics.CommandConfigured
	supportsCommandRunner := supportedSessionRunnerProvider("command")
	supportsOpenAIChatRunner := supportedSessionRunnerProvider("openai_chat")
	hasBuiltinFallbackRunner := supportedSessionRunnerProvider("go_builtin") && BuiltinSessionRunnerChatEndpoint != "" && BuiltinSessionRunnerChatModel != ""
	supportsWorkspaceModelAuthority := supportedSessionRunnerProvider(WorkspaceSessionRunnerProvider)
	workspaceModelResolved := false
	workspaceProviderID := ""
	if provider == WorkspaceSessionRunnerProvider && supportsWorkspaceModelAuthority && s != nil {
		resolution, err := providers.ResolveRunnerModelProfile(s.settingsStore, s.workspaceStore, s.secretStore, providers.ResolutionInput{
			MaxResponseBytes: defaultSessionRunnerModelResponseLimitBytes,
		})
		if err == nil && resolution.Resolved && resolution.ModelProfile != nil {
			workspaceModelResolved = true
			workspaceProviderID = resolution.ProviderID
			endpointConfigured = strings.TrimSpace(resolution.ModelProfile.Provider.Endpoint) != ""
			modelConfigured = strings.TrimSpace(resolution.ModelProfile.Model) != ""
		}
	}
	toolRoundPolicyValid := diagnostics.ChatToolRoundLimit >= 0
	toolRoundLimitUnlimited := diagnostics.ChatToolRoundLimit == 0
	toolRoundLimitImplemented := toolRoundPolicyValid
	runnerBoundsImplemented := diagnostics.ReplayLimit > 0 && diagnostics.OutputLimitBytes > 0
	runnerLoopImplemented := diagnostics.PollIntervalMS > 0 && diagnostics.LeaseTTLSeconds > 0
	hasRunnerConfigDiagnostics := strings.TrimSpace(diagnostics.RunnerID) != "" && provider != "disabled"
	setupHints := runnerSetupHints(provider)
	hasSetupHints := len(setupHints) > 0
	chatReady := provider == "openai_chat" && supportsOpenAIChatRunner && endpointConfigured && modelConfigured
	commandReady := provider == "command" && supportsCommandRunner && commandConfigured
	builtinReady := provider == "go_builtin" && hasBuiltinFallbackRunner && endpointConfigured && modelConfigured
	workspaceReady := provider == WorkspaceSessionRunnerProvider && workspaceModelResolved && endpointConfigured && modelConfigured
	ready := diagnostics.Enabled && hasRunnerConfigDiagnostics && toolRoundPolicyValid && runnerBoundsImplemented && runnerLoopImplemented && hasSetupHints && (chatReady || commandReady || builtinReady || workspaceReady)
	missing := []string{}
	if !diagnostics.Enabled {
		missing = append(missing, "runner.enabled")
	}
	if !toolRoundPolicyValid {
		missing = append(missing, "runner.chat_tool_round_limit (use 0 for unlimited or a positive value)")
	}
	if diagnostics.ReplayLimit <= 0 {
		missing = append(missing, "runner.replay_limit")
	}
	if diagnostics.OutputLimitBytes <= 0 {
		missing = append(missing, "runner.output_limit_bytes")
	}
	switch provider {
	case "openai_chat":
		if !endpointConfigured {
			missing = append(missing, "runner.chat_endpoint")
		}
		if !modelConfigured {
			missing = append(missing, "runner.chat_model")
		}
	case "go_builtin":
		if !endpointConfigured {
			missing = append(missing, "runner.builtin_endpoint")
		}
		if !modelConfigured {
			missing = append(missing, "runner.builtin_model")
		}
	case WorkspaceSessionRunnerProvider:
		if !workspaceModelResolved {
			missing = append(missing, "active workspace model provider")
		}
	case "command":
		if !commandConfigured {
			missing = append(missing, "runner.command")
		}
	default:
		if provider == "disabled" {
			missing = append(missing, "runner.provider")
		} else {
			missing = append(missing, "supported runner.provider")
		}
	}
	status := "partial"
	message := "The runner requires an active saved model Provider or an explicit external runner configuration. The deterministic Go endpoint is available only when go_builtin is selected explicitly for test or development use."
	if ready {
		status = "pass"
		message = "Session runner deployment diagnostics report a ready explicit model authority with a valid tool-round policy, bounded replay, retries, and output. A zero tool-round limit means the tool loop is intentionally unlimited. Readiness does not substitute for the final authorized live-model gate."
	}
	evidence := map[string]any{
		"transport":                       provider,
		"supportsCommandRunner":           supportsCommandRunner,
		"supportsOpenAIChatRunner":        supportsOpenAIChatRunner,
		"hasBuiltinFallbackRunner":        hasBuiltinFallbackRunner,
		"builtinExplicitOnly":             true,
		"supportsWorkspaceModelAuthority": supportsWorkspaceModelAuthority,
		"workspaceModelResolved":          workspaceModelResolved,
		"workspaceProviderId":             workspaceProviderID,
		"toolRoundLimitImplemented":       toolRoundLimitImplemented,
		"toolRoundPolicyValid":            toolRoundPolicyValid,
		"chatToolRoundLimit":              diagnostics.ChatToolRoundLimit,
		"chatToolRoundsUnlimited":         toolRoundLimitUnlimited,
		"runnerBoundsImplemented":         runnerBoundsImplemented,
		"runnerLoopImplemented":           runnerLoopImplemented,
		"hasRunnerConfigDiagnostics":      hasRunnerConfigDiagnostics,
		"hasSetupHints":                   hasSetupHints,
		"ready":                           ready,
		"enabled":                         diagnostics.Enabled,
		"provider":                        provider,
		"runnerId":                        diagnostics.RunnerID,
		"commandConfigured":               commandConfigured,
		"chatEndpointConfigured":          endpointConfigured,
		"chatModelConfigured":             modelConfigured,
		"chatAPIKeyConfigured":            diagnostics.ChatAPIKeySet,
		"chatAPIKeySource":                redactDiagnosticSecretSource(firstNonEmpty(diagnostics.ChatAPIKeySource, "none")),
		"hasSecretDiagnosticsRedaction":   hasSecretDiagnosticsRedaction,
		"chatToolCount":                   len(diagnostics.ChatTools),
		"pollIntervalMS":                  diagnostics.PollIntervalMS,
		"leaseTTLSeconds":                 diagnostics.LeaseTTLSeconds,
		"replayLimit":                     diagnostics.ReplayLimit,
		"outputLimitBytes":                diagnostics.OutputLimitBytes,
		"missing":                         missing,
		"setup":                           setupHints,
	}
	next := []string{}
	if !ready {
		next = append(next, "Configure and enable runner provider settings, then re-run AgentRuntimeDoctor.")
	}
	return status, message, evidence, next
}

func (s *Server) queryEngineDoctorArea(toolNames []string) (string, string, map[string]any, []string) {
	hasSessionJournal := s.eventJournal != nil
	hasSessionStore := s.sessionStore != nil
	hasRuntimeSkillContext := s.skillCatalog != nil
	hasRuntimeMemoryContext := s.runtimeStore != nil
	hasRuntimePermissionGateway := hasRegisteredTool(s.tools, toolcontract.AskUser) && hasRegisteredTool(s.tools, "SendMessage") && s.settingsStore != nil && s.runtimeStore != nil
	hasRuntimeHookContext := hasRegisteredTool(s.tools, "Bash") && hasRegisteredTool(s.tools, "Shell") && hasRegisteredTool(s.tools, "powershell") && s.settingsStore != nil && s.runtimeStore != nil
	hasRuntimeCompactContext := hasRegisteredTool(s.tools, "Compact") && hasSessionJournal && hasRuntimeMemoryContext
	hasRuntimeMCPContext := hasRegisteredTool(s.tools, "MCPTool") &&
		hasRegisteredTool(s.tools, "ListMcpTools") &&
		hasRegisteredTool(s.tools, "ListMcpResourcesTool") &&
		hasRegisteredTool(s.tools, "ReadMcpResourceTool")
	hasRuntimeProviderCacheContext := hasSessionJournal && hasSessionStore
	hasTaskRunAgentRuntimeLoop := hasRegisteredTool(s.tools, "TaskRun") && hasRegisteredTool(s.tools, "Agent") && s.taskRunStore != nil && s.taskStore != nil
	registeredToolCount := len(s.tools.Names())
	serviceOperationCount := len(s.registeredOperationNames())
	registeredContractCount := len(toolNames)
	ready := hasSessionJournal &&
		hasSessionStore &&
		hasRuntimeSkillContext &&
		hasRuntimeMemoryContext &&
		hasRuntimePermissionGateway &&
		hasRuntimeHookContext &&
		hasRuntimeCompactContext &&
		hasRuntimeMCPContext &&
		hasRuntimeProviderCacheContext &&
		hasTaskRunAgentRuntimeLoop &&
		registeredContractCount > 0
	status := "missing"
	message := "Go routes the session chat runner through internal/agentruntime with runtime policy, hook, skill, memory, compact, MCP, provider-cache, and TaskRun/Agent context assembly before model calls. Provider resume cache markers now flow into model request metadata and headers, and structured non-text transcript blocks are recovered as stable model-visible labels."
	next := []string{}
	if ready {
		status = "pass"
	} else {
		status = "partial"
		if !hasSessionJournal {
			next = append(next, "Configure durable session journal storage.")
		}
		if !hasSessionStore {
			next = append(next, "Configure session metadata storage.")
		}
		if !hasRuntimeSkillContext {
			next = append(next, "Load SKILL.md catalog before runner execution.")
		}
		if !hasRuntimeMemoryContext {
			next = append(next, "Configure runtime KV for memory, compact, approval, and hook context.")
		}
		if registeredContractCount == 0 {
			next = append(next, "Register runtime tools before model tool-call execution.")
		}
		if !hasRuntimePermissionGateway {
			next = append(next, "Register ask_user and SendMessage with settings/runtime stores so model tool calls use the permission gateway.")
		}
		if !hasRuntimeHookContext {
			next = append(next, "Register Bash, Shell, and PowerShell with settings/runtime stores so runtime hook context is executable.")
		}
		if !hasRuntimeCompactContext {
			next = append(next, "Register canonical Compact with session journal and runtime KV context.")
		}
		if !hasRuntimeMCPContext {
			next = append(next, "Register MCPTool, ListMcpTools, ListMcpResourcesTool, and ReadMcpResourceTool for model-visible MCP context.")
		}
		if !hasRuntimeProviderCacheContext {
			next = append(next, "Configure session metadata and journal storage for provider resume cache context.")
		}
		if !hasTaskRunAgentRuntimeLoop {
			next = append(next, "Register TaskRun, Agent, and Task with durable task/task-run stores.")
		}
	}
	evidence := map[string]any{
		"hasChatRunner":                         true,
		"usesAgentRuntimeEngine":                true,
		"hasSessionJournal":                     hasSessionJournal,
		"hasSessionStore":                       hasSessionStore,
		"registeredToolCount":                   registeredToolCount,
		"serviceOperationCount":                 serviceOperationCount,
		"hasRuntimePermissionGateway":           hasRuntimePermissionGateway,
		"hasRuntimeHookContext":                 hasRuntimeHookContext,
		"hasRuntimeSkillContext":                hasRuntimeSkillContext,
		"hasRuntimeMemoryContext":               hasRuntimeMemoryContext,
		"hasRuntimeCompactContext":              hasRuntimeCompactContext,
		"hasRuntimeMCPContext":                  hasRuntimeMCPContext,
		"hasRuntimeProviderCacheContext":        hasRuntimeProviderCacheContext,
		"hasProviderResumeCacheRequestMetadata": hasRuntimeProviderCacheContext,
		"hasProviderResumeCacheRequestHeaders":  hasRuntimeProviderCacheContext,
		"hasNonTextTranscriptRecovery":          true,
		"hasTaskRunAgentRuntimeLoop":            hasTaskRunAgentRuntimeLoop,
		"enginePackage":                         "internal/agentruntime",
		"modelProtocol":                         "openai-compatible chat.completions",
	}
	return status, message, evidence, next
}

func (s *Server) toolGatewayDoctorArea(toolNames []string) (string, string, map[string]any, []string) {
	registeredTools := len(s.tools.Names())
	serviceOperations := len(s.registeredOperationNames())
	registeredContracts := len(toolNames)
	hasCanonicalSkillDiscovery := hasRegisteredTool(s.tools, toolcontract.SearchSkills) && hasRegisteredTool(s.tools, toolcontract.Skill)
	hasMCPTool := hasRegisteredTool(s.tools, "MCPTool")
	hasBash := hasRegisteredTool(s.tools, "Bash")
	hasShell := hasRegisteredTool(s.tools, "Shell")
	hasPowerShell := hasRegisteredTool(s.tools, "powershell")
	hasEdit := hasRegisteredTool(s.tools, "Edit")
	hasFileReadBeforeWriteGate := hasRegisteredTool(s.tools, "Read") && hasRegisteredTool(s.tools, "Write") && hasRegisteredTool(s.tools, "Edit") && s.readState != nil
	hasNotebookReadBeforeEditGate := hasRegisteredTool(s.tools, "Read") && hasRegisteredTool(s.tools, "NotebookEdit") && s.readState != nil
	hasRuntimeAudit := s.runtimeStore != nil
	hasDirectGatewayApprovalPolicy := hasRegisteredTool(s.tools, toolcontract.AskUser) && hasRegisteredTool(s.tools, "SendMessage") && s.settingsStore != nil && s.runtimeStore != nil
	hasDirectGatewayPreHooks := s.settingsStore != nil && s.runtimeStore != nil && hasBash && hasShell && hasPowerShell
	hasDirectGatewayPostHooks := hasDirectGatewayPreHooks
	hasDeferredApprovalGateway := hasDirectGatewayApprovalPolicy && hasRuntimeAudit
	ready := registeredContracts > 0 && hasCanonicalSkillDiscovery && hasMCPTool && hasBash && hasEdit && hasFileReadBeforeWriteGate && hasNotebookReadBeforeEditGate && hasRuntimeAudit && hasDirectGatewayApprovalPolicy && hasDirectGatewayPreHooks && hasDirectGatewayPostHooks && hasDeferredApprovalGateway
	status := "partial"
	message := "Tools execute through one server dispatcher with per-tool schema validation; agent runtime calls pass through allowlist, approval, hook, and post-audit gates, and direct HTTP tool execution now passes through a direct gateway with high-risk approval policy, PreToolUse/PostToolUse hooks, and runtime execution audit."
	next := []string{}
	if ready {
		status = "pass"
		message = "Tools execute through one server dispatcher with schema validation, allowlist, approval, hook, post-audit, timeout metadata, original Shell routing, millisecond shell timeouts, background shell tasks, original Write/Edit and NotebookEdit read-before-edit stale-file protection, and distinct state classification across agent-runtime, direct HTTP, and deferred-approved executions."
	} else {
		if registeredContracts == 0 {
			next = append(next, "Register runtime tools before marking the tool gateway ready.")
		}
		if !hasRuntimeAudit {
			next = append(next, "Configure FileRoot/runtime KV so gateway audits are durable.")
		}
		if !hasDirectGatewayApprovalPolicy || !hasDeferredApprovalGateway {
			next = append(next, "Register ask_user and SendMessage with settings/runtime stores so direct and deferred approval gateways are active.")
		}
		if !hasDirectGatewayPreHooks || !hasDirectGatewayPostHooks {
			next = append(next, "Register Bash, Shell, and PowerShell with settings/runtime stores so direct gateway PreToolUse/PostToolUse hooks can run.")
		}
	}
	originalGeneratedStubTools := []string{
		"CtxInspectTool",
		"DiscoverSkillsTool",
		"ListPeersTool",
		"MonitorTool",
		"OverflowTestTool",
		"PushNotificationTool",
		"REPLTool",
		"ReviewArtifactTool",
		"SendUserFileTool",
		"SnipTool",
		"SubscribePRTool",
		"SuggestBackgroundPRTool",
		"TerminalCaptureTool",
		"VerifyPlanExecutionTool",
		"WebBrowserTool",
		"WorkflowTool",
	}
	originalUnavailableTools := []string{"TungstenTool"}
	evidence := map[string]any{
		"registeredTools":                registeredTools,
		"serviceOperations":              serviceOperations,
		"hasCanonicalSkillDiscovery":     hasCanonicalSkillDiscovery,
		"hasMCPTool":                     hasMCPTool,
		"hasBash":                        hasBash,
		"hasEdit":                        hasEdit,
		"hasAgentRuntimeGateway":         true,
		"hasDirectHTTPGateway":           true,
		"hasDeferredApprovalGateway":     hasDeferredApprovalGateway,
		"hasDirectGatewayApprovalPolicy": hasDirectGatewayApprovalPolicy,
		"hasDirectGatewayPreHooks":       hasDirectGatewayPreHooks,
		"hasDirectGatewayPostHooks":      hasDirectGatewayPostHooks,
		"hasDirectGatewayRuntimeAudit":   hasRuntimeAudit,
		"hasUnifiedExecutionAudit":       hasRuntimeAudit,
		"hasTimeoutAuditMetadata":        true,
		"hasOriginalShellRouter":         hasRegisteredTool(s.tools, "Shell"),
		"hasShellTimeoutMilliseconds":    hasBash && hasShell && hasPowerShell,
		"hasDirectShellSandboxGate":      hasBash && hasPowerShell,
		"hasOriginalShellSandboxGate":    hasShell,
		"hasBackgroundShellTasks":        hasRegisteredTool(s.tools, "TaskOutput") && hasRegisteredTool(s.tools, "TaskStop") && s.taskStore != nil,
		"hasFileReadBeforeWriteGate":     hasFileReadBeforeWriteGate,
		"hasNotebookReadBeforeEditGate":  hasNotebookReadBeforeEditGate,
		"hasDistinctStateClassification": true,
		"hasOriginalToolSurfaceAudit":    true,
		"hasDynamicMcpAuthPseudoTool":    true,
		"originalRealToolCoverage":       "covered",
		"dynamicOriginalToolMappings":    []string{"McpAuthTool->mcp__<server>__authenticate"},
		"aliasedOriginalToolMappings": []string{
			"FileReadTool->Read",
			"FileWriteTool->Write",
			"FileEditTool->Edit",
			"ListMcpResourcesTool->ListMcpResourcesTool",
			"ReadMcpResourceTool->ReadMcpResourceTool",
			"ScheduleCronTool->CronCreate/CronUpdate/CronList/CronDelete",
			"SyntheticOutputTool->StructuredOutput",
		},
		"excludedGeneratedStubTools":                     originalGeneratedStubTools,
		"originalGeneratedStubTools":                     originalGeneratedStubTools,
		"originalGeneratedStubToolCount":                 len(originalGeneratedStubTools),
		"originalGeneratedStubToolsExcludedFromCoverage": true,
		"disabledUnavailableTools":                       originalUnavailableTools,
		"originalUnavailableTools":                       originalUnavailableTools,
		"stateClasses":                                   []string{"completed", "failed", "blocked", "pending_approval", "denied", "timeout"},
		"auditOrigins":                                   []string{"agent-runtime", "http", "approved-deferred"},
		"auditNamespace":                                 toolGatewayAuditNamespace,
	}
	return status, message, evidence, next
}

func (s *Server) permissionsDoctorArea() (string, string, map[string]any, []string) {
	hasAskUser := hasRegisteredTool(s.tools, toolcontract.AskUser)
	hasApprovalStore := s.settingsStore != nil
	hasPendingApprovalQueue := s.runtimeStore != nil
	hasRememberedApprovalInspection := hasRegisteredTool(s.tools, "approval_remembered_list")
	hasRememberedApprovalRevocation := hasRegisteredTool(s.tools, "approval_remembered_revoke")
	hasSynonLinkPolicies := s.synonLink != nil
	hasApprovalResolution := hasRegisteredTool(s.tools, "SendMessage") && hasPendingApprovalQueue
	hasRememberedApprovals := hasApprovalStore && hasRememberedApprovalInspection && hasRememberedApprovalRevocation
	hasDirectShellSandboxGate := hasRegisteredTool(s.tools, "Bash") && hasRegisteredTool(s.tools, "powershell")
	hasOriginalShellSandboxGate := hasRegisteredTool(s.tools, "Shell")
	hasShellSandboxPolicy := hasDirectShellSandboxGate && hasOriginalShellSandboxGate
	hasMutatingToolGate := hasAskUser && hasApprovalStore && hasPendingApprovalQueue
	hasDirectCallerApproval := hasMutatingToolGate && hasApprovalResolution
	hasShellCommandSubstitutionClassifier := hasShellSandboxPolicy
	ready := hasAskUser &&
		hasApprovalStore &&
		hasPendingApprovalQueue &&
		hasApprovalResolution &&
		hasRememberedApprovals &&
		hasShellSandboxPolicy &&
		hasRememberedApprovalInspection &&
		hasRememberedApprovalRevocation &&
		hasSynonLinkPolicies
	status := "partial"
	message := "Agent runtime and direct HTTP callers gate mutating and external tools through approval defaults, persist ask/confirm requests, resolve pending approvals through SendMessage, support exact-input remembered approvals, and block high-risk shell commands before host execution, including deterministic command-substitution checks for remote scripts hidden inside Bash expansions."
	next := []string{}
	if ready {
		status = "pass"
		message = "Agent runtime and direct HTTP callers gate mutating and external tools through approval defaults, pending approval queues, SendMessage resolution, exact-input remembered approval reuse, remembered approval inspection/revocation, Synon Link policy enforcement, and deterministic shell sandbox blocking."
	} else {
		if !hasApprovalStore {
			next = append(next, "Configure settings storage so approval defaults and remembered decisions are durable.")
		}
		if !hasPendingApprovalQueue {
			next = append(next, "Configure runtime KV so pending approval requests are durable.")
		}
		if !hasRememberedApprovalInspection || !hasRememberedApprovalRevocation {
			next = append(next, "Register remembered approval management tools.")
		}
		if !hasApprovalResolution {
			next = append(next, "Register SendMessage so pending approval requests can be resolved.")
		}
		if !hasShellSandboxPolicy {
			next = append(next, "Register Bash, Shell, and PowerShell so shell sandbox policy covers executable shell tools.")
		}
	}
	evidence := map[string]any{
		"hasAskUserQuestion":                    hasAskUser,
		"hasApprovalStore":                      hasApprovalStore,
		"synonLinkPolicies":                     hasSynonLinkPolicies,
		"hasMutatingToolGate":                   hasMutatingToolGate,
		"hasDirectCallerApproval":               hasDirectCallerApproval,
		"hasPendingApprovalQueue":               hasPendingApprovalQueue,
		"hasApprovalResolution":                 hasApprovalResolution,
		"hasRememberedApprovals":                hasRememberedApprovals,
		"hasRememberedApprovalInspection":       hasRememberedApprovalInspection,
		"hasRememberedApprovalRevocation":       hasRememberedApprovalRevocation,
		"hasShellSandboxPolicy":                 hasShellSandboxPolicy,
		"hasDirectShellSandboxGate":             hasDirectShellSandboxGate,
		"hasOriginalShellSandboxGate":           hasOriginalShellSandboxGate,
		"hasShellCommandSubstitutionClassifier": hasShellCommandSubstitutionClassifier,
		"shellSandboxClassifierRules":           []string{"privilege-escalation", "destructive-removal", "system-permission-mutation", "global-vcs-configuration", "device-or-mount-mutation", "remote-script-pipe", "remote-script-substitution"},
		"rememberedApprovalCount":               s.rememberedApprovalDecisionCount(),
		"rememberedApprovalTools":               []string{"approval_remembered_list", "approval_remembered_revoke"},
		"pendingApprovalNamespace":              agentRuntimeApprovalNamespace,
	}
	return status, message, evidence, next
}
