package server

import (
	"fmt"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
)

const sessionRunnerFreshnessTestVersion = "729b7af7-3f67-4f9b-94af-80eeae747435"

func sessionRunnerFreshnessSaveMessages(filename, contentType string) []agentruntime.Message {
	return []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "save-1", Name: "save_artifacts"}}},
		{Role: "tool", ToolCallID: "save-1", Content: fmt.Sprintf(
			`{"artifacts":[{"version_id":%q,"filename":%q,"content_type":%q,"retention":"snapshot"}]}`,
			sessionRunnerFreshnessTestVersion, filename, contentType,
		)},
	}
}

func TestArtifactPublicationFreshnessRejectsReferencedReportBeforeLaterEvidence(t *testing.T) {
	messages := sessionRunnerFreshnessSaveMessages("report.md", "text/markdown")
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "source-1", Name: "fetch_article_fulltext"}}},
		agentruntime.Message{Role: "tool", ToolCallID: "source-1", Content: `{"ok":true,"result":{"status":"not_available"}}`},
	)
	final := "Report: {{artifact:" + sessionRunnerFreshnessTestVersion + "}}"
	failures := sessionRunnerArtifactPublicationFreshnessFailures(messages, final)
	if len(failures) != 1 || failures[0] != sessionRunnerArtifactPublicationStaleMarker+":report.md later_tool=fetch_article_fulltext" {
		t.Fatalf("freshness failures=%#v", failures)
	}
}

func TestArtifactPublicationFreshnessAcceptsReportPublishedAfterEvidence(t *testing.T) {
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "source-1", Name: "web_fetch"}}},
		{Role: "tool", ToolCallID: "source-1", Content: `{"ok":true,"result":{"status":"available"}}`},
	}
	messages = append(messages, sessionRunnerFreshnessSaveMessages("report.md", "text/markdown")...)
	final := "Report: {{artifact:" + sessionRunnerFreshnessTestVersion + "}}"
	if failures := sessionRunnerArtifactPublicationFreshnessFailures(messages, final); len(failures) != 0 {
		t.Fatalf("freshness failures=%#v", failures)
	}
}

func TestArtifactPublicationFreshnessPreservesUnchangedPriorTurnReport(t *testing.T) {
	messages := sessionRunnerFreshnessSaveMessages("target_evidence.md", "text/markdown")
	messages = append(messages,
		agentruntime.Message{Role: "user", Content: "Correct a separate molecular structure file."},
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "compute-1", Name: "python"}}},
		agentruntime.Message{Role: "tool", ToolCallID: "compute-1", Content: `{"ok":true}`},
	)
	final := "Unchanged evidence: {{artifact:" + sessionRunnerFreshnessTestVersion + "}}"
	if failures := sessionRunnerArtifactPublicationFreshnessFailures(messages, final); len(failures) != 0 {
		t.Fatalf("prior-turn artifact was invalidated by unrelated current-turn work: %#v", failures)
	}
}

func TestArtifactPublicationFreshnessStillRejectsCurrentTurnReportBeforeLaterEvidence(t *testing.T) {
	messages := []agentruntime.Message{{Role: "user", Content: "Prepare a current report."}}
	messages = append(messages, sessionRunnerFreshnessSaveMessages("report.md", "text/markdown")...)
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "source-1", Name: "fetch_article_fulltext"}}},
		agentruntime.Message{Role: "tool", ToolCallID: "source-1", Content: `{"ok":true,"result":{"status":"available"}}`},
	)
	final := "Report: {{artifact:" + sessionRunnerFreshnessTestVersion + "}}"
	failures := sessionRunnerArtifactPublicationFreshnessFailures(messages, final)
	if len(failures) != 1 || failures[0] != sessionRunnerArtifactPublicationStaleMarker+":report.md later_tool=fetch_article_fulltext" {
		t.Fatalf("current-turn freshness failures=%#v", failures)
	}
}

