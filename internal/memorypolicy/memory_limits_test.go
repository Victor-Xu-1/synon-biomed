package memorypolicy

import "testing"

func TestWorkspaceHardLimits(t *testing.T) {
	integers := map[string]struct {
		got  int
		want int
	}{
		"extract_max_per_run":             {ExtractMaxPerRun, 5},
		"listing_max_entities":            {ListingMaxEntities, 40},
		"profile_max_rows":                {ProfileMaxRows, 40},
		"frame_max_rows":                  {FrameMaxRows, 20},
		"recall_inject_max":               {RecallInjectMax, 6},
		"recall_xproj_rank_max":           {RecallCrossProjectRankMax, 3},
		"recall_xproj_max":                {RecallCrossProjectMax, 2},
		"recall_profile_max":              {RecallProfileMax, 2},
		"recall_reserve_max":              {RecallReserveMax, 2},
		"recall_spawn_query_max_tokens":   {RecallSpawnQueryMaxTokens, 25},
		"search_tool_max":                 {SearchToolMax, 20},
		"read_tool_max":                   {ReadToolMax, 100},
		"stale_age_days":                  {StaleAgeDays, 14},
		"search_query_max":                {SearchQueryMaxUTF16Units, 16384},
		"recall_min_words":                {RecallMinWords, 3},
		"memory_ops_cap":                  {OperationsPerKindMax, 20},
		"memory_text_cap":                 {TextMaxUTF16Units, 1000},
		"category_cap":                    {CategoryMax, 10},
		"category_name_max":               {CategoryNameMax, 64},
		"category_guidance_max":           {CategoryGuidanceMax, 280},
		"pi_classifier_concurrency":       {PIClassifierConcurrency, 8},
		"pi_classifier_max_tokens":        {PIClassifierMaxTokens, 512},
		"pi_classifier_busy_retries":      {PIClassifierBusyRetries, 2},
		"static_decode_max_passes":        {StaticDecodeMaxPasses, 1024},
		"extraction_transcript_max_chars": {ExtractionTranscriptMaxUTF16Units, 8000},
		"extraction_max_tokens":           {ExtractionMaxTokens, 2048},
		"extraction_forked_headroom":      {ExtractionForkedTokenHeadroom, 8192},
		"extraction_forked_max_tokens":    {ExtractionForkedMaxTokens, 10240},
		"generated_id_hex_length":         {GeneratedIDHexLength, 12},
		"index_body_preview_max":          {IndexBodyPreviewMax, 120},
		"related_prior_rows_max":          {RelatedPriorRowsMax, 3},
		"remove_retry_attempts":           {RemoveRetryAttempts, 2},
		"search_extra_entities_max":       {SearchExtraEntitiesMax, 3},
		"memory_id_max_length":            {MemoryIDMaxLength, 36},
		"category_id_max_length":          {CategoryIDMaxLength, 36},
		"project_id_max_length":           {ProjectIDMaxLength, 255},
		"user_id_max_length":              {UserIDMaxLength, 255},
		"artifact_id_max_length":          {ArtifactIDMaxLength, 36},
		"version_id_max_length":           {VersionIDMaxLength, 36},
		"frame_id_max_length":             {FrameIDMaxLength, 36},
	}
	for name, check := range integers {
		if check.got != check.want {
			t.Errorf("%s = %d, want %d", name, check.got, check.want)
		}
	}

	floats := map[string]struct {
		got  float64
		want float64
	}{
		"recall_rrf_threshold":          {RecallRRFThreshold, 0.029},
		"recall_project_boost":          {RecallProjectBoost, 1.5},
		"recall_spawn_query_df_ratio":   {RecallSpawnQueryDFMaxRatio, 0.16},
		"search_tool_rrf_threshold":     {SearchToolRRFThreshold, 0},
		"dedup_warn_threshold":          {DedupWarnThreshold, 0.025},
		"stale_rank_decay":              {StaleRankDecay, 1},
		"bm25_k1":                       {BM25K1, 1.2},
		"bm25_b":                        {BM25B, 0.75},
		"reciprocal_rank_fusion_offset": {RRFConstant, 60},
	}
	for name, check := range floats {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", name, check.got, check.want)
		}
	}
}

func TestUTF16LimitsMatchJavaScriptStringLength(t *testing.T) {
	if got := UTF16Length("A🧪B"); got != 4 {
		t.Fatalf("UTF16Length = %d, want 4", got)
	}
	if got := PrefixUTF16("A🧪B", 3); got != "A🧪" {
		t.Fatalf("PrefixUTF16 = %q", got)
	}
	if got := TruncateWithEllipsis("A🧪B", 3); got != "A…" {
		t.Fatalf("TruncateWithEllipsis = %q", got)
	}
}
