package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
)

const (
	defaultSessionRunnerID               = "synon-go-runner"
	defaultSessionRunnerLeaseTTL         = 5 * time.Minute
	defaultSessionRunnerPollInterval     = time.Second
	defaultSessionRunnerCommandTimeout   = 10 * time.Minute
	defaultSessionRunnerReplayLimit      = int64(200)
	defaultSessionRunnerOutputLimitBytes = int64(1024 * 1024)
)

type SessionRunnerCommandOptions struct {
	RunnerID         string
	Command          string
	Args             []string
	CommandTimeout   time.Duration
	LeaseTTL         time.Duration
	PollInterval     time.Duration
	ReplayLimit      int64
	OutputLimitBytes int64
}

type SessionRunnerCycleResult struct {
	Claimed                bool   `json:"claimed"`
	SessionID              string `json:"sessionId,omitempty"`
	RunnerID               string `json:"runnerId,omitempty"`
	Status                 string `json:"status,omitempty"`
	InterruptionReasonCode string `json:"interruptionReasonCode,omitempty"`
	InterruptionAutoResume bool   `json:"interruptionAutoResume,omitempty"`
	// AwaitingModelSelection distinguishes a pre-provider configuration gap
	// from a provider request that actually ran and failed. The resume dispatcher
	// uses it to park the same task until a model is configured.
	AwaitingModelSelection    bool   `json:"-"`
	AwaitingRecoveryCondition bool   `json:"-"`
	KernelOperationID         string `json:"-"`
	Attempt                   int    `json:"attempt,omitempty"`
	CheckpointEventID         int64  `json:"checkpointEventId,omitempty"`
	AssistantEventID          int64  `json:"assistantEventId,omitempty"`
	FinishEventID             int64  `json:"finishEventId,omitempty"`
	AutoAdvanced              bool   `json:"autoAdvanced,omitempty"`
}

type sessionRunnerCommandInput struct {
	RunnerID    string                `json:"runnerId"`
	Session     sessionstore.Session  `json:"session"`
	Entries     []eventjournal.Entry  `json:"entries"`
	Attempt     int                   `json:"attempt,omitempty"`
	WorkDir     string                `json:"workDir,omitempty"`
	Project     *sessionstore.Project `json:"project,omitempty"`
	NextEventID int64                 `json:"nextEventId,omitempty"`
}

type sessionRunnerCommandOutput struct {
	Status           string `json:"status"`
	Message          string `json:"message"`
	AssistantMessage string `json:"assistantMessage"`
}

