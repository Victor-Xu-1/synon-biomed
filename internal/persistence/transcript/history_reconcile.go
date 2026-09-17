package transcript

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ReconcileLegacyFrameHistoriesInput struct {
	Limit              int
	AfterOwnerID       string
	AfterSessionID     string
	MaxEvents          int
	MaxCandidates      int
	MaxShadowRows      int
	MaxBranches        int
	MaxCursorRows      int
	MaxAttempts        int
	MaxCheckpoints     int
	MaxBranchEvents    int
	MaxArtifactCommits int
	MaxArtifactRefs    int
	MaxRoutes          int
}

type LegacyFrameHistoryReconciliation struct {
	Scanned         int
	Activated       int
	Quarantined     int
	Blocked         int
	Deferred        int
	RemainingLegacy int
	Truncated       bool
	NextOwnerID     string
	NextSessionID   string
}

type legacyFrameHistoryCandidate struct {
	ownerID, sessionID, streamUID string
	epoch, inputRevision          int64
	consumedInputRevision         int64
	running, unresolvedDelivery   bool
}

func DefaultLegacyFrameHistoryReconciliationInput() ReconcileLegacyFrameHistoriesInput {
	return ReconcileLegacyFrameHistoriesInput{
		Limit: 8, MaxEvents: 100000, MaxCandidates: 10000, MaxShadowRows: 10000,
		MaxBranches: 1024, MaxCursorRows: 100000, MaxAttempts: 10000, MaxCheckpoints: 100000,
		MaxBranchEvents: 100000, MaxArtifactCommits: 100000, MaxArtifactRefs: 100000, MaxRoutes: 1024,
	}
}

