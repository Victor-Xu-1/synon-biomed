package runtimekv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Scope identifies the durable task or project that owns an operational
// entry. Empty fields are ignored. It lets deletion remove runtime residue
// without parsing arbitrary JSON at cleanup time.
type Scope struct {
	OwnerUserID string
	ProjectID   string
	RootFrameID string
	FrameID     string
}

// DeleteOlderThan removes only explicitly classified operational namespaces.
// Active-state namespaces have no implicit age limit.
func (s *Store) DeleteOlderThan(ctx context.Context, namespaces []string, cutoff time.Time) (int64, error) {
	if len(namespaces) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(namespaces))
	args := make([]any, 0, len(namespaces)+1)
	for index, namespace := range namespaces {
		if err := validateNamespace(namespace); err != nil {
			return 0, err
		}
		placeholders[index] = "?"
		args = append(args, namespace)
	}
	args = append(args, cutoff.UTC())
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDatabase(); err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM runtime_state_entries
		WHERE namespace IN (`+strings.Join(placeholders, ",")+`) AND updated_at<?`, args...)
	if err != nil {
		return 0, fmt.Errorf("sweep runtime state: %w", err)
	}
	return result.RowsAffected()
}

// DeleteScope removes operational projections after their owning durable
// project or task has been deleted.
func (s *Store) DeleteScope(ctx context.Context, scope Scope) (int64, error) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if value := strings.TrimSpace(scope.ProjectID); value != "" {
		conditions = append(conditions, "project_id=?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(scope.RootFrameID); value != "" {
		conditions = append(conditions, "root_frame_id=?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(scope.FrameID); value != "" {
		conditions = append(conditions, "frame_id=?")
		args = append(args, value)
	}
	if len(conditions) == 0 {
		if value := strings.TrimSpace(scope.OwnerUserID); value != "" {
			conditions = append(conditions, "owner_user_id=?")
			args = append(args, value)
		}
	}
	if len(conditions) == 0 {
		return 0, errors.New("runtime state deletion scope is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDatabase(); err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM runtime_state_entries WHERE `+strings.Join(conditions, " OR "), args...)
	if err != nil {
		return 0, fmt.Errorf("delete scoped runtime state: %w", err)
	}
	return result.RowsAffected()
}

func inferScope(namespace, key string, value any) Scope {
	object, _ := value.(map[string]any)
	scope := Scope{
		OwnerUserID: firstString(object, "ownerUserId", "owner_user_id", "ownerId", "owner_id", "userId", "user_id"),
		ProjectID:   firstString(object, "projectId", "project_id"),
		RootFrameID: firstString(object, "rootFrameId", "root_frame_id", "rootSessionId", "root_session_id"),
		FrameID:     firstString(object, "frameId", "frame_id", "sessionId", "session_id"),
	}
	switch namespace {
	case "compute-session-concurrency", "synon-memory-served", "todos":
		if scope.RootFrameID == "" {
			scope.RootFrameID = key
		}
	}
	return scope
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}