func (s *Server) RunSessionRunnerCommandOnce(ctx context.Context, options SessionRunnerCommandOptions) (SessionRunnerCycleResult, error) {
	options = normalizeSessionRunnerCommandOptions(options)
	if strings.TrimSpace(options.Command) == "" {
		return SessionRunnerCycleResult{}, errors.New("session runner command is required")
	}
	if s == nil || s.sessionStore == nil || s.eventJournal == nil {
		return SessionRunnerCycleResult{}, errors.New("session store is not configured")
	}
	if s.isDraining() {
		return SessionRunnerCycleResult{RunnerID: options.RunnerID}, nil
	}
	if _, err := s.recoverCompatibilityQueuedMessages(); err != nil {
		return SessionRunnerCycleResult{}, fmt.Errorf("recover compatibility message queue: %w", err)
	}

	session, claimed, err := s.sessionStore.ClaimNextRunner(options.RunnerID, options.LeaseTTL)
	if err != nil {
		return SessionRunnerCycleResult{}, err
	}
	result := SessionRunnerCycleResult{
		Claimed:  claimed,
		RunnerID: options.RunnerID,
	}
	if !claimed {
		return result, nil
	}
	result.SessionID = session.ID
	claim := sessionstore.RunnerClaimFromSession(session)
	claimToken := claim.ClaimToken
	if session.Runner != nil {
		result.Attempt = session.Runner.Attempt
	}
	activeCtx, activeRun, finishActiveRun, err := s.registerActiveSessionRun(ctx, session.ID, options.RunnerID)
	if err != nil {
		if errors.Is(err, ErrRuntimeDraining) {
			if interruptErr := s.interruptClaimedSessionRunner(
				SessionRunnerChatOptions{RunnerID: options.RunnerID}, &result, &activeSessionRun{}, claim, nil, "runtime_draining",
			); interruptErr != nil {
				return result, interruptErr
			}
			return result, nil
		}
		_, _, _ = s.sessionStore.ReleaseRunner(claim)
		return result, err
	}
	defer finishActiveRun()
	ctx = activeCtx
	if drained, drainErr := s.interruptSessionRunnerForRuntimeDrain(
		ctx, SessionRunnerChatOptions{RunnerID: options.RunnerID}, &result, activeRun, claim, nil,
	); drained {
		if drainErr != nil {
			return result, drainErr
		}
		return result, nil
	}

	entries, err := s.eventJournal.ReadAfter(session.ID, 0, int(options.ReplayLimit))
	if err != nil {
		return result, err
	}
	if len(entries) > 0 {
		result.CheckpointEventID = entries[len(entries)-1].EventID
	}
	checkpoint, err := s.checkpointSessionRunner(map[string]any{
		"sessionId":       session.ID,
		"runnerId":        options.RunnerID,
		"runnerAttempt":   result.Attempt,
		"claimToken":      claimToken,
		"status":          "running",
		"message":         "runner command started",
		"afterEventId":    result.CheckpointEventID,
		"clientMessageId": runnerCommandClientMessageID(options.RunnerID, session.ID, result.Attempt, claimToken, "checkpoint"),
	})
	if err != nil {
		return result, err
	}
	if event, ok := checkpoint["event"].(*eventjournal.Entry); ok && event != nil {
		result.CheckpointEventID = event.EventID
	}

	runCtx, stopHeartbeat := s.startSessionRunnerCommandHeartbeat(ctx, claim, options)
	output, commandErr := runSessionRunnerCommand(runCtx, options, sessionRunnerCommandInput{
		RunnerID:    options.RunnerID,
		Session:     session,
		Entries:     entries,
		Attempt:     result.Attempt,
		WorkDir:     session.WorkDir,
		Project:     session.Project,
		NextEventID: result.CheckpointEventID,
	})
	heartbeatErr := stopHeartbeat()
	if drained, drainErr := s.interruptSessionRunnerForRuntimeDrain(
		ctx, SessionRunnerChatOptions{RunnerID: options.RunnerID}, &result, activeRun, claim, nil,
	); drained {
		if drainErr != nil {
			return result, drainErr
		}
		return result, nil
	}
	if heartbeatErr != nil {
		return result, heartbeatErr
	}
	if commandErr != nil {
		output.Status = "failed"
		output.Message = commandErr.Error()
	}
	status, message := normalizeSessionRunnerCommandOutput(output)
	activeRun.settlement.Lock()
	if drained, drainErr := s.interruptSessionRunnerForRuntimeDrainLocked(
		ctx, SessionRunnerChatOptions{RunnerID: options.RunnerID}, &result, activeRun, claim, nil,
	); drained {
		activeRun.settlement.Unlock()
		if drainErr != nil {
			return result, drainErr
		}
		return result, nil
	}
	if cause := context.Cause(ctx); errors.Is(cause, ErrGenerationStopped) {
		status, message, output.AssistantMessage = "cancelled", cause.Error(), ""
	}
	if output.AssistantMessage != "" {
		assistant, err := s.appendSessionToolEvent(map[string]any{
			"sessionId":       session.ID,
			"role":            "assistant",
			"runnerId":        options.RunnerID,
			"runnerAttempt":   result.Attempt,
			"claimToken":      claimToken,
			"message":         map[string]any{"type": "message", "text": output.AssistantMessage},
			"clientMessageId": runnerCommandClientMessageID(options.RunnerID, session.ID, result.Attempt, claimToken, "assistant"),
		})
		if err != nil {
			status = "failed"
			message = fmt.Sprintf("append assistant message failed: %v", err)
		} else if assistant != nil {
			result.AssistantEventID = assistant.EventID
		}
	}
	if cause := context.Cause(ctx); errors.Is(cause, ErrGenerationStopped) {
		status, message, output.AssistantMessage = "cancelled", cause.Error(), ""
	}
	finished, err := s.finishSessionRunner(map[string]any{
		"sessionId":       session.ID,
		"runnerId":        options.RunnerID,
		"runnerAttempt":   result.Attempt,
		"claimToken":      claimToken,
		"status":          status,
		"message":         message,
		"afterEventId":    maxInt64(result.CheckpointEventID, result.AssistantEventID),
		"clientMessageId": runnerCommandClientMessageID(options.RunnerID, session.ID, result.Attempt, claimToken, "finish"),
	})
	if err != nil {
		activeRun.settlement.Unlock()
		return result, err
	}
	activeRun.settled = true
	activeRun.settlement.Unlock()
	if event, ok := finished["event"].(*eventjournal.Entry); ok && event != nil {
		result.FinishEventID = event.EventID
	}
	result.Status = status
	if status == "completed" {
		advanced, err := s.autoAdvanceTaskRunSession(session.ID, "TaskRun auto-advanced after runner command completion.")
		if err != nil {
			return result, err
		}
		result.AutoAdvanced = advanced
	}
	return result, nil
}

