package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

var errManualReviewSourceChanged = errors.New("manual review source changed")

type manualSessionReviewJob struct {
	session            sessionstore.Session
	options            SessionRunnerChatOptions
	run                *sessionRunnerChatRun
	reviewSpec         sessionRunnerReviewExecutionSpec
	result             agentruntime.RunResult
	originalMessages   []agentruntime.Message
	taskContract       sessionRunnerTaskContract
	workspaceEvidence  sessionReviewerWorkspaceEvidence
	reviewBinding      map[string]any
	reviewIndex        int
	reviewerFrame      workspace.Frame
	reviewerClaim      transcriptstore.RunnerClaim
	rootFrameID        string
	finishedEventID    int64
	projectionSnapshot transcriptstore.ProjectionSnapshot
}

type manualSessionReviewOutcome struct {
	status      string
	description string
}

func (s *Server) handleCompatibilityFrameAudit(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if frame.ID != frame.RootFrameID || frame.ParentFrameID != "" {
		writeV11Detail(w, http.StatusNotFound, "Frame "+frame.ID+" not found")
		return
	}
	if s.transcriptStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Manual review is unavailable")
		return
	}

	// Serialize the short reservation transaction so repeated clicks cannot
	// create two reviewers for the same immutable completion.
	s.manualReviewMu.Lock()
	job, existing, err := s.prepareManualSessionReview(r.Context(), frame)
	if err != nil {
		s.manualReviewMu.Unlock()
		switch {
		case errors.Is(err, ErrRuntimeDraining):
			writeV11Detail(w, http.StatusServiceUnavailable, "Runtime is restarting; manual review can be retried")
		case errors.Is(err, errManualReviewSourceChanged), errors.Is(err, ErrSessionRunAlreadyActive):
			writeV11Detail(w, http.StatusConflict, err.Error())
		default:
			writeV11StoreError(w, err)
		}
		return
	}
	if existing != nil {
		s.manualReviewMu.Unlock()
		writeJSON(w, http.StatusAccepted, manualReviewAcceptedResponse(frame.ID, *existing))
		return
	}

	activeCtx, activeRun, cleanup, err := s.registerActiveSessionRun(
		context.Background(), job.reviewerFrame.ID, "manual-review:"+job.reviewerFrame.ID,
	)
	s.manualReviewMu.Unlock()
	if err != nil {
		if errors.Is(err, ErrSessionRunAlreadyActive) {
			writeJSON(w, http.StatusAccepted, manualReviewAcceptedResponse(frame.ID, job.reviewerFrame))
			return
		}
		status := "failed"
		if errors.Is(err, ErrRuntimeDraining) {
			status = "cancelled"
		}
		_ = s.finishSessionReviewerFrame(job.reviewerFrame.ID, status, manualReviewFailureDescription(err), job.reviewIndex)
		if errors.Is(err, ErrRuntimeDraining) {
			writeV11Detail(w, http.StatusServiceUnavailable, "Runtime is restarting; manual review can be retried")
			return
		}
		writeV11StoreError(w, err)
		return
	}

	go s.runManualSessionReview(activeCtx, activeRun, cleanup, job)
	writeJSON(w, http.StatusAccepted, manualReviewAcceptedResponse(frame.ID, job.reviewerFrame))
}

func manualReviewAcceptedResponse(rootFrameID string, reviewer workspace.Frame) map[string]any {
	reviewKind := "runner_completion_review"
	if strings.HasPrefix(strings.TrimSpace(reviewer.ID), "scientific-review-") {
		reviewKind = "runner_scientific_review"
	}
	return map[string]any{
		"ok": true, "frame_id": reviewer.ID, "root_frame_id": rootFrameID,
		"status": "processing", "review_kind": reviewKind,
	}
}

