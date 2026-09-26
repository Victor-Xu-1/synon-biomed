package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"synon-go/internal/agentruntime"
	"synon-go/internal/executionprep"
	"synon-go/internal/toolcontract"
	"synon-go/internal/toolgateway"
)

type serverAgentRuntimeGatewayExecution struct {
	gateway       serverAgentRuntimeToolGateway
	call          agentruntime.ToolCall
	response      any
	responseParts []agentruntime.ContentPart
	result        agentruntime.ToolResult
	resultReady   bool
	badRequest    bool
}

type serverAgentRuntimeGatewayReceipt struct {
	Result     agentruntime.ToolResult
	Value      any
	Input      map[string]any
	Status     string
	Err        error
	BadRequest bool
}

type exactServerToolGatewayOptions struct {
	SuppressHooks       bool
	ResumeAfterApproval bool
	PermissionSource    string
	DirectExecutor      bool
	AuditExtra          map[string]any
	Kernel              *agentKernelContext
}

var serverAgentRuntimePipelineOnce sync.Once
var serverAgentRuntimePipelineValue *toolgateway.Pipeline

func serverAgentRuntimeToolPipeline() *toolgateway.Pipeline {
	serverAgentRuntimePipelineOnce.Do(func() {
		serverAgentRuntimePipelineValue = toolgateway.MustPipeline(
			toolgateway.Step{Stage: toolgateway.StageNormalize, Run: serverAgentRuntimeGatewayNormalize},
			toolgateway.Step{Stage: toolgateway.StageAdmit, Run: serverAgentRuntimeGatewayAdmit},
			toolgateway.Step{Stage: toolgateway.StagePreflight, Run: serverAgentRuntimeGatewayPreflight},
			toolgateway.Step{Stage: toolgateway.StageFailureBudget, Run: serverAgentRuntimeGatewayFailureBudget},
			toolgateway.Step{Stage: toolgateway.StageReviewScope, Run: serverAgentRuntimeGatewayReviewScope},
			toolgateway.Step{Stage: toolgateway.StagePreHooks, Run: serverAgentRuntimeGatewayPreHooks},
			toolgateway.Step{Stage: toolgateway.StageRevalidate, Run: serverAgentRuntimeGatewayRevalidate},
			toolgateway.Step{Stage: toolgateway.StagePermission, Run: serverAgentRuntimeGatewayPermission},
			toolgateway.Step{Stage: toolgateway.StageSourceBudget, Run: serverAgentRuntimeGatewaySourceBudget},
			toolgateway.Step{Stage: toolgateway.StageExecute, Run: serverAgentRuntimeGatewayExecute},
			toolgateway.Step{Stage: toolgateway.StageMaterialize, Run: serverAgentRuntimeGatewayMaterialize},
			toolgateway.Step{Stage: toolgateway.StagePostHooks, Run: serverAgentRuntimeGatewayPostHooks},
			toolgateway.Step{Stage: toolgateway.StageAudit, Run: serverAgentRuntimeGatewayAudit},
		)
	})
	return serverAgentRuntimePipelineValue
}

func serverAgentRuntimeExecution(invocation *toolgateway.Invocation) *serverAgentRuntimeGatewayExecution {
	execution, ok := invocation.Extension.(*serverAgentRuntimeGatewayExecution)
	if !ok || execution == nil {
		panic("server tool gateway invocation has no execution adapter")
	}
	return execution
}

func (g serverAgentRuntimeToolGateway) auditOrigin() string {
	if origin := strings.TrimSpace(g.origin); origin != "" {
		return origin
	}
	return "agent-runtime"
}

func (g serverAgentRuntimeToolGateway) ToolCallExecutionIdentity(
	call agentruntime.ToolCall,
) (agentruntime.ToolCall, bool) {
	name, err := canonicalRuntimeToolName(call.Name)
	if err != nil {
		return agentruntime.ToolCall{}, false
	}
	input := map[string]any{}
	if len(call.Arguments) > 0 && json.Unmarshal(call.Arguments, &input) != nil {
		return agentruntime.ToolCall{}, false
	}
	encoded, err := json.Marshal(g.normalizeAdmittedToolArguments(name, input))
	if err != nil {
		return agentruntime.ToolCall{}, false
	}
	resolved := call
	resolved.Name = name
	resolved.Arguments = encoded
	return resolved, true
}

