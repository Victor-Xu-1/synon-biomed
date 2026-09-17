package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// KernelLocalExecApprovalRecoveryCandidate is the durable authority required
// to settle an approval whose exact runner attempt no longer owns execution.
// A live lease is advisory only: ResolveKernelLocalExecApproval rechecks the
// same attempt transactionally before committing a denial.
type KernelLocalExecApprovalRecoveryCandidate struct {
	Resolution     KernelLocalExecApprovalResolutionInput
	RunnerLive     bool
	LeaseExpiresAt time.Time
}

// ListKernelLocalExecApprovalRecoveryCandidates returns a bounded, stable
// snapshot of unresolved local-execution approvals. It does not mutate state;
// callers must use ResolveKernelLocalExecApproval with RequireStaleRunner so
// the lease is checked again in the write transaction.
func (s *Store) ListKernelLocalExecApprovalRecoveryCandidates(
	ctx context.Context,
	limit int,
) ([]KernelLocalExecApprovalRecoveryCandidate, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return nil, false, errors.New("kernel local execution recovery context is required")
	}
	if limit <= 0 || limit > 1000 {
		return nil, false, errors.New("kernel local execution recovery limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.user_id,f.project_id,f.id,f.incarnation_id,f.root_frame_id,root.incarnation_id,
			json_extract(requested.payload,'$.request_id'),
			json_extract(requested.payload,'$.tool'),json_extract(requested.payload,'$.environment'),
			json_extract(requested.payload,'$.input_sha256'),json_extract(requested.payload,'$.stream_uid'),
			json_extract(requested.payload,'$.runner_id'),json_extract(requested.payload,'$.runner_attempt'),
			json_extract(requested.payload,'$.kernel_id'),json_extract(requested.payload,'$.kernel_generation'),
			attempt.status,attempt.expires_at,
			EXISTS(SELECT 1 FROM transcript_runner_attempts newer
				WHERE newer.stream_uid=json_extract(requested.payload,'$.stream_uid')
					AND newer.attempt>CAST(json_extract(requested.payload,'$.runner_attempt') AS INTEGER))
		FROM frame_runtime_metadata metadata
		JOIN frames f ON f.id=metadata.frame_id
		JOIN frames root ON root.id=f.root_frame_id AND root.project_id=f.project_id
		JOIN projects p ON p.id=f.project_id
		JOIN json_each(metadata.context_data,'$._pending_input_requests') pending
		JOIN frame_events requested ON requested.frame_id=f.id
			AND requested.event_type=?
			AND json_extract(requested.payload,'$.request_id')=json_extract(pending.value,'$.requestId')
		LEFT JOIN transcript_runner_attempts attempt
			ON attempt.stream_uid=json_extract(requested.payload,'$.stream_uid')
			AND attempt.attempt=CAST(json_extract(requested.payload,'$.runner_attempt') AS INTEGER)
			AND attempt.runner_id=json_extract(requested.payload,'$.runner_id')
		WHERE json_type(pending.value)='object'
			AND CAST(json_extract(pending.value,'$.version') AS INTEGER)=1
			AND lower(trim(json_extract(pending.value,'$.kind')))='local_exec'
		ORDER BY f.id,CAST(pending.key AS INTEGER)
		LIMIT ?`, KernelLocalExecApprovalRequestedEventType, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list kernel local execution approvals: %w", err)
	}
	defer rows.Close()

	now := s.now().UTC()
	candidates := make([]KernelLocalExecApprovalRecoveryCandidate, 0, limit)
	more := false
	for rows.Next() {
		if len(candidates) == limit {
			more = true
			break
		}
		var ownerID, projectID, frameID, frameIncarnation, rootFrameID, rootIncarnation string
		var requestID, tool, environment, inputSHA, streamUID, runnerID, kernelID, generationRaw sql.NullString
		var runnerAttempt sql.NullInt64
		var attemptStatus sql.NullString
		var leaseExpires sql.NullTime
		var newer bool
		if err := rows.Scan(&ownerID, &projectID, &frameID, &frameIncarnation, &rootFrameID, &rootIncarnation,
			&requestID, &tool, &environment, &inputSHA, &streamUID, &runnerID, &runnerAttempt,
			&kernelID, &generationRaw, &attemptStatus, &leaseExpires, &newer); err != nil {
			return nil, false, fmt.Errorf("scan kernel local execution approval: %w", err)
		}
		generation, err := strconv.ParseUint(strings.TrimSpace(generationRaw.String), 10, 64)
		if err != nil {
			return nil, false, errors.New("kernel local execution approval generation is invalid")
		}
		resolution := KernelLocalExecApprovalResolutionInput{
			OwnerUserID: ownerID, ProjectID: projectID, FrameID: frameID,
			FrameIncarnationID: frameIncarnation, RootFrameID: rootFrameID,
			RootIncarnationID: rootIncarnation, RequestID: requestID.String,
			Tool: tool.String, Environment: environment.String, InputSHA256: inputSHA.String,
			StreamUID: streamUID.String, RunnerID: runnerID.String, RunnerAttempt: runnerAttempt.Int64,
			KernelID: kernelID.String, ExpectedGeneration: generation,
			Approved: false, Scope: "once", RequireStaleRunner: true,
		}
		if err := normalizeKernelLocalExecApprovalResolution(&resolution); err != nil {
			return nil, false, err
		}
		live := attemptStatus.String == "running" && leaseExpires.Valid && leaseExpires.Time.After(now) && !newer
		candidates = append(candidates, KernelLocalExecApprovalRecoveryCandidate{
			Resolution: resolution, RunnerLive: live, LeaseExpiresAt: leaseExpires.Time.UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate kernel local execution approvals: %w", err)
	}
	return candidates, more, nil
}
