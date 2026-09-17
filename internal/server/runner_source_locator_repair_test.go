package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRecoveredSourceLocatorCorrectionCarriesASemanticRepairContract(t *testing.T) {
	detail := "runner completion reference integrity failed (cross_artifact_failures=1): " +
		"cross-artifact consistency failures evidence_source_locator_missing:evidence.csv row=7 source_type=evidence"
	contextMessage := recoveredRunnerCorrectionContext(runnerCorrectionEntriesForClassification(
		"artifact_reference_correction_required", detail,
	))
	parts := strings.SplitN(contextMessage, "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("correction context does not contain a JSON contract: %q", contextMessage)
	}
	var contract map[string]any
	if err := json.Unmarshal([]byte(parts[1]), &contract); err != nil {
		t.Fatalf("decode correction contract: %v", err)
	}
	repairs := anySliceValue(contract["repair_requirements"])
	if len(repairs) != 1 {
		t.Fatalf("repair requirements=%#v", repairs)
	}
	repair := mapValue(repairs[0])
	if repair["code"] != "evidence_source_locator_missing" ||
		repair["artifact_path"] != "evidence.csv" || numberValue(repair["row_number"]) != 7 ||
		repair["source_type"] != "evidence" || !boolValue(repair["semantic_change_required"], false) ||
		boolValue(repair["formatting_only_satisfies"], true) || boolValue(repair["unchanged_resave_satisfies"], true) {
		t.Fatalf("source-locator repair contract=%#v", repair)
	}
	wantKinds := []string{"url", "doi", "pmid", "pmcid", "clinical_trial_id", "accession", "regulatory_application_id"}
	if got := stringArrayValue(repair["accepted_locator_kinds"]); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("accepted locator kinds=%#v, want %#v", got, wantKinds)
	}
}

func TestSourceLocatorCorrectionUsesAtomicCapabilityTransitions(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a source-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_source_locator_missing:evidence.csv row=7 source_type=evidence",
	}
	tools := []agentruntime.ToolSchema{
		{Name: "read_file", Capabilities: []string{"artifact-read"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "web_search", Capabilities: []string{"source-discovery"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "web_fetch", Capabilities: []string{"source-locator-read"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "edit_file", Capabilities: []string{"artifact-edit"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "save_artifacts", Capabilities: []string{"artifact-publication"}, Exposure: agentruntime.ToolExposureDirect},
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	call := func(id, name, arguments string) agentruntime.Message {
		return agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: id, Name: name, Arguments: json.RawMessage(arguments),
		}}}
	}
	result := func(id, content string) agentruntime.Message {
		return agentruntime.Message{Role: "tool", ToolCallID: id, Content: content}
	}
	assertChoice := func(messages []agentruntime.Message, want string) {
		t.Helper()
		choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
		if choice["name"] != want {
			t.Fatalf("choice after %d messages=%#v, want %s", len(messages), choice, want)
		}
	}

	messages := []agentruntime.Message{boundary}
	assertChoice(messages, "read_file")
	messages = append(messages,
		call("read", "read_file", `{"file_path":"evidence.csv"}`),
		result("read", `{"ok":true,"content":"title,doi,source,key_finding\nStudy,,conference,Finding"}`),
	)
	assertChoice(messages, "web_search")
	messages = append(messages,
		call("search", "web_search", `{"query":"Study conference"}`),
		result("search", `{"ok":true,"result":{"sources":[{"title":"Study","url":"https://example.test/study"}]}}`),
	)
	assertChoice(messages, "web_fetch")
	messages = append(messages,
		call("fetch", "web_fetch", `{"url":"https://example.test/study"}`),
		result("fetch", `{"ok":true,"result":{"url":"https://example.test/study","statusCode":200,"body":"Primary source text"}}`),
	)
	assertChoice(messages, "edit_file")
	messages = append(messages,
		call("edit", "edit_file", `{"file_path":"evidence.csv"}`),
		result("edit", `{"ok":true,"changed":true}`),
	)
	assertChoice(messages, "save_artifacts")
	messages = append(messages,
		call("save", "save_artifacts", `{"files":["evidence.csv"]}`),
		result("save", `{"ok":true,"artifacts":[
			{"filename":"report.md","version_id":"report-1","unchanged":true},
			{"filename":"evidence.csv","version_id":"version-2"}
		]}`),
	)
	if sessionRunnerCorrectionStillRequiresAction(run, messages) ||
		!sessionRunnerCorrectionReadyForRevalidation(run, messages) {
		t.Fatal("changed locator publication did not converge to immutable revalidation")
	}
}

func TestSourceLocatorCorrectionCanRemoveUnsupportedRowWhenSourceRoutesAreUnavailable(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a source-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_source_locator_missing:evidence.csv row=4 source_type=evidence",
	}
	tools := []agentruntime.ToolSchema{
		{Name: "read_file", Capabilities: []string{"artifact-read"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "edit_file", Capabilities: []string{"artifact-edit"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "save_artifacts", Capabilities: []string{"artifact-publication"}, Exposure: agentruntime.ToolExposureDirect},
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	readCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "read", Name: "read_file"}}}
	readResult := agentruntime.Message{Role: "tool", ToolCallID: "read", Content: `{"ok":true,"content":"current row"}`}

	initial, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary}, tools).(map[string]any)
	if initial["name"] != "read_file" {
		t.Fatalf("initial no-source choice=%#v", initial)
	}
	afterRead, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{boundary, readCall, readResult}, tools,
	).(map[string]any)
	if afterRead["name"] != "edit_file" {
		t.Fatalf("no-source fallback=%#v, want artifact edit/removal", afterRead)
	}
}

func TestSaveBatchLocatorAdvisoryIncludesTheSameRepairContract(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	save := func(id, name, kind, content string) map[string]any {
		t.Helper()
		artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
			ArtifactID: id, ProjectID: "project-a", Name: name, Kind: kind,
			Content: []byte(content), CreatedBy: "runner",
		})
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{
			"artifact_id": artifact.ID, "version_id": version.ID, "size_bytes": version.SizeBytes,
			"input_path": name,
		}
	}
	report := save("report-locator", "report.md", "text/markdown", "# Report\n")
	data := save("data-locator", "evidence.csv", "text/csv",
		"title,doi,source,key_finding\nStudy,,Conference abstract,Finding\n")
	server := &Server{workspaceStore: store}
	warning := server.agentSavedArtifactBatchQualityAdvisory(
		context.Background(), "project-a", []any{report, data},
	)
	repairs := anySliceValue(warning["repair_requirements"])
	if len(repairs) != 1 || mapValue(repairs[0])["code"] != "evidence_source_locator_missing" {
		t.Fatalf("save advisory repair requirements=%#v (warning=%#v)", repairs, warning)
	}
}