func (g serverAgentRuntimeToolGateway) Execute(ctx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
	return serverAgentRuntimeGatewayResult(g.executeGateway(ctx, call))
}

func serverAgentRuntimeGatewayResult(receipt serverAgentRuntimeGatewayReceipt) (agentruntime.ToolResult, error) {
	if receipt.Input == nil {
		return receipt.Result, receipt.Err
	}
	executed, err := json.Marshal(receipt.Input)
	if err != nil {
		return receipt.Result, errors.Join(receipt.Err, fmt.Errorf("encode executed tool arguments: %w", err))
	}
	result := receipt.Result
	result.ExecutedArguments = append(json.RawMessage(nil), executed...)
	return result, receipt.Err
}

func (g serverAgentRuntimeToolGateway) executeGateway(ctx context.Context, call agentruntime.ToolCall) serverAgentRuntimeGatewayReceipt {
	if ctx == nil {
		ctx = context.Background()
	}
	g, ctx = g.bindTaskRunContext(ctx)
	if err := g.server.hydrateSessionRunnerManagedEnvironmentBindings(ctx, g.taskRun); err != nil {
		return serverAgentRuntimeGatewayReceipt{Err: fmt.Errorf("restore managed environment state: %w", err)}
	}
	ctx, cancel := boundedAgentRuntimeToolContext(ctx)
	defer cancel()
	execution := &serverAgentRuntimeGatewayExecution{gateway: g, call: call}
	invocation := toolgateway.NewInvocation(ctx, call.ID, call.Name, call.Arguments, execution)
	invocation.AuditExtra = copyMapAny(g.auditExtra)
	serverAgentRuntimeToolPipeline().Run(invocation)
	receipt := serverAgentRuntimeGatewayReceipt{
		Value: invocation.Value, Input: copyMapAny(invocation.Input), Status: invocation.Status,
		Err: invocation.Err, BadRequest: execution.badRequest,
	}
	if invocation.Err != nil {
		return receipt
	}
	if execution.resultReady {
		receipt.Result = execution.result
		receipt.Value = execution.result.Value
		return receipt
	}
	receipt.Result = agentruntime.ToolResult{Value: invocation.Value}
	return receipt
}

func (s *Server) executeExactToolGateway(
	ctx context.Context,
	origin, sessionID, callID, toolName string,
	input map[string]any,
	options exactServerToolGatewayOptions,
) serverAgentRuntimeGatewayReceipt {
	if input == nil {
		input = map[string]any{}
	}
	allowedName := strings.TrimSpace(toolName)
	if canonical, err := canonicalRuntimeToolName(toolName); err == nil {
		allowedName = canonical
	}
	schemas := s.agentRuntimeToolSchemasWithContext(ctx, []string{allowedName}, sessionID)
	if !agentRuntimeToolSchemaNamed(schemas, allowedName) && s != nil {
		if tool, found := s.registeredTool(allowedName); found && tool.Executable {
			schemas = append(schemas, agentruntime.ToolSchema{
				Name: tool.Name, Description: tool.Description, Parameters: chatToolParameters(tool),
			})
		}
	}
	kernel := options.Kernel
	if kernel == nil && strings.TrimSpace(sessionID) != "" {
		if options.ResumeAfterApproval {
			kernel = s.resolveApprovedAgentToolContext(ctx, strings.TrimSpace(sessionID))
		} else {
			kernel = s.resolveAgentKernelContext(ctx, strings.TrimSpace(sessionID))
		}
	}
	gateway := serverAgentRuntimeToolGateway{
		server:                s,
		origin:                origin,
		resumeAfterApproval:   options.ResumeAfterApproval,
		auditExtra:            copyMapAny(options.AuditExtra),
		permissionSource:      strings.TrimSpace(options.PermissionSource),
		allowedTools:          []string{allowedName},
		suppressHooks:         options.SuppressHooks,
		kernel:                kernel,
		sessionID:             strings.TrimSpace(sessionID),
		outputLimitBytes:      defaultSessionRunnerOutputLimitBytes,
		toolSchemas:           schemas,
		toolValidators:        agentRuntimeToolValidators(schemas),
		hasToolSnapshot:       len(schemas) > 0,
		exactToolAuthority:    true,
		deferSchemaToExecutor: options.DirectExecutor || strings.TrimSpace(toolName) != allowedName,
		directExecutor:        options.DirectExecutor,
	}
	return gateway.executeGateway(ctx, agentruntime.ToolCall{
		ID: callID, Name: toolName, Arguments: mustMarshalRawMessage(input),
	})
}

