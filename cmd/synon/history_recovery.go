package main

import (
	"context"
	"errors"
	"log"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	legacyFrameHistoryRecoveryInterval = 5 * time.Minute
	legacyFrameHistoryCycleTimeout     = 30 * time.Second
	legacyFrameHistoryPagePause        = 25 * time.Millisecond
	legacyFrameHistoryMaxPages         = 128
)

type legacyFrameHistoryReconciler interface {
	ReconcileNoStreamFrameHistories(
		context.Context,
		transcriptstore.ReconcileNoStreamFrameHistoriesInput,
	) (transcriptstore.NoStreamFrameHistoryReconciliation, error)
	ReconcileLegacyFrameHistories(
		context.Context,
		transcriptstore.ReconcileLegacyFrameHistoriesInput,
	) (transcriptstore.LegacyFrameHistoryReconciliation, error)
}

type legacyFrameHistoryCursor struct {
	ownerID   string
	sessionID string
}

func runLegacyFrameHistoryRecoveryLoop(ctx context.Context, repository legacyFrameHistoryReconciler) {
	if repository == nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	var noStreamCursor, legacyCursor legacyFrameHistoryCursor
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		noStreamCtx, cancelNoStream := context.WithTimeout(ctx, legacyFrameHistoryCycleTimeout)
		noStream, nextNoStream, noStreamComplete, noStreamErr := reconcileNoStreamFrameHistoryCycle(
			noStreamCtx, repository, noStreamCursor, legacyFrameHistoryMaxPages, legacyFrameHistoryPagePause,
		)
		cancelNoStream()
		if noStreamComplete {
			noStreamCursor = legacyFrameHistoryCursor{}
		} else if nextNoStream != (legacyFrameHistoryCursor{}) {
			noStreamCursor = nextNoStream
		}
		legacyCtx, cancelLegacy := context.WithTimeout(ctx, legacyFrameHistoryCycleTimeout)
		report, nextLegacy, legacyComplete, legacyErr := reconcileLegacyFrameHistoryCycle(
			legacyCtx, repository, legacyCursor, legacyFrameHistoryMaxPages, legacyFrameHistoryPagePause,
		)
		cancelLegacy()
		if legacyComplete {
			legacyCursor = legacyFrameHistoryCursor{}
		} else if nextLegacy != (legacyFrameHistoryCursor{}) {
			legacyCursor = nextLegacy
		}
		if reportHistoryRecoveryError(noStreamErr) || reportHistoryRecoveryError(legacyErr) {
			log.Printf("Transcript history recovery code=transcript_history_recovery_failed")
		} else if noStream.Scanned > 0 || report.Scanned > 0 {
			log.Printf("Transcript history recovery no_stream_scanned=%d payload_created=%d no_stream_blocked=%d no_stream_deferred=%d no_stream_remaining=%d legacy_scanned=%d activated=%d quarantined=%d blocked=%d deferred=%d legacy_remaining=%d truncated=%t",
				noStream.Scanned, noStream.PayloadCreated, noStream.Blocked, noStream.Deferred, noStream.Census.NoStream,
				report.Scanned, report.Activated, report.Quarantined, report.Blocked, report.Deferred,
				report.RemainingLegacy, noStream.Truncated || report.Truncated || !noStreamComplete || !legacyComplete,
			)
		}
		timer.Reset(legacyFrameHistoryRecoveryInterval)
	}
}

func reconcileNoStreamFrameHistoryCycle(
	ctx context.Context,
	repository legacyFrameHistoryReconciler,
	start legacyFrameHistoryCursor,
	maxPages int,
	pagePause time.Duration,
) (transcriptstore.NoStreamFrameHistoryReconciliation, legacyFrameHistoryCursor, bool, error) {
	var total transcriptstore.NoStreamFrameHistoryReconciliation
	input := transcriptstore.ReconcileNoStreamFrameHistoriesInput{
		Limit: 32, AfterOwnerID: start.ownerID, AfterSessionID: start.sessionID,
	}
	next := start
	if maxPages <= 0 || pagePause < 0 {
		return total, next, false, transcriptstore.ErrEventConflict
	}
	for page := 0; page < maxPages; page++ {
		current, err := repository.ReconcileNoStreamFrameHistories(ctx, input)
		if err != nil {
			return total, next, false, err
		}
		total.Scanned += current.Scanned
		total.PayloadCreated += current.PayloadCreated
		total.Blocked += current.Blocked
		total.Deferred += current.Deferred
		total.Census = current.Census
		if !current.Truncated {
			return total, legacyFrameHistoryCursor{}, true, nil
		}
		if current.NextOwnerID == "" || current.NextSessionID == "" ||
			(current.NextOwnerID == input.AfterOwnerID && current.NextSessionID == input.AfterSessionID) {
			return total, next, false, transcriptstore.ErrEventConflict
		}
		input.AfterOwnerID, input.AfterSessionID = current.NextOwnerID, current.NextSessionID
		next = legacyFrameHistoryCursor{ownerID: current.NextOwnerID, sessionID: current.NextSessionID}
		if err := waitLegacyFrameHistoryPage(ctx, pagePause); err != nil {
			return total, next, false, err
		}
	}
	total.Truncated = true
	return total, next, false, nil
}

func reportHistoryRecoveryError(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func reconcileLegacyFrameHistoryCycle(
	ctx context.Context,
	repository legacyFrameHistoryReconciler,
	start legacyFrameHistoryCursor,
	maxPages int,
	pagePause time.Duration,
) (transcriptstore.LegacyFrameHistoryReconciliation, legacyFrameHistoryCursor, bool, error) {
	var total transcriptstore.LegacyFrameHistoryReconciliation
	input := transcriptstore.DefaultLegacyFrameHistoryReconciliationInput()
	input.AfterOwnerID, input.AfterSessionID = start.ownerID, start.sessionID
	next := start
	if maxPages <= 0 || pagePause < 0 {
		return total, next, false, transcriptstore.ErrEventConflict
	}
	for page := 0; page < maxPages; page++ {
		current, err := repository.ReconcileLegacyFrameHistories(ctx, input)
		if err != nil {
			return total, next, false, err
		}
		total.Scanned += current.Scanned
		total.Activated += current.Activated
		total.Quarantined += current.Quarantined
		total.Blocked += current.Blocked
		total.Deferred += current.Deferred
		total.RemainingLegacy = current.RemainingLegacy
		if !current.Truncated {
			return total, legacyFrameHistoryCursor{}, true, nil
		}
		if current.NextOwnerID == "" || current.NextSessionID == "" ||
			(current.NextOwnerID == input.AfterOwnerID && current.NextSessionID == input.AfterSessionID) {
			return total, next, false, transcriptstore.ErrEventConflict
		}
		input.AfterOwnerID, input.AfterSessionID = current.NextOwnerID, current.NextSessionID
		next = legacyFrameHistoryCursor{ownerID: current.NextOwnerID, sessionID: current.NextSessionID}
		if err := waitLegacyFrameHistoryPage(ctx, pagePause); err != nil {
			return total, next, false, err
		}
	}
	total.Truncated = true
	return total, next, false, nil
}

func waitLegacyFrameHistoryPage(ctx context.Context, pause time.Duration) error {
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}
