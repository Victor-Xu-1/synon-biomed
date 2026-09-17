package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"synon-go/internal/agentruntime"
	"synon-go/internal/mcpdirectory"
	"synon-go/internal/providers"
	"synon-go/internal/skills"
	"synon-go/internal/tools/mcpstdio"
	"time"
)

func (s *Server) newAgentRuntimeEngineWithContext(
	ctx context.Context,
	options SessionRunnerChatOptions,
	runtimeToolSchemas ...[]agentruntime.ToolSchema,
) agentruntime.Engine {
	var model agentruntime.ModelClient
	if options.ModelProfile != nil {
		runtimeClient, err := providers.NewRuntimeModelClient(*options.ModelProfile, s.httpClient, options.ModelAudit)
		if err != nil {
			model = serverErrorModelClient{err: err}
		} else {
			model = runtimeClient
		}
	}
	if model == nil {
		profile := providers.ModelProfile{
			Provider: providers.ProviderProfile{
				ID:       "static-runner-config",
				Name:     "Static runner configuration",
				Type:     "openai-compatible",
				Protocol: providers.ProtocolOpenAICompatible,
				BaseURL:  options.Endpoint,
				Endpoint: options.Endpoint,
			},
			Model:  options.Model,
			APIKey: options.APIKey,
			Request: providers.RequestProfile{
				Timeout: options.RequestTimeout, MaxAttempts: options.MaxAttempts,
				MaxResponseBytes: int64(options.ModelResponseLimitBytes),
			},
		}
		staticModel, err := providers.NewRuntimeModelClient(profile, s.httpClient, options.ModelAudit)
		if err != nil {
			model = serverErrorModelClient{err: err}
		} else {
			streaming, ok := staticModel.(agentruntime.StreamingModelClient)
			if !ok {
				model = serverErrorModelClient{err: errors.New("static runner model client does not support streaming")}
			} else {
				model = sessionRunnerStaticStreamingCompatibilityClient{delegate: streaming, timeout: options.RequestTimeout}
			}
		}
	}
	if options.Endpoint == BuiltinSessionRunnerChatEndpoint && options.ModelProfile == nil {
		model = serverBuiltinModelClient{}
	}
	toolSchemaSnapshot := []agentruntime.ToolSchema(nil)
	if len(runtimeToolSchemas) > 0 {
		toolSchemaSnapshot = append(toolSchemaSnapshot, runtimeToolSchemas[0]...)
	}
	taskRun, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if err := s.hydrateSessionRunnerManagedEnvironmentBindings(ctx, taskRun); err != nil {
		model = serverErrorModelClient{err: fmt.Errorf("restore managed environment state: %w", err)}
	}
	largeToolResults := s.largeToolResultAuthorityForEngine(ctx, options.SessionID)
	maxToolResultBytes := options.OutputLimitBytes
	if largeToolResults != nil {
		maxToolResultBytes = runnerLargeToolResultInlineLimit(maxToolResultBytes)
	}
	return agentruntime.Engine{
		Model: model,
		Tools: serverAgentRuntimeToolGateway{
			server:             s,
			origin:             "agent-runtime",
			taskRun:            taskRun,
			allowedTools:       options.AllowedTools,
			selectedSkillNames: append([]string(nil), options.SelectedSkillNames...),
			skillPolicy:        runtimeSkillPolicyAuthorityFromOptions(options),
			kernel:             s.resolveAgentKernelContext(ctx, options.SessionID),
			sessionID:          options.SessionID,
			outputLimitBytes:   options.OutputLimitBytes,
			fileReadLimitBytes: maxToolResultBytes,
			toolSchemas:        toolSchemaSnapshot,
			toolValidators:     agentRuntimeToolValidators(toolSchemaSnapshot),
			hasToolSnapshot:    len(runtimeToolSchemas) > 0,
			reviewerEvidence:   sessionReviewerEvidenceScopeFromContext(ctx),
			sourceToolActivity: options.sourceToolActivity,
		},
		MaxToolResultBytes: maxToolResultBytes,
		LargeToolResults:   largeToolResults,
	}
}

