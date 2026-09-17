package transcript

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type streamDeleteTransaction interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func DeleteProjectStreamsTx(
	ctx context.Context,
	tx streamDeleteTransaction,
	ownerID, projectID string,
) (int64, error) {
	return deleteScopedStreamsTx(ctx, tx, ownerID, projectID, "", "")
}

func DeleteRootStreamsTx(
	ctx context.Context,
	tx streamDeleteTransaction,
	ownerID, projectID, rootFrameID string,
) (int64, error) {
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return 0, errors.New("transcript root frame id is required")
	}
	return deleteScopedStreamsTx(ctx, tx, ownerID, projectID, rootFrameID, "")
}

func DeleteFrameStreamsTx(
	ctx context.Context,
	tx streamDeleteTransaction,
	ownerID, projectID, frameID string,
) (int64, error) {
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return 0, errors.New("transcript frame id is required")
	}
	return deleteScopedStreamsTx(ctx, tx, ownerID, projectID, "", frameID)
}

func deleteScopedStreamsTx(
	ctx context.Context,
	tx streamDeleteTransaction,
	ownerID, projectID, rootFrameID, frameID string,
) (int64, error) {
	if tx == nil {
		return 0, errors.New("transcript stream delete transaction is required")
	}
	ownerID, projectID = strings.TrimSpace(ownerID), strings.TrimSpace(projectID)
	if ownerID == "" || projectID == "" {
		return 0, errors.New("transcript stream owner and project are required")
	}
	predicate := "owner_id = ? AND project_id = ?"
	args := []any{ownerID, projectID}
	if frameID != "" {
		predicate += " AND frame_id = ?"
		args = append(args, frameID)
	} else if rootFrameID != "" {
		predicate += " AND root_frame_id = ?"
		args = append(args, rootFrameID)
	}
	scope := "SELECT stream_uid FROM transcript_streams WHERE " + predicate
	for _, cleanup := range []struct {
		label string
		query string
	}{
		{
			label: "frame authority",
			query: "DELETE FROM transcript_frame_authority WHERE active_stream_uid IN (" + scope + ")",
		},
		{
			label: "history activation receipt",
			query: "DELETE FROM transcript_history_activation_receipts WHERE source_stream_uid IN (" + scope +
				") OR target_stream_uid IN (" + scope + ")",
		},
		{
			label: "ordinary history cursor map",
			query: "DELETE FROM transcript_history_ordinary_cursor_map WHERE ordinary_cutover_id IN (" +
				"SELECT ordinary_cutover_id FROM transcript_history_ordinary_cutover_runs " +
				"WHERE source_stream_uid IN (" + scope + ") OR target_stream_uid IN (" + scope + "))",
		},
		{
			label: "ordinary history cutover",
			query: "DELETE FROM transcript_history_ordinary_cutover_runs WHERE source_stream_uid IN (" + scope +
				") OR target_stream_uid IN (" + scope + ")",
		},
		{
			label: "typed history bootstrap receipt",
			query: "DELETE FROM transcript_typed_history_bootstrap_receipts WHERE stream_uid IN (" + scope + ")",
		},
		{
			label: "payload genesis receipt",
			query: "DELETE FROM transcript_payload_genesis_receipts WHERE stream_uid IN (" + scope + ")",
		},
		{
			label: "history cutover",
			query: "DELETE FROM transcript_history_cutover_runs WHERE stream_uid IN (" + scope + ")",
		},
		{
			label: "history backfill",
			query: "DELETE FROM transcript_history_backfill_runs WHERE stream_uid IN (" + scope + ")",
		},
	} {
		cleanupArgs := append([]any(nil), args...)
		if cleanup.label == "history activation receipt" || cleanup.label == "ordinary history cutover" {
			cleanupArgs = append(cleanupArgs, args...)
		} else if cleanup.label == "ordinary history cursor map" {
			cleanupArgs = append(cleanupArgs, args...)
		}
		if _, err := tx.ExecContext(ctx, cleanup.query, cleanupArgs...); err != nil {
			return 0, fmt.Errorf("delete transcript %s: %w", cleanup.label, err)
		}
	}
	for _, table := range []string{"transcript_artifact_refs", "transcript_artifact_commits"} {
		query := "DELETE FROM " + table + " WHERE stream_uid IN (SELECT stream_uid FROM transcript_streams WHERE " + predicate + ")"
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return 0, fmt.Errorf("delete transcript artifact association: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM transcript_streams WHERE "+predicate, args...)
	if err != nil {
		return 0, fmt.Errorf("delete transcript streams: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted transcript streams: %w", err)
	}
	return deleted, nil
}
