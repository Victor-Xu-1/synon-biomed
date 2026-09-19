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

func TestMergePreservesTransferSnapshotAcrossMilestone(t *testing.T) {
	rate := 2.5
	completedBytes, totalBytes := int64(4_000_000), int64(10_000_000)
	completedItems, totalItems := int64(2), int64(8)
	current := Update{
		Phase:          "downloading_packages",
		Process:        "micromamba",
		BytesPerSecond: &rate,
		BytesCompleted: &completedBytes,
		BytesTotal:     &totalBytes,
		Indeterminate:  false,
	}
	merged := Merge(current, Update{
		Phase:          "extracting_packages",
		CompletedItems: &completedItems,
		TotalItems:     &totalItems,
	})
	if merged.Phase != "extracting_packages" || merged.Process != "micromamba" ||
		merged.BytesPerSecond == nil || *merged.BytesPerSecond != rate ||
		merged.BytesCompleted == nil || *merged.BytesCompleted != completedBytes ||
		merged.BytesTotal == nil || *merged.BytesTotal != totalBytes || merged.Indeterminate {
		t.Fatalf("merged=%#v", merged)
	}
}

func TestNormalizeMarksByteRangeAsDeterminate(t *testing.T) {
	completed, total := int64(3), int64(10)
	update := Normalize(Update{
		Phase:          "downloading_packages",
		BytesCompleted: &completed,
		BytesTotal:     &total,
		Indeterminate:  true,
	})
	if update.Indeterminate {
		t.Fatalf("update=%#v", update)
	}
}
