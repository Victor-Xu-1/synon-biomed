package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/mcpdirectory"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

type kernelMCPTranscriptEvidenceAuthority struct {
	identity  kernelTranscriptExecutionIdentity
	stream    transcriptstore.Stream
	operation workspace.KernelLocalOperation
}

type kernelMCPResolution struct {
	connector       workspaceMCPRuntimeConnector
	runtime         workspaceMCPRuntimeContext
	runtimeSnapshot *mcpdirectory.RuntimeConnector
	executable      *mcpstdio.ServerConfig
	tool            mcpstdio.ToolProjection
}

type kernelMCPSourceEvidenceAttestation struct {
	Class             string
	ConnectorID       string
	ConnectorSource   string
	InputSchemaSHA256 string
	ReadOnlyHint      bool
}

func (s *Server) handleKernelMCPHostCall(
	ctx context.Context,
	bound kernelHostExecutionIdentity,
	current workspace.KernelFrameAccess,
	call kernelruntime.HostCall,
	args []any,
	kwargs map[string]any,
) (any, error) {
	return s.handleKernelMCPHostCallForAccess(ctx, bound, current.Frame.ID, current.UserID, call, args, kwargs)
}

func (s *Server) handleKernelMCPHostCallForAccess(
	ctx context.Context,
	bound kernelHostExecutionIdentity,
	frameID, ownerID string,
	call kernelruntime.HostCall,
	args []any,
	kwargs map[string]any,
) (result any, resultErr error) {
	if len(kwargs) != 0 || len(args) != 3 {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.mcp requires server, method, and one keyword object")
	}
	serverName, serverOK := args[0].(string)
	method, methodOK := args[1].(string)
	input, inputOK := args[2].(map[string]any)
	if args[2] == nil {
		input, inputOK = map[string]any{}, true
	}
	if !serverOK || strings.TrimSpace(serverName) == "" || !methodOK || strings.TrimSpace(method) == "" || !inputOK {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.mcp requires non-empty server and method strings plus a keyword object")
	}
	serverName = strings.TrimSpace(serverName)
	method = strings.TrimSpace(method)
	requestedMethod := method
	if s == nil || s.workspaceStore == nil {
		return nil, kernelruntime.NewHostCallError("audit_unavailable", "MCP audit store is unavailable")
	}
	auditInput := workspace.KernelMCPAuditInput{
		CallID: call.ID, FrameID: frameID, RootFrameID: bound.access.Frame.RootFrameID,
		OwnerUserID: ownerID, Server: serverName, Method: method, Input: copyMapAny(input),
	}
	auditEvent, err := s.workspaceStore.BeginKernelMCPAudit(ctx, auditInput)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("audit_unavailable", "MCP audit could not be persisted")
	}
	if err := s.publishWorkspaceEvent(auditEvent); err != nil {
		// The FrameEvent is already durable; the workspace projection can be
		// recovered independently and is not the audit authority.
	}
	auditCommitted := false
	defer func() {
		if auditCommitted {
			return
		}
		status, reason := kernelMCPAuditDisposition(resultErr)
		for attempt := 0; attempt < 3; attempt++ {
			auditCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			terminal, auditErr := s.workspaceStore.FinishKernelMCPAudit(auditCtx, workspace.KernelMCPAuditTerminalInput{
				KernelMCPAuditInput: auditInput, Status: status, Reason: reason, Result: result,
			})
			cancel()
			if auditErr == nil {
				if err := s.publishWorkspaceEvent(terminal); err != nil {
					// Durable audit authority already committed.
				}
				return
			}
			if attempt < 2 {
				time.Sleep(time.Duration(1<<attempt) * 100 * time.Millisecond)
			}
		}
		result = nil
		resultErr = kernelruntime.NewHostCallError("audit_unavailable", "MCP terminal audit could not be persisted")
	}()
	resolution, resolvedMethod, err := s.resolveKernelMCPHostTargetWithCompatibleMethod(ctx, frameID, ownerID, serverName, method, input)
	if err != nil {
		return nil, err
	}
	method = resolvedMethod
	if !kernelMCPToolAllowed(resolution.tool.Name, bound.allowedTools) {
		return nil, kernelruntime.NewHostCallError("permission_denied", "MCP tool is outside the agent tool boundary")
	}
	input = normalizeAgentRuntimeMCPArguments(resolution.tool.Name, resolution.tool.InputSchema, input)
	input = normalizeKernelMCPWorkspaceOutputPaths(input, bound.workspaceDir)
	validator, err := compileKernelMCPInputValidator(ctx, kernelMCPOracleInputSchema(resolution.tool.InputSchema))
	if err != nil {
		return nil, kernelMCPValidationError(err, resolution.tool.InputSchema)
	}
	if err := validator.Validate(input); err != nil {
		return nil, kernelMCPValidationError(err, resolution.tool.InputSchema)
	}
	encoded, _ := json.Marshal(input)
	toolCall := agentruntime.ToolCall{ID: call.ID, Name: resolution.tool.Name, Arguments: encoded}
	updated, hook := s.applyAgentRuntimePreToolHooks(ctx, resolution.tool.Name, toolCall, input)
	updated = normalizeAgentRuntimeMCPArguments(resolution.tool.Name, resolution.tool.InputSchema, updated)
	updated = normalizeKernelMCPWorkspaceOutputPaths(updated, bound.workspaceDir)
	auditInput.Input = copyMapAny(updated)
	if hook != nil {
		return nil, kernelruntime.NewHostCallError("policy_blocked", firstNonEmpty(stringValue(hook["error"]), "MCP tool was blocked by policy"))
	}
	if err := validator.Validate(updated); err != nil {
		return nil, kernelMCPValidationError(err, resolution.tool.InputSchema)
	}
	permission := s.kernelMCPPermissionResult(ctx, frameID, resolution, toolCall, updated)
	if permission != nil {
		decision := strings.TrimSpace(stringValue(permission["decision"]))
		if decision != "pending_approval" {
			return nil, kernelruntime.NewHostCallError("permission_denied", firstNonEmpty(stringValue(permission["error"]), "MCP tool permission was denied"))
		}
		approvalID := strings.TrimSpace(stringValue(permission["approvalId"]))
		if approvalID == "" {
			return nil, kernelruntime.NewHostCallError("approval_unavailable", "MCP approval request is unavailable")
		}
		if err := s.waitForKernelMCPApproval(ctx, approvalID); err != nil {
			return nil, err
		}
	}
	currentAccess, err := s.validateKernelHostIdentity(ctx, bound.access)
	if err != nil {
		return nil, err
	}
	if currentAccess.Frame.ID != frameID || currentAccess.UserID != ownerID {
		return nil, kernelruntime.NewHostCallError("permission_denied", "MCP frame authority changed before execution")
	}
	currentResolution, err := s.resolveKernelMCPHostTarget(ctx, frameID, ownerID, serverName, method)
	if err != nil {
		return nil, err
	}
	if !kernelMCPResolutionsEquivalent(currentResolution, resolution) {
		log.Printf("kernel MCP resolution changed before execution server=%q method=%q initial=%s current=%s", serverName, method, kernelMCPResolutionDebugSummary(resolution), kernelMCPResolutionDebugSummary(currentResolution))
		// Bundled read-only catalogs may be refreshed by the background warmup
		// flight between approval and execution. Stabilize that snapshot once
		// instead of turning a harmless catalog race into a failed scientific
		// task. Custom/directory connectors remain fail-closed because their
		// endpoint or credentials may have changed.
		if resolution.connector.Source != "bundled" || !resolution.tool.ReadOnlyHint {
			return nil, kernelruntime.NewHostCallError("connector_changed", "MCP connector authority changed before execution")
		}
		candidate := currentResolution
		stabilized := false
		for attempt := 0; attempt < 4; attempt++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			next, stabilizeErr := s.resolveKernelMCPHostTarget(ctx, frameID, ownerID, serverName, method)
			if stabilizeErr != nil {
				return nil, stabilizeErr
			}
			if kernelMCPResolutionsEquivalent(next, candidate) {
				currentResolution = next
				stabilized = true
				break
			}
			candidate = next
		}
		if !stabilized {
			log.Printf("kernel MCP bundled resolution did not stabilize server=%q method=%q candidate=%s", serverName, method, kernelMCPResolutionDebugSummary(candidate))
			return nil, kernelruntime.NewHostCallError("connector_changed", "MCP connector authority did not stabilize before execution")
		}
		validator, err = compileKernelMCPInputValidator(ctx, kernelMCPOracleInputSchema(currentResolution.tool.InputSchema))
		if err != nil {
			return nil, kernelMCPValidationError(err, currentResolution.tool.InputSchema)
		}
		updated = normalizeAgentRuntimeMCPArguments(currentResolution.tool.Name, currentResolution.tool.InputSchema, updated)
		updated = normalizeKernelMCPWorkspaceOutputPaths(updated, bound.workspaceDir)
		auditInput.Input = copyMapAny(updated)
		if err := validator.Validate(updated); err != nil {
			return nil, kernelMCPValidationError(err, currentResolution.tool.InputSchema)
		}
	}
	if !kernelMCPToolAllowed(currentResolution.tool.Name, bound.allowedTools) {
		return nil, kernelruntime.NewHostCallError("permission_denied", "MCP tool is outside the agent tool boundary")
	}
	if mode, explicit, policyErr := s.workspaceMCPRuntimeToolPolicy(ownerID, currentResolution.connector, currentResolution.tool.ToolName); policyErr != nil {
		return nil, kernelruntime.NewHostCallError("connector_error", "MCP connector policy is unavailable")
	} else if explicit && mode == "deny" {
		return nil, kernelruntime.NewHostCallError("permission_denied", "MCP tool permission was denied")
	}
	attestation, sourceEvidence := kernelMCPResolutionSourceEvidenceAttestation(currentResolution)
	var evidenceAuthority *kernelMCPTranscriptEvidenceAuthority
	if sourceEvidence {
		evidenceAuthority, err = s.kernelMCPTranscriptEvidenceAuthority(ctx, bound, currentAccess, call)
		if err != nil {
			return nil, err
		}
	}
	output, err := s.executeResolvedKernelMCPTool(ctx, currentResolution, updated)
	if errors.Is(err, mcpdirectory.ErrConnectorChanged) &&
		currentResolution.connector.Source == "bundled" && currentResolution.tool.ReadOnlyHint {
		// The resolved connector is checked once more inside the stdio call. A
		// bundled catalog refresh can land in that narrow window even after the
		// pre-call snapshot stabilized. Re-resolve the same owner-scoped target
		// and retry only a bounded number of times; never widen this to custom or
		// write-capable connectors.
		for attempt := 0; attempt < 4 && errors.Is(err, mcpdirectory.ErrConnectorChanged); attempt++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			candidate, resolveErr := s.resolveKernelMCPHostTarget(ctx, frameID, ownerID, serverName, method)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if candidate.connector.Source != "bundled" || !candidate.tool.ReadOnlyHint ||
				!kernelMCPToolAllowed(candidate.tool.Name, bound.allowedTools) {
				break
			}
			validator, err = compileKernelMCPInputValidator(ctx, kernelMCPOracleInputSchema(candidate.tool.InputSchema))
			if err != nil {
				return nil, kernelMCPValidationError(err, candidate.tool.InputSchema)
			}
			updated = normalizeAgentRuntimeMCPArguments(candidate.tool.Name, candidate.tool.InputSchema, updated)
			updated = normalizeKernelMCPWorkspaceOutputPaths(updated, bound.workspaceDir)
			auditInput.Input = copyMapAny(updated)
			if err = validator.Validate(updated); err != nil {
				return nil, kernelMCPValidationError(err, candidate.tool.InputSchema)
			}
			currentResolution = candidate
			resolution = candidate
			attestation, sourceEvidence = kernelMCPResolutionSourceEvidenceAttestation(currentResolution)
			if evidenceAuthority != nil && !sourceEvidence {
				break
			}
			output, err = s.executeResolvedKernelMCPTool(ctx, currentResolution, updated)
		}
	}
	if err != nil {
		var hostErr *kernelruntime.HostCallError
		if errors.As(err, &hostErr) {
			return nil, hostErr
		}
		if errors.Is(err, mcpdirectory.ErrConnectorChanged) {
			log.Printf("kernel MCP execution authority changed after retries server=%q method=%q resolution=%s", serverName, method, kernelMCPResolutionDebugSummary(currentResolution))
			return nil, kernelruntime.NewHostCallError("connector_changed", "MCP connector authority changed before execution")
		}
		return nil, kernelruntime.NewHostCallError("connector_error", boundedKernelMCPConnectorError(err))
	}
	response := map[string]any{"ok": true, "result": output}
	if requestedMethod != method {
		response["requested_method"] = requestedMethod
		response["resolved_method"] = method
	}
	s.auditAgentRuntimePostToolHooks(ctx, resolution.tool.Name, toolCall, updated, "completed", response)
	if evidenceAuthority != nil && kernelMCPStructuredEvidenceResult(output) {
		requestRaw, requestErr := json.Marshal(updated)
		resultRaw, resultErr := json.Marshal(output)
		if requestErr != nil || resultErr != nil {
			return nil, kernelruntime.NewHostCallError("audit_unavailable", "MCP evidence could not be encoded")
		}
		requestSHA := kernelMCPEvidenceSHA256(requestRaw)
		resultSHA := kernelMCPEvidenceSHA256(resultRaw)
		payload := map[string]any{
			"schema": "synon.kernel_mcp_evidence.v1", "status": "completed", "toolPhase": "completed",
			"toolName": currentResolution.tool.Name, "toolCallId": call.ID,
			"toolInput": updated, "toolResult": output,
			"outerToolCallId":   evidenceAuthority.operation.ToolCallID,
			"kernelOperationId": evidenceAuthority.operation.OperationID,
			"executionId":       evidenceAuthority.operation.ExecutionID,
			"hostCallId":        call.ID, "kernelId": evidenceAuthority.operation.KernelID,
			"kernelGeneration": evidenceAuthority.operation.KernelGeneration,
			"requestSha256":    requestSHA, "resultSha256": resultSHA,
			"evidenceClass": attestation.Class, "connectorId": attestation.ConnectorID,
			"connectorSource": attestation.ConnectorSource, "inputSchemaSha256": attestation.InputSchemaSHA256,
			"readOnlyHint": attestation.ReadOnlyHint,
		}
		terminalAudit := workspace.KernelMCPAuditTerminalInput{
			KernelMCPAuditInput: auditInput, Status: "completed", Result: output,
		}
		_, err = s.checkpointTranscriptRunnerEventWithDestinationsAndHook(
			ctx,
			&transcriptRunnerAuthority{Stream: evidenceAuthority.stream, Claim: evidenceAuthority.identity.RunnerClaim},
			transcriptstore.RunnerPhaseExecuting,
			"kernel-mcp-evidence-"+call.ID,
			payload,
			true,
			nil,
			func(commitCtx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
				_, commitErr := s.workspaceStore.CommitKernelMCPEvidenceTx(commitCtx, tx, event, workspace.KernelMCPEvidenceCommitInput{
					Audit: terminalAudit, OperationID: evidenceAuthority.operation.OperationID,
					OuterToolCallID: evidenceAuthority.operation.ToolCallID, HostCallID: call.ID,
					ExecutionID: evidenceAuthority.operation.ExecutionID, ToolName: currentResolution.tool.Name,
					KernelID: evidenceAuthority.operation.KernelID, KernelGeneration: evidenceAuthority.operation.KernelGeneration,
					Claim: evidenceAuthority.identity.RunnerClaim, RequestSHA256: requestSHA, ResultSHA256: resultSHA,
					EvidenceClass: attestation.Class, ConnectorID: attestation.ConnectorID,
					ConnectorSource: attestation.ConnectorSource, InputSchemaSHA256: attestation.InputSchemaSHA256,
					ReadOnlyHint: attestation.ReadOnlyHint,
				})
				return transcriptstore.RunnerCheckpointCommitReceipt{}, commitErr
			},
		)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("audit_unavailable", "MCP evidence could not be persisted")
		}
		s.bindKernelMCPTrustedScientificEvidenceForSession(
			ctx, evidenceAuthority.identity.FrameID, attestation, currentResolution.tool.Name, updated, output,
		)
		auditCommitted = true
	}
	return output, nil
}

