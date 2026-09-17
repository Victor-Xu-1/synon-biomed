package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DetachedKernelInventoryEntry is the durable cross-process projection used
// by the global compute panel. It deliberately contains only validated backend
// and execution facts; process liveness is verified by the server against the
// persisted PID start ticks before the entry is exposed.
type DetachedKernelInventoryEntry struct {
	Backend        KernelExecutionBackend
	SessionSpec    KernelExecutionSessionSpecV1
	Operation      *KernelLocalOperation
	Execution      *DetachedKernelExecution
	Request        *KernelDetachedExecutionRequestV1
	ExecutionCount int
}

// CountActiveDetachedKernelExecutions is the durable, process-independent
// liveness projection for health and deployment admission. In-memory manager
// handles are intentionally not authoritative across runner handoff or server
// restart; a fresh detached-backend heartbeat plus a nonterminal execution is.
func (s *Store) CountActiveDetachedKernelExecutions(ctx context.Context) (int, error) {
	if s == nil || s.db == nil || ctx == nil {
		return 0, errors.New("detached kernel execution store and context are required")
	}
	now := s.now().UTC()
	startingAfter := now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	heartbeatAfter := now.Add(-90 * time.Second).Format(time.RFC3339Nano)
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM kernel_detached_executions execution
		JOIN kernel_execution_backends backend ON backend.backend_id=execution.backend_id
			AND backend.backend_generation=execution.backend_generation
		WHERE execution.state IN ('accepted','dispatch_committed','started','cancel_requested')
			AND ((backend.state='starting' AND backend.updated_at>=?) OR
				(backend.state IN ('ready','draining') AND COALESCE(backend.heartbeat_at,backend.updated_at)>=?))`,
		startingAfter, heartbeatAfter).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active detached kernel executions: %w", err)
	}
	return count, nil
}

// ListActiveDetachedKernelInventory returns non-terminal backend sessions for
// one owner, together with the newest non-terminal execution when present.
// Stale durable rows are intentionally returned: the caller must prove process
// identity from PID plus start ticks before treating a ready backend as live.
func (s *Store) ListActiveDetachedKernelInventory(
	ctx context.Context,
	ownerUserID string,
	limit int,
) ([]DetachedKernelInventoryEntry, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	if s == nil || s.db == nil || ctx == nil || ownerUserID == "" || limit <= 0 || limit > 1000 {
		return nil, errors.New("detached kernel inventory owner and limit are required")
	}
	now := s.now().UTC()
	startingAfter := now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	heartbeatAfter := now.Add(-90 * time.Second).Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `SELECT backend_id FROM kernel_execution_backends
		WHERE owner_user_id=? AND (
			(state='starting' AND updated_at>=?) OR
			(state IN ('ready','draining') AND COALESCE(heartbeat_at,updated_at)>=?)
		) ORDER BY updated_at DESC,backend_id DESC LIMIT ?`, ownerUserID, startingAfter, heartbeatAfter, limit)
	if err != nil {
		return nil, fmt.Errorf("list detached kernel backends: %w", err)
	}
	defer rows.Close()
	backendIDs := make([]string, 0, limit)
	for rows.Next() {
		var backendID string
		if err := rows.Scan(&backendID); err != nil {
			return nil, err
		}
		backendIDs = append(backendIDs, backendID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	entries := make([]DetachedKernelInventoryEntry, 0, len(backendIDs))
	for _, backendID := range backendIDs {
		backend, found, err := s.GetKernelExecutionBackend(ctx, backendID)
		if err != nil {
			return nil, err
		}
		if !found || backend.OwnerUserID != ownerUserID {
			continue
		}
		spec, err := DecodeKernelExecutionSessionSpecV1(backend.SessionSpecJSON)
		if err != nil {
			return nil, ErrKernelExecutionBackendConflict
		}
		entry := DetachedKernelInventoryEntry{Backend: backend, SessionSpec: spec}
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_detached_executions
			WHERE backend_id=? AND state!='evidence_lost'`, backendID).Scan(&entry.ExecutionCount); err != nil {
			return nil, err
		}
		var executionID string
		err = s.db.QueryRowContext(ctx, `SELECT execution_id FROM kernel_detached_executions
			WHERE backend_id=? AND state IN ('accepted','dispatch_committed','started','cancel_requested')
			ORDER BY accepted_at DESC,execution_id DESC LIMIT 1`, backendID).Scan(&executionID)
		if errors.Is(err, sql.ErrNoRows) {
			entries = append(entries, entry)
			continue
		}
		if err != nil {
			return nil, err
		}
		execution, found, err := s.GetDetachedKernelExecution(ctx, executionID)
		if err != nil {
			return nil, err
		}
		if !found || execution.BackendID != backendID || execution.BackendGeneration != backend.BackendGeneration {
			return nil, ErrDetachedKernelExecutionConflict
		}
		request, err := DecodeKernelDetachedExecutionRequestV1(execution.RequestJSON)
		if err != nil {
			return nil, ErrDetachedKernelExecutionConflict
		}
		operation, found, err := s.GetKernelLocalOperation(ctx, ownerUserID, execution.OperationID)
		if err != nil {
			return nil, err
		}
		if !found || operation.ExecutionID != execution.ExecutionID || operation.KernelID != backend.KernelID {
			return nil, ErrDetachedKernelExecutionConflict
		}
		entry.Execution = &execution
		entry.Request = &request
		entry.Operation = &operation
		entries = append(entries, entry)
	}
	return entries, nil
}
