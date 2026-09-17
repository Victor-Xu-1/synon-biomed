package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/compat/oracle"
)

func TestRunContractsCLIScaffoldsHonestIncompleteEvidence(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer

	if err := runContractsCLI([]string{"scaffold", "--root", root}, &output); err != nil {
		t.Fatalf("runContractsCLI(scaffold) error = %v", err)
	}
	evidencePath := filepath.Join(root, "docs", "compatibility", "v1.1-behavior-evidence.json")
	manifest, err := oracle.Load(evidencePath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	report, err := oracle.Verify(root, manifest)
	if !errors.Is(err, oracle.ErrIncomplete) {
		t.Fatalf("Verify(scaffold) error = %v, want ErrIncomplete", err)
	}
	if report.InScope != 328 || report.Evidenced != 0 || report.Missing != 328 || report.FrontendOwned != 3 {
		t.Fatalf("scaffold report = %+v", report)
	}
	if !strings.Contains(output.String(), "\"missing\": 328") {
		t.Fatalf("scaffold output = %s", output.String())
	}

	if err := runContractsCLI([]string{"scaffold", "--root", root}, &bytes.Buffer{}); err == nil {
		t.Fatal("scaffold overwrote an existing evidence file without --force")
	}
	if err := runContractsCLI([]string{"scaffold", "--root", root, "--force"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runContractsCLI(scaffold --force) error = %v", err)
	}
}

func TestRunContractsCLIVerifyReportsMissingAndRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if err := runContractsCLI([]string{"scaffold", "--root", root}, &bytes.Buffer{}); err != nil {
		t.Fatalf("scaffold error = %v", err)
	}

	var output bytes.Buffer
	err := runContractsCLI([]string{"verify", "--root", root}, &output)
	if !errors.Is(err, oracle.ErrIncomplete) {
		t.Fatalf("verify error = %v, want ErrIncomplete", err)
	}
	var report contractVerificationReport
	if decodeErr := json.Unmarshal(output.Bytes(), &report); decodeErr != nil {
		t.Fatalf("decode verify output: %v", decodeErr)
	}
	if report.Valid || report.Implemented != 0 || report.Missing != 328 || report.HTTPRouteTotal != 34 {
		t.Fatalf("verify report = %+v", report)
	}

	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runContractsCLI([]string{"verify", "--root", root, "--evidence", "../outside.json"}, &bytes.Buffer{}); err == nil {
		t.Fatal("contracts verify accepted evidence path traversal")
	}
}

func TestRunContractsCLIReportReconcilesIncompleteEvidenceAndInventories(t *testing.T) {
	root := t.TempDir()
	if err := runContractsCLI([]string{"scaffold", "--root", root}, &bytes.Buffer{}); err != nil {
		t.Fatalf("scaffold error = %v", err)
	}
	writeOperationalJSON(t, filepath.Join(root, "docs", "compatibility", "v1.1-service-contract-coverage.json"), map[string]any{
		"schemaVersion": 1, "baseline": "synonbiomed-v1.1",
		"summary": map[string]int{"total": 190, "implemented": 188, "notApplicable": 2},
	})
	writeOperationalJSON(t, filepath.Join(root, "docs", "compatibility", "v1.1-realtime-contract-coverage.json"), map[string]any{
		"schemaVersion": 1, "baseline": "synonbiomed-v1.1",
		"summary": map[string]int{"eventsTotal": 47, "eventsImplemented": 47, "queriesTotal": 60, "queriesImplemented": 60},
	})

	var output bytes.Buffer
	if err := runContractsCLI([]string{"report", "--root", root}, &output); err != nil {
		t.Fatalf("report error = %v", err)
	}
	reportPath := filepath.Join(root, "docs", "compatibility", "current-report.json")
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report currentCompatibilityReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.SchemaVersion != 2 || report.Scope != "historical-non-web-compatibility" ||
		report.Authority != "strict-behavior-evidence" || report.CompatibilityBaselineEligible ||
		report.Behavior.Implemented != 0 || report.Behavior.Missing != 328 || len(report.Inventories) != 2 {
		t.Fatalf("report = %+v", report)
	}
	for _, inventory := range report.Inventories {
		if inventory.Authority != "inventory-only" || len(inventory.SHA256) != 64 {
			t.Fatalf("inventory = %+v", inventory)
		}
	}
	if !strings.Contains(output.String(), "\"compatibilityBaselineEligible\": false") ||
		strings.Contains(output.String(), "\"releaseEligible\"") {
		t.Fatalf("report output = %s", output.String())
	}

	writeOperationalJSON(t, filepath.Join(root, "docs", "compatibility", "v1.1-service-contract-coverage.json"), map[string]any{
		"baseline": "synonbiomed-v1.1", "summary": map[string]int{"total": 189},
	})
	if err := runContractsCLI([]string{"report", "--root", root, "--force"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "total=189, want 190") {
		t.Fatalf("report accepted inventory drift: %v", err)
	}
}

func TestRunContractsCLICapturesAndComparesRealLoopbackScenario(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte("{\"status\":\"ok\",\"token\":\"must-not-leak\"}"))
	}))
	defer server.Close()

	spec := oracle.ScenarioSpec{
		SchemaVersion: 1,
		ID:            "health",
		Contracts:     []oracle.ContractRef{{Kind: oracle.ServiceContract, Name: "healthCheck"}},
		Steps: []oracle.HTTPStep{{
			Name:           "health",
			Method:         http.MethodGet,
			Path:           "/health",
			ExpectedStatus: []int{http.StatusOK},
		}},
		Normalization: []string{"/runtime"},
		Redactions:    []string{"/steps/0/response/body/token"},
	}
	scenarioPath := filepath.Join(root, "docs", "compatibility", "scenarios", "health.json")
	writeOperationalJSON(t, scenarioPath, spec)

	for _, runtime := range []struct {
		name   string
		output string
	}{
		{name: "synonbiomed-v1.1", output: "docs/compatibility/evidence/health-v1.json"},
		{name: "synon-go-v4.0.2", output: "docs/compatibility/evidence/health-go.json"},
	} {
		if err := runContractsCLI([]string{
			"capture-http",
			"--root", root,
			"--scenario", "docs/compatibility/scenarios/health.json",
			"--base-url", server.URL,
			"--runtime", runtime.name,
			"--output", runtime.output,
		}, &bytes.Buffer{}); err != nil {
			t.Fatalf("capture %s error = %v", runtime.name, err)
		}
		captured, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(runtime.output)))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(captured), "must-not-leak") || !strings.Contains(string(captured), "<redacted>") {
			t.Fatalf("capture redaction failed: %s", captured)
		}
	}

	var output bytes.Buffer
	if err := runContractsCLI([]string{
		"compare",
		"--root", root,
		"--scenario", "docs/compatibility/scenarios/health.json",
		"--baseline", "docs/compatibility/evidence/health-v1.json",
		"--candidate", "docs/compatibility/evidence/health-go.json",
	}, &output); err != nil {
		t.Fatalf("compare error = %v", err)
	}
	if !strings.Contains(output.String(), "\"equal\": true") {
		t.Fatalf("compare output = %s", output.String())
	}
}
