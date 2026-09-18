package server

import (
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"synon-go/internal/runtimecontrol"
)

// A download can simultaneously occupy staging, artifact and workspace
// copies. Account conservatively on each destination (including when they
// share a filesystem), rather than impose an unrelated fixed file-size cap.
type agentDownloadDiskGuard struct {
	paths     []string
	available func(string) *uint64
}

func newAgentDownloadDiskGuard(workspaceDir, fileRoot string) agentDownloadDiskGuard {
	paths := []string{filepath.Clean(workspaceDir)}
	if strings.TrimSpace(fileRoot) != "" && filepath.Clean(fileRoot) != paths[0] {
		paths = append(paths, filepath.Clean(fileRoot))
	}
	return agentDownloadDiskGuard{paths: paths, available: runtimecontrol.AvailableBytes}
}

func (g agentDownloadDiskGuard) check(staged, next int64) error {
	if staged < 0 || next < 0 || next > math.MaxInt64-staged {
		return errAgentPublicScientificFileDiskSpace
	}
	// staged bytes already occupy disk. Reserve the two publication copies
	// and the next staging increment, without overflow on hostile lengths.
	total := uint64(staged) + uint64(next)
	if total > (math.MaxUint64-agentPublicScientificDiskReserve-uint64(next))/2 {
		return errAgentPublicScientificFileDiskSpace
	}
	required := 2*total + uint64(next) + agentPublicScientificDiskReserve
	for _, path := range g.paths {
		available := g.available(path)
		if available == nil || *available < required {
			return errAgentPublicScientificFileDiskSpace
		}
	}
	return nil
}

type agentDownloadDiskWriter struct {
	writer  io.Writer
	guard   agentDownloadDiskGuard
	written int64
	checked int64
	at      time.Time
}

func (w *agentDownloadDiskWriter) Write(p []byte) (int, error) {
	const window = int64(8 << 20)
	if w.at.IsZero() || w.written-w.checked+int64(len(p)) >= window || time.Since(w.at) >= time.Second {
		if err := w.guard.check(w.written, max(window, int64(len(p)))); err != nil {
			return 0, err
		}
		w.checked, w.at = w.written, time.Now()
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	return n, err
}
