package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type kernelLocalExecResolveAuthority struct {
	item      map[string]any
	decision  *workspace.KernelLocalExecApprovalDecision
	operation *workspace.KernelLocalOperation
}

type kernelLocalExecWaiterAuthority struct {
	OwnerUserID      string
	ProjectID        string
	FrameID          string
	FrameIncarnation string
	RootFrameID      string
	RootIncarnation  string
	StreamUID        string
	RunnerID         string
	RunnerAttempt    int64
	KernelID         string
	KernelGeneration uint64
}

func (s *Server) resolveKernelLocalExecApprovalInputs(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
	pendingByID map[string]map[string]any,
	responses []compatibilityInputResponse,
) (compatibilityResolveInputResult, bool, error) {
	if s == nil || s.workspaceStore == nil {
		return compatibilityResolveInputResult{}, false, nil
	}
	ownerID, ownerFound, err := s.workspaceStore.ProjectOwnerIDContext(ctx, frame.ProjectID)
	if err != nil {
		return compatibilityResolveInputResult{}, true, err
	}
	if !ownerFound {
		return compatibilityResolveInputResult{}, true, errors.New("kernel local execution approval owner is unavailable")
	}
	authorities := make([]kernelLocalExecResolveAuthority, len(responses))
	localCount := 0
	for index, response := range responses {
		id := strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID))
		if item := pendingByID[id]; strings.EqualFold(strings.TrimSpace(stringValue(item["kind"])), "local_exec") {
			if numberValue(item["version"]) == 2 {
				operation, found, err := s.workspaceStore.GetKernelLocalOperationByApprovalRequest(ctx, ownerID, id)
				if err != nil {
					return compatibilityResolveInputResult{}, true, err
				}
				if !found || operation.OperationID != strings.TrimSpace(stringValue(item["operation_id"])) {
					return compatibilityResolveInputResult{}, true, resolveInputRequestError(
						http.StatusConflict, "Kernel local operation approval conflicts with durable state.",
					)
				}
				authorities[index].operation = &operation
			} else {
				authorities[index].item = item
			}
			localCount++
			continue
		}
		if id == "" {
			continue
		}
		operation, operationFound, err := s.workspaceStore.GetKernelLocalOperationByApprovalRequest(ctx, ownerID, id)
		if err != nil {
			return compatibilityResolveInputResult{}, true, err
		}
		if operationFound {
			authorities[index].operation = &operation
			localCount++
			continue
		}
		decision, found, err := s.workspaceStore.GetKernelLocalExecApprovalDecision(
			ctx, ownerID, frame.ProjectID, frame.ID, frame.IncarnationID, id,
		)
		if err != nil {
			return compatibilityResolveInputResult{}, true, err
		}
		if found {
			authorities[index].decision = &decision
			localCount++
		}
	}
	if localCount == 0 {
		return compatibilityResolveInputResult{}, false, nil
	}
	if localCount != len(responses) {
		return compatibilityResolveInputResult{}, true, resolveInputRequestError(
			http.StatusBadRequest, "Local execution approvals cannot be mixed with other input responses.",
		)
	}
	seen := make(map[string]bool, len(responses))
	resolvedIDs := make([]string, 0, len(responses))
	for index, response := range responses {
		id := strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID))
		if id == "" {
			return compatibilityResolveInputResult{}, true, resolveInputRequestError(
				http.StatusBadRequest, "InputResponse requires tool_id or requestId",
			)
		}
		if seen[id] {
			return compatibilityResolveInputResult{}, true, resolveInputRequestError(
				http.StatusBadRequest, "Duplicate tool_id in batch: "+id+".",
			)
		}
		seen[id] = true
		if strings.TrimSpace(response.Mode) != "" || strings.TrimSpace(response.Redirect) != "" ||
			response.Text != nil || len(response.Answers) != 0 || strings.TrimSpace(response.Message) != "" {
			return compatibilityResolveInputResult{}, true, resolveInputRequestError(
				http.StatusBadRequest, "Local execution approval accepts only action, approved, and scope.",
			)
		}
		_, approved, scope, _, err := compatibilityApprovalResolution(response)
		if err != nil {
			return compatibilityResolveInputResult{}, true, resolveInputRequestError(http.StatusBadRequest, err.Error())
		}
		if !approved {
			scope = "once"
		}
		authority := authorities[index]
		if authority.operation != nil {
			operation := *authority.operation
			if !kernelLocalOperationApprovalScopeAllowed(operation.Tool, scope) {
				return compatibilityResolveInputResult{}, true, resolveInputRequestError(
					http.StatusBadRequest,
					"Software runtime approval is per operation and accepts only the once scope.",
				)
			}
			if operation.ProjectID != frame.ProjectID || operation.FrameID != frame.ID ||
				operation.FrameIncarnationID != frame.IncarnationID || operation.RootFrameID != frame.RootFrameID ||
				operation.ApprovalRequestID != id {
				return compatibilityResolveInputResult{}, true, resolveInputRequestError(
					http.StatusConflict, "Kernel local operation approval conflicts with durable state.",
				)
			}
			wantDecision := "deny"
			if approved {
				wantDecision = "allow"
			}
			if operation.State != workspace.KernelLocalOperationStatePendingApproval {
				if operation.ApprovalDecision != wantDecision || operation.ApprovalScope != scope {
					return compatibilityResolveInputResult{}, true, resolveInputRequestError(
						http.StatusConflict, "Kernel local operation approval conflicts with durable state.",
					)
				}
				resolvedIDs = append(resolvedIDs, id)
				continue
			}
			reasonCode := ""
			if !approved {
				reasonCode = "approval_denied"
			}
			auditAuthority := compatibilityApprovalAuthorityFromContext(ctx, ownerID)
			resolved, err := s.workspaceStore.ResolveKernelLocalOperationApproval(ctx,
				workspace.ResolveKernelLocalOperationApprovalInput{
					OwnerUserID: ownerID, OperationID: operation.OperationID,
					ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: id,
					Approved: approved, DecisionID: kernelLocalOperationDecisionID(
						ownerID, operation.OperationID, id, approved, scope,
					), Scope: scope, Source: auditAuthority.Source, ActorID: auditAuthority.ActorID, ReasonCode: reasonCode,
					AdmitRunnerRevision: true,
				})
			if err != nil {
				return compatibilityResolveInputResult{}, true, kernelLocalExecResolveError(err)
			}
			if resolved.ApprovalDecision != wantDecision || resolved.ApprovalScope != scope {
				return compatibilityResolveInputResult{}, true, resolveInputRequestError(
					http.StatusConflict, "Kernel local operation approval conflicts with durable state.",
				)
			}
			if err := s.wakeFrameResumeDispatchAfterKernelTransition(ctx, resolved); err != nil {
				return compatibilityResolveInputResult{}, true, err
			}
			resolvedIDs = append(resolvedIDs, id)
			continue
		}
		if authority.decision != nil {
			if authority.decision.Approved != approved || authority.decision.Scope != scope {
				return compatibilityResolveInputResult{}, true, resolveInputRequestError(
					http.StatusConflict, "Kernel local execution approval conflicts with durable state.",
				)
			}
			resolvedIDs = append(resolvedIDs, id)
			continue
		}
		resolution, err := kernelLocalExecResolutionFromPending(ownerID, frame, authority.item, approved, scope)
		if err != nil {
			return compatibilityResolveInputResult{}, true, resolveInputRequestError(http.StatusConflict, err.Error())
		}
		if approved && !s.kernelLocalExecWaiterIsActive(resolution) {
			return compatibilityResolveInputResult{}, true, resolveInputRequestError(
				http.StatusConflict, "Kernel local execution approval has no active execution waiter.",
			)
		}
		decision, event, created, err := s.workspaceStore.ResolveKernelLocalExecApproval(ctx, resolution)
		if err != nil {
			return compatibilityResolveInputResult{}, true, kernelLocalExecResolveError(err)
		}
		if decision.Approved != approved || decision.Scope != scope {
			return compatibilityResolveInputResult{}, true, resolveInputRequestError(
				http.StatusConflict, "Kernel local execution approval conflicts with durable state.",
			)
		}
		if created {
			if err := s.publishWorkspaceEvent(event); err != nil {
				return compatibilityResolveInputResult{}, true, err
			}
		}
		resolvedIDs = append(resolvedIDs, id)
	}
	metadata, _, err := s.workspaceStore.GetFrameRuntimeMetadata(frame.ID)
	if err != nil {
		return compatibilityResolveInputResult{}, true, err
	}
	remaining := make([]string, 0)
	for _, item := range compatibilityServerPendingInputs(metadata.ContextData) {
		if id := compatibilityServerPendingInputID(item); id != "" {
			remaining = append(remaining, id)
		}
	}
	if err := s.publishWebConfirmationRemovals(frame.ID, resolvedIDs, frame.Status); err != nil {
		return compatibilityResolveInputResult{}, true, err
	}
	return compatibilityResolveInputResult{Frame: frame, Status: frame.Status, RemainingIDs: remaining}, true, nil
}

