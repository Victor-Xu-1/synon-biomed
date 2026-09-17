package runtimecontrol

import (
	"slices"
	"testing"
)

func TestBuiltinAllowlistMatchesRecoveredV11Rules(t *testing.T) {
	groups := BuiltinAllowlistGroups()
	if len(groups) != 6 || groups[0].ID != "pkg" || !groups[0].Locked || len(groups[0].Domains) != 16 {
		t.Fatalf("groups = %#v", groups)
	}
	effective := EffectiveBuiltinAllowlist([]string{"pypi.org", "ncbi.nlm.nih.gov"}, []string{"pkg"})
	if !slices.Contains(effective, "pypi.org") {
		t.Fatalf("locked package domain was disabled: %#v", effective)
	}
	if slices.Contains(effective, "ncbi.nlm.nih.gov") {
		t.Fatalf("unlocked NIH domain was not disabled: %#v", effective)
	}
	disabledNIH := EffectiveBuiltinAllowlist(nil, []string{"nih"})
	if slices.Contains(disabledNIH, "ncbi.nlm.nih.gov") {
		t.Fatalf("unlocked NIH group was not disabled: %#v", disabledNIH)
	}
}
