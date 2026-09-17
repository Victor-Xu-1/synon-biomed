package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/routinescheduler"
)

const (
	defaultRoutineRunnerPrefix = "synon-go-routine"
	routineSchedulerAuditNS    = "routine-scheduler-audit"
	maxRoutineOutcomeSummary   = 64 << 10
	routineToolRoundAllowance  = 30 * time.Second
	routineFinalizeAllowance   = 30 * time.Second
	routineLeaseSafetyMargin   = 30 * time.Second
	routineMinimumLeaseTTL     = time.Second
)

type RoutineExecutorOptions struct {
	Chat           SessionRunnerChatOptions
	RunnerIDPrefix string
}

type routineExecutor struct {
	server         *Server
	chat           SessionRunnerChatOptions
	runnerIDPrefix string
}

// RoutineExecutionBudget returns zero for the v1.1-compatible unbounded main
// loop or for an unbounded per-round tool batch. The scheduler keeps that claim
// alive with a fenced database heartbeat. Fully bounded configurations retain a
// calculable deadline.
func RoutineExecutionBudget(options SessionRunnerChatOptions) (time.Duration, error) {
	options = normalizeSessionRunnerChatOptions(options)
	if options.RequestTimeout <= 0 || options.MaxToolRounds < 0 || options.MaxAttempts < 1 {
		return 0, errors.New("routine execution budget inputs must be positive")
	}
	if options.MaxToolRounds == 0 || options.MaxToolCallsPerRound == 0 {
		return 0, nil
	}
	// A review correction is a durable continuation in a later tick, so the
	// logical three-bounce budget does not belong to one execution-unit lease.
	// Within this tick, however, the reviewer may run after every completed tool
	// batch and once more for the terminal tail. Budget every such run even
	// though background work will normally overlap the main model.
	mainCandidateCalls := uint64(maxSessionRunnerArtifactReferenceRepairs + 1)
	reviewerRuns, err := checkedPositiveSum(uint64(options.MaxToolRounds), 1)
	if err != nil {
		return 0, fmt.Errorf("routine reviewer run count: %w", err)
	}
	reviewerCallsPerRun, err := checkedPositiveSum(uint64(sessionReviewerMaxToolRounds), 1)
	if err != nil {
		return 0, fmt.Errorf("routine reviewer model call count: %w", err)
	}
	reviewerModelCalls, err := checkedPositiveProduct(reviewerRuns, reviewerCallsPerRun)
	if err != nil {
		return 0, fmt.Errorf("routine reviewer model call budget: %w", err)
	}
	logicalModelCalls, err := checkedPositiveSum(
		uint64(options.MaxToolRounds), mainCandidateCalls, reviewerModelCalls,
	)
	if err != nil {
		return 0, fmt.Errorf("routine logical model call budget: %w", err)
	}
	modelCalls, err := checkedPositiveProduct(logicalModelCalls, uint64(options.MaxAttempts))
	if err != nil {
		return 0, fmt.Errorf("routine model call budget: %w", err)
	}
	modelBudget, err := checkedDurationProduct(options.RequestTimeout, modelCalls)
	if err != nil {
		return 0, fmt.Errorf("routine model timeout budget: %w", err)
	}
	reviewerToolRounds, err := checkedPositiveProduct(reviewerRuns, uint64(sessionReviewerMaxToolRounds))
	if err != nil {
		return 0, fmt.Errorf("routine reviewer tool round count: %w", err)
	}
	toolRounds, err := checkedPositiveSum(uint64(options.MaxToolRounds), reviewerToolRounds)
	if err != nil {
		return 0, fmt.Errorf("routine tool round count: %w", err)
	}
	toolCalls, err := checkedPositiveProduct(toolRounds, uint64(options.MaxToolCallsPerRound))
	if err != nil {
		return 0, fmt.Errorf("routine tool call count: %w", err)
	}
	toolBudget, err := checkedDurationProduct(routineToolRoundAllowance, toolCalls)
	if err != nil {
		return 0, fmt.Errorf("routine tool round budget: %w", err)
	}
	total, err := checkedDurationSum(modelBudget, toolBudget, routineFinalizeAllowance)
	if err != nil {
		return 0, fmt.Errorf("routine execution budget: %w", err)
	}
	return total, nil
}

