package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// rootDeletionResult is the data-plane result shared by every workspace
// deletion entry point.  The caller owns the surrounding transaction and is
// responsible for publishing the appropriate realtime events after the data
// stages have completed.
type rootDeletionResult struct {
	ProjectID        string
	RootFrameID      string
	Frames           []deletionFrame
	BlobPaths        []string
	FramesDeleted    int64
	ArtifactsDeleted int64
}

type deletionFrame struct {
	ID            string
	IncarnationID string
}

// deleteRootAggregateTx is the single data-plane path for deleting one
// conversation root.  Compatibility APIs, project deletion, and future
// callers must use this function instead of duplicating cascade SQL.
func (s *Store) deleteRootAggregateTx(
	ctx context.Context,
	tx *sql.Tx,
	ownerUserID, projectID, rootFrameID string,
) (rootDeletionResult, error) {
	if s == nil || s.db == nil {
		return rootDeletionResult{}, errors.New("workspace store is closed")
	}
	if tx == nil {
		return rootDeletionResult{}, errors.New("root deletion transaction is required")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	projectID = strings.TrimSpace(projectID)
	rootFrameID = strings.TrimSpace(rootFrameID)
	if ownerUserID == "" || projectID == "" || rootFrameID == "" {
		return rootDeletionResult{}, errors.New("root deletion owner, project, and root frame are required")
	}

	var projectExists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM projects WHERE id=? AND user_id=?`, projectID, ownerUserID,
	).Scan(&projectExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rootDeletionResult{}, fmt.Errorf("project %q does not exist", projectID)
		}
		return rootDeletionResult{}, fmt.Errorf("authorize root deletion project: %w", err)
	}

	result := rootDeletionResult{
		ProjectID:   projectID,
		RootFrameID: rootFrameID,
		Frames:      make([]deletionFrame, 0),
		BlobPaths:   make([]string, 0),
	}
	frameRows, err := tx.QueryContext(ctx, `
		SELECT id,incarnation_id
		FROM frames
		WHERE project_id=? AND root_frame_id=?
		ORDER BY root_sequence,id`, projectID, rootFrameID)
	if err != nil {
		return rootDeletionResult{}, fmt.Errorf("list root frames before delete: %w", err)
	}
	for frameRows.Next() {
		var frame deletionFrame
		if err := frameRows.Scan(&frame.ID, &frame.IncarnationID); err != nil {
			_ = frameRows.Close()
			return rootDeletionResult{}, fmt.Errorf("scan root frame before delete: %w", err)
		}
		result.Frames = append(result.Frames, frame)
	}
	if err := frameRows.Err(); err != nil {
		_ = frameRows.Close()
		return rootDeletionResult{}, fmt.Errorf("iterate root frames before delete: %w", err)
	}
	if err := frameRows.Close(); err != nil {
		return rootDeletionResult{}, fmt.Errorf("close root frame scan before delete: %w", err)
	}

	appendBlobPaths := func(rows *sql.Rows, label string) error {
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan %s: %w", label, err)
			}
			path = strings.TrimSpace(path)
			if path != "" {
				result.BlobPaths = append(result.BlobPaths, path)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate %s: %w", label, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close %s: %w", label, err)
		}
		return nil
	}
	runnerRows, err := tx.QueryContext(ctx, `
		SELECT storage_path
		FROM runner_large_tool_results
		WHERE owner_user_id=? AND project_id=? AND root_frame_id=? AND storage_path<>''`,
		ownerUserID, projectID, rootFrameID)
	if err != nil {
		return rootDeletionResult{}, fmt.Errorf("list root runner result blobs: %w", err)
	}
	if err := appendBlobPaths(runnerRows, "root runner result blobs"); err != nil {
		return rootDeletionResult{}, err
	}

	runtimeDeleteScopeID, err := prepareRuntimeAuditRootDeleteTx(ctx, tx, ownerUserID, projectID, rootFrameID)
	if err != nil {
		return rootDeletionResult{}, err
	}
	if _, err := transcriptstore.DeleteRootStreamsTx(ctx, tx, ownerUserID, projectID, rootFrameID); err != nil {
		return rootDeletionResult{}, fmt.Errorf("delete root transcript streams: %w", err)
	}
	// Realtime rows and delivered transport envelopes are transient wake/replay
	// state, not task history. Remove the retired cohort before the caller
	// enqueues the one authoritative deletion notification.
	if _, err := tx.ExecContext(ctx, `DELETE FROM realtime_events
		WHERE root_frame_id=? OR frame_id IN (
			SELECT id FROM frames WHERE project_id=? AND root_frame_id=?
		)`, rootFrameID, projectID, rootFrameID); err != nil {
		return rootDeletionResult{}, fmt.Errorf("delete root realtime transport: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_outbox
		WHERE (aggregate_type='frame' AND aggregate_id IN (
			SELECT id FROM frames WHERE project_id=? AND root_frame_id=?
		)) OR (json_valid(payload_json) AND (
			COALESCE(json_extract(payload_json,'$.event.rootFrameId'),'')=? OR
			COALESCE(json_extract(payload_json,'$.notificationRoot'),'')=? OR
			COALESCE(json_extract(payload_json,'$.frameId'),'') IN (
				SELECT id FROM frames WHERE project_id=? AND root_frame_id=?
			)
		))`, projectID, rootFrameID, rootFrameID, rootFrameID, projectID, rootFrameID); err != nil {
		return rootDeletionResult{}, fmt.Errorf("delete root outbox transport: %w", err)
	}

	artifactRows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT version.storage_path
		FROM artifact_versions AS version
		JOIN artifacts AS artifact ON artifact.id=version.artifact_id
		WHERE artifact.project_id=? AND version.storage_path<>'' AND (
			EXISTS (
				SELECT 1 FROM artifact_runtime_metadata AS metadata
				WHERE metadata.artifact_id=artifact.id AND metadata.root_frame_id=?
			) OR EXISTS (
				SELECT 1 FROM artifact_folders AS folder
				WHERE folder.id=artifact.folder_id AND folder.project_id=artifact.project_id
					AND folder.root_frame_id=?
			)
		)`, projectID, rootFrameID, rootFrameID)
	if err != nil {
		return rootDeletionResult{}, fmt.Errorf("list root artifact blobs: %w", err)
	}
	if err := appendBlobPaths(artifactRows, "root artifact blobs"); err != nil {
		return rootDeletionResult{}, err
	}

	artifactResult, err := tx.ExecContext(ctx, `
		DELETE FROM artifacts
		WHERE project_id=? AND (
			id IN (SELECT artifact_id FROM artifact_runtime_metadata WHERE root_frame_id=?) OR
			folder_id IN (SELECT id FROM artifact_folders WHERE project_id=? AND root_frame_id=?)
		)`, projectID, rootFrameID, projectID, rootFrameID)
	if err != nil {
		return rootDeletionResult{}, fmt.Errorf("delete root artifacts: %w", err)
	}
	result.ArtifactsDeleted, err = artifactResult.RowsAffected()
	if err != nil {
		return rootDeletionResult{}, fmt.Errorf("count root artifacts: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM artifact_folders WHERE project_id=? AND root_frame_id=?`, projectID, rootFrameID,
	); err != nil {
		return rootDeletionResult{}, fmt.Errorf("delete root artifact folders: %w", err)
	}

	frameResult, err := tx.ExecContext(ctx,
		`DELETE FROM frames WHERE project_id=? AND root_frame_id=?`, projectID, rootFrameID,
	)
	if err != nil {
		return rootDeletionResult{}, fmt.Errorf("delete root frames: %w", err)
	}
	result.FramesDeleted, err = frameResult.RowsAffected()
	if err != nil {
		return rootDeletionResult{}, fmt.Errorf("count root frames: %w", err)
	}
	if err := finishRuntimeAuditRootDeleteTx(ctx, tx, runtimeDeleteScopeID); err != nil {
		return rootDeletionResult{}, err
	}
	return result, nil
}

