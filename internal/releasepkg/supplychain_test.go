package releasepkg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGenerateAndVerifySupplyChainForRealSynonBinary(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join(testSourceDirectory(t), "..", ".."))
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("Go toolchain is required for the real release supply-chain test")
	}
	root := t.TempDir()
	binaryPath := filepath.Join(root, "synon-go")
	build := exec.Command(goBinary, "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-o", binaryPath, "./cmd/synon")
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real Synon binary: %v\n%s", err, output)
	}
	moduleCache := goEnv(t, goBinary, repositoryRoot, "GOMODCACHE")
	sourceContent, err := os.ReadFile(filepath.Join(repositoryRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	sourceDigest := sha256.Sum256(sourceContent)
	options := SupplyChainOptions{
		GeneratedAt: time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC),
		GOOS:        runtime.GOOS, GOARCH: runtime.GOARCH,
		BinaryPaths: []string{"synon-go"}, ModuleCache: moduleCache, GoRoot: runtime.GOROOT(),
		SourceURI: "pkg:generic/" + releaseName + "@" + releaseVersion, SourceRevision: "test-source-revision",
		SourceSHA256: hex.EncodeToString(sourceDigest[:]), SourceDirty: true,
	}
	report, err := GenerateSupplyChain(root, options)
	if err != nil {
		t.Fatalf("GenerateSupplyChain() error = %v", err)
	}
	if !report.Valid || report.Binaries != 1 || report.Modules < 10 || report.LicenseFiles < report.Modules || report.MissingLicense != 0 {
		t.Fatalf("supply-chain report = %#v", report)
	}
	if _, err := VerifySupplyChainForRelease(root); err == nil || !strings.Contains(err.Error(), "clean source revision") {
		t.Fatalf("VerifySupplyChainForRelease(dirty) error = %v", err)
	}
	options.SourceDirty = false
	if _, err := GenerateSupplyChain(root, options); err != nil {
		t.Fatalf("GenerateSupplyChain(clean) error = %v", err)
	}
	if report, err := VerifySupplyChainForRelease(root); err != nil || !report.Valid {
		t.Fatalf("VerifySupplyChainForRelease(clean) report=%#v error=%v", report, err)
	}
	first := readSupplyChainArtifacts(t, root)
	if _, err := GenerateSupplyChain(root, options); err != nil {
		t.Fatalf("repeat GenerateSupplyChain() error = %v", err)
	}
	second := readSupplyChainArtifacts(t, root)
	for name, content := range first {
		if !bytes.Equal(content, second[name]) {
			t.Fatalf("%s is not reproducible", name)
		}
	}
	if err := os.WriteFile(binaryPath, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySupplyChain(root); err == nil || !strings.Contains(err.Error(), "subject digest mismatch") {
		t.Fatalf("VerifySupplyChain(tampered) error = %v", err)
	}
}

func TestCollectModuleLicensesUsesVersionPinnedOverrideOnlyWhenArchiveLacksLicense(t *testing.T) {
	moduleCache := t.TempDir()
	overrides := t.TempDir()
	module := buildModule{Path: "example.test/owner/dependency", Version: "v1.2.3", Sum: "h1:test"}
	moduleDirectory := filepath.Join(moduleCache, "example.test/owner/dependency@v1.2.3")
	overrideDirectory := filepath.Join(overrides, "example.test/owner/dependency@v1.2.3")
	if err := os.MkdirAll(moduleDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(overrideDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overrideDirectory, "LICENSE"), []byte("audited license\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	records, total, missing, err := collectModuleLicenses([]buildModule{module}, "go-test", moduleCache, runtime.GOROOT(), overrides)
	if err != nil {
		t.Fatal(err)
	}
	if missing != 0 || total != 1 || len(records) != 1 || len(records[0].LicenseFiles) != 1 {
		t.Fatalf("records=%#v total=%d missing=%d", records, total, missing)
	}
	if records[0].LicenseFiles[0].Path != "curated/LICENSE" || records[0].LicenseFiles[0].Content != "audited license\n" {
		t.Fatalf("override evidence = %#v", records[0].LicenseFiles[0])
	}
}

func testSourceDirectory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(source)
}

func goEnv(t *testing.T, goBinary, root, name string) string {
	t.Helper()
	command := exec.Command(goBinary, "env", name)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go env %s: %v", name, err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		t.Fatalf("go env %s returned an empty value", name)
	}
	return value
}

func readSupplyChainArtifacts(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte, 3)
	for _, name := range []string{SBOMFile, ThirdPartyLicensesFile, ProvenanceFile} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		result[name] = content
	}
	return result
}
