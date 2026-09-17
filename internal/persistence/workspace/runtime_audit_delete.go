package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func prepareRuntimeAuditRootDeleteTx(
	ctx context.Context,
	tx *sql.Tx,
	ownerUserID, projectID, rootFrameID string,
) (string, error) {
	if tx == nil {
		return "", fmt.Errorf("runtime audit delete transaction is required")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	projectID = strings.TrimSpace(projectID)
	rootFrameID = strings.TrimSpace(rootFrameID)
	if ownerUserID == "" || projectID == "" || rootFrameID == "" {
		return "", fmt.Errorf("runtime audit delete owner, project, and root frame are required")
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
		return "", fmt.Errorf("defer runtime audit foreign keys: %w", err)
	}
	// A scope is committed only as part of a deletion transaction, but remove
	// any legacy residue before recreating it so a previously interrupted
	// deletion cannot make the next idempotent delete fail on UNIQUE(scope).
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_runtime_delete_scopes
		WHERE owner_user_id=? AND project_id=? AND root_frame_id=?`,
		ownerUserID, projectID, rootFrameID); err != nil {
		return "", fmt.Errorf("clear stale runtime audit delete scope: %w", err)
	}
	scopeID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_runtime_delete_scopes(
		scope_id,owner_user_id,project_id,root_frame_id,created_at
	) VALUES(?,?,?,?,?)`, scopeID, ownerUserID, projectID, rootFrameID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return "", fmt.Errorf("authorize runtime audit root delete: %w", err)
	}
	operationScope := `SELECT operation_id FROM kernel_local_operations
		WHERE owner_user_id=? AND project_id=? AND root_frame_id=?`
	streamScope := `SELECT stream_uid FROM transcript_streams
		WHERE owner_id=? AND project_id=? AND root_frame_id=?`
	args := []any{ownerUserID, projectID, rootFrameID}
	deletions := []struct {
		label string
		query string
	}{
		{
			label: "detached host calls",
			query: `DELETE FROM kernel_execution_host_calls WHERE execution_id IN (
				SELECT execution_id FROM kernel_detached_executions WHERE operation_id IN (` + operationScope + `)
			)`,
		},
		{
			label: "detached executions",
			query: `DELETE FROM kernel_detached_executions WHERE operation_id IN (` + operationScope + `)`,
		},
		{
			label: "detached result receipts",
			query: `DELETE FROM kernel_execution_result_receipts WHERE operation_id IN (` + operationScope + `)`,
		},
		{
			label: "execution backends",
			query: `DELETE FROM kernel_execution_backends
				WHERE owner_user_id=? AND project_id=? AND root_frame_id=?`,
		},
		{
			label: "runner large tool results",
			query: `DELETE FROM runner_large_tool_results
				WHERE owner_user_id=? AND project_id=? AND root_frame_id=?`,
		},
		{
			label: "operation materializations",
			query: `DELETE FROM kernel_local_operation_materializations WHERE operation_id IN (` + operationScope + `)`,
		},
		{
			label: "operation protocol receipts",
			query: `DELETE FROM kernel_local_operation_protocol_receipts WHERE operation_id IN (` + operationScope + `)`,
		},
		{
			label: "operation transitions",
			query: `DELETE FROM kernel_local_operation_transitions WHERE operation_id IN (` + operationScope + `)`,
		},
		{
			label: "local operations",
			query: `DELETE FROM kernel_local_operations WHERE operation_id IN (` + operationScope + `)`,
		},
		{
			label: "tool call items",
			query: `DELETE FROM transcript_tool_call_items WHERE stream_uid IN (` + streamScope + `)`,
		},
		{
			label: "tool call batches",
			query: `DELETE FROM transcript_tool_call_batches WHERE stream_uid IN (` + streamScope + `)`,
		},
		{
			label: "scientific compute submissions",
			query: `DELETE FROM scientific_compute_submissions
				WHERE job_id IN (
					SELECT job_id FROM compute_workbench_jobs
					WHERE owner_user_id=? AND project_id=? AND root_frame_id=?
				)`,
		},
		{
			label: "scientific compute receipt outputs",
			query: `DELETE FROM scientific_compute_receipt_outputs WHERE job_id IN (
				SELECT job_id FROM scientific_compute_receipts
				WHERE owner_user_id=? AND project_id=? AND root_frame_id=?
			)`,
		},
		{
			label: "scientific compute receipts",
			query: `DELETE FROM scientific_compute_receipts
				WHERE owner_user_id=? AND project_id=? AND root_frame_id=?`,
		},
		{
			label: "scientific compute handles",
			query: `DELETE FROM scientific_compute_handles WHERE job_id IN (
				SELECT job_id FROM scientific_compute_admissions
				WHERE owner_user_id=? AND project_id=? AND root_frame_id=?
			)`,
		},
	}
	for _, deletion := range deletions {
		if _, err := tx.ExecContext(ctx, deletion.query, args...); err != nil {
			return "", fmt.Errorf("delete runtime audit %s: %w", deletion.label, err)
		}
	}
	if err := deleteScientificComputeHandleAuthoritiesTx(ctx, tx, ownerUserID, projectID, rootFrameID); err != nil {
		return "", err
	}
	for _, deletion := range []struct {
		label string
		query string
	}{
		{
			label: "scientific compute admissions",
			query: `DELETE FROM scientific_compute_admissions
				WHERE owner_user_id=? AND project_id=? AND root_frame_id=?`,
		},
		{
			label: "scientific compute jobs",
			query: `DELETE FROM compute_workbench_jobs
				WHERE owner_user_id=? AND project_id=? AND root_frame_id=?`,
		},
	} {
		if _, err := tx.ExecContext(ctx, deletion.query, args...); err != nil {
			return "", fmt.Errorf("delete runtime audit %s: %w", deletion.label, err)
		}
	}
	return scopeID, nil
}

