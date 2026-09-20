package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"synon-go/internal/agentruntime"
	"synon-go/internal/toolcontract"
)

func (g serverAgentRuntimeToolGateway) AdmitsToolCall(call agentruntime.ToolCall) bool {
	requestedName := call.Name
	name, err := canonicalRuntimeToolName(requestedName)
	if err != nil {
		return false
	}
	if retiredAgentRuntimeRequestedName(requestedName) || retiredAgentRuntimeRequestedName(name) {
		return false
	}
	authorityName := name
	if g.hasToolSnapshot && name != toolcontract.AskUser {
		authorityName = requestedName
	}
	if !g.toolAllowed(authorityName) {
		return false
	}
	input := map[string]any{}
	if len(call.Arguments) > 0 && json.Unmarshal(call.Arguments, &input) != nil {
		return false
	}
	input = g.normalizeAdmittedToolArguments(name, input)
	return g.validateAdmittedToolArguments(name, input) == nil
}

// ToolCallAdmissionDiagnostic returns bounded schema feedback for the private
// model repair turn. It deliberately omits raw argument values so invalid
// content cannot be echoed into a prompt or leak sensitive task data.
func (g serverAgentRuntimeToolGateway) ToolCallAdmissionDiagnostic(call agentruntime.ToolCall) string {
	requestedName := call.Name
	name, err := canonicalRuntimeToolName(requestedName)
	if err != nil {
		return "tool name is invalid"
	}
	if retiredAgentRuntimeRequestedName(requestedName) || retiredAgentRuntimeRequestedName(name) {
		return "tool is retired from the current scientific Harness"
	}
	if !g.toolAllowed(name) {
		return "tool is not in the current exact tool snapshot"
	}
	input := map[string]any{}
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &input); err != nil {
			return "arguments are not valid JSON"
		}
	}
	input = g.normalizeAdmittedToolArguments(name, input)
	value := g.validateAdmittedToolArguments(name, input)
	if value == nil {
		return ""
	}
	if correction := g.invalidAskUserSelectedImplementationCorrection(name, input); correction != nil {
		raw, err := json.Marshal(correction)
		if err != nil {
			return "the exact scientific implementation is already selected; continue its recovery without asking again"
		}
		return string(raw)
	}
	diagnostic := map[string]any{
		"code":              value["code"],
		"message":           value["message"],
		"issues":            sanitizedAgentRuntimeValidationIssues(value["issues"]),
		"expectedArguments": value["expectedArguments"],
	}
	raw, err := json.Marshal(diagnostic)
	if err != nil {
		return "schema validation failed"
	}
	if len(raw) > 1800 {
		raw = raw[:1800]
	}
	return string(raw)
}

