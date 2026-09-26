package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/providers"
)

const (
	// Each independent review is bounded. Findings remain visible advisory
	// evidence; execution and persistence integrity are separate hard contracts.
	sessionReviewerMaxToolRounds         = 20
	sessionReviewerMaxReplCalls          = 8
	sessionReviewerMaxGenerationAttempts = 1
)

type sessionRunnerReview struct {
	Verdict      string                     `json:"verdict"`
	Summary      string                     `json:"summary"`
	Feedback     string                     `json:"feedback"`
	EvidenceRefs []string                   `json:"evidence_refs"`
	Issues       []sessionRunnerReviewIssue `json:"issues"`
}

type sessionRunnerReviewIssue struct {
	MessageIndex      int      `json:"msg_idx"`
	Claim             string   `json:"claim"`
	Verdict           string   `json:"verdict"`
	Severity          string   `json:"severity"`
	Evidence          string   `json:"evidence"`
	ArtifactVersionID *string  `json:"artifact_version_id"`
	EvidenceRefs      []string `json:"-"`
	EvidenceQuote     string   `json:"-"`
}

type sessionRunnerCompletionReviewCorrection struct {
	Summary string
	Issues  []sessionRunnerReviewIssue
}

func (err sessionRunnerCompletionReviewCorrection) Error() string {
	return sessionRunnerCompletionReviewCorrectionDetail(err.Summary, err.Issues)
}

func (err sessionRunnerCompletionReviewCorrection) runnerCorrection() transcriptstore.RunnerInterruptionCause {
	review := transcriptstore.RunnerReviewCondition{Summary: err.Summary}
	for _, issue := range err.Issues {
		version := ""
		if issue.ArtifactVersionID != nil {
			version = *issue.ArtifactVersionID
		}
		review.Issues = append(review.Issues, transcriptstore.RunnerReviewConditionIssue{
			MessageIndex: issue.MessageIndex, Claim: issue.Claim, Verdict: issue.Verdict, Severity: issue.Severity,
			Evidence: issue.Evidence, ArtifactVersionID: version, EvidenceRefs: append([]string(nil), issue.EvidenceRefs...), EvidenceQuote: issue.EvidenceQuote,
		})
	}
	return newRunnerCorrection("completion_review_correction_required", err.Error(), transcriptstore.RunnerCorrectionCondition{Review: &review})
}

func sessionRunnerCompletionReviewCorrectionDetail(summary string, issues []sessionRunnerReviewIssue) string {
	parts := []string{"completion reviewer rejected the current candidate"}
	if summary = strings.TrimSpace(summary); summary != "" {
		parts = append(parts, summary)
	}
	for _, issue := range issues {
		claim := strings.TrimSpace(issue.Claim)
		if claim == "" {
			continue
		}
		severity := strings.ToLower(strings.TrimSpace(issue.Severity))
		if severity == "" {
			severity = "unspecified"
		}
		parts = append(parts, severity+": "+claim)
	}
	detail := strings.Join(parts, "; ")
	detail = truncateSessionRunnerReferenceDiagnostic(detail, maxRunnerCorrectionResumeDetailBytes)
	return detail
}

// sessionRunnerReviewStageError marks failures that happened only after the
// main agent had already produced its candidate. The candidate remains durable
// diagnostic evidence, but the logical task cannot become completed until its
// required independent review succeeds.
type sessionRunnerReviewStageError struct {
	Cause error
}

// sessionRunnerEmptyFinalCandidateError prevents a completion-review
// infrastructure failure from being mistaken for a successful turn when the
// main agent did not leave a user-visible final answer. A review failure may
// preserve a real candidate, but an empty candidate must return to the
// bounded recovery path so the frame cannot finish with neither a report nor
// a conclusion.
type sessionRunnerEmptyFinalCandidateError struct {
	Cause  error
	Detail string
}

func (err *sessionRunnerEmptyFinalCandidateError) Error() string {
	if err == nil {
		return "runner produced an empty final candidate"
	}
	if strings.TrimSpace(err.Detail) != "" {
		return strings.TrimSpace(err.Detail)
	}
	if err.Cause != nil {
		return "runner produced an empty final candidate: " + err.Cause.Error()
	}
	return "runner produced an empty final candidate"
}