func (s *Server) prepareManualSessionReview(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
) (manualSessionReviewJob, *workspace.Frame, error) {
	stream, authoritative, err := s.resolveTranscriptFrameStream(ctx, frame.ID)
	if err != nil {
		return manualSessionReviewJob{}, nil, err
	}
	if !authoritative {
		return manualSessionReviewJob{}, nil, errors.New("manual review requires the canonical transcript runtime")
	}
	state, found, err := s.transcriptStore.GetLatestRunnerRuntimeState(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		return manualSessionReviewJob{}, nil, err
	}
	if !found || state.Attempt <= 0 || state.Attempt > math.MaxInt || state.FinishedEventID <= 0 ||
		state.ClaimedInputRevision != stream.InputRevision || !strings.EqualFold(state.Status, "completed") {
		return manualSessionReviewJob{}, nil, fmt.Errorf("%w: the task must finish before manual review starts", errManualReviewSourceChanged)
	}
	attempt := int(state.Attempt)
	reviewIndex, activeReviewer, err := s.nextManualReviewIndex(ctx, frame, stream, state)
	if err != nil {
		return manualSessionReviewJob{}, nil, err
	}
	if activeReviewer != nil {
		return manualSessionReviewJob{}, activeReviewer, nil
	}

	session, err := s.loadTranscriptFrameSessionProjection(stream)
	if err != nil {
		return manualSessionReviewJob{}, nil, err
	}
	options := normalizeSessionRunnerChatOptions(s.compactSummarizer)
	options.SessionID = frame.ID
	options.RunnerID = "manual-review:" + frame.ID
	reviewerOptions, err := s.resolveSessionReviewerOptions(ctx, session, options, attempt)
	if err != nil {
		return manualSessionReviewJob{}, nil, fmt.Errorf("resolve manual reviewer: %w", err)
	}

	authority := &transcriptRunnerAuthority{Stream: stream, Claim: transcriptstore.RunnerClaim{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: state.RunnerID,
		Attempt: state.Attempt, ClaimedInputRevision: state.ClaimedInputRevision,
		ResumeSource: state.ResumeSource, ResumeCheckpoint: state.ResumeCheckpoint,
		ClaimedAt: state.ClaimedAt, ExpiresAt: state.ExpiresAt,
	}}
	run := &sessionRunnerChatRun{
		SessionID: frame.ID, Attempt: attempt, Transcript: authority,
		SuppressReviewCheckpoints: true,
	}
	intent, found, err := s.transcriptStore.EnsureActiveFrameTaskIntent(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		return manualSessionReviewJob{}, nil, fmt.Errorf("resolve manual review task intent: %w", err)
	}
	// Task-intent revisions and stream input revisions are separate authorities.
	// Ask User answers advance the stream revision without replacing the task
	// intent. The terminal runner claim above fences the complete logical input;
	// EnsureActiveFrameTaskIntent independently resolves the latest task intent
	// on the active branch.
	if !found || strings.TrimSpace(intent.Text) == "" || intent.Revision <= 0 {
		return manualSessionReviewJob{}, nil, fmt.Errorf("%w: the canonical task intent is unavailable", errManualReviewSourceChanged)
	}
	run.TaskIntent = strings.TrimSpace(intent.Text)
	run.TaskIntentID = strings.TrimSpace(intent.ID)
	run.TaskIntentRevision = intent.Revision

	entries, projectionSnapshot, err := s.loadManualReviewRootTranscript(ctx, authority)
	if err != nil {
		return manualSessionReviewJob{}, nil, err
	}
	reviewSpec, err := s.resolveManualSessionReviewSpec(ctx, frame, run, entries)
	if err != nil {
		return manualSessionReviewJob{}, nil, fmt.Errorf("resolve manual review scope: %w", err)
	}
	reviewSpec.ReviewTrigger = "manual"
	candidate := latestManualReviewCandidate(entries)
	if candidate == "" {
		return manualSessionReviewJob{}, nil, fmt.Errorf("%w: the completed answer is unavailable", errManualReviewSourceChanged)
	}
	originalMessages, err := manualReviewMessagesFromEntries(entries)
	if err != nil {
		return manualSessionReviewJob{}, nil, fmt.Errorf("reconstruct manual review evidence: %w", err)
	}
	result := agentruntime.RunResult{
		FinalMessage: agentruntime.Message{Role: "assistant", Content: candidate},
	}
	workspaceEvidence, err := s.sessionReviewerWorkspaceEvidence(session, frame.ID)
	if err != nil {
		return manualSessionReviewJob{}, nil, err
	}
	baseBinding, err := sessionRunnerVerificationSourceRef(
		frame.ID, session.ID, reviewIndex, candidate, run, workspaceEvidence,
	)
	if err != nil {
		return manualSessionReviewJob{}, nil, err
	}
	targetTranscriptSHA256, err := sessionReviewerTargetTranscriptSHA256(originalMessages)
	if err != nil {
		return manualSessionReviewJob{}, nil, err
	}
	baseBinding["review_scope"] = "full_root_session"
	baseBinding["target_transcript_sha256"] = targetTranscriptSHA256
	baseBinding["message_count"] = len(originalMessages)
	baseBinding["branch_id"] = projectionSnapshot.BranchID
	baseBinding["branch_generation"] = projectionSnapshot.BranchGeneration
	baseBinding["through_publication_sequence"] = projectionSnapshot.ThroughPublicationSequence
	reviewerFrame, reviewerClaim, err := s.beginSessionReviewFrameWithClaim(
		ctx, session, reviewerOptions.Model, attempt, reviewIndex, reviewSpec,
	)
	if err != nil {
		return manualSessionReviewJob{}, nil, err
	}
	return manualSessionReviewJob{
		session: session, options: reviewerOptions, run: run, reviewSpec: reviewSpec, result: result,
		originalMessages:  originalMessages,
		taskContract:      buildSessionRunnerTaskContract(run.TaskIntent, run.TaskIntentID, run.TaskIntentRevision),
		workspaceEvidence: workspaceEvidence, reviewBinding: baseBinding,
		reviewIndex: reviewIndex, reviewerFrame: reviewerFrame, rootFrameID: frame.ID,
		reviewerClaim:      reviewerClaim,
		finishedEventID:    state.FinishedEventID,
		projectionSnapshot: projectionSnapshot,
	}, nil, nil
}