func kernelMCPResolutionSourceEvidenceAttestation(resolution kernelMCPResolution) (kernelMCPSourceEvidenceAttestation, bool) {
	if !resolution.tool.ReadOnlyHint || resolution.connector.Source != "bundled" ||
		resolution.runtimeSnapshot == nil || resolution.executable == nil ||
		resolution.runtimeSnapshot.Source != "bundled" || resolution.runtimeSnapshot.ID != resolution.connector.ID ||
		resolution.runtimeSnapshot.Name != resolution.connector.Name || strings.TrimSpace(resolution.connector.ID) == "" ||
		!strings.HasPrefix(strings.ToLower(strings.TrimSpace(resolution.tool.Name)), "mcp__") {
		return kernelMCPSourceEvidenceAttestation{}, false
	}
	inputSchema, err := json.Marshal(resolution.tool.InputSchema)
	if err != nil || len(inputSchema) == 0 || string(inputSchema) == "null" {
		return kernelMCPSourceEvidenceAttestation{}, false
	}
	return kernelMCPSourceEvidenceAttestation{
		Class:       workspace.KernelMCPEvidenceClassBundledReadOnly,
		ConnectorID: resolution.connector.ID, ConnectorSource: resolution.connector.Source,
		InputSchemaSHA256: kernelMCPEvidenceSHA256(inputSchema), ReadOnlyHint: true,
	}, true
}

