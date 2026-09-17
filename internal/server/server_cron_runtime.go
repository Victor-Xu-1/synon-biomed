package server

import (
	"crypto/sha256"

	"encoding/hex"

	"errors"
	"fmt"

	"sort"
	"strconv"
	"strings"

	"time"
)

const cronRuntimeNamespace = "scheduled-tasks"

const maxCronJobs = 50

func (s *Server) executeCronTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.runtimeStore == nil {
		return nil, errors.New("cron runtime store is not configured")
	}
	switch toolName {
	case "CronCreate":
		cron := strings.TrimSpace(stringValue(input["cron"]))
		if err := validateCronExpression(cron); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		if _, ok := nextCronRunAfter(cron, now); !ok {
			return nil, fmt.Errorf("Cron expression '%s' does not match any calendar date in the next year.", cron)
		}
		prompt := stringValue(input["prompt"])
		if strings.TrimSpace(prompt) == "" {
			return nil, errors.New("CronCreate prompt is required")
		}
		existing, err := s.runtimeStore.List(cronRuntimeNamespace)
		if err != nil {
			return nil, err
		}
		if len(existing) >= maxCronJobs {
			return nil, fmt.Errorf("Too many scheduled jobs (max %d). Cancel one first.", maxCronJobs)
		}
		id := cronJobID(cron, prompt, now)
		recurring := boolValue(input["recurring"], true)
		durable := boolValue(input["durable"], false)
		job := map[string]any{
			"id":            id,
			"cron":          cron,
			"humanSchedule": cronToHuman(cron),
			"prompt":        prompt,
			"recurring":     recurring,
			"durable":       durable,
			"createdAt":     now.Format(time.RFC3339Nano),
			"updatedAt":     now.Format(time.RFC3339Nano),
		}
		if _, err := s.runtimeStore.Set(cronRuntimeNamespace, id, job); err != nil {
			return nil, err
		}
		return map[string]any{"data": map[string]any{"id": id, "humanSchedule": job["humanSchedule"], "recurring": recurring, "durable": durable}, "job": job}, nil
	case "CronList":
		jobs, err := s.listCronJobs()
		if err != nil {
			return nil, err
		}
		return map[string]any{"data": map[string]any{"jobs": jobs}}, nil
	case "CronUpdate":
		id := strings.TrimSpace(stringValue(input["id"]))
		entry, ok, err := s.runtimeStore.Get(cronRuntimeNamespace, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("No scheduled job with id '%s'", id)
		}
		job := mapValue(entry.Value)
		if rawCron, ok := input["cron"]; ok {
			cron := strings.TrimSpace(stringValue(rawCron))
			if err := validateCronExpression(cron); err != nil {
				return nil, err
			}
			if _, ok := nextCronRunAfter(cron, time.Now().UTC()); !ok {
				return nil, fmt.Errorf("Cron expression '%s' does not match any calendar date in the next year.", cron)
			}
			job["cron"] = cron
			job["humanSchedule"] = cronToHuman(cron)
		}
		for _, key := range []string{"prompt", "name", "description", "folder", "model", "permissionMode", "frequency", "scheduledTime"} {
			if value, ok := input[key]; ok {
				job[key] = stringValue(value)
			}
		}
		for _, key := range []string{"worktree", "recurring"} {
			if _, ok := input[key]; ok {
				job[key] = boolValue(input[key], false)
			}
		}
		job["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := s.runtimeStore.Set(cronRuntimeNamespace, id, job); err != nil {
			return nil, err
		}
		return map[string]any{"data": map[string]any{"id": id, "humanSchedule": firstNonEmpty(stringValue(job["humanSchedule"]), cronToHuman(stringValue(job["cron"]))), "updated": true}, "job": job}, nil
	case "CronDelete":
		id := strings.TrimSpace(stringValue(input["id"]))
		deleted, err := s.runtimeStore.Delete(cronRuntimeNamespace, id)
		if err != nil {
			return nil, err
		}
		if !deleted {
			return nil, fmt.Errorf("No scheduled job with id '%s'", id)
		}
		return map[string]any{"data": map[string]any{"id": id}}, nil
	case "cron_tick":
		return s.executeCronTick(input)
	default:
		return nil, fmt.Errorf("unknown cron tool: %s", toolName)
	}
}

