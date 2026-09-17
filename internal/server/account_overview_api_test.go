package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"synon-go/internal/persistence/runtimekv"
	"synon-go/internal/persistence/workspace"
)

func TestAccountOverviewAPIUsesOwnerScopedSnapshotAndSkillProjection(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	for _, project := range []workspace.CreateProjectInput{
		{ID: "owned-project", UserID: "victor", Name: "Owned"},
		{ID: "foreign-project", UserID: "other", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []workspace.CreateFrameInput{
		{ID: "owned-completed", ProjectID: "owned-project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
		{ID: "owned-running", ProjectID: "owned-project", AgentName: "OPERON", Status: "processing", ConversationType: "agent"},
		{ID: "foreign-completed", ProjectID: "foreign-project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "owned-artifact", ProjectID: "owned-project", Name: "result.csv", Kind: "table", Content: []byte("result"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "foreign-artifact", ProjectID: "foreign-project", Name: "foreign.csv", Kind: "table", Content: []byte("foreign"),
	}); err != nil {
		t.Fatal(err)
	}

	runtimeStore := runtimekv.New(filepath.Join(root, "runtime.json"))
	defer runtimeStore.Close()
	if _, err := runtimeStore.Set(skillInvocationRuntimeNamespace, "skill-1", map[string]any{
		"skill": "docking", "createdAt": "2026-08-10T05:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeStore.Set(skillInvocationRuntimeNamespace, "skill-2", map[string]any{
		"skill": "docking", "createdAt": "2026-08-10T06:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	for _, audit := range []struct {
		key, sessionID string
		totalTokens    int64
	}{
		{key: "owned-completed", sessionID: "owned-completed", totalTokens: 1_200},
		{key: "owned-running", sessionID: "owned-running", totalTokens: 800},
		{key: "foreign", sessionID: "foreign-completed", totalTokens: 99_000},
	} {
		if _, err := runtimeStore.Set(sessionRunnerModelAuditRuntimeNamespace, audit.key, map[string]any{
			"sessionId": audit.sessionID, "totalTokens": audit.totalTokens, "recordedAt": time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	server := New(Options{Workspace: store, FileRoot: root, RuntimeStore: runtimeStore})
	defer closeTestServer(t, server)
	app := server.Handler()
	result := compatJSONRequest(t, app, http.MethodGet, "/api/account/overview?utc_offset_minutes=480", "victor", nil, http.StatusOK)

	metrics := result["metrics"].(map[string]any)
	if numberValue(metrics["totalTasks"]) != 2 || numberValue(metrics["completedTasks"]) != 1 ||
		numberValue(metrics["projectCount"]) != 1 || numberValue(metrics["artifactCount"]) != 1 {
		t.Fatalf("owner-scoped account metrics = %#v", metrics)
	}
	days, ok := result["activityDays"].([]any)
	if !ok || len(days) != accountActivityDayCount {
		t.Fatalf("activityDays = %#v, want %d entries", result["activityDays"], accountActivityDayCount)
	}
	if _, ok := days[0].(map[string]any)["tokenCount"]; !ok {
		t.Fatalf("activity day tokenCount is missing: %#v", days[0])
	}
	var tokenTotal int64
	for _, day := range days {
		tokenTotal += int64(numberValue(day.(map[string]any)["tokenCount"]))
	}
	if tokenTotal != 2_000 {
		t.Fatalf("owner-scoped token total = %d, want 2000", tokenTotal)
	}
	topSkills := result["topSkills"].([]any)
	if len(topSkills) != 1 || topSkills[0].(map[string]any)["name"] != "docking" ||
		numberValue(topSkills[0].(map[string]any)["invocationCount"]) != 2 {
		t.Fatalf("topSkills = %#v", topSkills)
	}
	if availability := result["availability"].(map[string]any); availability["projects"] != true ||
		availability["skills"] != true || availability["tokenUsage"] != true {
		t.Fatalf("availability = %#v", availability)
	}

	compatJSONRequest(t, app, http.MethodGet, "/api/account/overview?utc_offset_minutes=841", "victor", nil, http.StatusBadRequest)
	compatJSONRequest(t, app, http.MethodPost, "/api/account/overview", "victor", nil, http.StatusMethodNotAllowed)
}

func TestAccountOverviewAPIRevalidatesPrivateSnapshot(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "victor", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, FileRoot: root}).Handler()

	first := httptest.NewRecorder()
	app.ServeHTTP(first, compatRequest(t, http.MethodGet, "/api/account/overview", "victor", nil))
	if first.Code != http.StatusOK || first.Header().Get("ETag") == "" || first.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("first response = %d headers %#v", first.Code, first.Header())
	}
	secondRequest := compatRequest(t, http.MethodGet, "/api/account/overview", "victor", nil)
	secondRequest.Header.Set("If-None-Match", first.Header().Get("ETag"))
	second := httptest.NewRecorder()
	app.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("conditional response = %d body %q", second.Code, second.Body.String())
	}
}

func TestBuildAccountOverviewCalculatesCalendarAndStreaksInClientTimezone(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	tasks := make([]workspace.AccountOverviewTask, 0, 7)
	tokenUsageByDay := make(map[string]int64)
	statuses := []string{"completed", "success", "replaced", "failed", "cancelled", "completed", "processing"}
	for index, day := range []int{1, 2, 3, 4, 8, 9, 10} {
		observed := time.Date(2026, time.August, day, 4, 0, 0, 0, time.UTC)
		tasks = append(tasks, workspace.AccountOverviewTask{
			ID: "task-" + strconv.Itoa(index+1), Status: statuses[index], CreatedAt: observed, UpdatedAt: observed,
		})
		tokenUsageByDay[observed.In(time.FixedZone("account-client", 480*60)).Format("2006-01-02")] = int64((index + 1) * 1000)
	}
	latest := "2026-08-10T08:00:00Z"
	earlier := "2026-08-09T08:00:00Z"
	result := buildAccountOverview(workspace.AccountOverviewSnapshot{
		ProjectCount: 3, ArtifactCount: 11, Tasks: tasks,
	}, []skillUsageProjection{
		{Name: "literature", InvocationCount: 4, LastUsedAt: &latest},
		{Name: "docking", InvocationCount: 9, LastUsedAt: &earlier},
	}, true, tokenUsageByDay, true, now, 480)

	if result.Metrics.TotalTasks != 7 || result.Metrics.CompletedTasks != 4 ||
		result.Metrics.CurrentStreak != 3 || result.Metrics.LongestStreak != 4 || result.Metrics.ActiveDayCount != 7 {
		t.Fatalf("calculated metrics = %#v", result.Metrics)
	}
	if result.Metrics.CompletionRate == nil || *result.Metrics.CompletionRate != float64(4)/7 {
		t.Fatalf("completion rate = %#v", result.Metrics.CompletionRate)
	}
	if len(result.ActivityDays) != accountActivityDayCount || result.ActivityDays[len(result.ActivityDays)-1].Date != "2026-08-15" ||
		!result.ActivityDays[len(result.ActivityDays)-1].IsFuture {
		t.Fatalf("calendar boundary = first %#v last %#v", result.ActivityDays[0], result.ActivityDays[len(result.ActivityDays)-1])
	}
	var totalTokens int64
	for _, day := range result.ActivityDays {
		totalTokens += day.TokenCount
	}
	if totalTokens != 28_000 {
		t.Fatalf("daily token total = %d, want 28000", totalTokens)
	}
	if len(result.TopSkills) != 2 || result.TopSkills[0].Name != "docking" {
		t.Fatalf("ranked skills = %#v", result.TopSkills)
	}
	if !result.Availability.Projects || !result.Availability.Skills {
		t.Fatalf("availability = %#v", result.Availability)
	}
}

func TestAccountOverviewWithoutSkillStoreKeepsCoreMetricsAvailable(t *testing.T) {
	result := buildAccountOverview(workspace.AccountOverviewSnapshot{}, nil, false, nil, false, time.Now(), 0)
	if !result.Availability.Projects || result.Availability.Skills || result.Availability.TokenUsage ||
		result.TopSkills == nil || result.Metrics.CompletionRate != nil {
		t.Fatalf("empty overview = %#v", result)
	}
	if len(result.ActivityDays) != accountActivityDayCount {
		t.Fatalf("empty overview activity length = %d", len(result.ActivityDays))
	}
}
