package memoryextract

import (
	"reflect"
	"testing"
)

func TestWorkspaceParseEmittedCapsValidatesAndLetsRemoveWin(t *testing.T) {
	raw := map[string]any{
		"append": []any{
			map[string]any{"text": " first ", "evidence": "stated", "entity": "profile"},
			map[string]any{"text": "second", "evidence": "invalid"},
			map[string]any{"text": "beyond cap", "evidence": "observed"},
		},
		"replace": []any{
			map[string]any{"id": "known-a", "text": "first version", "evidence": "observed"},
			map[string]any{"id": "known-a", "text": "last version", "evidence": "bad"},
			map[string]any{"id": "unknown", "text": "ignored"},
		},
		"remove": []any{"known-a", "unknown", "known-b"},
	}
	known := map[string]struct{}{"known-a": {}, "known-b": {}}
	operations := ParseEmitted(raw, known, 3)
	if !reflect.DeepEqual(operations.Append, []AppendOperation{
		{Text: "first", Evidence: "stated", Entity: "profile"},
		{Text: "second", Evidence: "inferred"},
		{Text: "beyond cap", Evidence: "observed"},
	}) {
		t.Fatalf("append = %#v", operations.Append)
	}
	if len(operations.Replace) != 0 {
		t.Fatalf("remove did not win over replace: %#v", operations.Replace)
	}
	if !reflect.DeepEqual(operations.Remove, []string{"known-a", "known-b"}) {
		t.Fatalf("remove = %#v", operations.Remove)
	}
}

func TestWorkspaceDropRecalledAndServerStubOperations(t *testing.T) {
	operations := Operations{
		Append: []AppendOperation{
			{Text: "Release v4.0.2 ∶ use strict mode"},
			{Text: "A genuinely new durable project fact"},
			{Text: "short"},
		},
		Replace: []ReplaceOperation{
			{ID: "a", Text: "The assay ‑ endpoint is stable and verified"},
			{ID: "b", Text: "Another corrected durable decision"},
		},
		Remove: []string{"c"},
	}
	filtered := DropMatchingOperations(operations, []string{
		"release v4.0.2 : use strict mode for all tasks",
		"The assay - endpoint is stable and verified by tests",
	})
	if !reflect.DeepEqual(filtered.Append, []AppendOperation{{Text: "A genuinely new durable project fact"}, {Text: "short"}}) {
		t.Fatalf("filtered append = %#v", filtered.Append)
	}
	if !reflect.DeepEqual(filtered.Replace, []ReplaceOperation{{ID: "b", Text: "Another corrected durable decision"}}) {
		t.Fatalf("filtered replace = %#v", filtered.Replace)
	}
	if !reflect.DeepEqual(filtered.Remove, []string{"c"}) {
		t.Fatalf("filtered remove = %#v", filtered.Remove)
	}
}

func TestWorkspaceParseEmittedPreservesZeroAndNegativeConfiguredCaps(t *testing.T) {
	raw := map[string]any{"append": []any{
		map[string]any{"text": "first"}, map[string]any{"text": "second"}, map[string]any{"text": "third"},
	}}
	if operations := ParseEmitted(raw, nil, 0); len(operations.Append) != 0 {
		t.Fatalf("zero cap appended rows: %#v", operations.Append)
	}
	operations := ParseEmitted(raw, nil, -1)
	if len(operations.Append) != 2 || operations.Append[0].Text != "first" || operations.Append[1].Text != "second" {
		t.Fatalf("negative cap did not match JavaScript slice(0, -1): %#v", operations.Append)
	}
}