// wakeFrameResumeDispatchAfterKernelTransition makes a parked frame resume
// dispatch claimable as soon as its durable kernel operation changes from a
// waiting state to one the original tool call can consume. This single wake is
// shared by approval decisions and detached terminal settlement; it never
// creates or executes a second tool path.
func (s *Server) wakeFrameResumeDispatchAfterKernelTransition(
	ctx context.Context,
	operation workspace.KernelLocalOperation,
) error {
	return s.wakeFrameResumeDispatchForFrame(ctx, operation.FrameID)
}

func (s *Server) wakeFrameResumeDispatchForFrame(ctx context.Context, frameID string) error {
	if s == nil || s.workspaceStore == nil {
		return nil
	}
	dispatch, found, err := s.workspaceStore.GetCompatibilityFrameResumeDispatchByFrame(strings.TrimSpace(frameID))
	if err != nil || !found {
		return err
	}
	event, changed, err := s.workspaceStore.WakeCompatibilityFrameResumeDispatch(dispatch.ResumeEvent.ID)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return s.publishWorkspaceEvent(event)
}

// WakeFrameResumeDispatchAfterKernelSettlement bridges the committed kernel
// settlement to the single resume dispatcher. The outbox transaction remains
// terminal authority; this only clears the lost-wake backstop after commit.
func (s *Server) WakeFrameResumeDispatchAfterKernelSettlement(ctx context.Context, event workspace.OutboxEvent) error {
	settlement, err := workspace.DecodeKernelBackgroundSettlement(event)
	if err != nil {
		return err
	}
	if strings.TrimSpace(settlement.OperationID) == "" {
		return nil
	}
	return s.wakeFrameResumeDispatchForFrame(ctx, settlement.FrameID)
}

