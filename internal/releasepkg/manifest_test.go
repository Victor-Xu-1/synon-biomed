package releasepkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"synon-go/internal/buildinfo"
	"synon-go/internal/compat/oracle"
)

func TestLegacyManifestRejectsMissingCoverage(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "synon-go", "binary", 0o755)
	manifest, err := Generate(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = 3
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root); err == nil || !strings.Contains(err.Error(), "missing historical coverage") {
		t.Fatalf("legacy Verify() = %v", err)
	}
}

func TestLegacyManifestCoverageRemainsVerifiable(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "synon-go", "binary", 0o755)
	writeCompleteBehaviorEvidence(t, root)
	manifest, err := Generate(root, Options{})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	manifest.SchemaVersion = 3
	manifest.Coverage = &Coverage{
		Scope: "historical-non-web-compatibility", Baseline: "synonbiomed-v1.1",
		Authority: "strict-behavior-evidence", CompatibilityBaselineEligible: true,
		ServiceContracts: 190, ServiceImplemented: 187, ServiceNotApplicable: 3,
		HTTPRoutes: 34, HTTPRoutesImplemented: 34,
		RealtimeEvents: 47, RealtimeEventsImplemented: 47,
		RealtimeQueries: 60, RealtimeQueriesImplemented: 60,
		InScope: 328, Implemented: 328, Missing: 0,
	}
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root); err != nil {
		t.Fatalf("legacy package verification: %v", err)
	}
	manifest.Coverage.Implemented--
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root); err == nil {
		t.Fatal("inconsistent legacy scores accepted")
	}
	manifest.SchemaVersion = 4
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root); err == nil {
		t.Fatal("new manifest accepted historical scores")
	}
}

func TestGenerateAndVerifyRealReleaseTree(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "synon-go", "native-binary", 0o755)
	writeFixtureFile(t, root, "README.md", "standalone release", 0o644)
	writeFixtureFile(t, root, "assets/optional/worker.py", "print('optional')\n", 0o644)
	writeCompleteBehaviorEvidence(t, root)

	generatedAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manifest, err := Generate(root, Options{GeneratedAt: generatedAt})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if manifest.FileCount != 6 || manifest.TotalBytes == 0 {
		t.Fatalf("manifest inventory = %#v", manifest)
	}
	if manifest.Name != "synon-biomed" || manifest.Version != buildinfo.Release().Version {
		t.Fatalf("manifest identity = %q %q, want root-authority version", manifest.Name, manifest.Version)
	}
	if manifest.SchemaVersion != 4 {
		t.Fatalf("manifest schemaVersion = %d, want 4", manifest.SchemaVersion)
	}
	if manifest.Coverage != nil {
		t.Fatalf("manifest coverage = %#v", manifest.Coverage)
	}
	if manifest.CoreRuntimeRequires.Bun || manifest.CoreRuntimeRequires.Node || manifest.CoreRuntimeRequires.Go || manifest.CoreRuntimeRequires.Python {
		t.Fatalf("core runtime dependencies = %#v", manifest.CoreRuntimeRequires)
	}
	if len(manifest.Files) != 6 || manifest.Files[0].Path != "README.md" || manifest.Files[len(manifest.Files)-1].Path != "synon-go" {
		t.Fatalf("manifest file order = %#v", manifest.Files)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(root, ManifestFile))
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	var decoded Manifest
	if err := json.Unmarshal(manifestBytes, &decoded); err != nil {
		t.Fatalf("Unmarshal(manifest) error = %v", err)
	}
	if !decoded.GeneratedAt.Equal(generatedAt) {
		t.Fatalf("GeneratedAt = %s, want %s", decoded.GeneratedAt, generatedAt)
	}

	report, err := Verify(root)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !report.Valid || report.VerifiedFiles != 6 {
		t.Fatalf("Verify() report = %#v", report)
	}

	writeFixtureFile(t, root, "README.md", strings.Repeat("x", len("standalone release")), 0o644)
	if _, err := Verify(root); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Verify(tampered) error = %v", err)
	}
}

func TestVerifyRejectsExtraFilesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "synon-go", "binary", 0o755)
	writeCompleteBehaviorEvidence(t, root)
	if _, err := Generate(root, Options{}); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	writeFixtureFile(t, root, "unexpected.txt", "not in manifest", 0o600)
	if _, err := Verify(root); err == nil || !strings.Contains(err.Error(), "unexpected file") {
		t.Fatalf("Verify(extra file) error = %v", err)
	}

	if err := os.Remove(filepath.Join(root, "unexpected.txt")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := os.Symlink("synon-go", filepath.Join(root, "runtime-link")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := Verify(root); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Verify(symlink) error = %v", err)
	}
}

func TestVerifyWindowsManifestIgnoresNonPortablePOSIXMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode transition is not observable on Windows")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "synon-go.exe")
	if err := os.WriteFile(binary, []byte("windows-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCompleteBehaviorEvidence(t, root)
	if _, err := Generate(root, Options{GOOS: "windows", GOARCH: "amd64"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binary, 0o644); err != nil {
		t.Fatal(err)
	}
	if report, err := Verify(root); err != nil || !report.Valid {
		t.Fatalf("Windows manifest should tolerate extracted mode differences: report=%#v err=%v", report, err)
	}

	if _, err := Generate(root, Options{GOOS: "linux", GOARCH: "amd64"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root); err == nil {
		t.Fatal("Linux manifest accepted a POSIX mode mismatch")
	}
}

func writeFixtureFile(t *testing.T, root, relativePath, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", relativePath, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", relativePath, err)
	}
}

func readJSONFixture(t *testing.T, path string, target any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", path, err)
	}
}

func writeCompleteBehaviorEvidence(t *testing.T, root string) {
	t.Helper()
	baselineRelative := "docs/compatibility/evidence/release-baseline.json"
	candidateRelative := "docs/compatibility/evidence/release-candidate.json"
	baseline := []byte("{\"runtime\":\"synonbiomed-v1.1\",\"status\":\"ok\"}\n")
	candidate := []byte("{\"runtime\":\"synon-go-v4.0.2\",\"status\":\"ok\"}\n")
	writeFixtureFile(t, root, baselineRelative, string(baseline), 0o600)
	writeFixtureFile(t, root, candidateRelative, string(candidate), 0o600)
	baselineHash := sha256.Sum256(baseline)
	candidateHash := sha256.Sum256(candidate)

	manifest := oracle.Scaffold()
	for index := range manifest.Records {
		record := &manifest.Records[index]
		if record.Status == oracle.FrontendOwnedStatus {
			continue
		}
		record.Status = oracle.DifferentialPass
		record.Probe = &oracle.ProbeEvidence{
			ID:            string(record.Kind) + "-" + record.Name,
			Scenario:      "release strict evidence fixture",
			Fixture:       "internal/releasepkg/testdata/strict-evidence",
			Transport:     "real-file-content-addressed-fixture",
			Trigger:       "execute exact recovered contract against isolated persisted state",
			Normalization: []string{"/runtime"},
			Assertions:    []string{"baseline and candidate behavior match"},
			SourceFiles:   []string{"internal/releasepkg/manifest_test.go"},
			TestCommand:   "go test ./internal/releasepkg -count=1",
			BaselineCapture: oracle.CaptureRef{
				Artifact: baselineRelative, SHA256: hex.EncodeToString(baselineHash[:]),
				Runtime: "synonbiomed-v1.1", CapturedAt: "2026-07-11T00:00:00Z",
			},
			CandidateCapture: oracle.CaptureRef{
				Artifact: candidateRelative, SHA256: hex.EncodeToString(candidateHash[:]),
				Runtime: "synon-go-v4.0.2", CapturedAt: "2026-07-11T00:00:01Z",
			},
		}
		if record.Kind == oracle.EventContract {
			record.Probe.Producer = "real committed domain mutation"
			record.Probe.Delivery = "real delivery and replay"
		}
		if record.Kind == oracle.QueryContract {
			record.Probe.Invalidation = "real mutation invalidates exact query"
		}
	}
	data, err := oracle.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, root, "docs/compatibility/v1.1-behavior-evidence.json", string(data), 0o600)
}