func kernelMCPResolutionsEquivalent(left, right kernelMCPResolution) bool {
	if !reflect.DeepEqual(left.connector, right.connector) ||
		!kernelMCPRuntimeSnapshotsEquivalent(left.runtimeSnapshot, right.runtimeSnapshot) {
		return false
	}
	executableEquivalent := reflect.DeepEqual(left.executable, right.executable)
	if left.runtimeSnapshot != nil && right.runtimeSnapshot != nil &&
		left.runtimeSnapshot.Source == "bundled" && right.runtimeSnapshot.Source == "bundled" {
		// The executable config is derived from the same bundled authority and
		// may carry a refreshed etiquette environment value. Its presence is
		// part of the resolution shape; its ephemeral fields are not.
		executableEquivalent = (left.executable == nil) == (right.executable == nil)
	}
	return executableEquivalent &&
		left.tool.Name == right.tool.Name &&
		left.tool.ReadOnlyHint == right.tool.ReadOnlyHint &&
		reflect.DeepEqual(left.tool.InputSchema, right.tool.InputSchema)
}

// kernelMCPToolProjectionCompatibleForExecution permits a bundled read-only
// connector to refresh its live JSON schema between catalog discovery and the
// single initialized tools/list/tools/call session. The live schema is still
// validated below before tools/call; custom and directory connectors remain
// exact-match fail-closed because their authority is user-controlled.
func kernelMCPToolProjectionCompatibleForExecution(
	connectorSource string,
	resolved, live mcpstdio.ToolProjection,
) bool {
	if resolved.ToolName != live.ToolName || resolved.Name != live.Name ||
		resolved.ReadOnlyHint != live.ReadOnlyHint {
		return false
	}
	if reflect.DeepEqual(resolved.InputSchema, live.InputSchema) {
		return true
	}
	return connectorSource == "bundled" && resolved.ReadOnlyHint && live.ReadOnlyHint
}