func (s *Server) agentRuntimeToolSchemas(allowedTools []string, sessionID ...string) []agentruntime.ToolSchema {
	return s.agentRuntimeToolSchemasWithContext(context.Background(), allowedTools, sessionID...)
}

func (s *Server) agentRuntimeToolSchemasWithContext(ctx context.Context, allowedTools []string, sessionID ...string) []agentruntime.ToolSchema {
	return s.agentRuntimeToolSchemasWithContextOptions(ctx, allowedTools, false, sessionID...)
}

func (s *Server) agentRuntimeToolSchemasWithContextOptions(
	ctx context.Context,
	allowedTools []string,
	disableMCPDiscovery bool,
	sessionID ...string,
) []agentruntime.ToolSchema {
	if s == nil || s.tools == nil {
		return nil
	}
	kernelSessionID := ""
	if len(sessionID) > 0 {
		kernelSessionID = sessionID[0]
	}
	memoryToolsAvailable := true
	if strings.TrimSpace(kernelSessionID) != "" {
		resolved, err := s.resolveWorkspaceMemoryScope(ctx, kernelSessionID)
		memoryToolsAvailable = err == nil && resolved.Enabled
	}
	kernelIdentity := s.resolveAgentKernelContext(ctx, kernelSessionID)
	worktreeToolsAvailable := s.agentRuntimeWorktreeToolsAvailable(ctx, kernelIdentity)
	allowed := chatRunnerAllowedToolSet(allowedTools)
	names := s.modelSchemaNames(allowedTools)
	schemas := make([]agentruntime.ToolSchema, 0, len(names))
	for _, name := range names {
		if name == "ToolSearch" {
			continue
		}
		if disableMCPDiscovery && strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "mcp__") {
			continue
		}
		if !chatRunnerToolAllowed(name, allowed) {
			continue
		}
		if (name == "EnterWorktree" || name == "ExitWorktree") && !worktreeToolsAvailable {
			continue
		}
		if !memoryToolsAvailable && isWorkspaceMemoryToolName(name) {
			continue
		}
		tool, ok := s.registeredTool(name)
		if !ok || !tool.Executable {
			continue
		}
		schemas = append(schemas, agentruntime.ToolSchema{
			Name:         tool.Name,
			Description:  tool.Description,
			Parameters:   chatToolParameters(tool),
			Capabilities: append([]string(nil), tool.Capabilities...),
			Exposure:     registeredAgentRuntimeToolExposure(tool),
		})
	}
	if !disableMCPDiscovery && kernelIdentity == nil {
		schemas = append(schemas, s.agentRuntimeMCPToolSchemas(ctx, allowed, kernelSessionID)...)
	}
	schemas = append(schemas, s.agentKernelToolSchemas(kernelIdentity, allowed)...)
	if kernelIdentity != nil {
		schemas = preferCanonicalFrameFileToolSchemas(schemas)
	}
	if structuredOnboardingToolAllowed(allowedTools) {
		schemas = append(schemas, structuredOnboardingAttachmentToolSchema())
	}
	return schemas
}

func (s *Server) agentRuntimeWorktreeToolsAvailable(ctx context.Context, identity *agentKernelContext) bool {
	if s == nil || identity == nil || strings.TrimSpace(identity.workspaceDir) == "" {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := findGitRoot(probeCtx, identity.workspaceDir)
	return err == nil
}

func isWorkspaceMemoryToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "read_memory", "write_memory", "search_memory":
		return true
	default:
		return false
	}
}

func agentRuntimeToolSchemaNames(schemas []agentruntime.ToolSchema) []string {
	names := make([]string, 0, len(schemas))
	seen := map[string]struct{}{}
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		key := strings.ToLower(name)
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	return names
}