// ToolCallPreflightDiagnostics runs task-scoped policy before the engine emits
// EventModelResponse or EventToolStarted. Kernel tools cannot safely return a
// synthetic correction after their durable start boundary because the
// lifecycle store would then correctly require a matching kernel operation.
func (g serverAgentRuntimeToolGateway) ToolCallPreflightDiagnostics(ctx context.Context, calls []agentruntime.ToolCall) (map[int]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	diagnostics, err := g.noProgressBatchPreflight(ctx, calls)
	if err != nil {
		return nil, err
	}
	if diagnostics == nil {
		diagnostics = make(map[int]string)
	}
	for index, call := range calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if diagnostics[index] != "" {
			continue
		}
		if diagnostic := g.toolCallPreflightDiagnostic(ctx, call); diagnostic != "" {
			diagnostics[index] = diagnostic
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return diagnostics, nil
}

func (g serverAgentRuntimeToolGateway) toolCallPreflightDiagnostic(ctx context.Context, call agentruntime.ToolCall) string {
	requestedName := call.Name
	name, err := canonicalRuntimeToolName(requestedName)
	if err != nil || retiredAgentRuntimeRequestedName(requestedName) || retiredAgentRuntimeRequestedName(name) {
		return ""
	}
	authorityName := name
	if g.hasToolSnapshot && name != toolcontract.AskUser {
		authorityName = requestedName
	}
	if !g.toolAllowed(authorityName) {
		return ""
	}
	input := map[string]any{}
	if len(call.Arguments) > 0 && json.Unmarshal(call.Arguments, &input) != nil {
		return ""
	}
	input = g.normalizeAdmittedToolArguments(name, input)
	if g.validateAdmittedToolArguments(name, input) != nil {
		return ""
	}
	preflight := agentRuntimeGeneratePlanContractPreflight(name, input)
	if preflight == nil {
		preflight = g.computeQuestionImplementationPreflight(name, input)
	}
	if preflight == nil {
		preflight = agentRuntimePythonSyntaxPreflight(name, input)
	}
	if preflight == nil {
		var preparer executionSourcePreparer
		if g.server != nil && g.server.kernelManager != nil {
			preparer = g.server.kernelManager
		}
		preflight = agentExecutionPreparationPreflight(ctx, name, input, g.kernel, preparer)
	}
	if preflight == nil && g.server != nil && g.server.kernelManager != nil {
		preflight = agentRuntimePythonEnvironmentAPIPreflight(name, input, g.server.kernelManager)
	}
	if preflight == nil {
		preflight = agentRuntimePythonExplicitModulePreflight(name, input)
	}
	if preflight == nil {
		preflight = agentKernelOptionalFormatterPreflight(name, input)
	}
	if preflight == nil {
		preflight = agentKernelPythonFileHandleShadowPreflight(name, input)
	}
	if preflight == nil {
		preflight = agentKernelLargeToolResultPathPreflight(name, input)
	}
	if preflight == nil {
		preflight = agentKernelMCPCatalogCallPreflight(name, input)
	}
	if preflight == nil && g.reviewerEvidence == nil {
		preflight = agentRuntimeREPLThirdPartyImportPreflight(name, input)
	}
	if preflight == nil {
		preflight = agentRuntimeWorkspaceExistencePreflight(name, input, g.kernel)
	}
	if preflight == nil {
		preflight = agentRuntimeUnresolvedToolResultTemplatePreflight(name, input)
	}
	if preflight == nil {
		requiredSourceClass := ""
		if g.taskRun != nil {
			requiredSourceClass = g.taskRun.requiredMCPSourceClassSnapshot()
		}
		preflight = agentRuntimeREPLMCPContractPreflight(name, input, g.toolSchemas, requiredSourceClass)
	}
	if preflight == nil {
		preflight = g.agentRuntimeREPLRecoveryStatePreflight(name, input)
	}
	if preflight == nil {
		preflight = agentRuntimeUnresolvedSkillDirectoryPreflight(name, input)
	}
	if preflight == nil {
		preflight = agentRuntimeWorkspaceArtifactReferencePreflight(name, input)
	}
	if preflight == nil {
		preflight = g.agentRuntimeManagedExecutionOutputMutationPreflight(
			ctx, name, input,
		)
	}
	// Deterministic, context-free validation must also protect recovery runners.
	// A resumed engine can be constructed before the task-run value is rebound;
	// Claude-style pre-tool validation must not disappear during that window.
	if preflight == nil && g.taskRun == nil {
		return ""
	}
	if preflight == nil {
		preflight = g.agentRuntimeSkillExecutionContractPreflight(name, input)
	}
	if preflight == nil {
		preflight = g.agentRuntimePublicScientificSourcePreflight(name, input)
	}
	if preflight == nil {
		return ""
	}
	diagnostic := map[string]any{
		"code":     preflight["status"],
		"message":  preflight["message"],
		"recovery": preflight["recovery"],
	}
	if requiredReads := anySliceValue(preflight["required_reads"]); len(requiredReads) > 0 {
		diagnostic["required_reads"] = requiredReads
	}
	raw, err := json.Marshal(diagnostic)
	if err != nil {
		return "runtime preflight is required"
	}
	if len(raw) > 1800 {
		raw = raw[:1800]
	}
	return string(raw)
}

// agentRuntimeGeneratePlanContractPreflight keeps the conditional plan
// contract private. The public schema intentionally leaves content optional
// so an approval-only call remains valid; a new plan, however, must contain
// both its summary and phases before any durable tool lifecycle is published.
func agentRuntimeGeneratePlanContractPreflight(publicName string, input map[string]any) map[string]any {
	if strings.ToLower(strings.TrimSpace(publicName)) != generatePlanToolName || boolValue(input["approve"], false) {
		return nil
	}
	summary := strings.TrimSpace(stringValue(input["task_summary"]))
	if summary == "" {
		// normalizeGeneratedPlan already treats the bounded presentation summary
		// as an equivalent task summary. Keep preflight and execution on that same
		// contract so a complete plan is not rejected before normalization.
		summary = strings.TrimSpace(stringValue(input["human_description"]))
	}
	if summary != "" && len(anySliceValue(input["phases"])) > 0 {
		if _, _, _, err := normalizeGeneratedPlan(input); err == nil {
			return nil
		} else {
			return map[string]any{
				"ok": false, "status": "generate_plan_contract_preflight_required", "executed": false,
				"message":  "The working plan does not satisfy its structured module contract: " + err.Error(),
				"recovery": "Return one corrected generate_plan call. Represent each substantive research output module as one research step with its research question, depth, and Chinese and English discovery queries; keep synthesis and delivery as later steps.",
			}
		}
	}
	return map[string]any{
		"ok": false, "status": "generate_plan_contract_preflight_required", "executed": false,
		"message":  "A new plan requires both task_summary and at least one structured phase before execution.",
		"recovery": "Return one corrected generate_plan call containing task_summary and phases. Use approve=true alone only when approving an existing durable plan.",
	}
}

func agentRuntimeUnresolvedSkillDirectoryPreflight(publicName string, input map[string]any) map[string]any {
	name := strings.ToLower(strings.TrimSpace(publicName))
	if name != "bash" && name != "python" && name != "r" && name != "repl" && name != "powershell" {
		return nil
	}
	content := strings.Join([]string{
		stringValue(input["command"]), stringValue(input["code"]), stringValue(input["script"]),
	}, "\n")
	unresolved := false
	for _, marker := range []string{
		"${SYNON_SKILL_DIR}", "$SYNON_SKILL_DIR/", `$SYNON_SKILL_DIR\`, "%SYNON_SKILL_DIR%",
	} {
		if strings.Contains(content, marker) {
			unresolved = true
			break
		}
	}
	if !unresolved {
		for _, token := range managedExecutionCommandTokens(content) {
			if strings.HasPrefix(filepath.ToSlash(strings.TrimSpace(token)), "/.synon/runtime/skills/") {
				unresolved = true
				break
			}
		}
	}
	if !unresolved {
		return nil
	}
	return map[string]any{
		"ok": false, "status": "skill_runtime_path_preflight_required", "executed": false,
		"message":  "The call contains an unresolved or non-task Skill runtime path, so it was rejected before execution.",
		"recovery": "Use the exact absolute Base directory for this skill from the rendered skill result, verify the documented script path once, then issue one corrected call. Do not guess a source-repository path and do not repeat the unresolved command.",
	}
}

func agentRuntimeUnresolvedToolResultTemplatePreflight(publicName string, input map[string]any) map[string]any {
	switch strings.ToLower(strings.TrimSpace(publicName)) {
	case "edit_file", "file_write", "write", "edit", "patch", "file_patch", "file_replace", "json_patch", "notebookedit":
	default:
		return nil
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	lower := strings.ToLower(string(raw))
	unresolved := false
	for _, marker := range []string{
		"{{result", "{{ result", "{{tool_result", "{{ tool_result", "${result.", "${tool_result.",
	} {
		if strings.Contains(lower, marker) {
			unresolved = true
			break
		}
	}
	if !unresolved {
		return nil
	}
	return map[string]any{
		"ok": false, "status": "unresolved_tool_result_preflight_required", "executed": false,
		"message":  "The file mutation contains an unresolved tool-result placeholder and was rejected before writing.",
		"recovery": "Read or materialize the exact prior result through its authoritative tool contract, then write validated literal bytes once. Do not save placeholder text or reconstruct large-result bytes through a second file path.",
	}
}

func retiredAgentRuntimeRequestedName(name string) bool {
	if _, ok := toolcontract.CanonicalRuntimeAlias(strings.TrimSpace(name)); ok {
		return true
	}
	switch strings.TrimSpace(name) {
	case "Agent", "TaskRun", "StructuredOutput", "VisualReview",
		"ToolSearch", "Python", "R", "Bash", "Operon",
		softwareRuntimeToolName, "download_rcsb_file", "search_rcsb_structures",
		"binding_mode_analysis":
		return true
	default:
		return false
	}
}

func agentRuntimeFailedToolValue(response any, err error) map[string]any {
	value := agentRuntimeToolErrorValue(err)
	object, ok := response.(map[string]any)
	if !ok {
		return value
	}
	rawErrors, found := object["errors"]
	if !found {
		return value
	}
	bounded := make([]string, 0, 16)
	appendError := func(item string) {
		item = strings.TrimSpace(item)
		if item != "" && len([]byte(item)) <= 4096 && len(bounded) < maxAgentSavedArtifacts {
			bounded = append(bounded, item)
		}
	}
	switch errorsList := rawErrors.(type) {
	case []string:
		for _, item := range errorsList {
			appendError(item)
		}
	case []any:
		for _, raw := range errorsList {
			if item, ok := raw.(string); ok {
				appendError(item)
			}
			if failureMap, ok := raw.(map[string]any); ok {
				if encoded, mErr := json.Marshal(failureMap); mErr == nil {
					appendError(string(encoded))
				}
			}
		}
	case []map[string]any:
		for _, failureMap := range errorsList {
			if encoded, mErr := json.Marshal(failureMap); mErr == nil {
				appendError(string(encoded))
			}
		}
	}
	if len(bounded) > 0 {
		value["details"] = map[string]any{"errors": bounded}
	}
	return value
}

// agentRuntimeRecoverableToolErrorValue preserves safe model-correctable
// preconditions and item-level save_artifacts validation as non-terminal tool
// results. A stale exact edit has no side effect and should ask the model to
// refresh current text, while genuine file IO errors remain hard failures.
func agentRuntimeRecoverableToolErrorValue(toolName string, response any, err error) (map[string]any, bool) {
	if strings.TrimSpace(toolName) == "read_file" &&
		(err.Error() == "workspace file path is outside the authorized workspace" ||
			err.Error() == "workspace file path is invalid") {
		return map[string]any{
			"ok":        false,
			"executed":  false,
			"status":    "workspace_file_scope_required",
			"code":      "workspace_file_scope",
			"retryable": true,
			"message":   "The requested absolute path is outside the task workspace.",
			"recovery":  "use_the_artifact_version_id_for_project_artifacts_or_a_task_relative_file_path; do_not_retry_the_absolute_path",
		}, true
	}
	if strings.TrimSpace(toolName) == "read_file" {
		const missingPrefix = "read_file file does not exist in task workspace:"
		if strings.HasPrefix(err.Error(), missingPrefix) {
			requestedPath := strings.TrimSpace(strings.TrimPrefix(err.Error(), missingPrefix))
			return map[string]any{
				"ok":             false,
				"executed":       false,
				"status":         "workspace_file_missing",
				"code":           "workspace_file_missing",
				"requested_path": requestedPath,
				"retryable":      true,
				"message":        "The requested task-scoped file is not present. Inspect current task outputs or use a successful artifact version_id; do not repeat the missing path unchanged.",
				"recovery":       "inspect_current_task_scoped_outputs_or_use_a_successful_version_id; do_not_retry_the_missing_path_unchanged",
			}, true
		}
	}
	if strings.TrimSpace(toolName) == "edit_file" &&
		(err.Error() == "edit_file old_string was not found" || err.Error() == "edit_file old_string must occur exactly once") {
		return agentRuntimeEditConflictValue(), true
	}
	if strings.TrimSpace(toolName) != "save_artifacts" || !errors.Is(err, errAgentSaveArtifactsNoResults) {
		return nil, false
	}
	value, ok := response.(map[string]any)
	if !ok || len(anySliceValue(value["errors"])) == 0 {
		return nil, false
	}
	return agentSaveArtifactsCorrectionValue(value), true
}

func agentRuntimeToolResponseStatus(value any) (string, string) {
	switch agentruntime.ClassifyToolResult(value) {
	case agentruntime.ToolResultSucceeded:
		return "completed", ""
	case agentruntime.ToolResultPartial:
		return "partial", "tool result was partially successful and is recoverable"
	case agentruntime.ToolResultUnavailable:
		return "unavailable", "tool result source was unavailable"
	default:
		return "failed", agentruntime.ToolFailureEventMessage(value)
	}
}

func agentRuntimeContextError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

func agentRuntimeContextStatus(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrGenerationStopped) {
		return "cancelled"
	}
	return "failed"
}
