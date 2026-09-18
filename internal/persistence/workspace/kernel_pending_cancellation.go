package workspace

import (
	"context"
	"errors"
)

// ListPendingKernelCancellationIDs reads a bounded page from the cancellation
// ledger, including accepted work that never reached the in-memory executor.
// Acknowledgement is not completion: an acknowledged request stays discoverable
// until its terminal receipt commits, including a retry after a store lock.
func (s *Store) ListPendingKernelCancellationIDs(ctx context.Context, backendID string, generation int64) ([]string, error) {
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(backendID) || generation <= 0 {
		return nil, errors.New("kernel cancellation inventory authority is required")
	}
	readDB := s.readDB
	if readDB == nil {
		readDB = s.db
	}
	rows, err := readDB.QueryContext(ctx, `SELECT execution_id FROM kernel_detached_executions
   WHERE backend_id=? AND backend_generation=? AND state='cancel_requested'
   ORDER BY execution_id LIMIT 64`, backendID, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
