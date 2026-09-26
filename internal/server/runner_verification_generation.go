package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	"synon-go/internal/providers"
)

func (s *Server) reviewSessionRunnerCompletion(
	ctx context.Context,
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	result agentruntime.RunResult,
	originalMessages []agentruntime.Message,
	plan sessionRunnerTaskContract,
	run *sessionRunnerChatRun,
	workspaceEvidence sessionReviewerWorkspaceEvidence,
	reviewBinding map[string]any,
	reviewIndex int,
	reviewerFrameID string,
	reviewerBudget *agentruntime.ToolRoundBudget,
) (sessionRunnerReview, map[string]any, error) {
	spec, err := s.sessionCompletionReviewExecutionSpec()
	if err != nil {
		return sessionRunnerReview{}, nil, err
	}
	targetMessages := sessionReviewerTargetMessages(originalMessages, result.Messages)
	targetTranscriptSHA256, err := sessionReviewerTargetTranscriptSHA256(targetMessages)
	if err != nil {
		return sessionRunnerReview{}, nil, err
	}
	windows := sessionReviewerTranscriptWindows(targetMessages, sessionReviewerTranscriptChunkBytes)
	reviews := make([]sessionRunnerReview, 0, len(windows))
	verifiedBindings := make([]map[string]any, 0, len(windows))
	for index, window := range windows {
		chunkBinding := sessionReviewerChunkBinding(
			reviewBinding, window, index, len(windows), len(targetMessages), targetTranscriptSHA256,
		)
		chunkResult := result
		chunkResult.Messages = nil
		chunkBudget := reviewerBudget
		if index > 0 || chunkBudget == nil {
			chunkBudget = agentruntime.NewToolRoundBudget(sessionReviewerMaxToolRounds)
		}
		submission, verifiedBinding, err := s.reviewSessionRunnerWithSpec(
			ctx, session, options, chunkResult, window.Messages, plan, run,
			workspaceEvidence, chunkBinding, reviewIndex, reviewerFrameID, chunkBudget,
			spec, sessionReviewerWindowExcerpt(window),
		)
		if err != nil {
			return sessionRunnerReview{}, nil, err
		}
		verifiedBinding = copyMapAny(verifiedBinding)
		verifiedBinding["review_verdict"] = submission.Review.Verdict
		reviews = append(reviews, submission.Review)
		verifiedBindings = append(verifiedBindings, verifiedBinding)
	}
	aggregatedReview := aggregateSessionReviewerReviews(reviews, windows)
	aggregatedBinding, err := aggregateSessionReviewerBindings(
		reviewBinding, verifiedBindings, targetTranscriptSHA256, len(targetMessages),
	)
	if err != nil {
		return sessionRunnerReview{}, nil, err
	}
	aggregatedBinding["review_verdict"] = aggregatedReview.Verdict
	return aggregatedReview, aggregatedBinding, nil
}

