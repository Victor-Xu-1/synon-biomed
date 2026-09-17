package server

import (
	"strings"
	"synon-go/internal/toolcontract"
	"time"
)

func (s *Server) hooksDoctorArea() (string, string, map[string]any, []string) {
	hasRuntimeKVHookSource := s.runtimeStore != nil
	hasSettingsHookSource := s.settingsStore != nil
	hasCommandHookRunner := hasRegisteredTool(s.tools, "Bash") && hasRegisteredTool(s.tools, "Shell") && hasRegisteredTool(s.tools, "powershell")
	hasHTTPHookRunner := s != nil && s.httpClient != nil
	hasModelHookEndpoint := strings.TrimSpace(s.compactSummarizer.Endpoint) != ""
	hasModelHookModel := strings.TrimSpace(s.compactSummarizer.Model) != ""
	hasPromptHookRunner := hasHTTPHookRunner && hasModelHookEndpoint && hasModelHookModel
	agentHookReadOnlyToolCount := 0
	for _, name := range readOnlyAgentAllowedTools(nil) {
		if hasRegisteredTool(s.tools, name) {
			agentHookReadOnlyToolCount++
		}
	}
	hasAgentHookRunner := hasPromptHookRunner && agentHookReadOnlyToolCount > 0
	hasCompactHooks := hasRegisteredTool(s.tools, "Compact") && hasRuntimeKVHookSource && hasSettingsHookSource
	hasPostToolUseAudit := hasRuntimeKVHookSource
	hasAsyncPostToolHooks := hasRuntimeKVHookSource
	hasAsyncHookRewake := hasRuntimeKVHookSource
	hasPluginHookEnvInjection := hasCommandHookRunner
	hasSessionStartHooks := hasRuntimeKVHookSource && hasSettingsHookSource
	hasUserPromptSubmit := hasSessionStartHooks
	hasStopHooks := hasSessionStartHooks
	hasPreToolUse := hasRuntimeKVHookSource && hasSettingsHookSource && (hasCommandHookRunner || hasHTTPHookRunner || hasPromptHookRunner || hasAgentHookRunner)
	hasPostToolUseFailure := hasPostToolUseAudit
	hasOriginalHookShape := hasRuntimeKVHookSource || hasSettingsHookSource
	hasPluginHookSource := s != nil && s.plugins != nil && hasSettingsHookSource
	hasManagedHookTrust := hasSettingsHookSource
	ready := hasRuntimeKVHookSource &&
		hasSettingsHookSource &&
		hasCommandHookRunner &&
		hasHTTPHookRunner &&
		hasPromptHookRunner &&
		hasAgentHookRunner &&
		hasCompactHooks
	status := "partial"
	message := "Agent runtime supports PreToolUse block/deny/update hooks, bounded command, HTTP, prompt, and read-only agent hook execution with structured output parsing, original settings-style hook shape loading, plugin hook sources with managed-only trust control, plugin command-hook environment injection, synchronous and async PostToolUse/PostToolUseFailure audit hooks, runner-level SessionStart/UserPromptSubmit/Stop command hooks, and PreCompact/PostCompact command hooks."
	next := []string{}
	if ready {
		status = "pass"
	} else {
		if !hasRuntimeKVHookSource {
			next = append(next, "Configure runtime KV so hook audit and async rewake state are durable.")
		}
		if !hasSettingsHookSource {
			next = append(next, "Configure settings storage so original settings-style hooks can load.")
		}
		if !hasCommandHookRunner {
			next = append(next, "Register Bash, Shell, and PowerShell so command hooks can execute through the runtime shell runner.")
		}
		if !hasCompactHooks {
			next = append(next, "Register canonical Compact with settings/runtime storage so PreCompact and PostCompact hooks can run.")
		}
		if !hasHTTPHookRunner {
			next = append(next, "Configure an HTTP client so HTTP hooks can execute.")
		}
		if !hasPromptHookRunner || !hasAgentHookRunner {
			next = append(next, "Configure compact summarizer endpoint and model so prompt and read-only agent hooks can execute.")
		}
		if agentHookReadOnlyToolCount == 0 {
			next = append(next, "Register at least one read-only agent hook tool schema.")
		}
	}
	evidence := map[string]any{
		"hasPreToolUse":              hasPreToolUse,
		"hasPostToolUseAudit":        hasPostToolUseAudit,
		"hasPostToolUseFailure":      hasPostToolUseFailure,
		"hasAsyncPostToolHooks":      hasAsyncPostToolHooks,
		"hasAsyncHookRewake":         hasAsyncHookRewake,
		"hookNamespace":              agentRuntimeHookNamespace,
		"hookAuditNamespace":         agentRuntimeHookAuditNamespace,
		"hookRewakeNamespace":        agentRuntimeHookRewakeNamespace,
		"hasCommandHookRunner":       hasCommandHookRunner,
		"hasHTTPHookRunner":          hasHTTPHookRunner,
		"hasPromptHookRunner":        hasPromptHookRunner,
		"hasAgentHookRunner":         hasAgentHookRunner,
		"hasModelHookEndpoint":       hasModelHookEndpoint,
		"hasModelHookModel":          hasModelHookModel,
		"agentHookReadOnlyToolCount": agentHookReadOnlyToolCount,
		"hasOriginalHookShape":       hasOriginalHookShape,
		"hasSettingsHookSource":      hasSettingsHookSource,
		"hasRuntimeKVHookSource":     hasRuntimeKVHookSource,
		"hasPluginHookSource":        hasPluginHookSource,
		"hasPluginHookEnvInjection":  hasPluginHookEnvInjection,
		"hasManagedHookTrust":        hasManagedHookTrust,
		"hasSessionStartHooks":       hasSessionStartHooks,
		"hasCompactHooks":            hasCompactHooks,
		"hasUserPromptSubmit":        hasUserPromptSubmit,
		"hasStopHooks":               hasStopHooks,
		"hookPhases":                 []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "Stop", "PreCompact", "PostCompact"},
		"hookRunners":                []string{"command", "http", "prompt", "agent"},
	}
	return status, message, evidence, next
}