func kernelLocalExecResolveError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, workspace.ErrKernelLocalExecApprovalConflict) ||
		errors.Is(err, workspace.ErrKernelLocalExecApprovalStale) ||
		errors.Is(err, workspace.ErrKernelLocalExecApprovalUnavailable) ||
		errors.Is(err, workspace.ErrKernelLocalExecApprovalRunnerLive) ||
		errors.Is(err, workspace.ErrKernelLocalOperationConflict) ||
		errors.Is(err, workspace.ErrKernelLocalOperationStale) {
		return resolveInputRequestError(http.StatusConflict, "Kernel local execution approval authority changed.")
	}
	return err
}

func kernelLocalOperationDecisionID(
	ownerUserID, operationID, approvalRequestID string,
	approved bool,
	scope string,
) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"synon.kernel-operation-decision.v1", strings.TrimSpace(ownerUserID),
		strings.TrimSpace(operationID), strings.TrimSpace(approvalRequestID),
		strconv.FormatBool(approved), strings.TrimSpace(scope),
	}, "\x00")))
	return "kop_decision_" + hex.EncodeToString(digest[:])
}

func kernelLocalExecResolutionFromPending(
	ownerID string,
	frame workspace.CompatibilityFrame,
	item map[string]any,
	approved bool,
	scope string,
) (workspace.KernelLocalExecApprovalResolutionInput, error) {
	if numberValue(item["version"]) != 1 || strings.TrimSpace(stringValue(item["kind"])) != "local_exec" {
		return workspace.KernelLocalExecApprovalResolutionInput{}, errors.New("Kernel local execution approval request is invalid.")
	}
	generation, err := strconv.ParseUint(strings.TrimSpace(stringValue(item["kernel_generation"])), 10, 64)
	if err != nil || generation == 0 {
		return workspace.KernelLocalExecApprovalResolutionInput{}, errors.New("Kernel local execution approval generation is invalid.")
	}
	resolution := workspace.KernelLocalExecApprovalResolutionInput{
		OwnerUserID: ownerID, ProjectID: frame.ProjectID, FrameID: frame.ID,
		FrameIncarnationID: strings.TrimSpace(stringValue(item["frame_incarnation_id"])),
		RootFrameID:        strings.TrimSpace(stringValue(item["root_frame_id"])),
		RootIncarnationID:  strings.TrimSpace(stringValue(item["root_frame_incarnation_id"])),
		RequestID:          strings.TrimSpace(firstNonEmpty(stringValue(item["requestId"]), stringValue(item["request_id"]))),
		Tool:               strings.ToLower(strings.TrimSpace(stringValue(item["tool"]))),
		Environment:        strings.TrimSpace(stringValue(item["environment"])),
		InputSHA256:        strings.ToLower(strings.TrimSpace(stringValue(item["input_sha256"]))),
		StreamUID:          strings.TrimSpace(stringValue(item["stream_uid"])),
		RunnerID:           strings.TrimSpace(stringValue(item["runner_id"])),
		RunnerAttempt:      numberValue(item["runner_attempt"]),
		KernelID:           strings.TrimSpace(stringValue(item["kernel_id"])), ExpectedGeneration: generation,
		Approved: approved, Scope: scope,
	}
	if resolution.FrameIncarnationID != frame.IncarnationID || resolution.RootFrameID != frame.RootFrameID ||
		resolution.RequestID == "" || resolution.StreamUID == "" || resolution.RunnerID == "" ||
		resolution.RunnerAttempt <= 0 || resolution.KernelID == "" {
		return workspace.KernelLocalExecApprovalResolutionInput{}, errors.New("Kernel local execution approval authority is incomplete.")
	}
	return resolution, nil
}

func (s *Server) registerKernelLocalExecWaiter(
	requestID string,
	authority kernelLocalExecWaiterAuthority,
) error {
	if s == nil || strings.TrimSpace(requestID) == "" {
		return errors.New("kernel local execution waiter authority is unavailable")
	}
	s.kernelLocalExecMu.Lock()
	defer s.kernelLocalExecMu.Unlock()
	if s.kernelLocalExecWaiters == nil {
		s.kernelLocalExecWaiters = make(map[string]kernelLocalExecWaiterAuthority)
	}
	if _, exists := s.kernelLocalExecWaiters[requestID]; exists {
		return errors.New("kernel local execution approval already has an active waiter")
	}
	s.kernelLocalExecWaiters[requestID] = authority
	return nil
}

func (s *Server) unregisterKernelLocalExecWaiter(requestID string, authority kernelLocalExecWaiterAuthority) {
	if s == nil {
		return
	}
	s.kernelLocalExecMu.Lock()
	defer s.kernelLocalExecMu.Unlock()
	if current, found := s.kernelLocalExecWaiters[requestID]; found && current == authority {
		delete(s.kernelLocalExecWaiters, requestID)
	}
}

