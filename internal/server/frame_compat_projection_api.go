package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"time"
)

func compatibilityProjectResponse(project workspace.CompatibilityProject) map[string]any {
	response := map[string]any{
		"project_id": project.ID, "name": project.Name,
		"description": nullableCompatibilityString(project.Description), "context": project.ContextData,
		"artifact_count": project.ArtifactCount, "conversation_count": project.ConversationCount,
		"created_at": project.CreatedAt.UTC(), "updated_at": project.UpdatedAt.UTC(),
	}
	response["latest_conversation_id"] = nullableCompatibilityString(project.LatestConversationID)
	return response
}

func compatibilityFramePendingInputs(contextData map[string]any) []map[string]any {
	values, ok := contextData["_pending_input_requests"].([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

func (s *Server) compatibilityFrameResponse(frame workspace.CompatibilityFrame, single bool, seen map[string]bool) (map[string]any, error) {
	if seen[frame.ID] {
		return nil, fmt.Errorf("frame hierarchy contains a cycle at %q", frame.ID)
	}
	seen[frame.ID] = true
	children := make([]map[string]any, 0, len(frame.ChildIDs))
	for _, childID := range frame.ChildIDs {
		child, found, err := s.workspaceStore.GetCompatibilityFrame(childID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("frame child %q disappeared during projection", childID)
		}
		projection, err := s.compatibilityFrameResponse(child, false, seen)
		if err != nil {
			return nil, err
		}
		children = append(children, projection)
	}
	delete(seen, frame.ID)
	var inputData any
	if frame.InputData != nil {
		inputData = frame.InputData
	} else if single {
		inputData = map[string]any{}
	}
	var contextData any
	projectedContext, err := s.compatibilityFrameContextProjection(frame)
	if err != nil {
		return nil, err
	}
	if len(projectedContext) > 0 {
		contextData = projectedContext
	}
	var messageCount any
	canonicalMessageCount, canonicalActive, err := s.compatibilityFrameCanonicalMessageCount(frame)
	if err != nil {
		return nil, err
	}
	if canonicalActive {
		if canonicalMessageCount > 0 {
			messageCount = canonicalMessageCount
		}
	} else if frame.MessageCount > 0 {
		messageCount = frame.MessageCount
	}
	var outputData any
	pendingInputs := compatibilityFramePendingInputs(frame.ContextData)
	if status := strings.ToLower(strings.TrimSpace(frame.Status)); status == "completed" || status == "failed" || status == "cancelled" || status == "canceled" {
		// Defensive projection for pre-migration rows: terminal Frames cannot
		// expose stale interaction cards even if older cancellation code left
		// pending metadata behind.
		pendingInputs = nil
	}
	// The plan identifiers belong to the current frame task, not only to the
	// approval pause. Keep them projected while the task is running and after it
	// reaches a terminal state so clients never have to guess from old artifacts.
	planArtifactID := strings.TrimSpace(stringValue(frame.ContextData["_plan_artifact_id"]))
	planVersionID := strings.TrimSpace(stringValue(frame.ContextData["_plan_version_id"]))
	planReference, planReferenceFound, resolveErr := s.workspaceStore.GetCurrentTaskPlanReference(context.Background(), frame.ID)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if planReferenceFound {
		// Durable Transcript order is the plan-revision authority. Runtime
		// context remains a compatibility cache and must not override it.
		planArtifactID = planReference.ArtifactID
		planVersionID = planReference.VersionID
	}
	if frame.OutputData != nil || len(pendingInputs) > 0 || planArtifactID != "" {
		projectedOutput := make(map[string]any, len(frame.OutputData)+3)
		for key, value := range frame.OutputData {
			if key != "pending_input_requests" && key != "plan_artifact_id" && key != "plan_version_id" {
				projectedOutput[key] = value
			}
		}
		if len(pendingInputs) > 0 {
			projectedOutput["pending_input_requests"] = pendingInputs
		}
		if planArtifactID != "" {
			projectedOutput["plan_artifact_id"] = planArtifactID
			if planVersionID != "" {
				projectedOutput["plan_version_id"] = planVersionID
			}
			if planReferenceFound {
				projectedOutput["plan_revision_number"] = planReference.RevisionNumber
				projectedOutput["plan_revision_count"] = planReference.RevisionCount
				projectedOutput["plan_content_version"] = planReference.ContentVersion
				if !planReference.GeneratedAt.IsZero() {
					projectedOutput["plan_generated_at"] = planReference.GeneratedAt.UTC().Format(time.RFC3339Nano)
				}
			}
		}
		outputData = projectedOutput
	}
	var mentionedFiles any
	if frame.MentionedArtifactIDs != nil {
		mentionedFiles = frame.MentionedArtifactIDs
	}
	var specialistsUsed any
	if frame.SpecialistsUsed != nil {
		specialistsUsed = frame.SpecialistsUsed
	}
	statusDescription := frame.StatusDescription
	responseStatus := frame.Status
	runtimeProjection := map[string]any(nil)
	if single {
		var transcriptFailureDetail string
		runtimeProjection, transcriptFailureDetail, err = s.compatibilityFrameRuntimeProjection(frame.ID)
		if err != nil {
			return nil, err
		}
		if genericCompatibilityFailureDescription(frame.Status, statusDescription) && transcriptFailureDetail != "" {
			statusDescription = transcriptFailureDetail
		}
		internalStall, stallErr := s.compatibilityFrameHasInternalStall(frame.ID)
		if stallErr != nil {
			return nil, stallErr
		}
		frameTerminal := compatibilityFrameStatusTerminal(frame.Status)
		// Legacy and just-created Frames can be readable before a transcript
		// runtime projection exists. In that state there is no transcript-owned
		// presentation gate to wait for, and the nil map must remain a valid
		// "runtime metadata unavailable" result rather than being mutated.
		if runtimeProjection != nil && compatibilityFrameNeedsFinalPresentation(frame.Status, boolValue(runtimeProjection["runtime_terminal_presentation_ready"], false)) {
			responseStatus = "finalizing"
			runtimeProjection["runtime_stage"] = "finalizing"
		}
		runtimePaused := boolValue(runtimeProjection["runtime_paused"], false)
		if !frameTerminal && (runtimePaused || internalStall) {
			if runtimeProjection == nil {
				runtimeProjection = map[string]any{}
			}
			switch {
			case len(pendingInputs) > 0:
				responseStatus = workspace.FrameStatusAwaitingUserResponse
			case planArtifactID != "":
				responseStatus = workspace.FrameStatusAwaitingPlanApproval
			case runtimePaused:
				responseStatus = "paused"
			default:
				responseStatus = workspace.FrameStatusFailed
				runtimeProjection["runtime_active"] = false
				runtimeProjection["runtime_task_active"] = false
				runtimeProjection["runtime_failure_reason"] = "runtime_stalled"
			}
			if detail, ok := runtimeProjection["runtime_interruption_detail"].(string); ok && strings.TrimSpace(detail) != "" {
				statusDescription = detail
			}
		}
	}
	if single && strings.TrimSpace(frame.Status) == "failed" && genericCompatibilityFailureDescription(frame.Status, statusDescription) {
		// Older transcript-authoritative runs could persist the precise resume
		// failure in the durable dispatch event before terminal presentation was
		// written. Keep the single-frame recovery view truthful without adding an
		// N+1 lookup to collection and hierarchy projections.
		dispatch, found, err := s.workspaceStore.GetCompatibilityFrameResumeDispatchByFrame(frame.ID)
		if err != nil {
			return nil, err
		}
		if found && dispatch.Status == "failed" {
			statusDescription = strings.TrimSpace(dispatch.Error)
		}
	}
	response := map[string]any{
		"activity_counts": nil, "agent_name": frame.AgentName, "aux_cost": frame.AuxCost,
		"cache_read_tokens": frame.CacheReadTokens, "cache_write_tokens": frame.CacheWriteTokens, "children": children,
		"compaction_count": 0, "completed_at": frame.CompletedAt, "context_data": contextData,
		"context_limit": v11DefaultContextLimit, "context_usage_percent": nil, "context_used": nil,
		"conversation_type": frame.ConversationType, "created_at": frame.CreatedAt.UTC(),
		"delegate_name": nullableCompatibilityString(frame.DelegateName), "effort": nullableCompatibilityString(frame.Effort),
		"id": frame.ID, "input_data": inputData, "input_tokens": frame.InputTokens, "is_hidden": frame.IsHidden,
		"mentioned_files": mentionedFiles, "message_count": messageCount, "model": nullableCompatibilityString(frame.Model),
		"name": nullableCompatibilityString(frame.Name), "output_data": outputData, "output_tokens": frame.OutputTokens,
		"parent_frame_id": nullableCompatibilityString(frame.ParentFrameID), "project_id": frame.ProjectID,
		"root_frame_id": frame.RootFrameID, "specialists_used": specialistsUsed, "status": responseStatus,
		"status_description": nullableCompatibilityString(statusDescription), "task_summary": nullableCompatibilityString(frame.TaskSummary),
		"total_cost": frame.TotalCost, "updated_at": frame.UpdatedAt.UTC(),
	}
	for key, value := range runtimeProjection {
		response[key] = value
	}
	return response, nil
}

func genericCompatibilityFailureDescription(status, description string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "failed" && status != "cancelled" && status != "canceled" {
		return false
	}
	description = strings.ToLower(strings.TrimSpace(description))
	return description == "" || description == status || description == "error" || description == "failed" ||
		description == "cancelled" || description == "canceled"
}

// compatibilityFrameRuntimeProjection exposes the transcript-owned active-work
// clock only on the single-frame endpoint. Collection and hierarchy projections
// remain bounded and avoid a per-frame transcript lookup.
func (s *Server) compatibilityFrameRuntimeProjection(frameID string) (map[string]any, string, error) {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil {
		return nil, "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frameID)
	if err != nil || !found {
		return nil, "", err
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, frameContext.UserID, frameID)
	if err != nil || !found {
		return nil, "", err
	}
	summary, found, err := s.transcriptStore.GetLatestRunnerTimingSummary(ctx, stream.UID, stream.OwnerID)
	if err != nil || !found {
		return nil, "", err
	}
	taskSummary, taskFound, err := s.transcriptStore.GetRunnerTaskTimingSummary(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		return nil, "", err
	}
	if !taskFound {
		return nil, "", transcriptstore.ErrEventConflict
	}
	if summary.InputRevision != stream.InputRevision && !compatibilityFrameStatusTerminal(frameContext.Frame.Status) {
		// A newly accepted input can be visible before its runner claim exists.
		// Never project the previous logical input's terminal elapsed time into
		// that pending-start window.
		projection := compatibilityPendingFrameRuntimeProjection(stream, time.Now().UTC())
		pendingElapsed, _ := projection["runtime_elapsed_ms"].(int64)
		taskElapsed, sumErr := checkedTaskMetricSum(taskSummary.Elapsed.Milliseconds(), pendingElapsed)
		if sumErr != nil {
			return nil, "", fmt.Errorf("sum pending task elapsed time: %w", sumErr)
		}
		projection["runtime_task_active"] = true
		projection["runtime_task_elapsed_ms"] = taskElapsed
		projection["runtime_task_finished_at"] = nil
		projection["runtime_task_observed_at"] = projection["runtime_observed_at"]
		projection["runtime_task_started_at"] = taskSummary.StartedAt.UTC()
		taskMetricsProjection, metricsErr := s.compatibilityFrameTaskMetricsProjection(
			ctx, frameID, stream.UID, stream.OwnerID, stream.InputRevision,
		)
		if metricsErr != nil {
			return nil, "", metricsErr
		}
		for key, value := range taskMetricsProjection {
			projection[key] = value
		}
		return projection, "", nil
	}
	var finishedAt any
	if summary.FinishedAt != nil {
		finishedAt = summary.FinishedAt.UTC()
	}
	var taskFinishedAt any
	if taskSummary.FinishedAt != nil {
		taskFinishedAt = taskSummary.FinishedAt.UTC()
	}
	projection := map[string]any{
		"runtime_active":           summary.Active,
		"runtime_attempt":          summary.Attempt,
		"runtime_elapsed_ms":       summary.Elapsed.Milliseconds(),
		"runtime_finished_at":      finishedAt,
		"runtime_input_revision":   summary.InputRevision,
		"runtime_observed_at":      summary.ObservedAt.UTC(),
		"runtime_started_at":       summary.StartedAt.UTC(),
		"runtime_task_active":      taskSummary.Active,
		"runtime_task_elapsed_ms":  taskSummary.Elapsed.Milliseconds(),
		"runtime_task_finished_at": taskFinishedAt,
		"runtime_task_observed_at": taskSummary.ObservedAt.UTC(),
		"runtime_task_started_at":  taskSummary.StartedAt.UTC(),
	}
	taskMetricsProjection, err := s.compatibilityFrameTaskMetricsProjection(
		ctx, frameID, stream.UID, stream.OwnerID, summary.InputRevision,
	)
	if err != nil {
		return nil, "", err
	}
	for key, value := range taskMetricsProjection {
		projection[key] = value
	}
	reviewProjection, err := s.compatibilityFrameReviewProjection(ctx, frameContext.Frame, summary)
	if err != nil {
		return nil, "", err
	}
	for key, value := range reviewProjection {
		projection[key] = value
	}
	interruption, paused, err := latestNonResumableRunnerInterruption(ctx, s.transcriptStore, stream.UID, stream.OwnerID)
	if err != nil {
		return nil, "", err
	}
	if paused && summary.FinishedEventID <= 0 {
		projection["runtime_active"] = false
		projection["runtime_task_active"] = false
		projection["runtime_paused"] = true
		projection["runtime_interruption_reason"] = interruption.ReasonCode
		projection["runtime_interruption_detail"] = interruption.ResumeDetail
		if !interruption.CreatedAt.IsZero() {
			frozenElapsed := summary.Elapsed - summary.ObservedAt.Sub(interruption.CreatedAt)
			if frozenElapsed < 0 {
				frozenElapsed = 0
			}
			projection["runtime_elapsed_ms"] = frozenElapsed.Milliseconds()
			projection["runtime_observed_at"] = interruption.CreatedAt.UTC()
			taskFrozenElapsed := taskSummary.Elapsed - taskSummary.ObservedAt.Sub(interruption.CreatedAt)
			if taskFrozenElapsed < 0 {
				taskFrozenElapsed = 0
			}
			projection["runtime_task_elapsed_ms"] = taskFrozenElapsed.Milliseconds()
			projection["runtime_task_observed_at"] = interruption.CreatedAt.UTC()
		}
	}
	if summary.FinishedEventID <= 0 {
		return projection, "", nil
	}
	terminal, err := s.transcriptStore.GetTerminalProjection(
		ctx, stream.OwnerID, stream.UID, summary.FinishedEventID,
	)
	if err != nil {
		if errors.Is(err, transcriptstore.ErrTerminalProjectionUnavailable) || errors.Is(err, transcriptstore.ErrEventConflict) {
			return projection, "", nil
		}
		return nil, "", err
	}
	if terminal.TerminalStatus == "failed" && strings.TrimSpace(terminal.ReasonCode) != "" {
		projection["runtime_failure_reason"] = strings.TrimSpace(terminal.ReasonCode)
	} else if terminal.TerminalStatus == "failed" && strings.TrimSpace(interruption.ReasonCode) != "" {
		// A bounded correction dispatcher can settle the Frame before the
		// transcript reconciler closes the still-running attempt. That legacy
		// settlement has a generic terminal payload, while the immediately prior
		// durable interruption owns the precise failure reason. Preserve that
		// reason without projecting the already-terminal task as paused/stalled.
		projection["runtime_failure_reason"] = strings.TrimSpace(interruption.ReasonCode)
	}
	presentationReady, err := s.transcriptWebTerminalPresentationReady(ctx, stream)
	if err != nil {
		return nil, "", err
	}
	projection["runtime_terminal_presentation_ready"] = presentationReady
	return projection, strings.TrimSpace(terminal.Detail), nil
}

func compatibilityFrameStatusTerminal(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == workspace.FrameStatusCompleted || status == workspace.FrameStatusFailed ||
		status == workspace.FrameStatusCancelled || status == "canceled"
}

func compatibilityFrameNeedsFinalPresentation(status string, presentationReady bool) bool {
	return strings.EqualFold(strings.TrimSpace(status), workspace.FrameStatusCompleted) && !presentationReady
}

func latestNonResumableRunnerInterruption(
	ctx context.Context,
	repository *transcriptstore.Repository,
	streamUID string,
	ownerID string,
) (transcriptstore.RunnerInterruption, bool, error) {
	if repository == nil {
		return transcriptstore.RunnerInterruption{}, false, nil
	}
	// Interruption checkpoints are also used as durable handoff markers inside
	// one still-leased attempt. The attempt row, not the most recent checkpoint
	// label, owns logical-task liveness. A fresh running attempt must therefore
	// never be projected as a user-visible pause or failure.
	timing, timingFound, err := repository.GetLatestRunnerTimingSummary(ctx, streamUID, ownerID)
	if err != nil {
		return transcriptstore.RunnerInterruption{}, false, err
	}
	if timingFound && timing.Active {
		return transcriptstore.RunnerInterruption{}, false, nil
	}
	interruption, interrupted, err := repository.LatestRunnerInterruption(ctx, streamUID, ownerID)
	if err != nil || !interrupted {
		return transcriptstore.RunnerInterruption{}, false, err
	}
	resumableCheckpoint, resumable, err := repository.LatestResumableCheckpoint(ctx, streamUID, ownerID)
	if err != nil {
		return transcriptstore.RunnerInterruption{}, false, err
	}
	if interruption.ReasonCode == sessionRunnerModelProviderUnavailableReasonCode {
		// A missing model is explicitly resumable after configuration, but it is
		// still a visible waiting state while no runner owns the task. Do not let
		// the recoverable checkpoint make the UI look active or failed.
		return interruption, true, nil
	}
	return interruption, !resumable || resumableCheckpoint.Sequence != interruption.CheckpointSequence, nil
}

func (s *Server) compatibilityFrameReviewProjection(
	ctx context.Context,
	frame workspace.Frame,
	summary transcriptstore.RunnerTimingSummary,
) (map[string]any, error) {
	if frame.ID == "" || frame.ID != frame.RootFrameID || frame.ParentFrameID != "" || summary.Attempt <= 0 {
		return nil, nil
	}
	frames, err := s.workspaceStore.ListFramesForRoot(frame.ID)
	if err != nil {
		return nil, err
	}
	type reviewCandidate struct {
		frame    workspace.Frame
		metadata workspace.FrameRuntimeMetadata
		active   bool
	}
	var selected *reviewCandidate
	for _, candidate := range frames {
		if !strings.HasPrefix(candidate.ID, "completion-review-") && !strings.HasPrefix(candidate.ID, "scientific-review-") {
			continue
		}
		metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(candidate.ID)
		if err != nil {
			return nil, err
		}
		kind := stringValue(metadata.InputData["kind"])
		if !found || (kind != "runner_completion_review" && kind != "runner_scientific_review") ||
			numberValue(metadata.InputData["runner_attempt"]) != summary.Attempt || candidate.CreatedAt.Before(summary.StartedAt) {
			continue
		}
		active := false
		if strings.EqualFold(candidate.Status, "processing") || strings.EqualFold(candidate.Status, "running") {
			frameContext, contextFound, contextErr := s.workspaceStore.GetFrameRealtimeContext(candidate.ID)
			if contextErr != nil {
				return nil, contextErr
			}
			if contextFound {
				stream, streamFound, streamErr := s.transcriptStore.GetFrameStreamBySession(
					ctx, frameContext.UserID, candidate.ID,
				)
				if streamErr != nil {
					return nil, streamErr
				}
				if streamFound {
					state, stateFound, stateErr := s.transcriptStore.GetLatestRunnerRuntimeState(
						ctx, stream.UID, stream.OwnerID,
					)
					if stateErr != nil {
						return nil, stateErr
					}
					active = stateFound && strings.EqualFold(state.Status, "running") &&
						state.ExpiresAt.After(time.Now().UTC())
				}
			}
		}
		if selected == nil || active && !selected.active || active == selected.active && candidate.CreatedAt.After(selected.frame.CreatedAt) {
			copy := reviewCandidate{frame: candidate, metadata: metadata, active: active}
			selected = &copy
		}
	}
	if selected == nil {
		return nil, nil
	}
	full, found, err := s.workspaceStore.GetFrame(selected.frame.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	status := strings.ToLower(strings.TrimSpace(full.Status))
	projectedStatus := status
	kind := stringValue(selected.metadata.InputData["kind"])
	reviewVerdict := ""
	reviewIssueCount := 0
	reviewBlockingIssueCount := 0
	if !selected.active && status == "completed" {
		checks, err := s.workspaceStore.ListReviewerVerificationChecks(frame.ID, full.ID)
		if err != nil {
			return nil, err
		}
		reviewVerdict, reviewIssueCount, reviewBlockingIssueCount = compatibilityReviewOutcome(checks)
	}
	stage := "review_completed"
	if selected.active {
		stage = "reviewing"
		if kind == "runner_scientific_review" {
			stage = "scientific_reviewing"
		}
	} else if status == "failed" || status == "cancelled" || status == "canceled" ||
		status == "processing" || status == "running" {
		stage = "review_failed"
		projectedStatus = "failed"
	}
	return map[string]any{
		"runtime_stage":                       stage,
		"runtime_review_status":               projectedStatus,
		"runtime_review_kind":                 kind,
		"runtime_review_profile":              stringValue(selected.metadata.InputData["reviewer_profile"]),
		"runtime_review_frame_id":             full.ID,
		"runtime_review_description":          nullableCompatibilityString(full.StatusDescription),
		"runtime_review_trigger":              stringValue(selected.metadata.InputData["review_trigger"]),
		"runtime_review_verdict":              nullableCompatibilityString(reviewVerdict),
		"runtime_review_issue_count":          reviewIssueCount,
		"runtime_review_blocking_issue_count": reviewBlockingIssueCount,
	}, nil
}

func compatibilityReviewOutcome(checks []workspace.VerificationCheck) (string, int, int) {
	verdict := ""
	warningCount := 0
	blockingCount := 0
	for _, check := range checks {
		if sourceRef, ok := check.SourceRef.(map[string]any); ok {
			if scientificDecision, ok := sourceRef["scientific_decision"].(map[string]any); ok {
				if value := strings.ToLower(strings.TrimSpace(stringValue(scientificDecision["effective_verdict"]))); value == "pass" || value == "revise" {
					verdict = value
				}
			} else if value := strings.ToLower(strings.TrimSpace(stringValue(sourceRef["review_verdict"]))); value == "pass" || value == "revise" {
				verdict = value
			}
		}
		switch strings.ToLower(strings.TrimSpace(check.Verdict)) {
		case "fail":
			blockingCount++
		case "warn":
			warningCount++
		case "pass":
			if verdict == "" {
				verdict = "pass"
			}
		}
	}
	if blockingCount > 0 {
		verdict = "revise"
	} else if verdict == "pass" && warningCount > 0 {
		verdict = "pass_with_warnings"
	} else if verdict == "" && warningCount > 0 {
		verdict = "revise"
	}
	return verdict, warningCount + blockingCount, blockingCount
}

func compatibilityPendingFrameRuntimeProjection(stream transcriptstore.Stream, observedAt time.Time) map[string]any {
	startedAt := stream.UpdatedAt.UTC()
	if startedAt.IsZero() || startedAt.After(observedAt) {
		// A legacy stream may not carry the accepted-input timestamp. In that
		// case the first observation is the earliest truthful lower bound; keep
		// the clock live without inventing elapsed time before it was observed.
		startedAt = observedAt.UTC()
	}
	elapsed := observedAt.Sub(startedAt).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	return map[string]any{
		// The input is durable and claimable even before a runner attempt is
		// visible. Treat this as active preparation so clients keep a live clock
		// instead of presenting a misleading frozen 00:00 state.
		"runtime_active":         true,
		"runtime_attempt":        nil,
		"runtime_elapsed_ms":     elapsed,
		"runtime_finished_at":    nil,
		"runtime_input_revision": stream.InputRevision,
		"runtime_stage":          "starting",
		"runtime_observed_at":    observedAt.UTC(),
		"runtime_started_at":     startedAt,
	}
}
