package toolprogress

import "testing"

func TestMergeCarriesMilestonesButClearsPriorPhaseDetail(t *testing.T) {
	percent := float64(71)
	completed, total := int64(1), int64(8)
	current := Update{
		Phase: "downloading_packages", Message: "safe detail", PhasePercent: &percent,
		CompletedItems: &completed, TotalItems: &total,
	}
	merged := Merge(current, Update{Phase: "extracting_packages", Indeterminate: true})
	if merged.Phase != "extracting_packages" || merged.Message != "" || merged.PhasePercent != nil ||
		merged.CompletedItems == nil || *merged.CompletedItems != 1 ||
		merged.TotalItems == nil || *merged.TotalItems != 8 || !merged.Indeterminate {
		t.Fatalf("merged=%#v", merged)
	}
}

func TestNormalizeRejectsInventedPercentAndBoundsMilestones(t *testing.T) {
	percent := float64(101)
	completed, total := int64(9), int64(8)
	update := Normalize(Update{Phase: " validating_environment ", PhasePercent: &percent, CompletedItems: &completed, TotalItems: &total})
	if update.Phase != "validating_environment" || update.PhasePercent != nil ||
		update.CompletedItems == nil || *update.CompletedItems != 8 {
		t.Fatalf("update=%#v", update)
	}
}
