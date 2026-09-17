package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type ArtifactPriority string

const (
	ArtifactPriorityUnknown        ArtifactPriority = "unknown"
	ArtifactPriorityUserStarred    ArtifactPriority = "user_starred"
	ArtifactPriorityUserHidden     ArtifactPriority = "user_hidden"
	ArtifactPriorityUserNoPriority ArtifactPriority = "user_no_priority"
)

var ErrArtifactPriority = errors.New("artifact priority is invalid")

func (priority ArtifactPriority) Valid() bool {
	switch priority {
	case ArtifactPriorityUnknown,
		ArtifactPriorityUserStarred,
		ArtifactPriorityUserHidden,
		ArtifactPriorityUserNoPriority:
		return true
	default:
		return false
	}
}

func (s *Store) ListArtifactsForRoot(rootFrameID string, limit int) ([]Artifact, error) {
	root, found, err := s.GetFrame(rootFrameID)
	if err != nil {
		return nil, err
	}
	if !found || root.ID != root.RootFrameID {
		return nil, fmt.Errorf("root frame %q does not exist", rootFrameID)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	frames, err := s.ListFrames(root.ProjectID, 1000, 0)
	if err != nil {
		return nil, err
	}
	artifactIDs := map[string]bool{}
	metadataRows, err := s.db.QueryContext(context.Background(), `
		SELECT artifact_id FROM artifact_runtime_metadata WHERE root_frame_id = ? ORDER BY artifact_id`, root.ID)
	if err != nil {
		return nil, fmt.Errorf("list root artifact metadata: %w", err)
	}
	for metadataRows.Next() {
		var artifactID string
		if err := metadataRows.Scan(&artifactID); err != nil {
			_ = metadataRows.Close()
			return nil, fmt.Errorf("scan root artifact metadata: %w", err)
		}
		artifactIDs[artifactID] = true
	}
	if err := metadataRows.Err(); err != nil {
		_ = metadataRows.Close()
		return nil, fmt.Errorf("iterate root artifact metadata: %w", err)
	}
	if err := metadataRows.Close(); err != nil {
		return nil, err
	}
	for _, frame := range frames {
		if frame.RootFrameID != root.ID {
			continue
		}
		events, err := s.ListFrameEvents(frame.ID, 0, 1000)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			collectArtifactIDs(event.Payload, artifactIDs)
		}
	}
	artifacts := make([]Artifact, 0, len(artifactIDs))
	for artifactID := range artifactIDs {
		artifact, found, err := s.GetArtifact(artifactID)
		if err != nil {
			return nil, err
		}
		if !found || artifact.ProjectID != root.ProjectID {
			continue
		}
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Priority != artifacts[j].Priority {
			return artifactPriorityRank(artifacts[i].Priority) > artifactPriorityRank(artifacts[j].Priority)
		}
		if artifacts[i].UpdatedAt.Equal(artifacts[j].UpdatedAt) {
			return artifacts[i].ID > artifacts[j].ID
		}
		return artifacts[i].UpdatedAt.After(artifacts[j].UpdatedAt)
	})
	if len(artifacts) > limit {
		artifacts = artifacts[:limit]
	}
	return artifacts, nil
}

func (s *Store) UpdateArtifactPriority(artifactID string, priority ArtifactPriority) (Artifact, error) {
	if s == nil || s.db == nil {
		return Artifact{}, errors.New("workspace store is closed")
	}
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return Artifact{}, errors.New("artifact id is required")
	}
	if !priority.Valid() {
		return Artifact{}, ErrArtifactPriority
	}
	result, err := s.db.ExecContext(context.Background(),
		"UPDATE artifacts SET priority = ?, updated_at = ? WHERE id = ?",
		priority, s.now().UTC(), artifactID,
	)
	if err != nil {
		return Artifact{}, fmt.Errorf("update artifact priority: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Artifact{}, fmt.Errorf("count artifact priority update: %w", err)
	}
	if changed != 1 {
		return Artifact{}, fmt.Errorf("artifact %q does not exist", artifactID)
	}
	artifact, found, err := s.GetArtifact(artifactID)
	if err != nil {
		return Artifact{}, err
	}
	if !found {
		return Artifact{}, fmt.Errorf("artifact %q disappeared after priority update", artifactID)
	}
	return artifact, nil
}

func artifactPriorityRank(priority ArtifactPriority) int {
	switch priority {
	case ArtifactPriorityUserStarred:
		return 3
	case ArtifactPriorityUserNoPriority:
		return 2
	case ArtifactPriorityUnknown:
		return 1
	case ArtifactPriorityUserHidden:
		return 0
	default:
		return -1
	}
}

func (s *Store) BulkMoveArtifacts(artifactIDs []string, folderID string) ([]Artifact, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(artifactIDs))
	for _, rawID := range artifactIDs {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("at least one artifact id is required")
	}
	if len(ids) > 1000 {
		return nil, errors.New("artifact bulk move is limited to 1000 ids")
	}
	folderID = strings.TrimSpace(folderID)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin artifact bulk move: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	folderProjectID := ""
	if folderID != "" {
		if err := tx.QueryRowContext(ctx, "SELECT project_id FROM artifact_folders WHERE id = ?", folderID).Scan(&folderProjectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("folder %q does not exist", folderID)
			}
			return nil, fmt.Errorf("look up bulk move folder: %w", err)
		}
	}
	for _, artifactID := range ids {
		var projectID string
		if err := tx.QueryRowContext(ctx, "SELECT project_id FROM artifacts WHERE id = ?", artifactID).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("artifact %q does not exist", artifactID)
			}
			return nil, fmt.Errorf("look up bulk move artifact: %w", err)
		}
		if folderProjectID != "" && projectID != folderProjectID {
			return nil, fmt.Errorf("artifact %q and folder %q belong to different projects", artifactID, folderID)
		}
	}
	now := s.now().UTC()
	for _, artifactID := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE artifacts SET folder_id = ?, updated_at = ? WHERE id = ?",
			nullableString(folderID), now, artifactID,
		); err != nil {
			return nil, fmt.Errorf("move artifact %q: %w", artifactID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit artifact bulk move: %w", err)
	}
	artifacts := make([]Artifact, 0, len(ids))
	for _, artifactID := range ids {
		artifact, found, err := s.GetArtifact(artifactID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("artifact %q disappeared after bulk move", artifactID)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}