// A manual click is an explicit request for review, but it must not mutate the
// conversation's automatic-review preference. Reconstruct the same trusted,
// task-scoped scientific signals used by the automatic gate and select the
// bundled domain reviewer only when those signals (or the root agent) prove
// that the completed task is scientific. General tasks retain the ordinary
// completion reviewer.
func (s *Server) resolveManualSessionReviewSpec(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
	run *sessionRunnerChatRun,
	entries []eventjournal.Entry,
) (sessionRunnerReviewExecutionSpec, error) {
	if run == nil || run.Transcript == nil {
		return sessionRunnerReviewExecutionSpec{}, errors.New("manual review requires transcript runner authority")
	}
	scopedEntries, err := s.runnerEntriesForCurrentLogicalInput(ctx, entries, run)
	if err != nil {
		return sessionRunnerReviewExecutionSpec{}, err
	}
	skillNames := completedSkillNamesFromRunnerEntries(scopedEntries)
	capabilities := requiredScientificCapabilitiesFromRunnerEntries(scopedEntries)
	signals := trustedScientificReviewSignalsFromRunnerEntries(scopedEntries)
	run.addExecutedSkillNames(skillNames...)
	run.addRequiredScientificCapabilities(capabilities...)
	run.addTrustedScientificReviewSignals(signals...)

	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: frame.ID, StreamUID: run.Transcript.Stream.UID,
		RunnerAttempt: run.Attempt, ClaimedInputRevision: run.Transcript.Claim.ClaimedInputRevision,
		TaskIntentID: run.TaskIntentID, TaskIntentRevision: run.TaskIntentRevision,
		RootAgent:                      frame.AgentName,
		UserRequestedEvidenceReview:    true,
		SelectedSkillNames:             skillNames,
		RequiredScientificCapabilities: capabilities,
		TrustedScientificSignals:       signals,
	})
	if err != nil {
		return sessionRunnerReviewExecutionSpec{}, err
	}
	run.ReviewPolicy = &policy
	return s.sessionCompletionReviewExecutionSpec()
}