func kernelMCPRuntimeSnapshotsEquivalent(left, right *mcpdirectory.RuntimeConnector) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return mcpdirectory.RuntimeConnectorAuthorityEquivalent(*left, *right)
}

func kernelMCPResolutionDebugSummary(resolution kernelMCPResolution) string {
	snapshotID, snapshotSource, snapshotName, snapshotDisabled := "", "", "", false
	if resolution.runtimeSnapshot != nil {
		snapshotID = resolution.runtimeSnapshot.ID
		snapshotSource = resolution.runtimeSnapshot.Source
		snapshotName = resolution.runtimeSnapshot.Name
		snapshotDisabled = resolution.runtimeSnapshot.Config.Disabled
	}
	schemaDigest := ""
	if raw, err := json.Marshal(resolution.tool.InputSchema); err == nil {
		schemaDigest = kernelMCPEvidenceSHA256(raw)
	}
	return "{" +
		"connector_id=" + resolution.connector.ID +
		" connector_source=" + resolution.connector.Source +
		" connector_name=" + resolution.connector.Name +
		" connector_enabled=" + boolString(resolution.connector.Enabled) +
		" snapshot_id=" + snapshotID +
		" snapshot_source=" + snapshotSource +
		" snapshot_name=" + snapshotName +
		" snapshot_disabled=" + boolString(snapshotDisabled) +
		" tool=" + resolution.tool.ToolName +
		" tool_name=" + resolution.tool.Name +
		" readonly=" + boolString(resolution.tool.ReadOnlyHint) +
		" schema_sha256=" + schemaDigest +
		" executable=" + boolString(resolution.executable != nil) + "}"
}