func serverAgentRuntimeGatewayNormalize(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	name, err := canonicalRuntimeToolName(invocation.RequestedName)
	if err != nil {
		execution.badRequest = true
		value := map[string]any{"ok": false, "error": "tool name is invalid"}
		invocation.CompleteForAudit(value, "failed", stringValue(value["error"]), nil)
		return
	}
	invocation.CanonicalName = name
	execution.call.Name = name
	if !gateway.exactToolAuthority &&
		(retiredAgentRuntimeRequestedName(invocation.RequestedName) || retiredAgentRuntimeRequestedName(name)) {
		value := map[string]any{
			"ok": false, "code": "retired_tool", "tool": invocation.RequestedName,
			"error":     "the requested tool is retired from the scientific Harness",
			"retryable": false, "recovery": "use_an_exact_name_from_the_current_tool_snapshot",
		}
		invocation.CompleteForAudit(value, "blocked", "", nil)
		return
	}
	invocation.Context = gateway.server.withTranscriptArtifactToolSource(invocation.Context, execution.call.ID)
	if contextErr := agentRuntimeContextError(invocation.Context); contextErr != nil {
		invocation.CompleteForAudit(nil, agentRuntimeContextStatus(contextErr), contextErr.Error(), contextErr)
		return
	}
}

func serverAgentRuntimeGatewayAdmit(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	name := invocation.CanonicalName
	authorityName := name
	if gateway.hasToolSnapshot && name != toolcontract.AskUser {
		authorityName = invocation.RequestedName
	}
	if !gateway.toolAllowed(authorityName) {
		execution.badRequest = true
		value := map[string]any{
			"ok": false, "code": "tool_not_in_snapshot", "tool": invocation.RequestedName,
			"error":     fmt.Sprintf("tool %s is not allowed for agent runtime", name),
			"retryable": false, "recovery": "use_an_exact_name_from_the_current_tool_snapshot",
		}
		if suggestion := agentRuntimeMCPToolSuggestion(invocation.RequestedName, gateway.toolSchemas); suggestion != "" {
			value["suggestedToolName"] = suggestion
		}
		invocation.CompleteForAudit(value, "blocked", "", nil)
		return
	}
	input := map[string]any{}
	if len(invocation.Arguments) > 0 {
		if err := json.Unmarshal(invocation.Arguments, &input); err != nil {
			execution.badRequest = true
			value := map[string]any{"ok": false, "error": fmt.Sprintf("decode tool arguments: %v", err)}
			invocation.CompleteForAudit(value, "failed", stringValue(value["error"]), nil)
			return
		}
	}
	invocation.OriginalInput = copyMapAny(input)
	invocation.Input = gateway.normalizeAdmittedToolArguments(name, input)
	if !gateway.deferSchemaToExecutor {
		if value := gateway.validateAdmittedToolArguments(name, invocation.Input); value != nil {
			execution.badRequest = true
			invocation.CompleteForAudit(value, "failed", stringValue(value["message"]), nil)
			return
		}
	}
	if isAgentWorkspaceFileTool(name) {
		if err := validateAgentWorkspaceFileToolInput(name, invocation.Input); err != nil {
			execution.badRequest = true
			value := agentRuntimeFailedToolValue(nil, err)
			invocation.CompleteForAudit(value, "failed", err.Error(), nil)
		}
	}
}