func (s *Server) kernelLocalExecWaiterIsActive(input workspace.KernelLocalExecApprovalResolutionInput) bool {
	if s == nil {
		return false
	}
	want := kernelLocalExecWaiterAuthority{
		OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID, FrameID: input.FrameID,
		FrameIncarnation: input.FrameIncarnationID, RootFrameID: input.RootFrameID,
		RootIncarnation: input.RootIncarnationID, StreamUID: input.StreamUID,
		RunnerID: input.RunnerID, RunnerAttempt: input.RunnerAttempt, KernelID: input.KernelID,
		KernelGeneration: input.ExpectedGeneration,
	}
	s.kernelLocalExecMu.Lock()
	defer s.kernelLocalExecMu.Unlock()
	current, found := s.kernelLocalExecWaiters[input.RequestID]
	return found && current == want
}

func (s *Server) authorizeAgentKernelLocalOperation(
	ctx context.Context,
	identity *agentKernelContext,
	call agentruntime.ToolCall,
	publicName string,
	input map[string]any,
	environment string,
) (workspace.KernelLocalOperation, error) {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil || identity == nil {
		return workspace.KernelLocalOperation{}, errors.New("kernel local operation authority is unavailable")
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil || run.Transcript.Claim.Attempt <= 0 ||
		strings.TrimSpace(run.Transcript.Claim.ClaimToken) == "" {
		return workspace.KernelLocalOperation{}, errors.New("kernel local execution requires an active transcript runner claim")
	}
	claim := run.Transcript.Claim
	if claim.StreamUID != run.Transcript.Stream.UID || claim.OwnerID != identity.access.UserID ||
		run.Transcript.Stream.ProjectID != identity.access.Frame.ProjectID ||
		run.Transcript.Stream.RootFrameID != identity.access.Frame.RootFrameID ||
		run.Transcript.Stream.FrameID != identity.access.Frame.ID {
		return workspace.KernelLocalOperation{}, errors.New("kernel local execution runner authority does not match the Frame")
	}
	if err := s.validateLiveTranscriptRunnerClaim(ctx, claim); err != nil {
		return workspace.KernelLocalOperation{}, err
	}
	operationID := strings.TrimSpace(run.KernelOperationIDs[call.ID])
	if operationID == "" {
		return workspace.KernelLocalOperation{}, errors.New("kernel local operation identity is not bound to the active runner checkpoint")
	}
	operation, found, err := s.workspaceStore.GetKernelLocalOperation(
		ctx, identity.access.UserID, operationID,
	)
	if err != nil {
		return workspace.KernelLocalOperation{}, err
	}
	if !found {
		return workspace.KernelLocalOperation{}, errors.New("kernel local operation was not committed with the model tool call")
	}
	publicName = strings.ToLower(strings.TrimSpace(publicName))
	if publicName == "operon" {
		publicName = "repl"
	}
	var encodedInput []byte
	if publicName == softwareRuntimeToolName {
		encodedInput, _, err = canonicalSoftwareRuntimeInput(input)
	} else {
		encodedInput, err = json.Marshal(input)
	}
	if err != nil {
		return workspace.KernelLocalOperation{}, err
	}
	inputDigest := sha256.Sum256(encodedInput)
	if operation.OwnerUserID != identity.access.UserID || operation.ProjectID != identity.access.Frame.ProjectID ||
		operation.RootFrameID != identity.access.Frame.RootFrameID || operation.FrameID != identity.access.Frame.ID ||
		operation.FrameIncarnationID != identity.access.Frame.IncarnationID ||
		operation.RootFrameIncarnationID != identity.access.RootFrameIncarnationID ||
		operation.StreamUID != claim.StreamUID || operation.ToolCallID != call.ID || operation.Tool != publicName || operation.Environment != environment ||
		operation.InputSHA256 != hex.EncodeToString(inputDigest[:]) || !reflect.DeepEqual(operation.InputJSON, encodedInput) {
		return workspace.KernelLocalOperation{}, errors.New("kernel local operation conflicts with the requested execution")
	}

	if operation.State != workspace.KernelLocalOperationStatePendingApproval {
		switch operation.State {
		case workspace.KernelLocalOperationStateApproved:
			return operation, nil
		case workspace.KernelLocalOperationStateFailed, workspace.KernelLocalOperationStateCancelled:
			if operation.ExecutionLogID != "" {
				return operation, nil
			}
			return workspace.KernelLocalOperation{}, errors.New(workspace.KernelLocalExecutionApprovalDeniedMessage)
		case workspace.KernelLocalOperationStatePrepared:
			return workspace.KernelLocalOperation{}, errors.New("kernel local operation preparation requires recovery reconciliation")
		case workspace.KernelLocalOperationStateStarted, workspace.KernelLocalOperationStateOutcomeUnknown:
			return workspace.KernelLocalOperation{}, errors.New("kernel local operation outcome requires recovery reconciliation")
		case workspace.KernelLocalOperationStateCompleted:
			if operation.ExecutionLogID == "" {
				return workspace.KernelLocalOperation{}, errors.New("kernel local operation result authority is unavailable")
			}
			return operation, nil
		default:
			return workspace.KernelLocalOperation{}, errors.New("kernel local operation state is invalid")
		}
	}

	operation, decision, err := s.resolvePendingKernelLocalOperationPolicy(ctx, run, claim, operation)
	if err != nil {
		return workspace.KernelLocalOperation{}, err
	}
	switch decision {
	case "ask":
		return workspace.KernelLocalOperation{}, &agentruntime.PauseError{
			Status: "awaiting_approval", Message: "Kernel local execution is awaiting approval.",
			Data: map[string]any{
				"operation_id": operation.OperationID, "request_id": operation.ApprovalRequestID,
				"state_version": operation.StateVersion, "tool_call_id": operation.ToolCallID,
			},
		}
	case "allow":
		return operation, nil
	case "deny":
		if operation.State != workspace.KernelLocalOperationStateFailed &&
			operation.State != workspace.KernelLocalOperationStateCancelled {
			return workspace.KernelLocalOperation{}, errors.New("kernel local execution approval policy did not persist its denial")
		}
		if operation.ApprovalDecision != "deny" {
			return workspace.KernelLocalOperation{}, errors.New("kernel local execution was denied by the active approval policy")
		}
		return workspace.KernelLocalOperation{}, errors.New("kernel local execution was denied by the active approval policy")
	default:
		return workspace.KernelLocalOperation{}, errors.New("kernel local execution approval policy is invalid")
	}
}

// resolvePendingKernelLocalOperationPolicy is the single policy boundary for
// both uninterrupted execution and durable runner recovery. A recovered
// full-access conversation must not surface a confirmation merely because its
// operation was checkpointed before the original runner applied the policy.
func (s *Server) resolvePendingKernelLocalOperationPolicy(
	ctx context.Context,
	run *sessionRunnerChatRun,
	claim transcriptstore.RunnerClaim,
	operation workspace.KernelLocalOperation,
) (workspace.KernelLocalOperation, string, error) {
	if s == nil || s.workspaceStore == nil || run == nil {
		return workspace.KernelLocalOperation{}, "", errors.New("kernel local operation approval policy is unavailable")
	}
	if operation.State != workspace.KernelLocalOperationStatePendingApproval {
		return workspace.KernelLocalOperation{}, "", errors.New("kernel local operation is not pending approval")
	}
	approvalSessionID, err := kernelApprovalConversationFrameID(run.SessionID, operation.FrameID)
	if err != nil {
		return workspace.KernelLocalOperation{}, "", err
	}
	conversationMode, err := s.webSessionApprovalMode(approvalSessionID)
	if err != nil {
		return workspace.KernelLocalOperation{}, "", errors.New("kernel local execution conversation approval policy is unavailable")
	}
	policy, err := s.workspaceStore.KernelLocalExecApprovalPolicy(
		ctx, operation.OwnerUserID, operation.ProjectID, operation.RootFrameID,
		operation.Tool, operation.Environment,
	)
	if err != nil {
		return workspace.KernelLocalOperation{}, "", err
	}
	defaults := s.agentRuntimeApprovalDefaults()
	decision, err := kernelLocalOperationApprovalDecision(policy, defaults.Mode, conversationMode)
	if err != nil {
		return workspace.KernelLocalOperation{}, "", err
	}
	if decision == "ask" {
		return operation, decision, nil
	}
	if decision != "allow" && decision != "deny" {
		return workspace.KernelLocalOperation{}, "", errors.New("kernel local execution approval policy is invalid")
	}
	approved := decision == "allow"
	reasonCode := ""
	if !approved {
		reasonCode = "approval_policy_denied"
	}
	resolved, err := s.workspaceStore.ResolveKernelLocalOperationApproval(ctx,
		workspace.ResolveKernelLocalOperationApprovalInput{
			OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
			ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
			Approved: approved, DecisionID: kernelLocalOperationDecisionID(
				operation.OwnerUserID, operation.OperationID, operation.ApprovalRequestID, approved, "once",
			), Scope: "once", Source: "policy", ActorID: "system", ReasonCode: reasonCode,
			AdmitRunnerRevision: false, CurrentClaim: claim,
		})
	if err != nil {
		return workspace.KernelLocalOperation{}, "", err
	}
	return resolved, decision, nil
}

// kernelLocalOperationDefaultApprovalDecision keeps every local execution
// interactive by default. A governed tool boundary constrains what may run; it
// does not replace the user's per-operation consent. Explicit conversation or
// administrator policy may still allow or deny without prompting.
func kernelLocalOperationDefaultApprovalDecision(
	tool string,
	defaultMode string,
	conversationMode string,
) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(defaultMode))
	if selected := strings.ToLower(strings.TrimSpace(conversationMode)); selected != "" {
		mode = selected
	}
	decision, err := webPermissionDecision(mode, false)
	if err != nil {
		return "", errors.New("kernel local execution approval policy is invalid")
	}
	return decision, nil
}