func (s *Server) executeCronTick(input map[string]any) (any, error) {
	if s.taskRunStore == nil {
		return nil, errors.New("TaskRun store is not configured")
	}
	now := time.Now().UTC()
	if rawNow := strings.TrimSpace(stringValue(input["now"])); rawNow != "" {
		parsed, err := time.Parse(time.RFC3339Nano, rawNow)
		if err != nil {
			return nil, fmt.Errorf("cron_tick now must be RFC3339: %w", err)
		}
		now = parsed.UTC()
	}
	limit := int(numberValue(input["limit"]))
	if limit <= 0 {
		limit = 50
	}
	entries, err := s.runtimeStore.List(cronRuntimeNamespace)
	if err != nil {
		return nil, err
	}
	fired := []map[string]any{}
	for _, entry := range entries {
		if len(fired) >= limit {
			break
		}
		job := mapValue(entry.Value)
		id := firstNonEmpty(stringValue(job["id"]), entry.Key)
		cronExpr := strings.TrimSpace(stringValue(job["cron"]))
		prompt := stringValue(job["prompt"])
		if id == "" || cronExpr == "" || strings.TrimSpace(prompt) == "" {
			continue
		}
		anchor, ok := cronAnchorTime(job, entry.UpdatedAt)
		if !ok {
			continue
		}
		next, ok := nextCronRunAfter(cronExpr, anchor)
		if !ok || next.After(now) {
			continue
		}
		queued, err := s.executeTaskRunTool(map[string]any{
			"action":    "start",
			"objective": "Scheduled cron " + id,
			"message":   prompt,
			"success_criteria": []any{
				"Scheduled prompt is completed or a concrete blocker is recorded.",
			},
			"constraints": []any{
				"Started by cron_tick from scheduled task " + id + ".",
			},
			"task_graph": map[string]any{"steps": []any{map[string]any{
				"id":               "scheduled-prompt",
				"title":            "Execute scheduled prompt",
				"description":      prompt,
				"intent":           "scheduled_prompt",
				"expected_output":  "A completed response or an explicit durable blocker.",
				"acceptance_check": "The scheduled prompt is answered without changing its objective.",
				"risk_level":       "medium",
				"max_recoveries":   1,
				"executor": map[string]any{
					"kind":   "agent",
					"prompt": prompt,
				},
			}}},
		})
		if err != nil {
			return nil, err
		}
		recurring := boolValue(job["recurring"], false)
		if recurring {
			job["lastFiredAt"] = now.Format(time.RFC3339Nano)
			job["updatedAt"] = now.Format(time.RFC3339Nano)
			if _, err := s.runtimeStore.Set(cronRuntimeNamespace, id, job); err != nil {
				return nil, err
			}
		} else {
			if _, err := s.runtimeStore.Delete(cronRuntimeNamespace, id); err != nil {
				return nil, err
			}
		}
		fired = append(fired, map[string]any{
			"id":        id,
			"cron":      cronExpr,
			"prompt":    prompt,
			"recurring": recurring,
			"scheduled": next.Format(time.RFC3339Nano),
			"firedAt":   now.Format(time.RFC3339Nano),
			"taskRun":   queued,
		})
	}
	return map[string]any{
		"fired_count":  float64(len(fired)),
		"queued_count": float64(len(fired)),
		"fired":        fired,
		"now":          now.Format(time.RFC3339Nano),
	}, nil
}

func cronAnchorTime(job map[string]any, fallback time.Time) (time.Time, bool) {
	for _, key := range []string{"lastFiredAt", "createdAt"} {
		if parsed, ok := cronTimeValue(job[key]); ok {
			return parsed, true
		}
	}
	if !fallback.IsZero() {
		return fallback.UTC(), true
	}
	return time.Time{}, false
}

func cronTimeValue(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err != nil {
			return time.Time{}, false
		}
		return parsed.UTC(), true
	case float64:
		return time.UnixMilli(int64(typed)).UTC(), true
	case int64:
		return time.UnixMilli(typed).UTC(), true
	case int:
		return time.UnixMilli(int64(typed)).UTC(), true
	default:
		return time.Time{}, false
	}
}

func nextCronRunAfter(expression string, after time.Time) (time.Time, bool) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return time.Time{}, false
	}
	start := after.In(time.Local).Truncate(time.Minute).Add(time.Minute)
	end := start.Add(366 * 24 * time.Hour)
	for candidate := start; !candidate.After(end); candidate = candidate.Add(time.Minute) {
		if cronFieldsMatch(fields, candidate) {
			return candidate.UTC(), true
		}
	}
	return time.Time{}, false
}