func (s *Server) synonLinkIMDoctorArea() (string, string, map[string]any, []string) {
	hasSynonLinkService := s != nil && s.synonLink != nil
	hasSynonLinkTools := hasRegisteredTool(s.tools, "synon_link")
	hasPairingStore := s != nil && s.pairingStore != nil
	hasPairingTools := hasRegisteredTool(s.tools, "pairing_list") && hasRegisteredTool(s.tools, "pairing_allow") && hasRegisteredTool(s.tools, "pairing_revoke")
	hasIMMessageTool := hasRegisteredTool(s.tools, "im_message")
	hasIMConfigTool := hasRegisteredTool(s.tools, "im_config")
	hasSessionEventJournalTool := hasRegisteredTool(s.tools, "session_event_journal")
	hasIMLiveSessionJournal := s != nil && s.sessionStore != nil && s.eventJournal != nil
	hasAllPlatformDedupManagers := s != nil && s.feishuDedup != nil && s.wechatDedup != nil
	adapterLiveSmokePlatforms := s.adapterDoctorPlatforms(true)
	adapterLiveSmokeAudit := s.latestAdapterLiveSmokeAudits(adapterLiveSmokePlatforms)
	hasAnyAdapterCredentialSmokePass := adapterCredentialSmokeHasPass(adapterLiveSmokePlatforms)
	hasAnyAdapterLiveSmokePass := adapterLiveSmokeHasPass(adapterLiveSmokePlatforms)
	hasAllAdapterLiveSmokePass := adapterLiveSmokeAllRequiredPass(adapterLiveSmokePlatforms, []string{"feishu", "wechat"})
	ready := hasSynonLinkService && hasSynonLinkTools && hasPairingStore && hasPairingTools && hasIMMessageTool && hasIMConfigTool && hasSessionEventJournalTool && hasIMLiveSessionJournal && hasAllPlatformDedupManagers && hasAllAdapterLiveSmokePass
	status := "partial"
	message := "Synon Link and Feishu/WeChat channel APIs are implemented in the Go runtime with bridge tools, durable pairing, normalized inbound IM messages, per-channel deduplication, live-session journaling, credential bootstrap diagnostics, and fresh outbound-delivery smoke audits."
	next := []string{}
	if ready {
		status = "pass"
	} else {
		if !hasSynonLinkService || !hasSynonLinkTools {
			next = append(next, "Enable Synon Link service and register the canonical synon_link tool.")
		}
		if !hasPairingStore || !hasPairingTools {
			next = append(next, "Configure durable IM pairing store and pairing management tools.")
		}
		if !hasIMMessageTool {
			next = append(next, "Register executable im_message tool for normalized inbound messages.")
		}
		if !hasIMConfigTool {
			next = append(next, "Register im_config so message channel setup can be audited without exposing secrets.")
		}
		if !hasSessionEventJournalTool || !hasIMLiveSessionJournal {
			next = append(next, "Configure session event journal so IM live sessions can be replayed after reconnect.")
		}
		if !hasAllPlatformDedupManagers {
			next = append(next, "Initialize Feishu and WeChat message dedup managers.")
		}
		if !hasAllAdapterLiveSmokePass {
			next = append(next, "Run ToolDoctor adapters smoke with real platform credentials before declaring live IM channels ready.")
			next = append(next, "Run synon-go-live-im-smoke --require-all --json --runtime-url <runtime-url> after exporting the Feishu and WeChat live smoke credentials.")
		}
	}
	evidence := map[string]any{
		"hasSynonLinkService":                     hasSynonLinkService,
		"hasSynonLinkTools":                       hasSynonLinkTools,
		"hasPairingStore":                         hasPairingStore,
		"hasPairingTools":                         hasPairingTools,
		"hasIMMessageTool":                        hasIMMessageTool,
		"hasIMConfigTool":                         hasIMConfigTool,
		"hasSessionEventJournalTool":              hasSessionEventJournalTool,
		"hasIMLiveSessionJournal":                 hasIMLiveSessionJournal,
		"hasToolDoctorAdapterCredentialSmoke":     true,
		"hasFeishuAdapterLiveSmokeExecution":      true,
		"hasWeChatAdapterLiveSmokeExecution":      true,
		"hasAdapterLiveSmokePerPlatformReadiness": len(adapterLiveSmokePlatforms) > 0,
		"hasLiveIMSmokeScript":                    true,
		"hasLiveIMSmokeReleaseBinary":             true,
		"liveSmokePlanCommand":                    "synon-go-live-im-smoke --plan --json",
		"liveSmokeRequireAllCommand":              "synon-go-live-im-smoke --require-all --json",
		"liveSmokeRecordCommand":                  "synon-go-live-im-smoke --require-all --json --runtime-url <runtime-url>",
		"requiresDeliveryChecked":                 true,
		"deliveryAuditMaxAgeSeconds":              int64(adapterLiveSmokeAuditMaxAge / time.Second),
		"hasAnyAdapterLiveSmokePass":              hasAnyAdapterLiveSmokePass,
		"hasAnyAdapterCredentialSmokePass":        hasAnyAdapterCredentialSmokePass,
		"hasAllAdapterLiveSmokePass":              hasAllAdapterLiveSmokePass,
		"adapterLiveSmokePlatforms":               adapterLiveSmokePlatforms,
		"adapterLiveSmokeAudit":                   adapterLiveSmokeAudit,
		"adapterLiveSmokeAuditNamespace":          adapterLiveSmokeRuntimeNamespace,
		"hasAllPlatformDedupManagers":             hasAllPlatformDedupManagers,
		"platforms":                               []string{"feishu", "wechat"},
		"adapterEndpoints":                        []string{"/api/adapters/feishu/event", "/api/adapters/wechat/event", "/api/adapters/wechat/qr/start", "/api/adapters/wechat/qr/poll", "/api/adapters/feishu/qr/start", "/api/adapters/feishu/qr/poll"},
		"liveCredentialSmokeRequiredAtDeploy":     true,
	}
	return status, message, evidence, next
}