func (s *Server) reviewSessionRunnerWithSpec(
	ctx context.Context,
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	result agentruntime.RunResult,
	originalMessages []agentruntime.Message,
	plan sessionRunnerTaskContract,
	run *sessionRunnerChatRun,
	workspaceEvidence sessionReviewerWorkspaceEvidence,
	reviewBinding map[string]any,
	reviewIndex int,
	reviewerFrameID string,
	reviewerBudget *agentruntime.ToolRoundBudget,
	spec sessionRunnerReviewExecutionSpec,
	targetTranscriptExcerpt string,
) (sessionReviewerSubmission, map[string]any, error) {
	if strings.TrimSpace(spec.ReviewKind) == "" || strings.TrimSpace(spec.SystemPrompt) == "" ||
		spec.SubmissionMode != sessionReviewerSubmissionCompletion {
		return sessionReviewerSubmission{}, nil, errors.New("review execution specification is invalid")
	}
	// Give the fixed-job reviewer the same durable logical-task source authority
	// used by completion and artifact gates. This server-built handoff survives
	// compaction and continuation units and avoids asking the model to discover a
	// hidden archive API before it can check a governing method.
	durableEvidence, err := s.sessionRunnerDurableEvidenceMessages(ctx, run)
	if err != nil {
		return sessionReviewerSubmission{}, nil, fmt.Errorf("load durable logical-task source evidence: %w", err)
	}
	userPrompt := buildSessionReviewerPromptWithProjectedTranscript(
		session, originalMessages, result, plan, sessionRunnerTaskIntent(run),
		workspaceEvidence, reviewBinding, durableEvidence, targetTranscriptExcerpt,
	)
	var lastErr error
	for generationAttempt := 1; generationAttempt <= sessionReviewerMaxGenerationAttempts; generationAttempt++ {
		attemptPrompt := userPrompt
		if generationAttempt > 1 {
			attemptPrompt += sessionReviewerGenerationRetryNotice(lastErr)
		}
		submission, verifiedBinding, err := s.runSessionReviewerGeneration(
			ctx, session, options, run, workspaceEvidence, reviewBinding, reviewIndex, reviewerFrameID,
			reviewerBudget, spec, attemptPrompt,
		)
		if err == nil {
			return submission, verifiedBinding, nil
		}
		if context.Cause(ctx) != nil {
			return sessionReviewerSubmission{}, nil, context.Cause(ctx)
		}
		lastErr = err
		if !sessionReviewerRetryableGenerationError(err) || generationAttempt == sessionReviewerMaxGenerationAttempts {
			break
		}
		if err := s.checkpointSessionReviewerProtocolRetry(
			options, run, reviewIndex, reviewerFrameID, spec, generationAttempt,
		); err != nil {
			return sessionReviewerSubmission{}, nil, wrapSessionRunnerReviewStageError(err)
		}
	}
	return sessionReviewerSubmission{}, nil, lastErr
}

func sessionReviewerRetryableGenerationError(err error) bool {
	if providers.IsRetryableModelProtocolError(err) {
		return true
	}
	var rejectedSubmission *sessionReviewerSubmissionProtocolError
	if errors.As(err, &rejectedSubmission) {
		return true
	}
	var invalidToolBatch *agentruntime.ToolCallBatchValidationError
	return errors.As(err, &invalidToolBatch)
}

func sessionReviewerGenerationRetryNotice(err error) string {
	var rejectedSubmission *sessionReviewerSubmissionProtocolError
	if errors.As(err, &rejectedSubmission) {
		detail := truncateSessionRunnerReferenceDiagnostic(
			strings.TrimSpace(rejectedSubmission.Detail), maxRunnerCorrectionResumeDetailBytes,
		)
		return "\n\nRETRY NOTICE: The prior reviewer generation exhausted its bounded verdict-correction budget. Start a fresh review, trace the recorded execution again, and submit arguments that strictly match the advertised schema. Last validation error: " + detail
	}
	return "\n\nRETRY NOTICE: The prior reviewer generation was rejected because the model provider returned malformed tool-call protocol data. Re-read the immutable evidence and issue fresh tool calls whose arguments strictly match the advertised JSON schemas."
}