func kernelMCPToolDebugSummary(tool mcpstdio.ToolProjection) string {
	schemaDigest := ""
	if raw, err := json.Marshal(tool.InputSchema); err == nil {
		schemaDigest = kernelMCPEvidenceSHA256(raw)
	}
	return "{tool=" + tool.ToolName + " name=" + tool.Name + " readonly=" + boolString(tool.ReadOnlyHint) + " schema_sha256=" + schemaDigest + "}"
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func kernelMCPStructuredEvidenceResult(value any) bool {
	if encoded, ok := value.(string); ok {
		encoded = strings.TrimSpace(encoded)
		if encoded == "" || !json.Valid([]byte(encoded)) {
			return false
		}
		var structured any
		if json.Unmarshal([]byte(encoded), &structured) != nil {
			return false
		}
		switch structured.(type) {
		case map[string]any, []any:
			return true
		default:
			return false
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil || !json.Valid(encoded) {
		return false
	}
	var structured any
	if json.Unmarshal(encoded, &structured) != nil {
		return false
	}
	switch structured.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

func (s *Server) kernelMCPTranscriptEvidenceAuthority(
	ctx context.Context,
	bound kernelHostExecutionIdentity,
	current workspace.KernelFrameAccess,
	call kernelruntime.HostCall,
) (*kernelMCPTranscriptEvidenceAuthority, error) {
	binding := bound.transcriptExecution
	if binding.stream.UID == "" && binding.claim.StreamUID == "" && binding.outerToolUseID == "" && binding.operationID == "" {
		return nil, nil
	}
	if binding.operationID == "" {
		return nil, kernelruntime.NewHostCallError("permission_denied", "MCP transcript operation authority is unavailable")
	}
	identity, err := s.validateKernelTranscriptExecution(ctx, bound, current)
	if err != nil {
		return nil, err
	}
	operation, found, err := s.workspaceStore.GetKernelLocalOperation(ctx, identity.OwnerID, binding.operationID)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("storage_error", "MCP transcript operation could not be verified")
	}
	if !found || operation.StreamUID != identity.StreamUID || operation.ToolCallID != identity.OuterToolUseID ||
		operation.Tool != "repl" || operation.State != workspace.KernelLocalOperationStateStarted ||
		operation.ExecutionID == "" || operation.ExecutionID != call.CellID ||
		operation.KernelID != identity.KernelID || operation.KernelGeneration != int64(identity.KernelGeneration) ||
		operation.RunnerID != identity.RunnerID || operation.RunnerAttempt != identity.RunnerAttempt {
		return nil, kernelruntime.NewHostCallError("permission_denied", "MCP transcript execution authority does not match the live kernel")
	}
	return &kernelMCPTranscriptEvidenceAuthority{identity: identity, stream: binding.stream, operation: operation}, nil
}

func kernelMCPEvidenceSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func kernelMCPAuditDisposition(err error) (string, string) {
	if err == nil {
		return "completed", ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled", "context_cancelled"
	}
	var typed *kernelruntime.HostCallError
	if !errors.As(err, &typed) {
		return "failed", "internal"
	}
	switch typed.Code {
	case "invalid_arguments", "permission_denied", "policy_blocked", "unknown_server", "unknown_method", "ambiguous_server":
		return "blocked", typed.Code
	default:
		return "failed", typed.Code
	}
}

func (s *Server) executeResolvedKernelMCPTool(ctx context.Context, resolution kernelMCPResolution, input map[string]any) (string, error) {
	arguments := workspaceMCPToolArguments(resolution.tool.Name, input)
	inspect := func(tool mcpstdio.ToolProjection) error {
		if !kernelMCPToolProjectionCompatibleForExecution(resolution.connector.Source, resolution.tool, tool) {
			log.Printf("kernel MCP live tool projection changed connector=%q resolved=%s live=%s", resolution.connector.ID, kernelMCPToolDebugSummary(resolution.tool), kernelMCPToolDebugSummary(tool))
			return mcpdirectory.ErrConnectorChanged
		}
		validator, err := compileKernelMCPInputValidator(ctx, kernelMCPOracleInputSchema(tool.InputSchema))
		if err != nil {
			return err
		}
		if err := validator.Validate(input); err != nil {
			return kernelMCPValidationError(err, tool.InputSchema)
		}
		return nil
	}
	callOnce := func(parent context.Context) (string, error) {
		callCtx, cancel := context.WithTimeout(parent, 60*time.Second)
		defer cancel()
		if resolution.connector.Source == "custom" {
			if resolution.executable == nil {
				return "", errors.New("custom MCP connector configuration is unavailable")
			}
			config := *resolution.executable
			if strings.TrimSpace(config.URL) != "" {
				client, err := mcpdirectory.SecureHTTPClient(callCtx, config.URL, s.httpClient)
				if err != nil {
					return "", err
				}
				callCtx = mcpstdio.WithHTTPClient(callCtx, client)
			}
			output, callErr := mcpstdio.InspectAndCallToolForServer(callCtx, s.fileRoot, resolution.connector.Name, resolution.tool.ToolName, arguments, config, inspect)
			s.recordMCPConnectorInvocation(callCtx, resolution.runtime.UserID, resolution.connector.ID, resolution.connector.Source, resolution.connector.Name, resolution.tool.ToolName, callErr)
			return output, callErr
		}
		if s.mcpDirectory == nil || resolution.runtimeSnapshot == nil {
			return "", errors.New("MCP connector runtime snapshot is unavailable")
		}
		return s.mcpDirectory.InspectAndCallResolvedConnectorTool(callCtx, resolution.runtime.UserID, *resolution.runtimeSnapshot, resolution.tool.ToolName, arguments, inspect)
	}
	output, err := callOnce(ctx)
	if err == nil || !resolution.tool.ReadOnlyHint || ctx.Err() != nil || !errors.Is(err, context.DeadlineExceeded) {
		return output, err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(500 * time.Millisecond):
	}
	return callOnce(ctx)
}

func kernelMCPToolAllowed(toolName string, allowed map[string]struct{}) bool {
	if len(allowed) == 0 {
		return true
	}
	if _, ok := allowed[toolName]; ok {
		return true
	}
	if _, ok := allowed["mcp"]; ok {
		return true
	}
	_, generic := allowed["MCPTool"]
	return generic
}

func (s *Server) resolveKernelMCPHostTarget(
	ctx context.Context,
	frameID, ownerID, serverName, method string,
) (kernelMCPResolution, error) {
	serverName = strings.TrimSpace(serverName)
	runtimeContext, found, err := s.workspaceMCPRuntimeContextWithContext(ctx, frameID)
	if err != nil {
		return kernelMCPResolution{}, kernelruntime.NewHostCallError("connector_error", "MCP connector inventory is unavailable")
	}
	if !found || runtimeContext.UserID != ownerID {
		return kernelMCPResolution{}, kernelruntime.NewHostCallError("permission_denied", "MCP connector owner is not authorized")
	}
	candidates := kernelMCPConnectorMatches(runtimeContext.Connectors, serverName)
	if len(candidates) == 0 && kernelMCPBioAlias(serverName) {
		for _, connector := range runtimeContext.Connectors {
			if connector.Enabled && connector.Source == "bundled" {
				candidates = append(candidates, connector)
			}
		}
	}
	if len(candidates) == 0 {
		return kernelMCPResolution{}, kernelruntime.NewHostCallError("unknown_server", "MCP server is unknown or not connected")
	}
	matches := make([]kernelMCPResolution, 0, 1)
	for _, connector := range candidates {
		if runtimeContext.workspaceMCPToolExcluded(connector.ID, method) {
			continue
		}
		tools, listErr := s.workspaceMCPRuntimeConnectorToolsStable(ctx, runtimeContext.UserID, connector)
		if listErr != nil {
			continue
		}
		for _, tool := range tools {
			if tool.ToolName == method {
				match := kernelMCPResolution{connector: connector, runtime: runtimeContext, tool: tool}
				if connector.Source == "custom" {
					if connector.Custom == nil {
						continue
					}
					config, configErr := s.runtimeMCPConfig(*connector.Custom)
					if configErr != nil {
						continue
					}
					match.executable = &config
				} else {
					if s.mcpDirectory == nil {
						continue
					}
					snapshot, found, resolveErr := s.mcpDirectory.ResolveRuntimeConnector(ctx, runtimeContext.UserID, connector.ID)
					if resolveErr != nil || !found || snapshot.Source != connector.Source || snapshot.Name != connector.Name || snapshot.Config.Disabled {
						continue
					}
					match.runtimeSnapshot = &snapshot
					config := snapshot.Config
					match.executable = &config
				}
				matches = append(matches, match)
			}
		}
	}
	if len(matches) == 0 {
		return kernelMCPResolution{}, kernelruntime.NewHostCallError("unknown_method", "MCP method is unknown for the selected server")
	}
	if len(matches) != 1 {
		return kernelMCPResolution{}, kernelruntime.NewHostCallError("ambiguous_server", "MCP server or method selection is ambiguous")
	}
	return matches[0], nil
}
func canonicalKernelMCPServerName(name string) string {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "bundled:") {
		return strings.TrimSpace(name[len("bundled:"):])
	}
	return name
}

func kernelMCPConnectorMatches(connectors []workspaceMCPRuntimeConnector, name string) []workspaceMCPRuntimeConnector {
	exactID := make([]workspaceMCPRuntimeConnector, 0, 1)
	for _, connector := range connectors {
		if connector.Enabled && connector.ID == name {
			exactID = append(exactID, connector)
		}
	}
	if len(exactID) > 0 {
		return exactID
	}
	exactName := make([]workspaceMCPRuntimeConnector, 0, 1)
	for _, connector := range connectors {
		if connector.Enabled && connector.Name == name {
			exactName = append(exactName, connector)
		}
	}
	if len(exactName) > 0 {
		return exactName
	}
	normalized := mcpstdio.NormalizeName(name)
	result := make([]workspaceMCPRuntimeConnector, 0, 1)
	for _, connector := range connectors {
		if connector.Enabled && (mcpstdio.NormalizeName(connector.ID) == normalized || mcpstdio.NormalizeName(connector.Name) == normalized || mcpstdio.NormalizeName(canonicalKernelMCPServerName(connector.ID)) == normalized) {
			result = append(result, connector)
		}
	}
	return result
}

func kernelMCPBioAlias(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "bio" || name == "bundled:bio"
}

func (s *Server) kernelMCPPermissionResult(ctx context.Context, frameID string, resolution kernelMCPResolution, call agentruntime.ToolCall, input map[string]any) map[string]any {
	// Publish the complete immutable authority with the initial pending record.
	// A later Get/Set annotation can overwrite an immediate user decision with
	// an old pending snapshot, stranding the live host call after approval.
	return s.agentRuntimePermissionResultForSessionAndSourceWithContext(
		ctx, frameID, resolution.tool.Name, call, input, "kernel-host-mcp", map[string]any{
			"frameId": frameID, "mcpServerId": resolution.connector.ID,
			"mcpServer": resolution.connector.Name, "mcpTool": resolution.tool.ToolName, "kernelKind": "operon",
		},
	)
}

func (s *Server) waitForKernelMCPApproval(ctx context.Context, approvalID string) error {
	return s.waitForKernelHostApproval(ctx, approvalID, "MCP")
}

func kernelMCPValidationError(err error, schema map[string]any) error {
	message := "MCP input validation failed"
	var validation *jsonschema.ValidationError
	if errors.As(err, &validation) {
		detail := boundedKernelMCPErrorDetail(validation.Error(), 800)
		if detail != "" {
			message += ": " + detail
		}
	}
	if contract := kernelMCPInputContractSummary(schema); contract != "" {
		message += ". " + contract
	}
	return kernelruntime.NewHostCallError("invalid_arguments", message)
}

func kernelMCPInputContractSummary(schema map[string]any) string {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return "Expected an empty keyword object"
	}
	allowed := make([]string, 0, len(properties))
	for name := range properties {
		if value := strings.TrimSpace(name); value != "" {
			allowed = append(allowed, value)
		}
	}
	sort.Strings(allowed)
	if len(allowed) > 24 {
		allowed = append(allowed[:24], "...")
	}
	required := stringArrayValue(schema["required"])
	sort.Strings(required)
	message := "Allowed keys: " + strings.Join(allowed, ", ")
	if len(required) > 0 {
		message += "; required keys: " + strings.Join(required, ", ")
	}
	return message
}

func boundedKernelMCPConnectorError(err error) string {
	if err == nil {
		return "MCP connector call failed"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "MCP connector call timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "MCP connector call was cancelled"
	}
	detail := boundedKernelMCPErrorDetail(err.Error(), 800)
	if detail == "" {
		return "MCP connector call failed"
	}
	return "MCP connector call failed: " + detail
}

func boundedKernelMCPErrorDetail(value string, limit int) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	detail := ""
	for index := len(lines) - 1; index >= 0; index-- {
		candidate := strings.Join(strings.Fields(lines[index]), " ")
		if candidate != "" {
			detail = candidate
			break
		}
	}
	if detail == "" {
		return ""
	}
	detail = redactFeedbackString(detail)
	characters := []rune(detail)
	if limit > 0 && len(characters) > limit {
		detail = string(characters[:limit]) + "..."
	}
	return detail
}