func kernelLocalOperationApprovalDecision(
	policy workspace.ApprovalPolicyDecision,
	defaultMode string,
	conversationMode string,
) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(conversationMode))
	if policy.Found && strings.EqualFold(strings.TrimSpace(policy.Tier), "deny") {
		return "deny", nil
	}
	if mode == "deny" {
		return "deny", nil
	}
	if mode == "allow" {
		return "allow", nil
	}
	if policy.Found {
		switch strings.ToLower(strings.TrimSpace(policy.Tier)) {
		case "allow":
			return "allow", nil
		case "ask":
			return "ask", nil
		case "deny":
			return "deny", nil
		default:
			return "", errors.New("kernel local execution approval policy is invalid")
		}
	}
	return kernelLocalOperationDefaultApprovalDecision("", defaultMode, conversationMode)
}

func kernelLocalOperationApprovalScopeAllowed(tool, scope string) bool {
	if strings.EqualFold(strings.TrimSpace(tool), softwareRuntimeToolName) {
		return strings.EqualFold(strings.TrimSpace(scope), "once")
	}
	return true
}

func (s *Server) waitForAgentKernelLocalExecApproval(
	ctx context.Context,
	identity *agentKernelContext,
	call agentruntime.ToolCall,
	publicName string,
	input map[string]any,
	spec kernelruntime.SessionSpec,
	session kernelruntime.EnsuredSession,
	workingDir string,
	expectedGeneration uint64,
	prepare func(transcriptstore.RunnerClaim, workspace.KernelLocalExecApprovalResolutionInput) error,
) error {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil || identity == nil || session.Worker == nil {
		return errors.New("kernel local execution approval authority is unavailable")
	}
	publicName = strings.ToLower(strings.TrimSpace(publicName))
	if publicName == "operon" {
		publicName = "repl"
	}
	environment := spec.Environment
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil || run.Transcript.Claim.Attempt <= 0 ||
		strings.TrimSpace(run.Transcript.Claim.ClaimToken) == "" {
		return errors.New("kernel local execution requires an active transcript runner claim")
	}
	claim := run.Transcript.Claim
	claimTokenDigest := sha256.Sum256([]byte(claim.ClaimToken))
	claimTokenSHA := hex.EncodeToString(claimTokenDigest[:])
	if claim.StreamUID != run.Transcript.Stream.UID || claim.OwnerID != identity.access.UserID ||
		run.Transcript.Stream.ProjectID != identity.access.Frame.ProjectID ||
		run.Transcript.Stream.RootFrameID != identity.access.Frame.RootFrameID ||
		run.Transcript.Stream.FrameID != identity.access.Frame.ID {
		return errors.New("kernel local execution runner authority does not match the Frame")
	}
	if err := s.validateLiveTranscriptRunnerClaim(ctx, claim); err != nil {
		return err
	}
	policy, err := s.workspaceStore.KernelLocalExecApprovalPolicy(
		ctx, identity.access.UserID, identity.access.Frame.ProjectID,
		identity.access.Frame.RootFrameID, publicName, environment,
	)
	if err != nil {
		return err
	}
	approvalSessionID, err := kernelApprovalConversationFrameID(run.SessionID, identity.access.Frame.ID)
	if err != nil {
		return err
	}
	conversationMode, err := s.webSessionApprovalMode(approvalSessionID)
	if err != nil {
		return errors.New("kernel local execution conversation approval policy is unavailable")
	}
	defaults := s.agentRuntimeApprovalDefaults()
	decision, err := kernelLocalOperationApprovalDecision(policy, defaults.Mode, conversationMode)
	if err != nil {
		return err
	}
	switch decision {
	case "allow":
		return nil
	case "deny":
		return errors.New("kernel local execution was denied by approval defaults")
	case "ask":
	default:
		return errors.New("kernel local execution approval defaults are invalid")
	}
	encodedInput, err := json.Marshal(input)
	if err != nil {
		return err
	}
	inputDigest := sha256.Sum256(encodedInput)
	inputSHA := hex.EncodeToString(inputDigest[:])
	approvalID := agentKernelLocalExecApprovalID(claim, session.ID, expectedGeneration, call.ID, publicName, inputSHA)
	waiterAuthority := kernelLocalExecWaiterAuthority{
		OwnerUserID: identity.access.UserID, ProjectID: identity.access.Frame.ProjectID,
		FrameID: identity.access.Frame.ID, FrameIncarnation: identity.access.Frame.IncarnationID,
		RootFrameID:     identity.access.Frame.RootFrameID,
		RootIncarnation: identity.access.RootFrameIncarnationID,
		StreamUID:       claim.StreamUID, RunnerID: claim.RunnerID, RunnerAttempt: claim.Attempt,
		KernelID: session.ID, KernelGeneration: expectedGeneration,
	}
	if err := s.registerKernelLocalExecWaiter(approvalID, waiterAuthority); err != nil {
		return err
	}
	defer s.unregisterKernelLocalExecWaiter(approvalID, waiterAuthority)
	requested, err := s.workspaceStore.AddKernelLocalExecApprovalRequest(ctx,
		workspace.KernelLocalExecApprovalRequestInput{
			OwnerUserID: identity.access.UserID, ProjectID: identity.access.Frame.ProjectID,
			FrameID: identity.access.Frame.ID, FrameIncarnationID: identity.access.Frame.IncarnationID,
			RootFrameID:            identity.access.Frame.RootFrameID,
			RootFrameIncarnationID: identity.access.RootFrameIncarnationID,
			RequestID:              approvalID, Tool: publicName, ToolCallID: call.ID,
			Environment: environment, InputSHA256: inputSHA,
			StreamUID: claim.StreamUID, RunnerID: claim.RunnerID, RunnerAttempt: claim.Attempt,
			ClaimTokenSHA256: claimTokenSHA,
			KernelID:         session.ID, ExpectedGeneration: expectedGeneration,
			Code: stringValue(input["code"]), WorkingDir: workingDir,
			Background: boolValue(input["background"], false), Fresh: boolValue(input["fresh"], false),
		})
	if err != nil {
		return err
	}
	if requested.ID == "" {
		// A matching terminal decision may be retried only until its unique
		// consumption receipt is written. Its original request projection was
		// already committed atomically with the pending state.
	} else if frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(identity.access.Frame.ID); err != nil || !found {
		return s.cancelUnpresentedKernelLocalExecApproval(identity, claim, session.ID, expectedGeneration,
			approvalID, publicName, environment, inputSHA, claimTokenSHA,
			errors.New("kernel local execution approval could not be presented"))
	} else if err := s.publishWebConfirmationProjection(frameContext, requested); err != nil {
		return s.cancelUnpresentedKernelLocalExecApproval(identity, claim, session.ID, expectedGeneration,
			approvalID, publicName, environment, inputSHA, claimTokenSHA,
			errors.New("kernel local execution approval could not be presented"))
	}

	var approvedDecision workspace.KernelLocalExecApprovalDecision
	for {
		decisionWake := s.workspaceStore.KernelRetentionWake()
		decision, found, err := s.workspaceStore.GetKernelLocalExecApprovalDecision(
			ctx, identity.access.UserID, identity.access.Frame.ProjectID,
			identity.access.Frame.ID, identity.access.Frame.IncarnationID, approvalID,
		)
		if err != nil {
			return err
		}
		if found {
			if decision.Tool != publicName || decision.Environment != environment ||
				decision.InputSHA256 != inputSHA || decision.StreamUID != claim.StreamUID ||
				decision.RunnerID != claim.RunnerID || decision.RunnerAttempt != claim.Attempt ||
				decision.KernelID != session.ID || decision.ExpectedGeneration != expectedGeneration {
				return errors.New("kernel local execution approval decision changed authority")
			}
			if !decision.Approved {
				return errors.New(workspace.KernelLocalExecutionApprovalDeniedMessage)
			}
			approvedDecision = decision
			break
		}
		select {
		case <-ctx.Done():
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, cancelEvent, created, cleanupErr := s.workspaceStore.ResolveKernelLocalExecApproval(cancelCtx,
				workspace.KernelLocalExecApprovalResolutionInput{
					OwnerUserID: identity.access.UserID, ProjectID: identity.access.Frame.ProjectID,
					FrameID: identity.access.Frame.ID, FrameIncarnationID: identity.access.Frame.IncarnationID,
					RootFrameID:       identity.access.Frame.RootFrameID,
					RootIncarnationID: identity.access.RootFrameIncarnationID,
					RequestID:         approvalID, Tool: publicName, Environment: environment,
					InputSHA256: inputSHA, StreamUID: claim.StreamUID,
					RunnerID: claim.RunnerID, RunnerAttempt: claim.Attempt,
					ClaimTokenSHA256: claimTokenSHA,
					KernelID:         session.ID, ExpectedGeneration: expectedGeneration,
					Approved: false, Scope: "once",
				})
			if created {
				cleanupErr = errors.Join(cleanupErr, s.publishWorkspaceEvent(cancelEvent))
				cleanupErr = errors.Join(cleanupErr, s.publishWebConfirmationRemovals(
					identity.access.Frame.ID, []string{approvalID}, identity.access.Frame.Status))
			}
			cancel()
			return errors.Join(ctx.Err(), cleanupErr)
		case <-decisionWake:
		}
	}

	if err := s.validateLiveTranscriptRunnerClaim(ctx, claim); err != nil {
		return err
	}
	currentIdentity := s.resolveAgentKernelContext(ctx, run.SessionID)
	if currentIdentity == nil || filepath.Clean(currentIdentity.workspaceDir) != filepath.Clean(identity.workspaceDir) ||
		currentIdentity.access.UserID != identity.access.UserID ||
		currentIdentity.access.Frame.ProjectID != identity.access.Frame.ProjectID ||
		currentIdentity.access.Frame.ID != identity.access.Frame.ID ||
		currentIdentity.access.Frame.IncarnationID != identity.access.Frame.IncarnationID ||
		currentIdentity.access.Frame.RootFrameID != identity.access.Frame.RootFrameID ||
		currentIdentity.access.RootFrameIncarnationID != identity.access.RootFrameIncarnationID {
		return errors.New("kernel workspace authority changed while awaiting approval")
	}
	if _, err := s.validateKernelHostIdentity(ctx, identity.access); err != nil {
		return err
	}
	currentProtected, err := s.agentKernelProtectedPaths()
	if err != nil {
		return err
	}
	currentMounts, err := s.agentKernelConfinementMounts(identity.access.UserID, identity.workspaceDir, currentProtected)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(currentProtected, spec.ProtectedPaths) || !reflect.DeepEqual(currentMounts, spec.Mounts) {
		return errors.New("kernel confinement authority changed while awaiting approval")
	}
	if rawWorkingDir := strings.TrimSpace(stringValue(input["working_dir"])); rawWorkingDir != "" {
		currentWorkingDir, err := s.authorizeAgentKernelWorkingDir(identity.access.UserID, identity.workspaceDir, rawWorkingDir)
		if err != nil || currentWorkingDir != workingDir {
			return errors.New("kernel working directory authority changed while awaiting approval")
		}
	}
	currentEncoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	currentDigest := sha256.Sum256(currentEncoded)
	if currentDigest != inputDigest {
		return errors.New("kernel input changed while awaiting approval")
	}
	if expectedGeneration == 0 || session.Worker.Generation() != expectedGeneration {
		return errors.New("kernel generation changed while awaiting approval")
	}
	if prepare == nil {
		return errors.New("kernel local execution approval handoff is unavailable")
	}
	return prepare(claim, workspace.KernelLocalExecApprovalResolutionInput{
		OwnerUserID: identity.access.UserID, ProjectID: identity.access.Frame.ProjectID,
		FrameID: identity.access.Frame.ID, FrameIncarnationID: identity.access.Frame.IncarnationID,
		RootFrameID:       identity.access.Frame.RootFrameID,
		RootIncarnationID: identity.access.RootFrameIncarnationID,
		RequestID:         approvalID, Tool: publicName, Environment: environment,
		InputSHA256: inputSHA, StreamUID: claim.StreamUID,
		RunnerID: claim.RunnerID, RunnerAttempt: claim.Attempt,
		ClaimTokenSHA256: claimTokenSHA, KernelID: session.ID,
		ExpectedGeneration: expectedGeneration, Approved: true, Scope: approvedDecision.Scope,
		HandoffLeaseTTL: defaultSessionRunnerLeaseTTL,
	})
}