func (s *Server) startSessionRunnerCommandHeartbeat(ctx context.Context, claim sessionstore.RunnerMutationClaim, options SessionRunnerCommandOptions) (context.Context, func() error) {
	if s == nil || s.sessionStore == nil {
		return ctx, func() error { return nil }
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(sessionRunnerHeartbeatInterval(options.LeaseTTL))
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				session, renewed, err := s.sessionStore.HeartbeatRunner(claim, options.LeaseTTL)
				if err == nil && renewed {
					continue
				}
				if err == nil {
					owner := "none"
					if session.Runner != nil {
						owner = session.Runner.RunnerID
					}
					err = fmt.Errorf("runner command lease was lost to runner %s", owner)
				} else {
					err = fmt.Errorf("renew runner command lease: %w", err)
				}
				select {
				case heartbeatErr <- err:
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
		}
		// Close the race between the last timer tick and command settlement.
		// HeartbeatRunner is fenced, so this renews only while this exact
		// runner attempt still owns the session.
		session, renewed, err := s.sessionStore.HeartbeatRunner(claim, options.LeaseTTL)
		if err == nil && renewed {
			return nil
		}
		if err == nil {
			owner := "none"
			if session.Runner != nil {
				owner = session.Runner.RunnerID
			}
			return fmt.Errorf("runner command lease was lost to runner %s", owner)
		}
		return fmt.Errorf("renew runner command lease before settlement: %w", err)
	}
}

func sessionRunnerHeartbeatInterval(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		ttl = defaultSessionRunnerLeaseTTL
	}
	interval := ttl / 4
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	if interval >= ttl {
		interval = ttl / 2
		if interval <= 0 {
			interval = time.Nanosecond
		}
	}
	if interval > 30*time.Second {
		return 30 * time.Second
	}
	return interval
}

