package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScientificArtifactFailureFeedbackPreservesParserDiagnosis(t *testing.T) {
	validate := scientificArtifactStreamTestValidator(t)
	result, err := validate(context.Background(), strings.NewReader("smiles name\nCCO valid\nnot_a_molecule invalid\n"), "smi")
	if !errors.Is(err, errInvalidScientificArtifact) || result.ParsedCount != 1 || result.InvalidRecordCount != 1 {
		t.Fatalf("real parser result=%#v err=%v", result, err)
	}
	failure := agentSaveArtifactFailure("molecules.smi", fmt.Errorf("save: %w", err))
	if failure["code"] != "invalid_scientific_artifact" || failure["validation_code"] != "invalid_smiles_records" ||
		failure["validation_format"] != "smi" || failure["validation_records"] != 2 ||
		failure["validation_parsed_records"] != 1 || failure["validation_invalid_records"] != 1 {
		t.Fatalf("parser diagnosis lost at save boundary: %#v", failure)
	}
	if !strings.Contains(stringValue(failure["validation_detail"]), "invalid_smiles_records") {
		t.Fatalf("missing actionable detail: %#v", failure)
	}
}

func TestAgentSaveArtifactsScientificFailureThenRepair(t *testing.T) {
	fixture := newAgentSaveArtifactsFixtureWithKernelManager(t, realManagedScientificKernelManagerForServerTest(t))
	const name = "out/molecules.smi"
	input := map[string]any{"files": []any{name}, "language": "python", "environment": "synon-biomed-python", "human_description": "Save molecular data"}
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, name, "smiles name\nCCO valid\nnot_a_molecule invalid\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "invalid-source", 1, write)
	result, err := fixture.server.executeAgentSaveArtifacts(fixture.toolContext(t, "save-invalid", input), fixture.identity, "save-invalid", input)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) {
		t.Fatalf("invalid data accepted: %#v %v", result, err)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 || failures[0]["validation_code"] != "invalid_smiles_records" || failures[0]["validation_invalid_records"] != 1 {
		t.Fatalf("save response lost diagnosis: %#v", result)
	}
	assertAgentSaveArtifactsNoCanonicalWrites(t, fixture)
	write = writeAgentSaveArtifactsFile(t, fixture.projectPath, name, "smiles name\nCCO ethanol\nCC ethane\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "repaired-source", 2, write)
	result, err = fixture.server.executeAgentSaveArtifacts(fixture.toolContext(t, "save-repaired", input), fixture.identity, "save-repaired", input)
	if err != nil || result["errors"] != nil || len(agentSaveArtifactResults(t, result)) != 1 {
		t.Fatalf("repaired data could not publish: %#v %v", result, err)
	}
}

func TestScientificArtifactValidatorExitContract(t *testing.T) {
	python := os.Getenv("SYNON_TEST_SCIENTIFIC_PYTHON")
	if python == "" {
		t.Skip("set SYNON_TEST_SCIENTIFIC_PYTHON for real subprocess protocol tests")
	}
	for _, test := range []struct {
		name string
		ok   bool
		code string
		exit int
	}{
		{"invalid_with_success_exit", false, "invalid_smiles_records", 0},
		{"valid_with_failure_exit", true, "valid_smiles", 2},
		{"invalid_with_unexpected_exit", false, "invalid_smiles_records", 7},
		{"unknown_invalid_code", false, "unexpected_value", 2},
		{"parser_failure_with_success_exit", false, "parser_failed", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := scientificArtifactValidation{SchemaVersion: 2, Format: "smi", OK: test.ok, Code: test.code,
				DelimiterCount: 1, SupplierRecordCount: 1, ParsedCount: 1, AtomCountRecords: 1, TerminalDelimiter: true, RDKitVersion: "2024.03.5"}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			validator := filepath.Join(t.TempDir(), "validator.py")
			script := fmt.Sprintf("import sys\nsys.stdin.buffer.read()\nprint(%q)\nsys.exit(%d)\n", string(raw), test.exit)
			if err := os.WriteFile(validator, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = runScientificArtifactValidator(context.Background(), strings.NewReader("CCO\n"), "smi", python, validator, "protocol-test", "2024.03.5")
			if !errors.Is(err, errScientificArtifactValidationUnavailable) || errors.Is(err, errInvalidScientificArtifact) {
				t.Fatalf("validator protocol fault blamed on user data: %v", err)
			}
		})
	}
}
