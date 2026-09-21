package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"errors"
	"fmt"
	"hash"
	"sort"

	"strings"
	"sync"
	"time"

	"synon-go/internal/agentruntime"

	transcriptstore "synon-go/internal/persistence/transcript"

	"synon-go/internal/sciencecapability"
)

const defaultSessionRunnerChatSystemPrompt = "You are Synon, a concise assistant inside a private automation runtime. Answer the user's latest message directly."

const BuiltinSessionRunnerChatEndpoint = "builtin://synon-go/deterministic-chat"

const WorkspaceSessionRunnerProvider = "workspace"

const BuiltinSessionRunnerChatModel = "synon-go-deterministic"

const defaultSessionRunnerChatRequestTimeout = 2 * time.Minute

const defaultSessionRunnerChatPreparationTimeout = 2 * time.Minute

// Compact-model enrichment must never consume the runner's whole preparation
// budget. The deterministic compact handoff is already complete before this
// optional enrichment starts, so a slow provider can be abandoned without
// losing task state or blocking the canonical execution path.
const defaultSessionRunnerCompactSummaryTimeout = 20 * time.Second

// Approved kernel operations are durable work, not ordinary chat preparation.
// The default remains a safety floor for ordinary local operations. A
// software_runtime recovery derives a larger operation-owned budget from its
// admitted timeout_seconds so a legitimate install cannot be killed and
// restarted every ten minutes.
const defaultSessionRunnerKernelRecoveryTimeout = 10 * time.Minute

const defaultSessionRunnerChatMaxAttempts = 4

// Keep model transport independent from per-tool result truncation. Bundled
// agents intentionally use smaller tool-result budgets to protect context, but
// those budgets must never terminate an otherwise valid long model response.
const defaultSessionRunnerModelResponseLimitBytes = int64(1024 * 1024)

// Keep durable transcript writes within one display frame. The first packet is
// still persisted immediately; this cap only coalesces a burst that arrives
// faster than the browser can paint it.
const sessionRunnerContentDeltaFlushInterval = 16 * time.Millisecond
const sessionRunnerContentDeltaFlushBytes = 256
const sessionRunnerContentDeltaPersistenceTimeout = 5 * time.Second
const maxRunnerCorrectionResumeDetailBytes = 4096
const sessionRunnerProviderContinuationContractVersion = 1

// Bump this only when the generic durable correction strategy materially
// improves. Retry budgets are scoped to this revision so failures produced by
// an older repair contract cannot permanently quarantine a task after an
// upgrade, while repeated failures under the current strategy stay bounded.
const sessionRunnerRecoveryContractRevision = 25

const sessionRunnerConsecutiveIdenticalToolRoundBudget = 3
const sessionRunnerToolRoundNoProgressReasonCode = "runner_tool_round_no_progress"
const sessionRunnerToolRoundNoProgressExhaustedReasonCode = "runner_tool_round_no_progress_exhausted"
const sessionRunnerCorrectionNoProgressExhaustedReasonCode = transcriptstore.RetiredCorrectionBudgetReason
const sessionRunnerToolRoundLimitReasonCode = "runner_tool_round_limit_exhausted"
const sessionRunnerToolFailedReasonCode = "tool_call_failed"
const sessionRunnerProviderContextPressureReasonCode = "provider_context_pressure"
const sessionRunnerProviderOutputTokenLimitReasonCode = "provider_output_token_limit"
const sessionRunnerModelProtocolErrorReasonCode = "provider_model_protocol_error"
const sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode = "required_tool_choice_unsatisfied"
const sessionRunnerCompletionReviewRecoveryReasonCode = "completion_review_recovery_required"
const sessionRunnerEmptyFinalResponseReasonCode = "empty_final_response"
const sessionRunnerContentDeltaPersistenceDeadlineReasonCode = "content_delta_persistence_deadline"
const sessionRunnerResponseLanguageMismatchReasonCode = "response_language_mismatch"
const sessionRunnerVisualMediaUnsupportedReasonCode = "visual_review_model_media_unsupported"
const sessionRunnerStoreContentionReasonCode = "transient_store_contention"
const sessionRunnerToolLifecyclePersistenceReasonCode = "tool_lifecycle_persistence_interrupted"
const sessionRunnerKernelOperationPendingRecoveryReasonCode = "kernel_operation_pending_recovery"
const sessionRunnerKernelRecoveryTimeoutReasonCode = "kernel_operation_recovery_timeout"
const sessionRunnerPreparationTimeoutReasonCode = "runner_preparation_timeout"
const sessionRunnerSelectedSkillContractUnavailableReasonCode = "selected_skill_contract_unavailable"
const sessionRunnerExpiredLeaseRecoveryReasonCode = "runner_lease_expired"