func (s *Server) RunSessionRunnerCommandLoop(ctx context.Context, options SessionRunnerCommandOptions) error {
	options = normalizeSessionRunnerCommandOptions(options)
	ticker := time.NewTicker(options.PollInterval)
	defer ticker.Stop()

	for {
		if s.isDraining() {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		result, err := s.RunSessionRunnerCommandOnce(ctx, options)
		if err != nil {
			return err
		}
		if result.Claimed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func runSessionRunnerCommand(ctx context.Context, options SessionRunnerCommandOptions, input sessionRunnerCommandInput) (sessionRunnerCommandOutput, error) {
	rawInput, err := json.Marshal(input)
	if err != nil {
		return sessionRunnerCommandOutput{}, err
	}
	runCtx := ctx
	cancel := func() {}
	if options.CommandTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, options.CommandTimeout)
	}
	defer cancel()

	cmd := exec.Command(options.Command, options.Args...)
	if input.WorkDir != "" {
		cmd.Dir = input.WorkDir
	}
	cmd.Stdin = bytes.NewReader(rawInput)
	stdout := newBoundedCommandBuffer(options.OutputLimitBytes)
	stderr := newBoundedCommandBuffer(options.OutputLimitBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	process, err := startRunnerProcess(cmd)
	if err != nil {
		return sessionRunnerCommandOutput{}, fmt.Errorf("start runner command: %w", err)
	}
	defer process.close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-runCtx.Done():
		killErr := process.kill()
		err = <-done
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			if killErr != nil {
				return sessionRunnerCommandOutput{}, fmt.Errorf("runner command timed out after %s and process cleanup failed: %w", options.CommandTimeout, killErr)
			}
			return sessionRunnerCommandOutput{}, fmt.Errorf("runner command timed out after %s", options.CommandTimeout)
		}
		if killErr != nil {
			return sessionRunnerCommandOutput{}, fmt.Errorf("runner command cancelled and process cleanup failed: %w", killErr)
		}
		return sessionRunnerCommandOutput{}, runCtx.Err()
	}
	rawOutput := bytes.TrimSpace(stdout.Bytes())
	if len(rawOutput) == 0 {
		if err != nil {
			return sessionRunnerCommandOutput{}, fmt.Errorf("runner command failed: %w: %s", err, strings.TrimSpace(string(stderr.Bytes())))
		}
		return sessionRunnerCommandOutput{Status: "completed", Message: "runner command completed"}, nil
	}
	var output sessionRunnerCommandOutput
	if decodeErr := json.Unmarshal(rawOutput, &output); decodeErr != nil {
		return sessionRunnerCommandOutput{}, fmt.Errorf("decode runner command output: %w: %s", decodeErr, string(rawOutput))
	}
	if err != nil {
		return output, fmt.Errorf("runner command failed: %w: %s", err, strings.TrimSpace(string(stderr.Bytes())))
	}
	return output, nil
}

func normalizeSessionRunnerCommandOptions(options SessionRunnerCommandOptions) SessionRunnerCommandOptions {
	options.RunnerID = strings.TrimSpace(options.RunnerID)
	if options.RunnerID == "" {
		options.RunnerID = defaultSessionRunnerID
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = defaultSessionRunnerLeaseTTL
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultSessionRunnerPollInterval
	}
	if options.CommandTimeout <= 0 {
		options.CommandTimeout = defaultSessionRunnerCommandTimeout
	}
	if options.ReplayLimit <= 0 {
		options.ReplayLimit = defaultSessionRunnerReplayLimit
	}
	if options.OutputLimitBytes <= 0 {
		options.OutputLimitBytes = defaultSessionRunnerOutputLimitBytes
	}
	options.Command = strings.TrimSpace(options.Command)
	return options
}

func normalizeSessionRunnerCommandOutput(output sessionRunnerCommandOutput) (string, string) {
	status := strings.TrimSpace(output.Status)
	switch status {
	case "", "completed":
		status = "completed"
	case "failed":
	default:
		return "failed", fmt.Sprintf("unsupported runner command status: %s", status)
	}
	message := strings.TrimSpace(output.Message)
	if message == "" {
		if status == "completed" {
			message = "runner command completed"
		} else {
			message = "runner command failed"
		}
	}
	return status, message
}

func runnerCommandClaimToken(session sessionstore.Session) string {
	return sessionstore.RunnerClaimToken(session.ID, session.Runner)
}

func runnerCommandClientMessageID(runnerID string, sessionID string, attempt int, claimToken string, phase string) string {
	return fmt.Sprintf("%s:%s:%d:%s:%s", runnerID, sessionID, attempt, claimToken, phase)
}

func maxInt64(left int64, right int64) int64 {
	if right > left {
		return right
	}
	return left
}

type boundedCommandBuffer struct {
	limit     int64
	remaining int64
	buffer    bytes.Buffer
}

func newBoundedCommandBuffer(limit int64) *boundedCommandBuffer {
	if limit <= 0 {
		limit = defaultSessionRunnerOutputLimitBytes
	}
	return &boundedCommandBuffer{limit: limit, remaining: limit}
}

func (b *boundedCommandBuffer) Write(data []byte) (int, error) {
	if b.remaining > 0 {
		toWrite := data
		if int64(len(toWrite)) > b.remaining {
			toWrite = toWrite[:b.remaining]
		}
		_, _ = b.buffer.Write(toWrite)
		b.remaining -= int64(len(toWrite))
	}
	return len(data), nil
}

func (b *boundedCommandBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}