func (s *Server) runSessionReviewerGeneration(
	ctx context.Context,
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
	workspaceEvidence sessionReviewerWorkspaceEvidence,
	reviewBinding map[string]any,
	reviewIndex int,
	reviewerFrameID string,
	reviewerBudget *agentruntime.ToolRoundBudget,
	spec sessionRunnerReviewExecutionSpec,
	userPrompt string,
) (sessionReviewerSubmission, map[string]any, error) {
	scope, err := newSessionReviewerEvidenceScopeForMode(reviewBinding, workspaceEvidence, spec.SubmissionMode)
	if err != nil {
		return sessionReviewerSubmission{}, nil, err
	}
	scope.sessionID = session.ID
	reviewScopeCtx := withSessionReviewerEvidenceScope(ctx, scope)
	reviewCtx, cancelReview := context.WithCancelCause(reviewScopeCtx)
	defer cancelReview(nil)
	submitSchema := sessionReviewerSubmitToolSchema()
	reviewerTools := s.sessionReviewerRuntimeToolSchemasForSubmission(
		reviewCtx, options, workspaceEvidence.ArtifactsAvailable, submitSchema,
	)
	runtimeOptions := options
	runtimeOptions.SessionID = reviewerFrameID
	reviewerRun, _ := reviewCtx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if reviewerRun != nil && reviewerRun.Transcript != nil {
		runtimeOptions.RunnerID = reviewerRun.Transcript.Claim.RunnerID
	}
	engine := s.newAgentRuntimeEngineWithContext(reviewCtx, runtimeOptions, reviewerTools)
	dynamicReviewerModel := &sessionRunnerDynamicModelClient{
		server: s, sessionID: session.ID, session: session, fallback: engine.Model,
		fallbackModel: options.Model, role: "reviewer", audit: options.ModelAudit,
		resolutionInput: providers.ResolutionInput{
			Context:   reviewCtx,
			ProjectID: sessionRunnerProjectID(session), RequestTimeout: options.RequestTimeout,
			MaxAttempts: options.MaxAttempts, MaxResponseBytes: options.ModelResponseLimitBytes,
		},
	}
	engine.Model = dynamicReviewerModel
	engine.Tools = sessionReviewerToolGateway(engine.Tools, scope)
	engine.OnEventError = func(event agentruntime.Event) error {
		if event.Type == agentruntime.EventModelResponse && len(event.ToolCalls) > 0 {
			return wrapSessionRunnerReviewStageError(s.checkpointChatModelToolCalls(runtimeOptions, reviewerRun, event.ToolCalls))
		}
		if event.Type != agentruntime.EventToolStarted && event.Type != agentruntime.EventToolCompleted &&
			event.Type != agentruntime.EventToolFailed && event.Type != agentruntime.EventToolPaused {
			return nil
		}
		if err := s.checkpointSessionRunnerToolEvent(reviewCtx, runtimeOptions, reviewerRun, event); err != nil {
			return wrapSessionRunnerReviewStageError(err)
		}
		protocolError := func() error {
			if event.Type != agentruntime.EventToolFailed || strings.TrimSpace(event.ToolName) != scope.submissionToolName() {
				return nil
			}
			return scope.submissionProtocolError()
		}
		if run == nil {
			if err := protocolError(); err != nil {
				cancelReview(err)
				return err
			}
			return nil
		}
		status := "running"
		message := "reviewer tool " + event.ToolName + " running"
		details := map[string]any{
			"toolName": event.ToolName, "reviewIndex": reviewIndex,
			"reviewerFrameId": reviewerFrameID, "reviewKind": spec.ReviewKind,
			"reviewerProfile": spec.ProfileName,
		}
		if event.Type == agentruntime.EventToolCompleted {
			status = "completed"
			message = "reviewer tool " + event.ToolName + " completed"
		} else if event.Type == agentruntime.EventToolFailed {
			status = "failed"
			var failureDetails map[string]any
			message, failureDetails = sessionReviewerToolFailureCheckpoint(event)
			for key, value := range failureDetails {
				details[key] = value
			}
		}
		checkpointErr := wrapSessionRunnerReviewStageError(s.checkpointChatTool(
			options, run, status, message,
			event.ToolCallID, "verification_tool",
			details,
		))
		if protocolErr := protocolError(); protocolErr != nil {
			if checkpointErr != nil {
				combined := errors.Join(protocolErr, checkpointErr)
				cancelReview(combined)
				return combined
			}
			cancelReview(protocolErr)
			return protocolErr
		}
		return checkpointErr
	}
	reviewResult, err := engine.Run(reviewCtx, agentruntime.RunRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: spec.SystemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Tools:                             reviewerTools,
		MaxToolRounds:                     sessionReviewerMaxToolRounds,
		MaxConsecutiveIdenticalToolRounds: sessionRunnerConsecutiveIdenticalToolRoundBudget,
		ToolRoundBudget:                   reviewerBudget,
		MaxToolCallsPerRound:              options.MaxToolCallsPerRound,
	})
	if err != nil {
		return sessionReviewerSubmission{}, nil, err
	}
	submission, submitted := scope.submissionSnapshot()
	if !submitted {
		return sessionReviewerSubmission{}, nil, &sessionRunnerReviewerEvidenceUnavailableError{
			Detail: spec.ProfileName + " reviewer did not submit a verdict",
		}
	}
	receipts, err := validateSessionReviewerEvidence(
		submission.Review, reviewResult.Messages, scope,
	)
	if err != nil {
		return sessionReviewerSubmission{}, nil, &sessionRunnerReviewerEvidenceUnavailableError{Detail: err.Error()}
	}
	verifiedBinding, err := bindSessionReviewerEvidence(reviewBinding, receipts, reviewResult.Messages)
	if err != nil {
		return sessionReviewerSubmission{}, nil, err
	}
	verifiedBinding = copyMapAny(verifiedBinding)
	verifiedBinding["reviewer_model"] = firstNonEmpty(dynamicReviewerModel.LastSuccessfulModel(), options.Model)
	return submission, verifiedBinding, nil
}