const maxSessionRunnerPlanGoalRunes = 600
const noChatToolsAllowedSentinel = "__synon_no_chat_tools_allowed__"
const sessionRunnerModelAuditRuntimeNamespace = "session-runner-model-audit"

type sessionRunnerBoundedCorrection interface {
	error
	runnerCorrection() transcriptstore.RunnerInterruptionCause
}

var errSessionRunnerPreparationDeadline = errors.New("runner preparation deadline exceeded")
var errSessionRunnerKernelRecoveryDeadline = errors.New("kernel operation recovery deadline exceeded")

type sessionRunnerPreparationTimeout struct {
	stage   string
	timeout time.Duration
	cause   error
}

func (e sessionRunnerPreparationTimeout) Error() string {
	stage := strings.TrimSpace(e.stage)
	if stage == "" {
		stage = "unknown"
	}
	return fmt.Sprintf("runner preparation stage %q exceeded %s", stage, e.timeout)
}

func (e sessionRunnerPreparationTimeout) Unwrap() error {
	return e.cause
}

func (e sessionRunnerPreparationTimeout) runnerCorrection() transcriptstore.RunnerInterruptionCause {
	stage := strings.TrimSpace(e.stage)
	if stage == "" {
		stage = "unknown"
	}
	return newRunnerTextCorrection(sessionRunnerPreparationTimeoutReasonCode, fmt.Sprintf(
		"runner preparation timed out during %s after %s; durable history and prior tool results are preserved, and the next bounded attempt must resume from the latest checkpoint",
		stage, e.timeout,
	))
}

func classifySessionRunnerPreparationStageError(
	stage string,
	timeout time.Duration,
	operationCtx context.Context,
	err error,
) error {
	if err == nil {
		return nil
	}
	if operationCtx != nil && errors.Is(operationCtx.Err(), context.DeadlineExceeded) {
		return sessionRunnerPreparationTimeout{stage: stage, timeout: timeout, cause: err}
	}
	return err
}

type sessionRunnerKernelRecoveryTimeout struct {
	timeout time.Duration
	cause   error
}

func (e sessionRunnerKernelRecoveryTimeout) Error() string {
	if e.timeout <= 0 {
		return "kernel operation recovery exceeded its bounded deadline"
	}
	return fmt.Sprintf("kernel operation recovery exceeded %s", e.timeout)
}

func (e sessionRunnerKernelRecoveryTimeout) Unwrap() error {
	return e.cause
}

func (e sessionRunnerKernelRecoveryTimeout) runnerCorrection() transcriptstore.RunnerInterruptionCause {
	return newRunnerTextCorrection(sessionRunnerKernelRecoveryTimeoutReasonCode, fmt.Sprintf(
		"approved kernel work exceeded its bounded recovery window of %s; preserve the exact durable operation and resume it from its latest checkpoint with the active runner lease",
		e.timeout,
	))
}

type sessionRunnerPersistenceInterruption struct {
	cause error
}

func (e sessionRunnerPersistenceInterruption) Error() string {
	if e.cause == nil {
		return "content delta persistence exceeded its local operation deadline"
	}
	return e.cause.Error()
}

func (e sessionRunnerPersistenceInterruption) Unwrap() error {
	return e.cause
}

type sessionRunnerToolLifecyclePersistenceInterruption struct {
	eventType  agentruntime.EventType
	toolCallID string
	cause      error
}

func (e sessionRunnerToolLifecyclePersistenceInterruption) Error() string {
	if e.cause == nil {
		return "tool lifecycle persistence was interrupted"
	}
	return e.cause.Error()
}

func (e sessionRunnerToolLifecyclePersistenceInterruption) Unwrap() error {
	return e.cause
}

func (e sessionRunnerToolLifecyclePersistenceInterruption) diagnosticDetail() string {
	types := make([]string, 0, 4)
	for cause, depth := e.cause, 0; cause != nil && depth < 8; cause, depth = errors.Unwrap(cause), depth+1 {
		types = append(types, fmt.Sprintf("%T", cause))
	}
	if len(types) == 0 {
		types = append(types, "none")
	}
	return fmt.Sprintf(
		"a tool lifecycle checkpoint was not committed; recover the exact durable tool batch before continuing (event_type=%s cause_types=%s)",
		e.eventType, strings.Join(types, ">"),
	)
}

