package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	runtimekv "synon-go/internal/persistence/runtimekv"
)

type frameTaskModelUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	TotalTokens      int64
	ModelCallCount   int64
	UsageAvailable   bool
}

func (s *Server) compatibilityFrameTaskMetricsProjection(
	ctx context.Context,
	frameID, streamUID, ownerID string,
	inputRevision int64,
) (map[string]any, error) {
	if s == nil || s.transcriptStore == nil {
		return nil, nil
	}
	authority, found, err := s.transcriptStore.GetRunnerTaskMetricsAuthority(
		ctx, streamUID, ownerID, inputRevision,
	)
	if err != nil || !found {
		return nil, err
	}
	projection := map[string]any{
		"runtime_tool_call_count":      authority.LatestToolCallCount,
		"runtime_task_tool_call_count": authority.TaskToolCallCount,
	}
	if s.runtimeStore == nil {
		return projection, nil
	}
	entries, err := s.runtimeStore.ListReadOnly(sessionRunnerModelAuditRuntimeNamespace)
	if err != nil {
		return nil, fmt.Errorf("list task model audits: %w", err)
	}
	taskUsage, err := aggregateFrameTaskModelUsage(entries, frameID, authority.TaskAttempts)
	if err != nil {
		return nil, err
	}
	projectFrameTaskModelUsage(projection, "runtime_task_", taskUsage)
	if len(authority.LatestAttempts) == 0 {
		projection["runtime_model_call_count"] = int64(0)
		return projection, nil
	}
	latestUsage, err := aggregateFrameTaskModelUsage(entries, frameID, authority.LatestAttempts)
	if err != nil {
		return nil, err
	}
	projectFrameTaskModelUsage(projection, "runtime_", latestUsage)
	return projection, nil
}

func projectFrameTaskModelUsage(projection map[string]any, prefix string, usage frameTaskModelUsage) {
	projection[prefix+"model_call_count"] = usage.ModelCallCount
	if !usage.UsageAvailable {
		return
	}
	projection[prefix+"input_tokens"] = usage.InputTokens
	projection[prefix+"output_tokens"] = usage.OutputTokens
	projection[prefix+"cache_read_tokens"] = usage.CacheReadTokens
	projection[prefix+"cache_write_tokens"] = usage.CacheWriteTokens
	projection[prefix+"total_tokens"] = usage.TotalTokens
}

func aggregateFrameTaskModelUsage(
	entries []runtimekv.Entry,
	frameID string,
	attempts []int64,
) (frameTaskModelUsage, error) {
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return frameTaskModelUsage{}, errors.New("frame id is required")
	}
	allowedAttempts := make(map[int64]struct{}, len(attempts))
	for _, attempt := range attempts {
		if attempt <= 0 {
			return frameTaskModelUsage{}, errors.New("task model audit attempt must be positive")
		}
		allowedAttempts[attempt] = struct{}{}
	}
	if len(allowedAttempts) == 0 {
		return frameTaskModelUsage{}, errors.New("at least one task model audit attempt is required")
	}

	var result frameTaskModelUsage
	for _, entry := range entries {
		record, ok := entry.Value.(map[string]any)
		if !ok || strings.TrimSpace(taskMetricStringValue(record, "sessionId")) != frameID {
			continue
		}
		attempt, present, err := nonNegativeAuditInteger(record["attempt"])
		if err != nil || !present || attempt <= 0 {
			if err == nil {
				err = errors.New("positive attempt is required")
			}
			return frameTaskModelUsage{}, fmt.Errorf("invalid task model audit attempt: %w", err)
		}
		if _, ok := allowedAttempts[attempt]; !ok {
			continue
		}
		if result.ModelCallCount == math.MaxInt64 {
			return frameTaskModelUsage{}, errors.New("task model call count overflow")
		}
		result.ModelCallCount++

		input, inputPresent, err := nonNegativeAuditInteger(record["promptTokens"])
		if err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("invalid task prompt token usage: %w", err)
		}
		output, outputPresent, err := nonNegativeAuditInteger(record["completionTokens"])
		if err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("invalid task completion token usage: %w", err)
		}
		cacheRead, cacheReadPresent, err := nonNegativeAuditInteger(record["cacheReadTokens"])
		if err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("invalid task cache-read token usage: %w", err)
		}
		cacheWrite, cacheWritePresent, err := nonNegativeAuditInteger(record["cacheWriteTokens"])
		if err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("invalid task cache-write token usage: %w", err)
		}
		total, totalPresent, err := nonNegativeAuditInteger(record["totalTokens"])
		if err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("invalid task total token usage: %w", err)
		}
		usagePresent := inputPresent || outputPresent || cacheReadPresent || cacheWritePresent || totalPresent
		if !usagePresent {
			continue
		}
		result.UsageAvailable = true
		if !totalPresent {
			total, err = checkedTaskMetricSum(input, output)
			if err != nil {
				return frameTaskModelUsage{}, fmt.Errorf("derive task total token usage: %w", err)
			}
		}
		if result.InputTokens, err = checkedTaskMetricSum(result.InputTokens, input); err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("sum task prompt token usage: %w", err)
		}
		if result.OutputTokens, err = checkedTaskMetricSum(result.OutputTokens, output); err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("sum task completion token usage: %w", err)
		}
		if result.CacheReadTokens, err = checkedTaskMetricSum(result.CacheReadTokens, cacheRead); err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("sum task cache-read token usage: %w", err)
		}
		if result.CacheWriteTokens, err = checkedTaskMetricSum(result.CacheWriteTokens, cacheWrite); err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("sum task cache-write token usage: %w", err)
		}
		if result.TotalTokens, err = checkedTaskMetricSum(result.TotalTokens, total); err != nil {
			return frameTaskModelUsage{}, fmt.Errorf("sum task total token usage: %w", err)
		}
	}
	return result, nil
}

func taskMetricStringValue(record map[string]any, key string) string {
	value, _ := record[key].(string)
	return value
}

func checkedTaskMetricSum(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, errors.New("non-negative task metric overflow")
	}
	return left + right, nil
}

func nonNegativeAuditInteger(value any) (int64, bool, error) {
	if value == nil {
		return 0, false, nil
	}
	var integer int64
	switch typed := value.(type) {
	case int:
		integer = int64(typed)
	case int8:
		integer = int64(typed)
	case int16:
		integer = int64(typed)
	case int32:
		integer = int64(typed)
	case int64:
		integer = typed
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, true, errors.New("integer exceeds int64")
		}
		integer = int64(typed)
	case uint8:
		integer = int64(typed)
	case uint16:
		integer = int64(typed)
	case uint32:
		integer = int64(typed)
	case uint64:
		if typed > math.MaxInt64 {
			return 0, true, errors.New("integer exceeds int64")
		}
		integer = int64(typed)
	case float32:
		floating := float64(typed)
		if math.IsNaN(floating) || math.IsInf(floating, 0) || floating != math.Trunc(floating) || floating > math.MaxInt64 {
			return 0, true, errors.New("value is not an exact int64")
		}
		integer = int64(floating)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) || typed > math.MaxInt64 {
			return 0, true, errors.New("value is not an exact int64")
		}
		integer = int64(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return 0, true, errors.New("value is not an exact int64")
		}
		integer = parsed
	default:
		return 0, true, fmt.Errorf("unsupported numeric type %T", value)
	}
	if integer < 0 {
		return 0, true, errors.New("value must be non-negative")
	}
	return integer, true, nil
}
