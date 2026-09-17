package server

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/persistence/runtimekv"
	"synon-go/internal/persistence/workspace"
)

const (
	accountActivityWeeks    = 53
	accountActivityDayCount = accountActivityWeeks * 7
	accountRecentWindowDays = 30
	accountTopSkillLimit    = 5
)

type accountOverviewMetrics struct {
	TotalTasks      int      `json:"totalTasks"`
	CompletedTasks  int      `json:"completedTasks"`
	ProjectCount    int      `json:"projectCount"`
	ArtifactCount   int      `json:"artifactCount"`
	CurrentStreak   int      `json:"currentStreak"`
	LongestStreak   int      `json:"longestStreak"`
	CompletionRate  *float64 `json:"completionRate"`
	RecentTaskCount int      `json:"recentTaskCount"`
	ActiveDayCount  int      `json:"activeDayCount"`
}

type accountOverviewActivityDay struct {
	Date       string `json:"date"`
	Count      int    `json:"count"`
	TokenCount int64  `json:"tokenCount"`
	IsFuture   bool   `json:"isFuture"`
}

type accountOverviewAvailability struct {
	Projects   bool `json:"projects"`
	Skills     bool `json:"skills"`
	TokenUsage bool `json:"tokenUsage"`
}

type accountOverviewResponse struct {
	Metrics      accountOverviewMetrics       `json:"metrics"`
	ActivityDays []accountOverviewActivityDay `json:"activityDays"`
	TopSkills    []skillUsageProjection       `json:"topSkills"`
	Availability accountOverviewAvailability  `json:"availability"`
	LoadedAt     string                       `json:"loadedAt"`
}

func (s *Server) handleAccountOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "workspace runtime is not configured"})
		return
	}
	offsetMinutes, err := parseAccountUTCOffsetMinutes(r)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}

	snapshot, err := s.workspaceStore.ReadAccountOverview(r.Context(), userID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "account overview is unavailable"})
		return
	}
	usage := []skillUsageProjection{}
	skillsAvailable := false
	tokenUsageByDay := map[string]int64{}
	tokenUsageAvailable := false
	if s.runtimeStore != nil {
		if entries, listErr := s.runtimeStore.List(skillInvocationRuntimeNamespace); listErr == nil {
			usage = skillUsageProjections(entries)
			skillsAvailable = true
		}
		if entries, listErr := s.runtimeStore.ListReadOnly(sessionRunnerModelAuditRuntimeNamespace); listErr == nil {
			location := time.FixedZone("account-client", offsetMinutes*60)
			if projected, projectionErr := accountTokenUsageByDay(entries, snapshot.Tasks, location); projectionErr == nil {
				tokenUsageByDay = projected
				tokenUsageAvailable = true
			}
		}
	}

	response := buildAccountOverview(
		snapshot,
		usage,
		skillsAvailable,
		tokenUsageByDay,
		tokenUsageAvailable,
		time.Now(),
		offsetMinutes,
	)
	writePrivateRevalidatedWorkspaceJSON(w, r, userID, response)
}

func parseAccountUTCOffsetMinutes(r *http.Request) (int, error) {
	values, present := r.URL.Query()["utc_offset_minutes"]
	if !present {
		return 0, nil
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return 0, &accountOverviewRequestError{"utc_offset_minutes must be one integer"}
	}
	value, err := strconv.Atoi(strings.TrimSpace(values[0]))
	if err != nil || value < -14*60 || value > 14*60 {
		return 0, &accountOverviewRequestError{"utc_offset_minutes must be between -840 and 840"}
	}
	return value, nil
}

type accountOverviewRequestError struct{ message string }

func (e *accountOverviewRequestError) Error() string { return e.message }

func buildAccountOverview(
	snapshot workspace.AccountOverviewSnapshot,
	usage []skillUsageProjection,
	skillsAvailable bool,
	tokenUsageByDay map[string]int64,
	tokenUsageAvailable bool,
	now time.Time,
	offsetMinutes int,
) accountOverviewResponse {
	if now.IsZero() {
		now = time.Now()
	}
	location := time.FixedZone("account-client", offsetMinutes*60)
	today := accountLocalDay(now, location)
	activityCounts := make(map[string]int)
	activeDays := make(map[string]struct{})
	completedTasks := 0
	recentTasks := 0
	recentCutoff := now.Add(-accountRecentWindowDays * 24 * time.Hour)

	for _, task := range snapshot.Tasks {
		if accountTaskIsCompleted(task.Status) {
			completedTasks++
		}
		if !task.CreatedAt.IsZero() && !task.CreatedAt.Before(recentCutoff) && !task.CreatedAt.After(now) {
			recentTasks++
		}
		if task.UpdatedAt.IsZero() {
			continue
		}
		key := task.UpdatedAt.In(location).Format("2006-01-02")
		activityCounts[key]++
		activeDays[key] = struct{}{}
	}

	var completionRate *float64
	if len(snapshot.Tasks) > 0 {
		value := float64(completedTasks) / float64(len(snapshot.Tasks))
		completionRate = &value
	}

	start := today.AddDate(0, 0, -((accountActivityWeeks-1)*7 + int(today.Weekday())))
	activityDays := make([]accountOverviewActivityDay, 0, accountActivityDayCount)
	for index := 0; index < accountActivityDayCount; index++ {
		day := start.AddDate(0, 0, index)
		key := day.Format("2006-01-02")
		activityDays = append(activityDays, accountOverviewActivityDay{
			Date: key, Count: activityCounts[key], TokenCount: tokenUsageByDay[key], IsFuture: day.After(today),
		})
	}

	return accountOverviewResponse{
		Metrics: accountOverviewMetrics{
			TotalTasks: len(snapshot.Tasks), CompletedTasks: completedTasks,
			ProjectCount: snapshot.ProjectCount, ArtifactCount: snapshot.ArtifactCount,
			CurrentStreak:  accountCurrentStreak(activeDays, today),
			LongestStreak:  accountLongestStreak(activeDays, location),
			CompletionRate: completionRate, RecentTaskCount: recentTasks, ActiveDayCount: len(activeDays),
		},
		ActivityDays: activityDays,
		TopSkills:    accountTopSkills(usage),
		Availability: accountOverviewAvailability{
			Projects: true, Skills: skillsAvailable, TokenUsage: tokenUsageAvailable,
		},
		LoadedAt: accountOverviewDataTimestamp(snapshot, usage).Format(time.RFC3339Nano),
	}
}

