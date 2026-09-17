package server

import (
	"context"
	"errors"
	"log"
	"strings"
	eventjournal "synon-go/internal/persistence/journal"
	workspace "synon-go/internal/persistence/workspace"
	"time"
)

func (s *Server) monitorFrameResumeDispatch(ctx context.Context, cancel context.CancelCauseFunc, done <-chan struct{}, errCh chan<- error, dispatch workspace.CompatibilityFrameResumeDispatch, ttl time.Duration) {
	interval := ttl / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			active, err := s.renewFrameResumeDispatchClaim(ctx, dispatch, ttl)
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				cancel(newSessionRunnerInfrastructureInterruption(
					sessionRunnerResumeDispatchInterruptedReasonCode, err,
				))
				return
			}
			if !active {
				select {
				case errCh <- errFrameResumeDispatchClaimLost:
				default:
				}
				cancel(newSessionRunnerInfrastructureInterruption(
					sessionRunnerResumeDispatchInterruptedReasonCode, errFrameResumeDispatchClaimLost,
				))
				return
			}
		}
	}
}

func (s *Server) renewFrameResumeDispatchClaim(
	ctx context.Context,
	dispatch workspace.CompatibilityFrameResumeDispatch,
	ttl time.Duration,
) (bool, error) {
	retry := 0
	return retryFrameResumeDispatchRenewal(ctx, ttl, func() (bool, error) {
		active, err := s.workspaceStore.RenewCompatibilityFrameResumeDispatch(
			dispatch.ResumeEvent.ID, dispatch.Attempt, dispatch.ClaimToken, ttl,
		)
		if isTransientSQLiteContention(err) {
			retry++
			log.Printf(
				"frame_resume_dispatch_renewal_retry resume_event=%s dispatch_attempt=%d retry=%d error=%v",
				dispatch.ResumeEvent.ID, dispatch.Attempt, retry, err,
			)
		}
		return active, err
	})
}

func retryFrameResumeDispatchRenewal(
	ctx context.Context,
	ttl time.Duration,
	renew func() (bool, error),
) (bool, error) {
	if renew == nil {
		return false, errors.New("resume dispatch renewal function is required")
	}
	delay := frameResumeDispatchRenewRetryDelay
	if ttl > 0 && ttl/10 < delay {
		delay = ttl / 10
	}
	if delay <= 0 {
		delay = time.Millisecond
	}
	for attempt := 1; attempt <= frameResumeDispatchRenewMaxAttempts; attempt++ {
		active, err := renew()
		if err == nil || !isTransientSQLiteContention(err) || attempt == frameResumeDispatchRenewMaxAttempts {
			return active, err
		}
		timer := time.NewTimer(delay * time.Duration(attempt))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false, ctx.Err()
		case <-timer.C:
		}
	}
	return false, errors.New("resume dispatch renewal retry exhausted")
}

func frameResumeRunnerID(eventID string) string {
	return "frame-resume:" + strings.TrimSpace(eventID)
}

func cloneResumeOrchestration(input map[string]any) map[string]any {
	output := make(map[string]any, len(input)+2)
	for key, value := range input {
		output[key] = value
	}
	return output
}

func resumeJournalMessageCount(entries []eventjournal.Entry) int {
	count := 0
	for _, entry := range entries {
		role := strings.TrimSpace(stringValue(entry.Message["role"]))
		if role == "user" || role == "assistant" {
			count++
		}
	}
	return count
}

func latestResumeJournalUserTime(entries []eventjournal.Entry, fallback time.Time) time.Time {
	for index := len(entries) - 1; index >= 0; index-- {
		if strings.TrimSpace(stringValue(entries[index].Message["role"])) != "user" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, entries[index].CreatedAt); err == nil {
			return parsed
		}
		break
	}
	return fallback
}
