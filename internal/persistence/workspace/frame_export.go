package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type FrameSessionExport struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Project       Project              `json:"project"`
	RootFrame     Frame                `json:"rootFrame"`
	Frames        []FrameSessionRecord `json:"frames"`
	Artifacts     []Artifact           `json:"artifacts"`
}

type FrameSessionRecord struct {
	Frame  Frame        `json:"frame"`
	Events []FrameEvent `json:"events"`
}

func (s *Store) BuildFrameSessionExport(rootFrameID string) (FrameSessionExport, error) {
	if s == nil || s.db == nil {
		return FrameSessionExport{}, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return FrameSessionExport{}, errors.New("root frame id is required")
	}
	root, found, err := s.GetFrame(rootFrameID)
	if err != nil {
		return FrameSessionExport{}, err
	}
	if !found || root.ID != root.RootFrameID {
		return FrameSessionExport{}, fmt.Errorf("root frame %q does not exist", rootFrameID)
	}
	project, found, err := s.GetProject(root.ProjectID)
	if err != nil {
		return FrameSessionExport{}, err
	}
	if !found {
		return FrameSessionExport{}, fmt.Errorf("project %q does not exist", root.ProjectID)
	}
	frames, err := s.listRootFrames(root.ID)
	if err != nil {
		return FrameSessionExport{}, err
	}
	records := make([]FrameSessionRecord, 0, len(frames))
	artifactIDs := map[string]bool{}
	for _, frame := range frames {
		events, err := s.listAllFrameEvents(frame.ID)
		if err != nil {
			return FrameSessionExport{}, err
		}
		for _, event := range events {
			collectArtifactIDs(event.Payload, artifactIDs)
		}
		records = append(records, FrameSessionRecord{Frame: frame, Events: events})
	}
	artifacts := make([]Artifact, 0, len(artifactIDs))
	for artifactID := range artifactIDs {
		artifact, found, err := s.GetArtifact(artifactID)
		if err != nil {
			return FrameSessionExport{}, err
		}
		if found && artifact.ProjectID == root.ProjectID {
			artifacts = append(artifacts, artifact)
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Priority != artifacts[j].Priority {
			return artifactPriorityRank(artifacts[i].Priority) > artifactPriorityRank(artifacts[j].Priority)
		}
		return artifacts[i].ID < artifacts[j].ID
	})
	return FrameSessionExport{
		SchemaVersion: 1, Project: project, RootFrame: root,
		Frames: records, Artifacts: artifacts,
	}, nil
}

func (s *Store) listRootFrames(rootFrameID string) ([]Frame, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, project_id, COALESCE(parent_frame_id, ''), root_frame_id, root_sequence,
			agent_name, status, conversation_type, name, created_at, updated_at
		FROM frames WHERE root_frame_id = ? ORDER BY root_sequence, id`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list root frames for export: %w", err)
	}
	defer rows.Close()
	frames := []Frame{}
	for rows.Next() {
		var frame Frame
		if err := rows.Scan(
			&frame.ID, &frame.ProjectID, &frame.ParentFrameID, &frame.RootFrameID,
			&frame.RootSequence, &frame.AgentName, &frame.Status, &frame.ConversationType,
			&frame.Name, &frame.CreatedAt, &frame.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan exported frame: %w", err)
		}
		frames = append(frames, frame)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exported frames: %w", err)
	}
	return frames, nil
}

func (s *Store) listAllFrameEvents(frameID string) ([]FrameEvent, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, frame_id, sequence, event_type, payload, created_at
		FROM frame_events WHERE frame_id = ? ORDER BY sequence`, frameID)
	if err != nil {
		return nil, fmt.Errorf("list frame events for export: %w", err)
	}
	defer rows.Close()
	events := []FrameEvent{}
	for rows.Next() {
		var event FrameEvent
		var payload string
		if err := rows.Scan(&event.ID, &event.FrameID, &event.Sequence, &event.Type, &payload, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan exported frame event: %w", err)
		}
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode exported frame event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exported frame events: %w", err)
	}
	return events, nil
}
