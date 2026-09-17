package main

import (
	"context"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type historyRecoveryFake struct {
	noStreamReports []transcriptstore.NoStreamFrameHistoryReconciliation
	noStreamInputs  []transcriptstore.ReconcileNoStreamFrameHistoriesInput
	reports         []transcriptstore.LegacyFrameHistoryReconciliation
	inputs          []transcriptstore.ReconcileLegacyFrameHistoriesInput
}

func (fake *historyRecoveryFake) ReconcileNoStreamFrameHistories(
	_ context.Context,
	input transcriptstore.ReconcileNoStreamFrameHistoriesInput,
) (transcriptstore.NoStreamFrameHistoryReconciliation, error) {
	fake.noStreamInputs = append(fake.noStreamInputs, input)
	if len(fake.noStreamReports) == 0 {
		return transcriptstore.NoStreamFrameHistoryReconciliation{}, nil
	}
	report := fake.noStreamReports[0]
	fake.noStreamReports = fake.noStreamReports[1:]
	return report, nil
}

func (fake *historyRecoveryFake) ReconcileLegacyFrameHistories(
	_ context.Context,
	input transcriptstore.ReconcileLegacyFrameHistoriesInput,
) (transcriptstore.LegacyFrameHistoryReconciliation, error) {
	fake.inputs = append(fake.inputs, input)
	if len(fake.reports) == 0 {
		return transcriptstore.LegacyFrameHistoryReconciliation{}, nil
	}
	report := fake.reports[0]
	fake.reports = fake.reports[1:]
	return report, nil
}

func TestReconcileLegacyFrameHistoryCycleAdvancesStablePagesAndAggregates(t *testing.T) {
	fake := &historyRecoveryFake{reports: []transcriptstore.LegacyFrameHistoryReconciliation{
		{Scanned: 8, Activated: 2, Blocked: 1, RemainingLegacy: 11, Truncated: true, NextOwnerID: "owner-a", NextSessionID: "session-z"},
		{Scanned: 3, Activated: 1, Quarantined: 1, Deferred: 1, RemainingLegacy: 8},
	}}
	report, next, complete, err := reconcileLegacyFrameHistoryCycle(
		context.Background(), fake, legacyFrameHistoryCursor{}, legacyFrameHistoryMaxPages, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || next != (legacyFrameHistoryCursor{}) {
		t.Fatalf("complete=%t next=%#v", complete, next)
	}
	if report.Scanned != 11 || report.Activated != 3 || report.Blocked != 1 || report.Quarantined != 1 ||
		report.Deferred != 1 || report.RemainingLegacy != 8 || report.Truncated {
		t.Fatalf("report=%#v", report)
	}
	if len(fake.inputs) != 2 || fake.inputs[0].AfterOwnerID != "" || fake.inputs[0].AfterSessionID != "" ||
		fake.inputs[1].AfterOwnerID != "owner-a" || fake.inputs[1].AfterSessionID != "session-z" {
		t.Fatalf("inputs=%#v", fake.inputs)
	}
}

func TestReconcileNoStreamFrameHistoryCycleAdvancesStablePagesAndAggregates(t *testing.T) {
	fake := &historyRecoveryFake{noStreamReports: []transcriptstore.NoStreamFrameHistoryReconciliation{
		{Scanned: 8, PayloadCreated: 4, Blocked: 1, Deferred: 3,
			Truncated: true, NextOwnerID: "owner-a", NextSessionID: "session-z"},
		{Scanned: 3, PayloadCreated: 1, Blocked: 1, Deferred: 1,
			Census: transcriptstore.FrameHistoryAuthorityCensus{Total: 20, NoStream: 2, Legacy: 3, PayloadActive: 15}},
	}}
	report, next, complete, err := reconcileNoStreamFrameHistoryCycle(
		context.Background(), fake, legacyFrameHistoryCursor{}, legacyFrameHistoryMaxPages, 0,
	)
	if err != nil || !complete || next != (legacyFrameHistoryCursor{}) {
		t.Fatalf("report=%#v complete=%t next=%#v err=%v", report, complete, next, err)
	}
	if report.Scanned != 11 || report.PayloadCreated != 5 ||
		report.Blocked != 2 || report.Deferred != 4 || report.Census.NoStream != 2 || report.Truncated {
		t.Fatalf("report=%#v", report)
	}
	if len(fake.noStreamInputs) != 2 || fake.noStreamInputs[0].Limit != 32 ||
		fake.noStreamInputs[1].AfterOwnerID != "owner-a" || fake.noStreamInputs[1].AfterSessionID != "session-z" {
		t.Fatalf("inputs=%#v", fake.noStreamInputs)
	}
}

func TestReconcileLegacyFrameHistoryCycleResumesAfterPageBudget(t *testing.T) {
	fake := &historyRecoveryFake{reports: []transcriptstore.LegacyFrameHistoryReconciliation{
		{Scanned: 8, Quarantined: 8, RemainingLegacy: 9, Truncated: true, NextOwnerID: "owner-prefix", NextSessionID: "session-prefix"},
		{Scanned: 1, Activated: 1, RemainingLegacy: 8},
	}}
	first, cursor, complete, err := reconcileLegacyFrameHistoryCycle(
		context.Background(), fake, legacyFrameHistoryCursor{}, 1, 0,
	)
	if err != nil || complete || !first.Truncated ||
		cursor != (legacyFrameHistoryCursor{ownerID: "owner-prefix", sessionID: "session-prefix"}) {
		t.Fatalf("first=%#v cursor=%#v complete=%t err=%v", first, cursor, complete, err)
	}
	second, cursor, complete, err := reconcileLegacyFrameHistoryCycle(context.Background(), fake, cursor, 1, 0)
	if err != nil || !complete || cursor != (legacyFrameHistoryCursor{}) || second.Activated != 1 {
		t.Fatalf("second=%#v cursor=%#v complete=%t err=%v", second, cursor, complete, err)
	}
	if len(fake.inputs) != 2 || fake.inputs[1].AfterOwnerID != "owner-prefix" ||
		fake.inputs[1].AfterSessionID != "session-prefix" {
		t.Fatalf("inputs=%#v", fake.inputs)
	}
}