func serverAgentRuntimeGatewayPreflight(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	name := invocation.CanonicalName
	if preflight := gateway.agentRuntimeRegisteredAcquisitionPreflight(name, invocation.Input); preflight != nil {
		invocation.CompleteForAudit(preflight, "completed", "", nil)
		return
	}
	if gateway.resumeAfterApproval {
		return
	}
	preflight := agentRuntimeUnresolvedToolResultTemplatePreflight(name, invocation.Input)
	if preflight == nil {
		preflight = agentRuntimeUnresolvedSkillDirectoryPreflight(name, invocation.Input)
	}
	if preflight == nil {
		preflight = agentRuntimeRequiredMCPRecoveryPreflight(invocation.Context, name, invocation.Input)
	}
	if preflight == nil {
		preflight = agentRuntimeWorkspaceArtifactReferencePreflight(name, invocation.Input)
	}
	if preflight == nil {
		preflight = gateway.agentRuntimeManagedExecutionOutputMutationPreflight(
			invocation.Context, name, invocation.Input,
		)
	}
	// The engine performs the same task-scoped check before it publishes a tool
	// call, but the execution gateway is the final authority for recovered
	// batches, background calls, and any future engine adapter. Keeping the
	// check here prevents a second execution path from bypassing loaded Skill
	// contracts when an older checkpoint falls outside the model replay window.
	if preflight == nil {
		preflight = gateway.agentRuntimeSkillExecutionContractPreflight(name, invocation.Input, invocation.Context)
	}
	if preflight != nil {
		invocation.CompleteForAudit(preflight, "completed", "", nil)
	}
}

func serverAgentRuntimeGatewayFailureBudget(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	if gateway.resumeAfterApproval {
		return
	}
	name := invocation.CanonicalName
	if value := gateway.durableSemanticFailureBoundary(invocation.Context, name, invocation.Input); value != nil {
		invocation.CompleteForAudit(value, "blocked", stringValue(value["message"]), nil)
		return
	}
	if name == agentRuntimeMCPUnavailableToolName {
		value := agentRuntimeMCPUnavailableResult()
		invocation.CompleteForAudit(value, "failed", stringValue(value["message"]), nil)
	}
}

func serverAgentRuntimeGatewayReviewScope(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	if execution.gateway.resumeAfterApproval {
		return
	}
	scope := execution.gateway.reviewerEvidence
	if scope == nil {
		return
	}
	if err := scope.authorize(invocation.CanonicalName, execution.call.ID, invocation.Input); err != nil {
		value := agentRuntimeToolErrorValue(err)
		invocation.CompleteForAudit(value, "blocked", err.Error(), nil)
	}
}

func serverAgentRuntimeGatewayPreHooks(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	if gateway.suppressHooks || invocation.CanonicalName == "wait_for_notification" {
		return
	}
	input, hook := gateway.server.applyAgentRuntimePreToolHooks(
		invocation.Context, invocation.CanonicalName, execution.call, invocation.Input,
	)
	invocation.Input = input
	if contextErr := agentRuntimeContextError(invocation.Context); contextErr != nil {
		invocation.CompleteForAudit(hook, agentRuntimeContextStatus(contextErr), contextErr.Error(), contextErr)
		return
	}
	if hook != nil {
		invocation.CompleteForAudit(hook, "blocked", "", nil)
	}
}

func serverAgentRuntimeGatewayRevalidate(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	name := invocation.CanonicalName
	if name == "wait_for_notification" {
		return
	}
	if gateway.suppressHooks {
		if name == "StructuredOutput" && strings.TrimSpace(gateway.sessionID) != "" {
			invocation.Input["sessionId"] = strings.TrimSpace(gateway.sessionID)
		}
		return
	}
	if name == "StructuredOutput" && !gateway.directExecutor {
		delete(invocation.Input, "sessionId")
	}
	if !gateway.deferSchemaToExecutor {
		if value := gateway.validateAdmittedToolArguments(name, invocation.Input); value != nil {
			execution.badRequest = true
			invocation.CompleteForAudit(value, "failed", stringValue(value["message"]), nil)
			return
		}
	}
	if isAgentWorkspaceFileTool(name) {
		if err := validateAgentWorkspaceFileToolInput(name, invocation.Input); err != nil {
			execution.badRequest = true
			value := agentRuntimeFailedToolValue(nil, err)
			invocation.CompleteForAudit(value, "failed", err.Error(), nil)
			return
		}
	}
	if name == "StructuredOutput" && strings.TrimSpace(gateway.sessionID) != "" {
		invocation.Input["sessionId"] = strings.TrimSpace(gateway.sessionID)
	}
}