func (run *sessionRunnerChatRun) setRequiredMCPSourceClass(class string) {
	if run == nil {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.requiredMCPSourceClass = strings.ToLower(strings.TrimSpace(class))
}

func (run *sessionRunnerChatRun) requiredMCPSourceClassSnapshot() string {
	if run == nil {
		return ""
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return run.requiredMCPSourceClass
}

func (run *sessionRunnerChatRun) addRequiredScientificCapabilities(capabilities ...string) {
	if run == nil || len(capabilities) == 0 {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.RequiredScientificCapabilities = uniqueSortedScientificCapabilities(append(
		append([]string(nil), run.RequiredScientificCapabilities...), capabilities...,
	))
}

func (run *sessionRunnerChatRun) requiredScientificCapabilitiesSnapshot() []string {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return append([]string(nil), run.RequiredScientificCapabilities...)
}

func (run *sessionRunnerChatRun) addExecutedSkillNames(names ...string) {
	if run == nil || len(names) == 0 {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.ExecutedSkillNames = uniqueSortedFolded(append(
		append([]string(nil), run.ExecutedSkillNames...), names...,
	))
}

func (run *sessionRunnerChatRun) executedSkillNamesSnapshot() []string {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return append([]string(nil), run.ExecutedSkillNames...)
}

func (run *sessionRunnerChatRun) setResolvedUserEvidence(evidence map[string]string) {
	if run == nil {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.ResolvedUserEvidence = make(map[string]string, len(evidence))
	for question, answer := range evidence {
		if question = strings.TrimSpace(question); question != "" {
			if answer = strings.TrimSpace(answer); answer != "" {
				run.ResolvedUserEvidence[question] = answer
			}
		}
	}
}

func (run *sessionRunnerChatRun) resolvedUserEvidenceRecordsSnapshot() []managedExecutionUserEvidence {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	result := make([]managedExecutionUserEvidence, 0, len(run.ResolvedUserEvidence))
	if intent := strings.TrimSpace(run.TaskIntent); intent != "" {
		result = append(result, managedExecutionUserEvidence{Source: "task", Text: intent})
	}
	questions := make([]string, 0, len(run.ResolvedUserEvidence))
	for question := range run.ResolvedUserEvidence {
		questions = append(questions, question)
	}
	sort.Strings(questions)
	for _, question := range questions {
		result = append(result, managedExecutionUserEvidence{
			Source: "ask_user", Question: question, Text: run.ResolvedUserEvidence[question],
		})
	}
	return result
}

func (run *sessionRunnerChatRun) setPendingRequiredSkillNames(names ...string) {
	if run == nil {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.PendingRequiredSkillNames = uniqueSortedFolded(names)
}

func (run *sessionRunnerChatRun) pendingRequiredSkillNamesSnapshot() []string {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return append([]string(nil), run.PendingRequiredSkillNames...)
}

func (run *sessionRunnerChatRun) setSelectedImplementations(implementations ...string) {
	if run == nil {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.SelectedImplementations = uniqueSortedFolded(implementations)
}

func (run *sessionRunnerChatRun) setRegistrySelectedImplementation(implementation string) {
	if run == nil || strings.TrimSpace(implementation) == "" {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.RegistrySelectedImplementations = []string{strings.TrimSpace(implementation)}
}

func (run *sessionRunnerChatRun) consumeRegistrySelectedImplementations() []string {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	selected := append([]string(nil), run.RegistrySelectedImplementations...)
	run.RegistrySelectedImplementations = nil
	return selected
}

func (run *sessionRunnerChatRun) selectedImplementationsSnapshot() []string {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return append([]string(nil), run.SelectedImplementations...)
}

func (run *sessionRunnerChatRun) setSelectedEvidenceResolvers(resolvers ...sciencecapability.ExecutionEvidenceResolver) {
	if run == nil {
		return
	}
	byGroup := map[string]sciencecapability.ExecutionEvidenceResolver{}
	for _, resolver := range resolvers {
		resolver.EvidenceGroup = strings.TrimSpace(resolver.EvidenceGroup)
		resolver.Skill = strings.TrimSpace(resolver.Skill)
		resolver.Implementation = strings.TrimSpace(resolver.Implementation)
		if resolver.EvidenceGroup != "" && resolver.Skill != "" && resolver.Implementation != "" {
			byGroup[strings.ToLower(resolver.EvidenceGroup)] = resolver
		}
	}
	groups := make([]string, 0, len(byGroup))
	for group := range byGroup {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	selected := make([]sciencecapability.ExecutionEvidenceResolver, 0, len(groups))
	for _, group := range groups {
		selected = append(selected, byGroup[group])
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.SelectedEvidenceResolvers = selected
}

func (run *sessionRunnerChatRun) selectedEvidenceResolversSnapshot() []sciencecapability.ExecutionEvidenceResolver {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return append([]sciencecapability.ExecutionEvidenceResolver(nil), run.SelectedEvidenceResolvers...)
}

func (run *sessionRunnerChatRun) setImplementationSelectionRequired(required bool) {
	if run == nil {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.ImplementationSelectionRequired = required
}

func (run *sessionRunnerChatRun) implementationSelectionRequiredSnapshot() bool {
	if run == nil {
		return false
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return run.ImplementationSelectionRequired
}

func (run *sessionRunnerChatRun) addExecutedSkillInvocationKeys(keys ...string) {
	if run == nil || len(keys) == 0 {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	if run.executedSkillInvocationKeys == nil {
		run.executedSkillInvocationKeys = make(map[string]struct{}, len(keys))
	}
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			run.executedSkillInvocationKeys[key] = struct{}{}
		}
	}
}

func (run *sessionRunnerChatRun) hasExecutedSkillInvocationKey(key string) bool {
	if run == nil || strings.TrimSpace(key) == "" {
		return false
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	_, found := run.executedSkillInvocationKeys[key]
	return found
}

func (run *sessionRunnerChatRun) scientificCapabilityLock() *sync.Mutex {
	return &run.scientificCapabilitiesMu
}

type sessionRunnerProviderContinuationV1 struct {
	ContractVersion                    int64
	StreamUID                          string
	OwnerID                            string
	BranchID                           string
	BranchGeneration                   int64
	RootAttempt                        int64
	RootSegmentOrdinal                 int64
	RootStartedEventID                 int64
	RootStartedPublicationSequence     int64
	PreviousAttempt                    int64
	CurrentSegmentOrdinal              int64
	SegmentIndex                       int64
	AcceptedThroughEventID             int64
	AcceptedThroughPublicationSequence int64
	AcceptedSemanticBytes              int64
	AcceptedSHA256                     string
}

type sessionRunnerProviderContinuationState struct {
	Contract              sessionRunnerProviderContinuationV1
	Content               strings.Builder
	PrivateCandidate      strings.Builder
	Digest                hash.Hash
	ConsecutiveNoProgress int
}

func (run *sessionRunnerChatRun) assistantSegmentOrdinal() int64 {
	if run == nil || run.AssistantSegmentOrdinal <= 0 {
		return 1
	}
	return run.AssistantSegmentOrdinal
}

func (run *sessionRunnerChatRun) assistantMessageID() string {
	if run == nil {
		return ""
	}
	return transcriptAssistantMessageID(run.SessionID, int64(run.Attempt), run.assistantSegmentOrdinal())
}

func (run *sessionRunnerChatRun) beginNextAssistantSegment() {
	if run == nil {
		return
	}
	run.ProviderContinuation = nil
	run.ProviderAttemptSemanticBytes = 0
	if !run.AssistantSegmentHasContent {
		return
	}
	run.AssistantSegmentOrdinal = run.assistantSegmentOrdinal() + 1
	run.AssistantSegmentHasContent = false
	run.AssistantSegmentContent.Reset()
}

func (s *Server) restoreTranscriptAssistantSegmentState(ctx context.Context, run *sessionRunnerChatRun) error {
	if s == nil || s.transcriptStore == nil || run == nil || run.Transcript == nil ||
		run.Transcript.Claim.ResumeSource != transcriptstore.ResumeSourceCheckpoint {
		return nil
	}
	authority := run.Transcript
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, authority.Stream.UID, authority.Stream.OwnerID)
	if err != nil {
		return fmt.Errorf("load assistant segment resume snapshot: %w", err)
	}
	var lastOrdinal int64
	lastSegmentHasContent := false
	toolBoundaryAfterContent := false
	resumeFenceSeen := false
	segmentBoundariesAfterContent := int64(0)
	seenAskUserBoundaries := map[string]struct{}{}
	segmentNormalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
	recordAskUserBoundary := func(callID string) {
		callID = strings.TrimSpace(callID)
		if callID == "" {
			return
		}
		if _, duplicate := seenAskUserBoundaries[callID]; duplicate {
			return
		}
		seenAskUserBoundaries[callID] = struct{}{}
		segmentBoundariesAfterContent++
		resumeFenceSeen = true
	}
	err = s.visitTranscriptWebProjectionSnapshotRaw(
		ctx, authority.Stream, authority.Stream.OwnerID, snapshot, true,
		func(projected transcriptstore.ProjectedEvent) error {
			// Segment identities are scoped to one runner attempt. Filter before
			// normalization so a malformed, reset, or terminal segment from an
			// older attempt cannot poison checkpoint recovery for the current one.
			// The Web history reducer intentionally normalizes all attempts, but
			// this restore path owns exactly authority.Claim.Attempt.
			if projected.Event.RunnerAttempt == nil || *projected.Event.RunnerAttempt != authority.Claim.Attempt {
				return nil
			}
			normalized, err := segmentNormalizer.normalize(projected)
			if err != nil {
				return err
			}
			projected = normalized
			payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
			if err != nil {
				return err
			}
			switch projected.Event.Type {
			case "content_delta", "content_reset", "assistant_message":
				segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
				if err != nil {
					return err
				}
				if !present {
					return transcriptstore.ErrEventConflict
				}
				if segment.Ordinal < lastOrdinal {
					return transcriptstore.ErrEventConflict
				}
				if segment.Ordinal > lastOrdinal {
					lastSegmentHasContent = false
				}
				switch projected.Event.Type {
				case "content_delta":
					if transcriptPayloadText(payload) != "" {
						lastSegmentHasContent = true
					}
				case "content_reset", "assistant_message":
					lastSegmentHasContent = transcriptPayloadText(payload) != ""
				}
				lastOrdinal = segment.Ordinal
				toolBoundaryAfterContent = false
				segmentBoundariesAfterContent = 0
			case transcriptstore.AskUserResultEventType:
				fact, found, err := transcriptAskUserProtocolFact(
					projected.Event, projected.ResolvedPayloadJSON, payload,
				)
				if err != nil {
					return err
				}
				if found && fact.Typed && fact.Kind == "result" && !fact.Pending {
					recordAskUserBoundary(fact.CallID)
				}
			case "runner_checkpoint":
				if transcriptWebRuntimeDrainToolBoundary(payload) && lastOrdinal > 0 {
					toolBoundaryAfterContent = true
				}
				// AskUser completion resumes the same runner attempt through the
				// durable input-resolution checkpoint. The provider may restart its
				// segment ordinal at one, but the Web transcript normalizer treats
				// this as a new segment after the preceding tool boundary. Restore
				// the same authority before emitting live deltas so realtime and
				// canonical history use one message identity.
				askUserResume := transcriptWebAskUserResumeFence(payload)
				if askUserResume {
					recordAskUserBoundary(webString(payload["toolCallId"]))
				}
				correctionResume := transcriptWebCorrectionResumeFence(payload)
				if transcriptWebRuntimeDrainFence(payload) || askUserResume ||
					transcriptWebRunnerResumeStateFence(payload) {
					resumeFenceSeen = true
					if (toolBoundaryAfterContent || correctionResume) && lastOrdinal > 0 && segmentBoundariesAfterContent == 0 {
						segmentBoundariesAfterContent = 1
					}
				}
			case "runner_finished":
				lastOrdinal = 0
				lastSegmentHasContent = false
				toolBoundaryAfterContent = false
				resumeFenceSeen = false
				segmentBoundariesAfterContent = 0
				seenAskUserBoundaries = map[string]struct{}{}
				segmentNormalizer = newTranscriptWebRuntimeDrainSegmentNormalizer()
				return nil
			}
			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("restore assistant segment resume state: %w", err)
	}
	if !resumeFenceSeen {
		return nil
	}
	baseOrdinal := lastOrdinal
	if baseOrdinal <= 0 && segmentBoundariesAfterContent > 0 {
		// A provider may emit an AskUser tool call without any durable prose.
		// The tool boundary still occupies the first assistant turn, so resumed
		// prose must start at segment two rather than reusing ordinal one.
		baseOrdinal = 1
	}
	if baseOrdinal <= 0 {
		return nil
	}
	run.AssistantSegmentOrdinal = baseOrdinal
	run.AssistantSegmentHasContent = lastSegmentHasContent
	if segmentBoundariesAfterContent > 0 {
		if segmentBoundariesAfterContent > transcriptstore.AssistantSegmentMaxOrdinal-baseOrdinal {
			return transcriptstore.ErrEventConflict
		}
		run.AssistantSegmentOrdinal += segmentBoundariesAfterContent
		run.AssistantSegmentHasContent = false
	}
	return nil
}

func (run *sessionRunnerChatRun) recordProviderAcceptedContent(delta string, event transcriptstore.Event) {
	if run == nil || run.Transcript == nil || delta == "" || event.EventID <= 0 || event.PublicationSeq <= 0 {
		return
	}
	state := run.ProviderContinuation
	if state == nil {
		state = &sessionRunnerProviderContinuationState{Digest: sha256.New()}
		state.Contract = sessionRunnerProviderContinuationV1{
			ContractVersion: sessionRunnerProviderContinuationContractVersion,
			StreamUID:       run.Transcript.Stream.UID, OwnerID: run.Transcript.Stream.OwnerID,
			RootAttempt: run.Transcript.Claim.Attempt, RootSegmentOrdinal: run.assistantSegmentOrdinal(),
			PreviousAttempt: run.Transcript.Claim.Attempt, CurrentSegmentOrdinal: run.assistantSegmentOrdinal(),
		}
		run.ProviderContinuation = state
	}
	if state.Digest == nil {
		state.Digest = sha256.New()
	}
	if state.Contract.RootStartedEventID == 0 {
		state.Contract.RootStartedEventID = event.EventID
		state.Contract.RootStartedPublicationSequence = event.PublicationSeq
	}
	state.Content.WriteString(delta)
	_, _ = state.Digest.Write([]byte(delta))
	state.Contract.AcceptedSemanticBytes += int64(len(delta))
	state.Contract.AcceptedThroughEventID = event.EventID
	state.Contract.AcceptedThroughPublicationSequence = event.PublicationSeq
	state.Contract.AcceptedSHA256 = hex.EncodeToString(state.Digest.Sum(nil))
	run.ProviderAttemptSemanticBytes += int64(len(delta))
}

type sessionRunnerTaskContract struct {
	Version               int                          `json:"version"`
	TaskIntentID          string                       `json:"taskIntentId,omitempty"`
	TaskIntentRevision    int64                        `json:"taskIntentRevision,omitempty"`
	TaskIntentSHA256      string                       `json:"taskIntentSha256,omitempty"`
	AcceptanceChecks      []string                     `json:"acceptanceChecks"`
	TemporalScopes        []sessionRunnerTemporalScope `json:"temporalScopes,omitempty"`
	CorrectionReason      string                       `json:"correctionReason,omitempty"`
	CorrectionConditionID string                       `json:"correctionConditionId,omitempty"`
	CorrectionFingerprint string                       `json:"correctionFingerprint,omitempty"`
}

type chatCompletionMessage struct {
	Role       string                   `json:"role"`
	Content    string                   `json:"content,omitempty"`
	ToolCallID string                   `json:"tool_call_id,omitempty"`
	ToolCalls  []chatCompletionToolCall `json:"tool_calls,omitempty"`
}

type chatCompletionRequest struct {
	Model       string                  `json:"model"`
	Messages    []chatCompletionMessage `json:"messages"`
	Tools       []chatCompletionTool    `json:"tools,omitempty"`
	Temperature float64                 `json:"temperature,omitempty"`
}

type chatCompletionTool struct {
	Type     string                     `json:"type"`
	Function chatCompletionToolFunction `json:"function"`
}

type chatCompletionToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type chatCompletionToolCall struct {
	ID       string                         `json:"id"`
	Type     string                         `json:"type"`
	Function chatCompletionToolCallFunction `json:"function"`
}

type chatCompletionToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatCompletionMessage `json:"message"`
	} `json:"choices"`
}

func (s *Server) RunSessionRunnerChatOnce(ctx context.Context, options SessionRunnerChatOptions) (SessionRunnerCycleResult, error) {
	return s.runSessionRunnerChatOnce(ctx, options, nil)
}
