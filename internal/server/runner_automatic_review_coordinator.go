package server

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const sessionAutomaticReviewMinInterval = 2 * time.Minute

type sessionAutomaticReviewJob struct {
	Start    int
	End      int
	Messages []agentruntime.Message
}

type sessionAutomaticReviewUnit struct {
	Window          sessionReviewerTranscriptWindow
	Review          sessionRunnerReview
	Binding         map[string]any
	ReviewerFrameID string
}

type sessionAutomaticReviewState struct {
	ReviewIndex     int
	CoveredThrough  int
	Units           []sessionAutomaticReviewUnit
	ReviewerFrame   *workspace.Frame
	ReviewerClaim   transcriptstore.RunnerClaim
	ReviewerContext context.Context
	ReviewerBound   bool
	stopHeartbeat   func() error
}

func (state *sessionAutomaticReviewState) stopReviewerHeartbeat() error {
	if state == nil || state.stopHeartbeat == nil {
		return nil
	}
	return state.stopHeartbeat()
}

type sessionAutomaticReviewCoordinator struct {
	server          *Server
	ctx             context.Context
	cancel          context.CancelCauseFunc
	session         sessionstore.Session
	plan            sessionRunnerTaskContract
	reviewRun       *sessionRunnerChatRun
	reviewIndex     int
	reviewerOptions SessionRunnerChatOptions

	mu              sync.Mutex
	cond            *sync.Cond
	messages        []agentruntime.Message
	cursor          int
	pendingToolCall map[string]struct{}
	jobs            []sessionAutomaticReviewJob
	closing         bool
	abort           bool
	done            chan struct{}
	closingSignal   chan struct{}
	closeSignalOnce sync.Once

	reviewerFrame   *workspace.Frame
	reviewerClaim   transcriptstore.RunnerClaim
	reviewerContext context.Context
	reviewerBound   bool
	stopHeartbeat   func() error
	units           []sessionAutomaticReviewUnit
	coveredThrough  int
	sequence        int
}

func (s *Server) newSessionAutomaticReviewCoordinator(
	ctx context.Context,
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	requestMessages []agentruntime.Message,
	plan sessionRunnerTaskContract,
	run *sessionRunnerChatRun,
) (*sessionAutomaticReviewCoordinator, error) {
	if s == nil || run == nil || run.Transcript == nil || run.ReviewPolicy == nil ||
		!sessionRunnerReviewPolicyRequired(*run.ReviewPolicy) {
		return nil, nil
	}
	reviewerOptions, err := s.resolveSessionReviewerOptions(ctx, session, options, sessionRunnerAttempt(run))
	if err != nil {
		return nil, err
	}
	reviewIndex, err := s.transcriptStore.NextRunnerReviewIndex(
		ctx, run.Transcript.Stream.UID, run.Transcript.Stream.OwnerID, run.Transcript.Claim.Attempt,
	)
	if err != nil {
		return nil, err
	}
	reviewCtx, cancel := context.WithCancelCause(ctx)
	reviewRun := &sessionRunnerChatRun{
		SessionID: run.SessionID, Attempt: run.Attempt, ClaimToken: run.ClaimToken,
		AfterEventID: run.AfterEventID, Transcript: run.Transcript,
		TaskIntent: run.TaskIntent, TaskIntentID: run.TaskIntentID,
		TaskIntentRevision: run.TaskIntentRevision, ReviewPolicy: run.ReviewPolicy,
		SuppressReviewCheckpoints: true,
	}
	coordinator := &sessionAutomaticReviewCoordinator{
		server: s, ctx: reviewCtx, cancel: cancel, session: session,
		plan: plan, reviewRun: reviewRun, reviewIndex: reviewIndex, reviewerOptions: reviewerOptions,
		messages: append([]agentruntime.Message(nil), requestMessages...),
		cursor:   len(requestMessages), coveredThrough: len(requestMessages),
		pendingToolCall: make(map[string]struct{}), done: make(chan struct{}),
		closingSignal: make(chan struct{}),
	}
	coordinator.cond = sync.NewCond(&coordinator.mu)
	go coordinator.run()
	return coordinator, nil
}