// RoutineLockTTL returns a lease strictly longer than the complete tick
// budget, while respecting a larger configured lease.
func RoutineLockTTL(configured, executionBudget time.Duration) (time.Duration, error) {
	if executionBudget < 0 {
		return 0, errors.New("routine execution budget must not be negative")
	}
	if executionBudget == 0 {
		if configured <= 0 {
			return 0, errors.New("unbounded routine execution requires a positive renewable lease")
		}
		return configured, nil
	}
	minimum, err := checkedDurationSum(executionBudget, routineLeaseSafetyMargin)
	if err != nil {
		return 0, fmt.Errorf("routine lock ttl: %w", err)
	}
	if configured > minimum {
		return configured, nil
	}
	return minimum, nil
}

// NewRoutineExecutor connects durable routine ticks to the production frame,
// session, Provider authority, and Agent runner path.
func (s *Server) NewRoutineExecutor(options RoutineExecutorOptions) (routinescheduler.Executor, error) {
	if s == nil || s.workspaceStore == nil || s.sessionStore == nil || s.eventJournal == nil || s.secretStore == nil || s.settingsStore == nil {
		return nil, errors.New("routine executor requires workspace, session, journal, settings, and secret stores")
	}
	options.Chat = normalizeSessionRunnerChatOptions(options.Chat)
	// A routine owns a durable session while it may be inside a provider or
	// tool round. A sub-second lease is shorter than the scheduling and JSON
	// persistence jitter of the compatibility session store, so it can expire
	// even while the owner is healthy. Keep the configured value for normal
	// leases, but enforce a bounded floor before claiming and renewing the
	// routine session.
	if options.Chat.LeaseTTL < routineMinimumLeaseTTL {
		options.Chat.LeaseTTL = routineMinimumLeaseTTL
	}
	options.Chat.SessionID = ""
	options.Chat.RunnerID = ""
	prefix := strings.TrimSpace(options.RunnerIDPrefix)
	if prefix == "" {
		prefix = defaultRoutineRunnerPrefix
	}
	return &routineExecutor{server: s, chat: options.Chat, runnerIDPrefix: prefix}, nil
}