func (s *Server) nextManualReviewIndex(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
	stream transcriptstore.Stream,
	state transcriptstore.RunnerRuntimeState,
) (int, *workspace.Frame, error) {
	index, err := s.transcriptStore.NextRunnerReviewIndex(ctx, stream.UID, stream.OwnerID, state.Attempt)
	if err != nil {
		return 0, nil, err
	}
	frames, err := s.workspaceStore.ListFramesForRoot(frame.ID)
	if err != nil {
		return 0, nil, err
	}
	for _, candidate := range frames {
		metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(candidate.ID)
		if err != nil {
			return 0, nil, err
		}
		kind := stringValue(metadata.InputData["kind"])
		if !found || (kind != "runner_completion_review" && kind != "runner_scientific_review") ||
			numberValue(metadata.InputData["runner_attempt"]) != state.Attempt {
			continue
		}
		candidateIndex := int(numberValue(metadata.InputData["review_index"]))
		if candidateIndex >= index {
			index = candidateIndex + 1
		}
		if strings.EqualFold(stringValue(metadata.InputData["review_trigger"]), "manual") &&
			strings.EqualFold(candidate.Status, "processing") {
			copy := candidate
			return index, &copy, nil
		}
	}
	return index, nil, nil
}

func latestManualReviewCandidate(entries []eventjournal.Entry) string {
	for index := len(entries) - 1; index >= 0; index-- {
		if !strings.EqualFold(strings.TrimSpace(stringValue(entries[index].Message["role"])), "assistant") {
			continue
		}
		if text := strings.TrimSpace(runnerMessageText(entries[index].Message)); text != "" {
			return text
		}
	}
	return ""
}

func (s *Server) runManualSessionReview(
	ctx context.Context,
	activeRun *activeSessionRun,
	cleanup func(),
	job manualSessionReviewJob,
) {
	defer cleanup()
	outcome := manualSessionReviewOutcome{status: "failed", description: "Manual review was interrupted; retry is available"}
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(os.Stderr, "[manual-review] panic for %s: %v\n", job.reviewerFrame.ID, recovered)
			outcome = manualSessionReviewOutcome{status: "failed", description: "Manual review was interrupted; retry is available"}
		}
		if err := s.settleManualSessionReview(ctx, activeRun, job, outcome); err != nil {
			fmt.Fprintf(os.Stderr, "[manual-review] settle %s: %v\n", job.reviewerFrame.ID, err)
		}
	}()

	reviewerCtx, stopReviewerHeartbeat := s.startSessionReviewerHeartbeat(ctx, job.reviewerClaim)
	var bindErr error
	reviewerCtx, bindErr = s.bindSessionReviewerRun(reviewerCtx, job.reviewerFrame, job.reviewerClaim)
	if bindErr != nil {
		_ = stopReviewerHeartbeat()
		outcome.description = manualReviewFailureDescription(bindErr)
		return
	}
	review, verifiedBinding, err := s.executeManualSessionReview(reviewerCtx, job)
	if heartbeatErr := stopReviewerHeartbeat(); err == nil && heartbeatErr != nil {
		err = heartbeatErr
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[manual-review] review %s failed: %v\n", job.reviewerFrame.ID, err)
		outcome.status = sessionReviewerFailureStatus(ctx, err)
		outcome.description = manualReviewFailureDescription(err)
		return
	}
	if err := s.validateManualReviewSource(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "[manual-review] source %s changed: %v\n", job.reviewerFrame.ID, err)
		outcome.description = manualReviewFailureDescription(err)
		return
	}
	reviewerModel := sessionReviewerModelFromVerifiedBinding(verifiedBinding, job.options.Model)
	if _, err := s.persistSessionRunnerReview(
		job.rootFrameID, job.session.ID, job.reviewerFrame.ID, reviewerModel,
		job.result.FinalMessage.Content, review, job.reviewIndex, verifiedBinding,
	); err != nil {
		fmt.Fprintf(os.Stderr, "[manual-review] persist %s failed: %v\n", job.reviewerFrame.ID, err)
		outcome.description = manualReviewFailureDescription(err)
		return
	}
	outcome.status = "completed"
	if review.Verdict == "pass" {
		outcome.description = "Manual review completed"
	} else {
		outcome.description = "Manual review completed with findings"
	}
}