type kernelMCPInputValidator struct {
	schema *jsonschema.Schema
}

func kernelMCPOracleInputSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	normalized := make(map[string]any, len(schema)+1)
	for key, value := range schema {
		normalized[key] = value
	}
	if properties, ok := normalized["properties"].(map[string]any); ok && len(properties) > 0 {
		if _, declared := normalized["additionalProperties"]; !declared {
			// MCP method arguments are named contracts. Bundled generators such
			// as Pydantic omit this keyword while their implementation silently
			// drops unknown fields; fail closed at the shared host/direct schema
			// boundary so a model cannot believe an ignored scientific filter ran.
			normalized["additionalProperties"] = false
		}
	}
	return normalized
}

func compileKernelMCPInputValidator(_ context.Context, schema map[string]any) (*kernelMCPInputValidator, error) {
	schema = kernelMCPOracleInputSchema(schema)
	compiled, err := compileKernelDraft7Schema(schema)
	if err != nil {
		return &kernelMCPInputValidator{}, nil
	}
	return &kernelMCPInputValidator{schema: compiled}, nil
}

func compileKernelDraft7Schema(schema map[string]any) (*jsonschema.Schema, error) {
	if dialect, ok := schema["$schema"].(string); ok && strings.TrimSpace(dialect) != "" {
		normalized := strings.TrimSpace(dialect)
		if normalized != "http://json-schema.org/draft-07/schema#" && normalized != "http://json-schema.org/draft-07/schema" {
			return nil, errors.New("JSON Schema dialect is unsupported")
		}
	}
	rawSchema, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	normalizedSchema, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	compiler.UseLoader(kernelMCPDenySchemaLoader{})
	compiler.UseRegexpEngine(compileKernelMCPECMARegexp())
	if err := compiler.AddResource("kernel-mcp-schema.json", normalizedSchema); err != nil {
		return nil, err
	}
	compiled, err := compiler.Compile("kernel-mcp-schema.json")
	if err != nil {
		return nil, err
	}
	return compiled, nil
}

