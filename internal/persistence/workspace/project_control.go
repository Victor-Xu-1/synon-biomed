package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Bench struct {
	ID            string    `json:"id"`
	RootFrameID   string    `json:"rootFrameId"`
	ProjectID     string    `json:"projectId"`
	Name          string    `json:"name"`
	TaskSummary   string    `json:"taskSummary,omitempty"`
	AgentName     string    `json:"agentName"`
	Status        string    `json:"status"`
	ChildCount    int       `json:"childCount"`
	ArtifactCount int       `json:"artifactCount"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type UpdateBenchInput struct {
	Name        *string
	TaskSummary *string
}

type ProjectDashboardEntry struct {
	Project         Project   `json:"project"`
	BenchCount      int       `json:"benchCount"`
	ProcessingCount int       `json:"processingCount"`
	ArtifactCount   int       `json:"artifactCount"`
	LastActiveAt    time.Time `json:"lastActiveAt"`
}

type ProjectDashboard struct {
	Projects        []ProjectDashboardEntry `json:"projects"`
	ProjectCount    int                     `json:"projectCount"`
	BenchCount      int                     `json:"benchCount"`
	ProcessingCount int                     `json:"processingCount"`
	ArtifactCount   int                     `json:"artifactCount"`
}

type ProcessingCounts struct {
	ByStatus        map[string]int `json:"byStatus"`
	TotalProcessing int            `json:"totalProcessing"`
}

func (s *Store) ListBenches(projectID string, limit int, query string) ([]Bench, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("bench project id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	frames, err := s.ListFrames(projectID, 1000, 0)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	benches := make([]Bench, 0)
	for _, frame := range frames {
		if frame.ID != frame.RootFrameID {
			continue
		}
		bench, err := s.benchProjection(frame, frames)
		if err != nil {
			return nil, err
		}
		if needle != "" && !strings.Contains(strings.ToLower(bench.Name), needle) &&
			!strings.Contains(strings.ToLower(bench.TaskSummary), needle) {
			continue
		}
		benches = append(benches, bench)
	}
	sort.Slice(benches, func(i, j int) bool {
		if benches[i].UpdatedAt.Equal(benches[j].UpdatedAt) {
			return benches[i].ID > benches[j].ID
		}
		return benches[i].UpdatedAt.After(benches[j].UpdatedAt)
	})
	if len(benches) > limit {
		benches = benches[:limit]
	}
	return benches, nil
}

func (s *Store) GetBench(frameID string) (Bench, bool, error) {
	frame, found, err := s.GetFrame(frameID)
	if err != nil || !found {
		return Bench{}, found, err
	}
	if frame.ID != frame.RootFrameID {
		return Bench{}, false, nil
	}
	frames, err := s.ListFrames(frame.ProjectID, 1000, 0)
	if err != nil {
		return Bench{}, false, err
	}
	bench, err := s.benchProjection(frame, frames)
	return bench, err == nil, err
}

func (s *Store) benchProjection(root Frame, projectFrames []Frame) (Bench, error) {
	events, err := s.ListFrameEvents(root.ID, 0, 1000)
	if err != nil {
		return Bench{}, err
	}
	taskSummary := ""
	artifactIDs := map[string]bool{}
	for _, event := range events {
		if event.Type == "bench_updated" {
			if summary, ok := event.Payload["taskSummary"].(string); ok {
				taskSummary = summary
			}
		}
		collectArtifactIDs(event.Payload, artifactIDs)
	}
	childCount := 0
	for _, frame := range projectFrames {
		if frame.RootFrameID != root.ID || frame.ID == root.ID {
			continue
		}
		childCount++
		childEvents, err := s.ListFrameEvents(frame.ID, 0, 1000)
		if err != nil {
			return Bench{}, err
		}
		for _, event := range childEvents {
			collectArtifactIDs(event.Payload, artifactIDs)
		}
	}
	return Bench{
		ID: root.ID, RootFrameID: root.ID, ProjectID: root.ProjectID,
		Name: root.Name, TaskSummary: taskSummary, AgentName: root.AgentName,
		Status: root.Status, ChildCount: childCount, ArtifactCount: len(artifactIDs),
		CreatedAt: root.CreatedAt, UpdatedAt: root.UpdatedAt,
	}, nil
}

func (s *Store) UpdateBench(frameID string, input UpdateBenchInput) (Bench, FrameEvent, error) {
	if s == nil || s.db == nil {
		return Bench{}, FrameEvent{}, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(frameID) == "" || (input.Name == nil && input.TaskSummary == nil) {
		return Bench{}, FrameEvent{}, errors.New("bench id and at least one update field are required")
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
		return Bench{}, FrameEvent{}, errors.New("bench name must not be empty")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Bench{}, FrameEvent{}, fmt.Errorf("begin bench update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	frame, err := branchFrameByID(ctx, tx, frameID)
	if err != nil {
		return Bench{}, FrameEvent{}, err
	}
	if frame.ID != frame.RootFrameID {
		return Bench{}, FrameEvent{}, fmt.Errorf("frame %q is not a root bench", frameID)
	}
	now := s.now().UTC()
	name := frame.Name
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE frames SET name = ?, updated_at = ? WHERE id = ?", name, now, frameID); err != nil {
		return Bench{}, FrameEvent{}, fmt.Errorf("update bench frame: %w", err)
	}
	payload := map[string]any{"name": name}
	if input.TaskSummary != nil {
		payload["taskSummary"] = strings.TrimSpace(*input.TaskSummary)
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return Bench{}, FrameEvent{}, err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?", frameID).Scan(&sequence); err != nil {
		return Bench{}, FrameEvent{}, err
	}
	event := FrameEvent{ID: uuid.NewString(), FrameID: frameID, Sequence: sequence, Type: "bench_updated", Payload: payload, CreatedAt: now}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		event.ID, event.FrameID, event.Sequence, event.Type, string(rawPayload), event.CreatedAt,
	); err != nil {
		return Bench{}, FrameEvent{}, fmt.Errorf("insert bench update event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Bench{}, FrameEvent{}, fmt.Errorf("commit bench update: %w", err)
	}
	bench, found, err := s.GetBench(frameID)
	if err != nil {
		return Bench{}, FrameEvent{}, err
	}
	if !found {
		return Bench{}, FrameEvent{}, fmt.Errorf("bench %q disappeared after update", frameID)
	}
	return bench, event, nil
}

func (s *Store) Dashboard() (ProjectDashboard, error) {
	projects, err := s.ListProjects(1000, 0)
	if err != nil {
		return ProjectDashboard{}, err
	}
	return s.dashboardForProjects(projects)
}

func (s *Store) DashboardForUser(userID string) (ProjectDashboard, error) {
	projects, err := s.ListProjectsForUser(userID, 1000, 0)
	if err != nil {
		return ProjectDashboard{}, err
	}
	return s.dashboardForProjects(projects)
}

func (s *Store) dashboardForProjects(projects []Project) (ProjectDashboard, error) {
	dashboard := ProjectDashboard{Projects: make([]ProjectDashboardEntry, 0, len(projects)), ProjectCount: len(projects)}
	for _, project := range projects {
		benches, err := s.ListBenches(project.ID, 1000, "")
		if err != nil {
			return ProjectDashboard{}, err
		}
		artifacts, err := s.ListArtifacts(project.ID, 1000, 0)
		if err != nil {
			return ProjectDashboard{}, err
		}
		entry := ProjectDashboardEntry{
			Project: project, BenchCount: len(benches), ArtifactCount: len(artifacts),
			LastActiveAt: project.UpdatedAt,
		}
		for _, bench := range benches {
			if frameStatusProcessing(bench.Status) {
				entry.ProcessingCount++
			}
			if bench.UpdatedAt.After(entry.LastActiveAt) {
				entry.LastActiveAt = bench.UpdatedAt
			}
		}
		dashboard.BenchCount += entry.BenchCount
		dashboard.ProcessingCount += entry.ProcessingCount
		dashboard.ArtifactCount += entry.ArtifactCount
		dashboard.Projects = append(dashboard.Projects, entry)
	}
	sort.Slice(dashboard.Projects, func(i, j int) bool {
		return dashboard.Projects[i].LastActiveAt.After(dashboard.Projects[j].LastActiveAt)
	})
	return dashboard, nil
}

func (s *Store) GetProcessingCounts() (ProcessingCounts, error) {
	projects, err := s.ListProjects(1000, 0)
	if err != nil {
		return ProcessingCounts{}, err
	}
	return s.processingCountsForProjects(projects)
}

func (s *Store) GetProcessingCountsForUser(userID string) (ProcessingCounts, error) {
	projects, err := s.ListProjectsForUser(userID, 1000, 0)
	if err != nil {
		return ProcessingCounts{}, err
	}
	return s.processingCountsForProjects(projects)
}

func (s *Store) processingCountsForProjects(projects []Project) (ProcessingCounts, error) {
	counts := ProcessingCounts{ByStatus: map[string]int{}}
	for _, project := range projects {
		benches, err := s.ListBenches(project.ID, 1000, "")
		if err != nil {
			return ProcessingCounts{}, err
		}
		for _, bench := range benches {
			counts.ByStatus[bench.Status]++
			if frameStatusProcessing(bench.Status) {
				counts.TotalProcessing++
			}
		}
	}
	return counts, nil
}

func frameStatusProcessing(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled", "canceled", "stopped":
		return false
	default:
		return true
	}
}

func collectArtifactIDs(value any, output map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "artifactId" || key == "artifact_id" {
				if id := strings.TrimSpace(fmt.Sprint(item)); id != "" {
					output[id] = true
				}
			} else {
				collectArtifactIDs(item, output)
			}
		}
	case []any:
		for _, item := range typed {
			collectArtifactIDs(item, output)
		}
	}
}