func TestArtifactPublicationFreshnessAllowsReadbackAndNonNarrativeArtifacts(t *testing.T) {
	for _, fixture := range []struct {
		name, filename, contentType string
	}{
		{name: "report readback", filename: "report.md", contentType: "text/markdown"},
		{name: "image", filename: "figure.png", contentType: "image/png"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			messages := sessionRunnerFreshnessSaveMessages(fixture.filename, fixture.contentType)
			messages = append(messages,
				agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "read-1", Name: "read_file"}}},
				agentruntime.Message{Role: "tool", ToolCallID: "read-1", Content: `{"ok":true,"content":"checked"}`},
			)
			final := "Artifact: {{artifact:" + sessionRunnerFreshnessTestVersion + "}}"
			if failures := sessionRunnerArtifactPublicationFreshnessFailures(messages, final); len(failures) != 0 {
				t.Fatalf("freshness failures=%#v", failures)
			}
		})
	}
}

func TestArtifactPublicationFreshnessIgnoresCapabilityInspectionAfterSave(t *testing.T) {
	messages := sessionRunnerFreshnessSaveMessages("report.md", "text/markdown")
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "skill-1", Name: "skill", Arguments: []byte(`{"skill":"research-workflow"}`),
		}}},
		agentruntime.Message{Role: "tool", ToolCallID: "skill-1", Content: `{"ok":true,"reused":true}`},
	)
	final := "Report: {{artifact:" + sessionRunnerFreshnessTestVersion + "}}"
	if failures := sessionRunnerArtifactPublicationFreshnessFailures(messages, final); len(failures) != 0 {
		t.Fatalf("capability inspection invalidated unchanged report=%#v", failures)
	}
}

func TestArtifactPublicationFreshnessScopesDirectEditsToTheirArtifact(t *testing.T) {
	const reportVersion = "539f9c8a-56ae-4a17-90ef-487488826a95"
	messages := []agentruntime.Message{
		{Role: "user", Content: "Prepare a report and evidence table."},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "save-report", Name: "save_artifacts"}}},
		{Role: "tool", ToolCallID: "save-report", Content: fmt.Sprintf(
			`{"artifacts":[{"version_id":%q,"filename":"report.md","content_type":"text/markdown","retention":"snapshot"}]}`,
			reportVersion,
		)},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "edit-evidence", Name: "edit_file",
			Arguments: []byte(`{"file_path":"evidence.md","old_string":"old","new_string":"new"}`),
		}}},
		{Role: "tool", ToolCallID: "edit-evidence", Content: `{"ok":true}`},
	}
	final := "Report: {{artifact:" + reportVersion + "}}"
	if failures := sessionRunnerArtifactPublicationFreshnessFailures(messages, final); len(failures) != 0 {
		t.Fatalf("companion edit invalidated report=%#v", failures)
	}

	messages[3].ToolCalls[0].Arguments = []byte(
		`{"file_path":"./report.md","old_string":"old","new_string":"new"}`,
	)
	failures := sessionRunnerArtifactPublicationFreshnessFailures(messages, final)
	if len(failures) != 1 || failures[0] != sessionRunnerArtifactPublicationStaleMarker+":report.md later_tool=edit_file" {
		t.Fatalf("same-file edit freshness failures=%#v", failures)
	}

	messages[2].Content = fmt.Sprintf(
		`{"artifacts":[{"version_id":%q,"filename":"report.md","input_path":"deliverables/report.md","content_type":"text/markdown","retention":"snapshot"}]}`,
		reportVersion,
	)
	messages[3].ToolCalls[0].Arguments = []byte(`{"file_path":"deliverables/../deliverables/report.md"}`)
	failures = sessionRunnerArtifactPublicationFreshnessFailures(messages, final)
	if len(failures) != 1 || failures[0] != sessionRunnerArtifactPublicationStaleMarker+":report.md later_tool=edit_file" {
		t.Fatalf("nested same-file edit freshness failures=%#v", failures)
	}
}