func accountTokenUsageByDay(
	entries []runtimekv.Entry,
	tasks []workspace.AccountOverviewTask,
	location *time.Location,
) (map[string]int64, error) {
	if location == nil {
		return nil, errors.New("account token usage location is required")
	}
	visibleTaskIDs := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if id := strings.TrimSpace(task.ID); id != "" {
			visibleTaskIDs[id] = struct{}{}
		}
	}
	totals := make(map[string]int64)
	for _, entry := range entries {
		record, ok := entry.Value.(map[string]any)
		if !ok {
			return nil, errors.New("account model audit must be an object")
		}
		sessionID := strings.TrimSpace(taskMetricStringValue(record, "sessionId"))
		if _, visible := visibleTaskIDs[sessionID]; !visible {
			continue
		}
		total, totalPresent, err := nonNegativeAuditInteger(record["totalTokens"])
		if err != nil {
			return nil, fmt.Errorf("invalid account model audit total tokens: %w", err)
		}
		if !totalPresent {
			input, inputPresent, inputErr := nonNegativeAuditInteger(record["promptTokens"])
			if inputErr != nil {
				return nil, fmt.Errorf("invalid account model audit prompt tokens: %w", inputErr)
			}
			output, outputPresent, outputErr := nonNegativeAuditInteger(record["completionTokens"])
			if outputErr != nil {
				return nil, fmt.Errorf("invalid account model audit completion tokens: %w", outputErr)
			}
			if !inputPresent && !outputPresent {
				continue
			}
			total, err = checkedTaskMetricSum(input, output)
			if err != nil {
				return nil, fmt.Errorf("derive account model audit total tokens: %w", err)
			}
		}
		observedAt, err := accountModelAuditTime(record, entry.UpdatedAt)
		if err != nil {
			return nil, err
		}
		key := observedAt.In(location).Format("2006-01-02")
		if totals[key] > math.MaxInt64-total {
			return nil, errors.New("account daily token usage overflow")
		}
		totals[key] += total
	}
	return totals, nil
}

func accountModelAuditTime(record map[string]any, fallback time.Time) (time.Time, error) {
	for _, key := range []string{"recordedAt", "finishedAt", "startedAt"} {
		if raw := strings.TrimSpace(taskMetricStringValue(record, key)); raw != "" {
			parsed := parseProjectionTime(raw)
			if parsed.IsZero() {
				return time.Time{}, fmt.Errorf("invalid account model audit %s", key)
			}
			return parsed, nil
		}
	}
	if fallback.IsZero() {
		return time.Time{}, errors.New("account model audit timestamp is required")
	}
	return fallback, nil
}

func accountOverviewDataTimestamp(snapshot workspace.AccountOverviewSnapshot, usage []skillUsageProjection) time.Time {
	latest := snapshot.UpdatedAt
	for _, skill := range usage {
		if observed := projectionTime(skill.LastUsedAt); observed.After(latest) {
			latest = observed
		}
	}
	if latest.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return latest.UTC()
}

func accountTaskIsCompleted(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case workspace.FrameStatusCompleted, workspace.FrameStatusSuccess, workspace.FrameStatusReplaced, "finished", "complete":
		return true
	default:
		return false
	}
}

func accountLocalDay(value time.Time, location *time.Location) time.Time {
	local := value.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func accountCurrentStreak(activeDays map[string]struct{}, today time.Time) int {
	cursor := today
	if _, active := activeDays[cursor.Format("2006-01-02")]; !active {
		cursor = cursor.AddDate(0, 0, -1)
	}
	streak := 0
	for {
		if _, active := activeDays[cursor.Format("2006-01-02")]; !active {
			return streak
		}
		streak++
		cursor = cursor.AddDate(0, 0, -1)
	}
}

func accountLongestStreak(activeDays map[string]struct{}, location *time.Location) int {
	days := make([]time.Time, 0, len(activeDays))
	for value := range activeDays {
		if parsed, err := time.ParseInLocation("2006-01-02", value, location); err == nil {
			days = append(days, parsed)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	longest, current := 0, 0
	var previous time.Time
	for _, day := range days {
		if !previous.IsZero() && previous.AddDate(0, 0, 1).Equal(day) {
			current++
		} else {
			current = 1
		}
		if current > longest {
			longest = current
		}
		previous = day
	}
	return longest
}

func accountTopSkills(usage []skillUsageProjection) []skillUsageProjection {
	result := append([]skillUsageProjection(nil), usage...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].InvocationCount != result[j].InvocationCount {
			return result[i].InvocationCount > result[j].InvocationCount
		}
		left := projectionTime(result[i].LastUsedAt)
		right := projectionTime(result[j].LastUsedAt)
		if !left.Equal(right) {
			return left.After(right)
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	if len(result) > accountTopSkillLimit {
		result = result[:accountTopSkillLimit]
	}
	if result == nil {
		return []skillUsageProjection{}
	}
	return result
}

func projectionTime(value *string) time.Time {
	if value == nil {
		return time.Time{}
	}
	return parseProjectionTime(*value)
}
