package memoryconfig

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestWorkspaceMemoryConfigDefaultsAndJSONOverlay(t *testing.T) {
	want := Default()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Config
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, want) {
		t.Fatalf("round-trip config = %#v, want %#v", roundTrip, want)
	}

	var overlaid Config
	if err := json.Unmarshal([]byte(`{"enabled":true,"pi_classifier_model":"must-be-stripped","extract_delta":false,"stale_rank_decay":0,"recall_xproj_max":0,"unknown":"stripped"}`), &overlaid); err != nil {
		t.Fatal(err)
	}
	if !overlaid.Enabled || overlaid.ExtractDelta || overlaid.StaleRankDecay != 0 || overlaid.RecallCrossProjectMax != 0 {
		t.Fatalf("overlaid config = %#v", overlaid)
	}
	if overlaid.ExtractMode != "forked" || overlaid.SearchToolMax != 20 {
		t.Fatalf("missing fields did not retain defaults: %#v", overlaid)
	}
}

func TestWorkspaceMemoryConfigRejectsOnlySourceConstrainedValues(t *testing.T) {
	for name, payload := range map[string]string{
		"null":         `{"enabled":null}`,
		"mode":         `{"extract_mode":"compact"}`,
		"negative max": `{"recall_xproj_max":-1}`,
		"ratio low":    `{"recall_spawn_query_df_max_ratio":-0.1}`,
		"ratio high":   `{"recall_spawn_query_df_max_ratio":1.1}`,
		"fraction int": `{"extract_every_n":1.5}`,
	} {
		t.Run(name, func(t *testing.T) {
			var config Config
			if err := json.Unmarshal([]byte(payload), &config); err == nil {
				t.Fatalf("accepted invalid payload %s", payload)
			}
		})
	}

	config := Default()
	config.StaleRankDecay = math.Inf(1)
	if err := config.Validate(); err == nil {
		t.Fatal("accepted non-finite stale_rank_decay")
	}
	config = Default()
	config.ExtractMode = "haiku"
	if err := config.Validate(); err != nil {
		t.Fatalf("rejected reference haiku extraction mode: %v", err)
	}
	config = Default()
	config.ExtractEveryN = -4
	if err := config.Validate(); err != nil {
		t.Fatalf("invented a min(1) constraint absent from the reference runtime: %v", err)
	}
}