func cronFieldsMatch(fields []string, value time.Time) bool {
	local := value.In(time.Local)
	weekday := int(local.Weekday())
	return cronFieldMatches(fields[0], local.Minute(), 0, 59) &&
		cronFieldMatches(fields[1], local.Hour(), 0, 23) &&
		cronFieldMatches(fields[2], local.Day(), 1, 31) &&
		cronFieldMatches(fields[3], int(local.Month()), 1, 12) &&
		(cronFieldMatches(fields[4], weekday, 0, 7) || (weekday == 0 && cronFieldMatches(fields[4], 7, 0, 7)))
}

func cronFieldMatches(field string, value int, min int, max int) bool {
	for _, part := range strings.Split(field, ",") {
		base := part
		step := 1
		if strings.Contains(base, "/") {
			pieces := strings.Split(base, "/")
			if len(pieces) != 2 {
				continue
			}
			parsedStep, err := strconv.Atoi(pieces[1])
			if err != nil || parsedStep <= 0 {
				continue
			}
			step = parsedStep
			base = pieces[0]
		}
		start, end := min, max
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				continue
			}
			var err error
			start, err = strconv.Atoi(bounds[0])
			if err != nil {
				continue
			}
			end, err = strconv.Atoi(bounds[1])
			if err != nil {
				continue
			}
		default:
			parsed, err := strconv.Atoi(base)
			if err != nil {
				continue
			}
			start, end = parsed, parsed
		}
		if value >= start && value <= end && (value-start)%step == 0 {
			return true
		}
	}
	return false
}

func (s *Server) listCronJobs() ([]any, error) {
	entries, err := s.runtimeStore.List(cronRuntimeNamespace)
	if err != nil {
		return nil, err
	}
	jobs := make([]any, 0, len(entries))
	for _, entry := range entries {
		job := mapValue(entry.Value)
		if job["id"] == nil {
			job["id"] = entry.Key
		}
		if stringValue(job["humanSchedule"]) == "" {
			job["humanSchedule"] = cronToHuman(stringValue(job["cron"]))
		}
		jobs = append(jobs, job)
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		left := mapValue(jobs[i])
		right := mapValue(jobs[j])
		if stringValue(left["createdAt"]) != stringValue(right["createdAt"]) {
			return stringValue(left["createdAt"]) < stringValue(right["createdAt"])
		}
		return stringValue(left["id"]) < stringValue(right["id"])
	})
	return jobs, nil
}

func cronJobID(cron string, prompt string, at time.Time) string {
	sum := sha256.Sum256([]byte(cron + "\n" + prompt + "\n" + at.Format(time.RFC3339Nano)))
	return "cron-" + hex.EncodeToString(sum[:])[:16]
}

func validateCronExpression(expression string) error {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return fmt.Errorf("Invalid cron expression '%s'. Expected 5 fields: M H DoM Mon DoW.", expression)
	}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for index, field := range fields {
		if err := validateCronField(field, ranges[index][0], ranges[index][1]); err != nil {
			return fmt.Errorf("Invalid cron expression '%s': %w", expression, err)
		}
	}
	return nil
}

func validateCronField(field string, min int, max int) error {
	if field == "" {
		return errors.New("empty field")
	}
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return errors.New("empty list item")
		}
		base := part
		if strings.Contains(part, "/") {
			pieces := strings.Split(part, "/")
			if len(pieces) != 2 || pieces[1] == "" {
				return fmt.Errorf("invalid step %q", part)
			}
			step, err := strconv.Atoi(pieces[1])
			if err != nil || step <= 0 {
				return fmt.Errorf("invalid step %q", part)
			}
			base = pieces[0]
		}
		if base == "*" {
			continue
		}
		if strings.Contains(base, "-") {
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				return fmt.Errorf("invalid range %q", part)
			}
			start, errStart := strconv.Atoi(bounds[0])
			end, errEnd := strconv.Atoi(bounds[1])
			if errStart != nil || errEnd != nil || start > end || start < min || end > max {
				return fmt.Errorf("invalid range %q", part)
			}
			continue
		}
		value, err := strconv.Atoi(base)
		if err != nil || value < min || value > max {
			return fmt.Errorf("invalid value %q", part)
		}
	}
	return nil
}

func cronToHuman(expression string) string {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return expression
	}
	return "local cron " + strings.Join(fields, " ")
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
