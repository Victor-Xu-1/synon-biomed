package server

import (
	"strings"
	"testing"
)

func TestAppendRequiredDeliverableLinksAddsEveryMissingSnapshotOnce(t *testing.T) {
	reportVersion := "123e4567-e89b-12d3-a456-426614174000"
	ledgerVersion := "123e4567-e89b-12d3-a456-426614174001"
	content := "调研完成。\n\n[报告]({{artifact:" + reportVersion + "}})"
	got := appendSessionRunnerRequiredDeliverableLinks(content, "zh", []sessionRunnerRequiredDeliverableLink{
		{Name: "decision_report.md", VersionID: reportVersion},
		{Name: "source_ledger.csv", VersionID: ledgerVersion},
	})
	if strings.Count(got, reportVersion) != 1 || strings.Count(got, ledgerVersion) != 1 {
		t.Fatalf("required deliverable links=%q", got)
	}
	if !strings.Contains(got, "交付文件") || !strings.Contains(got, "[source_ledger.csv]({{artifact:"+ledgerVersion+"}})") {
		t.Fatalf("localized delivery section=%q", got)
	}
}

func TestAppendRequiredDeliverableLinksIgnoresInvalidOrAlreadyPresentReferences(t *testing.T) {
	version := "123e4567-e89b-12d3-a456-426614174000"
	content := "[report.md]({{artifact:" + version + "}})"
	got := appendSessionRunnerRequiredDeliverableLinks(content, "en", []sessionRunnerRequiredDeliverableLink{
		{Name: "report.md", VersionID: version},
		{Name: "scratch.json", VersionID: "not-a-version"},
	})
	if got != content {
		t.Fatalf("unchanged final=%q", got)
	}
}
