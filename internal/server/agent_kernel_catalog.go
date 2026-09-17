package server

import (
	"context"

	"strings"

	"synon-go/internal/agentruntime"

	"synon-go/internal/toolcontract"
)

func (s *Server) agentKernelToolSchemas(identity *agentKernelContext, allowed map[string]struct{}) []agentruntime.ToolSchema {
	if identity == nil {
		return nil
	}
	schemas := []agentruntime.ToolSchema{}
	if s.kernelManager != nil && s.kernelManager.ManagedEnvironmentSupervisorReady() {
		for _, schema := range agentEnvironmentManagementToolSchemas() {
			if agentKernelToolAllowed(schema.Name, allowed) {
				schemas = append(schemas, schema)
			}
		}
		if agentKernelToolAllowed("bash", allowed) {
			schemas = append(schemas, agentKernelBashToolSchema())
		}
	}
	for _, schema := range agentComputeToolSchemas() {
		if schema.Name == computeProviderToolName && !s.agentComputeProviderToolReady(identity) {
			continue
		}
		if agentKernelToolAllowed(schema.Name, allowed) {
			schemas = append(schemas, schema)
		}
	}
	for _, schema := range agentPermissionToolSchemas() {
		if agentKernelToolAllowed(schema.Name, allowed) {
			schemas = append(schemas, schema)
		}
	}
	if s.kernelManager != nil && s.kernelManager.ManagedPythonCapabilityAvailable() {
		schemas = append(schemas, agentruntime.ToolSchema{
			Name: "python", Description: agentKernelPythonDescription,
			Parameters: agentKernelLanguageToolParameters("Python", agentKernelManagedPythonEnvironment),
		})
	}
	if s.kernelManager != nil && s.kernelManager.RuntimeReady("r", "r") {
		schemas = append(schemas, agentruntime.ToolSchema{
			Name:        "r",
			Description: agentKernelRDescription,
			Parameters:  agentKernelLanguageToolParameters("R", "r"),
		})
	}
	if s.kernelManager != nil && s.kernelManager.RuntimeReady("python", "repl") {
		schemas = append(schemas, agentKernelReplToolSchema())
	}
	for _, schema := range []agentruntime.ToolSchema{agentWorkspaceReadFileToolSchema(), agentWorkspaceEditFileToolSchema()} {
		if agentKernelToolAllowed(schema.Name, allowed) {
			schemas = append(schemas, schema)
		}
	}
	if agentKernelToolAllowed("save_artifacts", allowed) {
		schemas = append(schemas, agentSaveArtifactsToolSchema())
	}
	if s.publicScientificFiles != nil && agentKernelToolAllowed("download_public_scientific_file", allowed) {
		schemas = append(schemas, agentPublicScientificFileToolSchema())
	}
	if s.rcsbFiles != nil && agentKernelToolAllowed("download_rcsb_file", allowed) {
		schemas = append(schemas, agentRCSBFileToolSchema())
	}
	if s.rcsbSearch != nil && agentKernelToolAllowed("search_rcsb_structures", allowed) {
		schemas = append(schemas, agentRCSBSearchToolSchema())
	}
	result := schemas[:0]
	for _, schema := range schemas {
		if agentKernelToolAllowed(schema.Name, allowed) {
			result = append(result, schema)
		}
	}
	result = append(result, agentruntime.ToolSchema{
		Name:        "wait_for_notification",
		Description: "Wait for queued background-work notifications, or return pending work when the timeout expires.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"human_description": map[string]any{
					"type": "string", "minLength": 1, "maxLength": 256,
					"description": "Short present-participle label naming the background result being awaited.",
				},
				"timeout_seconds": map[string]any{
					"type":        "number",
					"description": "Maximum seconds to block when nothing is queued yet, capped at 1800 seconds. Compute jobs can run for hours; the daemon's poller checks every ~15s, so a 600-1800s timeout is reasonable for jobs you expect to finish. Use ~30s only when you want to peek and do something else on timeout.",
					"default":     30,
					"maximum":     1800,
				},
			},
		},
	})
	for index := range result {
		result[index] = annotateOwnedAgentRuntimeToolSchema(result[index])
	}
	return result
}

