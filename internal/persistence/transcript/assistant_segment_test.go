package transcript

import "testing"

func TestParseAssistantSegmentV1UsesClosedVersionedContract(t *testing.T) {
	segment, present, err := ParseAssistantSegmentV1(map[string]any{
		"assistant_segment": map[string]any{
			"version": float64(1), "ordinal": float64(2), "replace_scope": "attempt",
		},
	})
	if err != nil || !present || segment.Ordinal != 2 || segment.ReplaceScope != AssistantReplaceScopeAttempt {
		t.Fatalf("segment=%#v present=%t err=%v", segment, present, err)
	}
	segment, present, err = ParseAssistantSegmentV1(map[string]any{
		"assistant_segment": map[string]any{
			"version": 1, "ordinal": 3, "replace_scope": "segment",
		},
	})
	if err != nil || !present || segment.Ordinal != 3 || segment.ReplaceScope != AssistantReplaceScopeSegment {
		t.Fatalf("narrow segment=%#v present=%t err=%v", segment, present, err)
	}

	if _, present, err := ParseAssistantSegmentV1(map[string]any{}); err != nil || present {
		t.Fatalf("legacy payload present=%t err=%v", present, err)
	}

	for _, invalid := range []map[string]any{
		{"assistant_segment": "1"},
		{"assistant_segment": map[string]any{"version": 2, "ordinal": 1}},
		{"assistant_segment": map[string]any{"version": 1, "ordinal": 0}},
		{"assistant_segment": map[string]any{"version": 1, "ordinal": 2.5}},
		{"assistant_segment": map[string]any{"version": 1, "ordinal": 2, "replace_scope": "message"}},
		{"assistant_segment": map[string]any{"version": 1, "ordinal": 2, "extra": true}},
	} {
		if _, present, err := ParseAssistantSegmentV1(invalid); err == nil || !present {
			t.Fatalf("accepted invalid payload %#v present=%t err=%v", invalid, present, err)
		}
	}
}