func serverAgentRuntimeGatewayPermission(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	name := invocation.CanonicalName
	if gateway.suppressHooks || gateway.resumeAfterApproval || name == "wait_for_notification" || isAgentKernelToolName(name) {
		return
	}
	permissionSource := strings.TrimSpace(gateway.permissionSource)
	if permissionSource == "" {
		permissionSource = "agent-runtime"
	}
	permission := gateway.server.agentRuntimePermissionResultForSessionAndSourceWithContext(
		invocation.Context, gateway.sessionID, name, execution.call, invocation.Input, permissionSource,
	)
	if contextErr := agentRuntimeContextError(invocation.Context); contextErr != nil {
		invocation.CompleteForAudit(permission, agentRuntimeContextStatus(contextErr), contextErr.Error(), contextErr)
		return
	}
	if permission == nil {
		return
	}
	if pause := transcriptAgentToolApprovalPause(
		invocation.Context, gateway.sessionID, name, execution.call, invocation.Input, permission,
	); pause != nil {
		invocation.CompleteForAudit(pause.Data, "paused", "", pause)
		return
	}
	status := stringValue(permission["decision"])
	if status == "" {
		status = "blocked"
	}
	invocation.CompleteForAudit(permission, status, "", nil)
}

func serverAgentRuntimeGatewaySourceBudget(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	name := invocation.CanonicalName
	if gateway.suppressHooks || name == "wait_for_notification" {
		return
	}
	capabilities := agentRuntimeToolCapabilities(gateway.toolSchemas, name)
	if gateway.taskRun != nil {
		if taskCapabilities := gateway.taskRun.toolCapabilities(name); len(taskCapabilities) > 0 {
			capabilities = taskCapabilities
		}
	}
	if ordinal := gateway.sourceToolActivity.record(capabilities); ordinal > 0 {
		if invocation.AuditExtra == nil {
			invocation.AuditExtra = make(map[string]any)
		}
		invocation.AuditExtra["sourceAttemptOrdinal"] = ordinal
	}
}

func serverAgentRuntimeGatewayExecute(invocation *toolgateway.Invocation) {
	invocation.Context = context.WithValue(invocation.Context, agentToolExecutionContextKey{}, true)
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	name := invocation.CanonicalName
	// Keep the immutable binary-route invariant at the final execution
	// boundary as well as the preflight stage. Approved resumes may bypass
	// permission-oriented preflight, but they must never bypass route safety.
	if boundary := gateway.agentRuntimeRegisteredAcquisitionPreflight(name, invocation.Input); boundary != nil {
		invocation.CompleteForAudit(boundary, "completed", "", nil)
		return
	}
	// Rebind the final source after all earlier stages, including approved
	// resumes. The proof stays in a private host context, never tool arguments.
	if boundary := gateway.agentRuntimeImplementationExecutionChoicePreflight(name, invocation.Input, invocation.Context); boundary != nil {
		invocation.CompleteForAudit(boundary, "completed", "", nil)
		return
	}
	invocation.Context = executionprep.WithObservation(invocation.Context,
		gateway.agentRuntimeDiagnosticObservation(invocation.Context, name, invocation.Input))
	if name == "wait_for_notification" {
		result, err := gateway.server.executeAgentKernelNotificationWait(
			invocation.Context, gateway.kernel, execution.call.ID, invocation.Input,
		)
		if err != nil {
			execution.badRequest = true
			invocation.CompleteForAudit(result, "failed", err.Error(), err)
			return
		}
		invocation.CompleteForAudit(result, "completed", "", nil)
		return
	}
	var response any
	var err error
	if gateway.directExecutor {
		response, err = gateway.server.executeToolResponse(invocation.Context, name, invocation.Input)
	} else {
		response, err = gateway.executeAgentToolResponse(
			invocation.Context, execution.call, name, invocation.Input,
		)
	}
	if err == nil {
		gateway.taskRun.bindManagedEnvironmentToolResult(name, invocation.Input, response)
		execution.response = response
		return
	}
	execution.badRequest = true
	if value, recoverable := agentRuntimeRecoverableToolErrorValue(name, response, err); recoverable {
		serverAgentRuntimeCompleteExecutionError(invocation, gateway.suppressHooks, value, "partial", "tool result was partially successful and is recoverable", nil)
		return
	}
	var pause *agentruntime.PauseError
	if errors.As(err, &pause) {
		serverAgentRuntimeCompleteExecutionError(invocation, gateway.suppressHooks, pause.Data, "paused", "", pause)
		return
	}
	if contextErr := agentRuntimeContextError(invocation.Context); contextErr != nil {
		invocation.CompleteForAudit(nil, agentRuntimeContextStatus(contextErr), contextErr.Error(), contextErr)
		return
	}
	value := agentRuntimeFailedToolValue(response, err)
	serverAgentRuntimeCompleteExecutionError(invocation, gateway.suppressHooks, value, "failed", err.Error(), nil)
}