func kernelApprovalConversationFrameID(runSessionID, frameID string) (string, error) {
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return "", errors.New("kernel local execution Frame approval authority is unavailable")
	}
	if sessionID := strings.TrimSpace(runSessionID); sessionID != "" && sessionID != frameID {
		return "", errors.New("kernel local execution runner session does not match the Frame approval authority")
	}
	return frameID, nil
}

func (s *Server) cancelUnpresentedKernelLocalExecApproval(
	identity *agentKernelContext,
	claim transcriptstore.RunnerClaim,
	kernelID string,
	expectedGeneration uint64,
	requestID, tool, environment, inputSHA, claimTokenSHA string,
	presentationErr error,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, event, created, cleanupErr := s.workspaceStore.ResolveKernelLocalExecApproval(cleanupCtx,
		workspace.KernelLocalExecApprovalResolutionInput{
			OwnerUserID: identity.access.UserID, ProjectID: identity.access.Frame.ProjectID,
			FrameID: identity.access.Frame.ID, FrameIncarnationID: identity.access.Frame.IncarnationID,
			RootFrameID:       identity.access.Frame.RootFrameID,
			RootIncarnationID: identity.access.RootFrameIncarnationID,
			RequestID:         requestID, Tool: tool, Environment: environment, InputSHA256: inputSHA,
			StreamUID: claim.StreamUID, RunnerID: claim.RunnerID, RunnerAttempt: claim.Attempt,
			ClaimTokenSHA256: claimTokenSHA, KernelID: kernelID, ExpectedGeneration: expectedGeneration,
			Approved: false, Scope: "once",
		})
	if created {
		cleanupErr = errors.Join(cleanupErr, s.publishWorkspaceEvent(event))
		cleanupErr = errors.Join(cleanupErr, s.publishWebConfirmationRemovals(
			identity.access.Frame.ID, []string{requestID}, identity.access.Frame.Status))
	}
	return errors.Join(presentationErr, cleanupErr)
}

func (s *Server) validateLiveTranscriptRunnerClaim(ctx context.Context, claim transcriptstore.RunnerClaim) error {
	if s == nil || s.transcriptStore == nil {
		return errors.New("transcript runner authority is unavailable")
	}
	return s.transcriptStore.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		_, err := tx.ValidateLiveRunnerClaim(ctx, claim)
		return err
	})
}

func agentKernelLocalExecApprovalID(
	claim transcriptstore.RunnerClaim,
	kernelID string,
	expectedGeneration uint64,
	toolCallID, tool, inputSHA string,
) string {
	tokenDigest := sha256.Sum256([]byte(claim.ClaimToken))
	raw := strings.Join([]string{
		claim.StreamUID, claim.OwnerID, claim.RunnerID, fmt.Sprintf("%d", claim.Attempt),
		hex.EncodeToString(tokenDigest[:]), strings.TrimSpace(kernelID), fmt.Sprintf("%d", expectedGeneration),
		strings.TrimSpace(toolCallID),
		strings.ToLower(strings.TrimSpace(tool)), strings.TrimSpace(inputSHA),
	}, "\x00")
	digest := sha256.Sum256([]byte(raw))
	return "kernel-local-exec-" + hex.EncodeToString(digest[:16])
}