func (v *kernelMCPInputValidator) Validate(input map[string]any) error {
	if v == nil || v.schema == nil {
		return nil
	}
	rawInput, err := json.Marshal(input)
	if err != nil {
		return errors.New("MCP tool input is not valid JSON")
	}
	normalizedInput, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawInput))
	if err != nil {
		return errors.New("MCP tool input is not valid JSON")
	}
	return v.schema.Validate(normalizedInput)
}

func validateKernelMCPInput(schema map[string]any, input map[string]any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	validator, err := compileKernelMCPInputValidator(ctx, schema)
	if err != nil {
		return err
	}
	return validator.Validate(input)
}

type kernelMCPDenySchemaLoader struct{}

func (kernelMCPDenySchemaLoader) Load(string) (any, error) {
	return nil, errors.New("external MCP schema references are not allowed")
}

type kernelMCPECMARegexp struct {
	expression *regexp2.Regexp
}

func (expression *kernelMCPECMARegexp) MatchString(value string) bool {
	matched, err := expression.expression.MatchString(value)
	return err == nil && matched
}

func (expression *kernelMCPECMARegexp) String() string {
	return expression.expression.String()
}

func compileKernelMCPECMARegexp() jsonschema.RegexpEngine {
	return func(pattern string) (jsonschema.Regexp, error) {
		expression, err := regexp2.Compile(pattern, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		return &kernelMCPECMARegexp{expression: expression}, nil
	}
}