func (e *routineExecutor) ExecuteRoutineTick(ctx context.Context, tick routinescheduler.Tick) (routinescheduler.Result, error) {
	if ctx == nil {
		return routinescheduler.Result{}, errors.New("routine executor context is required")
	}
	if err := validateRoutineTick(tick); err != nil {
		return routinescheduler.Result{}, err
	}
	frame, project, err := e.resolveRoutineFrame(tick)
	if err != nil {
		return routinescheduler.Result{}, err
	}
	if recovered, found, err := e.recoverPersistedRoutineOutcome(tick); found {
		if clearErr := e.clearRoutineDeferred(tick); clearErr != nil {
			return routinescheduler.Result{}, clearErr
		}
		return recovered, err
	} else if err != nil {
		return routinescheduler.Result{}, err
	}
	if err := e.ensureRoutineSession(frame, project, tick); err != nil {
		return routinescheduler.Result{}, err
	}
	if reconciled, found, err := e.reconcileDeferredRoutineTick(ctx, frame, tick); found {
		return reconciled, err
	} else if err != nil {
		return routinescheduler.Result{}, err
	}

	runnerID := routineTickRunnerID(e.runnerIDPrefix, tick)
	reserved, claimed, err := e.server.sessionStore.ClaimRunner(frame.ID, runnerID, e.chat.LeaseTTL)
	if err != nil {
		return routinescheduler.Result{}, fmt.Errorf("reserve routine session runner: %w", err)
	}
	if !claimed {
		owner := "unknown"
		if reserved.Runner != nil {
			owner = reserved.Runner.RunnerID
		}
		return routinescheduler.Result{}, fmt.Errorf("routine session is already owned by runner %s", owner)
	}
	claim := sessionstore.RunnerClaimFromSession(reserved)
	releaseOnFailure := true
	defer func() {
		if releaseOnFailure {
			_, _, _ = e.server.sessionStore.ReleaseRunner(claim)
		}
	}()
	runCtx, stopHeartbeat := e.startRoutineSessionHeartbeat(ctx, claim)
	heartbeatStopped := false
	defer func() {
		if !heartbeatStopped {
			_ = stopHeartbeat()
		}
	}()

	messageID := routineTickStableID("message", tick)
	_, _, err = e.server.submitFrameMessage(e.server.workspaceStore, frameMessageSubmission{
		FrameID: frame.ID, MessageUUID: messageID, ClientMessageID: messageID,
		Text: tick.Instruction,
	})
	if err != nil {
		return routinescheduler.Result{}, fmt.Errorf("submit routine instruction: %w", err)
	}

	reserved, found, err := e.server.sessionStore.Get(frame.ID)
	if err != nil || !found {
		return routinescheduler.Result{}, fmt.Errorf("reload reserved routine session: found=%t: %w", found, err)
	}
	recovered, recoveredTick, recoveryErr := e.recoverFinishedRoutineTick(frame.ID, runnerID, reserved, tick)
	if recoveredTick {
		releaseOnFailure = false
		return recovered, recoveryErr
	}
	if recoveryErr != nil {
		return routinescheduler.Result{}, recoveryErr
	}

	chat := e.chat
	chat.SessionID = frame.ID
	chat.RunnerID = runnerID
	runCtx = withAgentRuntimeToolExecutionTimeout(runCtx, routineToolRoundAllowance)
	releaseOnFailure = false
	cycle, err := e.server.runSessionRunnerChatOnce(runCtx, chat, &claim)
	heartbeatErr := stopHeartbeat()
	heartbeatStopped = true
	if heartbeatErr != nil {
		return routinescheduler.Result{}, heartbeatErr
	}
	if err != nil {
		return routinescheduler.Result{}, fmt.Errorf("run routine Agent session: %w", err)
	}
	if !cycle.Claimed {
		return routinescheduler.Result{}, errors.New("routine Agent runner did not retain its reserved session lease")
	}
	if cycle.Status == "awaiting_user_response" {
		if err := e.persistRoutineDeferred(tick, runnerID); err != nil {
			return routinescheduler.Result{}, err
		}
		releaseOnFailure = false
		return routinescheduler.Result{Summary: "awaiting user response", Deferred: true}, nil
	}
	summary, summaryErr := e.runnerCycleSummary(frame.ID, cycle)
	summary = boundRoutineOutcomeSummary(summary)
	if cycle.Status != "completed" {
		if summaryErr != nil {
			return routinescheduler.Result{}, fmt.Errorf("routine Agent run failed and result could not be read: %w", summaryErr)
		}
		if summary == "" {
			summary = "routine Agent run failed"
		}
		if err := e.persistRoutineOutcome(tick, runnerID, cycle, summary, false); err != nil {
			return routinescheduler.Result{}, err
		}
		releaseOnFailure = false
		return routinescheduler.Result{Summary: summary}, errors.New(summary)
	}
	if summaryErr != nil {
		return routinescheduler.Result{}, summaryErr
	}
	if err := e.persistRoutineOutcome(tick, runnerID, cycle, summary, true); err != nil {
		return routinescheduler.Result{}, err
	}
	releaseOnFailure = false
	return routinescheduler.Result{Summary: summary}, nil
}

func (e *routineExecutor) startRoutineSessionHeartbeat(parent context.Context, claim sessionstore.RunnerMutationClaim) (context.Context, func() error) {
	heartbeatCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(sessionRunnerHeartbeatInterval(e.chat.LeaseTTL))
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				session, renewed, err := e.server.sessionStore.HeartbeatRunner(claim, e.chat.LeaseTTL)
				if err != nil {
					if errors.Is(err, sessionstore.ErrRunnerClaimStale) && routineSessionTerminalForClaim(session, claim) {
						return
					}
					select {
					case heartbeatErr <- fmt.Errorf("renew routine session lease: %w", err):
					default:
					}
					cancel()
					return
				}
				if renewed {
					continue
				}
				if routineSessionTerminalForClaim(session, claim) {
					return
				}
				owner := "none"
				if session.Runner != nil {
					owner = session.Runner.RunnerID
				}
				select {
				case heartbeatErr <- fmt.Errorf("routine session lease was lost to runner %s", owner):
				default:
				}
				cancel()
				return
			}
		}
	}()
	var once sync.Once
	return heartbeatCtx, func() error {
		once.Do(cancel)
		<-done
		select {
		case err := <-heartbeatErr:
			return err
		default:
			return nil
		}
	}
}

func routineSessionTerminalForClaim(session sessionstore.Session, claim sessionstore.RunnerMutationClaim) bool {
	runner := session.Runner
	if runner == nil || session.ID != claim.SessionID || runner.RunnerID != claim.RunnerID || runner.Attempt != claim.Attempt ||
		sessionstore.RunnerClaimToken(session.ID, runner) != claim.ClaimToken {
		return false
	}
	return runner.Status == "completed" || runner.Status == "failed" || runner.Status == "cancelled"
}

