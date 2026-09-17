package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DataLifecyclePolicy defines the reference lifecycle: transient
// content gets a short reference-safe grace period, resolved transport state
// is kept for one week, completed compute usage for 30 days, and superseded
// memory for 90 days. Conversation messages and final artifacts are not
// time-limited by this policy.
type DataLifecyclePolicy struct {
	ContentSnapshotGrace       time.Duration
	EphemeralArtifactMaxAge    time.Duration
	TerminalRealtimeGrace      time.Duration
	TransportReceiptMaxAge     time.Duration
	ComputeUsageMaxAge         time.Duration
	SupersededMemoryMaxAge     time.Duration
	ResolvedQueuedIntentMaxAge time.Duration
}

func DefaultDataLifecyclePolicy() DataLifecyclePolicy {
	return DataLifecyclePolicy{
		ContentSnapshotGrace:       time.Hour,
		EphemeralArtifactMaxAge:    24 * time.Hour,
		TerminalRealtimeGrace:      time.Hour,
		TransportReceiptMaxAge:     7 * 24 * time.Hour,
		ComputeUsageMaxAge:         30 * 24 * time.Hour,
		SupersededMemoryMaxAge:     90 * 24 * time.Hour,
		ResolvedQueuedIntentMaxAge: 7 * 24 * time.Hour,
	}
}

type DataLifecycleReport struct {
	ContentSnapshots       int64 `json:"contentSnapshots"`
	EphemeralArtifacts     int64 `json:"ephemeralArtifacts"`
	ComputeUsage           int64 `json:"computeUsage"`
	SupersededMemories     int64 `json:"supersededMemories"`
	ResolvedQueuedIntents  int64 `json:"resolvedQueuedIntents"`
	RealtimeEvents         int64 `json:"realtimeEvents"`
	DeliveredOutbox        int64 `json:"deliveredOutbox"`
	ExpiredDeadLetters     int64 `json:"expiredDeadLetters"`
	ArtifactBlobsScheduled int   `json:"artifactBlobsScheduled"`
}

func (r DataLifecycleReport) Changed() bool {
	return r.ContentSnapshots+r.EphemeralArtifacts+r.ComputeUsage+r.SupersededMemories+
		r.ResolvedQueuedIntents+r.RealtimeEvents+r.DeliveredOutbox+r.ExpiredDeadLetters > 0
}

