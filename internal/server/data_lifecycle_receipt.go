package server

import (
	"context"
	"fmt"
	"time"
)

// runDataLifecycleSweepWithReceipt executes one complete lifecycle pass and
// returns a bounded success receipt even when there was nothing to delete. The
// daily operational log supplies the receipt timestamp and seven-day retention;
// the payload contains aggregate counts only, never paths, IDs, or user data.
func (s *Server) runDataLifecycleSweepWithReceipt(
	ctx context.Context,
	now time.Time,
) (string, error) {
	report, err := s.runDataLifecyclePass(ctx, now)
	if err != nil {
		return "", err
	}
	return dataLifecycleSuccessReceipt(report), nil
}

func dataLifecycleSuccessReceipt(report dataLifecyclePassReport) string {
	workspaceReport := report.Workspace
	return fmt.Sprintf(
		"data lifecycle sweep completed status=ok changed=%t "+
			"content_snapshots=%d ephemeral_artifacts=%d compute_usage=%d "+
			"superseded_memories=%d resolved_queued_intents=%d realtime_events=%d "+
			"delivered_outbox=%d expired_dead_letters=%d artifact_blobs_scheduled=%d "+
			"runtime_audits=%d runtime_usage=%d",
		report.Changed(),
		workspaceReport.ContentSnapshots,
		workspaceReport.EphemeralArtifacts,
		workspaceReport.ComputeUsage,
		workspaceReport.SupersededMemories,
		workspaceReport.ResolvedQueuedIntents,
		workspaceReport.RealtimeEvents,
		workspaceReport.DeliveredOutbox,
		workspaceReport.ExpiredDeadLetters,
		workspaceReport.ArtifactBlobsScheduled,
		report.Audits,
		report.Usage,
	)
}
