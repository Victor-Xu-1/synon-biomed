package server

import (
	"synon-go/internal/toolprogress"
	"time"
)

type scientificRuntimeProgress struct {
	Phase          string
	Percent        *float64
	BytesCompleted *int64
	BytesTotal     *int64
	UpdatedAt      time.Time
}

// Installer output is already interpreted by the shared progress observer.
// Keep only observed numeric facts; never publish raw installer messages.
func (s *Server) reportScientificRuntimeProgress(id string, attempt int, update toolprogress.Update) {
	update = toolprogress.Normalize(update)
	progress := &scientificRuntimeProgress{Phase: update.Phase, UpdatedAt: time.Now().UTC()}
	if update.PhasePercent != nil {
		value := *update.PhasePercent
		progress.Percent = &value
	}
	if update.BytesCompleted != nil {
		value := *update.BytesCompleted
		progress.BytesCompleted = &value
	}
	if update.BytesTotal != nil {
		value := *update.BytesTotal
		progress.BytesTotal = &value
	}
	s.scientificRuntimeWarmupMu.Lock()
	defer s.scientificRuntimeWarmupMu.Unlock()
	status := s.scientificRuntimeWarmups[id]
	if status.State != "preparing" || status.Attempt != attempt {
		return
	}
	status.Progress = progress
	s.scientificRuntimeWarmups[id] = status
}

func appendScientificRuntimeProgress(value map[string]any, progress *scientificRuntimeProgress) {
	if progress == nil {
		return
	}
	if progress.Phase != "" {
		value["phase"] = progress.Phase
	}
	if progress.Percent != nil {
		value["phase_percent"] = *progress.Percent
	}
	if progress.BytesCompleted != nil {
		value["bytes_completed"] = *progress.BytesCompleted
	}
	if progress.BytesTotal != nil {
		value["bytes_total"] = *progress.BytesTotal
	}
	value["updated_at"] = progress.UpdatedAt.Format(time.RFC3339)
}
