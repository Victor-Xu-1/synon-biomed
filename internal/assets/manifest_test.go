package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyRejectsTamperedAssetAndAcceptsExactManifest(t *testing.T) {
	root := t.TempDir()
	assetPath := filepath.Join(root, "skills", "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o700); err != nil {
		t.Fatalf("make asset directory: %v", err)
	}
	content := []byte("# Example\n")
	if err := os.WriteFile(assetPath, content, 0o600); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	sum := sha256.Sum256(content)
	manifest := Manifest{SchemaVersion: 1, Files: []File{{Path: "skills/example/SKILL.md", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(content))}}}
	if report, err := Verify(root, manifest); err != nil || report.Checked != 1 || report.TotalBytes != int64(len(content)) {
		t.Fatalf("verify exact manifest = %#v, %v", report, err)
	}
	if err := os.WriteFile(assetPath, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper asset: %v", err)
	}
	if _, err := Verify(root, manifest); err == nil {
		t.Fatal("tampered asset passed verification")
	}
}

func TestVerifyRejectsManifestPathEscape(t *testing.T) {
	if _, err := Verify(t.TempDir(), Manifest{SchemaVersion: 1, Files: []File{{Path: "../outside", SHA256: "00", Bytes: 0}}}); err == nil {
		t.Fatal("path escape passed verification")
	}
}

func TestVerifyRequiresDeclaredLicenseFile(t *testing.T) {
	manifest := Manifest{SchemaVersion: 1, License: "BSD-3-Clause", LicenseFile: "LICENSE"}
	if _, err := Verify(t.TempDir(), manifest); err == nil {
		t.Fatal("undeclared asset license file passed verification")
	}
}
