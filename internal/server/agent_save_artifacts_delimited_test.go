package server

import (
	"errors"
	"strings"
	"testing"
)

func TestAgentSaveArtifactsRejectsMalformedDelimitedTableBeforeCanonicalWrite(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	valid := writeAgentSaveArtifactsFile(
		t, fixture.projectPath, "out/valid.csv",
		"name,method,result\nA,\"NMR, FTIR\",pass\n",
	)
	invalid := writeAgentSaveArtifactsFile(
		t, fixture.projectPath, "out/invalid.csv",
		"name,method,result\nA,NMR, FTIR,pass\n",
	)
	fixture.saveExecution(
		t, fixture.identity.access, fixture.projectPath,
		"execution-delimited-policy", 1, valid, invalid,
	)
	input := map[string]any{
		"files": []any{"out/valid.csv", "out/invalid.csv"}, "language": "python",
		"human_description": "Saving structured comparison tables",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-delimited-policy", input),
		fixture.identity, "save-delimited-policy", input,
	)
	if err != nil && !errors.Is(err, errAgentSaveArtifactsNoResults) {
		t.Fatalf("mixed delimited save returned error: %v", err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || artifacts[0]["filename"] != "valid.csv" {
		t.Fatalf("published artifacts=%#v", result)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 {
		t.Fatalf("delimited failures=%#v", result)
	}
	if failures[0]["path"] != "out/invalid.csv" ||
		failures[0]["code"] != "invalid_delimited_artifact" ||
		failures[0]["retryable"] != true {
		t.Fatalf("delimited failure=%#v", failures[0])
	}
	if detail := failures[0]["validation_detail"]; detail == nil ||
		!strings.Contains(detail.(string), "record 2") ||
		!strings.Contains(detail.(string), "wrong number of fields") {
		t.Fatalf("delimited validation detail=%#v", failures[0])
	}
	recovery := failures[0]["recovery"].(map[string]any)
	if recovery["action"] != "write_consistent_csv_or_tsv_then_retry_only_that_file" {
		t.Fatalf("delimited recovery=%#v", recovery)
	}
	if !strings.Contains(stringValue(recovery["writer"]), "standard CSV or TSV writer") {
		t.Fatalf("delimited recovery did not recommend the canonical writer path: %#v", recovery)
	}
	if listed, listErr := fixture.store.ListArtifacts("project-save", 100, 0); listErr != nil || len(listed) != 1 {
		t.Fatalf("canonical artifacts=%#v err=%v", listed, listErr)
	}
}
