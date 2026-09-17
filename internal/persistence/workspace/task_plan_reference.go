package workspace

import (
	"context"
	"errors"
	"strings"
	"time"
)

// TaskPlanReference identifies the one current plan artifact owned by a frame.
// It is intentionally narrower than the conversation artifact listing: callers
// must never guess a task plan from another frame or from artifact recency.
type TaskPlanReference struct {
	ArtifactID     string
	VersionID      string
	ContentVersion int
	RevisionNumber int
	RevisionCount  int
	GeneratedAt    time.Time
}

// GetCurrentTaskPlanReference resolves legacy tasks whose plan artifact was
// durably associated with the frame before plan identity was copied into frame
// runtime metadata. Multiple working-plan revisions are ordered only by their
// durable Transcript execution authority; artifact timestamps and list order
// are never used to guess which plan the task executed.
func (s *Store) GetCurrentTaskPlanReference(ctx context.Context, frameID string) (TaskPlanReference, bool, error) {
	if s == nil || s.db == nil {
		return TaskPlanReference{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return TaskPlanReference{}, false, errors.New("frame id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact.id, version.id, artifact.name, artifact.kind,
			version.version_number, version.created_at
		FROM artifact_runtime_metadata metadata
		JOIN artifacts artifact ON artifact.id=metadata.artifact_id
		JOIN artifact_versions version
			ON version.artifact_id=artifact.id AND version.version_number=artifact.current_version_number
		WHERE metadata.frame_id=? AND metadata.superseded_by_artifact_id IS NULL
		ORDER BY artifact.id`, frameID)
	if err != nil {
		return TaskPlanReference{}, false, err
	}
	defer rows.Close()
	candidates := make([]TaskPlanReference, 0, 2)
	for rows.Next() {
		var reference TaskPlanReference
		var name, kind string
		if err := rows.Scan(
			&reference.ArtifactID, &reference.VersionID, &name, &kind,
			&reference.ContentVersion, &reference.GeneratedAt,
		); err != nil {
			return TaskPlanReference{}, false, err
		}
		lowerName := strings.ToLower(strings.TrimSpace(name))
		lowerKind := strings.ToLower(strings.TrimSpace(kind))
		if lowerName != "plan.json" && !(strings.HasPrefix(lowerName, "plan_") && strings.HasSuffix(lowerName, ".json")) {
			continue
		}
		if lowerKind != "application/json" && lowerKind != "json" {
			continue
		}
		candidates = append(candidates, reference)
	}
	if err := rows.Err(); err != nil {
		return TaskPlanReference{}, false, err
	}
	if len(candidates) == 0 {
		return TaskPlanReference{}, false, nil
	}
	selected := candidates[0]
	if len(candidates) > 1 {
		var found bool
		selected, found, err = s.resolveTaskPlanTranscriptAuthority(ctx, frameID, candidates)
		if err != nil || !found {
			return TaskPlanReference{}, false, err
		}
	} else {
		selected.RevisionNumber = 1
		selected.RevisionCount = 1
	}
	events, err := s.ListFrameEvents(frameID, 0, 10000)
	if err != nil {
		return TaskPlanReference{}, false, err
	}
	for _, event := range events {
		if event.Type != "plan_discarded" {
			continue
		}
		discardedArtifactID := strings.TrimSpace(stringValue(event.Payload["artifact_id"]))
		if discardedArtifactID == "" || discardedArtifactID == selected.ArtifactID {
			return TaskPlanReference{}, false, nil
		}
	}
	return selected, true, nil
}

// resolveTaskPlanTranscriptAuthority selects the last plan revision actually
// produced by the task runner. A tie between distinct artifacts fails closed:
// ordinal is output ordering within one event, not semantic plan authority.
func (s *Store) resolveTaskPlanTranscriptAuthority(
	ctx context.Context,
	frameID string,
	candidates []TaskPlanReference,
) (TaskPlanReference, bool, error) {
	byArtifact := make(map[string]TaskPlanReference, len(candidates))
	for _, candidate := range candidates {
		byArtifact[candidate.ArtifactID] = candidate
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT commit_row.artifact_id,commit_row.runner_attempt,commit_row.source_event_id
		FROM transcript_streams stream
		JOIN transcript_artifact_commits commit_row ON commit_row.stream_uid=stream.stream_uid
		WHERE stream.session_id=? AND stream.kind='frame_ref' AND commit_row.relation='produced'
		ORDER BY commit_row.runner_attempt DESC,commit_row.source_event_id DESC,commit_row.artifact_id`, frameID)
	if err != nil {
		return TaskPlanReference{}, false, err
	}
	defer rows.Close()
	var selected TaskPlanReference
	var topAttempt, topEvent int64
	found := false
	seenArtifacts := make(map[string]bool, len(candidates))
	revisionCount := 0
	for rows.Next() {
		var artifactID string
		var attempt, sourceEvent int64
		if err := rows.Scan(&artifactID, &attempt, &sourceEvent); err != nil {
			return TaskPlanReference{}, false, err
		}
		candidate, ok := byArtifact[artifactID]
		if !ok {
			continue
		}
		if seenArtifacts[artifactID] {
			continue
		}
		seenArtifacts[artifactID] = true
		revisionCount++
		if !found {
			selected, topAttempt, topEvent, found = candidate, attempt, sourceEvent, true
			continue
		}
		if attempt != topAttempt || sourceEvent != topEvent {
			continue
		}
		if candidate.ArtifactID != selected.ArtifactID {
			return TaskPlanReference{}, false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return TaskPlanReference{}, false, err
	}
	if found {
		selected.RevisionNumber = revisionCount
		selected.RevisionCount = revisionCount
	}
	return selected, found, nil
}