func (s *Server) executeManualSessionReview(
	ctx context.Context,
	job manualSessionReviewJob,
) (sessionRunnerReview, map[string]any, error) {
	budget := agentruntime.NewToolRoundBudget(sessionReviewerMaxToolRounds)
	return s.reviewSessionRunnerCompletion(
		ctx, job.session, job.options, job.result, job.originalMessages, job.taskContract, job.run,
		job.workspaceEvidence, job.reviewBinding, job.reviewIndex, job.reviewerFrame.ID, budget,
	)
}

func (s *Server) validateManualReviewSource(ctx context.Context, job manualSessionReviewJob) error {
	stream, authoritative, err := s.resolveTranscriptFrameStream(ctx, job.rootFrameID)
	if err != nil {
		return err
	}
	if !authoritative || stream.UID != job.run.Transcript.Stream.UID ||
		stream.InputRevision != job.run.Transcript.Claim.ClaimedInputRevision {
		return errManualReviewSourceChanged
	}
	state, found, err := s.transcriptStore.GetLatestRunnerRuntimeState(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		return err
	}
	if !found || !strings.EqualFold(state.Status, "completed") || state.Attempt != int64(job.run.Attempt) ||
		state.ClaimedInputRevision != stream.InputRevision || state.FinishedEventID != job.finishedEventID {
		return errManualReviewSourceChanged
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		return err
	}
	if snapshot.BranchID != job.projectionSnapshot.BranchID ||
		snapshot.BranchGeneration != job.projectionSnapshot.BranchGeneration ||
		snapshot.ThroughPublicationSequence != job.projectionSnapshot.ThroughPublicationSequence {
		return errManualReviewSourceChanged
	}
	evidence, err := s.sessionReviewerWorkspaceEvidence(job.session, job.rootFrameID)
	if err != nil {
		return err
	}
	binding, err := sessionRunnerVerificationSourceRef(
		job.rootFrameID, job.session.ID, job.reviewIndex, job.result.FinalMessage.Content, job.run, evidence,
	)
	if err != nil {
		return err
	}
	if binding["candidate_sha256"] != job.reviewBinding["candidate_sha256"] ||
		binding["artifact_inventory_sha256"] != job.reviewBinding["artifact_inventory_sha256"] {
		return errManualReviewSourceChanged
	}
	return nil
}

func (s *Server) settleManualSessionReview(
	ctx context.Context,
	activeRun *activeSessionRun,
	job manualSessionReviewJob,
	outcome manualSessionReviewOutcome,
) error {
	activeRun.settlement.Lock()
	defer activeRun.settlement.Unlock()
	if activeRun.settled {
		return nil
	}
	if activeRun.draining.Load() || errors.Is(context.Cause(ctx), ErrRuntimeDraining) ||
		errors.Is(context.Cause(ctx), context.Canceled) || errors.Is(context.Cause(ctx), ErrGenerationStopped) {
		outcome.status = "cancelled"
		outcome.description = "Manual review was cancelled; the completed task result is unchanged"
	}
	if err := s.finishSessionReviewerFrame(
		job.reviewerFrame.ID, outcome.status, outcome.description, job.reviewIndex,
	); err != nil {
		return err
	}
	activeRun.settled = true
	return nil
}

func manualReviewFailureDescription(err error) string {
	if errors.Is(err, errManualReviewSourceChanged) {
		return "Manual review stopped because the task or its artifacts changed; start a new review for the current result"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrGenerationStopped) || errors.Is(err, ErrRuntimeDraining) {
		return "Manual review was cancelled; the completed task result is unchanged"
	}
	detail := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(detail, "invalid json"), strings.Contains(detail, "malformed"),
		strings.Contains(detail, "tool-call protocol"), strings.Contains(detail, "invalid tool"):
		return "Reviewer model returned invalid structured tool data after bounded retries; switch models or retry manual review"
	case strings.Contains(detail, "model") && (strings.Contains(detail, "not found") || strings.Contains(detail, "overload")):
		return "Reviewer model is unavailable; switch models or retry manual review"
	default:
		return "Manual review was interrupted; the completed task result is unchanged and review can be retried"
	}
}
