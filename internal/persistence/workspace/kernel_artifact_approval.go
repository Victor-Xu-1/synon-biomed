package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) AddKernelArtifactApprovalRequest(
	ctx context.Context, ownerUserID, projectID, frameID string, request map[string]any,
) error {
	return s.mutateKernelArtifactApprovalRequest(ctx, ownerUserID, projectID, frameID, request, false)
}

func (s *Store) RemoveKernelArtifactApprovalRequest(
	ctx context.Context, ownerUserID, projectID, frameID, requestID string,
) error {
	return s.mutateKernelArtifactApprovalRequest(ctx, ownerUserID, projectID, frameID,
		map[string]any{"requestId": strings.TrimSpace(requestID)}, true)
}

func (s *Store) mutateKernelArtifactApprovalRequest(
	ctx context.Context, ownerUserID, projectID, frameID string, request map[string]any, remove bool,
) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	ownerUserID, projectID, frameID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID), strings.TrimSpace(frameID)
	requestID := strings.TrimSpace(compatibilityStringValue(request["requestId"]))
	if ownerUserID == "" || projectID == "" || frameID == "" || requestID == "" {
		return errors.New("kernel artifact approval owner, project, frame, and request id are required")
	}
	s.branchMu.Lock()
	defer s.branchMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var owned string
		if err := tx.QueryRowContext(ctx, `
			SELECT f.id FROM frames f JOIN projects p ON p.id=f.project_id AND p.user_id=?
			WHERE f.id=? AND f.project_id=?`, ownerUserID, frameID, projectID).Scan(&owned); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrKernelArtifactNotFound
			}
			return fmt.Errorf("verify kernel artifact approval frame: %w", err)
		}
		contextData := map[string]any{}
		var encoded sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT context_data FROM frame_runtime_metadata WHERE frame_id=?`, frameID).Scan(&encoded); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read kernel artifact approval state: %w", err)
		}
		if encoded.Valid && strings.TrimSpace(encoded.String) != "" && json.Unmarshal([]byte(encoded.String), &contextData) != nil {
			return errors.New("kernel artifact approval state is invalid")
		}
		pending := compatibilityPendingInputRequests(contextData)
		next := make([]map[string]any, 0, len(pending)+1)
		found := false
		for _, item := range pending {
			if compatibilityPendingInputID(item) != requestID {
				next = append(next, item)
				continue
			}
			found = true
			if !remove && !mapsEqualJSON(item, request) {
				return errors.New("kernel artifact approval request conflicts with durable state")
			}
			if !remove {
				next = append(next, item)
			}
		}
		if !remove && !found {
			next = append(next, request)
		}
		contextData["_pending_input_requests"] = compatibilityMapsToAny(next)
		raw, err := json.Marshal(contextData)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO frame_runtime_metadata(frame_id,context_data) VALUES(?,?)
			ON CONFLICT(frame_id) DO UPDATE SET context_data=excluded.context_data`, frameID, string(raw))
		return err
	})
}

func mapsEqualJSON(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