func serverAgentRuntimeCompleteExecutionError(
	invocation *toolgateway.Invocation,
	suppressHooks bool,
	value any,
	status string,
	errorMessage string,
	err error,
) {
	if suppressHooks {
		invocation.CompleteForAudit(value, status, errorMessage, err)
		return
	}
	invocation.CompleteWithPostHooks(value, status, errorMessage, err)
}

func serverAgentRuntimeGatewayMaterialize(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	name := invocation.CanonicalName
	response, parts := unwrapAgentRuntimeRichToolResponse(execution.response)
	response = compactAgentRuntimeToolResponse(name, response)
	if gateway.suppressHooks {
		if contextErr := agentRuntimeContextError(invocation.Context); contextErr != nil {
			invocation.CompleteForAudit(response, agentRuntimeContextStatus(contextErr), contextErr.Error(), contextErr)
			return
		}
	}
	status, errorMessage := agentRuntimeToolResponseStatus(response)
	if status == "completed" && name == "read_file" {
		gateway.taskRun.recordInputAttachmentRead(invocation.Input)
	}
	if !gateway.suppressHooks && status == "completed" && gateway.reviewerEvidence != nil {
		var err error
		response, err = gateway.reviewerEvidence.record(name, execution.call.ID, invocation.Input, response, parts)
		if err != nil {
			value := map[string]any{"ok": false, "error": err.Error()}
			invocation.CompleteWithPostHooks(value, "failed", err.Error(), nil)
			return
		}
	}
	execution.response = response
	execution.responseParts = parts
	execution.result = gateway.trustedAgentRuntimeToolResult(invocation.Context, execution.call, response, parts)
	if status == "completed" && gateway.server.sessionRunnerEvidenceTool(name) {
		if state := gateway.server.generatedPlanResearchHandoffContext(gateway.sessionID, execution.call, response); state != "" {
			var modelContext map[string]any
			if json.Unmarshal([]byte(state), &modelContext) == nil {
				execution.result.ModelContext = modelContext
			}
		}
	}
	execution.resultReady = true
	invocation.Status = status
	invocation.ErrorMessage = errorMessage
	if gateway.suppressHooks {
		invocation.Value = response
	} else {
		invocation.Value = structuredOnboardingToolAuditProjection(name, response)
	}
}

func serverAgentRuntimeGatewayPostHooks(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	if gateway.suppressHooks {
		return
	}
	gateway.server.auditAgentRuntimePostToolHooks(
		invocation.Context, invocation.CanonicalName, execution.call, invocation.Input, invocation.Status, invocation.Value,
	)
	if contextErr := agentRuntimeContextError(invocation.Context); contextErr != nil {
		if invocation.AuditExtra == nil {
			invocation.AuditExtra = map[string]any{}
		}
		invocation.AuditExtra["postHookStatus"] = agentRuntimeContextStatus(contextErr)
	}
}

func serverAgentRuntimeGatewayAudit(invocation *toolgateway.Invocation) {
	execution := serverAgentRuntimeExecution(invocation)
	gateway := execution.gateway
	if gateway.server == nil {
		return
	}
	gateway.server.recordToolGatewayAudit(
		gateway.auditOrigin(), invocation.CanonicalName, execution.call.ID,
		invocation.OriginalInput, invocation.Input, invocation.Status, invocation.Value,
		invocation.ErrorMessage, invocation.StartedAt, invocation.AuditExtra,
	)
}
