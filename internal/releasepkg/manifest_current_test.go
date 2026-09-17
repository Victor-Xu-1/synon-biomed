package releasepkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentManifestDoesNotRequireHistoricalEngineeringArchive(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "synon-go", "standalone-binary", 0o755)
	manifest, err := Generate(root, Options{GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 4 || manifest.FileCount != 1 {
		t.Fatalf("current manifest identity/inventory = %#v", manifest)
	}
	raw, err := os.ReadFile(filepath.Join(root, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"coverage"`) || strings.Contains(string(raw), "synonbiomed-v1.1") {
		t.Fatal("current package exposes historical development scores")
	}
	if report, err := Verify(root); err != nil || !report.Valid {
		t.Fatalf("current verify = %#v, %v", report, err)
	}
	writeFixtureFile(t, root, "synon-go", "tampered-binary", 0o755)
	if _, err := Verify(root); err == nil {
		t.Fatal("current package integrity check accepted changed bytes")
	}
}

func TestCurrentManifestStillRequiresRequestedSupplyChain(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "synon-go", "standalone-binary", 0o755)
	if _, err := Generate(root, Options{RequireSupplyChain: true}); err == nil {
		t.Fatal("missing supply-chain records were accepted")
	}
}
