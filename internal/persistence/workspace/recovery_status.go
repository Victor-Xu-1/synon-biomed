package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type RecoveryStatus struct {
	ComputeJobsByState      map[string]int `json:"computeJobsByState"`
	PendingTerminations     int            `json:"pendingTerminations"`
	ManagedEndpointsByState map[string]int `json:"managedEndpointsByState"`
	KernelChildrenByState   map[string]int `json:"kernelChildrenByState"`
}

func (s *Store) RecoveryStatus(ctx context.Context) (RecoveryStatus, error) {
	if s == nil || s.db == nil {
		return RecoveryStatus{}, errors.New("workspace store is closed")
	}
	status := RecoveryStatus{
		ComputeJobsByState: map[string]int{}, ManagedEndpointsByState: map[string]int{}, KernelChildrenByState: map[string]int{},
	}
	if err := scanStateCounts(ctx, s.db, `SELECT state, COUNT(*) FROM compute_workbench_jobs GROUP BY state ORDER BY state`, status.ComputeJobsByState); err != nil {
		return RecoveryStatus{}, fmt.Errorf("read compute recovery state: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM compute_pending_terminate`).Scan(&status.PendingTerminations); err != nil {
		return RecoveryStatus{}, fmt.Errorf("read pending compute terminations: %w", err)
	}
	if err := scanStateCounts(ctx, s.db, `SELECT state, COUNT(*) FROM compute_managed_endpoints GROUP BY state ORDER BY state`, status.ManagedEndpointsByState); err != nil {
		return RecoveryStatus{}, fmt.Errorf("read managed endpoint recovery state: %w", err)
	}
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return RecoveryStatus{}, err
	}
	if err := scanStateCounts(ctx, s.db, `SELECT status, COUNT(*) FROM kernel_child_supervision GROUP BY status ORDER BY status`, status.KernelChildrenByState); err != nil {
		return RecoveryStatus{}, fmt.Errorf("read kernel recovery state: %w", err)
	}
	return status, nil
}

type stateCountQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func scanStateCounts(ctx context.Context, query stateCountQuery, statement string, destination map[string]int) error {
	rows, err := query.QueryContext(ctx, statement)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return err
		}
		destination[state] = count
	}
	return rows.Err()
}
