package oracle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffoldUsesEveryExactRecoveredContractAndStaysIncomplete(t *testing.T) {
	manifest := Scaffold()
	if manifest.SchemaVersion != 2 || len(manifest.Records) != 331 {
		t.Fatalf("scaffold header or count = version %d records %d", manifest.SchemaVersion, len(manifest.Records))
	}
	assertRecord(t, manifest, ServiceContract, "getAgents")
	assertRecord(t, manifest, ServiceContract, "saveArtifactVersion")
	assertRecord(t, manifest, HTTPRouteContract, "POST /api/compute/providers/:name/probe")
	assertRecord(t, manifest, HTTPRouteContract, "GET /api/compute/jobs/:jobId/logs")
	assertRecord(t, manifest, EventContract, "artifact_created")
	assertRecord(t, manifest, EventContract, "compute_job_update")
	assertRecord(t, manifest, QueryContract, "artifactLineage")
	assertRecord(t, manifest, QueryContract, "mcpToolGrants")

	report, err := Verify(t.TempDir(), manifest)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Verify(scaffold) error = %v, want ErrIncomplete", err)
	}
	if report.Total != 331 || report.InScope != 328 || report.FrontendOwned != 3 || report.Evidenced != 0 || report.Missing != 328 {
		t.Fatalf("scaffold report = %+v", report)
	}
}

func TestVerifyRequiresContentAddressedRealEvidenceForEveryKind(t *testing.T) {
	root := t.TempDir()
	manifest := completeEvidenceManifest(t, root)

	report, err := Verify(root, manifest)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !report.Valid || report.InScope != 328 || report.Evidenced != 328 || report.Missing != 0 {
		t.Fatalf("report = %+v", report)
	}

	t.Run("event producer", func(t *testing.T) {
		candidate := cloneManifest(t, manifest)
		record := findRecord(t, &candidate, EventContract, "artifact_created")
		record.Probe.Producer = ""
		_, err := Verify(root, candidate)
		if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "producer") {
			t.Fatalf("Verify() error = %v, want missing producer", err)
		}
	})

	t.Run("event delivery and replay", func(t *testing.T) {
		candidate := cloneManifest(t, manifest)
		record := findRecord(t, &candidate, EventContract, "artifact_created")
		record.Probe.Delivery = ""
		_, err := Verify(root, candidate)
		if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "delivery") {
			t.Fatalf("Verify() error = %v, want missing delivery", err)
		}
	})

	t.Run("query invalidation", func(t *testing.T) {
		candidate := cloneManifest(t, manifest)
		record := findRecord(t, &candidate, QueryContract, "artifactLineage")
		record.Probe.Invalidation = ""
		_, err := Verify(root, candidate)
		if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "invalidation") {
			t.Fatalf("Verify() error = %v, want missing invalidation", err)
		}
	})

	t.Run("capture hash", func(t *testing.T) {
		candidate := cloneManifest(t, manifest)
		record := findRecord(t, &candidate, ServiceContract, "getAgents")
		record.Probe.CandidateCapture.SHA256 = strings.Repeat("0", 64)
		_, err := Verify(root, candidate)
		if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "sha256 mismatch") {
			t.Fatalf("Verify() error = %v, want capture hash mismatch", err)
		}
	})

	t.Run("capture traversal", func(t *testing.T) {
		candidate := cloneManifest(t, manifest)
		record := findRecord(t, &candidate, ServiceContract, "getAgents")
		record.Probe.CandidateCapture.Artifact = "../outside.json"
		_, err := Verify(root, candidate)
		if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "escapes evidence root") {
			t.Fatalf("Verify() error = %v, want path escape", err)
		}
	})
}

func TestVerifyRejectsFakeNamesDuplicatesAndEvidenceOnlyClaims(t *testing.T) {
	root := t.TempDir()
	manifest := completeEvidenceManifest(t, root)

	manifest.Records[0].Name = "method-1"
	_, err := Verify(root, manifest)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "unknown service") {
		t.Fatalf("Verify(fake name) error = %v", err)
	}

	manifest = completeEvidenceManifest(t, root)
	manifest.Records[1].Name = manifest.Records[0].Name
	_, err = Verify(root, manifest)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Verify(duplicate) error = %v", err)
	}

	manifest = completeEvidenceManifest(t, root)
	record := findRecord(t, &manifest, ServiceContract, "getAgents")
	record.Status = ImplementedReal
	record.Probe = nil
	record.Reason = "GET /api/go/agents exists"
	_, err = Verify(root, manifest)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "probe") {
		t.Fatalf("Verify(evidence string only) error = %v", err)
	}
}

func completeEvidenceManifest(t *testing.T, root string) Manifest {
	t.Helper()
	baselinePath := filepath.Join(root, "captures", "baseline.json")
	candidatePath := filepath.Join(root, "captures", "candidate.json")
	writeCapture(t, baselinePath, "{\"runtime\":\"v1.1\",\"status\":\"ok\"}\n")
	writeCapture(t, candidatePath, "{\"runtime\":\"synon-go\",\"status\":\"ok\"}\n")
	baselineHash := fileHash(t, baselinePath)
	candidateHash := fileHash(t, candidatePath)

	manifest := Scaffold()
	for index := range manifest.Records {
		record := &manifest.Records[index]
		if record.Status == FrontendOwnedStatus {
			continue
		}
		record.Status = DifferentialPass
		record.Reason = ""
		record.Probe = &ProbeEvidence{
			ID:            string(record.Kind) + "-" + record.Name,
			Scenario:      "real compatibility fixture transition",
			Fixture:       "testdata/v1.1/fixture",
			Transport:     "http-websocket",
			Trigger:       "invoke the named contract against persisted state",
			Normalization: []string{"/runtime"},
			Assertions:    []string{"response status and payload match", "durable state matches"},
			SourceFiles:   []string{"internal/server/runtime_compat_api_test.go"},
			TestCommand:   "go test ./internal/server -run RuntimeCompat -count=1",
			BaselineCapture: CaptureRef{
				Artifact:   "captures/baseline.json",
				SHA256:     baselineHash,
				Runtime:    "synonbiomed-v1.1",
				CapturedAt: "2026-07-11T00:00:00Z",
			},
			CandidateCapture: CaptureRef{
				Artifact:   "captures/candidate.json",
				SHA256:     candidateHash,
				Runtime:    "synon-go-v4.0.2",
				CapturedAt: "2026-07-11T00:00:01Z",
			},
		}
		switch record.Kind {
		case EventContract:
			record.Probe.Producer = "real committed domain mutation"
			record.Probe.Delivery = "real WebSocket receipt plus cursor replay"
		case QueryContract:
			record.Probe.Invalidation = "real mutation emits the exact query invalidation"
		}
	}
	return manifest
}

func assertRecord(t *testing.T, manifest Manifest, kind ContractKind, name string) {
	t.Helper()
	for _, record := range manifest.Records {
		if record.Kind == kind && record.Name == name {
			return
		}
	}
	t.Fatalf("missing %s contract %q", kind, name)
}

func findRecord(t *testing.T, manifest *Manifest, kind ContractKind, name string) *Record {
	t.Helper()
	for index := range manifest.Records {
		record := &manifest.Records[index]
		if record.Kind == kind && record.Name == name {
			return record
		}
	}
	t.Fatalf("missing %s contract %q", kind, name)
	return nil
}

func writeCapture(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func cloneManifest(t *testing.T, manifest Manifest) Manifest {
	t.Helper()
	data, err := Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	cloned, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return cloned
}
