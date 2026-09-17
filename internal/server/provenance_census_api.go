package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

const provenanceCensusCacheTTL = 60 * time.Second

type provenanceCensusCacheEntry struct {
	storedAt time.Time
	value    workspace.ProvenanceCensusResult
}

type provenanceCensusInFlight struct {
	done  chan struct{}
	value workspace.ProvenanceCensusResult
	err   error
}

type provenanceCensusCache struct {
	mu       sync.Mutex
	entries  map[string]provenanceCensusCacheEntry
	inFlight map[string]*provenanceCensusInFlight
}

func (s *Server) handleProvenanceCensus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "workspace storage is not configured"})
		return
	}
	windowDays := parseProvenanceWindowDays(r.URL.Query().Get("window_days"))
	splitAt, err := parseProvenanceSplitAt(r.URL.Query().Get("split_at"))
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "split_at must be an ISO date"})
		return
	}
	key := strconv.FormatFloat(windowDays, 'g', -1, 64) + "|"
	if splitAt != nil {
		key += splitAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := s.provenanceCensusCache.get(r.Context(), key, func(ctx context.Context) (workspace.ProvenanceCensusResult, error) {
		return s.workspaceStore.GetProvenanceCensus(ctx, workspace.ProvenanceCensusOptions{
			DetectionWindowDays: windowDays,
			SplitAt:             splitAt,
		})
	})
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "unable to compute provenance census"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, result)
}

func parseProvenanceWindowDays(raw string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 7
	}
	return min(value, 365)
}

func parseProvenanceSplitAt(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	} {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			parsed = parsed.UTC()
			return &parsed, nil
		}
	}
	return nil, errors.New("invalid ISO date")
}

func (c *provenanceCensusCache) get(
	ctx context.Context,
	key string,
	load func(context.Context) (workspace.ProvenanceCensusResult, error),
) (workspace.ProvenanceCensusResult, error) {
	now := time.Now()
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && now.Sub(entry.storedAt) < provenanceCensusCacheTTL {
		c.mu.Unlock()
		return entry.value, nil
	}
	if call, ok := c.inFlight[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return workspace.ProvenanceCensusResult{}, context.Cause(ctx)
		case <-call.done:
			return call.value, call.err
		}
	}
	if c.entries == nil {
		c.entries = make(map[string]provenanceCensusCacheEntry)
	}
	if c.inFlight == nil {
		c.inFlight = make(map[string]*provenanceCensusInFlight)
	}
	call := &provenanceCensusInFlight{done: make(chan struct{})}
	c.inFlight[key] = call
	c.mu.Unlock()

	call.value, call.err = load(ctx)
	c.mu.Lock()
	if call.err == nil {
		c.entries[key] = provenanceCensusCacheEntry{storedAt: time.Now(), value: call.value}
	}
	delete(c.inFlight, key)
	close(call.done)
	c.mu.Unlock()
	return call.value, call.err
}
