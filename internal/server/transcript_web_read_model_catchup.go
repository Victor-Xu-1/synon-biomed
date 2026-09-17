package server

import (
	"context"
	"errors"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// A request-scoped catch-up has priority over the global repair queue and must
// be long enough to rebuild a real, tool-heavy scientific conversation once.
// Concurrent readers share the same per-stream flight, so this bound does not
// multiply projection work.
const transcriptWebRequestCatchupTimeout = 15 * time.Second

type transcriptWebCatchupKey struct {
	ownerID   string
	streamUID string
	branchID  string
}

type transcriptWebCatchupFlight struct {
	done chan struct{}
	err  error
}

func transcriptWebFenceReady(fence transcriptstore.TranscriptWebProjectionFence) bool {
	return fence.StateFound && fence.StateStatus == "ready" &&
		fence.StateProjectorVersion == transcriptstore.TranscriptWebProjectorVersion &&
		fence.StateBranchGeneration == fence.BranchGeneration &&
		fence.StateThroughPublicationSequence == fence.ThroughPublicationSequence &&
		fence.StateSourceRevision == fence.SourceRevision
}

func transcriptWebFenceCanCatchUp(fence transcriptstore.TranscriptWebProjectionFence) bool {
	// A newly-created conversation has no projection row yet. Treat that
	// bounded, exact-stream state as catch-up eligible so its first history read
	// and first realtime claim do not wait behind the global repair queue.
	return !fence.StateFound || fence.StateStatus == "ready" && !transcriptWebFenceReady(fence)
}

// catchUpTranscriptWebReadModel collapses concurrent reads for one stale
// conversation into one bounded rebuild. Different stream keys never share a
// flight, preserving task isolation and parallel progress.
func (s *Server) catchUpTranscriptWebReadModel(
	requestCtx context.Context,
	ownerID, streamUID, branchID string,
) error {
	key := transcriptWebCatchupKey{ownerID: ownerID, streamUID: streamUID, branchID: branchID}
	s.transcriptWebCatchupMu.Lock()
	if s.transcriptWebCatchups == nil {
		s.transcriptWebCatchups = make(map[transcriptWebCatchupKey]*transcriptWebCatchupFlight)
	}
	if flight := s.transcriptWebCatchups[key]; flight != nil {
		s.transcriptWebCatchupMu.Unlock()
		select {
		case <-flight.done:
			return flight.err
		case <-requestCtx.Done():
			return requestCtx.Err()
		}
	}
	flight := &transcriptWebCatchupFlight{done: make(chan struct{})}
	s.transcriptWebCatchups[key] = flight
	s.transcriptWebCatchupMu.Unlock()

	catchupCtx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), transcriptWebRequestCatchupTimeout)
	flight.err = s.rebuildOneStaleTranscriptWebReadModel(catchupCtx, key)
	cancel()

	s.transcriptWebCatchupMu.Lock()
	delete(s.transcriptWebCatchups, key)
	close(flight.done)
	s.transcriptWebCatchupMu.Unlock()
	return flight.err
}

func (s *Server) rebuildOneStaleTranscriptWebReadModel(
	ctx context.Context,
	key transcriptWebCatchupKey,
) error {
	work, found, err := s.transcriptWebReadModel.GetTranscriptWebProjectionWork(
		ctx, key.ownerID, key.streamUID, key.branchID,
	)
	if err != nil || !found {
		return err
	}
	eligible := work.Status == transcriptstore.TranscriptWebProjectionWorkMissing ||
		work.ProjectionStatus == "ready" &&
			(work.Status == transcriptstore.TranscriptWebProjectionWorkDirty ||
				work.Status == transcriptstore.TranscriptWebProjectionWorkStale)
	if !eligible {
		return nil
	}
	if err := s.rebuildTranscriptWebReadModel(ctx, work); err != nil {
		if errors.Is(err, transcriptstore.ErrBranchStateStale) ||
			errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
			fence, fenceErr := s.transcriptWebReadModel.GetTranscriptWebProjectionFence(
				ctx, key.ownerID, key.streamUID, key.branchID,
			)
			if fenceErr == nil && transcriptWebFenceReady(fence) {
				return nil
			}
		}
		return err
	}
	return nil
}