func (err *sessionRunnerEmptyFinalCandidateError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func (err *sessionRunnerReviewStageError) Error() string {
	if err == nil || err.Cause == nil {
		return "completion review failed"
	}
	return err.Cause.Error()
}

func (err *sessionRunnerReviewStageError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func wrapSessionRunnerReviewStageError(err error) error {
	if err == nil {
		return nil
	}
	return &sessionRunnerReviewStageError{Cause: err}
}

func sessionRunnerReviewCandidateUsable(result agentruntime.RunResult) bool {
	return strings.TrimSpace(sessionRunnerLatestFinalCandidateContent(
		result.Messages, result.FinalMessage.Content,
	)) != ""
}

type sessionReviewerArtifactEvidence struct {
	ArtifactID    string `json:"artifactId"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	VersionID     string `json:"versionId"`
	ContentSHA256 string `json:"contentSha256"`
}

type sessionReviewerWorkspaceEvidence struct {
	WorkspacePath      string                            `json:"workspacePath"`
	Artifacts          []sessionReviewerArtifactEvidence `json:"artifacts"`
	ArtifactsAvailable bool                              `json:"artifactsAvailable"`
	InventoryComplete  bool                              `json:"inventoryComplete"`
}

func (s *Server) runVerifiedSessionAgent(
	ctx context.Context,
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	engine agentruntime.Engine,
	request agentruntime.RunRequest,
	plan sessionRunnerTaskContract,
	run *sessionRunnerChatRun,
	onPlanModeCandidateRejected sessionRunnerPlanModeCandidateRejected,
) (agentruntime.RunResult, error) {
	if request.ToolRoundBudget == nil {
		request.ToolRoundBudget = agentruntime.NewToolRoundBudget(request.MaxToolRounds)
	}
	automaticReview, automaticReviewErr := s.newSessionAutomaticReviewCoordinator(
		ctx, session, options, request.Messages, plan, run,
	)
	if automaticReviewErr != nil {
		// Automatic windows reduce error risk but never replace the terminal
		// review. A checkpoint-initialization failure therefore falls back to the
		// terminal sweep and remains diagnosable without failing the main task.
		log.Printf("automatic reviewer checkpoint initialization failed: %v", automaticReviewErr)
	}
	if automaticReview != nil {
		rootOnEventError := engine.OnEventError
		engine.OnEventError = func(event agentruntime.Event) error {
			if rootOnEventError != nil {
				if err := rootOnEventError(event); err != nil {
					return err
				}
			}
			automaticReview.observe(event)
			return nil
		}
	}
	automaticReviewClosed := false
	defer func() {
		if automaticReview != nil && !automaticReviewClosed {
			automaticReview.close(false)
		}
	}()
	result, err := s.runSessionAgentWithArtifactReferenceRepair(
		ctx, session, engine, request, run, onPlanModeCandidateRejected,
	)
	var automaticReviewState *sessionAutomaticReviewState
	if automaticReview != nil {
		automaticReviewState = automaticReview.close(err == nil)
		automaticReviewClosed = true
	}
	if err != nil {
		return result, err
	}
	automaticReviewerTransferred := false
	defer func() {
		if automaticReviewState == nil || automaticReviewState.ReviewerFrame == nil || automaticReviewerTransferred {
			return
		}
		_ = automaticReviewState.stopReviewerHeartbeat()
		_ = s.finishSessionReviewerFrame(
			automaticReviewState.ReviewerFrame.ID, "cancelled",
			"Automatic review checkpoint was superseded before terminal review",
			automaticReviewState.ReviewIndex,
		)
	}()
	session, err = s.refreshSessionRunnerReviewPolicyForCompletion(session, options, run)
	if err != nil {
		return result, fmt.Errorf("refresh completion review policy: %w", err)
	}
	logicalTaskMessages := make([]agentruntime.Message, 0, len(request.Messages)+len(result.Messages))
	durableEvidence, evidenceErr := s.sessionRunnerDurableExplicitToolContractMessages(ctx, run)
	if evidenceErr != nil {
		return result, fmt.Errorf("load durable explicit tool receipts: %w", evidenceErr)
	}
	logicalTaskMessages = append(logicalTaskMessages, durableEvidence...)
	logicalTaskMessages = append(logicalTaskMessages, request.Messages...)
	logicalTaskMessages = append(logicalTaskMessages, result.Messages...)
	explicitToolContract := buildSessionRunnerExplicitToolContract(sessionRunnerTaskIntent(run))
	explicitToolGaps := explicitToolContract.gaps(logicalTaskMessages)
	finalCandidateContent := sessionRunnerLatestFinalCandidateContent(result.Messages, result.FinalMessage.Content)
	explicitToolGaps = append(explicitToolGaps, explicitToolContract.finalGaps(logicalTaskMessages, finalCandidateContent)...)
	if len(explicitToolGaps) > 0 {
		issues := make([]sessionRunnerReviewIssue, 0, len(explicitToolGaps))
		for _, gap := range explicitToolGaps {
			issues = append(issues, sessionRunnerReviewIssue{
				MessageIndex: 0, Claim: gap, Verdict: "fail", Severity: "high", Evidence: gap,
			})
		}
		return result, sessionRunnerCompletionReviewCorrection{
			Summary: "explicit user-required tool evidence is incomplete", Issues: issues,
		}
	}
	freshResultMessages := result.Messages
	if len(result.Messages) >= len(request.Messages) {
		// Engine.Run returns the complete provider-visible message window. Only
		// the suffix created after this request is fresh evidence for an explicit
		// rerun; historical tool receipts belong to earlier input revisions.
		freshResultMessages = result.Messages[len(request.Messages):]
	}
	if executionErr := sessionRunnerFreshExecutionCompletionError(request.Messages, freshResultMessages); executionErr != nil {
		return result, executionErr
	}
	// Task semantics are not reclassified from user prose here. Explicit Tool
	// requests, typed plans, selected Skill contracts, active operations, and
	// artifact/source lineage retain their own authorities. This completion
	// stage only consumes those machine-readable contracts.
	reviewRequired, policyErr := sessionRunnerEvidenceReviewRequired(session, run)
	if policyErr != nil {
		return result, wrapSessionRunnerReviewStageError(policyErr)
	}
	if !reviewRequired {
		return result, nil
	}
	reviewerOptions, err := s.resolveSessionReviewerOptions(ctx, session, options, sessionRunnerAttempt(run))
	if err != nil {
		return result, wrapSessionRunnerReviewStageError(fmt.Errorf("resolve completion reviewer: %w", err))
	}
	rootFrameID, err := s.sessionRunnerVerificationRoot(session.ID)
	if err != nil {
		return result, wrapSessionRunnerReviewStageError(err)
	}
	reviewerBudget := agentruntime.NewToolRoundBudget(sessionReviewerMaxToolRounds)
	openCheckIDs, err := s.sessionRunnerOpenVerificationCheckIDs(rootFrameID, session.ID, run)
	if err != nil {
		return result, wrapSessionRunnerReviewStageError(fmt.Errorf("load completion review findings: %w", err))
	}
	reviewStart := 0
	if run != nil && run.Transcript != nil {
		reviewStart, err = s.transcriptStore.NextRunnerReviewIndex(
			ctx, run.Transcript.Stream.UID, run.Transcript.Stream.OwnerID, run.Transcript.Claim.Attempt,
		)
		if err != nil {
			return result, wrapSessionRunnerReviewStageError(fmt.Errorf("restore completion review cursor: %w", err))
		}
	}
	reviewIndex := reviewStart
	workspaceEvidence, err := s.sessionReviewerWorkspaceEvidence(session, rootFrameID)
	if err != nil {
		return result, wrapSessionRunnerReviewStageError(fmt.Errorf("resolve completion review evidence: %w", err))
	}
	reviewBinding, err := sessionRunnerVerificationSourceRef(
		rootFrameID, session.ID, reviewIndex, result.FinalMessage.Content, run, workspaceEvidence,
	)
	if err != nil {
		return result, wrapSessionRunnerReviewStageError(fmt.Errorf("bind completion review evidence: %w", err))
	}
	reviewBinding["review_scope"] = "logical_task_terminal"
	var reviewerFrame workspace.Frame
	var reviewerClaim transcriptstore.RunnerClaim
	reusedAutomaticReviewer := automaticReviewState != nil &&
		automaticReviewState.ReviewerFrame != nil && automaticReviewState.ReviewIndex == reviewIndex
	if reusedAutomaticReviewer {
		reviewerFrame = *automaticReviewState.ReviewerFrame
		reviewerClaim = automaticReviewState.ReviewerClaim
		automaticReviewerTransferred = true
	} else {
		reviewerFrame, reviewerClaim, err = s.beginSessionReviewerFrameWithClaim(
			ctx, session, reviewerOptions.Model, sessionRunnerAttempt(run), reviewIndex,
		)
		if err != nil {
			return result, wrapSessionRunnerReviewStageError(fmt.Errorf("start completion reviewer frame: %w", err))
		}
	}
	if err := s.checkpointSessionReview(options, run, reviewIndex, "running", "independent completion review started", map[string]any{"reviewerFrameId": reviewerFrame.ID}); err != nil {
		_ = s.finishSessionReviewerFrame(reviewerFrame.ID, "failed", err.Error(), reviewIndex)
		if reusedAutomaticReviewer {
			_ = automaticReviewState.stopReviewerHeartbeat()
		}
		return result, wrapSessionRunnerReviewStageError(err)
	}
	bookmarkerDone := make(chan error, 1)
	go func() {
		bookmarkerDone <- s.runSessionBookmarkerAtReviewCheckpoint(
			ctx, session, reviewerOptions, result, run, reviewIndex,
		)
	}()
	defer func() {
		if bookmarkerErr := <-bookmarkerDone; bookmarkerErr != nil {
			// Bookmarking is a navigation aid, not a completion verdict. Preserve
			// its hidden fixed-job failure without failing or retrying the task.
			log.Printf("completion bookmarker checkpoint %d failed: %v", reviewIndex, bookmarkerErr)
		}
	}()
	var reviewerCtx context.Context
	var stopReviewerHeartbeat func() error
	if reusedAutomaticReviewer {
		reviewerCtx = automaticReviewState.ReviewerContext
		stopReviewerHeartbeat = automaticReviewState.stopReviewerHeartbeat
	} else {
		reviewerCtx, stopReviewerHeartbeat = s.startSessionReviewerHeartbeat(ctx, reviewerClaim)
	}
	if !reusedAutomaticReviewer || !automaticReviewState.ReviewerBound {
		reviewerCtx, err = s.bindSessionReviewerRun(reviewerCtx, reviewerFrame, reviewerClaim)
	}
	if err != nil {
		log.Printf("completion reviewer runner binding failed: frame=%s err=%v", reviewerFrame.ID, err)
		_ = stopReviewerHeartbeat()
		_ = s.finishSessionReviewerFrame(reviewerFrame.ID, "failed", err.Error(), reviewIndex)
		_ = s.checkpointSessionReview(options, run, reviewIndex, "failed", err.Error(), map[string]any{"reviewerFrameId": reviewerFrame.ID})
		return result, wrapSessionRunnerReviewStageError(fmt.Errorf("bind completion reviewer runner: %w", err))
	}
	reviewResult := result
	reviewMessages := request.Messages
	terminalMessageStart := 0
	completeReviewMessages := sessionReviewerTargetMessages(request.Messages, result.Messages)
	completeReviewTranscriptSHA256, transcriptHashErr := sessionReviewerTargetTranscriptSHA256(completeReviewMessages)
	if transcriptHashErr != nil {
		_ = stopReviewerHeartbeat()
		_ = s.finishSessionReviewerFrame(reviewerFrame.ID, "failed", transcriptHashErr.Error(), reviewIndex)
		return result, wrapSessionRunnerReviewStageError(transcriptHashErr)
	}
	if automaticReviewState != nil && len(automaticReviewState.Units) > 0 &&
		automaticReviewState.CoveredThrough >= 0 && automaticReviewState.CoveredThrough <= len(completeReviewMessages) {
		terminalMessageStart = automaticReviewState.CoveredThrough
		reviewMessages = append([]agentruntime.Message(nil), completeReviewMessages[terminalMessageStart:]...)
		reviewResult.Messages = nil
	}
	terminalReviewBinding := copyMapAny(reviewBinding)
	terminalReviewBinding["message_start"] = terminalMessageStart
	terminalReviewBinding["message_count"] = len(completeReviewMessages)
	review, verifiedReviewBinding, reviewErr := s.reviewSessionRunnerCompletion(
		reviewerCtx, session, reviewerOptions, reviewResult, reviewMessages, plan, run,
		workspaceEvidence, terminalReviewBinding, reviewIndex, reviewerFrame.ID, reviewerBudget,
	)
	if reviewErr == nil && automaticReviewState != nil && len(automaticReviewState.Units) > 0 {
		review, verifiedReviewBinding, reviewErr = combineSessionAutomaticAndTerminalReviews(
			reviewBinding, automaticReviewState.Units, review, verifiedReviewBinding,
			terminalMessageStart, len(completeReviewMessages), completeReviewTranscriptSHA256,
		)
	}
	if heartbeatErr := stopReviewerHeartbeat(); heartbeatErr != nil {
		reviewErr = errors.Join(reviewErr, wrapSessionRunnerReviewStageError(heartbeatErr))
	}
	// Inventory consistency is required even when a provider outage prevents a
	// verdict. Advisory review must not deliver a candidate bound to stale data.
	currentEvidence, evidenceErr := s.sessionReviewerWorkspaceEvidence(session, rootFrameID)
	if evidenceErr != nil {
		reviewErr = errors.Join(reviewErr, wrapSessionRunnerReviewStageError(fmt.Errorf("recheck completion review evidence: %w", evidenceErr)))
	} else {
		currentBinding, bindingErr := sessionRunnerVerificationSourceRef(
			rootFrameID, session.ID, reviewIndex, result.FinalMessage.Content, run, currentEvidence,
		)
		if bindingErr == nil && currentBinding["artifact_inventory_sha256"] != reviewBinding["artifact_inventory_sha256"] {
			bindingErr = errors.New("artifact inventory changed during completion review; the current immutable versions require a fresh review")
		}
		if bindingErr != nil {
			reviewErr = errors.Join(reviewErr, wrapSessionRunnerReviewStageError(bindingErr))
		}
	}
	if reviewErr != nil {
		status := sessionReviewerFailureStatus(ctx, reviewErr)
		finishErr := s.finishSessionReviewerFrame(reviewerFrame.ID, status, reviewErr.Error(), reviewIndex)
		checkpointErr := s.checkpointSessionReview(options, run, reviewIndex, status, reviewErr.Error(), map[string]any{
			"reviewerFrameId": reviewerFrame.ID, "disposition": "unverified",
		})
		if cause := context.Cause(ctx); cause != nil {
			return result, cause
		}
		if finishErr != nil || checkpointErr != nil || !sessionReviewerFailureIsAdvisory(reviewErr) || !sessionRunnerReviewCandidateUsable(result) {
			return result, wrapSessionRunnerReviewStageError(errors.Join(reviewErr, finishErr, checkpointErr))
		}
		return result, nil
	}
	reviewerModel := sessionReviewerModelFromVerifiedBinding(verifiedReviewBinding, reviewerOptions.Model)
	// Keep the reviewer's original verdict. Disposition describes whether that
	// finding blocks delivery, not whether the reviewer actually found an issue.
	verifiedReviewBinding = copyMapAny(verifiedReviewBinding)
	verifiedReviewBinding["review_disposition"] = "advisory"
	checkIDs, persistErr := s.persistSessionRunnerReview(
		rootFrameID, session.ID, reviewerFrame.ID, reviewerModel,
		result.FinalMessage.Content, review, reviewIndex, verifiedReviewBinding,
	)
	if persistErr != nil {
		finishErr := s.finishSessionReviewerFrame(reviewerFrame.ID, "failed", persistErr.Error(), reviewIndex)
		_ = s.checkpointSessionReview(options, run, reviewIndex, "failed", persistErr.Error(), map[string]any{"reviewerFrameId": reviewerFrame.ID})
		if finishErr != nil {
			return result, wrapSessionRunnerReviewStageError(errors.Join(fmt.Errorf("persist completion review: %w", persistErr), fmt.Errorf("finish completion reviewer frame: %w", finishErr)))
		}
		return result, wrapSessionRunnerReviewStageError(fmt.Errorf("persist completion review: %w", persistErr))
	}
	if err := s.finishSessionReviewerFrame(reviewerFrame.ID, "completed", firstNonEmpty(review.Summary, "independent completion review completed"), reviewIndex); err != nil {
		return result, wrapSessionRunnerReviewStageError(fmt.Errorf("finish completion reviewer frame: %w", err))
	}
	if review.Verdict == "pass" {
		if len(openCheckIDs) > 0 {
			if err := s.workspaceStore.ResolveVerificationChecks(rootFrameID, openCheckIDs, review.Summary); err != nil {
				return result, wrapSessionRunnerReviewStageError(fmt.Errorf("resolve corrected verification findings: %w", err))
			}
		}
		if err := s.checkpointSessionReview(options, run, reviewIndex, "completed", firstNonEmpty(review.Summary, "completion review passed"), map[string]any{
			"verdict": "pass", "checkIds": checkIDs, "sourceRef": verifiedReviewBinding,
		}); err != nil {
			return result, wrapSessionRunnerReviewStageError(err)
		}
		return result, nil
	}
	if err := s.checkpointSessionReview(options, run, reviewIndex, "completed", firstNonEmpty(review.Summary, "completion review recorded as advisory"), map[string]any{
		"verdict": review.Verdict, "disposition": "advisory", "checkIds": checkIDs, "sourceRef": verifiedReviewBinding,
	}); err != nil {
		return result, wrapSessionRunnerReviewStageError(err)
	}
	return result, nil
}

func sessionRunnerEvidenceReviewRequired(session sessionstore.Session, run *sessionRunnerChatRun) (bool, error) {
	if !sessionRunnerVerificationEnabled(session) {
		return false, nil
	}
	if run == nil || run.ReviewPolicy == nil {
		return sessionRunnerVerificationEnabled(session), nil
	}
	policy := *run.ReviewPolicy
	if err := validateSessionRunnerResolvedReviewPolicy(policy); err != nil {
		return false, fmt.Errorf("validate resolved review policy: %w", err)
	}
	if !sessionRunnerReviewPolicyMatchesRun(policy, run) ||
		policy.SessionID != strings.TrimSpace(session.ID) {
		return false, errors.New("resolved review policy does not match the active runner authority")
	}
	return policy.EvidenceReviewRequired, nil
}

func sessionRunnerVerificationEnabled(session sessionstore.Session) bool {
	config, _ := session.Orchestration["sessionConfig"].(map[string]any)
	if config == nil {
		config, _ = session.Orchestration["session_config"].(map[string]any)
	}
	for _, key := range []string{"verifier_mode", "verifierMode"} {
		switch value := config[key].(type) {
		case string:
			return strings.EqualFold(strings.TrimSpace(value), "on")
		case bool:
			return value
		}
	}
	return false
}

func sessionRunnerReviewerModel(session sessionstore.Session) string {
	config, _ := session.Orchestration["sessionConfig"].(map[string]any)
	if config == nil {
		config, _ = session.Orchestration["session_config"].(map[string]any)
	}
	return strings.TrimSpace(firstNonEmpty(stringValue(config["reviewer_model"]), stringValue(config["reviewerModel"])))
}

func sessionReviewerModelFromVerifiedBinding(binding map[string]any, fallback string) string {
	return strings.TrimSpace(firstNonEmpty(stringValue(binding["reviewer_model"]), fallback))
}

func (s *Server) resolveSessionReviewerOptions(ctx context.Context, session sessionstore.Session, options SessionRunnerChatOptions, attempt int) (SessionRunnerChatOptions, error) {
	reviewer := options
	reviewer.AllowedTools = readOnlyAgentAllowedTools(options.AllowedTools)
	reviewer.SelectedSkillNames = nil
	reviewer.DisableSkillDiscovery = true
	requestedModel := sessionRunnerReviewerModel(session)
	projectID := sessionRunnerProjectID(session)
	profile, _, err := s.resolveSessionModelProfileWithFallback(session, providers.ResolutionInput{
		Context: ctx, ProjectID: projectID, RequestTimeout: options.RequestTimeout,
		MaxAttempts: options.MaxAttempts, MaxResponseBytes: options.ModelResponseLimitBytes,
	}, requestedModel, "workspace-reviewer-model")
	if err != nil {
		return reviewer, err
	}
	if profile != nil {
		reviewer.Endpoint = profile.Provider.Endpoint
		reviewer.APIKey = profile.APIKey
		reviewer.Model = profile.Model
		reviewer.ModelProfile = profile
	}
	reviewer.ModelAudit = func(record providers.AuditRecord) {
		s.recordSessionRunnerModelAuditRole(session.ID, attempt, "reviewer", record)
	}
	return reviewer, nil
}