func validateRoutineTick(tick routinescheduler.Tick) error {
	for label, value := range map[string]string{
		"routine id": tick.RoutineID, "root frame id": tick.RootFrameID,
		"owner user id": tick.OwnerUserID, "on_tick instruction": tick.Instruction,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	if tick.Attempt < 1 {
		return errors.New("routine tick attempt must be positive")
	}
	return nil
}

func (e *routineExecutor) resolveRoutineFrame(tick routinescheduler.Tick) (workspace.Frame, workspace.Project, error) {
	frame, found, err := e.server.workspaceStore.GetFrame(tick.RootFrameID)
	if err != nil {
		return workspace.Frame{}, workspace.Project{}, err
	}
	if !found {
		return workspace.Frame{}, workspace.Project{}, fmt.Errorf("routine root frame %q does not exist", tick.RootFrameID)
	}
	if frame.ID != frame.RootFrameID {
		return workspace.Frame{}, workspace.Project{}, fmt.Errorf("routine frame %q is not a root frame", frame.ID)
	}
	ownerID, found, err := e.server.workspaceStore.ProjectOwnerID(frame.ProjectID)
	if err != nil {
		return workspace.Frame{}, workspace.Project{}, err
	}
	if !found || strings.TrimSpace(ownerID) != strings.TrimSpace(tick.OwnerUserID) {
		return workspace.Frame{}, workspace.Project{}, errors.New("routine owner does not own the root frame project")
	}
	project, found, err := e.server.workspaceStore.GetProject(frame.ProjectID)
	if err != nil {
		return workspace.Frame{}, workspace.Project{}, err
	}
	if !found {
		return workspace.Frame{}, workspace.Project{}, fmt.Errorf("routine project %q does not exist", frame.ProjectID)
	}
	return frame, project, nil
}

func (e *routineExecutor) ensureRoutineSession(frame workspace.Frame, project workspace.Project, tick routinescheduler.Tick) error {
	session, found, err := e.server.sessionStore.Get(frame.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !found {
		title := strings.TrimSpace(frame.Name)
		if title == "" {
			title = frame.ID
		}
		session = sessionstore.Session{ID: frame.ID, Title: title, CreatedAt: now}
	}
	if session.Project != nil && strings.TrimSpace(session.Project.ID) != "" && session.Project.ID != project.ID {
		return fmt.Errorf("routine root session is bound to project %q, not %q", session.Project.ID, project.ID)
	}
	workDir := strings.TrimSpace(project.Path)
	if workDir == "" {
		workDir = e.server.fileRoot
	}
	session.WorkDir = workDir
	session.Project = &sessionstore.Project{ID: project.ID, Name: project.Name, Path: project.Path, BoundAt: now}
	session.Orchestration = copyStringAnyMap(session.Orchestration)
	session.Orchestration["routine"] = map[string]any{
		"routineId": tick.RoutineID, "tickAttempt": tick.Attempt,
		"rootFrameId": tick.RootFrameID, "ownerUserId": tick.OwnerUserID,
	}
	return e.server.sessionStore.Upsert(session)
}

func (e *routineExecutor) recoverFinishedRoutineTick(sessionID, runnerID string, session sessionstore.Session, tick routinescheduler.Tick) (routinescheduler.Result, bool, error) {
	if session.Runner == nil || session.Runner.RunnerID != runnerID {
		return routinescheduler.Result{}, false, nil
	}
	claimToken := runnerCommandClaimToken(session)
	clientID := runnerCommandClientMessageID(runnerID, sessionID, session.Runner.Attempt, claimToken, "chat-finish")
	finished, found, err := e.server.existingRunnerEventByClientMessageID(sessionID, clientID, "runner_finished", runnerID)
	if err != nil || !found {
		return routinescheduler.Result{}, false, err
	}
	status := strings.TrimSpace(stringValue(finished.Message["status"]))
	afterEventID := numberValue(finished.Message["afterEventId"])
	summary, err := e.journalMessageText(sessionID, afterEventID)
	if err != nil {
		return routinescheduler.Result{}, false, err
	}
	cycle := SessionRunnerCycleResult{Claimed: true, SessionID: sessionID, RunnerID: runnerID, Attempt: session.Runner.Attempt, FinishEventID: finished.EventID, Status: status}
	if afterEventID > 0 {
		cycle.AssistantEventID = afterEventID
	}
	successful := status == "completed"
	if !successful && summary == "" {
		summary = strings.TrimSpace(stringValue(finished.Message["text"]))
	}
	summary = boundRoutineOutcomeSummary(summary)
	if err := e.persistRoutineOutcome(tick, runnerID, cycle, summary, successful); err != nil {
		return routinescheduler.Result{}, false, err
	}
	if !successful {
		if summary == "" {
			summary = "routine Agent run failed"
		}
		return routinescheduler.Result{Summary: summary}, true, errors.New(summary)
	}
	return routinescheduler.Result{Summary: summary}, true, nil
}

func (e *routineExecutor) runnerCycleSummary(sessionID string, cycle SessionRunnerCycleResult) (string, error) {
	if cycle.AssistantEventID > 0 {
		return e.journalMessageText(sessionID, cycle.AssistantEventID)
	}
	if cycle.Status == "interrupted" && cycle.CheckpointEventID > 0 {
		summary, err := e.journalMessageText(sessionID, cycle.CheckpointEventID)
		if err != nil || summary != "" {
			return summary, err
		}
	}
	session, found, err := e.server.sessionStore.Get(sessionID)
	if err != nil {
		return "", err
	}
	if found && session.Runner != nil {
		return strings.TrimSpace(session.Runner.LastCheckpoint), nil
	}
	return "", nil
}

func (e *routineExecutor) journalMessageText(sessionID string, eventID int64) (string, error) {
	if eventID <= 0 {
		return "", nil
	}
	entries, err := e.server.eventJournal.ReadAfter(sessionID, eventID-1, 1)
	if err != nil {
		return "", err
	}
	if len(entries) != 1 || entries[0].EventID != eventID {
		return "", fmt.Errorf("routine journal event %d was not found", eventID)
	}
	for _, key := range []string{"resumeDetail", "resume_detail"} {
		if detail := strings.TrimSpace(stringValue(entries[0].Message[key])); detail != "" {
			return detail, nil
		}
	}
	return strings.TrimSpace(runnerMessageText(entries[0].Message)), nil
}

func (e *routineExecutor) persistRoutineOutcome(tick routinescheduler.Tick, runnerID string, cycle SessionRunnerCycleResult, summary string, successful bool) error {
	summary = boundRoutineOutcomeSummary(summary)
	eventType := "routine_tick_failed"
	if successful {
		eventType = "routine_tick_completed"
	}
	event, err := e.server.workspaceStore.AppendFrameEvent(workspace.FrameEventInput{
		ID: routineTickStableID("outcome", tick), FrameID: tick.RootFrameID, Type: eventType,
		Payload: map[string]any{
			"routineId": tick.RoutineID, "tickAttempt": tick.Attempt,
			"runnerId": runnerID, "successful": successful, "summary": summary,
			"sessionId": cycle.SessionID, "assistantEventId": cycle.AssistantEventID,
			"finishEventId": cycle.FinishEventID,
		},
	})
	if err != nil {
		return fmt.Errorf("persist routine frame outcome: %w", err)
	}
	if err := e.server.publishWorkspaceEvent(event); err != nil {
		return fmt.Errorf("publish routine frame outcome: %w", err)
	}
	return nil
}

func (e *routineExecutor) recoverPersistedRoutineOutcome(tick routinescheduler.Tick) (routinescheduler.Result, bool, error) {
	eventID := routineTickStableID("outcome", tick)
	event, found, err := e.server.workspaceStore.GetFrameEventByID(eventID)
	if err != nil || !found {
		return routinescheduler.Result{}, false, err
	}
	if event.FrameID != tick.RootFrameID {
		return routinescheduler.Result{}, true, fmt.Errorf("persisted routine outcome %q belongs to frame %q", eventID, event.FrameID)
	}
	if event.Type != "routine_tick_completed" && event.Type != "routine_tick_failed" {
		return routinescheduler.Result{}, true, fmt.Errorf("persisted routine outcome %q has invalid type %q", eventID, event.Type)
	}
	if strings.TrimSpace(stringValue(event.Payload["routineId"])) != tick.RoutineID {
		return routinescheduler.Result{}, true, fmt.Errorf("persisted routine outcome %q has a different routine id", eventID)
	}
	if attempt := numberValue(event.Payload["tickAttempt"]); attempt != int64(tick.Attempt) {
		return routinescheduler.Result{}, true, fmt.Errorf("persisted routine outcome %q has tick attempt %d, want %d", eventID, attempt, tick.Attempt)
	}
	successful, ok := event.Payload["successful"].(bool)
	if !ok {
		return routinescheduler.Result{}, true, fmt.Errorf("persisted routine outcome %q has no boolean successful field", eventID)
	}
	if successful != (event.Type == "routine_tick_completed") {
		return routinescheduler.Result{}, true, fmt.Errorf("persisted routine outcome %q type and successful field disagree", eventID)
	}
	summary, ok := event.Payload["summary"].(string)
	if !ok {
		return routinescheduler.Result{}, true, fmt.Errorf("persisted routine outcome %q has no string summary", eventID)
	}
	if !utf8.ValidString(summary) || len(summary) > maxRoutineOutcomeSummary {
		return routinescheduler.Result{}, true, fmt.Errorf("persisted routine outcome %q summary is invalid or exceeds %d bytes", eventID, maxRoutineOutcomeSummary)
	}
	if err := e.server.publishWorkspaceEvent(event); err != nil {
		return routinescheduler.Result{}, true, fmt.Errorf("republish persisted routine outcome %q: %w", eventID, err)
	}
	result := routinescheduler.Result{Summary: summary}
	if successful {
		return result, true, nil
	}
	if summary == "" {
		summary = "routine Agent run failed"
		result.Summary = summary
	}
	return result, true, errors.New(summary)
}

func boundRoutineOutcomeSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if len(summary) <= maxRoutineOutcomeSummary {
		return summary
	}
	summary = summary[:maxRoutineOutcomeSummary]
	for len(summary) > 0 && !utf8.ValidString(summary) {
		summary = summary[:len(summary)-1]
	}
	return summary
}

func checkedPositiveProduct(left, right uint64) (uint64, error) {
	if left == 0 || right == 0 {
		return 0, errors.New("factors must be positive")
	}
	if left > math.MaxUint64/right {
		return 0, errors.New("integer overflow")
	}
	return left * right, nil
}

func checkedPositiveSum(values ...uint64) (uint64, error) {
	var total uint64
	for _, value := range values {
		if value == 0 {
			continue
		}
		if total > math.MaxUint64-value {
			return 0, errors.New("integer overflow")
		}
		total += value
	}
	if total == 0 {
		return 0, errors.New("values must include a positive integer")
	}
	return total, nil
}

func checkedDurationProduct(value time.Duration, factor uint64) (time.Duration, error) {
	if value <= 0 || factor == 0 {
		return 0, errors.New("duration and factor must be positive")
	}
	if factor > uint64(math.MaxInt64/int64(value)) {
		return 0, errors.New("duration overflow")
	}
	return time.Duration(uint64(value) * factor), nil
}

func checkedDurationSum(values ...time.Duration) (time.Duration, error) {
	var total time.Duration
	for _, value := range values {
		if value <= 0 {
			return 0, errors.New("durations must be positive")
		}
		if total > time.Duration(math.MaxInt64)-value {
			return 0, errors.New("duration overflow")
		}
		total += value
	}
	return total, nil
}

func routineTickRunnerID(prefix string, tick routinescheduler.Tick) string {
	digest := sha256.Sum256([]byte(tick.RoutineID))
	return fmt.Sprintf("%s-%x-%d", prefix, digest[:6], tick.Attempt)
}

func routineTickStableID(kind string, tick routinescheduler.Tick) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", kind, tick.RoutineID, tick.Attempt)))
	return fmt.Sprintf("routine-%s-%x", kind, digest[:16])
}

func copyStringAnyMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input)+1)
	for key, value := range input {
		output[key] = value
	}
	return output
}

// RecordRoutineSchedulerError writes a bounded production audit record. The
// caller remains responsible for also emitting the error to the process log.
func (s *Server) RecordRoutineSchedulerError(err error) error {
	if err == nil {
		return nil
	}
	if s == nil || s.runtimeStore == nil {
		return errors.New("runtime audit store is not configured")
	}
	now := time.Now().UTC()
	message := err.Error()
	if len(message) > 4096 {
		message = message[:4096]
	}
	_, auditErr := s.runtimeStore.Set(routineSchedulerAuditNS, fmt.Sprintf("error-%d", now.UnixNano()), map[string]any{
		"type": "scheduler_error", "message": message, "at": now,
	})
	return auditErr
}

var _ routinescheduler.Executor = (*routineExecutor)(nil)