func (s *Server) checkpointSessionReviewerProtocolRetry(
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
	reviewIndex int,
	reviewerFrameID string,
	spec sessionRunnerReviewExecutionSpec,
	failedGenerationAttempt int,
) error {
	details := map[string]any{
		"reviewerFrameId":         reviewerFrameID,
		"reviewKind":              spec.ReviewKind,
		"reviewerProfile":         spec.ProfileName,
		"retryReason":             "model_protocol_error",
		"failedGenerationAttempt": failedGenerationAttempt,
		"nextGenerationAttempt":   failedGenerationAttempt + 1,
		"maxGenerationAttempts":   sessionReviewerMaxGenerationAttempts,
	}
	message := fmt.Sprintf(
		"independent reviewer returned malformed tool-call protocol data; retrying generation %d of %d",
		failedGenerationAttempt+1, sessionReviewerMaxGenerationAttempts,
	)
	return s.checkpointSessionReview(options, run, reviewIndex, "running", message, details)
}

func sessionReviewerToolFailureCheckpoint(event agentruntime.Event) (string, map[string]any) {
	details := map[string]any{}
	inputDigest := sha256.Sum256([]byte(event.Arguments))
	resultDigest := sha256.Sum256([]byte(event.Result))
	details["toolInputBytes"] = len([]byte(event.Arguments))
	details["toolInputSha256"] = hex.EncodeToString(inputDigest[:])
	details["toolResultBytes"] = len([]byte(event.Result))
	details["toolResultSha256"] = hex.EncodeToString(resultDigest[:])

	projection := map[string]any{"ok": false}
	decoded := decodeToolEventJSON(event.Result)
	if result, ok := decoded.(map[string]any); ok {
		for _, key := range []string{"status", "code", "error", "message", "recovery", "retryable"} {
			value, found := result[key]
			if !found {
				continue
			}
			bounded, _ := boundedToolGatewayAuditField(value)
			projection[key] = bounded
		}
	} else if text, ok := decoded.(string); ok && strings.TrimSpace(text) != "" {
		bounded, _ := boundedToolGatewayAuditField(text)
		projection["error"] = bounded
	}

	diagnostic := strings.TrimSpace(event.Message)
	if diagnostic == "" || diagnostic == "tool result reported failure" {
		for _, key := range []string{"error", "message", "code"} {
			if candidate := strings.TrimSpace(stringValue(projection[key])); candidate != "" {
				diagnostic = candidate
				break
			}
		}
	}
	if diagnostic == "" {
		diagnostic = "tool result reported failure"
	}
	boundedDiagnostic, _ := boundedToolGatewayAuditField(diagnostic)
	diagnostic = truncateSessionRunnerReferenceDiagnostic(
		strings.TrimSpace(stringValue(boundedDiagnostic)), toolGatewayAuditTextMaxBytes,
	)
	projection["error"] = diagnostic
	details["toolResult"] = projection
	details["failureMessage"] = diagnostic
	if code := strings.TrimSpace(stringValue(projection["code"])); code != "" {
		details["failureCode"] = code
	}

	message := fmt.Sprintf("reviewer tool %s failed", strings.TrimSpace(event.ToolName))
	if diagnostic != "" && diagnostic != "tool result reported failure" {
		message += ": " + diagnostic
	}
	return message, details
}