func agentRuntimeToolSchemaNamed(schemas []agentruntime.ToolSchema, name string) bool {
	for _, schema := range schemas {
		if strings.EqualFold(strings.TrimSpace(schema.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func filterAgentRuntimeToolSchemas(schemas []agentruntime.ToolSchema, allowedTools []string) []agentruntime.ToolSchema {
	allowed := chatRunnerAllowedToolSet(allowedTools)
	filtered := make([]agentruntime.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		if name == "" {
			continue
		}
		toolAllowed := chatRunnerToolAllowed(name, allowed)
		if name == "wait_for_notification" {
			toolAllowed = true
		}
		if isAgentKernelToolName(name) {
			toolAllowed = agentKernelToolAllowed(name, allowed)
		}
		if toolAllowed {
			filtered = append(filtered, schema)
		}
	}
	return filtered
}

func agentRuntimeToolSchemaSearchMatches(query string, schemas []agentruntime.ToolSchema, maxResults int) []string {
	candidates := make([]toolDiscoveryCandidate, 0, len(schemas))
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		if name == "" {
			continue
		}
		candidates = append(candidates, toolDiscoveryCandidate{
			Name: name,
			Haystack: strings.Join([]string{
				schema.Description,
				strings.Join(agentRuntimeToolSchemaInputNames(schema), " "),
			}, " "),
		})
	}
	return rankToolDiscoveryCandidates(query, candidates, maxResults)
}

func (s *Server) agentRuntimeMCPToolSchemas(parent context.Context, allowed map[string]struct{}, sessionID ...string) []agentruntime.ToolSchema {
	if s == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	list, unavailable := s.agentRuntimeMCPToolList(parent)
	schemas, listUnavailable := agentRuntimeMCPToolListSchemas(list, allowed)
	unavailable = unavailable || listUnavailable
	workspaceSessionID := ""
	if len(sessionID) > 0 {
		workspaceSessionID = sessionID[0]
	}
	workspace := s.agentRuntimeWorkspaceMCPToolSchemas(parent, workspaceSessionID, allowed, schemas)
	schemas = append(schemas, workspace.Schemas...)
	unavailable = unavailable || workspace.Unavailable
	if unavailable && agentRuntimeMCPDiagnosticAllowed(allowed) && !agentRuntimeToolSchemaNamed(schemas, agentRuntimeMCPUnavailableToolName) {
		schemas = append(schemas, agentRuntimeMCPUnavailableToolSchema())
	}
	return schemas
}

// agentRuntimeMCPToolList discovers dynamic MCP servers through the same
// address-pinned outbound policy used by the workspace MCP runtime: remote
// HTTP servers are validated as public HTTPS and pinned per request, while the
// server's injected HTTP client remains available to transport the probe.
func (s *Server) agentRuntimeMCPToolList(parent context.Context) (mcpstdio.ToolListResult, bool) {
	return s.agentRuntimeMCPToolListWithBudget(parent, agentRuntimeMCPDiscoveryBudget)
}

// agentRuntimeMCPToolListWithBudget keeps admission discovery best-effort. A
// configured connector may be unavailable without preventing the model from
// receiving the rest of the tool snapshot or making its first response.
func (s *Server) agentRuntimeMCPToolListWithBudget(parent context.Context, budget time.Duration) (mcpstdio.ToolListResult, bool) {
	if parent == nil {
		parent = context.Background()
	}
	if budget <= 0 {
		budget = agentRuntimeMCPDiscoveryBudget
	}
	discoveryCtx, stopDiscovery := context.WithTimeout(parent, budget)
	defer stopDiscovery()

	result := mcpstdio.ToolListResult{Servers: []mcpstdio.ServerProjection{}, Tools: []mcpstdio.ToolProjection{}}
	configs, err := mcpstdio.LoadServers(s.fileRoot)
	if err != nil {
		return result, agentRuntimeContextError(parent) == nil
	}
	unavailable := false
	for name, config := range configs {
		projection := mcpstdio.ServerProjection{Name: name, Status: "configured-not-connected", Configured: true, Scope: config.Scope}
		if config.Disabled {
			projection.Status = "disabled"
			result.Servers = append(result.Servers, projection)
			continue
		}
		if !config.IsCallableTransport() {
			projection.Error = fmt.Sprintf("MCP transport %q is not callable by the Go runtime.", config.TransportLabel())
			result.Servers = append(result.Servers, projection)
			unavailable = true
			continue
		}
		probeCtx := discoveryCtx
		if strings.TrimSpace(config.URL) != "" {
			client, secureErr := mcpdirectory.SecureHTTPClient(discoveryCtx, config.URL, s.httpClient)
			if secureErr != nil {
				projection.Error = secureErr.Error()
				projection.Status = "failed"
				result.Servers = append(result.Servers, projection)
				unavailable = true
				continue
			}
			probeCtx = mcpstdio.WithHTTPClient(discoveryCtx, client)
		}
		tools, toolErr := mcpstdio.ListToolsForServer(probeCtx, s.fileRoot, name, config)
		if toolErr != nil {
			projection.Error = toolErr.Error()
			projection.Status = "failed"
			result.Servers = append(result.Servers, projection)
			unavailable = true
			continue
		}
		projection.Status = "connected"
		projection.ToolCount = len(tools)
		result.Servers = append(result.Servers, projection)
		result.Tools = append(result.Tools, tools...)
	}
	return result, unavailable
}

func agentRuntimeMCPToolListSchemas(list mcpstdio.ToolListResult, allowed map[string]struct{}) ([]agentruntime.ToolSchema, bool) {
	schemas := make([]agentruntime.ToolSchema, 0, len(list.Tools))
	for _, tool := range list.Tools {
		if !chatRunnerToolAllowed(tool.Name, allowed) {
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
		capabilities := []string{"mcp", "tool-execution"}
		if tool.ReadOnlyHint {
			capabilities = append(capabilities, "read-only")
		}
		schemas = append(schemas, agentruntime.ToolSchema{
			Name:         tool.Name,
			Description:  description,
			Parameters:   parameters,
			OutputSchema: tool.OutputSchema,
			Capabilities: capabilities,
			Exposure:     agentruntime.ToolExposureHidden,
		})
	}
	return schemas, agentRuntimeMCPServerDiscoveryUnavailable(list.Servers)
}

func agentRuntimeMCPServerDiscoveryUnavailable(servers []mcpstdio.ServerProjection) bool {
	for _, server := range servers {
		status := strings.ToLower(strings.TrimSpace(server.Status))
		if status == "disabled" || status == "connected" || status == "needs_auth" {
			continue
		}
		if strings.TrimSpace(server.Error) != "" || status == "failed" {
			return true
		}
	}
	return false
}

func agentRuntimeMCPDiagnosticAllowed(allowed map[string]struct{}) bool {
	if len(allowed) == 0 {
		return true
	}
	for name := range allowed {
		name = strings.TrimSpace(name)
		if strings.EqualFold(name, "MCPTool") || strings.HasPrefix(strings.ToLower(name), "mcp__") || strings.EqualFold(name, agentRuntimeMCPUnavailableToolName) {
			return true
		}
	}
	return false
}

func agentRuntimeMCPUnavailableToolSchema() agentruntime.ToolSchema {
	return agentruntime.ToolSchema{
		Name: agentRuntimeMCPUnavailableToolName, Description: agentRuntimeMCPUnavailableDescription,
		Parameters: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
	}
}

func agentRuntimeMCPUnavailableResult() map[string]any {
	return map[string]any{
		"ok": false, "code": "mcp_schema_discovery_unavailable",
		"message":     "MCP tool discovery is temporarily unavailable.",
		"recoverable": true, "recovery": "retry_after_connector_recovery_or_inspect_mcp_settings",
	}
}

type serverAgentRuntimeToolGateway struct {
	server *Server
	origin string
	// resumeAfterApproval skips the preflight and permission decisions already
	// persisted before the pause. Resume hooks still run through the same stage
	// order so the durable approval lifecycle retains its existing contract.
	resumeAfterApproval bool
	auditExtra          map[string]any
	permissionSource    string
	// taskRun is captured when the gateway is created. The agent runtime may
	// derive a fresh execution context while parking or resuming an approval;
	// keeping the same task-scoped authority here prevents Skill state from
	// disappearing before a scientific tool reaches its preflight gate.
	taskRun               *sessionRunnerChatRun
	allowedTools          []string
	selectedSkillNames    []string
	skillPolicy           runtimeSkillPolicyAuthority
	suppressHooks         bool
	kernel                *agentKernelContext
	sessionID             string
	outputLimitBytes      int64
	fileReadLimitBytes    int64
	toolSchemas           []agentruntime.ToolSchema
	toolValidators        map[string]agentRuntimeMCPValidator
	hasToolSnapshot       bool
	exactToolAuthority    bool
	deferSchemaToExecutor bool
	directExecutor        bool
	reviewerEvidence      *sessionReviewerEvidenceScope
	sourceToolActivity    *sessionRunnerSourceToolActivity
}

func (g serverAgentRuntimeToolGateway) RequiredToolChoice(
	messages []agentruntime.Message,
	tools []agentruntime.ToolSchema,
) any {
	pendingSkills := uniqueSortedFolded(append(
		runnerPendingAskUserRequiredSkillNames(messages),
		g.pendingSelectedEvidenceResolverSkillNames()...,
	))
	if g.taskRun != nil {
		g.taskRun.setPendingRequiredSkillNames(pendingSkills...)
	}
	if choice := runnerPendingAskUserRequiredSkillChoice(pendingSkills, tools); choice != nil {
		return choice
	}
	if g.taskRun == nil || strings.TrimSpace(g.taskRun.TaskIntent) == "" {
		return nil
	}
	if choice := g.generatedPlanPriorityToolChoice(messages, tools); choice != nil {
		return choice
	}
	if len(tools) > 0 && sessionRunnerImmediateArtifactRepairRequired(g.taskRun, messages) {
		if choice := sessionRunnerCorrectionRequiredToolChoice(g.taskRun, messages, tools); choice != nil {
			g.taskRun.setRequiredMCPSourceClass(runnerRequiredMCPSourceClassForChoice(g.taskRun, messages, choice, tools))
			return choice
		}
		g.taskRun.setRequiredMCPSourceClass("")
		return "required"
	}
	if len(tools) > 0 && sessionRunnerCorrectionStillRequiresAction(g.taskRun, messages) {
		if choice := sessionRunnerCorrectionRequiredToolChoice(g.taskRun, messages, tools); choice != nil {
			g.taskRun.setRequiredMCPSourceClass(runnerRequiredMCPSourceClassForChoice(g.taskRun, messages, choice, tools))
			return choice
		}
		g.taskRun.setRequiredMCPSourceClass("")
		return "required"
	}
	if len(tools) > 0 && sessionRunnerCorrectionReadyForRevalidation(g.taskRun, messages) {
		// Freeze the newly changed candidate for one immutable validation pass.
		// If validation still finds a defect, the outer runner opens a fresh
		// correction with current facts; further provider-selected tools here can
		// only obscure convergence or repeat an unchanged save.
		g.taskRun.setRequiredMCPSourceClass("")
		return "none"
	}
	planChoice := g.generatedPlanRequiredToolChoice(messages, tools)
	var activePlanAction map[string]any
	hasActivePlanAction := false
	if g.server != nil {
		_, activePlanAction, hasActivePlanAction = g.server.generatedPlanActiveStep(g.sessionID)
	}
	lastSettledPlanCall, hasLastSettledPlanCall := generatedPlanLastSettledToolCall(messages)
	planHasSourceContinuation := researchContinuationRequiresExecution(
		mapValue(activePlanAction["research_continuation"]),
	)
	planControlOwnsPriority := hasActivePlanAction &&
		(strings.TrimSpace(stringValue(activePlanAction["status"])) == "in_progress" ||
			hasLastSettledPlanCall && normalizeAgentToolName(lastSettledPlanCall.Name) != normalizeAgentToolName(updateStepStatusToolName))
	planChoiceName := normalizeAgentToolName(stringValue(mapValue(planChoice)["name"]))
	if (planHasSourceContinuation && planChoice != nil) ||
		(planControlOwnsPriority && planChoiceName == normalizeAgentToolName(updateStepStatusToolName)) {
		// Durable plan reconciliation is a machine-owned control transition.
		// A source-owned continuation is the next authoritative research action.
		// Settle either before generic Skill routing so a completed source call
		// cannot be diverted while its evidence still belongs to this module.
		return planChoice
	}
	if planChoice != nil {
		return planChoice
	}
	if !hasActivePlanAction && sessionRunnerInlineArtifactRepairReadyForRevalidation(messages) {
		// The latest publication resolved an earlier inline draft warning. Stop
		// provider-selected mutation now and return to the immutable completion
		// validator; another save cannot add evidence or improve the same bytes.
		return "none"
	}
	g.taskRun.setRequiredMCPSourceClass("")
	return nil
}

// AdditionalModelToolSchemas is the single post-snapshot exposure resolver.
// Plans may activate their progress Tool and selected Skills may activate the
// exact Tools they declare. Both resolve against the immutable runtime
// authority captured at turn start; neither path can register an executor or
// independently promote a hidden Tool.
func (g serverAgentRuntimeToolGateway) AdditionalModelToolSchemas(
	current []agentruntime.ToolSchema,
) []agentruntime.ToolSchema {
	if g.server == nil || g.taskRun == nil || len(g.toolSchemas) == 0 {
		return nil
	}
	selected := make([]skills.Skill, 0)
	selectedNames := uniqueSortedFolded(append(
		append([]string(nil), g.selectedSkillNames...),
		g.taskRun.executedSkillNamesSnapshot()...,
	))
	seenSkills := make(map[string]struct{}, len(selectedNames))
	for _, name := range selectedNames {
		closure, err := g.runtimeSkillClosure(name)
		if err != nil {
			continue
		}
		for _, skill := range closure {
			key := strings.ToLower(strings.TrimSpace(skill.Name))
			if key == "" {
				continue
			}
			if _, duplicate := seenSkills[key]; duplicate {
				continue
			}
			seenSkills[key] = struct{}{}
			selected = append(selected, skill)
		}
	}
	activated := make(map[string]struct{})
	for _, skill := range selected {
		for _, name := range skill.Tools {
			if key := normalizeAgentToolName(name); key != "" {
				activated[key] = struct{}{}
			}
		}
	}
	if g.taskRun.planProgressAvailable.Load() {
		activated[normalizeAgentToolName(updateStepStatusToolName)] = struct{}{}
	}
	additions := make([]agentruntime.ToolSchema, 0, len(activated))
	for _, schema := range projectAgentRuntimeToolSchemas(g.toolSchemas, selected, activated, false) {
		if agentRuntimeToolSchemaNamed(current, schema.Name) ||
			agentRuntimeToolSchemaNamed(additions, schema.Name) {
			continue
		}
		additions = append(additions, schema)
	}
	return additions
}

type agentRuntimeHookConfig struct {
	Key string
	Raw map[string]any
}

type agentRuntimeToolExecutionTimeoutKey struct{}

type agentRuntimeParentSessionContextKey struct{}

func withAgentRuntimeParentSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, agentRuntimeParentSessionContextKey{}, strings.TrimSpace(sessionID))
}

func agentRuntimeParentSessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(agentRuntimeParentSessionContextKey{}).(string)
	return strings.TrimSpace(value)
}

func (g serverAgentRuntimeToolGateway) effectiveAllowedTools() []string {
	if g.hasToolSnapshot {
		return agentRuntimeToolSchemaNames(g.toolSchemas)
	}
	return g.allowedTools
}

func (g serverAgentRuntimeToolGateway) toolAllowed(name string) bool {
	if g.exactToolAuthority {
		canonical, err := canonicalRuntimeToolName(name)
		if err != nil {
			return false
		}
		for _, allowed := range g.allowedTools {
			allowedCanonical, err := canonicalRuntimeToolName(allowed)
			if err == nil && canonical == allowedCanonical {
				return true
			}
		}
		return false
	}
	if g.hasToolSnapshot {
		for _, schema := range g.toolSchemas {
			if schema.Name == name {
				return true
			}
		}
		return false
	}
	if name == "wait_for_notification" {
		return true
	}
	return chatRunnerToolAllowed(name, chatRunnerAllowedToolSet(g.allowedTools))
}

func withAgentRuntimeToolExecutionTimeout(ctx context.Context, timeout time.Duration) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return ctx
	}
	return context.WithValue(ctx, agentRuntimeToolExecutionTimeoutKey{}, timeout)
}

func boundedAgentRuntimeToolContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout, _ := ctx.Value(agentRuntimeToolExecutionTimeoutKey{}).(time.Duration)
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// AdmitsToolCall performs the same name, snapshot, normalization, and schema
// checks as Execute without running the tool or writing audit state. The
// runtime failure guard uses it only after repeated invalid calls so a valid
// correction can still reach the gateway.