// ReconcileLegacyFrameHistories advances only histories that the existing
// classification, backfill, cutover, and activation contracts prove safe. A
// poison/conflict audit remains durable quarantine evidence. Transient live
// work is left on its current authority and retried by a later reconciliation.
func (r *Repository) ReconcileLegacyFrameHistories(
	ctx context.Context,
	input ReconcileLegacyFrameHistoriesInput,
) (LegacyFrameHistoryReconciliation, error) {
	var report LegacyFrameHistoryReconciliation
	if r == nil || r.db == nil {
		return report, ErrSchemaUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLegacyFrameHistoryReconciliationInput(input); err != nil {
		return report, err
	}
	input.AfterOwnerID = strings.TrimSpace(input.AfterOwnerID)
	input.AfterSessionID = strings.TrimSpace(input.AfterSessionID)
	if (input.AfterOwnerID == "") != (input.AfterSessionID == "") {
		return report, errors.New("complete legacy frame history cursor is required")
	}
	candidates, truncated, err := r.listLegacyFrameHistoryCandidates(
		ctx, input.AfterOwnerID, input.AfterSessionID, input.Limit,
	)
	if err != nil {
		return report, err
	}
	report.Truncated = truncated
	if truncated && len(candidates) > 0 {
		report.NextOwnerID = candidates[len(candidates)-1].ownerID
		report.NextSessionID = candidates[len(candidates)-1].sessionID
	}
	for _, candidate := range candidates {
		if err := context.Cause(ctx); err != nil {
			return report, err
		}
		report.Scanned++
		state, err := r.GetBranchState(ctx, candidate.streamUID, candidate.ownerID)
		if err != nil {
			return report, fmt.Errorf("read legacy history branch authority: %w", err)
		}
		audit, _, err := r.AuditAskUserHistory(ctx, AuditAskUserHistoryInput{
			StreamUID: candidate.streamUID, OwnerID: candidate.ownerID, BranchID: state.ActiveBranchID,
			MaxEvents: input.MaxEvents, MaxCandidates: input.MaxCandidates, MaxShadowRows: input.MaxShadowRows,
		})
		if errors.Is(err, ErrHistoryAuditBudgetExceeded) {
			audit, _, err = r.auditOrdinaryFrameHistory(
				ctx, candidate.streamUID, candidate.ownerID, state.ActiveBranchID,
			)
			if errors.Is(err, errOrdinaryHistoryShapeUnsupported) {
				report.Deferred++
				continue
			}
		}
		if errors.Is(err, ErrBranchStateStale) {
			report.Blocked++
			continue
		}
		if err != nil {
			return report, fmt.Errorf("audit legacy frame history: %w", err)
		}
		switch audit.Status {
		case AskUserHistoryQuarantined, AskUserHistoryConflict:
			report.Quarantined++
			continue
		case AskUserHistoryEligible:
			if candidate.inputRevision != candidate.consumedInputRevision || candidate.running || candidate.unresolvedDelivery {
				report.Blocked++
				continue
			}
		case AskUserHistoryNotApplicable:
			if candidate.inputRevision != candidate.consumedInputRevision || candidate.running || candidate.unresolvedDelivery {
				report.Blocked++
				continue
			}
			_, _, err := r.ActivateOrdinaryFrameHistory(ctx, audit.RunID, candidate.streamUID, candidate.ownerID)
			if errors.Is(err, errOrdinaryHistoryShapeUnsupported) {
				report.Deferred++
				continue
			}
			if isLegacyHistoryTransientBlock(err) {
				report.Blocked++
				continue
			}
			if err != nil {
				return report, fmt.Errorf("activate ordinary legacy frame history: %w", err)
			}
			report.Activated++
			continue
		case AskUserHistoryNativeV1:
			report.Deferred++
			continue
		default:
			return report, ErrEventConflict
		}
		backfill, _, err := r.StageAskUserHistoryBackfill(ctx, StageAskUserHistoryBackfillInput{
			RunID: audit.RunID, StreamUID: candidate.streamUID, OwnerID: candidate.ownerID,
			BranchID: state.ActiveBranchID, MaxEvents: input.MaxEvents,
			MaxCandidates: input.MaxCandidates, MaxShadowRows: input.MaxShadowRows,
		})
		if isLegacyHistoryTransientBlock(err) {
			report.Blocked++
			continue
		}
		if err != nil {
			return report, fmt.Errorf("stage legacy frame history: %w", err)
		}
		cutover, _, err := r.PrepareAskUserHistoryCutover(ctx, PrepareAskUserHistoryCutoverInput{
			BackfillID: backfill.BackfillID, StreamUID: candidate.streamUID, OwnerID: candidate.ownerID,
			MaxBranches: input.MaxBranches, MaxEvents: input.MaxEvents,
			MaxCursorRows: input.MaxCursorRows, MaxShadowRows: input.MaxShadowRows,
		})
		if isLegacyHistoryTransientBlock(err) {
			report.Blocked++
			continue
		}
		if err != nil {
			return report, fmt.Errorf("prepare legacy frame history cutover: %w", err)
		}
		_, _, err = r.ActivateAskUserHistoryCutover(ctx, ActivateAskUserHistoryCutoverInput{
			CutoverID: cutover.CutoverID, OwnerID: candidate.ownerID,
			MaxBranches: input.MaxBranches, MaxEvents: input.MaxEvents, MaxAttempts: input.MaxAttempts,
			MaxCheckpoints: input.MaxCheckpoints, MaxBranchEvents: input.MaxBranchEvents,
			MaxArtifactCommits: input.MaxArtifactCommits, MaxArtifactRefs: input.MaxArtifactRefs,
			MaxRoutes: input.MaxRoutes,
		})
		if isLegacyHistoryTransientBlock(err) {
			report.Blocked++
			continue
		}
		if errors.Is(err, ErrEventConflict) {
			completed, blocked, checkErr := r.classifyLegacyHistoryActivationConflict(ctx, candidate)
			if checkErr != nil {
				return report, checkErr
			}
			if completed {
				report.Activated++
				continue
			}
			if blocked {
				report.Blocked++
				continue
			}
		}
		if err != nil {
			return report, fmt.Errorf("activate legacy frame history: %w", err)
		}
		report.Activated++
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_frame_authority
		WHERE read_authority='legacy_mixed_v1' AND write_authority='legacy_frame_ref_v1'`).
		Scan(&report.RemainingLegacy); err != nil {
		return report, schemaError(err)
	}
	return report, nil
}

func (r *Repository) classifyLegacyHistoryActivationConflict(
	ctx context.Context,
	candidate legacyFrameHistoryCandidate,
) (completed bool, blocked bool, err error) {
	var streamUID, readAuthority, writeAuthority string
	var epoch, inputRevision, consumedInputRevision int64
	var hasActivation, running, unresolvedDelivery, unboundArtifact bool
	err = r.db.QueryRowContext(ctx, `SELECT authority.active_stream_uid,authority.active_epoch,
		authority.read_authority,authority.write_authority,authority.activation_id IS NOT NULL,
		stream.input_revision,stream.consumed_input_revision,
		EXISTS(SELECT 1 FROM transcript_runner_attempts attempt
			WHERE attempt.stream_uid=stream.stream_uid AND attempt.status='running'),
		EXISTS(SELECT 1 FROM transcript_delivery_intents intent
			WHERE intent.stream_uid=stream.stream_uid AND intent.status IN ('pending','inflight','failed')),
		EXISTS(SELECT 1 FROM transcript_artifact_commits artifact
			WHERE artifact.stream_uid=stream.stream_uid AND artifact.bound_event_id IS NULL)
		FROM transcript_frame_authority authority
		JOIN transcript_streams stream ON stream.stream_uid=authority.active_stream_uid
		WHERE authority.owner_id=? AND authority.session_id=?`, candidate.ownerID, candidate.sessionID).Scan(
		&streamUID, &epoch, &readAuthority, &writeAuthority, &hasActivation,
		&inputRevision, &consumedInputRevision, &running, &unresolvedDelivery, &unboundArtifact,
	)
	if err != nil {
		return false, false, schemaError(err)
	}
	if readAuthority == "transcript_payload_v1" && writeAuthority == "transcript_payload_v1" && hasActivation {
		return true, false, nil
	}
	if streamUID != candidate.streamUID || epoch != candidate.epoch || readAuthority != "legacy_mixed_v1" ||
		writeAuthority != "legacy_frame_ref_v1" || hasActivation {
		return false, false, nil
	}
	return false, inputRevision != consumedInputRevision || running || unresolvedDelivery || unboundArtifact, nil
}

func validateLegacyFrameHistoryReconciliationInput(input ReconcileLegacyFrameHistoriesInput) error {
	if input.Limit <= 0 || input.Limit > 1000 || input.MaxEvents <= 0 || input.MaxEvents > 100000 ||
		input.MaxCandidates <= 0 || input.MaxCandidates > 10000 ||
		input.MaxShadowRows <= 0 || input.MaxShadowRows > 10000 ||
		input.MaxBranches <= 0 || input.MaxBranches > 1024 ||
		input.MaxCursorRows <= 0 || input.MaxCursorRows > 100000 ||
		input.MaxAttempts <= 0 || input.MaxAttempts > 10000 ||
		input.MaxCheckpoints <= 0 || input.MaxCheckpoints > 100000 ||
		input.MaxBranchEvents <= 0 || input.MaxBranchEvents > 100000 ||
		input.MaxArtifactCommits <= 0 || input.MaxArtifactCommits > 100000 ||
		input.MaxArtifactRefs <= 0 || input.MaxArtifactRefs > 100000 ||
		input.MaxRoutes <= 0 || input.MaxRoutes > 1024 {
		return errors.New("bounded legacy frame history reconciliation resources are required")
	}
	return nil
}

func (r *Repository) listLegacyFrameHistoryCandidates(
	ctx context.Context,
	afterOwnerID, afterSessionID string,
	limit int,
) ([]legacyFrameHistoryCandidate, bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT authority.owner_id,authority.session_id,
		authority.active_stream_uid,authority.active_epoch,stream.input_revision,stream.consumed_input_revision,
		EXISTS(SELECT 1 FROM transcript_runner_attempts attempt
			WHERE attempt.stream_uid=stream.stream_uid AND attempt.status='running'),
		EXISTS(SELECT 1 FROM transcript_delivery_intents intent
			WHERE intent.stream_uid=stream.stream_uid AND intent.status IN ('pending','inflight','failed'))
		FROM transcript_frame_authority authority
		JOIN transcript_streams stream ON stream.stream_uid=authority.active_stream_uid
		LEFT JOIN transcript_history_classification_runs audit ON audit.stream_uid=stream.stream_uid
		WHERE authority.read_authority='legacy_mixed_v1' AND authority.write_authority='legacy_frame_ref_v1'
			AND (authority.owner_id>? OR (authority.owner_id=? AND authority.session_id>?))
		GROUP BY authority.owner_id,authority.session_id,authority.active_stream_uid,authority.active_epoch,
			stream.input_revision,stream.consumed_input_revision
		ORDER BY authority.owner_id,authority.session_id
		LIMIT ?`, afterOwnerID, afterOwnerID, afterSessionID, limit+1)
	if err != nil {
		return nil, false, schemaError(err)
	}
	defer rows.Close()
	result := make([]legacyFrameHistoryCandidate, 0, limit+1)
	for rows.Next() {
		var candidate legacyFrameHistoryCandidate
		if err := rows.Scan(
			&candidate.ownerID, &candidate.sessionID, &candidate.streamUID, &candidate.epoch,
			&candidate.inputRevision, &candidate.consumedInputRevision,
			&candidate.running, &candidate.unresolvedDelivery,
		); err != nil {
			return nil, false, schemaError(err)
		}
		if strings.TrimSpace(candidate.ownerID) == "" || strings.TrimSpace(candidate.sessionID) == "" ||
			strings.TrimSpace(candidate.streamUID) == "" || candidate.epoch <= 0 {
			return nil, false, ErrEventConflict
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, false, schemaError(err)
	}
	truncated := len(result) > limit
	if truncated {
		result = result[:limit]
	}
	return result, truncated, nil
}

func isLegacyHistoryTransientBlock(err error) bool {
	return errors.Is(err, ErrHistoryBackfillBlocked) || errors.Is(err, ErrBranchStateStale) ||
		errors.Is(err, ErrClaimStale) || errors.Is(err, ErrDeliveryClaimStale)
}