func agentKernelReplToolSchema() agentruntime.ToolSchema {
	return agentruntime.ToolSchema{Name: "repl", Description: agentKernelReplDescription, Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"human_description": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 256, "description": "Short present-participle action label for this cell.",
			},
			"code": map[string]any{
				"type": "string", "description": "One complete Python control-plane step. The pre-injected host module is available only in repl.",
			},
			"working_dir": map[string]any{
				"type": "string", "description": "Optional task-workspace-relative directory (preferred) or absolute authorized host-grant directory. Do not also call os.chdir() for the same path. An explicit cwd change persists in the control kernel, and omission keeps the current cwd.",
			},
			"background": map[string]any{
				"type": "boolean", "default": false, "description": "Run asynchronously and deliver the durable result as a notification.",
			},
			"fresh": map[string]any{
				"type": "boolean", "description": "Use a disposable isolated repl instead of the persistent control kernel.",
			},
		},
		"required": []string{"code", "human_description"},
	}}
}

func agentKernelBashToolSchema() agentruntime.ToolSchema {
	return agentruntime.ToolSchema{Name: "bash", Description: agentKernelBashDescription, Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"command": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 262144, "description": "One complete Bash step. Package-manager mutation is rejected; use the managed environment tools.",
			},
			"environment": map[string]any{
				"type": "string", "description": "Verified managed environment name returned by manage_environments or manage_packages.",
			},
			"working_dir": map[string]any{
				"type": "string", "description": "Optional task-workspace-relative directory. Omit to use the task workspace.",
			},
			"background": map[string]any{
				"type": "boolean", "default": false, "description": "Run asynchronously and deliver the durable result as a notification.",
			},
			"human_description": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 256, "description": "Short present-participle action label for this command.",
			},
		},
		"required": []string{"command", "environment", "human_description"},
	}}
}

func agentKernelLanguageToolParameters(language, defaultEnvironment string) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"human_description": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 256, "description": "Short present-participle action label for this cell.",
			},
			"code": map[string]any{
				"type": "string", "description": "One complete " + language + " analysis step, including the checks needed before the next model action.",
			},
			"environment": map[string]any{
				"type": "string", "default": defaultEnvironment,
				"description": "Verified managed environment name. Use the advertised default when it satisfies the task; use manage_environments and manage_packages for additional software.",
			},
			"working_dir": map[string]any{
				"type": "string", "description": "Optional task-workspace-relative directory (preferred) or absolute authorized host-grant directory. Do not also call os.chdir() for the same path. Omit this field to use the task workspace safely; paths outside authorized roots are rejected before execution.",
			},
			"background": map[string]any{
				"type": "boolean", "default": false, "description": "Run asynchronously and deliver the durable result as a notification.",
			},
		},
		"required": []string{"code", "environment", "human_description"},
	}
}

func agentKernelToolAllowed(name string, allowed map[string]struct{}) bool {
	return chatRunnerToolAllowed(name, allowed)
}

func (g serverAgentRuntimeToolGateway) executeToolResponse(ctx context.Context, name string, input map[string]any) (any, error) {
	if isAgentKernelToolName(name) && g.kernel != nil {
		return g.server.executeAgentKernelToolWithLimit(ctx, g.kernel, name, input, g.outputLimitBytes, g.effectiveAllowedTools())
	}
	if response, handled, err := g.server.executeWorkspaceMemoryTool(ctx, g.sessionID, name, input); handled {
		return response, err
	}
	if name == "ListMcpTools" {
		if response, handled, err := g.server.executeWorkspaceListMCPTools(ctx, g.sessionID, input); handled {
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "result": response}, nil
		}
	}
	if response, handled, err := g.server.executeWorkspaceMCPTool(ctx, g.sessionID, name, input); handled {
		return response, err
	}
	return g.server.executeToolResponse(ctx, name, input)
}

func isAgentKernelToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "python", "r", "bash", "repl":
		return true
	default:
		return false
	}
}

func (g serverAgentRuntimeToolGateway) executeAgentToolResponse(ctx context.Context, call agentruntime.ToolCall, name string, input map[string]any) (any, error) {
	if g.readReuseEnabled(name) {
		if response, ok := g.taskRun.lookupReadReuse(name, input); ok {
			return response, nil
		}
	}
	response, err := g.executeAgentToolResponseUncached(ctx, call, name, input)
	if err == nil && g.readReuseEnabled(name) {
		g.taskRun.storeReadReuse(name, input, response)
	}
	return response, err
}

