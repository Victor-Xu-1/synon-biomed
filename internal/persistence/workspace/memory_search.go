package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"synon-go/internal/memoryconfig"
	"synon-go/internal/memorypolicy"
)

const defaultMemorySearchLimit = memorypolicy.SearchToolMax

type MemorySearchOptions struct {
	UserID         string
	ProjectID      string
	FrameID        string
	Query          string
	Limit          int
	RecordAccess   bool
	Now            time.Time
	Config         *memoryconfig.Config
	RRFThreshold   *float64
	StaleRankDecay *float64
}

// SearchMemories searches the authenticated user's full active pool, matching
// the workspace explicit search_memory contract. Frame scratchpads remain
// isolated to the trusted root frame; project relevance affects ranking, not
// visibility. Automatic recall applies the stricter cross-project quota.
func (s *Store) SearchMemories(ctx context.Context, options MemorySearchOptions) ([]Memory, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	options.UserID = strings.TrimSpace(options.UserID)
	options.ProjectID = strings.TrimSpace(options.ProjectID)
	options.FrameID = strings.TrimSpace(options.FrameID)
	options.Query = boundMemoryRecallQuery(options.Query)
	config := memoryconfig.Default()
	limit := options.Limit
	if options.Config != nil {
		config = *options.Config
		if limit == 0 {
			limit = config.SearchToolMax
		}
	} else if limit == 0 {
		limit = defaultMemorySearchLimit
	}
	if options.UserID == "" {
		return nil, errors.New("memory user id is required")
	}
	enabled, err := s.MemoryEnabledWithDefault(ctx, options.UserID, config.Enabled)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return []Memory{}, nil
	}
	queryTokens := memorySearchTokens(options.Query)
	if len(queryTokens) == 0 {
		return []Memory{}, nil
	}
	if options.Now.IsZero() {
		options.Now = s.now().UTC()
	} else {
		options.Now = options.Now.UTC()
	}

	memories, err := s.memorySearchCandidates(ctx, options.UserID, options.FrameID)
	if err != nil {
		return nil, fmt.Errorf("query memory search candidates: %w", err)
	}
	inProject, err := s.memoryRecallProjectIDs(ctx, options.UserID, options.ProjectID)
	if err != nil {
		return nil, err
	}
	staleRankDecay := config.StaleRankDecay
	if options.StaleRankDecay != nil {
		staleRankDecay = *options.StaleRankDecay
	}
	staleness, staleRankDecay, err := s.memoryRecallRankingPolicy(ctx, memories, &staleRankDecay)
	if err != nil {
		return nil, err
	}
	scores := scoreMemoryRecallIndexWithPolicy(buildMemoryRecallIndex(memories), queryTokens, inProject, staleness, config.RecallProjectBoost, staleRankDecay)
	threshold := config.SearchToolRRFThreshold
	if options.RRFThreshold != nil {
		threshold = *options.RRFThreshold
	}
	filtered := scores[:0]
	for _, score := range scores {
		if score.rrf >= threshold {
			filtered = append(filtered, score)
		}
	}
	scores = filtered[:memoryRecallSliceEnd(len(filtered), limit)]
	results := make([]Memory, 0, len(scores))
	for _, score := range scores {
		memory := score.memory
		memory.RecallScore = score.rrf
		results = append(results, memory)
	}
	if options.RecordAccess && len(results) > 0 {
		if err := s.recordMemoryRecall(ctx, results, options.Now); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (s *Store) NearestMemoryAmong(ctx context.Context, options MemorySearchOptions, allowedIDs map[string]struct{}) (*Memory, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	options.UserID = strings.TrimSpace(options.UserID)
	options.ProjectID = strings.TrimSpace(options.ProjectID)
	options.FrameID = strings.TrimSpace(options.FrameID)
	options.Query = boundMemoryRecallQuery(options.Query)
	if options.UserID == "" {
		return nil, errors.New("memory user id is required")
	}
	config := memoryconfig.Default()
	if options.Config != nil {
		config = *options.Config
	}
	enabled, err := s.MemoryEnabledWithDefault(ctx, options.UserID, config.Enabled)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	queryTokens := memorySearchTokens(options.Query)
	if len(queryTokens) == 0 || len(allowedIDs) == 0 {
		return nil, nil
	}
	memories, err := s.memorySearchCandidates(ctx, options.UserID, options.FrameID)
	if err != nil {
		return nil, err
	}
	scoped := memories[:0]
	for _, memory := range memories {
		if memoryVisibleInProject(memory, options.ProjectID) {
			scoped = append(scoped, memory)
		}
	}
	memories = scoped
	// The reference nearestAmong contract runs against the lexical index directly.
	// Project and stale boosts belong to recall/search ranking, not duplicate
	// detection, because they would change the warning threshold itself.
	for _, score := range scoreMemoryDocuments(memories, queryTokens, nil) {
		if _, allowed := allowedIDs[score.memory.ID]; !allowed {
			continue
		}
		memory := score.memory
		memory.RecallScore = score.rrf
		return &memory, nil
	}
	return nil, nil
}

func (s *Store) memorySearchCandidates(ctx context.Context, userID, frameID string) ([]Memory, error) {
	query := memorySelect + ` WHERE m.user_id = ? AND m.superseded_by IS NULL`
	args := []any{userID}
	if frameID == "" {
		query += ` AND m.subject_frame_id IS NULL`
	} else {
		query += ` AND (m.subject_frame_id IS NULL OR m.subject_frame_id = ?)`
		args = append(args, frameID)
	}
	query += ` ORDER BY m.created_at DESC, m.id`
	memories, err := scanMemoryRows(ctx, s.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query memory search candidates: %w", err)
	}
	return memories, nil
}

func (s *Store) MarkMemoryRowsSurfaced(ctx context.Context, userID string, memories []Memory, at time.Time) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("memory user id is required")
	}
	owned := make([]Memory, 0, len(memories))
	for _, memory := range memories {
		if memory.UserID == userID {
			owned = append(owned, memory)
		}
	}
	if len(owned) == 0 {
		return nil
	}
	if at.IsZero() {
		at = s.now().UTC()
	}
	return s.recordMemoryRecall(ctx, owned, at.UTC())
}