func (s *Server) mcpDoctorArea() (string, string, map[string]any, []string) {
	hasMCPTool := hasRegisteredTool(s.tools, "MCPTool")
	hasListMcpTools := hasRegisteredTool(s.tools, "ListMcpTools")
	hasListMcpResourcesTool := hasRegisteredTool(s.tools, "ListMcpResourcesTool")
	hasReadMcpResourceTool := hasRegisteredTool(s.tools, "ReadMcpResourceTool")
	hasSkillSearch := hasRegisteredTool(s.tools, toolcontract.SearchSkills)
	hasSkillTool := hasRegisteredTool(s.tools, toolcontract.Skill)
	hasSkillCatalog := s != nil && s.skillCatalog != nil && len(s.skillCatalog.Skills()) > 0
	hasRuntimeStore := s != nil && s.runtimeStore != nil
	hasDynamicMCPDispatch := hasMCPTool
	hasScopedHostMCPBridge := hasMCPTool && hasSkillSearch && hasSkillTool
	hasDynamicMCPPromptContext := hasSkillSearch && hasSkillTool && hasSkillCatalog
	hasSkillDrivenMCPSelection := hasDynamicMCPPromptContext
	hasMCPAgentRuntimeGateway := hasMCPTool
	hasMCPAgentRuntimeApprovalGate := hasMCPTool && s != nil && s.settingsStore != nil
	hasMCPServerPermissionPolicy := hasMCPAgentRuntimeApprovalGate
	hasMCPAuthTool := hasMCPTool
	hasMCPOAuthPKCE := hasMCPAuthTool
	hasMCPOAuthTokenCache := hasMCPAuthTool && hasRuntimeStore
	hasBoundedMCPOAuthTokenCache := hasMCPOAuthTokenCache
	hasPrivateMCPOAuthTokenCache := hasMCPOAuthTokenCache
	ready := hasMCPTool &&
		hasListMcpTools &&
		hasListMcpResourcesTool &&
		hasReadMcpResourceTool &&
		hasDynamicMCPDispatch &&
		hasScopedHostMCPBridge &&
		hasDynamicMCPPromptContext &&
		hasSkillDrivenMCPSelection &&
		hasMCPAgentRuntimeGateway &&
		hasMCPAgentRuntimeApprovalGate &&
		hasMCPServerPermissionPolicy &&
		hasMCPAuthTool &&
		hasMCPOAuthPKCE &&
		hasMCPOAuthTokenCache &&
		hasBoundedMCPOAuthTokenCache &&
		hasPrivateMCPOAuthTokenCache
	status := "partial"
	message := "MCP list/call/resource tools, the scoped host.mcp bridge, OAuth authentication, Skill-guided catalog context, and agent-runtime gateway controls are required before MCP readiness can pass."
	next := []string{}
	if ready {
		status = "pass"
		message = "MCP list/call/resource tools are present, connected methods are cataloged and invoked through the scoped host.mcp bridge inside repl, OAuth flows cache Bearer tokens in bounded private files, selected Skills annotate MCP context, and calls use the same allowlist, server policy, approval, PreToolUse, and PostToolUse gateway as local tools."
	} else {
		if !hasMCPTool || !hasListMcpTools || !hasListMcpResourcesTool || !hasReadMcpResourceTool {
			next = append(next, "Register the canonical MCPTool, ListMcpTools, ListMcpResourcesTool, and ReadMcpResourceTool service authorities.")
		}
		if !hasSkillSearch || !hasSkillTool || !hasSkillCatalog || !hasScopedHostMCPBridge {
			next = append(next, "Load search_skills, skill, the packaged catalog, and the scoped repl host.mcp bridge.")
		}
		if !hasRuntimeStore {
			next = append(next, "Configure runtime KV so MCP OAuth bearer tokens can be cached durably.")
		}
		if !hasMCPAgentRuntimeApprovalGate {
			next = append(next, "Configure settings storage so MCP calls use the same approval and server permission policy as local tools.")
		}
	}
	evidence := map[string]any{
		"hasMCPTool":                     hasMCPTool,
		"hasListMcpTools":                hasListMcpTools,
		"hasListMcpResourcesTool":        hasListMcpResourcesTool,
		"hasReadMcpResourceTool":         hasReadMcpResourceTool,
		"hasDynamicMCPDispatch":          hasDynamicMCPDispatch,
		"hasScopedHostMCPBridge":         hasScopedHostMCPBridge,
		"hasDynamicMCPPromptContext":     hasDynamicMCPPromptContext,
		"hasSkillDrivenMCPSelection":     hasSkillDrivenMCPSelection,
		"hasMCPAgentRuntimeGateway":      hasMCPAgentRuntimeGateway,
		"hasMCPAgentRuntimeApprovalGate": hasMCPAgentRuntimeApprovalGate,
		"hasMCPServerPermissionPolicy":   hasMCPServerPermissionPolicy,
		"hasMCPAuthTool":                 hasMCPAuthTool,
		"hasMCPOAuthPKCE":                hasMCPOAuthPKCE,
		"hasMCPOAuthTokenCache":          hasMCPOAuthTokenCache,
		"hasBoundedMCPOAuthTokenCache":   hasBoundedMCPOAuthTokenCache,
		"hasPrivateMCPOAuthTokenCache":   hasPrivateMCPOAuthTokenCache,
	}
	return status, message, evidence, next
}