func (coordinator *sessionAutomaticReviewCoordinator) observe(event agentruntime.Event) {
	if coordinator == nil {
		return
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closing || coordinator.abort {
		return
	}
	switch event.Type {
	case agentruntime.EventModelResponse:
		message := agentruntime.Message{
			Role: "assistant", Content: strings.TrimSpace(event.Message),
			ToolCalls: append([]agentruntime.ToolCall(nil), event.ToolCalls...),
		}
		coordinator.messages = append(coordinator.messages, message)
		for _, call := range event.ToolCalls {
			if callID := strings.TrimSpace(call.ID); callID != "" {
				coordinator.pendingToolCall[callID] = struct{}{}
			}
		}
	case agentruntime.EventToolCompleted, agentruntime.EventToolFailed:
		callID := strings.TrimSpace(event.ToolCallID)
		if _, pending := coordinator.pendingToolCall[callID]; !pending {
			return
		}
		content := strings.TrimSpace(event.Result)
		if content == "" {
			content = fmt.Sprintf(`{"status":%q,"tool":%q}`, string(event.Type), strings.TrimSpace(event.ToolName))
		}
		coordinator.messages = append(coordinator.messages, agentruntime.Message{
			Role: "tool", ToolCallID: callID, Content: content,
		})
		delete(coordinator.pendingToolCall, callID)
		if len(coordinator.pendingToolCall) == 0 && coordinator.cursor < len(coordinator.messages) {
			coordinator.jobs = append(coordinator.jobs, sessionAutomaticReviewJob{
				Start: coordinator.cursor, End: len(coordinator.messages),
				Messages: append([]agentruntime.Message(nil), coordinator.messages[coordinator.cursor:]...),
			})
			coordinator.cursor = len(coordinator.messages)
			coordinator.cond.Signal()
		}
	}
}

func (coordinator *sessionAutomaticReviewCoordinator) close(success bool) *sessionAutomaticReviewState {
	if coordinator == nil {
		return nil
	}
	coordinator.mu.Lock()
	coordinator.closing = true
	coordinator.abort = !success
	coordinator.closeSignalOnce.Do(func() { close(coordinator.closingSignal) })
	if !success {
		coordinator.cancel(context.Canceled)
	}
	coordinator.cond.Broadcast()
	coordinator.mu.Unlock()
	<-coordinator.done

	coordinator.mu.Lock()
	state := &sessionAutomaticReviewState{
		ReviewIndex: coordinator.reviewIndex, CoveredThrough: coordinator.coveredThrough,
		ReviewerClaim: coordinator.reviewerClaim, ReviewerContext: coordinator.reviewerContext,
		ReviewerBound: coordinator.reviewerBound, stopHeartbeat: coordinator.stopHeartbeat,
	}
	if coordinator.reviewerFrame != nil {
		frame := *coordinator.reviewerFrame
		state.ReviewerFrame = &frame
	}
	for _, unit := range coordinator.units {
		if unit.Window.End <= coordinator.coveredThrough {
			state.Units = append(state.Units, unit)
		}
	}
	coordinator.mu.Unlock()
	if success {
		return state
	}
	if err := state.stopReviewerHeartbeat(); err != nil {
		log.Printf("automatic reviewer heartbeat stop failed: %v", err)
	}
	if state.ReviewerFrame != nil {
		_ = coordinator.server.finishSessionReviewerFrame(
			state.ReviewerFrame.ID, "cancelled",
			"Automatic review was cancelled with the root task", state.ReviewIndex,
		)
	}
	return nil
}

func (coordinator *sessionAutomaticReviewCoordinator) run() {
	defer close(coordinator.done)
	var lastReviewStarted time.Time
	for {
		coordinator.mu.Lock()
		for len(coordinator.jobs) == 0 && !coordinator.closing {
			coordinator.cond.Wait()
		}
		if coordinator.abort || len(coordinator.jobs) == 0 && coordinator.closing {
			coordinator.mu.Unlock()
			return
		}
		closing := coordinator.closing
		coordinator.mu.Unlock()
		if !closing && !lastReviewStarted.IsZero() {
			remaining := time.Until(lastReviewStarted.Add(sessionAutomaticReviewMinInterval))
			if remaining > 0 {
				timer := time.NewTimer(remaining)
				select {
				case <-timer.C:
				case <-coordinator.closingSignal:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
				case <-coordinator.ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return
				}
			}
		}
		coordinator.mu.Lock()
		if coordinator.abort {
			coordinator.mu.Unlock()
			return
		}
		jobs := append([]sessionAutomaticReviewJob(nil), coordinator.jobs...)
		coordinator.jobs = nil
		coordinator.mu.Unlock()

		job := combineSessionAutomaticReviewJobs(jobs)
		lastReviewStarted = time.Now()
		unit, err := coordinator.review(job)
		coordinator.mu.Lock()
		if err != nil {
			log.Printf("automatic reviewer checkpoint failed: start=%d end=%d err=%v", job.Start, job.End, err)
		} else {
			coordinator.units = append(coordinator.units, unit)
			if job.Start == coordinator.coveredThrough {
				coordinator.coveredThrough = job.End
			}
		}
		coordinator.mu.Unlock()
	}
}

func combineSessionAutomaticReviewJobs(jobs []sessionAutomaticReviewJob) sessionAutomaticReviewJob {
	if len(jobs) == 0 {
		return sessionAutomaticReviewJob{}
	}
	combined := sessionAutomaticReviewJob{Start: jobs[0].Start, End: jobs[len(jobs)-1].End}
	for _, job := range jobs {
		combined.Messages = append(combined.Messages, job.Messages...)
	}
	return combined
}

func (coordinator *sessionAutomaticReviewCoordinator) review(
	job sessionAutomaticReviewJob,
) (sessionAutomaticReviewUnit, error) {
	if len(job.Messages) == 0 || job.End <= job.Start {
		return sessionAutomaticReviewUnit{}, fmt.Errorf("automatic review checkpoint is invalid: empty checkpoint window")
	}
	if err := coordinator.ensureReviewer(); err != nil {
		return sessionAutomaticReviewUnit{}, err
	}
	rootFrameID, err := coordinator.server.sessionRunnerVerificationRoot(coordinator.session.ID)
	if err != nil {
		return sessionAutomaticReviewUnit{}, err
	}
	workspaceEvidence, err := coordinator.server.sessionReviewerWorkspaceEvidence(coordinator.session, rootFrameID)
	if err != nil {
		return sessionAutomaticReviewUnit{}, err
	}
	candidate := "Automatic review checkpoint after recorded tool execution."
	for index := len(job.Messages) - 1; index >= 0; index-- {
		if job.Messages[index].Role == "assistant" && strings.TrimSpace(job.Messages[index].Content) != "" {
			candidate = strings.TrimSpace(job.Messages[index].Content)
			break
		}
	}
	binding, err := sessionRunnerVerificationSourceRef(
		rootFrameID, coordinator.session.ID, coordinator.reviewIndex, candidate,
		coordinator.reviewRun, workspaceEvidence,
	)
	if err != nil {
		return sessionAutomaticReviewUnit{}, err
	}
	coordinator.mu.Lock()
	sequence := coordinator.sequence
	coordinator.sequence++
	frameID := coordinator.reviewerFrame.ID
	reviewerCtx := coordinator.reviewerContext
	coordinator.mu.Unlock()
	binding["review_scope"] = "logical_task_checkpoint"
	binding["automatic_checkpoint_sequence"] = sequence
	binding["message_start"] = job.Start
	binding["message_end"] = job.End
	result := agentruntime.RunResult{FinalMessage: agentruntime.Message{Role: "assistant", Content: candidate}}
	review, verifiedBinding, err := coordinator.server.reviewSessionRunnerCompletion(
		reviewerCtx, coordinator.session, coordinator.reviewerOptions, result, job.Messages,
		coordinator.plan, coordinator.reviewRun, workspaceEvidence, binding,
		coordinator.reviewIndex, frameID, agentruntime.NewToolRoundBudget(sessionReviewerMaxToolRounds),
	)
	if err != nil {
		return sessionAutomaticReviewUnit{}, err
	}
	verifiedBinding["review_scope"] = "logical_task_checkpoint"
	verifiedBinding["automatic_checkpoint_sequence"] = sequence
	verifiedBinding["message_start"] = job.Start
	verifiedBinding["message_end"] = job.End
	return sessionAutomaticReviewUnit{
		Window: sessionReviewerTranscriptWindow{Start: job.Start, End: job.End},
		Review: review, Binding: verifiedBinding, ReviewerFrameID: frameID,
	}, nil
}

func (coordinator *sessionAutomaticReviewCoordinator) ensureReviewer() error {
	coordinator.mu.Lock()
	if coordinator.reviewerFrame != nil && coordinator.reviewerBound {
		coordinator.mu.Unlock()
		return nil
	}
	frame := coordinator.reviewerFrame
	claim := coordinator.reviewerClaim
	reviewerCtx := coordinator.reviewerContext
	coordinator.mu.Unlock()

	if frame == nil {
		created, createdClaim, err := coordinator.server.beginSessionReviewerFrameWithClaim(
			coordinator.ctx, coordinator.session, coordinator.reviewerOptions.Model,
			coordinator.reviewRun.Attempt, coordinator.reviewIndex,
		)
		if err != nil {
			return err
		}
		frame = &created
		claim = createdClaim
		reviewerCtx, coordinator.stopHeartbeat = coordinator.server.startSessionReviewerHeartbeat(
			coordinator.ctx, claim,
		)
		coordinator.mu.Lock()
		coordinator.reviewerFrame = frame
		coordinator.reviewerClaim = claim
		coordinator.reviewerContext = reviewerCtx
		coordinator.mu.Unlock()
	}
	boundCtx, err := coordinator.server.bindSessionReviewerRun(reviewerCtx, *frame, claim)
	if err != nil {
		return err
	}
	coordinator.mu.Lock()
	coordinator.reviewerContext = boundCtx
	coordinator.reviewerBound = true
	coordinator.mu.Unlock()
	return nil
}

func combineSessionAutomaticAndTerminalReviews(
	baseBinding map[string]any,
	background []sessionAutomaticReviewUnit,
	terminalReview sessionRunnerReview,
	terminalBinding map[string]any,
	terminalStart, terminalEnd int,
	targetTranscriptSHA256 string,
) (sessionRunnerReview, map[string]any, error) {
	reviews := make([]sessionRunnerReview, 0, len(background)+1)
	windows := make([]sessionReviewerTranscriptWindow, 0, len(background)+1)
	bindings := make([]map[string]any, 0, len(background)+1)
	frameIDs := make([]string, 0, len(background))
	for _, unit := range background {
		reviews = append(reviews, unit.Review)
		windows = append(windows, unit.Window)
		bindings = append(bindings, unit.Binding)
		frameIDs = append(frameIDs, unit.ReviewerFrameID)
	}
	terminalBinding = copyMapAny(terminalBinding)
	terminalBinding["message_start"] = terminalStart
	terminalBinding["message_end"] = terminalEnd
	reviews = append(reviews, terminalReview)
	windows = append(windows, sessionReviewerTranscriptWindow{Start: terminalStart, End: terminalEnd})
	bindings = append(bindings, terminalBinding)
	aggregatedReview := aggregateSessionReviewerReviews(reviews, windows)
	aggregatedBinding, err := aggregateSessionReviewerBindings(
		baseBinding, bindings, targetTranscriptSHA256, terminalEnd,
	)
	if err != nil {
		return sessionRunnerReview{}, nil, err
	}
	aggregatedBinding["review_scope"] = "logical_task_terminal"
	aggregatedBinding["automatic_checkpoint_count"] = len(background)
	aggregatedBinding["background_reviewer_frame_ids"] = uniqueSortedFolded(frameIDs)
	aggregatedBinding["review_verdict"] = aggregatedReview.Verdict
	return aggregatedReview, aggregatedBinding, nil
}
