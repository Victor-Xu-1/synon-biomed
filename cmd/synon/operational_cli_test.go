package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/compat/oracle"
)

func TestRunVerifyContractsCLIRejectsLegacyCountsAndRequiresBehaviorCaptures(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs", "compatibility")
	if err := os.MkdirAll(docs, 0o700); err != nil {
		t.Fatal(err)
	}
	services := make([]map[string]any, 0, 190)
	for index := 0; index < 188; index++ {
		services = append(services, map[string]any{"method": fmt.Sprintf("method-%d", index+1), "status": "implemented", "evidence": "real route"})
	}
	services = append(services,
		map[string]any{"method": "anchorPreflight", "status": "not_applicable", "evidence": "client-only Web behavior"},
		map[string]any{"method": "anchorAuthPreflight", "status": "not_applicable", "evidence": "client-only Web behavior"},
	)
	writeOperationalJSON(t, filepath.Join(docs, "v1.1-service-contract-coverage.json"), map[string]any{
		"summary":  map[string]any{"total": 190, "implemented": 188, "partial": 0, "missing": 0, "notApplicable": 2},
		"services": services,
	})
	events := make([]map[string]any, 47)
	for index := range events {
		events[index] = map[string]any{"name": fmt.Sprintf("event-%d", index+1), "status": "implemented", "evidence": "real fanout"}
	}
	queries := make([]map[string]any, 60)
	for index := range queries {
		queries[index] = map[string]any{"name": fmt.Sprintf("query-%d", index+1), "status": "implemented", "evidence": "real invalidation"}
	}
	writeOperationalJSON(t, filepath.Join(docs, "v1.1-realtime-contract-coverage.json"), map[string]any{
		"summary": map[string]any{"eventsTotal": 47, "eventsImplemented": 47, "queriesTotal": 60, "queriesImplemented": 60, "totalContracts": 107, "totalImplemented": 107, "coveragePercent": 100},
		"events":  events, "queries": queries,
	})

	scaffold := oracle.Scaffold()
	writeOracleEvidence(t, filepath.Join(docs, "v1.1-behavior-evidence.json"), scaffold)
	var output bytes.Buffer
	if err := runVerifyContractsCLI([]string{"--root", root}, &output); !errors.Is(err, oracle.ErrIncomplete) {
		t.Fatalf("legacy count/evidence-string reports error = %v, want ErrIncomplete", err)
	}

	manifest, candidateCapture := completeOracleEvidence(t, root)
	writeOracleEvidence(t, filepath.Join(docs, "v1.1-behavior-evidence.json"), manifest)
	output.Reset()
	if err := runVerifyContractsCLI([]string{"--root", root}, &output); err != nil {
		t.Fatalf("verify strict behavior evidence error = %v", err)
	}
	var report contractVerificationReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Valid || report.HTTPRouteTotal != 34 || report.InScope != 328 || report.Implemented != 328 || report.Missing != 0 {
		t.Fatalf("report = %#v", report)
	}

	if err := os.WriteFile(candidateCapture, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runVerifyContractsCLI([]string{"--root", root}, &output); err == nil {
		t.Fatal("tampered candidate capture passed verification")
	}
}

func completeOracleEvidence(t *testing.T, root string) (oracle.Manifest, string) {
	t.Helper()
	baselineCapture := filepath.Join(root, "docs", "compatibility", "evidence", "baseline.json")
	candidateCapture := filepath.Join(root, "docs", "compatibility", "evidence", "candidate.json")
	for path, content := range map[string]string{
		baselineCapture:  "{\"runtime\":\"v1.1\",\"status\":\"ok\"}\n",
		candidateCapture: "{\"runtime\":\"synon-go\",\"status\":\"ok\"}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	baselineHash := sha256.Sum256(readOperationalFile(t, baselineCapture))
	candidateHash := sha256.Sum256(readOperationalFile(t, candidateCapture))
	manifest := oracle.Scaffold()
	for index := range manifest.Records {
		record := &manifest.Records[index]
		if record.Status == oracle.FrontendOwnedStatus {
			continue
		}
		record.Status = oracle.DifferentialPass
		record.Probe = &oracle.ProbeEvidence{
			ID:            string(record.Kind) + "-" + record.Name,
			Scenario:      "real persisted compatibility transition",
			Fixture:       "testdata/v1.1/fixture",
			Transport:     "http-websocket",
			Trigger:       "invoke exact recovered contract",
			Normalization: []string{"/runtime"},
			Assertions:    []string{"response and durable effects match"},
			SourceFiles:   []string{"internal/server/runtime_compat_api_test.go"},
			TestCommand:   "go test ./internal/server -run RuntimeCompat -count=1",
			BaselineCapture: oracle.CaptureRef{
				Artifact:   "docs/compatibility/evidence/baseline.json",
				SHA256:     hex.EncodeToString(baselineHash[:]),
				Runtime:    "synonbiomed",
				CapturedAt: "2026-07-11T00:00:00Z",
			},
			CandidateCapture: oracle.CaptureRef{
				Artifact:   "docs/compatibility/evidence/candidate.json",
				SHA256:     hex.EncodeToString(candidateHash[:]),
				Runtime:    "synon-go-v4.0.2",
				CapturedAt: "2026-07-11T00:00:01Z",
			},
		}
		if record.Kind == oracle.EventContract {
			record.Probe.Producer = "real committed domain mutation"
			record.Probe.Delivery = "real WebSocket receipt and cursor replay"
		}
		if record.Kind == oracle.QueryContract {
			record.Probe.Invalidation = "real mutation emits exact query invalidation"
		}
	}
	return manifest, candidateCapture
}

func writeOracleEvidence(t *testing.T, path string, manifest oracle.Manifest) {
	t.Helper()
	data, err := oracle.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readOperationalFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRunAssetsCLIVerifiesKnownPacksAndRejectsTampering(t *testing.T) {
	root := t.TempDir()
	micromambaManifest := filepath.Join(root, "assets", "optional", "micromamba", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(micromambaManifest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(micromambaManifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	packs := operationalAssetPacks(root)
	for _, pack := range packs {
		assetPath := filepath.Join(pack.AssetRoot, "payload.txt")
		if err := os.MkdirAll(filepath.Dir(assetPath), 0o700); err != nil {
			t.Fatal(err)
		}
		content := []byte(pack.Name)
		if err := os.WriteFile(assetPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		writeOperationalJSON(t, pack.ManifestPath, map[string]any{
			"schemaVersion": 1,
			"files":         []map[string]any{{"path": "payload.txt", "sha256": hex.EncodeToString(digest[:]), "bytes": len(content)}},
		})
	}

	var output bytes.Buffer
	if err := runAssetsCLI([]string{"verify", "--root", root}, &output); err != nil {
		t.Fatalf("assets verify error = %v", err)
	}
	var report assetVerificationReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Valid || len(report.Packs) != 7 || report.Checked != 7 {
		t.Fatalf("asset report = %#v", report)
	}
	if err := os.WriteFile(filepath.Join(packs[0].AssetRoot, "payload.txt"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runAssetsCLI([]string{"verify", "--root", root}, &output); err == nil {
		t.Fatal("tampered asset pack passed verification")
	}
}

func TestRunDoctorCLIUsesRealHTTPAndHonorsReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"name":"synon-go","status":"ok","version":"4.0.2"}`))
		case "/api/tools/AgentRuntimeDoctor/execute":
			var request struct {
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Input["scope"] != "mcp" {
				t.Fatalf("doctor scope = %#v", request.Input)
			}
			_, _ = w.Write([]byte(`{"result":{"ok":true,"scope":"mcp","summary":{"pass":1,"partial":0,"missing":0,"fail":0},"areas":[{"name":"mcp","status":"pass"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	if err := runDoctorCLI(context.Background(), []string{"--base-url", server.URL, "--scope", "mcp"}, &output); err != nil {
		t.Fatalf("doctor error = %v", err)
	}
	if !strings.Contains(output.String(), `"ok": true`) || !strings.Contains(output.String(), `"status": "ok"`) {
		t.Fatalf("doctor output = %s", output.String())
	}
}

func TestRunDoctorCLIFailsStrictlyWhenRuntimeIsNotReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":{"ok":false,"scope":"all","summary":{"pass":1,"partial":0,"missing":1,"fail":0},"areas":[]}}`))
	}))
	defer server.Close()

	if err := runDoctorCLI(context.Background(), []string{"--base-url", server.URL}, &bytes.Buffer{}); err == nil {
		t.Fatal("not-ready runtime passed strict doctor")
	}
	if err := runDoctorCLI(context.Background(), []string{"--base-url", server.URL, "--allow-not-ready"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("allow-not-ready doctor error = %v", err)
	}
}

func TestNormalizeMigrationAliasAndServeArgs(t *testing.T) {
	if got := normalizeMigrationArgs([]string{"run", "--source-db", "source.db"}); strings.Join(got, " ") != "migrate --source-db source.db" {
		t.Fatalf("migration args = %#v", got)
	}
	if got := normalizeServeArgs([]string{"serve", "--health-json"}); len(got) != 1 || got[0] != "--health-json" {
		t.Fatalf("serve args = %#v", got)
	}
}

func TestParseServeCommandFlagsTreatsHelpAsSuccessfulOutput(t *testing.T) {
	var output bytes.Buffer
	parsed, helpRequested, err := parseServeCommandFlags([]string{"--help"}, &output)
	if err != nil {
		t.Fatalf("parse help: %v", err)
	}
	if !helpRequested || parsed.version || parsed.health {
		t.Fatalf("help parse = %#v requested=%t", parsed, helpRequested)
	}
	if !strings.Contains(output.String(), "Usage of synon-go serve:") ||
		!strings.Contains(output.String(), "-version") ||
		!strings.Contains(output.String(), "-health-json") {
		t.Fatalf("help output = %q", output.String())
	}
}

func TestResolveRuntimeAssetPathUsesBundledExecutableRoot(t *testing.T) {
	root := t.TempDir()
	relative := "assets/synon-link/synon-link-extension-v0.6.10.zip"
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveRuntimeAssetPath(relative, root); got != path {
		t.Fatalf("resolved asset path = %q, want %q", got, path)
	}
	missing := "assets/synon-link/missing.zip"
	if got := resolveRuntimeAssetPath(missing, root); got != missing {
		t.Fatalf("missing asset path = %q", got)
	}
}

func TestResolveRuntimeAssetDirectoryPathUsesBundledSkillRoot(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "skills", "synonbiomed")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := resolveRuntimeAssetDirectoryPath("skills/synonbiomed", root); got != directory {
		t.Fatalf("resolved directory = %q, want %q", got, directory)
	}
	if got := resolveRuntimeAssetDirectoryPath("skills/missing", root); got != "" {
		t.Fatalf("missing directory resolved to %q", got)
	}
}

func writeOperationalJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