func (s *Server) compactMemoryDoctorArea() (string, string, map[string]any, []string) {
	hasRuntimeStore := s.runtimeStore != nil
	hasCompactJournalEvent := s.eventJournal != nil && hasRegisteredTool(s.tools, "Compact")
	hasRunnerCompactResumeInject := hasCompactJournalEvent
	hasWorkspaceMemoryExtraction := s.memoryExtraction != nil && s.workspaceStore != nil
	hasUnifiedMemoryPolicy := hasWorkspaceMemoryExtraction
	compactEndpointConfigured := strings.TrimSpace(s.compactSummarizer.Endpoint) != ""
	compactModelConfigured := strings.TrimSpace(s.compactSummarizer.Model) != ""
	externalCompactSummarizerReady := compactEndpointConfigured && compactModelConfigured
	deterministicCompactFallbackReady := hasRuntimeStore
	compactSummarizerReady := externalCompactSummarizerReady
	status := "partial"
	message := "Go has durable compact checkpoints, model-context resume injection, context-pressure auto-compact, and one Synon workspace-memory extraction and recall authority."
	next := []string{}
	if hasRuntimeStore && compactSummarizerReady {
		status = "pass"
		message = "Compact and memory runtime is ready with durable compact checkpoints, runner resume injection, configured model-generated compact summaries, and unified workspace-memory extraction and recall."
	} else {
		if !hasRuntimeStore {
			next = append(next, "Configure runtime KV so compact handoff state is durable.")
		}
		if !compactEndpointConfigured {
			next = append(next, "Set compact summarizer endpoint to enable model-generated compact summaries.")
		}
		if !compactModelConfigured {
			next = append(next, "Set compact summarizer model to enable model-generated compact summaries.")
		}
	}
	evidence := map[string]any{
		"runtimeStore":                         hasRuntimeStore,
		"hasCompactJournalEvent":               hasCompactJournalEvent,
		"hasRunnerCompactResumeInject":         hasRunnerCompactResumeInject,
		"supportsModelGeneratedCompactSummary": true,
		"hasModelGeneratedCompactSummary":      externalCompactSummarizerReady,
		"hasDeterministicCompactFallback":      deterministicCompactFallbackReady,
		"externalCompactSummarizerReady":       externalCompactSummarizerReady,
		"compactSummarizerReady":               compactSummarizerReady,
		"compactEndpointConfigured":            compactEndpointConfigured,
		"compactModelConfigured":               compactModelConfigured,
		"hasCompactSetupHints":                 true,
		"hasAutoCompactContextPressure":        true,
		"hasWorkspaceMemoryExtraction":         hasWorkspaceMemoryExtraction,
		"hasUnifiedMemoryPolicy":               hasUnifiedMemoryPolicy,
		"setup": []string{
			"Configure CompactSummarizer.Endpoint with an OpenAI-compatible /v1/chat/completions endpoint.",
			"Configure CompactSummarizer.Model with the summarizer model name.",
			"Set CompactSummarizer.APIKey when the endpoint requires authentication.",
		},
	}
	return status, message, evidence, next
}
