package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/releasepkg"
)

func TestReleaseManifestCLICreatesAndVerifiesPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "synon-go"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runReleaseManifestCLI([]string{"create", "--root", root, "--goos", "linux", "--goarch", "amd64"}, &output); err != nil {
		t.Fatalf("create error = %v", err)
	}
	var manifest releasepkg.Manifest
	if err := json.Unmarshal(output.Bytes(), &manifest); err != nil {
		t.Fatalf("decode create output: %v", err)
	}
	if manifest.GOOS != "linux" || manifest.GOARCH != "amd64" || manifest.FileCount != 1 || manifest.SchemaVersion != 4 || manifest.Coverage != nil {
		t.Fatalf("manifest = %#v", manifest)
	}

	output.Reset()
	if err := runReleaseManifestCLI([]string{"verify", "--root", root}, &output); err != nil {
		t.Fatalf("verify error = %v", err)
	}
	var report releasepkg.VerificationReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode verify output: %v", err)
	}
	if !report.Valid || report.VerifiedFiles != 1 {
		t.Fatalf("report = %#v", report)
	}

	for _, args := range [][]string{nil, {"unknown"}, {"create"}, {"verify"}} {
		if err := runReleaseManifestCLI(args, &output); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}
}