func (g serverAgentRuntimeToolGateway) executeAgentToolResponseUncached(ctx context.Context, call agentruntime.ToolCall, name string, input map[string]any) (any, error) {
	canonical, err := canonicalRuntimeToolName(name)
	if err != nil {
		return nil, err
	}
	name = canonical
	call.Name = canonical
	if name == kernelDelegateSubmitToolName && strings.TrimSpace(g.sessionID) != "" {
		return g.server.executeKernelDelegateSubmitOutput(g.sessionID, input)
	}
	if isAgentWorkspaceFileTool(name) {
		if name == "read_file" {
			ctx = withAgentWorkspaceReadBudget(ctx, g.fileReadLimitBytes)
		}
		return g.server.executeAgentWorkspaceFileTool(ctx, g.kernel, name, input)
	}
	if name == "save_artifacts" {
		return g.server.executeAgentSaveArtifacts(ctx, g.kernel, call.ID, input)
	}
	if name == "download_public_scientific_file" {
		return g.server.executeAgentPublicScientificFileDownload(ctx, g.kernel, call.ID, input)
	}
	if name == "download_rcsb_file" {
		return g.server.executeAgentRCSBFileDownload(ctx, g.kernel, call.ID, input)
	}
	if name == "search_rcsb_structures" {
		return g.server.executeAgentRCSBSearch(ctx, g.kernel, input)
	}
	if name == onboardingReadAttachmentToolName {
		return g.server.executeStructuredOnboardingAttachment(ctx, input, g.outputLimitBytes)
	}
	if name == generatePlanToolName && strings.TrimSpace(g.sessionID) != "" {
		return g.server.executeAgentGeneratePlan(ctx, g.sessionID, call.ID, input)
	}
	if name == updateStepStatusToolName && strings.TrimSpace(g.sessionID) != "" {
		return g.server.executeAgentUpdateStepStatus(ctx, g.sessionID, call.ID, input)
	}
	if name == toolcontract.AskUser && strings.TrimSpace(g.sessionID) != "" {
		return g.server.executeAgentAskUserQuestion(ctx, g.sessionID, call.ID, name, input)
	}
	if isAgentEnvironmentManagementTool(name) {
		return g.server.executeAgentEnvironmentManagementTool(ctx, g.kernel, call, name, input)
	}
	if isAgentComputeTool(name) {
		return g.server.executeAgentComputeTool(ctx, g.kernel, call, name, input)
	}
	if isAgentPermissionTool(name) {
		return g.server.executeAgentPermissionTool(ctx, g.kernel, call, name, input)
	}
	if name == toolcontract.SearchSkills {
		authority := g.skillDiscoveryToolAuthority(ctx)
		g.server.addComputeProviderSkillAuthorities(g.kernel, authority)
		connectors := runtimeMCPConnectorSkills(g.toolSchemas)
		policy := g.skillPolicy
		policy.AllowedNames = runtimeSkillAllowedNamesWithConnectorDependencies(policy.AllowedNames, connectors)
		result, err := g.server.executeSkillSearchToolWithRuntimeSkillsAndPolicy(
			input, connectors, policy, authority,
		)
		if err != nil {
			return nil, err
		}
		return agentRuntimeSkillSearchModelResult(result, input, g.taskRun), nil
	}
	if name == toolcontract.Skill {
		authority := g.skillDiscoveryToolAuthority(ctx)
		g.server.addComputeProviderSkillAuthorities(g.kernel, authority)
		connectors := runtimeMCPConnectorSkills(g.toolSchemas)
		policy := g.skillPolicy
		policy.AllowedNames = runtimeSkillAllowedNamesWithConnectorDependencies(policy.AllowedNames, connectors)
		result, err := g.server.executeSkillToolWithRuntimeSkillsAndPolicy(
			ctx, input, connectors, policy, authority,
		)
		if err != nil {
			return nil, err
		}
		return agentRuntimeSkillModelResult(result), nil
	}
	if isAgentKernelToolName(name) && g.kernel != nil {
		return g.server.executeAgentKernelToolWithApproval(
			ctx, g.kernel, call, name, input, g.outputLimitBytes, g.effectiveAllowedTools(),
		)
	}
	if response, handled, err := g.server.executeWorkspaceMCPTool(ctx, g.sessionID, name, input, call.ID); handled {
		return response, err
	}
	return g.executeToolResponse(ctx, name, input)
}

func (g serverAgentRuntimeToolGateway) skillDiscoveryToolAuthority(ctx context.Context) map[string]struct{} {
	if g.hasToolSnapshot {
		return agentRuntimeToolSchemaNameSet(g.toolSchemas)
	}
	resolved := agentRuntimeToolSchemaNameSet(
		g.server.agentRuntimeToolSchemasWithContext(ctx, g.allowedTools, g.sessionID),
	)
	exact := make(map[string]struct{}, len(g.allowedTools))
	for _, name := range g.allowedTools {
		trimmed := strings.TrimSpace(name)
		canonical, ok := toolcontract.NormalizeRuntimeName(trimmed)
		if !ok || canonical != trimmed {
			continue
		}
		if _, present := resolved[canonical]; present {
			exact[canonical] = struct{}{}
		}
	}
	return exact
}