// deleteProjectRootsTx discovers roots from both frames and every root-scoped
// runtime table. This prevents an orphaned runtime cohort from bypassing the
// root cleanup and blocking the final project DELETE with a foreign key error.
func (s *Store) deleteProjectRootsTx(
	ctx context.Context,
	tx *sql.Tx,
	ownerUserID, projectID string,
) ([]rootDeletionResult, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	projectID = strings.TrimSpace(projectID)
	if ownerUserID == "" || projectID == "" {
		return nil, errors.New("project root deletion owner and project are required")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT root_frame_id FROM (
			SELECT root_frame_id FROM frames WHERE project_id=?
			UNION SELECT root_frame_id FROM runner_large_tool_results WHERE owner_user_id=? AND project_id=?
			UNION SELECT root_frame_id FROM kernel_execution_backends WHERE owner_user_id=? AND project_id=?
			UNION SELECT root_frame_id FROM kernel_local_operations WHERE owner_user_id=? AND project_id=?
			UNION SELECT root_frame_id FROM transcript_streams WHERE owner_id=? AND project_id=?
			UNION SELECT root_frame_id FROM compute_workbench_jobs WHERE owner_user_id=? AND project_id=?
			UNION SELECT root_frame_id FROM scientific_compute_admissions WHERE owner_user_id=? AND project_id=?
			UNION SELECT root_frame_id FROM scientific_compute_handle_authorities WHERE owner_user_id=? AND project_id=?
			UNION SELECT root_frame_id FROM scientific_compute_receipts WHERE owner_user_id=? AND project_id=?
			UNION SELECT root_frame_id FROM artifact_folders WHERE project_id=?
			UNION SELECT metadata.root_frame_id
				FROM artifact_runtime_metadata AS metadata
				JOIN artifacts AS artifact ON artifact.id=metadata.artifact_id
				WHERE artifact.project_id=?
			UNION SELECT root_frame_id FROM workspace_runtime_delete_scopes WHERE owner_user_id=? AND project_id=?
		) WHERE root_frame_id IS NOT NULL AND root_frame_id<>'' ORDER BY root_frame_id`,
		projectID,
		ownerUserID, projectID,
		ownerUserID, projectID,
		ownerUserID, projectID,
		ownerUserID, projectID,
		ownerUserID, projectID,
		ownerUserID, projectID,
		ownerUserID, projectID,
		ownerUserID, projectID,
		projectID,
		projectID,
		ownerUserID, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list project deletion roots: %w", err)
	}
	rootIDs := make([]string, 0)
	for rows.Next() {
		var rootID string
		if err := rows.Scan(&rootID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan project deletion root: %w", err)
		}
		rootIDs = append(rootIDs, rootID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate project deletion roots: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close project deletion roots: %w", err)
	}
	results := make([]rootDeletionResult, 0, len(rootIDs))
	for _, rootID := range rootIDs {
		deleted, err := s.deleteRootAggregateTx(ctx, tx, ownerUserID, projectID, rootID)
		if err != nil {
			return nil, err
		}
		results = append(results, deleted)
	}
	return results, nil
}

// deleteSingleFrameTx is the canonical data-plane path for the newer
// single-frame API. It deliberately does not remove the whole root; the
// root/tree API above is used when a conversation task is deleted.
func (s *Store) deleteSingleFrameTx(
	ctx context.Context,
	tx *sql.Tx,
	expected Frame,
	ownerUserID string,
) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if tx == nil {
		return errors.New("frame deletion transaction is required")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	expected.ID = strings.TrimSpace(expected.ID)
	expected.ProjectID = strings.TrimSpace(expected.ProjectID)
	expected.RootFrameID = strings.TrimSpace(expected.RootFrameID)
	expected.IncarnationID = strings.TrimSpace(expected.IncarnationID)
	if expected.ID == "" || expected.ProjectID == "" || expected.RootFrameID == "" || expected.IncarnationID == "" || ownerUserID == "" {
		return errors.New("frame id, project, root frame, incarnation, and owner user id are required")
	}
	var projectID, rootFrameID, incarnationID string
	if err := tx.QueryRowContext(ctx, `
		SELECT frame.project_id,frame.root_frame_id,frame.incarnation_id
		FROM frames AS frame JOIN projects AS project ON project.id=frame.project_id
		WHERE frame.id=? AND project.user_id=?`, expected.ID, ownerUserID,
	).Scan(&projectID, &rootFrameID, &incarnationID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("frame %q does not exist", expected.ID)
		}
		return fmt.Errorf("authorize frame delete: %w", err)
	}
	if projectID != expected.ProjectID || rootFrameID != expected.RootFrameID || incarnationID != expected.IncarnationID {
		return fmt.Errorf("frame %q changed before delete", expected.ID)
	}
	if _, err := transcriptstore.DeleteFrameStreamsTx(ctx, tx, ownerUserID, projectID, expected.ID); err != nil {
		return fmt.Errorf("delete frame transcript streams: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM frames WHERE id=? AND project_id IN (SELECT id FROM projects WHERE user_id=?)`, expected.ID, ownerUserID,
	)
	if err != nil {
		return fmt.Errorf("delete frame: %w", err)
	}
	if err := requireOneMutationRow(result, "frame", expected.ID); err != nil {
		return err
	}
	return nil
}
