package memorytools

import (
	"slices"
	"testing"
)

func TestWorkspaceExtractorLiteralValidation(t *testing.T) {
	oldText := "Use release v4.0.2 at /opt/synon/config.json on 127.0.0.1:8766."
	updated := "Use release v4.0.3."
	missing := MissingDurableLiterals(oldText, updated)
	for _, literal := range []string{"v4.0.2", "/opt/synon/config.json", "127.0.0.1:8766"} {
		if !slices.Contains(missing, literal) {
			t.Fatalf("missing literals %#v do not contain %q", missing, literal)
		}
	}
	if len(missing) <= 3 {
		t.Fatalf("missing literals = %#v", missing)
	}

	repaired := "Use release v4.0.3 (was v4.0.2) at /opt/synon/config.json on 127.0.0.1:8766."
	if missing := MissingDurableLiterals(oldText, repaired); len(missing) != 0 {
		t.Fatalf("repaired text still misses %#v", missing)
	}
	if unexpected := UnexpectedDurableLiterals(repaired, oldText, updated); len(unexpected) != 0 {
		t.Fatalf("repaired text introduced %#v", unexpected)
	}
	if unexpected := UnexpectedDurableLiterals(repaired+" Build 987654.", oldText, updated); !slices.Contains(unexpected, "987654") {
		t.Fatalf("unexpected literals = %#v", unexpected)
	}

	updatedWithHistory := "Use build 654321 (was 123456)."
	if missing := MissingDurableLiterals(updatedWithHistory, "Use build 654321."); len(missing) != 0 {
		t.Fatalf("ordinary predecessor check retained was-clause values: %#v", missing)
	}
	if missing := MissingUpdatedDurableLiterals(updatedWithHistory, "Use build 654321."); !slices.Contains(missing, "123456") {
		t.Fatalf("updated-text check dropped was-clause value: %#v", missing)
	}
}
