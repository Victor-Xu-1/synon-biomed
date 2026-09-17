// Package memoryconfig owns the imported reference memory configuration contract.
package memoryconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"synon-go/internal/memorypolicy"
)

type Config struct {
	Enabled                    bool    `json:"enabled"`
	PIClassifierEnabled        bool    `json:"pi_classifier_enabled"`
	ExtractEnabled             bool    `json:"extract_enabled"`
	ExtractMode                string  `json:"extract_mode"`
	ExtractEveryN              int     `json:"extract_every_n"`
	ExtractMaxPerRun           int     `json:"extract_max_per_run"`
	ExtractDelta               bool    `json:"extract_delta"`
	ContextInSystemPrompt      bool    `json:"context_in_system_prompt"`
	ListingMaxEntities         int     `json:"listing_max_entities"`
	ProfileMaxRows             int     `json:"profile_max_rows"`
	FrameMaxRows               int     `json:"frame_max_rows"`
	RecallRRFThreshold         float64 `json:"recall_rrf_threshold"`
	RecallInjectMax            int     `json:"recall_inject_max"`
	RecallProjectBoost         float64 `json:"recall_project_boost"`
	RecallCrossProjectRankMax  int     `json:"recall_xproj_rank_max"`
	RecallCrossProjectMax      int     `json:"recall_xproj_max"`
	RecallProfileMax           int     `json:"recall_profile_max"`
	RecallReserveMax           int     `json:"recall_reserve_max"`
	RecallSpawnQueryMaxTokens  int     `json:"recall_spawn_query_max_tokens"`
	RecallSpawnQueryDFMaxRatio float64 `json:"recall_spawn_query_df_max_ratio"`
	SearchToolMax              int     `json:"search_tool_max"`
	SearchToolRRFThreshold     float64 `json:"search_tool_rrf_threshold"`
	ReadToolMax                int     `json:"read_tool_max"`
	DedupWarnThreshold         float64 `json:"dedup_warn_threshold"`
	StaleAgeDays               int     `json:"stale_age_days"`
	StaleRankDecay             float64 `json:"stale_rank_decay"`
}

func Default() Config {
	return Config{
		Enabled:                    memorypolicy.EnabledDefault,
		PIClassifierEnabled:        memorypolicy.PIClassifierEnabledDefault,
		ExtractEnabled:             memorypolicy.ExtractEnabledDefault,
		ExtractMode:                memorypolicy.ExtractModeDefault,
		ExtractEveryN:              memorypolicy.ExtractEveryNDefault,
		ExtractMaxPerRun:           memorypolicy.ExtractMaxPerRun,
		ExtractDelta:               memorypolicy.ExtractDeltaDefault,
		ContextInSystemPrompt:      memorypolicy.ContextInSystemPromptDefault,
		ListingMaxEntities:         memorypolicy.ListingMaxEntities,
		ProfileMaxRows:             memorypolicy.ProfileMaxRows,
		FrameMaxRows:               memorypolicy.FrameMaxRows,
		RecallRRFThreshold:         memorypolicy.RecallRRFThreshold,
		RecallInjectMax:            memorypolicy.RecallInjectMax,
		RecallProjectBoost:         memorypolicy.RecallProjectBoost,
		RecallCrossProjectRankMax:  memorypolicy.RecallCrossProjectRankMax,
		RecallCrossProjectMax:      memorypolicy.RecallCrossProjectMax,
		RecallProfileMax:           memorypolicy.RecallProfileMax,
		RecallReserveMax:           memorypolicy.RecallReserveMax,
		RecallSpawnQueryMaxTokens:  memorypolicy.RecallSpawnQueryMaxTokens,
		RecallSpawnQueryDFMaxRatio: memorypolicy.RecallSpawnQueryDFMaxRatio,
		SearchToolMax:              memorypolicy.SearchToolMax,
		SearchToolRRFThreshold:     memorypolicy.SearchToolRRFThreshold,
		ReadToolMax:                memorypolicy.ReadToolMax,
		DedupWarnThreshold:         memorypolicy.DedupWarnThreshold,
		StaleAgeDays:               memorypolicy.StaleAgeDays,
		StaleRankDecay:             memorypolicy.StaleRankDecay,
	}
}

func (config Config) Validate() error {
	if config.ExtractMode != "forked" && config.ExtractMode != "haiku" {
		return fmt.Errorf("memory.extract_mode must be forked or haiku, got %q", config.ExtractMode)
	}
	for name, value := range map[string]int{
		"recall_xproj_rank_max":         config.RecallCrossProjectRankMax,
		"recall_xproj_max":              config.RecallCrossProjectMax,
		"recall_profile_max":            config.RecallProfileMax,
		"recall_reserve_max":            config.RecallReserveMax,
		"recall_spawn_query_max_tokens": config.RecallSpawnQueryMaxTokens,
	} {
		if value < 0 {
			return fmt.Errorf("memory.%s must be non-negative", name)
		}
	}
	if config.RecallSpawnQueryDFMaxRatio < 0 || config.RecallSpawnQueryDFMaxRatio > 1 {
		return errors.New("memory.recall_spawn_query_df_max_ratio must be between 0 and 1")
	}
	for name, value := range map[string]float64{
		"recall_rrf_threshold":            config.RecallRRFThreshold,
		"recall_project_boost":            config.RecallProjectBoost,
		"recall_spawn_query_df_max_ratio": config.RecallSpawnQueryDFMaxRatio,
		"search_tool_rrf_threshold":       config.SearchToolRRFThreshold,
		"dedup_warn_threshold":            config.DedupWarnThreshold,
		"stale_rank_decay":                config.StaleRankDecay,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("memory.%s must be finite", name)
		}
	}
	return nil
}

func (config *Config) UnmarshalJSON(data []byte) error {
	if config == nil {
		return errors.New("memory config target is nil")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode memory config object: %w", err)
	}
	if fields == nil {
		return errors.New("memory config must be an object")
	}
	for name, raw := range fields {
		if _, known := memoryConfigFieldNames[name]; known && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("memory.%s must not be null", name)
		}
	}
	next := Default()
	type plainConfig Config
	if err := json.Unmarshal(data, (*plainConfig)(&next)); err != nil {
		return fmt.Errorf("decode memory config fields: %w", err)
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*config = next
	return nil
}

var memoryConfigFieldNames = map[string]struct{}{
	"enabled": {}, "pi_classifier_enabled": {}, "extract_enabled": {}, "extract_mode": {},
	"extract_every_n": {}, "extract_max_per_run": {}, "extract_delta": {}, "context_in_system_prompt": {},
	"listing_max_entities": {}, "profile_max_rows": {}, "frame_max_rows": {}, "recall_rrf_threshold": {},
	"recall_inject_max": {}, "recall_project_boost": {}, "recall_xproj_rank_max": {}, "recall_xproj_max": {},
	"recall_profile_max": {}, "recall_reserve_max": {}, "recall_spawn_query_max_tokens": {},
	"recall_spawn_query_df_max_ratio": {}, "search_tool_max": {}, "search_tool_rrf_threshold": {},
	"read_tool_max": {}, "dedup_warn_threshold": {}, "stale_age_days": {}, "stale_rank_decay": {},
}