// SweepDataLifecycle removes only data that is either reproducible or already
// terminal. A running frame, conversation message, execution log, final
// artifact, active memory, pending queue item, and undelivered outbox row is
// never selected by this sweep.
func (s *Store) SweepDataLifecycle(ctx context.Context, policy DataLifecyclePolicy) (DataLifecycleReport, error) {
	if s == nil || s.db == nil {
		return DataLifecycleReport{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return DataLifecycleReport{}, errors.New("data lifecycle context is required")
	}
	if err := validateDataLifecyclePolicy(policy); err != nil {
		return DataLifecycleReport{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DataLifecycleReport{}, fmt.Errorf("begin data lifecycle sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	report := DataLifecycleReport{}
	blobPaths, err := lifecycleEphemeralArtifactBlobs(ctx, tx, now.Add(-policy.EphemeralArtifactMaxAge))
	if err != nil {
		return DataLifecycleReport{}, err
	}
	report.ArtifactBlobsScheduled = len(blobPaths)
	if report.EphemeralArtifacts, err = lifecycleDelete(ctx, tx, `
		DELETE FROM artifacts
		WHERE id IN (
			SELECT artifact.id
			FROM artifacts AS artifact
			JOIN artifact_runtime_metadata AS metadata ON metadata.artifact_id=artifact.id
			LEFT JOIN frames AS root ON root.id=metadata.root_frame_id
			WHERE metadata.is_ephemeral=1 AND artifact.updated_at<?
				AND (root.id IS NULL OR root.status IN ('completed','failed','cancelled','success','replaced'))
		)`, now.Add(-policy.EphemeralArtifactMaxAge)); err != nil {
		return DataLifecycleReport{}, fmt.Errorf("delete stale ephemeral artifacts: %w", err)
	}
	if report.ContentSnapshots, err = lifecycleDelete(ctx, tx, `
		DELETE FROM content_snapshots
		WHERE created_at<?
			AND NOT EXISTS (
				SELECT 1 FROM artifact_version_provenance AS provenance
				WHERE provenance.lineage_snapshot_hash=content_snapshots.hash
					OR provenance.env_snapshot_hash=content_snapshots.hash
			)`, now.Add(-policy.ContentSnapshotGrace)); err != nil {
		return DataLifecycleReport{}, fmt.Errorf("delete unreferenced content snapshots: %w", err)
	}
	if report.ComputeUsage, err = lifecycleDelete(ctx, tx, `DELETE FROM compute_usage
		WHERE ended_at IS NOT NULL AND ended_at<?`, now.Add(-policy.ComputeUsageMaxAge)); err != nil {
		return DataLifecycleReport{}, fmt.Errorf("delete expired compute usage: %w", err)
	}
	// Delete only the oldest safe predecessor in a supersession chain. Deleting
	// a middle row would SET NULL on its predecessor and accidentally reactivate
	// stale memory.
	if report.SupersededMemories, err = lifecycleDelete(ctx, tx, `
		DELETE FROM memories
		WHERE superseded_by IS NOT NULL AND updated_at<?
			AND EXISTS (SELECT 1 FROM memories AS successor WHERE successor.id=memories.superseded_by)
			AND NOT EXISTS (SELECT 1 FROM memories AS predecessor WHERE predecessor.superseded_by=memories.id)`,
		now.Add(-policy.SupersededMemoryMaxAge)); err != nil {
		return DataLifecycleReport{}, fmt.Errorf("delete expired superseded memories: %w", err)
	}
	if report.ResolvedQueuedIntents, err = lifecycleDelete(ctx, tx, `
		DELETE FROM queued_user_messages
		WHERE state IN ('drained','retracted') AND resolved_at IS NOT NULL AND resolved_at<?`,
		now.Add(-policy.ResolvedQueuedIntentMaxAge)); err != nil {
		return DataLifecycleReport{}, fmt.Errorf("delete resolved queued intents: %w", err)
	}
	if report.RealtimeEvents, err = lifecycleDelete(ctx, tx, `
		DELETE FROM realtime_events
		WHERE id NOT LIKE 'transcript-history-rebase:%' AND (
			(root_frame_id<>'' AND created_at<? AND (
				NOT EXISTS (SELECT 1 FROM frames AS root WHERE root.id=realtime_events.root_frame_id)
				OR EXISTS (SELECT 1 FROM frames AS root WHERE root.id=realtime_events.root_frame_id
					AND root.status IN ('completed','failed','cancelled','success','replaced'))
			)) OR
			(root_frame_id='' AND created_at<?)
		)`, now.Add(-policy.TerminalRealtimeGrace), now.Add(-policy.TransportReceiptMaxAge)); err != nil {
		return DataLifecycleReport{}, fmt.Errorf("delete expired realtime events: %w", err)
	}
	deliveredCutoffMillis := now.Add(-policy.TerminalRealtimeGrace).UnixMilli()
	if report.DeliveredOutbox, err = lifecycleDelete(ctx, tx, `
		DELETE FROM workspace_outbox
		WHERE status='delivered' AND delivered_at_ms IS NOT NULL AND delivered_at_ms<?
			AND NOT EXISTS (
				SELECT 1 FROM scientific_compute_submissions AS submission
				WHERE submission.outbox_event_id=workspace_outbox.event_id
			)`, deliveredCutoffMillis); err != nil {
		return DataLifecycleReport{}, fmt.Errorf("delete delivered outbox receipts: %w", err)
	}
	deadLetterCutoffMillis := now.Add(-policy.TransportReceiptMaxAge).UnixMilli()
	if report.ExpiredDeadLetters, err = lifecycleDelete(ctx, tx, `
		DELETE FROM workspace_outbox
		WHERE status='dead_letter' AND dead_lettered_at_ms IS NOT NULL AND dead_lettered_at_ms<?
			AND NOT EXISTS (
				SELECT 1 FROM scientific_compute_submissions AS submission
				WHERE submission.outbox_event_id=workspace_outbox.event_id
			)`, deadLetterCutoffMillis); err != nil {
		return DataLifecycleReport{}, fmt.Errorf("delete expired outbox dead letters: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return DataLifecycleReport{}, fmt.Errorf("commit data lifecycle sweep: %w", err)
	}
	if len(blobPaths) > 0 {
		if err := s.RemoveArtifactBlobs(blobPaths); err != nil {
			return report, fmt.Errorf("data lifecycle metadata committed but artifact blob cleanup is incomplete: %w", err)
		}
	}
	return report, nil
}

func validateDataLifecyclePolicy(policy DataLifecyclePolicy) error {
	values := map[string]time.Duration{
		"content snapshot grace":         policy.ContentSnapshotGrace,
		"ephemeral artifact max age":     policy.EphemeralArtifactMaxAge,
		"terminal realtime grace":        policy.TerminalRealtimeGrace,
		"transport receipt max age":      policy.TransportReceiptMaxAge,
		"compute usage max age":          policy.ComputeUsageMaxAge,
		"superseded memory max age":      policy.SupersededMemoryMaxAge,
		"resolved queued intent max age": policy.ResolvedQueuedIntentMaxAge,
	}
	for name, value := range values {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	return nil
}

func lifecycleDelete(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func lifecycleEphemeralArtifactBlobs(ctx context.Context, tx *sql.Tx, cutoff time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT version.storage_path
		FROM artifacts AS artifact
		JOIN artifact_runtime_metadata AS metadata ON metadata.artifact_id=artifact.id
		JOIN artifact_versions AS version ON version.artifact_id=artifact.id
		LEFT JOIN frames AS root ON root.id=metadata.root_frame_id
		WHERE metadata.is_ephemeral=1 AND artifact.updated_at<? AND version.storage_path<>''
			AND (root.id IS NULL OR root.status IN ('completed','failed','cancelled','success','replaced'))`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list stale ephemeral artifact blobs: %w", err)
	}
	defer rows.Close()
	paths := make([]string, 0)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if path = strings.TrimSpace(path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths, rows.Err()
}