func TestArtifactPublicationFreshnessConvergesAcrossCompanionRepairs(t *testing.T) {
	const (
		reportV1   = "541c8d4d-d0f2-41f9-ad3b-877eaa1e73d1"
		evidenceV1 = "25059f80-0659-460a-b6ee-c4a19e482f00"
		reportV2   = "e73e7a95-e978-4a89-83f7-0fb91ca09cc0"
		evidenceV2 = "2f370df9-6edd-423c-b11e-8c6afea5ec3f"
	)
	save := func(callID, versionID, filename string) []agentruntime.Message {
		return []agentruntime.Message{
			{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: callID, Name: "save_artifacts"}}},
			{Role: "tool", ToolCallID: callID, Content: fmt.Sprintf(
				`{"artifacts":[{"version_id":%q,"filename":%q,"content_type":"text/markdown","retention":"snapshot"}]}`,
				versionID, filename,
			)},
		}
	}
	edit := func(callID, filename string) []agentruntime.Message {
		return []agentruntime.Message{
			{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
				ID: callID, Name: "edit_file", Arguments: []byte(fmt.Sprintf(`{"file_path":%q}`, filename)),
			}}},
			{Role: "tool", ToolCallID: callID, Content: `{"ok":true}`},
		}
	}
	messages := []agentruntime.Message{{Role: "user", Content: "Prepare companion deliverables."}}
	messages = append(messages, save("save-report-v1", reportV1, "report.md")...)
	messages = append(messages, save("save-evidence-v1", evidenceV1, "evidence.md")...)
	messages = append(messages, edit("edit-evidence", "evidence.md")...)
	messages = append(messages, save("save-evidence-v2", evidenceV2, "evidence.md")...)
	messages = append(messages, edit("edit-report", "report.md")...)
	messages = append(messages, save("save-report-v2", reportV2, "report.md")...)
	final := "Report: {{artifact:" + reportV2 + "}} Evidence: {{artifact:" + evidenceV2 + "}}"
	if failures := sessionRunnerArtifactPublicationFreshnessFailures(messages, final); len(failures) != 0 {
		t.Fatalf("companion repair did not converge=%#v", failures)
	}
}

func TestStaleArtifactPublicationUsesAgentOwnedCorrectionPath(t *testing.T) {
	detail := "runner completion reference integrity failed (cross_artifact_failures=1): " +
		sessionRunnerArtifactPublicationStaleMarker + ":report.md later_tool=web_fetch"
	if !runnerCorrectionIsAgentOwnedArtifactRepair("artifact_reference_correction_required", detail) {
		t.Fatal("stale publication must use the existing artifact correction path")
	}
	entries := runnerCorrectionEntriesForClassification("artifact_reference_correction_required", detail)
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{"synon.runner_recovery.v1", `"required_transition":"repair_current_candidate"`, "report.md", sessionRunnerArtifactPublicationStaleMarker} {
		if !strings.Contains(context, required) {
			t.Fatalf("correction context missing %q: %s", required, context)
		}
	}
}

func TestRecoveredPublicationFreshnessCorrectionMigratesByContractRevision(t *testing.T) {
	detail := "runner completion reference integrity failed " +
		"(unresolved_artifacts=0 malformed_artifact_references=0 unsupported_citations=0 " +
		"invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=1 " +
		"invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=0): " +
		"cross-artifact consistency failures " + sessionRunnerArtifactPublicationStaleMarker +
		":target_evidence.md later_tool=python"
	entries := func(revision int64, correctionDetail string) []eventjournal.Entry {
		return []eventjournal.Entry{{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "interrupted",
			"reason_code":   "artifact_reference_correction_required",
			"resume_detail": correctionDetail, "recovery_contract_revision": revision,
		}}}
	}

	if context := recoveredRunnerCorrectionContext(entries(sessionRunnerRecoveryContractRevision-1, detail)); context != "" {
		t.Fatalf("superseded prior-contract correction remained active: %s", context)
	}
	if context := recoveredRunnerCorrectionContext(entries(sessionRunnerRecoveryContractRevision, detail)); !strings.Contains(context, sessionRunnerArtifactPublicationStaleMarker) {
		t.Fatalf("current-contract correction was discarded: %s", context)
	}
	blocking := strings.Replace(detail, "invalid_scientific_artifacts=0", "invalid_scientific_artifacts=1", 1)
	if context := recoveredRunnerCorrectionContext(entries(sessionRunnerRecoveryContractRevision-1, blocking)); !strings.Contains(context, "invalid_scientific_artifacts=1") {
		t.Fatalf("older correction with another blocker was discarded: %s", context)
	}
	unknown := detail + "; unexpected_new_failure:report.md"
	if context := recoveredRunnerCorrectionContext(entries(sessionRunnerRecoveryContractRevision-1, unknown)); !strings.Contains(context, "unexpected_new_failure") {
		t.Fatalf("older correction with unknown trailing detail was discarded: %s", context)
	}
}