// deleteScientificComputeHandleAuthoritiesTx removes the self-referencing
// authority chain from the tail toward the root. A single DELETE statement is
// unsafe here because the schema deliberately uses RESTRICT on the previous
// authority relation.
func deleteScientificComputeHandleAuthoritiesTx(
	ctx context.Context,
	tx *sql.Tx,
	ownerUserID, projectID, rootFrameID string,
) error {
	for {
		var jobID string
		var authoritySeq int64
		err := tx.QueryRowContext(ctx, `
			SELECT job_id,authority_seq
			FROM scientific_compute_handle_authorities
			WHERE owner_user_id=? AND project_id=? AND root_frame_id=?
			ORDER BY job_id DESC,authority_seq DESC LIMIT 1`,
			ownerUserID, projectID, rootFrameID,
		).Scan(&jobID, &authoritySeq)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("select scientific compute handle authority for delete: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			DELETE FROM scientific_compute_handle_authorities
			WHERE owner_user_id=? AND project_id=? AND root_frame_id=? AND job_id=? AND authority_seq=?`,
			ownerUserID, projectID, rootFrameID, jobID, authoritySeq)
		if err != nil {
			return fmt.Errorf("delete scientific compute handle authority: %w", err)
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count scientific compute handle authority delete: %w", err)
		}
		if deleted != 1 {
			return fmt.Errorf("scientific compute handle authority %q/%d changed during delete", jobID, authoritySeq)
		}
	}
}

func finishRuntimeAuditRootDeleteTx(ctx context.Context, tx *sql.Tx, scopeID string) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM workspace_runtime_delete_scopes WHERE scope_id=?`, scopeID)
	if err != nil {
		return fmt.Errorf("close runtime audit delete scope: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count closed runtime audit delete scopes: %w", err)
	}
	if deleted != 1 {
		return fmt.Errorf("runtime audit delete scope %q is unavailable", scopeID)
	}
	return nil
}
